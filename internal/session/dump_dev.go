//go:build dev

package session

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/openzot/openzot/agent"
)

// dumpFailure writes the refused exchange next to the session log, on a
// developer build only. Named `<session>.failure.json`, it carries the request
// that provoked an opaque upstream error and the response that came back - the
// artifact that turns "Provider returned error (400)" from a shrug into
// something answerable. Best-effort: a dump that cannot be written must never
// disturb the run it is diagnosing.
func (r *Recorder) dumpFailure(failure *agent.Failure) {
	if failure == nil || (failure.RequestBody == "" && failure.ResponseBody == "") {
		return
	}

	path := strings.TrimSuffix(r.writer.Path(), ".jsonl") + ".failure.json"

	payload := struct {
		Status       int    `json:"status"`
		RequestBytes int    `json:"request_bytes"`
		RequestBody  string `json:"request_body"`
		ResponseBody string `json:"response_body"`
	}{
		Status:       failure.Status,
		RequestBytes: failure.RequestBytes,
		RequestBody:  failure.RequestBody,
		ResponseBody: failure.ResponseBody,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}

	_ = os.WriteFile(path, data, 0o600)
}
