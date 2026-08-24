package tui

import (
	"fmt"
	"strings"

	"github.com/openzot/openzot/agent"
)

// DigestStatus maps a run's stop reason and exit code to the one human word a
// digest shows: "done", "failed", or "cancelled". Shared so every embedding
// tool classifies an ending the same way rather than each inventing its own.
func DigestStatus(reason string, code int) string {
	switch reason {
	case agent.ReasonAborted:
		return "cancelled"
	case agent.ReasonFailed:
		return "failed"
	}

	if code != 0 {
		return "failed"
	}

	return "done"
}

// Digest is the compact end-of-run report printed after a run concludes.
//
// It exists because the two places a run's ending shows up are both poor
// records: the full-screen viewer runs in the alternate screen and takes its
// stats with it when it restores the terminal, and the streaming renderer ends
// on a prose line with no numbers at all. Neither tells an operator the one
// thing they most need afterwards - the session to resume. The digest is a
// small, fixed block that survives on the main screen and carries it.
type Digest struct {
	// Status is the human-readable ending: "done", "failed", "cancelled".
	Status string

	// Session is the id of the recorded session, empty when none was written.
	Session string

	// Resume is the exact command that continues this session - "rook --resume
	// <id>" - so the operator copies a line rather than assembling one. Empty
	// when the run is not resumable (no session recorded).
	Resume string

	// Iterations and Calls are the run's agentic rounds and total tool calls.
	Iterations int
	Calls      int

	// InputTokens and OutputTokens are the provider-billed totals for the run.
	InputTokens  int
	OutputTokens int

	// Message is the prose the ending carried - a success summary or a failure
	// reason. Rendered last, and only when present.
	Message string
}

// RenderDigest formats a Digest as an aligned two-column block.
//
// The shape is deliberately the simplest thing that is both readable and
// trivial to parse: one row per line, a single-word key, then the value as the
// rest of the line. A consumer splits each line on its first run of spaces -
// key left, value right - with no quoting or escaping to handle, because every
// key is one token and every value is free to contain spaces. No borders, no
// ANSI: a block that survives being piped through `grep` or `awk` unharmed.
//
// Empty fields are omitted rather than shown blank, so a run with no session
// simply has no session/resume rows.
func RenderDigest(d Digest) string {
	type row struct {
		key   string
		value string
	}

	var rows []row

	if d.Status != "" {
		rows = append(rows, row{"status", d.Status})
	}

	if d.Session != "" {
		rows = append(rows, row{"session", d.Session})
	}

	if d.Resume != "" {
		rows = append(rows, row{"resume", d.Resume})
	}

	rows = append(rows,
		row{"iterations", fmt.Sprintf("%d", d.Iterations)},
		row{"calls", fmt.Sprintf("%d", d.Calls)},
		row{"input-tokens", fmt.Sprintf("%d", d.InputTokens)},
		row{"output-tokens", fmt.Sprintf("%d", d.OutputTokens)},
	)

	if m := strings.TrimSpace(d.Message); m != "" {
		// A multi-line message would break the one-row-per-line contract, so it
		// is flattened to a single line - the digest is a pointer to the full
		// record, not the record itself.
		rows = append(rows, row{"message", strings.Join(strings.Fields(m), " ")})
	}

	width := 0
	for _, r := range rows {
		if len(r.key) > width {
			width = len(r.key)
		}
	}

	var b strings.Builder

	for _, r := range rows {
		fmt.Fprintf(&b, "%-*s  %s\n", width, r.key, r.value)
	}

	return b.String()
}
