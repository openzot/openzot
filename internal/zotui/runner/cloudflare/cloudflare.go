// Package cloudflare runs jobs on Cloudflare sandboxes.
//
// This is a stub: the Runner/Sandbox shape is in place and wired into dispatch,
// but the calls to Cloudflare's API are TODO.
package cloudflare

import (
	"context"
	"errors"
	"io"

	"github.com/openzot/openzot/internal/zotui/runner"
)

// Driver creates Cloudflare sandboxes for one account.
type Driver struct {
	AccountID string
	APIToken  string
}

// New returns a Cloudflare driver.
func New(accountID, apiToken string) *Driver {
	return &Driver{AccountID: accountID, APIToken: apiToken}
}

// Type implements runner.Runner.
func (d *Driver) Type() string { return "cloudflare" }

// Create boots a sandbox from spec.
func (d *Driver) Create(ctx context.Context, spec runner.Spec) (runner.Sandbox, error) {
	// TODO: call the Cloudflare Sandbox API to boot a container from spec.Image
	// with spec.Env, and return a handle that streams exec output back.
	_ = spec
	return nil, errors.New("cloudflare: Create not wired yet")
}

// sandbox is the running-instance handle. Fields land once the API client does.
type sandbox struct {
	id string
}

func (s *sandbox) Exec(ctx context.Context, cmd []string, env map[string]string, out io.Writer) (int, error) {
	return 0, errors.New("cloudflare: Exec not wired yet")
}

func (s *sandbox) Destroy(ctx context.Context) error {
	return errors.New("cloudflare: Destroy not wired yet")
}

// Compile-time checks that the driver satisfies the interfaces.
var (
	_ runner.Runner  = (*Driver)(nil)
	_ runner.Sandbox = (*sandbox)(nil)
)
