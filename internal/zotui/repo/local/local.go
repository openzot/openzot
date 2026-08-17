// Package local exposes an explicitly configured local checkout without
// manufacturing a remote credential.
package local

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/openzot/openzot/internal/zotui/repo"
)

type Provider struct{ repositories []string }

func New(repositories []string) *Provider {
	return &Provider{repositories: slices.Clone(repositories)}
}

func (p *Provider) MintToken(_ context.Context, repositories []string) (*repo.Token, error) {
	for _, repository := range repositories {
		if !slices.Contains(p.repositories, repository) {
			return nil, fmt.Errorf("local repo: repository %q is not configured", repository)
		}
	}
	return &repo.Token{Repos: slices.Clone(repositories), ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (p *Provider) ListRepositories(context.Context) ([]string, error) {
	return slices.Clone(p.repositories), nil
}

var _ repo.Provider = (*Provider)(nil)
