package order

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsAFullOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order.yaml")

	write(t, path, `
objective: |-
  add rate limiting to the API
acceptance:
  - "requests beyond the limit receive 429"
  - "  the suite passes  "
  - ""
constraints:
  - do not change handler signatures
`)

	order, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if order.Objective != "add rate limiting to the API" {
		t.Errorf("Objective = %q", order.Objective)
	}

	// entries are trimmed and empties dropped, so a stray "- " in the YAML does
	// not become a criterion the agent is asked to satisfy
	if len(order.Acceptance) != 2 || order.Acceptance[1] != "the suite passes" {
		t.Errorf("Acceptance = %q", order.Acceptance)
	}

	if len(order.Constraints) != 1 {
		t.Errorf("Constraints = %q", order.Constraints)
	}

	if order.Path != path {
		t.Errorf("Path = %q, want the file it came from", order.Path)
	}
}

func TestLoadErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	empty := filepath.Join(t.TempDir(), "empty.yaml")
	write(t, empty, "objective: '  '\n")

	// a typo must fail loudly rather than silently dropping what the operator
	// thought they set
	typo := filepath.Join(t.TempDir(), "typo.yaml")
	write(t, typo, "objective: x\nacceptence:\n  - y\n")

	prose := filepath.Join(t.TempDir(), "prose.yaml")
	write(t, prose, "fix the bug in the parser\n")

	for name, path := range map[string]string{
		"a missing file":      missing,
		"an empty objective":  empty,
		"an unknown field":    typo,
		"prose, not an order": prose,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(path); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestFromTextWrapsProse(t *testing.T) {
	order := FromText("  fix the flaky test  ")

	if order.Objective != "fix the flaky test" {
		t.Errorf("Objective = %q", order.Objective)
	}

	if len(order.Acceptance) != 0 || len(order.Constraints) != 0 {
		t.Errorf("a wrapped mission invented criteria: %+v", order)
	}
}

func TestFromTextAcceptsAFullOrder(t *testing.T) {
	order := FromText("objective: build the parser\nacceptance:\n  - it parses\n")

	if order.Objective != "build the parser" {
		t.Errorf("Objective = %q", order.Objective)
	}

	if len(order.Acceptance) != 1 || order.Acceptance[0] != "it parses" {
		t.Errorf("Acceptance = %q, want the document's own criteria", order.Acceptance)
	}
}

// Encode must round-trip through Parse: it is how an order travels to another
// process (the zotui sandbox), and an encoding Parse rejects would strand it.
func TestEncodeRoundTrips(t *testing.T) {
	original := Order{
		Objective:   "line one\nline two: with a colon",
		Acceptance:  []string{"a: tricky { criterion }", "plain"},
		Constraints: []string{"# not a comment"},
	}

	decoded, err := Parse([]byte(original.Encode()))
	if err != nil {
		t.Fatalf("Parse(Encode()): %v", err)
	}

	decoded.Path = original.Path

	if decoded.Objective != original.Objective {
		t.Errorf("Objective = %q, want %q", decoded.Objective, original.Objective)
	}

	if len(decoded.Acceptance) != 2 || decoded.Acceptance[0] != original.Acceptance[0] {
		t.Errorf("Acceptance = %q", decoded.Acceptance)
	}

	if len(decoded.Constraints) != 1 || decoded.Constraints[0] != original.Constraints[0] {
		t.Errorf("Constraints = %q", decoded.Constraints)
	}
}

func TestTaskRendersTheWholeContract(t *testing.T) {
	order := Order{
		Objective:   "add rate limiting",
		Acceptance:  []string{"429 beyond the limit", "suite passes"},
		Constraints: []string{"no new dependencies"},
	}

	task := order.Task()

	for _, want := range []string{
		"add rate limiting",
		"Acceptance criteria",
		"1. 429 beyond the limit",
		"2. suite passes",
		"Constraints",
		"- no new dependencies",
	} {
		if !strings.Contains(task, want) {
			t.Errorf("Task() is missing %q:\n%s", want, task)
		}
	}
}

func TestTaskOfABareObjectiveIsJustTheObjective(t *testing.T) {
	task := Order{Objective: "fix the typo"}.Task()

	if task != "fix the typo" {
		t.Errorf("Task() = %q - empty sections must not render headings", task)
	}
}

// The scaffold's whole job is to produce a file zot itself will accept.
func TestScaffoldWritesALoadableOrder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "orders")

	path, err := Scaffold(dir, Order{Objective: "Fix the typo in README!"})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	if filepath.Base(path) != "fix-the-typo-in-readme.yaml" {
		t.Errorf("path = %q", path)
	}

	order, err := Load(path)
	if err != nil {
		t.Fatalf("the scaffold does not load: %v", err)
	}

	if order.Objective != "Fix the typo in README!" {
		t.Errorf("Objective = %q", order.Objective)
	}

	// the stub sections are comments: present to invite editing, absent from
	// the parsed order
	if len(order.Acceptance) != 0 || len(order.Constraints) != 0 {
		t.Errorf("the scaffold's commented stubs leaked into the order: %+v", order)
	}
}

