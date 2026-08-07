// Package source abstracts a repository provider - where jobs get their code and
// how zotui mints per-job credentials for it.
//
// GitHub is the first implementation (a GitHub App that mints installation
// tokens); GitLab and others satisfy the same interface, so the rest of zotui
// never depends on a specific provider.
package source

import (
	"context"
	"time"
)

// Token is a short-lived, repository-scoped credential a provider mints for one
// job's sandbox.
type Token struct {
	Value     string
	ExpiresAt time.Time
	Repos     []string
}

// Source is a repository provider.
type Source interface {
	// MintToken mints a short-lived credential scoped to repos.
	MintToken(ctx context.Context, repos []string) (*Token, error)

	// ListRepositories returns the repositories this provider exposes to zotui -
	// the choices offered when no lockdown is configured.
	ListRepositories(ctx context.Context) ([]string, error)
}
