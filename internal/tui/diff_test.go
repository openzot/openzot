package tui

import (
	"strings"
	"testing"
)

func TestRenderDiffEdit(t *testing.T) {
	old := "func routes() {\n\tmux.Handle(\"/\", index)\n}\n"
	neu := "func routes() {\n\tmux.Handle(\"/\", index)\n\tmux.Handle(\"/health\", health)\n}\n"

	out := renderDiff("server.go", old, neu, 70)
	if out == "" {
		t.Fatal("expected a diff panel, got empty string")
	}
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╰") {
		t.Error("expected a rounded border around the diff panel")
	}
	if !strings.Contains(out, "server.go") {
		t.Error("expected the file path in the panel title")
	}
	// The added line should be present somewhere in the rendered output.
	if !strings.Contains(out, "health") {
		t.Error("expected the added line content to be shown")
	}
}

func TestRenderDiffNoChangeOrTooNarrow(t *testing.T) {
	if out := renderDiff("a.go", "x\n", "x\n", 70); out != "" {
		t.Error("expected empty output when old == new")
	}
	if out := renderDiff("a.go", "x\n", "y\n", 8); out != "" {
		t.Error("expected empty output when width is too narrow")
	}
}

func TestLineDiff(t *testing.T) {
	ops := lineDiff([]string{"a", "b", "c"}, []string{"a", "x", "c"})
	var kinds []int
	for _, o := range ops {
		kinds = append(kinds, o.kind)
	}
	// Expect: equal(a), delete(b), insert(x), equal(c) — order of del/ins may vary
	// but there must be exactly one delete and one insert and two equals.
	var eq, del, ins int
	for _, k := range kinds {
		switch k {
		case diffEqual:
			eq++
		case diffDelete:
			del++
		case diffInsert:
			ins++
		}
	}
	if eq != 2 || del != 1 || ins != 1 {
		t.Errorf("got eq=%d del=%d ins=%d, want eq=2 del=1 ins=1", eq, del, ins)
	}
}
