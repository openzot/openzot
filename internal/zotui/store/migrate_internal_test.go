package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Adding provider selection must preserve the ignored development database and
// its existing workers/runs; old rows resolve through their environment default.
func TestProviderMigrationPreservesExistingRows(t *testing.T) {
	s, err := openSQLite(filepath.Join(t.TempDir(), "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, statement := range baselineSchema {
		if _, err := s.exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.exec(`CREATE TABLE schema_migrations (
		version BIGINT PRIMARY KEY, name TEXT NOT NULL, applied_at BIGINT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(`INSERT INTO schema_migrations VALUES (2, 'workers and runs', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(`INSERT INTO workers VALUES
		('worker_old', 'builder', 'repo', 'owner/project', 'go', 'glm', 'ship', 10, '', '', 0, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(`INSERT INTO runs VALUES
		('run_old', 'worker_old', 'succeeded', 'ship', 'glm', 10, 1, '', '', 0, '', '', 1, 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplyMigrations(); err != nil {
		t.Fatalf("apply provider migration: %v", err)
	}
	worker, err := s.GetWorker(context.Background(), "worker_old")
	if err != nil || worker.Provider != "" || worker.Model != "glm" {
		t.Fatalf("migrated worker = %+v, %v", worker, err)
	}
	run, err := s.GetRun(context.Background(), "run_old")
	if err != nil || run.Provider != "" || run.Model != "glm" {
		t.Fatalf("migrated run = %+v, %v", run, err)
	}
}
