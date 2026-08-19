package github

import (
	"context"
	"testing"
)

func TestPublicRepositoriesAreExplicitAndCredentialFree(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New accepted an unrestricted public connection")
	}
	provider, err := New([]string{"openzot/openzot"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	token, err := provider.MintToken(context.Background(), []string{"openzot/openzot"})
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if token.Value != "" || len(token.Repos) != 1 {
		t.Fatalf("public token = %+v", token)
	}
	if _, err := provider.MintToken(context.Background(), []string{"private/other"}); err == nil {
		t.Fatal("MintToken accepted an unconfigured repository")
	}
	repositories, err := provider.ListRepositories(context.Background())
	if err != nil || len(repositories) != 1 || repositories[0] != "openzot/openzot" {
		t.Fatalf("ListRepositories = %v, %v", repositories, err)
	}
	repositories[0] = "changed/outside"
	second, _ := provider.ListRepositories(context.Background())
	if second[0] != "openzot/openzot" {
		t.Fatalf("repository state was exposed: %v", second)
	}
}
