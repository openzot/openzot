package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/zotui/compute"
)

type call struct {
	binary string
	args   []string
	stdin  string
}

type response struct {
	code   int
	err    error
	output string
}

type fakeRunner struct {
	calls     []call
	responses []response
}

func (r *fakeRunner) Run(_ context.Context, in io.Reader, out io.Writer, binary string, args ...string) (int, error) {
	var stdin []byte
	if in != nil {
		stdin, _ = io.ReadAll(in)
	}
	r.calls = append(r.calls, call{binary: binary, args: slices.Clone(args), stdin: string(stdin)})
	var result response
	if len(r.responses) > 0 {
		result, r.responses = r.responses[0], r.responses[1:]
	}
	if out != nil {
		_, _ = io.WriteString(out, result.output)
	}
	return result.code, result.err
}

func TestDockerSandboxLifecycle(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}, {}, {}, {}, {}, {}, {code: 7, output: "worker failed\n"}, {}}}
	driver := newDriver(runner)
	sandbox, err := driver.Create(context.Background(), compute.Spec{
		Env:           map[string]string{"GOFLAGS": "-mod=mod"},
		Source:        compute.Source{URL: "https://github.com/openzot/openzot.git"},
		Worker:        compute.Worker{Platform: "linux/amd64", Data: []byte("deployed-zot-binary")},
		Model:         compute.ModelSpec{Provider: "zai", Model: "glm-5.2", APIKey: "secret", BaseURL: "https://models.test"},
		MaxIterations: 41,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if driver.Type() != "docker" {
		t.Fatalf("Type = %q", driver.Type())
	}
	create := strings.Join(runner.calls[0].args, " ")
	for _, want := range []string{"create --name zotui-", "--workdir /workspace", "--env GOFLAGS=-mod=mod", "--entrypoint sh " + defaultImage} {
		if !strings.Contains(create, want) {
			t.Errorf("docker create does not contain %q: %s", want, create)
		}
	}
	if strings.Contains(create, "--mount") {
		t.Fatalf("isolated container used a host mount: %s", create)
	}
	if runner.calls[1].args[0] != "start" {
		t.Fatalf("second call = %v", runner.calls[1].args)
	}
	if got := strings.Join(runner.calls[2].args, " "); !strings.Contains(got, "exec --user 0") || !strings.Contains(got, "chown -R") {
		t.Fatalf("workspace was not prepared for the sandbox user: %s", got)
	}
	if got := strings.Join(runner.calls[3].args, " "); !strings.Contains(got, "git clone --depth 1 https://github.com/openzot/openzot.git /workspace") {
		t.Fatalf("remote source was not cloned into the sandbox: %s", got)
	}
	installedWorker := strings.Join(runner.calls[4].args, " ")
	if runner.calls[4].stdin != "deployed-zot-binary" || !strings.Contains(installedWorker, "cat > /tmp/zotui-worker/zot") ||
		!strings.Contains(installedWorker, "chmod 755 /tmp/zotui-worker/zot") {
		t.Fatalf("installed worker = args %v, stdin %q", runner.calls[4].args, runner.calls[4].stdin)
	}
	var installed struct {
		DefaultProvider string `json:"default_provider"`
		Agent           struct {
			Model         string `json:"model"`
			MaxIterations int    `json:"max_iterations"`
		} `json:"agent"`
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal([]byte(runner.calls[5].stdin), &installed); err != nil {
		t.Fatalf("installed config: %v", err)
	}
	if installed.DefaultProvider != "worker" || installed.Agent.Model != "glm-5.2" || installed.Agent.MaxIterations != 41 ||
		installed.Providers["worker"]["driver"] != "zai" || installed.Providers["worker"]["api_key"] != "secret" {
		t.Fatalf("installed config = %+v", installed)
	}

	var output strings.Builder
	code, err := sandbox.Exec(context.Background(), []string{sandbox.WorkerPath(), "repair the tests"}, map[string]string{"ZOT_REPO": "openzot/openzot", "GH_TOKEN": ""}, &output)
	if err != nil || code != 7 || output.String() != "worker failed\n" {
		t.Fatalf("Exec = code %d, output %q, err %v", code, output.String(), err)
	}
	execCall := strings.Join(runner.calls[6].args, " ")
	// A browser can render ANSI but cannot drive an interactive terminal. A TTY
	// would make zot print pager controls and alternate-screen frames.
	if got := runner.calls[6].args; len(got) < 1 || got[0] != "exec" || slices.Contains(got, "--tty") {
		t.Fatalf("docker exec allocated an interactive TTY: %v", got)
	}
	if !strings.Contains(execCall, "--env ZOT_REPO=openzot/openzot") || !strings.HasSuffix(execCall, "/tmp/zotui-worker/zot repair the tests") {
		t.Fatalf("docker exec = %s", execCall)
	}
	if err := sandbox.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := sandbox.Destroy(context.Background()); err != nil || len(runner.calls) != 8 {
		t.Fatalf("idempotent Destroy made another call: %v, calls=%d", err, len(runner.calls))
	}
	if got := runner.calls[7].args; len(got) != 3 || got[0] != "rm" || got[1] != "--force" {
		t.Fatalf("cleanup call = %v", got)
	}
}

// A private clone credential must enter through stdin and be removed after the
// clone; putting it in Docker's argv exposes it through the host process list.
func TestCreateClonesPrivateRemoteWithoutCredentialInArguments(t *testing.T) {
	runner := &fakeRunner{}
	sandbox, err := newDriver(runner).Create(context.Background(), compute.Spec{
		Source: compute.Source{URL: "https://github.com/openzot/private.git", Username: "x-access-token", Password: "short-lived-secret"},
		Worker: compute.Worker{Data: []byte("zot")}, Model: compute.ModelSpec{Provider: "openai", Model: "gpt"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sandbox.Destroy(context.Background())

	if got := runner.calls[3].stdin; got != "x-access-token\nshort-lived-secret\n" {
		t.Fatalf("credential stdin = %q", got)
	}
	for _, call := range runner.calls {
		if strings.Contains(strings.Join(call.args, " "), "short-lived-secret") {
			t.Fatalf("credential leaked into command arguments: %v", call.args)
		}
	}
	clone := strings.Join(runner.calls[4].args, " ")
	if !strings.Contains(clone, "GIT_ASKPASS=/tmp/zotui-git/askpass") || !strings.Contains(clone, "rm -rf /tmp/zotui-git") {
		t.Fatalf("authenticated clone did not use an ephemeral askpass helper: %s", clone)
	}
}

// A local checkout is transferred as a Git bundle and cloned inside the
// container, so the agent cannot write through to the host working tree.
func TestCreateClonesLocalGitBundleWithoutMountingHost(t *testing.T) {
	runner := &fakeRunner{}
	sandbox, err := newDriver(runner).Create(context.Background(), compute.Spec{
		Source: compute.Source{LocalPath: "/src/openzot"}, Worker: compute.Worker{Data: []byte("zot")},
		Model: compute.ModelSpec{Provider: "openai", Model: "gpt"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer sandbox.Destroy(context.Background())

	create := strings.Join(runner.calls[0].args, " ")
	if strings.Contains(create, "--mount") {
		t.Fatalf("local checkout was mounted: %s", create)
	}
	if runner.calls[3].binary != "git" || !strings.Contains(strings.Join(runner.calls[3].args, " "), "-C /src/openzot bundle create") {
		t.Fatalf("local source was not bundled: %+v", runner.calls[3])
	}
	if runner.calls[4].binary != "docker" || runner.calls[4].args[0] != "cp" {
		t.Fatalf("bundle was not copied into the container: %+v", runner.calls[4])
	}
	if got := strings.Join(runner.calls[5].args, " "); !strings.Contains(got, "git clone --no-local /tmp/zotui-source.bundle /workspace") {
		t.Fatalf("bundle was not cloned in the container: %s", got)
	}
}

func TestCreateCleansUpAfterStartFailure(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}, {code: 1, output: "cannot start"}, {}}}
	_, err := newDriver(runner).Create(context.Background(), compute.Spec{
		Image: "image", Worker: compute.Worker{Data: []byte("zot")}, Model: compute.ModelSpec{Provider: "openai", Model: "gpt"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("Create error = %v", err)
	}
	if !slices.Contains(runner.calls[0].args, "image") || slices.Contains(runner.calls[0].args, defaultImage) {
		t.Fatalf("custom image did not override default: %v", runner.calls[0].args)
	}
	if got := runner.calls[len(runner.calls)-1].args; got[0] != "rm" || got[1] != "--force" {
		t.Fatalf("failed container was not cleaned up: %v", got)
	}
}

func TestCreateValidatesSpecBeforeDocker(t *testing.T) {
	tests := []compute.Spec{
		{},
		{Image: "image"},
		{Image: "image", Model: compute.ModelSpec{Provider: "p", Model: "m"}},
		{Worker: compute.Worker{Data: []byte("zot")}, Model: compute.ModelSpec{Provider: "p", Model: "m"}, Source: compute.Source{LocalPath: "/local", URL: "https://example.test/repo.git"}},
		{Worker: compute.Worker{Data: []byte("zot")}, Model: compute.ModelSpec{Provider: "p", Model: "m"}, Source: compute.Source{URL: "https://example.test/repo.git", Password: "secret"}},
		{Worker: compute.Worker{Data: []byte("zot")}, Model: compute.ModelSpec{Provider: "p", Model: "m"}, Source: compute.Source{URL: "https://example.test/repo.git", Username: "user\nname", Password: "secret"}},
	}
	for _, spec := range tests {
		runner := &fakeRunner{}
		if _, err := newDriver(runner).Create(context.Background(), spec); err == nil {
			t.Fatalf("Create accepted invalid spec: %+v", spec)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("invalid spec reached Docker: %+v", runner.calls)
		}
	}
}

func TestExecReportsCommandStartError(t *testing.T) {
	runner := &fakeRunner{responses: []response{{err: errors.New("docker missing")}}}
	s := &sandbox{name: "zotui-test", binary: "docker", runner: runner}
	if _, err := s.Exec(context.Background(), []string{"zot", "mission"}, nil, io.Discard); err == nil {
		t.Fatal("Exec hid Docker start error")
	}
}