func TestScaffoldCarriesAMultilineObjective(t *testing.T) {
	dir := t.TempDir()

	path, err := Scaffold(dir, Order{Objective: "first line\n\nthird line: with a colon"})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	order, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if order.Objective != "first line\n\nthird line: with a colon" {
		t.Errorf("Objective = %q", order.Objective)
	}
}

// Scaffolding the same objective twice is routine, not an error.
func TestScaffoldUniquifiesRatherThanOverwrites(t *testing.T) {
	dir := t.TempDir()

	first, err := Scaffold(dir, Order{Objective: "fix the bug"})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	second, err := Scaffold(dir, Order{Objective: "fix the bug"})
	if err != nil {
		t.Fatalf("Scaffold again: %v", err)
	}

	if first == second {
		t.Fatalf("both scaffolds wrote %s", first)
	}

	if filepath.Base(second) != "fix-the-bug-2.yaml" {
		t.Errorf("second = %q", second)
	}
}

// A filled order - a drafted one - scaffolds with its sections as real YAML,
// not commented stubs, and the file round-trips through Load.
func TestScaffoldRendersFilledSections(t *testing.T) {
	path, err := Scaffold(t.TempDir(), Order{
		Objective:   "add rate limiting",
		Acceptance:  []string{"429 beyond the limit", "the: suite { passes }"},
		Constraints: []string{"no new dependencies"},
	})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("the drafted scaffold does not load: %v", err)
	}

	if len(loaded.Acceptance) != 2 || loaded.Acceptance[1] != "the: suite { passes }" {
		t.Errorf("Acceptance = %q", loaded.Acceptance)
	}

	if len(loaded.Constraints) != 1 || loaded.Constraints[0] != "no new dependencies" {
		t.Errorf("Constraints = %q", loaded.Constraints)
	}
}

// An empty objective scaffolds the blank form - which must refuse to run until
// it is filled in, or a forgotten edit becomes a run with no goal.
func TestScaffoldBlankForm(t *testing.T) {
	path, err := Scaffold(t.TempDir(), Order{})
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	if filepath.Base(path) != "order.yaml" {
		t.Errorf("path = %q", path)
	}

	if _, err := Load(path); err == nil {
		t.Error("the unedited blank form must not load")
	}
}

func TestScaffoldErrors(t *testing.T) {
	// a file where the directory should go
	blocked := filepath.Join(t.TempDir(), "orders")
	write(t, blocked, "not a directory")

	if _, err := Scaffold(filepath.Join(blocked, "sub"), Order{Objective: "x"}); err == nil {
		t.Error("an uncreatable directory must be an error")
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Fix the bug", "fix-the-bug"},
		{"añadir límites!!", "a-adir-l-mites"},
		{"...", "order"},
		{strings.Repeat("very long objective ", 10), "very-long-objective-very-long-objective-very-lon"},
	}

	for _, test := range tests {
		if got := slug(test.in); got != test.want {
			t.Errorf("slug(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
