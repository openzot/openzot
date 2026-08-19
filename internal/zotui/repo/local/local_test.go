package local

import (
	"context"
	"testing"
)

func TestProviderRestrictsAndCopiesRepositories(t *testing.T) {
	configured := []string{"openzot/openzot"}
	p := New(configured)
	configured[0] = "changed/outside"

	repositories, err := p.ListRepositories(context.Background())
	if err != nil || len(repositories) != 1 || repositories[0] != "openzot/openzot" {
		t.Fatalf("ListRepositories = %v, %v", repositories, err)
	}
	repositories[0] = "changed/copy"
	repositories, _ = p.ListRepositories(context.Background())
	if repositories[0] != "openzot/openzot" {
		t.Fatal("ListRepositories exposed provider state")
	}

	token, err := p.MintToken(context.Background(), []string{"openzot/openzot"})
	if err != nil || token.Value != "" || token.ExpiresAt.IsZero() {
		t.Fatalf("MintToken = %+v, %v", token, err)
	}
	if _, err := p.MintToken(context.Background(), []string{"other/repo"}); err == nil {
		t.Fatal("MintToken allowed an unconfigured repository")
	}
}
