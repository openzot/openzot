package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver (cgo-free), registered as "sqlite"
)

// sqlStore is the database/sql implementation of Store. The query code is shared;
// the dialect handles per-backend differences (see store.go).
type sqlStore struct {
	db *sql.DB
	d  dialect
}

// openSQLite opens (creating the file and its directory if needed) the sqlite
// database at path. ":memory:" yields an ephemeral database. It does not touch
// the schema - that is ApplyMigrations' job (see migrate.go).
func openSQLite(path string) (*sqlStore, error) {
	if path == "" {
		return nil, errors.New("store/sqlite: empty dsn")
	}
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("create db directory: %w", err)
			}
		}
	}

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	// sqlite serialises writers; a single connection avoids "database is locked".
	db.SetMaxOpenConns(1)

	return &sqlStore{db: db, d: sqliteDialect}, nil
}

// Close releases the database handle.
func (s *sqlStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Create records a newly scheduled job and returns its id.
func (s *sqlStore) Create(ctx context.Context, j Job) (string, error) {
	id := j.ID
	if id == "" {
		id = newID()
	}
	if j.Status == "" {
		j.Status = StatusScheduled
	}
	now := unix(time.Now())

	_, err := s.exec(`
INSERT INTO jobs (id, source, repository, task, environment, model, status, exit_code, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, j.Source, j.Repository, j.Task, j.Environment, j.Model, string(j.Status), nil, now, now)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Get returns a job by id.
func (s *sqlStore) Get(ctx context.Context, id string) (*Job, error) {
	row := s.queryRow(`
SELECT id, source, repository, task, environment, model, status, exit_code, created_at, updated_at
FROM jobs WHERE id = ?`, id)

	j, err := scanJob(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// List returns jobs, newest first.
func (s *sqlStore) List(ctx context.Context) ([]Job, error) {
	rows, err := s.query(`
SELECT id, source, repository, task, environment, model, status, exit_code, created_at, updated_at
FROM jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetStatus updates a job's status, and its exit code once it settles.
func (s *sqlStore) SetStatus(ctx context.Context, id string, status Status, exitCode *int) error {
	var ec any
	if exitCode != nil {
		ec = *exitCode
	}
	res, err := s.exec(`UPDATE jobs SET status = ?, exit_code = ?, updated_at = ? WHERE id = ?`,
		string(status), ec, unix(time.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// scanJob reads one job row via a Scan function (works for *sql.Row and *sql.Rows).
func scanJob(scan func(dest ...any) error) (Job, error) {
	var (
		j                Job
		status           string
		exit             sql.NullInt64
		created, updated int64
	)
	if err := scan(&j.ID, &j.Source, &j.Repository, &j.Task, &j.Environment, &j.Model, &status, &exit, &created, &updated); err != nil {
		return Job{}, err
	}
	j.Status = Status(status)
	if exit.Valid {
		v := int(exit.Int64)
		j.ExitCode = &v
	}
	j.CreatedAt = fromUnix(created)
	j.UpdatedAt = fromUnix(updated)
	return j, nil
}

var _ Store = (*sqlStore)(nil)
