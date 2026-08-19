// Package web serves the zot command center and its JSON API.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/store"
)

//go:embed assets
var files embed.FS

type Server struct{ app *app.App }

// New builds the command center handler. addr is the address the server is
// configured to listen on; its host joins the Host-header allowlist so a
// deliberate non-loopback bind keeps working. extraHosts adds names the server
// answers to beyond the bind host ($ZOTUI_ALLOWED_HOSTS); the single entry "*"
// disables the Host check entirely.
func New(a *app.App, addr string, extraHosts ...string) http.Handler {
	s := &Server{app: a}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/workers", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /workers", s.appShell)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/workers", s.createWorker)
	mux.HandleFunc("PUT /api/workers/{id}", s.updateWorker)
	mux.HandleFunc("DELETE /api/workers/{id}", s.deleteWorker)
	mux.HandleFunc("POST /api/workers/{id}/runs", s.startRun)
	mux.HandleFunc("POST /api/runs/{id}/{action}", s.controlRun)
	mux.HandleFunc("GET /api/runs/{id}/output", s.runOutput)
	assets, _ := fs.Sub(files, "assets")
	static := noCache(http.FileServerFS(assets))
	mux.Handle("GET /global.css", static)
	mux.Handle("GET /components.js", static)
	mux.Handle("GET /app.js", static)
	mux.Handle("GET /vendor/", static)
	return sameSiteOnly(allowedHosts(addr, extraHosts), mux)
}

// sameSiteOnly rejects requests a browser on another site could aim at this
// listener. Binding loopback keeps network peers out but not a page the operator
// visits: DNS rebinding lets an attacker's own hostname resolve to 127.0.0.1 and
// become same-origin, after which /api/state and a POST to /api/workers/{id}/runs
// are arbitrary code execution in the configured compute. Pinning the Host header
// defeats the rebind - the browser still sends the attacker's name - and the
// Origin / Sec-Fetch-Site checks defeat a plain cross-site form post.
func sameSiteOnly(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(allowed, r.Host) {
			http.Error(w, "request host is not served by this command center", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		site := r.Header.Get("Sec-Fetch-Site")
		if site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site requests cannot change state", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !originMatchesHost(origin, r.Host) {
			http.Error(w, "cross-origin requests cannot change state", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHosts is the loopback names the default bind answers to, plus the host
// an operator explicitly configured and any extra names they allowlisted. A
// wildcard bind adds nothing - which interfaces the listener accepts from and
// which names a page may aim at it are separate questions, and the common
// wildcard bind is a container whose port is published to loopback, exactly the
// deployment DNS rebinding drives. Serving by other names takes an explicit
// entry, and the single entry "*" disables the check outright (nil allowlist).
func allowedHosts(addr string, extra []string) []string {
	allowed := []string{"localhost", "127.0.0.1", "::1"}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host = normalizeHost(host); host {
	case "", "0.0.0.0", "::":
	default:
		allowed = append(allowed, host)
	}
	for _, name := range extra {
		switch name = normalizeHost(name); name {
		case "":
		case "*":
			return nil
		default:
			allowed = append(allowed, name)
		}
	}
	return allowed
}

func hostAllowed(allowed []string, requested string) bool {
	if allowed == nil {
		return true
	}
	host, _, err := net.SplitHostPort(requested)
	if err != nil {
		host = requested
	}
	return slices.Contains(allowed, normalizeHost(host))
}

// normalizeHost folds a host name the way both sides of the allowlist need:
// DNS names are case-insensitive and IPv6 literals may or may not carry
// brackets, so entries and requests must meet in the same form.
func normalizeHost(host string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
}

func originMatchesHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}

func (s *Server) appShell(w http.ResponseWriter, _ *http.Request) {
	page, err := files.ReadFile("assets/operations/instances.html")
	if err != nil {
		http.Error(w, "command center unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// Serve runs until ctx is cancelled, then drains outstanding HTTP requests.
func Serve(ctx context.Context, addr string, handler http.Handler) error {
	httpServer := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- httpServer.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

type stateResponse struct {
	Choices app.Choices      `json:"choices"`
	Workers []workerResponse `json:"workers"`
}

type workerResponse struct {
	store.Worker
	Runs []store.Run `json:"runs"`
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	workers, err := s.app.Workers(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	choices, err := s.app.Choices(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	response := stateResponse{Choices: choices, Workers: make([]workerResponse, 0, len(workers))}
	for _, worker := range workers {
		runs, err := s.app.Runs(r.Context(), worker.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		response.Workers = append(response.Workers, workerResponse{Worker: worker, Runs: runs})
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) createWorker(w http.ResponseWriter, r *http.Request) {
	p, ok := decodeWorker(w, r)
	if !ok {
		return
	}
	id, err := s.app.CreateWorker(r.Context(), p)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) updateWorker(w http.ResponseWriter, r *http.Request) {
	p, ok := decodeWorker(w, r)
	if !ok {
		return
	}
	if err := s.app.UpdateWorker(r.Context(), r.PathValue("id"), p); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteWorker(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteWorker(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	id, err := s.app.StartRun(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) controlRun(w http.ResponseWriter, r *http.Request) {
	var err error
	switch r.PathValue("action") {
	case "pause":
		err = s.app.PauseRun(r.Context(), r.PathValue("id"))
	case "resume":
		err = s.app.ResumeRun(r.Context(), r.PathValue("id"))
	case "stop":
		err = s.app.StopRun(r.Context(), r.PathValue("id"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runOutput serves the run's output from ?offset= onward. The browser polls this
// every 1.5s, so it must ship only what is new; X-Output-Next is the offset for
// the next poll and X-Output-Start says where the returned bytes really begin,
// which differs from the request when the store has already dropped that far.
func (s *Server) runOutput(w http.ResponseWriter, r *http.Request) {
	offset := int64(0)
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "offset must be a non-negative integer"})
			return
		}
		offset = parsed
	}
	output, err := s.app.RunOutput(r.Context(), r.PathValue("id"), offset)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Output-Start", strconv.FormatInt(output.Start, 10))
	w.Header().Set("X-Output-Next", strconv.FormatInt(output.Next, 10))
	_, _ = w.Write(output.Data)
}

func decodeWorker(w http.ResponseWriter, r *http.Request) (app.WorkerParams, bool) {
	// A JSON content type is not something a cross-site HTML form can produce, so
	// insisting on it keeps a drive-by form post off the worker endpoints.
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "request must be application/json"})
		return app.WorkerParams{}, false
	}
	var p app.WorkerParams
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
		return app.WorkerParams{}, false
	}
	return p, true
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	} else if strings.Contains(err.Error(), "active run") || strings.Contains(err.Error(), "start worker") {
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		_, _ = fmt.Fprintln(w, `{"error":"encode response"}`)
	}
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
