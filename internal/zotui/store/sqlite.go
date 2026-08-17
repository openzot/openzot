package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type sqlStore struct {
	db *sql.DB
	d  dialect
}

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
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	return &sqlStore{db: db, d: sqliteDialect}, nil
}

func (s *sqlStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *sqlStore) CreateWorker(ctx context.Context, w Worker) (string, error) {
	if w.ID == "" {
		w.ID = newID("worker")
	}
	if w.MaxIterations <= 0 {
		w.MaxIterations = 20
	}
	now := unix(time.Now())
	_, err := s.db.ExecContext(ctx, s.d.rebind(`INSERT INTO workers
(id, name, repo, repository, environment, model, mission, max_iterations, schedule_cron, schedule_tz, runtime_minutes, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), w.ID, w.Name, w.Repo, w.Repository,
		w.Environment, w.Model, w.Mission, w.MaxIterations, w.Schedule.Cron,
		w.Schedule.Timezone, w.Schedule.RuntimeMinutes, now, now)
	return w.ID, err
}

func (s *sqlStore) UpdateWorker(ctx context.Context, w Worker) error {
	res, err := s.db.ExecContext(ctx, s.d.rebind(`UPDATE workers SET
name = ?, repo = ?, repository = ?, environment = ?, model = ?, mission = ?, max_iterations = ?,
schedule_cron = ?, schedule_tz = ?, runtime_minutes = ?, updated_at = ? WHERE id = ?`),
		w.Name, w.Repo, w.Repository, w.Environment, w.Model, w.Mission, w.MaxIterations,
		w.Schedule.Cron, w.Schedule.Timezone, w.Schedule.RuntimeMinutes, unix(time.Now()), w.ID)
	return changed(res, err)
}

func (s *sqlStore) GetWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, s.d.rebind(`SELECT id, name, repo, repository,
environment, model, mission, max_iterations, schedule_cron, schedule_tz, runtime_minutes, created_at, updated_at
FROM workers WHERE id = ?`), id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &w, err
}

func (s *sqlStore) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, repo, repository, environment, model, mission,
max_iterations, schedule_cron, schedule_tz, runtime_minutes, created_at, updated_at
FROM workers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Worker, 0)
	for rows.Next() {
		w, err := scanWorker(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *sqlStore) DeleteWorker(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, s.d.rebind(`DELETE FROM workers WHERE id = ?`), id)
	return changed(res, err)
}

func (s *sqlStore) CreateRun(ctx context.Context, r Run) (string, error) {
	if r.ID == "" {
		r.ID = newID("run")
	}
	if r.Status == "" {
		r.Status = RunScheduled
	}
	now := unix(time.Now())
	_, err := s.db.ExecContext(ctx, s.d.rebind(`INSERT INTO runs
(id, worker_id, status, mission, model, max_iterations, iteration, tool, action, exit_code, error, output,
created_at, updated_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.ID, r.WorkerID, string(r.Status), r.Mission, r.Model, r.MaxIterations, r.Iteration,
		r.Tool, r.Action, nullableInt(r.ExitCode), r.Error, "", now, now, nullableTime(r.StartedAt), nullableTime(r.FinishedAt))
	return r.ID, err
}

func (s *sqlStore) GetRun(ctx context.Context, id string) (*Run, error) {
	r, err := scanRun(s.db.QueryRowContext(ctx, s.d.rebind(runSelect+` WHERE id = ?`), id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}

func (s *sqlStore) ListRuns(ctx context.Context, workerID string) ([]Run, error) {
	q, args := runSelect+` WHERE worker_id = ? ORDER BY created_at DESC`, []any{workerID}
	if workerID == "" {
		q, args = runSelect+` ORDER BY created_at DESC`, nil
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0)
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sqlStore) SetRunStatus(ctx context.Context, id string, status RunStatus, exitCode *int, reason string) error {
	now := unix(time.Now())
	var started, finished any
	if status == RunRunning {
		started = now
	}
	if status.Terminal() {
		finished = now
	}
	res, err := s.db.ExecContext(ctx, s.d.rebind(`UPDATE runs SET status = ?, exit_code = ?, error = ?,
started_at = COALESCE(started_at, ?), finished_at = COALESCE(finished_at, ?), updated_at = ? WHERE id = ?`),
		string(status), nullableInt(exitCode), reason, started, finished, now, id)
	return changed(res, err)
}

func (s *sqlStore) UpdateRunProgress(ctx context.Context, id string, iteration int, tool, action string) error {
	res, err := s.db.ExecContext(ctx, s.d.rebind(`UPDATE runs SET iteration = ?, tool = ?, action = ?, updated_at = ? WHERE id = ?`),
		iteration, tool, action, unix(time.Now()), id)
	return changed(res, err)
}

func (s *sqlStore) AppendRunOutput(ctx context.Context, id string, output []byte) error {
	res, err := s.db.ExecContext(ctx, s.d.rebind(`UPDATE runs SET output = output || ?, updated_at = ? WHERE id = ?`),
		string(output), unix(time.Now()), id)
	return changed(res, err)
}

func (s *sqlStore) RunOutput(ctx context.Context, id string) (string, error) {
	var output string
	err := s.db.QueryRowContext(ctx, s.d.rebind(`SELECT output FROM runs WHERE id = ?`), id).Scan(&output)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return output, err
}

const runSelect = `SELECT id, worker_id, status, mission, model, max_iterations, iteration, tool, action,
exit_code, error, created_at, updated_at, started_at, finished_at FROM runs`

func scanWorker(scan func(...any) error) (Worker, error) {
	var w Worker
	var created, updated int64
	err := scan(&w.ID, &w.Name, &w.Repo, &w.Repository, &w.Environment, &w.Model, &w.Mission,
		&w.MaxIterations, &w.Schedule.Cron, &w.Schedule.Timezone, &w.Schedule.RuntimeMinutes, &created, &updated)
	w.CreatedAt, w.UpdatedAt = fromUnix(created), fromUnix(updated)
	return w, err
}

func scanRun(scan func(...any) error) (Run, error) {
	var r Run
	var status string
	var exit, started, finished sql.NullInt64
	var created, updated int64
	err := scan(&r.ID, &r.WorkerID, &status, &r.Mission, &r.Model, &r.MaxIterations, &r.Iteration,
		&r.Tool, &r.Action, &exit, &r.Error, &created, &updated, &started, &finished)
	r.Status = RunStatus(status)
	if exit.Valid {
		v := int(exit.Int64)
		r.ExitCode = &v
	}
	r.CreatedAt, r.UpdatedAt = fromUnix(created), fromUnix(updated)
	r.StartedAt, r.FinishedAt = scanTime(started), scanTime(finished)
	return r, err
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return unix(*v)
}

func scanTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromUnix(v.Int64)
	return &t
}

func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

var _ Store = (*sqlStore)(nil)
