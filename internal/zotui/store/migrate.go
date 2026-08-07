package store

import (
	"fmt"
	"sort"
	"time"
)

// Migration is one ordered schema change: a set of statements applied atomically
// and recorded so it runs exactly once. Version must be unique and ascending.
// Statements run one at a time (some drivers reject multi-statement Exec).
//
// The mechanics here are adopted from crmkit's store: version 1 is the baseline
// (the full current schema); later versions are deltas appended over time. This
// is the only place that defines schema, and ApplyMigrations is the only code
// that writes it.
type Migration struct {
	Version    int
	Name       string
	Statements []string
}

// baselineSchema is migration 1: the jobs table and its indexes, as individual
// statements. BIGINT holds unix epochs; it has INTEGER affinity in sqlite and a
// 64-bit integer in postgres, so the same DDL is portable.
var baselineSchema = []string{
	`CREATE TABLE IF NOT EXISTS jobs (
	id          TEXT PRIMARY KEY,
	source      TEXT NOT NULL,
	repository  TEXT NOT NULL,
	task        TEXT NOT NULL,
	environment TEXT NOT NULL,
	model       TEXT NOT NULL,
	status      TEXT NOT NULL,
	exit_code   BIGINT,
	created_at  BIGINT NOT NULL,
	updated_at  BIGINT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status)`,
}

// migrations is the ordered, authoritative schema history.
var migrations = []Migration{
	{Version: 1, Name: "initial schema", Statements: baselineSchema},
}

// MigrationState reports how the database's schema compares to the code.
type MigrationState struct {
	Applied []int       // versions recorded as applied, ascending
	Pending []Migration // registered migrations not yet applied, ascending
	Current int         // highest applied version (0 = none / empty database)
	Latest  int         // highest available version
}

func orderedMigrations() []Migration {
	out := make([]Migration, len(migrations))
	copy(out, migrations)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// MigrationStatus reads which migrations have been applied and computes what is
// pending. It is strictly read-only - it never creates the bookkeeping table - so
// an absent schema_migrations table (a fresh database) is reported as zero
// applied.
func (s *sqlStore) MigrationStatus() (MigrationState, error) {
	applied := map[int]bool{}
	var st MigrationState

	rows, err := s.query(`SELECT version FROM schema_migrations ORDER BY version`)
	if rows != nil {
		defer rows.Close()
	}
	if err == nil {
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				return MigrationState{}, err
			}
			applied[v] = true
			st.Applied = append(st.Applied, v)
			if v > st.Current {
				st.Current = v
			}
		}
		if err := rows.Err(); err != nil {
			return MigrationState{}, err
		}
	}
	// An error above means the table is absent (fresh DB); treat as zero applied.

	for _, m := range orderedMigrations() {
		if m.Version > st.Latest {
			st.Latest = m.Version
		}
		if !applied[m.Version] {
			st.Pending = append(st.Pending, m)
		}
	}
	return st, nil
}

// ApplyMigrations applies every pending migration in version order, each in its
// own transaction, recording it in schema_migrations. This is the single
// schema-writing entry point. It is a no-op when the schema is already current.
func (s *sqlStore) ApplyMigrations() ([]Migration, error) {
	if _, err := s.exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
	version    BIGINT PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at BIGINT NOT NULL
)`); err != nil {
		return nil, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	state, err := s.MigrationStatus()
	if err != nil {
		return nil, err
	}

	var done []Migration
	for _, m := range state.Pending {
		tx, err := s.db.Begin()
		if err != nil {
			return done, err
		}
		for _, stmt := range m.Statements {
			if _, err := s.txExec(tx, stmt); err != nil {
				_ = tx.Rollback()
				return done, fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
			}
		}
		if _, err := s.txExec(tx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.Version, m.Name, time.Now().Unix()); err != nil {
			_ = tx.Rollback()
			return done, err
		}
		if err := tx.Commit(); err != nil {
			return done, err
		}
		done = append(done, m)
	}
	return done, nil
}
