// Package vercel provides remote zotui compute using Vercel Sandbox.
package vercel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
)

const (
	defaultBaseURL = "https://vercel.com/api"
	workspace      = "/vercel/sandbox"
	configPath     = workspace + "/.zot-home/.config/zot/config.json"
	configArchive  = ".zot-home/.config/zot/config.json"
	workerPath     = workspace + "/.zot-worker/zot"
	workerArchive  = ".zot-worker/zot"
	defaultTimeout = 45 * time.Minute
)

// Driver creates ephemeral Vercel Sandbox sessions.
type Driver struct {
	token      string
	teamID     string
	projectID  string
	timeout    time.Duration
	timeoutErr error
	baseURL    string
	client     *http.Client
}

// New returns a Vercel compute driver. baseURL is normally empty; it exists for
// self-hosted API proxies and local integration testing.
func New(token, teamID, projectID, timeout, baseURL string) *Driver {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	driver := newDriver(token, teamID, projectID, baseURL, http.DefaultClient)
	if strings.TrimSpace(timeout) != "" {
		driver.timeout, driver.timeoutErr = time.ParseDuration(timeout)
	}
	return driver
}

func newDriver(token, teamID, projectID, baseURL string, client *http.Client) *Driver {
	return &Driver{token: token, teamID: teamID, projectID: projectID, timeout: defaultTimeout,
		baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

// Type implements compute.Provider.
func (*Driver) Type() string { return "vercel" }

func (*Driver) Platform() string { return "linux/amd64" }

// Create starts a Vercel sandbox and installs the Zot worker and its private configuration.
func (d *Driver) Create(ctx context.Context, spec compute.Spec) (compute.Sandbox, error) {
	if err := d.validate(spec); err != nil {
		return nil, err
	}
	name, err := sandboxName()
	if err != nil {
		return nil, fmt.Errorf("vercel: name sandbox: %w", err)
	}

	env := cloneEnv(spec.Env)
	env["HOME"] = workspace + "/.zot-home"
	env["XDG_CACHE_HOME"] = env["HOME"] + "/.cache"
	env["XDG_CONFIG_HOME"] = env["HOME"] + "/.config"
	env["XDG_DATA_HOME"] = env["HOME"] + "/.local/share"
	env["XDG_STATE_HOME"] = env["HOME"] + "/.local/state"
	env["ZOT_CONFIG"] = configPath

	request := createRequest{Name: name, ProjectID: d.projectID, Image: strings.TrimSpace(spec.Image),
		Persistent: false, Timeout: d.timeout.Milliseconds(), Env: env}
	if spec.Source.URL != "" {
		request.Source = &gitSource{Type: "git", URL: spec.Source.URL, Depth: 1}
		if spec.Source.Password != "" {
			request.Source.Username = spec.Source.Username
			request.Source.Password = spec.Source.Password
		}
	}
	var created createResponse
	if err := d.jsonRequest(ctx, http.MethodPost, "/v3/sandboxes", request, &created); err != nil {
		return nil, fmt.Errorf("vercel: create sandbox: %w", err)
	}
	if created.Sandbox.Name == "" || created.Session.ID == "" {
		return nil, errors.New("vercel: create sandbox returned no sandbox name or session ID")
	}
	if created.Session.CWD == "" {
		created.Session.CWD = workspace
	}
	cwd := created.Session.CWD
	if spec.Source.Directory != "" {
		cwd = path.Join(cwd, spec.Source.Directory)
	}
	s := &sandbox{driver: d, name: created.Sandbox.Name, sessionID: created.Session.ID, cwd: cwd}

	config, err := compute.EncodeZotConfig(spec)
	if err != nil {
		s.cleanup()
		return nil, fmt.Errorf("vercel: encode zot config: %w", err)
	}
	if err := s.installPayload(ctx, config, spec.Worker.Data); err != nil {
		s.cleanup()
		return nil, err
	}
	return s, nil
}

func (d *Driver) validate(spec compute.Spec) error {
	if strings.TrimSpace(d.token) == "" {
		return errors.New("vercel: token is required")
	}
	if strings.TrimSpace(d.teamID) == "" {
		return errors.New("vercel: team_id is required")
	}
	if strings.TrimSpace(d.projectID) == "" {
		return errors.New("vercel: project_id is required")
	}
	if d.timeoutErr != nil {
		return fmt.Errorf("vercel: timeout must be a positive duration: %w", d.timeoutErr)
	}
	if d.timeout <= 0 {
		return errors.New("vercel: timeout must be a positive duration")
	}
	if strings.TrimSpace(spec.Model.Provider) == "" || strings.TrimSpace(spec.Model.Model) == "" {
		return errors.New("vercel: model provider and name are required")
	}
	if len(spec.Worker.Data) == 0 {
		return errors.New("vercel: worker binary is required")
	}
	if len(spec.Mounts) != 0 {
		return errors.New("vercel: local host mounts are not supported; use a remote repo connection")
	}
	if directory := spec.Source.Directory; directory != "" && (path.IsAbs(directory) || path.Clean(directory) != directory || directory == ".") {
		return errors.New("vercel: source directory must be a clean relative path")
	}
	endpoint, err := url.Parse(d.baseURL)
	if err != nil {
		return fmt.Errorf("vercel: invalid base_url: %w", err)
	}
	if endpoint.Scheme == "" || endpoint.Host == "" {
		return errors.New("vercel: base_url must be an absolute URL")
	}
	return nil
}

type createRequest struct {
	Name       string            `json:"name"`
	ProjectID  string            `json:"projectId"`
	Image      string            `json:"image,omitempty"`
	Persistent bool              `json:"persistent"`
	Timeout    int64             `json:"timeout"`
	Env        map[string]string `json:"env"`
	Source     *gitSource        `json:"source,omitempty"`
}

type gitSource struct {
	Type     string `json:"type"`
	URL      string `json:"url"`
	Depth    int    `json:"depth,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type createResponse struct {
	Sandbox struct {
		Name string `json:"name"`
	} `json:"sandbox"`
	Session struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"session"`
}

type sandbox struct {
	driver    *Driver
	name      string
	sessionID string
	cwd       string
	mu        sync.Mutex
	dead      bool
}

func (*sandbox) WorkerPath() string { return workerPath }

func (s *sandbox) installPayload(ctx context.Context, config, worker []byte) error {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, file := range []struct {
		name string
		mode int64
		data []byte
	}{{configArchive, 0o600, config}, {workerArchive, 0o755, worker}} {
		if err := tw.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data)), Typeflag: tar.TypeReg}); err != nil {
			return fmt.Errorf("vercel: archive runtime payload: %w", err)
		}
		if _, err := tw.Write(file.data); err != nil {
			return fmt.Errorf("vercel: archive runtime payload: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("vercel: archive runtime payload: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("vercel: archive runtime payload: %w", err)
	}

	path := "/v2/sandboxes/sessions/" + url.PathEscape(s.sessionID) + "/fs/write"
	response, err := s.driver.request(ctx, http.MethodPost, path, &archive, map[string]string{
		"Content-Type": "application/gzip", "x-cwd": workspace,
	})
	if err != nil {
		return fmt.Errorf("vercel: install runtime payload: %w", err)
	}
	defer response.Body.Close()
	if err := checkResponse(response); err != nil {
		return fmt.Errorf("vercel: install runtime payload: %w", err)
	}
	return nil
}

