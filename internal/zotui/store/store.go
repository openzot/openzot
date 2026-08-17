// Package store persists workers, their runs, and run output. The web command
// center is a view over this durable state, not the owner of it.
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

// ErrNotFound is returned when a lookup matches no record.
var ErrNotFound = errors.New("store: not found")

// RunStatus is where one execution of a worker is in its lifecycle.
type RunStatus string

const (
	RunScheduled RunStatus = "scheduled"
	RunRunning   RunStatus = "running"
	RunPaused    RunStatus = "paused"
	RunStopped   RunStatus = "stopped"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// Terminal reports whether a run can no longer be controlled.
func (s RunStatus) Terminal() bool {
	return s == RunStopped || s == RunSucceeded || s == RunFailed
}

// Schedule describes recurring execution. An empty Cron disables scheduling.
type Schedule struct {
	Cron           string `json:"cron"`
	Timezone       string `json:"timezone"`
	RuntimeMinutes int    `json:"runtimeMinutes"`
}

// Worker is a reusable binding between a repository, environment, model, and
// mission. Starting it creates a new Run without changing this definition.
type Worker struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Repo          string    `json:"repo"`
	Repository    string    `json:"repository"`
	Environment   string    `json:"environment"`
	Model         string    `json:"model"`
	Mission       string    `json:"mission"`
	MaxIterations int       `json:"maxIterations"`
	Schedule      Schedule  `json:"schedule"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Run is one durable execution of a worker.
type Run struct {
	ID            string     `json:"id"`
	WorkerID      string     `json:"workerId"`
	Status        RunStatus  `json:"status"`
	Mission       string     `json:"mission"`
	Model         string     `json:"model"`
	MaxIterations int        `json:"maxIterations"`
	Iteration     int        `json:"iteration"`
	Tool          string     `json:"tool"`
	Action        string     `json:"action"`
	ExitCode      *int       `json:"exitCode,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

// Store persists the command center domain. Implementations must be safe for
// concurrent use.
type Store interface {
	CreateWorker(context.Context, Worker) (string, error)
	UpdateWorker(context.Context, Worker) error
	GetWorker(context.Context, string) (*Worker, error)
	ListWorkers(context.Context) ([]Worker, error)
	DeleteWorker(context.Context, string) error

	CreateRun(context.Context, Run) (string, error)
	GetRun(context.Context, string) (*Run, error)
	ListRuns(context.Context, string) ([]Run, error)
	SetRunStatus(context.Context, string, RunStatus, *int, string) error
	UpdateRunProgress(context.Context, string, int, string, string) error
	AppendRunOutput(context.Context, string, []byte) error
	RunOutput(context.Context, string) (string, error)
	Close() error
}

// Config selects and locates the store.
type Config struct {
	Driver string
	DSN    string
}

// Open returns a migrated Store for the configured driver.
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

type dialect struct{ dollarPlaceholders bool }

var sqliteDialect = dialect{}

func (d dialect) rebind(q string) string {
	if !d.dollarPlaceholders {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := range len(q) {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

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

func unix(t time.Time) int64     { return t.UnixMilli() }
func fromUnix(v int64) time.Time { return time.UnixMilli(v).UTC() }

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
