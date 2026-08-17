package vercel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/zotui/compute"
)

func TestSandboxLifecycleUsesVercelAPIAndStreamsRawOutput(t *testing.T) {
	var createBody map[string]any
	var commandBody map[string]any
	var installedConfig []byte
	var installedWorker []byte
	var installedWorkerMode int64
	stopped := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sandbox-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("teamId") != "team_123" {
			t.Errorf("teamId = %q", r.URL.Query().Get("teamId"))
		}

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v3/sandboxes":
			decodeJSON(t, r.Body, &createBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"sandbox":{"name":"zotui-test"},"session":{"id":"sess_123","cwd":"/vercel/sandbox"},"routes":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes/sessions/sess_123/fs/write":
			if got := r.Header.Get("x-cwd"); got != "/vercel/sandbox" {
				t.Errorf("config extraction cwd = %q", got)
			}
			installed := readTarFiles(t, r.Body)
			installedConfig = installed[".zot-home/.config/zot/config.json"].data
			installedWorker = installed[".zot-worker/zot"].data
			installedWorkerMode = installed[".zot-worker/zot"].mode
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes/sessions/sess_123/cmd":
			decodeJSON(t, r.Body, &commandBody)
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = io.WriteString(w, "{\"command\":{\"id\":\"cmd_123\",\"exitCode\":null}}\n")
			_, _ = io.WriteString(w, "{\"stream\":\"stdout\",\"data\":\"\\u001b[32mworking\\u001b[0m\\n\"}\n")
			_, _ = io.WriteString(w, "{\"stream\":\"stderr\",\"data\":\"warning\\n\"}\n")
			_, _ = io.WriteString(w, "{\"command\":{\"id\":\"cmd_123\",\"exitCode\":7}}\n")
		case r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes/sessions/sess_123/stop":
			stopped = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"session":{"id":"sess_123","status":"stopped"}}`)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	driver := newDriver("sandbox-token", "team_123", "prj_123", server.URL, server.Client())
	sandbox, err := driver.Create(context.Background(), compute.Spec{
		Image: "go-environment:latest",
		Env:   map[string]string{"GOFLAGS": "-mod=mod"},
		Source: compute.Source{
			URL:       "https://github.com/openzot/openzot.git",
			Username:  "x-access-token",
			Password:  "repo-token",
			Directory: "openzot",
		},
		Worker:        compute.Worker{Platform: "linux/amd64", Data: []byte("deployed-zot-binary")},
		Model:         compute.ModelSpec{Provider: "zai", Model: "glm-5.2", APIKey: "model-secret"},
		MaxIterations: 99,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if driver.Type() != "vercel" {
		t.Fatalf("Type = %q", driver.Type())
	}
	if createBody["projectId"] != "prj_123" || createBody["image"] != "go-environment:latest" || createBody["persistent"] != false {
		t.Fatalf("create body = %#v", createBody)
	}
	if createBody["timeout"] != float64((45 * time.Minute).Milliseconds()) {
		t.Fatalf("default timeout = %#v", createBody["timeout"])
	}
	source, _ := createBody["source"].(map[string]any)
	if source["url"] != "https://github.com/openzot/openzot.git" || source["password"] != "repo-token" {
		t.Fatalf("source = %#v", source)
	}
	env, _ := createBody["env"].(map[string]any)
	if env["GOFLAGS"] != "-mod=mod" || env["ZOT_CONFIG"] != "/vercel/sandbox/.zot-home/.config/zot/config.json" {
		t.Fatalf("sandbox env = %#v", env)
	}
	var configBody struct {
		Agent struct {
			MaxIterations int `json:"max_iterations"`
		} `json:"agent"`
		Backends map[string]map[string]string `json:"backends"`
	}
	if err := json.Unmarshal(installedConfig, &configBody); err != nil {
		t.Fatalf("installed config: %v", err)
	}
	if configBody.Agent.MaxIterations != 99 || configBody.Backends["worker"]["api_key"] != "model-secret" {
		t.Fatalf("installed config = %+v", configBody)
	}
	if string(installedWorker) != "deployed-zot-binary" || installedWorkerMode != 0o755 {
		t.Fatalf("installed worker = %q, mode %#o", installedWorker, installedWorkerMode)
	}

	var output strings.Builder
	code, err := sandbox.Exec(context.Background(), []string{sandbox.WorkerPath(), "fix the tests"}, map[string]string{"ZOT_UI_COLOR": "always"}, &output)
	if err != nil || code != 7 {
		t.Fatalf("Exec = code %d, err %v", code, err)
	}
	if output.String() != "\x1b[32mworking\x1b[0m\nwarning\n" {
		t.Fatalf("raw output = %q", output.String())
	}
	if commandBody["command"] != "/vercel/sandbox/.zot-worker/zot" || commandBody["cwd"] != "/vercel/sandbox/openzot" || commandBody["wait"] != true || commandBody["logs"] != true {
		t.Fatalf("command body = %#v", commandBody)
	}
	if err := sandbox.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := sandbox.Destroy(context.Background()); err != nil {
		t.Fatalf("idempotent Destroy: %v", err)
	}
	if !stopped {
		t.Fatal("sandbox session was not stopped")
	}
}

func TestCreateRejectsUnsupportedOrIncompleteSpecs(t *testing.T) {
	driver := newDriver("token", "team", "project", "https://example.invalid", http.DefaultClient)
	tests := []compute.Spec{
		{},
		{Image: "image"},
		{Image: "image", Model: compute.ModelSpec{Provider: "p", Model: "m"}},
		{Image: "image", Model: compute.ModelSpec{Provider: "p", Model: "m"}, Mounts: []compute.Mount{{Source: "/local", Target: "/workspace"}}},
		{Image: "image", Model: compute.ModelSpec{Provider: "p", Model: "m"}, Source: compute.Source{Directory: "../outside"}},
	}
	for _, spec := range tests {
		if _, err := driver.Create(context.Background(), spec); err == nil {
			t.Fatalf("Create accepted invalid spec: %+v", spec)
		}
	}

	for name, driver := range map[string]*Driver{
		"token":   newDriver("", "team", "project", "https://example.invalid", http.DefaultClient),
		"team":    newDriver("token", "", "project", "https://example.invalid", http.DefaultClient),
		"project": newDriver("token", "team", "", "https://example.invalid", http.DefaultClient),
	} {
		if _, err := driver.Create(context.Background(), validSpec()); err == nil {
			t.Fatalf("Create accepted missing %s", name)
		}
	}
	invalidTimeout := New("token", "team", "project", "not-a-duration", "https://example.invalid")
	if _, err := invalidTimeout.Create(context.Background(), validSpec()); err == nil {
		t.Fatal("Create accepted an invalid timeout")
	}
	invalidURL := New("token", "team", "project", "1m", "not-an-absolute-url")
	if _, err := invalidURL.Create(context.Background(), validSpec()); err == nil {
		t.Fatal("Create accepted an invalid base URL")
	}
}

func TestCreateStopsSandboxWhenConfigurationUploadFails(t *testing.T) {
	stopped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v3/sandboxes":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"sandbox":{"name":"zotui-test"},"session":{"id":"sess_failed","cwd":"/vercel/sandbox"}}`)
		case "/v2/sandboxes/sessions/sess_failed/fs/write":
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		case "/v2/sandboxes/sessions/sess_failed/stop":
			stopped = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	driver := newDriver("token", "team", "project", server.URL, server.Client())
	_, err := driver.Create(context.Background(), compute.Spec{
		Image: "go-environment:latest", Worker: compute.Worker{Data: []byte("zot")}, Model: compute.ModelSpec{Provider: "zai", Model: "glm-5.2"},
	})
	if err == nil || !strings.Contains(err.Error(), "storage unavailable") {
		t.Fatalf("Create error = %v", err)
	}
	if !stopped {
		t.Fatal("failed sandbox was not stopped")
	}
}

func validSpec() compute.Spec {
	return compute.Spec{Image: "image", Worker: compute.Worker{Data: []byte("zot")},
		Model: compute.ModelSpec{Provider: "p", Model: "m"}}
}

func decodeJSON(t *testing.T, r io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(target); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

type archivedFile struct {
	data []byte
	mode int64
}

func readTarFiles(t *testing.T, r io.Reader) map[string]archivedFile {
	t.Helper()
	gz, err := gzip.NewReader(r)
	if err != nil {
		t.Fatalf("open gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string]archivedFile{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %q: %v", header.Name, err)
		}
		files[header.Name] = archivedFile{data: body, mode: header.Mode}
	}
}
