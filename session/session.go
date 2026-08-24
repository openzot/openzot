// Package session is the public face of openzot's session recording, the
// durable log a run leaves behind so it can be inspected afterwards and resumed.
//
// It re-exports the types from the internal session package, so an embedding
// application like rook can record and resume runs without reaching into
// openzot's internals - the same pattern the agent and tui packages already
// follow.
package session

import (
	"time"

	"github.com/openzot/openzot/internal/session"
)

// Writer is the public alias for the internal session writer, which appends
// records to a JSON Lines log.
type Writer = session.Writer

// Session is a log read back from disk: the meta, messages, events, and result
// of a run, plus any conversations a compaction discarded.
type Session = session.Session

// Meta describes how a run started: the task, model, provider, and workdir.
type Meta = session.Meta

// Entry is one session listed in a directory.
type Entry = session.Entry

// Start opens a log for a run beginning at now, choosing an unused id.
func Start(dir string, now time.Time, meta Meta) (*Writer, error) {
	return session.Start(dir, now, meta)
}

// Load reads a session log.
func Load(path string) (*Session, error) {
	return session.Load(path)
}

// List enumerates the sessions in dir, newest first.
func List(dir string) ([]Entry, error) {
	return session.List(dir)
}

// Resolve turns a session reference into a path.
func Resolve(dir, reference string) (string, error) {
	return session.Resolve(dir, reference)
}

// NewRecorder wraps a writer as an agent.Recorder, so the engine can drive the
// log from its own event stream.
func NewRecorder(writer *Writer) *session.Recorder {
	return session.NewRecorder(writer)
}
