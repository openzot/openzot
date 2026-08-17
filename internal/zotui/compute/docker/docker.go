// Package docker provides local zotui compute using Docker containers.
package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
)

const (
	workspace  = "/workspace"
	configPath = "/tmp/zot.yaml"
)

type commandRunner interface {
	Run(context.Context, io.Reader, io.Writer, string, ...string) (int, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, in io.Reader, out io.Writer, binary string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// Driver creates ephemeral local Docker containers.
type Driver struct {
	binary string
	runner commandRunner
}

// New returns a Docker compute driver using the Docker CLI.
func New() *Driver { return &Driver{binary: "docker", runner: execRunner{}} }

func newDriver(runner commandRunner) *Driver { return &Driver{binary: "docker", runner: runner} }

// Type implements compute.Provider.
func (d *Driver) Type() string { return "docker" }

// Create starts a container and installs the resolved zot configuration in it.
func (d *Driver) Create(ctx context.Context, spec compute.Spec) (compute.Sandbox, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	name, err := containerName()
	if err != nil {
		return nil, fmt.Errorf("name container: %w", err)
	}

	args := []string{"create", "--name", name, "--init", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--workdir", workspace}
	env := cloneEnv(spec.Env)
	env["HOME"] = "/tmp/zot-home"
	env["XDG_CACHE_HOME"] = "/tmp/zot-home/.cache"
	env["XDG_CONFIG_HOME"] = "/tmp/zot-home/.config"
	env["XDG_DATA_HOME"] = "/tmp/zot-home/.local/share"
	env["XDG_STATE_HOME"] = "/tmp/zot-home/.local/state"
	env["ZOT_CONFIG"] = configPath
	for _, key := range sortedKeys(env) {
		args = append(args, "--env", key+"="+env[key])
	}
	for _, mount := range spec.Mounts {
		value := "type=bind,source=" + mount.Source + ",target=" + mount.Target
		if mount.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	args = append(args, "--entrypoint", "sh", spec.Image, "-c",
		`mkdir -p "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_STATE_HOME"; trap 'exit 0' TERM INT; while :; do sleep 3600 & wait $!; done`)

	var output bytes.Buffer
	if code, runErr := d.runner.Run(ctx, nil, &output, d.binary, args...); runErr != nil || code != 0 {
		return nil, commandError("create", code, runErr, output.String())
	}
	s := &sandbox{name: name, binary: d.binary, runner: d.runner}
	if code, runErr := d.runner.Run(ctx, nil, &output, d.binary, "start", name); runErr != nil || code != 0 {
		s.cleanup()
		return nil, commandError("start", code, runErr, output.String())
	}

	data, err := zotConfig(spec)
	if err != nil {
		s.cleanup()
		return nil, fmt.Errorf("encode zot config: %w", err)
	}
	output.Reset()
	if code, runErr := d.runner.Run(ctx, bytes.NewReader(data), &output, d.binary, "exec", "-i", name, "sh", "-c", "umask 077; cat > "+configPath); runErr != nil || code != 0 {
		s.cleanup()
		return nil, commandError("configure", code, runErr, output.String())
	}
	return s, nil
}

type sandbox struct {
	name   string
	binary string
	runner commandRunner
	mu     sync.Mutex
	dead   bool
}

func (s *sandbox) Exec(ctx context.Context, cmd []string, env map[string]string, out io.Writer) (int, error) {
	if len(cmd) == 0 {
		return -1, fmt.Errorf("docker: command is required")
	}
	args := []string{"exec"}
	for _, key := range sortedKeys(env) {
		args = append(args, "--env", key+"="+env[key])
	}
	args = append(args, s.name)
	args = append(args, cmd...)
	return s.runner.Run(ctx, nil, out, s.binary, args...)
}

func (s *sandbox) Destroy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return nil
	}
	var output bytes.Buffer
	code, err := s.runner.Run(ctx, nil, &output, s.binary, "rm", "--force", s.name)
	if err != nil || code != 0 {
		return commandError("remove", code, err, output.String())
	}
	s.dead = true
	return nil
}

func (s *sandbox) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.Destroy(ctx)
}

func validateSpec(spec compute.Spec) error {
	if strings.TrimSpace(spec.Image) == "" {
		return fmt.Errorf("docker: image is required")
	}
	if strings.TrimSpace(spec.Model.Provider) == "" || strings.TrimSpace(spec.Model.Model) == "" {
		return fmt.Errorf("docker: model provider and name are required")
	}
	for _, mount := range spec.Mounts {
		if mount.Source == "" || mount.Target == "" {
			return fmt.Errorf("docker: mount source and target are required")
		}
		if strings.Contains(mount.Source, ",") || strings.Contains(mount.Target, ",") {
			return fmt.Errorf("docker: mount paths cannot contain commas")
		}
	}
	return nil
}

func zotConfig(spec compute.Spec) ([]byte, error) {
	return json.Marshal(map[string]any{
		"default_backend": "worker",
		"agent":           map[string]any{"model": spec.Model.Model, "max_iterations": spec.MaxIterations},
		"backends": map[string]any{"worker": map[string]string{
			"provider": spec.Model.Provider, "base_url": spec.Model.BaseURL, "api_key": spec.Model.APIKey,
		}},
	})
}

func containerName() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "zotui-" + hex.EncodeToString(raw), nil
}

func commandError(action string, code int, err error, output string) error {
	detail := strings.TrimSpace(output)
	if err != nil {
		return fmt.Errorf("docker %s: %w", action, err)
	}
	if detail != "" {
		return fmt.Errorf("docker %s exited with code %d: %s", action, code, detail)
	}
	return fmt.Errorf("docker %s exited with code %d", action, code)
}

func cloneEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+6)
	for key, value := range env {
		out[key] = value
	}
	return out
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var (
	_ compute.Provider = (*Driver)(nil)
	_ compute.Sandbox  = (*sandbox)(nil)
)
