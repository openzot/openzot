package dispatch

import (
	"context"
	"errors"
	"io"
	"strings"
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
	// the first exec is the order write; the mission travels in the environment,
	// not shell-interpolated into the command
	if !strings.Contains(strings.Join(sandbox.command, " "), orderPath) {
		t.Fatalf("order write command = %v", sandbox.command)
	}
	if !strings.Contains(sandbox.env["ZOT_DISPATCH_ORDER"], "objective: test") {
		t.Fatalf("order environment = %q", sandbox.env["ZOT_DISPATCH_ORDER"])
	}
}

// The mission becomes a work order file, and the worker is invoked on that
// file: zot takes orders, not argv prose.
func TestDispatchWritesTheOrderThenRunsIt(t *testing.T) {
	sandbox := &recordingSandbox{}
	d := Dispatcher{Repo: fakeRepo{}, Resolver: fakeResolver{sandbox: sandbox}, Worker: testWorker, Output: io.Discard}

	result, err := d.Dispatch(context.Background(), Execution{Repository: "openzot/openzot", Mission: "fix the bug"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}

	if len(sandbox.execs) != 2 {
		t.Fatalf("execs = %d, want the order write then the run", len(sandbox.execs))
	}

	write := sandbox.execs[0]
	if write.command[0] != "/bin/sh" || !strings.Contains(write.command[2], orderPath) {
		t.Errorf("order write = %v", write.command)
	}
	if !strings.Contains(write.env["ZOT_DISPATCH_ORDER"], "objective: fix the bug") {
		t.Errorf("order payload = %q", write.env["ZOT_DISPATCH_ORDER"])
	}

	run := sandbox.execs[1]
	if len(run.command) != 2 || run.command[0] != "/runtime/zot" || run.command[1] != orderPath {
		t.Errorf("run command = %v", run.command)
	}
	if _, ok := run.env["ZOT_DISPATCH_ORDER"]; ok {
		t.Errorf("the order payload leaked into the run environment: %v", run.env)
	}
}

// A mission that already is an order document passes through intact, so the
// worker form can carry acceptance criteria.
func TestDispatchCarriesAFullOrderMission(t *testing.T) {
	sandbox := &recordingSandbox{}
	d := Dispatcher{Repo: fakeRepo{}, Resolver: fakeResolver{sandbox: sandbox}, Worker: testWorker, Output: io.Discard}

	if _, err := d.Dispatch(context.Background(),
		Execution{Repository: "openzot/openzot", Mission: "objective: build it\nacceptance:\n  - it builds\n"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	payload := sandbox.execs[0].env["ZOT_DISPATCH_ORDER"]
	if !strings.Contains(payload, "it builds") {
		t.Errorf("the mission's own acceptance criteria were dropped: %q", payload)
	}
}

type recordedExec struct {
	command []string
	env     map[string]string
}

type recordingSandbox struct{ execs []recordedExec }

func (s *recordingSandbox) WorkerPath() string { return "/runtime/zot" }
func (s *recordingSandbox) Exec(_ context.Context, command []string, env map[string]string, _ io.Writer) (int, error) {
	s.execs = append(s.execs, recordedExec{command: command, env: env})
	return 0, nil
}
func (s *recordingSandbox) Destroy(context.Context) error { return nil }

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
