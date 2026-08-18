package app

import (
	"context"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/dispatch"
	"github.com/openzot/openzot/internal/zotui/repo"
)

type discoveringRepo struct {
	repositories []string
	calls        int
}

// A configured lockdown can narrow a GitHub App installation, but it cannot
// make an ungranted repository appear in the form or pass authorization.
func TestChoicesIntersectLockdownWithInstallationRepositories(t *testing.T) {
	provider := &discoveringRepo{repositories: []string{"acme/api", "acme/web"}}
	cfg := &config.Config{
		Repos: map[string]config.Repo{"github": {
			Type: "github", Repositories: []string{"acme/api", "acme/missing"},
		}},
		Environments: map[string]config.Environment{"remote": {}},
	}
	a := New(cfg, nil)
	a.repoCache["github"] = provider

	choices, err := a.Choices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := choices.Repositories["github"]; len(got) != 1 || got[0] != "acme/api" {
		t.Fatalf("repositories = %v", got)
	}
	if provider.calls != 1 {
		t.Fatalf("installation discovery calls = %d", provider.calls)
	}
	if err := a.authorize(context.Background(), dispatch.Execution{
		Repo: "github", Repository: "acme/api", Environment: "remote",
	}); err != nil {
		t.Fatalf("authorize granted repository: %v", err)
	}
	if err := a.authorize(context.Background(), dispatch.Execution{
		Repo: "github", Repository: "acme/missing", Environment: "remote",
	}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("authorize ungranted repository: %v", err)
	}
}

func (r *discoveringRepo) ListRepositories(context.Context) ([]string, error) {
	r.calls++
	return r.repositories, nil
}

func (*discoveringRepo) MintToken(context.Context, []string) (*repo.Token, error) {
	return nil, nil
}

func TestChoicesDiscoversAndCachesInstallationRepositories(t *testing.T) {
	provider := &discoveringRepo{repositories: []string{"acme/web", "acme/api"}}
	a := New(&config.Config{Repos: map[string]config.Repo{"github": {Type: "github"}}}, nil)
	a.repoCache["github"] = provider

	first, err := a.Choices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Repositories["github"]; len(got) != 2 || got[0] != "acme/api" || got[1] != "acme/web" {
		t.Fatalf("repositories = %v", got)
	}
	first.Repositories["github"][0] = "changed/outside"
	second, err := a.Choices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || second.Repositories["github"][0] != "acme/api" {
		t.Fatalf("calls = %d, cached repositories = %v", provider.calls, second.Repositories["github"])
	}
}
