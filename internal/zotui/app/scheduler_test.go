package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/store"
)

func TestCronScheduleValidationAndMatching(t *testing.T) {
	valid := store.Schedule{Cron: "0 8-20/4 * * 1-5", Timezone: "Europe/London", RuntimeMinutes: 90}
	if err := validateSchedule(valid); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	mondayAtNoon := time.Date(2026, 8, 17, 11, 0, 0, 0, time.UTC)
	if !cronMatches(valid.Cron, valid.Timezone, mondayAtNoon) {
		t.Fatal("expected noon London on Monday to match")
	}
	if cronMatches(valid.Cron, valid.Timezone, mondayAtNoon.Add(time.Minute)) {
		t.Fatal("unexpected match outside minute zero")
	}
	for _, invalid := range []store.Schedule{
		{Cron: "0 * * *", Timezone: "UTC"},
		{Cron: "61 * * * *", Timezone: "UTC"},
		{Cron: "*/0 * * * *", Timezone: "UTC"},
		{Cron: "0 * * * *", Timezone: "Nowhere/Imaginary"},
	} {
		if err := validateSchedule(invalid); err == nil {
			t.Fatalf("invalid schedule accepted: %+v", invalid)
		}
	}
}

func TestSchedulerStartsOncePerMinute(t *testing.T) {
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "scheduler.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{
		Repos:        map[string]config.Repo{"acme": {Repositories: []string{"acme/api"}}},
		Compute:      map[string]config.Compute{"cf": {Type: "cloudflare"}},
		Providers:    map[string]config.Provider{"zai": {Models: map[string]config.Model{"glm": {}}}},
		Environments: map[string]config.Environment{"go": {Compute: "cf", Provider: "zai", Model: "glm"}},
	}
	a := New(cfg, st)
	workerID, err := a.CreateWorker(context.Background(), WorkerParams{Name: "scheduled", Repo: "acme",
		Repository: "acme/api", Environment: "go", Mission: "work", Schedule: store.Schedule{Cron: "* * * * *", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.runDue(context.Background(), now)
	a.runDue(context.Background(), now)
	runs, err := st.ListRuns(context.Background(), workerID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("scheduled runs = %+v, %v", runs, err)
	}
	deadline := time.Now().Add(time.Second)
	for !runs[0].Status.Terminal() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		runs, _ = st.ListRuns(context.Background(), workerID)
	}
}

func TestOldDispatchCannotReleaseAResumedRun(t *testing.T) {
	a := &App{cancels: map[string]*runCancel{}}
	_, oldCancel := context.WithCancel(context.Background())
	newContext, newCancel := context.WithCancel(context.Background())
	old := &runCancel{cancel: oldCancel}
	newer := &runCancel{cancel: newCancel}
	a.cancels["run"] = newer
	a.release("run", old)
	if a.cancels["run"] != newer {
		t.Fatal("old dispatch released the resumed run's cancellation handle")
	}
	a.cancel("run")
	if newContext.Err() == nil {
		t.Fatal("active resumed run was not cancelled")
	}
}
