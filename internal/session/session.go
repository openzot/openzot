// Package session records a run to disk so it can be inspected afterwards and
// picked up again.
//
// An autonomous run is unattended by definition: nobody watched it, and by the
// time anyone looks the terminal is gone. A session log is what turns "it
// failed overnight" into something answerable - what it tried, what the tools
// returned, and where it stopped.
//
// It is also what makes a run resumable. The conversation is the entire state
// of an agent, so writing it down and reading it back is enough to continue: no
// server-side session, no snapshot format, nothing to keep in sync.
//
// The format is JSON Lines. One record per line, appended as the run goes, so a
// log is readable while the run is still going and a crashed run still leaves
// everything up to the crash. A truncated final line loses one record rather
// than the file.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind identifies a record.
type Kind string

const (
	// KindMeta opens a log: the task, model and settings the run started with.
	// Exactly one, first.
	KindMeta Kind = "meta"

	// KindMessage is one conversation message. Replaying these in order
	// reconstructs the agent's state.
	KindMessage Kind = "message"

	// KindEvent is something that happened - a tool call, a retry, a nudge.
	// Not needed to resume; kept because it is what explains a run afterwards.
	KindEvent Kind = "event"

	// KindResult closes a log with the outcome. Absent means the run did not
	// finish, which is itself worth knowing.
	KindResult Kind = "result"

	// KindReset discards the messages recorded before it. The engine compacts
	// its own history, and a log that kept both the pre- and post-compaction
	// turns would resume into a conversation that never happened. Events are
	// kept: they are the narrative of the run, not its state.
	KindReset Kind = "reset"
)

// Record is one line of a session log.
type Record struct {
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`

	// Meta is set on a KindMeta record.
	Meta *Meta `json:"meta,omitempty"`

	// Message is set on a KindMessage record.
	Message *Message `json:"message,omitempty"`

	// Event is set on a KindEvent record.
	Event *Event `json:"event,omitempty"`

	// Result is set on a KindResult record.
	Result *Result `json:"result,omitempty"`
}

// Meta describes how a run started.
type Meta struct {
	// ID identifies the session, and names its file.
	ID string `json:"id"`

	// Task is the brief the run was given.
	Task string `json:"task"`

	Model    string `json:"model"`
	Provider string `json:"provider"`
	Backend  string `json:"backend"`

	// Workdir is where the agent's tools operated.
	Workdir string `json:"workdir"`

	// ResumedFrom names the session this one continues, if any.
	ResumedFrom string `json:"resumedFrom,omitempty"`
}

// Message is one conversation entry.
//
// The activity is stored as its own object rather than a free-form bag, so a
// log written today still reads back into a run tomorrow: a typed field either
// decodes or does not, where a map quietly loses whatever it did not expect.
type Message struct {
	Type     string    `json:"type"`
	Text     string    `json:"text"`
	Activity *Activity `json:"activity,omitempty"`
}

// Activity is a tool call recorded in a log.
type Activity struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Result    any    `json:"result,omitempty"`
	Failure   string `json:"failure,omitempty"`
}

// Event is something that happened during the run.
type Event struct {
	Kind      string `json:"kind"`
	Tool      string `json:"tool,omitempty"`
	Text      string `json:"text,omitempty"`
	Iteration int    `json:"iteration,omitempty"`
}

// Result is how a run ended.
type Result struct {
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`

	Iterations    int `json:"iterations"`
	Calls         int `json:"calls"`
	Continuations int `json:"continuations"`
	Cycles        int `json:"cycles"`
	Settles       int `json:"settles"`
}

// Writer appends records to a session log.
//
// Safe for concurrent use: the engine emits events from its own goroutine while
// the caller may be recording messages.
type Writer struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
	id   string
	path string
}

// NewID returns a session identifier that sorts chronologically.
//
// Time-ordered rather than random so `ls` shows runs in the order they happened,
// which is how anyone actually looks for one.
func NewID(now time.Time) string {
	return now.UTC().Format("20060102-150405")
}

// Start opens a log for a run beginning at now, choosing an unused id.
//
// Two runs started in the same second collide on the time-derived id, and one
// of them would otherwise lose its log entirely. A suffix is added rather than
// finer timestamps because precision alone cannot guarantee uniqueness across
// processes - only claiming the file can.
func Start(dir string, now time.Time, meta Meta) (*Writer, error) {
	base := NewID(now)

	for attempt := 0; ; attempt++ {
		id := base

		if attempt > 0 {
			id = fmt.Sprintf("%s-%d", base, attempt+1)
		}

		writer, err := Create(dir, id, meta)
		if err == nil {
			return writer, nil
		}

		// only a taken id is worth retrying; a bad directory will never resolve
		if !errors.Is(err, os.ErrExist) || attempt >= 99 {
			return nil, err
		}
	}
}

// Create opens a new session log under dir.
//
// The directory is created if needed. A session whose id already exists is an
// error rather than an append: two runs sharing a log would interleave into
// something that resumes as neither.
func Create(dir, id string, meta Meta) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	path := filepath.Join(dir, id+".jsonl")

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create session log: %w", err)
	}

	writer := &Writer{file: file, enc: json.NewEncoder(file), id: id, path: path}

	meta.ID = id

	if err := writer.write(Record{Kind: KindMeta, At: time.Now().UTC(), Meta: &meta}); err != nil {
		file.Close()

		return nil, err
	}

	return writer, nil
}

