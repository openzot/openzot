package tui

import (
	"strings"
	"testing"
)

func TestPlainDiffNoANSI(t *testing.T) {
	out := plainDiff("edit", map[string]interface{}{
		"path":      "x.go",
		"oldString": "a := 1\n",
		"newString": "a := 2\n",
	})
	if out == "" {
		t.Fatal("expected plain diff output")
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain diff must not contain ANSI escape codes")
	}
	for _, want := range []string{"--- x.go", "- a := 1", "+ a := 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain diff missing %q in:\n%s", want, out)
		}
	}
}

func TestIsInteractiveUnderTest(t *testing.T) {
	// `go test` pipes stdout, so the detector should report non-interactive and
	// zot would pick plain mode.
	if isInteractive() {
		t.Skip("stdout is a terminal in this environment")
	}
}
