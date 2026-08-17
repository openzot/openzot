package app

import (
	"context"
	"testing"

	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/repo"
)

type discoveringRepo struct {
	repositories []string
	calls        int
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
