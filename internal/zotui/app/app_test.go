package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/store"
)

func testConfig() *config.Config {
	return &config.Config{
		Sources: map[string]config.Source{
			// a per-source lockdown lets authorization pass offline (no key needed)
			"acme": {Type: "github", Repositories: []string{"acme/api"}},
		},
		Runners: map[string]config.Runner{"cf": {Type: "cloudflare"}},
		Models:  map[string]config.Model{"glm": {Provider: "zai", Model: "glm-5.2"}},
		Environments: map[string]config.Environment{
			"go": {Runner: "cf", Model: "glm", Image: "img"},
		},
	}
}

// The command center drives everything through the app: scheduling records a job
// (defaulting the model from the environment), a lockdown rejects a stray repo,
// and cancelling a scheduled job marks it cancelled.
func TestAppScheduleAuthorizeCancel(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(dir, "z.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	a := app.New(testConfig(), st)
	ctx := context.Background()

	// choices the create form offers
	if got := a.Sources(); len(got) != 1 || got[0] != "acme" {
		t.Errorf("Sources() = %v", got)
	}

	// schedule a permitted job; the model defaults from the environment
	id, err := a.Schedule(ctx, app.ScheduleParams{Source: "acme", Repository: "acme/api", Environment: "go", Task: "do it"})
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	jobs, err := a.Jobs(ctx)
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != id {
		t.Fatalf("expected the scheduled job, got %+v", jobs)
	}
	if jobs[0].Model != "glm" {
		t.Errorf("model = %q, want the environment default glm", jobs[0].Model)
	}

	// a repo outside the source lockdown is rejected before anything is recorded
	if _, err := a.Schedule(ctx, app.ScheduleParams{Source: "acme", Repository: "acme/other", Environment: "go", Task: "x"}); err == nil {
		t.Error("expected the lockdown to reject an unlisted repo")
	}

	// cancel a scheduled job (created directly so it is not racing the dispatch)
	cid, err := st.Create(ctx, store.Job{Source: "acme", Repository: "acme/api", Task: "t", Environment: "go", Model: "glm", Status: store.StatusScheduled})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Cancel(ctx, cid); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if j, _ := st.Get(ctx, cid); j.Status != store.StatusCancelled {
		t.Errorf("status = %q, want cancelled", j.Status)
	}

	// let the scheduled job's background dispatch settle before the store closes
	time.Sleep(150 * time.Millisecond)
}
