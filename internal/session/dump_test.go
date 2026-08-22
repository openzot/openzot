package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openzot/openzot/agent"
	"github.com/openzot/openzot/internal/buildinfo"
)

// The failure dump is the artifact that makes an opaque upstream 400
// diagnosable: the exact request that provoked it, next to the session log.
// It is a developer convenience with the same boundary as .env loading - the
// request body is the whole prompt - so the test asserts whichever half
// applies to the build it is compiled into.
func TestFailureDumpIsWrittenOnlyByADevBuild(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "20260821-230000", Meta{Task: "x"})
	if err != nil {
		t.Fatal(err)
	}

	recorder := NewRecorder(writer)

	if err := recorder.RecordResult(agent.Summary{
		Reason: "error",
		Error:  "provider: Provider returned error (400)",
		Failure: &agent.Failure{
			Status:       400,
			RequestBytes: 30375,
			RequestBody:  `{"model":"stealth/ox-alpha","messages":[{"role":"user","content":"go"}]}`,
			ResponseBody: `{"error":{"message":"Provider returned error","metadata":{"raw":"ERROR"}}}`,
		},
	}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	dump := filepath.Join(dir, "20260821-230000.failure.json")

	data, statErr := os.ReadFile(dump)

	if !buildinfo.Dev {
		if statErr == nil {
			t.Fatal("a released build must not spill the request body to disk")
		}

		return
	}

	if statErr != nil {
		t.Fatalf("a developer build should have written the dump: %v", statErr)
	}

	var payload struct {
		Status       int    `json:"status"`
		RequestBody  string `json:"request_body"`
		ResponseBody string `json:"response_body"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("the dump is not valid JSON: %v", err)
	}

	if payload.Status != 400 || !strings.Contains(payload.RequestBody, "stealth/ox-alpha") || !strings.Contains(payload.ResponseBody, "ERROR") {
		t.Errorf("the dump is missing the exchange: %+v", payload)
	}
}

// The dump must be written the moment a failure occurs, not deferred to the
// run's end - a run killed mid-retry never reaches the end, and the failing
// exchange is exactly what is wanted then. RecordFailure is the incremental
// path; a dev build persists it immediately, a released build does not.
func TestFailureDumpIsWrittenIncrementally(t *testing.T) {
	dir := t.TempDir()

	writer, err := Create(dir, "20260821-231500", Meta{Task: "x"})
	if err != nil {
		t.Fatal(err)
	}

	recorder := NewRecorder(writer)

	// no result recorded - the run is still going, exactly as when a kill lands
	if err := recorder.RecordFailure(&agent.Failure{
		Status:      400,
		RequestBody: `{"model":"stealth/ox-alpha"}`,
	}); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	_, statErr := os.Stat(filepath.Join(dir, "20260821-231500.failure.json"))

	if buildinfo.Dev && statErr != nil {
		t.Errorf("a dev build must dump on the failure itself, before the run ends: %v", statErr)
	}

	if !buildinfo.Dev && statErr == nil {
		t.Error("a released build must not dump the request body")
	}
}
