package dispatch

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/openzot/openzot/internal/zotui/compute"
	"github.com/openzot/openzot/internal/zotui/repo"
)

type fakeRepo struct{}

func (fakeRepo) MintToken(context.Context, []string) (*repo.Token, error) {
	return &repo.Token{}, nil
}

type tokenRepo struct{ value string }

func (r tokenRepo) MintToken(context.Context, []string) (*repo.Token, error) {
	return &repo.Token{Value: r.value}, nil
}
func (tokenRepo) ListRepositories(context.Context) ([]string, error) { return nil, nil }

type capturingProvider struct {
	sandbox compute.Sandbox
	spec    compute.Spec
}

func (*capturingProvider) Type() string     { return "capture" }
func (*capturingProvider) Platform() string { return "linux/amd64" }
func (p *capturingProvider) Create(_ context.Context, spec compute.Spec) (compute.Sandbox, error) {
	p.spec = spec
	return p.sandbox, nil
}

type providerResolver struct{ provider compute.Provider }

func (r providerResolver) Resolve(Execution) (compute.Provider, compute.Spec, error) {
	return r.provider, compute.Spec{Platform: "linux/amd64", Source: compute.Source{URL: "https://github.com/openzot/openzot.git", Username: "x-access-token", Directory: "openzot"}}, nil
}
func (fakeRepo) ListRepositories(context.Context) ([]string, error) { return nil, nil }

type fakeResolver struct{ sandbox compute.Sandbox }

func (r fakeResolver) Resolve(Execution) (compute.Provider, compute.Spec, error) {
	return fakeProvider{sandbox: r.sandbox}, compute.Spec{Platform: "linux/amd64"}, nil
}

type fakeProvider struct{ sandbox compute.Sandbox }

func (fakeProvider) Type() string     { return "fake" }
func (fakeProvider) Platform() string { return "linux/amd64" }
func (p fakeProvider) Create(context.Context, compute.Spec) (compute.Sandbox, error) {
	return p.sandbox, nil
}

type cancelingSandbox struct {
	cancel             context.CancelFunc
	destroyed          bool
	destroyContextLive bool
	env                map[string]string
	command            []string
}

func (s *cancelingSandbox) WorkerPath() string { return "/runtime/zot" }
func (s *cancelingSandbox) Exec(ctx context.Context, command []string, env map[string]string, _ io.Writer) (int, error) {
	s.env = env
	s.command = command
	s.cancel()
	return 0, ctx.Err()
}
func (s *cancelingSandbox) Destroy(ctx context.Context) error {
	s.destroyed = true
	s.destroyContextLive = ctx.Err() == nil
	return nil
}

func TestDispatchCleansUpWithLiveContextAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sandbox := &cancelingSandbox{cancel: cancel}
	d := Dispatcher{Repo: fakeRepo{}, Resolver: fakeResolver{sandbox: sandbox}, Worker: testWorker, Output: io.Discard}
	_, err := d.Dispatch(ctx, Execution{Repository: "openzot/openzot", Mission: "test"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Dispatch error = %v", err)
	}
	if !sandbox.destroyed || !sandbox.destroyContextLive {
		t.Fatalf("cleanup = destroyed %v, live context %v", sandbox.destroyed, sandbox.destroyContextLive)
	}
	if sandbox.env["ZOT_REPO"] != "openzot/openzot" {
		t.Fatalf("repository environment = %v", sandbox.env)
	}
	if sandbox.env["ZOT_UI_COLOR"] != "always" {
		t.Fatalf("browser terminal color capability was not declared: %v", sandbox.env)
	}
	if _, ok := sandbox.env["GH_TOKEN"]; ok {
		t.Fatalf("an empty repository credential was injected: %v", sandbox.env)
	}
	if len(sandbox.command) != 2 || sandbox.command[0] != "/runtime/zot" || sandbox.command[1] != "test" {
		t.Fatalf("worker command = %v", sandbox.command)
	}
}

// A run must deploy the worker selected for the compute platform and execute
// the provider's installed path; otherwise images still secretly need Zot.
func TestDispatchPassesMintedCredentialToComputeSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sandbox := &cancelingSandbox{cancel: cancel}
	provider := &capturingProvider{sandbox: sandbox}
	requestedPlatform := ""
	d := Dispatcher{Repo: tokenRepo{value: "repo-token"}, Resolver: providerResolver{provider: provider}, Output: io.Discard,
		Worker: func(platform string) (compute.Worker, error) {
			requestedPlatform = platform
			return compute.Worker{Platform: platform, Data: []byte("worker executable")}, nil
		}}
	_, _ = d.Dispatch(ctx, Execution{Repository: "openzot/openzot", Mission: "test"})
	if provider.spec.Source.Password != "repo-token" {
		t.Fatalf("compute source credential = %q", provider.spec.Source.Password)
	}
	if requestedPlatform != "linux/amd64" || string(provider.spec.Worker.Data) != "worker executable" {
		t.Fatalf("worker platform = %q, payload = %q", requestedPlatform, provider.spec.Worker.Data)
	}
}

func testWorker(platform string) (compute.Worker, error) {
	return compute.Worker{Platform: platform, Data: []byte("worker executable")}, nil
}
