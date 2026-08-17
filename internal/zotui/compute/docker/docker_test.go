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
	args  []string
	stdin string
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

func (r *fakeRunner) Run(_ context.Context, in io.Reader, out io.Writer, _ string, args ...string) (int, error) {
	var stdin []byte
	if in != nil {
		stdin, _ = io.ReadAll(in)
	}
	r.calls = append(r.calls, call{args: slices.Clone(args), stdin: string(stdin)})
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
	runner := &fakeRunner{responses: []response{{}, {}, {}, {code: 7, output: "worker failed\n"}, {}}}
	driver := newDriver(runner)
	sandbox, err := driver.Create(context.Background(), compute.Spec{
		Image:         "openzot/zot:dev",
		Env:           map[string]string{"GOFLAGS": "-mod=mod"},
		Mounts:        []compute.Mount{{Source: "/src/openzot", Target: "/workspace"}},
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
	for _, want := range []string{"create --name zotui-", "--workdir /workspace", "--env GOFLAGS=-mod=mod", "--mount type=bind,source=/src/openzot,target=/workspace", "--entrypoint sh openzot/zot:dev"} {
		if !strings.Contains(create, want) {
			t.Errorf("docker create does not contain %q: %s", want, create)
		}
	}
	if runner.calls[1].args[0] != "start" {
		t.Fatalf("second call = %v", runner.calls[1].args)
	}
	var installed struct {
		DefaultBackend string `json:"default_backend"`
		Agent          struct {
			Model         string `json:"model"`
			MaxIterations int    `json:"max_iterations"`
		} `json:"agent"`
		Backends map[string]map[string]string `json:"backends"`
	}
	if err := json.Unmarshal([]byte(runner.calls[2].stdin), &installed); err != nil {
		t.Fatalf("installed config: %v", err)
	}
	if installed.DefaultBackend != "worker" || installed.Agent.Model != "glm-5.2" || installed.Agent.MaxIterations != 41 || installed.Backends["worker"]["api_key"] != "secret" {
		t.Fatalf("installed config = %+v", installed)
	}

	var output strings.Builder
	code, err := sandbox.Exec(context.Background(), []string{"zot", "repair the tests"}, map[string]string{"ZOT_REPO": "openzot/openzot", "GH_TOKEN": ""}, &output)
	if err != nil || code != 7 || output.String() != "worker failed\n" {
		t.Fatalf("Exec = code %d, output %q, err %v", code, output.String(), err)
	}
	execCall := strings.Join(runner.calls[3].args, " ")
	// Worker stdout must be a TTY so zot emits its real ANSI terminal view for
	// wterm instead of silently falling back to the unstyled pipe renderer.
	if got := runner.calls[3].args; len(got) < 2 || got[0] != "exec" || got[1] != "--tty" {
		t.Fatalf("docker exec did not allocate a TTY: %v", got)
	}
	if !strings.Contains(execCall, "--env ZOT_REPO=openzot/openzot") || !strings.HasSuffix(execCall, "zot repair the tests") {
		t.Fatalf("docker exec = %s", execCall)
	}
	if err := sandbox.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := sandbox.Destroy(context.Background()); err != nil || len(runner.calls) != 5 {
		t.Fatalf("idempotent Destroy made another call: %v, calls=%d", err, len(runner.calls))
	}
	if got := runner.calls[4].args; len(got) != 3 || got[0] != "rm" || got[1] != "--force" {
		t.Fatalf("cleanup call = %v", got)
	}
}

func TestCreateCleansUpAfterStartFailure(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}, {code: 1, output: "cannot start"}, {}}}
	_, err := newDriver(runner).Create(context.Background(), compute.Spec{
		Image: "image", Model: compute.ModelSpec{Provider: "openai", Model: "gpt"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("Create error = %v", err)
	}
	if got := runner.calls[len(runner.calls)-1].args; got[0] != "rm" || got[1] != "--force" {
		t.Fatalf("failed container was not cleaned up: %v", got)
	}
}

func TestCreateValidatesSpecBeforeDocker(t *testing.T) {
	tests := []compute.Spec{
		{},
		{Image: "image"},
		{Image: "image", Model: compute.ModelSpec{Provider: "p", Model: "m"}, Mounts: []compute.Mount{{Source: "/bad,path", Target: "/workspace"}}},
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
