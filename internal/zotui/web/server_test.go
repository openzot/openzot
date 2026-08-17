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
		Models:       map[string]config.Model{"glm": {Provider: "zai", Model: "glm-5.2"}},
		Environments: map[string]config.Environment{"go": {Compute: "cf", Model: "glm"}},
	}
	handler := web.New(app.New(cfg, st))

	response := serve(handler, http.MethodGet, "/operations/instances.html", "")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("Software Factory")) || bytes.Contains(body, []byte("const outputSets")) {
		t.Fatalf("web app status=%d body=%q", response.StatusCode, body[:min(len(body), 200)])
	}

	payload := `{"name":"builder","repo":"acme","repository":"acme/api","environment":"go","mission":"ship it","maxIterations":8,"schedule":{"cron":"","timezone":"UTC","runtimeMinutes":0}}`
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
	if response.StatusCode != http.StatusOK || !strings.Contains(stateBody, `"name":"builder"`) || !strings.Contains(stateBody, `"repos":["acme"]`) || !strings.Contains(stateBody, `"runs":[]`) {
		t.Fatalf("state status=%d body=%s", response.StatusCode, stateBody)
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