// Exec runs a command without a pseudo-terminal and writes Vercel's raw stdout
// and stderr chunks in arrival order. ANSI bytes are deliberately untouched.
func (s *sandbox) Exec(ctx context.Context, cmd []string, env map[string]string, out io.Writer) (int, error) {
	if len(cmd) == 0 {
		return -1, errors.New("vercel: command is required")
	}
	if out == nil {
		out = io.Discard
	}
	if env == nil {
		env = map[string]string{}
	}
	body := struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		CWD     string            `json:"cwd"`
		Env     map[string]string `json:"env"`
		Sudo    bool              `json:"sudo"`
		Wait    bool              `json:"wait"`
		Logs    bool              `json:"logs"`
	}{Command: cmd[0], Args: cmd[1:], CWD: s.cwd, Env: env, Wait: true, Logs: true}
	encoded, err := json.Marshal(body)
	if err != nil {
		return -1, fmt.Errorf("vercel: encode command: %w", err)
	}
	path := "/v2/sandboxes/sessions/" + url.PathEscape(s.sessionID) + "/cmd"
	response, err := s.driver.request(ctx, http.MethodPost, path, bytes.NewReader(encoded), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return -1, fmt.Errorf("vercel: execute command: %w", err)
	}
	defer response.Body.Close()
	if err := checkResponse(response); err != nil {
		return -1, fmt.Errorf("vercel: execute command: %w", err)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "application/x-ndjson" {
		return -1, fmt.Errorf("vercel: execute command: expected application/x-ndjson, got %q", mediaType)
	}

	decoder := json.NewDecoder(response.Body)
	for {
		var event commandEvent
		if err := decoder.Decode(&event); err != nil {
			if ctx.Err() != nil {
				return -1, ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return -1, errors.New("vercel: command stream ended before an exit code")
			}
			return -1, fmt.Errorf("vercel: decode command stream: %w", err)
		}
		if event.Command != nil && event.Command.ExitCode != nil {
			return *event.Command.ExitCode, nil
		}
		switch event.Stream {
		case "stdout", "stderr":
			var data string
			if err := json.Unmarshal(event.Data, &data); err != nil {
				return -1, fmt.Errorf("vercel: decode %s: %w", event.Stream, err)
			}
			if _, err := io.WriteString(out, data); err != nil {
				return -1, fmt.Errorf("vercel: write command output: %w", err)
			}
		case "error":
			var streamErr struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(event.Data, &streamErr); err != nil {
				return -1, fmt.Errorf("vercel: decode stream error: %w", err)
			}
			return -1, fmt.Errorf("vercel: command stream %s: %s", streamErr.Code, streamErr.Message)
		}
	}
}

