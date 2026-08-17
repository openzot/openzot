// Package store persists scheduled jobs and their progress, so you can schedule a
// job, close the tool, and come back later to see where it got to. The command
// center is a view over this durable state, not a process you have to keep open.
//
// The interface is storage-agnostic; sqlite is the default driver and the door is
// open to others (postgres, ...). The migration mechanics and the dialect/rebind
// approach are adopted from crmkit's store.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned when a lookup matches no job.
var ErrNotFound = errors.New("store: not found")

// Status is where a job is in its lifecycle.
type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusRunning   Status = "running"
	StatusSettled   Status = "settled"   // zot recorded _success
	StatusFailed    Status = "failed"    // zot recorded _failure, or the run errored
	StatusCancelled Status = "cancelled" // cancelled from the command center
)

// Job is a scheduled unit of work and its tracked progress.
type Job struct {
	ID          string
	Repo        string
	Repository  string
	Task        string
	Environment string
	Model       string
	Status      Status
	ExitCode    *int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store persists jobs. Implementations must be safe for concurrent use.
type Store interface {
	Create(ctx context.Context, j Job) (string, error)
	Get(ctx context.Context, id string) (*Job, error)
	List(ctx context.Context) ([]Job, error)
	SetStatus(ctx context.Context, id string, status Status, exitCode *int) error
	Close() error
}

// Config selects and locates the store.
type Config struct {
	Driver string // sqlite (default), postgres, ...
	DSN    string // path or connection string
}

// Open returns a Store for the configured driver, applying any pending schema
// migrations. zotui is a local single-user tool, so it migrates on open;
// ApplyMigrations remains the single schema-writing entry point (from crmkit).
func Open(cfg Config) (Store, error) {
	switch cfg.Driver {
	case "", "sqlite":
		s, err := openSQLite(cfg.DSN)
		if err != nil {
			return nil, err
		}
		if _, err := s.ApplyMigrations(); err != nil {
			_ = s.Close()
			return nil, err
		}
		return s, nil
	default:
		return nil, errors.New("store: unsupported driver " + cfg.Driver)
	}
}

// --- dialect -----------------------------------------------------------------
//
// All SQL is written once with "?" placeholders; the dialect rebinds them for the
// target backend. SQLite is a no-op; the door is open to Postgres ($1, $2) later.

type dialect struct {
	dollarPlaceholders bool
}

var sqliteDialect = dialect{}

func (d dialect) rebind(q string) string {
	if !d.dollarPlaceholders {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

// The exec/query helpers route every statement through rebind.

func (s *sqlStore) exec(q string, args ...any) (sql.Result, error) {
	return s.db.Exec(s.d.rebind(q), args...)
}

func (s *sqlStore) query(q string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.d.rebind(q), args...)
}

func (s *sqlStore) queryRow(q string, args ...any) *sql.Row {
	return s.db.QueryRow(s.d.rebind(q), args...)
}

func (s *sqlStore) txExec(tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return tx.Exec(s.d.rebind(q), args...)
}

// --- small helpers -----------------------------------------------------------

func unix(t time.Time) int64     { return t.Unix() }
func fromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

// newID returns a sortable-enough, unique job id.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "job_" + hex.EncodeToString(b[:])
}
