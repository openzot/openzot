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

	"github.com/openzot/openzot/internal/provider"
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

	Model string `json:"model"`
	// Provider is the selected named connection; Driver is the resolved
	// implementation that handles its endpoint and protocol quirks.
	Provider string `json:"provider"`
	Driver   string `json:"driver"`

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

	// Images are what an attachment message showed the model. The record keeps
	// their shape and digest; the bytes live in a blob beside this log, because
	// a megabyte of base64 on one line would defeat both of the reasons this
	// format is JSON Lines.
	Images []provider.Image `json:"images,omitempty"`
}

// Activity is a tool call recorded in a log.
type Activity struct {
	Kind      string `json:"kind"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Result    any    `json:"result,omitempty"`
	Failure   string `json:"failure,omitempty"`

	// The turn's reasoning state, in whichever form the transport carries it.
	// Without these a resumed run replays its calls stripped of the thinking
	// that produced them - which a reasoning model's provider may reject
	// outright, and at best degrades the model's continuity.
	ReasoningItems   []provider.ReasoningItem `json:"reasoning_items,omitempty"`
	ReasoningDetails json.RawMessage          `json:"reasoning_details,omitempty"`
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

	// Error is the underlying failure on an error ending - the provider's own
	// words, not the loop's summary of them. "the provider failed" answers
	// nothing at three in the morning; the 404 naming the wrong model does.
	Error string `json:"error,omitempty"`

	// Failure is the wire evidence behind Error, when the failure was a
	// provider response. The raw exchange is what troubleshooting needs: an
	// opaque upstream "ERROR" and a proper context-length message read the
	// same in Error, but the refused request's size tells them apart.
	Failure *Failure `json:"failure,omitempty"`

	Code int `json:"code"`

	Iterations    int `json:"iterations"`
	Calls         int `json:"calls"`
	Continuations int `json:"continuations"`
	Cycles        int `json:"cycles"`
	Settles       int `json:"settles"`

	// InputTokens and OutputTokens are the provider-billed totals for the run,
	// persisted so an audit or a resumed run can see cost rather than only the
	// terminal that produced it. Omitted from an older log, which read back as
	// zero - not wrong, just unrecorded.
	InputTokens  int `json:"inputTokens,omitempty"`
	OutputTokens int `json:"outputTokens,omitempty"`
}

// Failure is the wire evidence of a provider refusal.
type Failure struct {
	Status       int    `json:"status"`
	ResponseBody string `json:"response_body,omitempty"`
	RequestBytes int    `json:"request_bytes,omitempty"`
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

	// blobs is the directory image bytes are written to, a sibling of the log
	// named after it. Created on first use: a run that never attaches an image
	// leaves no empty directory behind.
	blobs string
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

	writer := &Writer{
		file:  file,
		enc:   json.NewEncoder(file),
		id:    id,
		path:  path,
		blobs: filepath.Join(dir, id+BlobSuffix),
	}

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

// BlobSuffix names the directory holding a session's image blobs. A sibling of
// the log rather than a shared store, so a session stays one self-contained
// thing: copying or caching a log carries its images, and deleting it is two
// paths rather than a reference count.
const BlobSuffix = ".blobs"

// Blobs returns the directory this writer keeps image bytes in.
func (w *Writer) Blobs() string { return w.blobs }

// StoreImage writes an image's bytes beside the log and returns the record to
// serialise in its place.
//
// Content-addressed: the same screenshot attached twice is stored once, and a
// record can never name bytes that are not the ones it describes. The returned
// image carries no Bytes - what goes in the log is the shape, not the payload.
func (w *Writer) StoreImage(image provider.Image) (provider.Image, error) {
	if len(image.Bytes) == 0 {
		return image, nil // already stored, or nothing to store
	}

	if w.blobs == "" {
		// no directory to write to: keep it inline rather than lose it
		image.Source = provider.SourceInline
		image.Data = image.Encoded()
		image.Bytes = nil

		return image, nil
	}

	if err := os.MkdirAll(w.blobs, 0o700); err != nil {
		return image, fmt.Errorf("create blob directory: %w", err)
	}

	path := filepath.Join(w.blobs, blobName(image))

	// the digest is the name, so an existing file is already these bytes
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, image.Bytes, 0o600); err != nil {
			return image, fmt.Errorf("write image blob: %w", err)
		}
	}

	image.Source = provider.SourceBlob
	image.Bytes = nil
	image.Data = ""

	return image, nil
}

// blobName is an image's file name: its digest, with the extension for its type
// so the directory can be browsed by a human.
func blobName(image provider.Image) string {
	digest := image.Digest

	if index := strings.Index(digest, ":"); index >= 0 {
		digest = digest[index+1:]
	}

	if digest == "" {
		digest = provider.Digest(image.Bytes)[len("sha256:"):]
	}

	return digest + "." + image.Extension()
}

// Session is a log read back from disk.
type Session struct {
	Meta     Meta
	Messages []Message
	Events   []Event
	Result   *Result

	// Blobs is where this log's image bytes live, empty for a session parsed
	// from a reader with no path behind it. An image whose bytes cannot be
	// found is not sent - the text that described it is what remains.
	Blobs string

	// Truncated reports that the log ended mid-record, which is what a crashed
	// or killed run leaves behind. The records before it are still usable.
	Truncated bool

	// Discarded holds the conversations a reset threw away, oldest first: each
	// is what the history looked like before the engine compacted it. Not part
	// of the run's state - a resume ignores them - but they are the turns that
	// actually happened, which is what an export for analysis or training wants.
	Discarded [][]Message

	// Started and Ended are the timestamps of the first and last records, zero
	// for a session parsed from nothing.
	Started time.Time
	Ended   time.Time
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

	session, err := Read(file)

	if session != nil {
		session.Blobs = strings.TrimSuffix(path, ".jsonl") + BlobSuffix
	}

	return session, err
}

// LoadImage restores an image's bytes from wherever the record says they are.
//
// A missing blob is not an error: logs are copied, pruned and cached, and a run
// that refused to resume because a screenshot from an hour ago had been deleted
// would be worse than one that resumes with the description of it. The image is
// returned unready, and the wire drops it.
func (s *Session) LoadImage(image provider.Image) provider.Image {
	if len(image.Bytes) > 0 || image.Data != "" {
		return image
	}

	if s == nil || s.Blobs == "" {
		return image
	}

	data, err := os.ReadFile(filepath.Join(s.Blobs, blobName(image)))

	if err != nil {
		return image
	}

	// the name is the digest, so a file that does not hash to it is not this
	// image - a truncated copy, or a directory someone edited
	if image.Digest != "" && provider.Digest(data) != image.Digest {
		return image
	}

	image.Bytes = data

	return image
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

		if !record.At.IsZero() {
			if session.Started.IsZero() {
				session.Started = record.At
			}

			session.Ended = record.At
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
			if len(session.Messages) > 0 {
				session.Discarded = append(session.Discarded, session.Messages)
			}

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

	// ResumedFrom names the session this one continues, empty if it started
	// clean. Listed rather than left inside the log so a caller can walk a
	// chain of resumes without loading every session in the directory.
	ResumedFrom string
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
			ID:          strings.TrimSuffix(file.Name(), ".jsonl"),
			Path:        path,
			Task:        session.Meta.Task,
			Complete:    session.Complete(),
			ResumedFrom: session.Meta.ResumedFrom,
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