type commandEvent struct {
	Command *struct {
		ID       string `json:"id"`
		ExitCode *int   `json:"exitCode"`
	} `json:"command,omitempty"`
	Stream string          `json:"stream,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// Destroy stops the active Vercel session. Non-persistent sandboxes discard
// their filesystem when the session stops.
func (s *sandbox) Destroy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead {
		return nil
	}
	path := "/v2/sandboxes/sessions/" + url.PathEscape(s.sessionID) + "/stop"
	response, err := s.driver.request(ctx, http.MethodPost, path, nil, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return fmt.Errorf("vercel: stop sandbox: %w", err)
	}
	defer response.Body.Close()
	if err := checkResponse(response); err != nil {
		return fmt.Errorf("vercel: stop sandbox: %w", err)
	}
	s.dead = true
	return nil
}

func (s *sandbox) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.Destroy(ctx)
}

func (d *Driver) jsonRequest(ctx context.Context, method, path string, value, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	response, err := d.request(ctx, method, path, bytes.NewReader(encoded), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := checkResponse(response); err != nil {
		return err
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (d *Driver) request(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	requestURL, err := url.Parse(d.baseURL + path)
	if err != nil {
		return nil, err
	}
	query := requestURL.Query()
	query.Set("teamId", d.teamID)
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("User-Agent", "openzot/zotui")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return d.client.Do(request)
}

func checkResponse(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("vercel API returned %d: %s", response.StatusCode, detail)
}

func sandboxName() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "zotui-" + hex.EncodeToString(raw), nil
}

func cloneEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+6)
	for key, value := range env {
		out[key] = value
	}
	return out
}

var (
	_ compute.Provider = (*Driver)(nil)
	_ compute.Sandbox  = (*sandbox)(nil)
)
