package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/store"
	"github.com/openzot/openzot/internal/zotui/web"
)

func TestCommandCenterAPIAndAssets(t *testing.T) {
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "web.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{
		Repos:        map[string]config.Repo{"acme": {Repositories: []string{"acme/api"}}},
		Compute:      map[string]config.Compute{"cf": {Type: "cloudflare"}},
		Providers:    map[string]config.Provider{"zai": {Models: map[string]config.Model{"glm": {Model: "glm-5.2"}}}},
		Environments: map[string]config.Environment{"go": {Compute: "cf", Provider: "zai", Model: "glm"}},
	}
	handler := web.New(app.New(cfg, st))

	response := serve(handler, http.MethodGet, "/", "")
	if response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != "/workers" {
		t.Fatalf("root route status=%d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	response.Body.Close()

	response = serve(handler, http.MethodGet, "/workers", "")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Software Factory")) || !bytes.Contains(body, []byte(`id="provider-grid"`)) || bytes.Contains(body, []byte("const outputSets")) {
		t.Fatalf("web app status=%d body=%q", response.StatusCode, body[:min(len(body), 200)])
	}
	// A released zotui binary must render without reaching a public CDN.
	if bytes.Contains(body, []byte("https://")) ||
		!bytes.Contains(body, []byte(`/vendor/fonts/fonts.css`)) ||
		!bytes.Contains(body, []byte(`/vendor/wterm/terminal.css`)) {
		t.Fatalf("web app does not use only bundled dependencies: %q", body[:min(len(body), 500)])
	}
	for _, asset := range []string{
		"/vendor/fonts/fonts.css",
		"/vendor/fonts/dotgothic16-latin-400-normal.woff2",
		"/vendor/wterm/dom/index.js",
		"/vendor/wterm/core/wasm-inline.js",
	} {
		response = serve(handler, http.MethodGet, asset, "")
		assetBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || len(assetBody) == 0 {
			t.Fatalf("bundled asset %s status=%d bytes=%d", asset, response.StatusCode, len(assetBody))
		}
	}
	response = serve(handler, http.MethodGet, "/operations/instances.html", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("mockup path remains public: status=%d", response.StatusCode)
	}
	response.Body.Close()

	payload := `{"name":"builder","repo":"acme","repository":"acme/api","environment":"go","provider":"zai","model":"glm","mission":"ship it","maxIterations":8,"schedule":{"cron":"","timezone":"UTC","runtimeMinutes":0}}`
	response = serve(handler, http.MethodPost, "/api/workers", payload)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d: %s", response.StatusCode, read(response))
	}
	var created map[string]string
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	response = serve(handler, http.MethodGet, "/api/state", "")
	stateBody := read(response)
	if response.StatusCode != http.StatusOK || !strings.Contains(stateBody, `"name":"builder"`) || !strings.Contains(stateBody, `"provider":"zai"`) || !strings.Contains(stateBody, `"repos":["acme"]`) || !strings.Contains(stateBody, `"runs":[]`) {
		t.Fatalf("state status=%d body=%s", response.StatusCode, stateBody)
	}
	var statePayload struct {
		Choices app.Choices `json:"choices"`
	}
	if err := json.Unmarshal([]byte(stateBody), &statePayload); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if got := statePayload.Choices.Repositories["acme"]; len(got) != 1 || got[0] != "acme/api" {
		t.Fatalf("repository choices did not reach the browser: %v", got)
	}
	if len(statePayload.Choices.Providers) != 1 || statePayload.Choices.Providers[0] != "zai" || len(statePayload.Choices.Models["zai"]) != 1 || statePayload.Choices.Models["zai"][0] != "glm" {
		t.Fatalf("provider model choices did not reach the browser: %+v", statePayload.Choices)
	}
	if statePayload.Choices.DefaultMaxIterations != 1_000_000 {
		t.Fatalf("worker default did not reach the browser: %d", statePayload.Choices.DefaultMaxIterations)
	}

	runID, err := st.CreateRun(context.Background(), store.Run{WorkerID: created["id"], Mission: "ship it", Model: "glm", MaxIterations: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendRunOutput(context.Background(), runID, []byte("live output")); err != nil {
		t.Fatal(err)
	}
	response = serve(handler, http.MethodPost, "/api/runs/"+runID+"/pause", "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("pause status = %d: %s", response.StatusCode, read(response))
	}
	response = serve(handler, http.MethodGet, "/api/runs/"+runID+"/output", "")
	if got := read(response); got != "live output" {
		t.Fatalf("output = %q", got)
	}
	response = serve(handler, http.MethodDelete, "/api/workers/"+created["id"], "")
	if response.StatusCode != http.StatusConflict || !strings.Contains(read(response), "active run") {
		t.Fatal("expected active run to prevent worker deletion")
	}
	response = serve(handler, http.MethodPost, "/api/runs/"+runID+"/stop", "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("stop status = %d: %s", response.StatusCode, read(response))
	}
	response.Body.Close()
	response = serve(handler, http.MethodDelete, "/api/workers/"+created["id"], "")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", response.StatusCode, read(response))
	}
	response.Body.Close()
	response = serve(handler, http.MethodGet, "/api/state", "")
	if got := read(response); strings.Contains(got, `"name":"builder"`) {
		t.Fatalf("deleted worker remains in state: %s", got)
	}
}

func TestAPIRejectsUnknownFields(t *testing.T) {
	st, err := store.Open(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "web.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	handler := web.New(app.New(&config.Config{}, st))
	response := serve(handler, http.MethodPost, "/api/workers", `{"legacyJob":true}`)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(read(response), "unknown field") {
		t.Fatal("expected strict JSON request validation")
	}
}

func serve(handler http.Handler, method, target, body string) *http.Response {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Result()
}

func read(response *http.Response) string {
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return string(body)
}