// ID returns the session identifier.
func (w *Writer) ID() string { return w.id }

// Path returns the log file.
func (w *Writer) Path() string { return w.path }

// write appends one record and flushes it.
//
// Flushed per record on purpose: the log has to be readable while the run is in
// flight, and a crashed run has to leave everything up to the crash. Buffering
// would lose exactly the tail that explains a failure.
func (w *Writer) write(record Record) error {
	w.mu.Lock()

	defer w.mu.Unlock()

	if w.file == nil {
		return errors.New("session: log is closed")
	}

	if err := w.enc.Encode(record); err != nil {
		return err
	}

	return w.file.Sync()
}

// Message records a conversation entry.
func (w *Writer) Message(message Message) error {
	return w.write(Record{Kind: KindMessage, At: time.Now().UTC(), Message: &message})
}

// Reset discards the messages recorded so far.
func (w *Writer) Reset() error {
	return w.write(Record{Kind: KindReset, At: time.Now().UTC()})
}

// Event records something that happened.
func (w *Writer) Event(event Event) error {
	return w.write(Record{Kind: KindEvent, At: time.Now().UTC(), Event: &event})
}

// Result records the outcome and closes the log.
func (w *Writer) Result(result Result) error {
	if err := w.write(Record{Kind: KindResult, At: time.Now().UTC(), Result: &result}); err != nil {
		return err
	}

	return w.Close()
}

// Close releases the file. Safe to call twice.
func (w *Writer) Close() error {
	w.mu.Lock()

	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	err := w.file.Close()

	w.file = nil

	return err
}

// Session is a log read back from disk.
type Session struct {
	Meta     Meta
	Messages []Message
	Events   []Event
	Result   *Result

	// Truncated reports that the log ended mid-record, which is what a crashed
	// or killed run leaves behind. The records before it are still usable.
	Truncated bool
}

// Complete reports whether the run recorded an outcome.
func (s *Session) Complete() bool { return s.Result != nil }

// Load reads a session log.
//
// A malformed line is tolerated rather than fatal: a log is written by a process
// that may be killed at any moment, and refusing to read the ninety-nine good
// records because the hundredth is half-written would defeat the purpose.
func Load(path string) (*Session, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer file.Close()

	return Read(file)
}

// Read parses a session log from a reader.
func Read(r io.Reader) (*Session, error) {
	session := &Session{}

	scanner := bufio.NewScanner(r)

	// a tool result can be large, and it is one line
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "" {
			continue
		}

		var record Record

		if err := json.Unmarshal([]byte(line), &record); err != nil {
			// a half-written final line is the normal shape of a crash
			session.Truncated = true

			continue
		}

		switch record.Kind {
		case KindMeta:
			if record.Meta != nil {
				session.Meta = *record.Meta
			}

		case KindMessage:
			if record.Message != nil {
				session.Messages = append(session.Messages, *record.Message)
			}

		case KindEvent:
			if record.Event != nil {
				session.Events = append(session.Events, *record.Event)
			}

		case KindReset:
			session.Messages = nil

		case KindResult:
			if record.Result != nil {
				session.Result = record.Result
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return session, err
	}

	return session, nil
}

// Entry is a session listed in a directory.
type Entry struct {
	ID      string
	Path    string
	Task    string
	Started time.Time

	// Complete reports whether the run recorded an outcome.
	Complete bool

	// Reason is the recorded stop reason, empty for an unfinished run.
	Reason string
}

// List enumerates the sessions in dir, newest first.
//
// Reads each log rather than just stat-ing it, because the useful columns - the
// task, whether it finished - live inside. Sessions are small and there are not
// many; a listing that only showed filenames would send the user to `cat`
// anyway.
func List(dir string) ([]Entry, error) {
	files, err := os.ReadDir(dir)

	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, err
	}

	var entries []Entry

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") {
			continue
		}

		path := filepath.Join(dir, file.Name())

		session, err := Load(path)
		if err != nil {
			continue
		}

		entry := Entry{
			ID:       strings.TrimSuffix(file.Name(), ".jsonl"),
			Path:     path,
			Task:     session.Meta.Task,
			Complete: session.Complete(),
		}

		if info, err := file.Info(); err == nil {
			entry.Started = info.ModTime()
		}

		if session.Result != nil {
			entry.Reason = session.Result.Reason
		}

		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID > entries[j].ID
	})

	return entries, nil
}

// Resolve turns a session reference into a path.
//
// A reference is an id, a filename, or a path. "last" picks the most recent,
// which is what someone reaching for a resume almost always means.
func Resolve(dir, reference string) (string, error) {
	reference = strings.TrimSpace(reference)

	if reference == "" {
		return "", errors.New("session: no session given")
	}

	if reference == "last" {
		entries, err := List(dir)
		if err != nil {
			return "", err
		}

		if len(entries) == 0 {
			return "", fmt.Errorf("session: no sessions in %s", dir)
		}

		return entries[0].Path, nil
	}

	// an explicit path wins, so a log from anywhere can be replayed
	if strings.ContainsRune(reference, os.PathSeparator) {
		if _, err := os.Stat(reference); err == nil {
			return reference, nil
		}
	}

	candidate := filepath.Join(dir, strings.TrimSuffix(reference, ".jsonl")+".jsonl")

	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("session: %q not found in %s", reference, dir)
	}

	return candidate, nil
}
