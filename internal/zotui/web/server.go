// Package web serves the zot command center and its JSON API.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/store"
)

//go:embed assets
var files embed.FS

type Server struct{ app *app.App }

func New(a *app.App) http.Handler {
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
	return mux
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
	response := stateResponse{Choices: s.app.Choices(), Workers: make([]workerResponse, 0, len(workers))}
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

func (s *Server) runOutput(w http.ResponseWriter, r *http.Request) {
	output, err := s.app.RunOutput(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(output))
}

func decodeWorker(w http.ResponseWriter, r *http.Request) (app.WorkerParams, bool) {
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
