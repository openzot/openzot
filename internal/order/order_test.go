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

// A title is a label for people. A declared one wins; without one the file name
// is already a perfectly good name, because order files are named from their
// objective. Sentence case, not Title Case - the name is a sentence.
func TestDisplayTitlePrefersTheDeclaredOneThenTheFileName(t *testing.T) {
	tests := []struct {
		name  string
		order Order
		want  string
	}{
		{
			name:  "a declared title wins",
			order: Order{Title: "Rate limiting", Path: "/book/.zot/orders/add-rate-limiting-to-the-api.yaml"},
			want:  "Rate limiting",
		},
		{
			name:  "the file name becomes one",
			order: Order{Path: "/book/.zot/orders/fix-the-flaky-test.yaml"},
			want:  "Fix the flaky test",
		},
		{
			name:  "underscores read as spaces too",
			order: Order{Path: "fix_the_flaky_test.yaml"},
			want:  "Fix the flaky test",
		},
		{
			name:  "a one-word name still capitalises",
			order: Order{Path: "cleanup.yaml"},
			want:  "Cleanup",
		},
		{
			name:  "an already-capitalised name is left alone",
			order: Order{Path: "API-cleanup.yaml"},
			want:  "API cleanup",
		},
		{
			name:  "a name that is only separators yields nothing to show",
			order: Order{Path: "---.yaml"},
			want:  "",
		},
		{
			name:  "an order that was never a file has no name to show",
			order: Order{Objective: "a synthesized order with a very long objective nobody wants as a title"},
			want:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.order.DisplayTitle(); got != test.want {
				t.Errorf("DisplayTitle = %q, want %q", got, test.want)
			}
		})
	}
}

// The title is a label for humans and never reaches the model: the objective is
// the contract, and spending context on a name for it would be paying twice for
// the same words.
func TestTheTitleStaysOutOfTheTask(t *testing.T) {
	o := Order{
		Title:      "Rate limiting",
		Objective:  "add rate limiting to the api",
		Acceptance: []string{"the suite passes"},
	}

	if strings.Contains(o.Task(), "Rate limiting") {
		t.Errorf("the title must not enter the task the agent is given:\n%s", o.Task())
	}

	if !strings.Contains(o.Task(), "add rate limiting to the api") {
		t.Errorf("the objective must still be the task:\n%s", o.Task())
	}
}

// The field is optional in both directions: an order without one parses as it
// always did, and one with a title round-trips through the file.
func TestTitleIsOptionalAndRoundTrips(t *testing.T) {
	plain, err := Parse([]byte("objective: do the thing\n"))
	if err != nil {
		t.Fatalf("an order without a title must parse: %v", err)
	}

	if plain.Title != "" {
		t.Errorf("Title = %q, want empty", plain.Title)
	}

	titled, err := Parse([]byte("title: The Thing\nobjective: do the thing\n"))
	if err != nil {
		t.Fatalf("an order with a title must parse: %v", err)
	}

	if titled.Title != "The Thing" {
		t.Errorf("Title = %q, want %q", titled.Title, "The Thing")
	}

	// through a file and back
	dir := t.TempDir()

	path, err := Scaffold(dir, titled)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("a scaffolded titled order does not load: %v", err)
	}

	if reloaded.Title != "The Thing" {
		t.Errorf("the title did not survive the file: %q", reloaded.Title)
	}
}

// An untitled scaffold advertises the field without implying it is expected -
// a stub nobody has to fill in, because the file name already names the order.
func TestAnUntitledScaffoldMentionsTheField(t *testing.T) {
	dir := t.TempDir()

	path, err := Scaffold(dir, Order{Objective: "do the thing"})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), "# title:") {
		t.Errorf("the scaffold should show the optional title field:\n%s", data)
	}

	// commented out, so it stays optional and the file still loads
	if _, err := Load(path); err != nil {
		t.Errorf("the scaffold must still load: %v", err)
	}
}

// A drafted order is named by its title, not its objective. An objective is
// however the thought arrived; slugging one gives
// the-new-command-should-have-an-interactive-versi.yaml, which is hard to tell
// from its neighbours in the directory where orders are actually browsed.
func TestScaffoldNamesTheFileByTitleWhenThereIsOne(t *testing.T) {
	dir := t.TempDir()

	titled, err := Scaffold(dir, Order{
		Title:     "Interactive zot new",
		Objective: "the new command should have an interactive version where I can continuously type new brain farts that get recorded as orders",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := filepath.Base(titled); got != "interactive-zot-new.yaml" {
		t.Errorf("file name = %q, want it named from the title", got)
	}

	// without a title the objective still names it, exactly as before
	untitled, err := Scaffold(dir, Order{Objective: "fix the flaky test"})
	if err != nil {
		t.Fatal(err)
	}

	if got := filepath.Base(untitled); got != "fix-the-flaky-test.yaml" {
		t.Errorf("file name = %q, want it named from the objective", got)
	}

	// and a name already taken is still uniquified rather than overwritten
	again, err := Scaffold(dir, Order{Title: "Interactive zot new", Objective: "something else"})
	if err != nil {
		t.Fatal(err)
	}

	if again == titled {
		t.Error("scaffolding the same title twice must not overwrite the first")
	}

	if got := filepath.Base(again); got != "interactive-zot-new-2.yaml" {
		t.Errorf("the second file is %q, want it uniquified from the title", got)
	}
}
