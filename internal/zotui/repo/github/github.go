// Package github exposes explicitly configured public GitHub repositories.
package github

import (
	"context"
	"fmt"
	"slices"

	"github.com/openzot/openzot/internal/zotui/repo"
)

// Provider grants read-only access to a fixed list of public repositories. It
// returns no credential, so remote compute clones them anonymously.
type Provider struct{ repositories []string }

func New(repositories []string) (*Provider, error) {
	if len(repositories) == 0 {
		return nil, fmt.Errorf("public GitHub repo connection requires an explicit repositories list")
	}
	return &Provider{repositories: slices.Clone(repositories)}, nil
}

func (p *Provider) MintToken(_ context.Context, repositories []string) (*repo.Token, error) {
	for _, repository := range repositories {
		if !slices.Contains(p.repositories, repository) {
			return nil, fmt.Errorf("public GitHub repo %q is not configured", repository)
		}
	}
	return &repo.Token{Repos: slices.Clone(repositories)}, nil
}

func (p *Provider) ListRepositories(context.Context) ([]string, error) {
	return slices.Clone(p.repositories), nil
}

var _ repo.Provider = (*Provider)(nil)
