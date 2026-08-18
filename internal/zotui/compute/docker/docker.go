// Package docker provides local zotui compute using Docker containers.
package docker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
)

const (
	workspace    = "/workspace"
	configPath   = "/tmp/zot.yaml"
	workerPath   = "/tmp/zotui-worker/zot"
	defaultImage = "mcr.microsoft.com/devcontainers/base:bookworm"
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

// Platform implements compute.Provider. Docker Desktop and the development
// container run Linux containers using the host architecture by default.
func (*Driver) Platform() string { return "linux/" + runtime.GOARCH }

// Create starts a container and installs the Zot worker and its configuration.
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
	image := strings.TrimSpace(spec.Image)
	if image == "" {
		image = defaultImage
	}
	args = append(args, "--entrypoint", "sh", image, "-c",
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
	output.Reset()
	prepareWorkspace := fmt.Sprintf("mkdir -p %s; chown -R %d:%d %s", workspace, os.Getuid(), os.Getgid(), workspace)
	if code, runErr := d.runner.Run(ctx, nil, &output, d.binary, "exec", "--user", "0", name, "sh", "-c", prepareWorkspace); runErr != nil || code != 0 {
		s.cleanup()
		return nil, commandError("prepare workspace", code, runErr, output.String())
	}
	if err := d.seedSource(ctx, s, spec.Source); err != nil {
		s.cleanup()
		return nil, err
	}
	output.Reset()
	installWorker := "umask 077; mkdir -p /tmp/zotui-worker; cat > " + workerPath + "; chmod 755 " + workerPath
	if code, runErr := d.runner.Run(ctx, bytes.NewReader(spec.Worker.Data), &output, d.binary,
		"exec", "-i", name, "sh", "-c", installWorker); runErr != nil || code != 0 {
		s.cleanup()
		return nil, commandError("install worker", code, runErr, output.String())
	}

	data, err := compute.EncodeZotConfig(spec)
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

func (d *Driver) seedSource(ctx context.Context, s *sandbox, source compute.Source) error {
	if source.LocalPath != "" {
		return d.cloneLocal(ctx, s, source.LocalPath)
	}
	if source.URL == "" {
		return nil
	}
	return d.cloneRemote(ctx, s, source)
}

func (d *Driver) cloneLocal(ctx context.Context, s *sandbox, localPath string) error {
	tempDir, err := os.MkdirTemp("", "zotui-source-")
	if err != nil {
		return fmt.Errorf("docker bundle local source: %w", err)
	}
	defer os.RemoveAll(tempDir)
	bundlePath := filepath.Join(tempDir, "source.bundle")

	var output bytes.Buffer
	if code, runErr := d.runner.Run(ctx, nil, &output, "git", "-C", localPath, "bundle", "create", bundlePath, "--all"); runErr != nil || code != 0 {
		return commandError("bundle local source", code, runErr, output.String())
	}
	output.Reset()
	if code, runErr := d.runner.Run(ctx, nil, &output, d.binary, "cp", bundlePath, s.name+":/tmp/zotui-source.bundle"); runErr != nil || code != 0 {
		return commandError("copy local source", code, runErr, output.String())
	}
	output.Reset()
	clone := `git clone --no-local /tmp/zotui-source.bundle /workspace; clone_status=$?; rm -f /tmp/zotui-source.bundle; exit "$clone_status"`
	if code, runErr := d.runner.Run(ctx, nil, &output, d.binary, "exec", s.name, "sh", "-c", clone); runErr != nil || code != 0 {
		return commandError("clone local source", code, runErr, output.String())
	}
	return nil
}

func (d *Driver) cloneRemote(ctx context.Context, s *sandbox, source compute.Source) error {
	var output bytes.Buffer
	if source.Password == "" {
		if code, runErr := d.runner.Run(ctx, nil, &output, d.binary, "exec", s.name,
			"git", "clone", "--depth", "1", source.URL, workspace); runErr != nil || code != 0 {
			return commandError("clone remote source", code, runErr, output.String())
		}
		return nil
	}

	credential := source.Username + "\n" + source.Password + "\n"
	installCredential := `umask 077
mkdir -p /tmp/zotui-git
IFS= read -r username
IFS= read -r password
printf '%s' "$username" > /tmp/zotui-git/username
printf '%s' "$password" > /tmp/zotui-git/password
cat > /tmp/zotui-git/askpass <<'EOF'
#!/bin/sh
case "$1" in
  *Username*) cat /tmp/zotui-git/username ;;
  *) cat /tmp/zotui-git/password ;;
esac
EOF
chmod 700 /tmp/zotui-git/askpass`
	if code, runErr := d.runner.Run(ctx, strings.NewReader(credential), &output, d.binary,
		"exec", "-i", s.name, "sh", "-c", installCredential); runErr != nil || code != 0 {
		return commandError("install Git credential", code, runErr, output.String())
	}
	output.Reset()
	clone := `trap 'rm -rf /tmp/zotui-git' EXIT; GIT_ASKPASS=/tmp/zotui-git/askpass GIT_TERMINAL_PROMPT=0 git clone --depth 1 "$1" /workspace`
	if code, runErr := d.runner.Run(ctx, nil, &output, d.binary,
		"exec", s.name, "sh", "-c", clone, "sh", source.URL); runErr != nil || code != 0 {
		return commandError("clone remote source", code, runErr, output.String())
	}
	return nil
}

type sandbox struct {
	name   string
	binary string
	runner commandRunner
	mu     sync.Mutex
	dead   bool
}

func (*sandbox) WorkerPath() string { return workerPath }

func (s *sandbox) Exec(ctx context.Context, cmd []string, env map[string]string, out io.Writer) (int, error) {
	if len(cmd) == 0 {
		return -1, fmt.Errorf("docker: command is required")
	}
	// Keep stdout as a stream. The caller separately declares ANSI support; a
	// pseudo-TTY would make zot start its keyboard-driven alternate-screen UI.
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
	if strings.TrimSpace(spec.Model.Provider) == "" || strings.TrimSpace(spec.Model.Model) == "" {
		return fmt.Errorf("docker: model provider and name are required")
	}
	if len(spec.Worker.Data) == 0 {
		return fmt.Errorf("docker: worker binary is required")
	}
	if spec.Source.LocalPath != "" && spec.Source.URL != "" {
		return fmt.Errorf("docker: source cannot contain both a local path and remote URL")
	}
	if spec.Source.Password != "" && strings.TrimSpace(spec.Source.Username) == "" {
		return fmt.Errorf("docker: source username is required with a password")
	}
	if strings.ContainsAny(spec.Source.Username, "\r\n") || strings.ContainsAny(spec.Source.Password, "\r\n") {
		return fmt.Errorf("docker: source credentials cannot contain newlines")
	}
	return nil
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
