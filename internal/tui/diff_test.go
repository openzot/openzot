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
	// Expect: equal(a), delete(b), insert(x), equal(c) - order of del/ins may vary
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

// The diff is what an operator reads to decide whether an unattended run did
// something reasonable, so its edges have to hold: an enormous file, a deletion
// with no replacement, an index off the end of the array.

// A pathologically large diff falls back to a wholesale replace rather than
// allocating a table nobody can afford.
func TestLineDiffFallsBackOnHugeInputs(t *testing.T) {
	old := make([]string, 2100)
	new := make([]string, 2100)

	for i := range old {
		old[i] = "old"
		new[i] = "new"
	}

	ops := lineDiff(old, new)

	if len(ops) != len(old)+len(new) {
		t.Fatalf("got %d ops, want a wholesale replace of %d", len(ops), len(old)+len(new))
	}

	var deletes, inserts int

	for _, op := range ops {
		switch op.kind {
		case diffDelete:
			deletes++
		case diffInsert:
			inserts++
		default:
			t.Errorf("the fallback must not emit equal or gap ops, got %v", op.kind)
		}
	}

	if deletes != len(old) || inserts != len(new) {
		t.Errorf("got %d deletes and %d inserts", deletes, inserts)
	}
}

func TestLineDiffHandlesEmptySides(t *testing.T) {
	if ops := lineDiff(nil, nil); len(ops) != 0 {
		t.Errorf("two empty files differ in %d ops", len(ops))
	}

	ops := lineDiff([]string{"a", "b"}, nil)

	if len(ops) != 2 {
		t.Fatalf("got %d ops, want two deletions", len(ops))
	}

	for _, op := range ops {
		if op.kind != diffDelete {
			t.Errorf("op = %v, want a deletion", op.kind)
		}
	}

	ops = lineDiff(nil, []string{"a", "b"})

	for _, op := range ops {
		if op.kind != diffInsert {
			t.Errorf("op = %v, want an insertion", op.kind)
		}
	}
}

// Trailing lines on either side must not be dropped: an edit that appends is
// the commonest thing an agent does.
func TestLineDiffKeepsTrailingLines(t *testing.T) {
	ops := lineDiff([]string{"a"}, []string{"a", "b", "c"})

	var inserted []int

	for _, op := range ops {
		if op.kind == diffInsert {
			inserted = append(inserted, op.newIdx)
		}
	}

	if len(inserted) != 2 || inserted[0] != 1 || inserted[1] != 2 {
		t.Errorf("appended lines were not reported: %v", inserted)
	}
}

func TestLineAtIsBoundsSafe(t *testing.T) {
	lines := []string{"a", "b"}

	for _, index := range []int{-1, 2, 100} {
		if got := lineAt(lines, index); got != "" {
			t.Errorf("lineAt(%d) = %q, want empty", index, got)
		}
	}

	if got := lineAt(lines, 1); got != "b" {
		t.Errorf("lineAt(1) = %q", got)
	}

	if got := lineAt(nil, 0); got != "" {
		t.Errorf("lineAt on nothing = %q", got)
	}
}

func TestClipHandlesDegenerateWidths(t *testing.T) {
	for _, width := range []int{0, -1, -100} {
		if got := clip("hello", width); got != "" {
			t.Errorf("clip(%d) = %q, want empty", width, got)
		}
	}

	if got := clip("hello", 100); got != "hello" {
		t.Errorf("clip of a short string = %q", got)
	}
}
