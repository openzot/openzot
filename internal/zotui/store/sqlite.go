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
	now := unix(time.Now())
	_, err := s.db.ExecContext(ctx, s.d.rebind(`INSERT INTO workers
(id, name, repo, repository, environment, provider, model, mission, max_iterations, schedule_cron, schedule_tz, runtime_minutes, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), w.ID, w.Name, w.Repo, w.Repository,
		w.Environment, w.Provider, w.Model, w.Mission, w.MaxIterations, w.Schedule.Cron,
		w.Schedule.Timezone, w.Schedule.RuntimeMinutes, now, now)
	return w.ID, err
}

func (s *sqlStore) UpdateWorker(ctx context.Context, w Worker) error {
	res, err := s.db.ExecContext(ctx, s.d.rebind(`UPDATE workers SET
name = ?, repo = ?, repository = ?, environment = ?, provider = ?, model = ?, mission = ?, max_iterations = ?,
schedule_cron = ?, schedule_tz = ?, runtime_minutes = ?, updated_at = ? WHERE id = ?`),
		w.Name, w.Repo, w.Repository, w.Environment, w.Provider, w.Model, w.Mission, w.MaxIterations,
		w.Schedule.Cron, w.Schedule.Timezone, w.Schedule.RuntimeMinutes, unix(time.Now()), w.ID)
	return changed(res, err)
}

func (s *sqlStore) GetWorker(ctx context.Context, id string) (*Worker, error) {
	w, err := scanWorker(s.db.QueryRowContext(ctx, s.d.rebind(`SELECT id, name, repo, repository,
environment, provider, model, mission, max_iterations, schedule_cron, schedule_tz, runtime_minutes, created_at, updated_at
FROM workers WHERE id = ?`), id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &w, err
}

func (s *sqlStore) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, repo, repository, environment, provider, model, mission,
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
(id, worker_id, status, mission, provider, model, max_iterations, iteration, tool, action, exit_code, error,
created_at, updated_at, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.ID, r.WorkerID, string(r.Status), r.Mission, r.Provider, r.Model, r.MaxIterations, r.Iteration,
		r.Tool, r.Action, nullableInt(r.ExitCode), r.Error, now, now, nullableTime(r.StartedAt), nullableTime(r.FinishedAt))
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

// ActiveRuns returns every run that is not terminal, newest first. A process
// that starts up uses it to find runs a previous process left mid-flight.
func (s *sqlStore) ActiveRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, s.d.rebind(runSelect+` WHERE status IN (?, ?, ?) ORDER BY created_at DESC`),
		string(RunScheduled), string(RunRunning), string(RunPaused))
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

// AppendRunOutput writes one chunk. The cost is a single row insert regardless
// of how much the run has already emitted; once the run passes maxRunOutputBytes
// the chunks that fall out of the window are discarded.
func (s *sqlStore) AppendRunOutput(ctx context.Context, id string, output []byte) error {
	if len(output) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := s.txExec(tx, `UPDATE runs SET updated_at = ? WHERE id = ?`, unix(time.Now()), id)
	if err := changed(res, err); err != nil {
		return err
	}
	var seq, start int64
	err = tx.QueryRowContext(ctx, s.d.rebind(`SELECT seq + 1, byte_end FROM run_output
WHERE run_id = ? ORDER BY seq DESC LIMIT 1`), id).Scan(&seq, &start)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	end := start + int64(len(output))
	if _, err := s.txExec(tx, `INSERT INTO run_output (run_id, seq, byte_start, byte_end, data)
VALUES (?, ?, ?, ?, ?)`, id, seq, start, end, output); err != nil {
		return err
	}
	if trim := end - maxRunOutputBytes; trim > 0 {
		if _, err := s.txExec(tx, `DELETE FROM run_output WHERE run_id = ? AND byte_end <= ?`, id, trim); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RunOutput returns the run's output from offset onward. Start reports where the
// returned bytes actually begin: it is later than offset when the cap has already
// discarded them, which tells a reader its tail is no longer continuous.
func (s *sqlStore) RunOutput(ctx context.Context, id string, offset int64) (Output, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, s.d.rebind(`SELECT 1 FROM runs WHERE id = ?`), id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Output{}, ErrNotFound
		}
		return Output{}, err
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, s.d.rebind(`SELECT byte_start, byte_end, data FROM run_output
WHERE run_id = ? AND byte_end > ? ORDER BY seq`), id, offset)
	if err != nil {
		return Output{}, err
	}
	defer rows.Close()
	out := Output{Start: offset, Next: offset}
	first := true
	for rows.Next() {
		var start, end int64
		var data []byte
		if err := rows.Scan(&start, &end, &data); err != nil {
			return Output{}, err
		}
		if first {
			if start < offset {
				data, start = data[offset-start:], offset
			}
			out.Start, first = start, false
		}
		out.Data = append(out.Data, data...)
		out.Next = end
	}
	return out, rows.Err()
}

const runSelect = `SELECT id, worker_id, status, mission, provider, model, max_iterations, iteration, tool, action,
exit_code, error, created_at, updated_at, started_at, finished_at FROM runs`

func scanWorker(scan func(...any) error) (Worker, error) {
	var w Worker
	var created, updated int64
	err := scan(&w.ID, &w.Name, &w.Repo, &w.Repository, &w.Environment, &w.Provider, &w.Model, &w.Mission,
		&w.MaxIterations, &w.Schedule.Cron, &w.Schedule.Timezone, &w.Schedule.RuntimeMinutes, &created, &updated)
	w.CreatedAt, w.UpdatedAt = fromUnix(created), fromUnix(updated)
	return w, err
}

func scanRun(scan func(...any) error) (Run, error) {
	var r Run
	var status string
	var exit, started, finished sql.NullInt64
	var created, updated int64
	err := scan(&r.ID, &r.WorkerID, &status, &r.Mission, &r.Provider, &r.Model, &r.MaxIterations, &r.Iteration,
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
