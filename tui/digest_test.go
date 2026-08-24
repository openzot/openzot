package tui

import (
	"strings"
	"testing"

	"github.com/openzot/openzot/agent"
)

func TestRenderDigestIsColumnarAndParsable(t *testing.T) {
	out := RenderDigest(Digest{
		Status:       "done",
		Session:      "20260824-143210",
		Resume:       "rook --resume 20260824-143210",
		Iterations:   42,
		Calls:        137,
		InputTokens:  1234567,
		OutputTokens: 84210,
		Message:      "Found 3 auth bypasses.",
	})

	// Every non-empty line must parse as (key, value) by splitting on the first
	// run of spaces - the whole point of the format.
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		key, value, found := strings.Cut(strings.TrimRight(line, " "), " ")
		if !found {
			t.Fatalf("line %q is not a key/value pair", line)
		}
		got[key] = strings.TrimSpace(value)
	}

	want := map[string]string{
		"status":        "done",
		"session":       "20260824-143210",
		"resume":        "rook --resume 20260824-143210",
		"iterations":    "42",
		"calls":         "137",
		"input-tokens":  "1234567",
		"output-tokens": "84210",
		"message":       "Found 3 auth bypasses.",
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRenderDigestOmitsEmptyFields(t *testing.T) {
	out := RenderDigest(Digest{Status: "done", Iterations: 1, Calls: 1})

	for _, absent := range []string{"session", "resume", "message"} {
		if strings.Contains(out, absent) {
			t.Errorf("a run with no %s must not render that row:\n%s", absent, out)
		}
	}
}

func TestRenderDigestFlattensMultilineMessage(t *testing.T) {
	out := RenderDigest(Digest{Status: "done", Message: "line one\nline two"})

	// The one-row-per-line contract must hold even for a multi-line message.
	if strings.Contains(out, "line one\nline two") {
		t.Error("a multi-line message must be flattened to one line")
	}
	if !strings.Contains(out, "line one line two") {
		t.Errorf("the message must survive flattening:\n%s", out)
	}
}

func TestDigestStatus(t *testing.T) {
	cases := []struct {
		reason string
		code   int
		want   string
	}{
		{agent.ReasonSettled, 0, "done"},
		{agent.ReasonFailed, 1, "failed"},
		{agent.ReasonAborted, 1, "cancelled"},
		{agent.ReasonIterations, 3, "failed"},
	}

	for _, c := range cases {
		if got := DigestStatus(c.reason, c.code); got != c.want {
			t.Errorf("DigestStatus(%q, %d) = %q, want %q", c.reason, c.code, got, c.want)
		}
	}
}
