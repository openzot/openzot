// Package order defines the work order: the document a zot run is dispatched
// from.
//
// zot deliberately takes no prose on the command line. A factory accepts a work
// order, not a conversation - a durable objective, the acceptance criteria that
// define "done", and the constraints the work must hold to. The order is a file
// so it outlives the invocation: it can be edited, committed, re-run, and later
// judged against.
//
// An order is advisory input - what to do - and may therefore live anywhere,
// including the repository being worked on. How the result is judged (quality
// gates) is deliberately not part of the order schema: adjudication belongs to
// the operator's configuration, never to a document the agent can write.
package order

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The book's layout. A project's orders and the ledger of what has been run
// from them live together under one dotted directory at its root, the way every
// other tool that keeps state in a repository does it: .zot/orders/<slug>.yaml
// and .zot/records/<slug>/<run>.yaml. Two top-level orders/ and records/
// directories claimed generic names in the root of somebody else's project,
// which is not zot's to take.
//
// Only the defaults live here. An order may be read from anywhere, and the
// ledger root is configurable - see Ledger - so this names the convention
// rather than enforcing it.
const (
	// BookDir is the per-project directory holding both.
	BookDir = ".zot"

	ordersName  = "orders"
	recordsName = "records"
)

// OrdersDir is where new orders for the project rooted at dir are scaffolded.
func OrdersDir(dir string) string { return filepath.Join(dir, BookDir, ordersName) }

// RecordsDir is the default ledger root for the project rooted at dir.
func RecordsDir(dir string) string { return filepath.Join(dir, BookDir, recordsName) }

// Order is one work order: a single run's brief.
type Order struct {
	// Title is an optional short label for the order, for people rather than
	// for the agent. It never reaches the model - see Task - because the
	// objective is the contract and a title is only how a human recognises it
	// in a list or a viewer.
	Title string `yaml:"title,omitempty"`

	// Objective is the durable goal of the run. It goes into the system prompt
	// and survives compaction, so the agent cannot forget it on a long run.
	Objective string `yaml:"objective"`

	// Acceptance are the criteria that define "done". They travel with the
	// objective into the system prompt, and they are the contract a future
	// verification gate judges the result against.
	Acceptance []string `yaml:"acceptance,omitempty"`

	// Constraints are rules the work must hold to throughout - boundaries, not
	// goals.
	Constraints []string `yaml:"constraints,omitempty"`

	// Path is where the order was loaded from, for reporting. Empty for an
	// order that never was a file (a synthesized one).
	Path string `yaml:"-"`
}

// List returns the order files directly inside dir, in filename order - the
// batch a bare `zot` runs. Only the top level is listed, matching the shell
// glob the invocation is named after, and a missing directory is an empty
// listing rather than an error: a project with no book yet simply has no
// outstanding work.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("read orders: %w", err)
	}

	var paths []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".yaml") {
			continue
		}

		paths = append(paths, filepath.Join(dir, entry.Name()))
	}

	sort.Strings(paths)

	return paths, nil
}

// Load reads and parses one order file.
func Load(path string) (Order, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Order{}, fmt.Errorf("read order: %w", err)
	}

	order, err := Parse(data)
	if err != nil {
		return Order{}, fmt.Errorf("order %s: %w", path, err)
	}

	order.Path = path

	return order, nil
}

// Parse decodes order YAML. Unknown fields are rejected - a typo like
// "acceptence:" must fail loudly rather than silently dropping the criteria the
// operator thought they set.
func Parse(data []byte) (Order, error) {
	var order Order

	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)

	if err := decoder.Decode(&order); err != nil {
		return Order{}, fmt.Errorf("parse: %w", err)
	}

	order.Title = strings.TrimSpace(order.Title)
	order.Objective = strings.TrimSpace(order.Objective)
	order.Acceptance = cleanList(order.Acceptance)
	order.Constraints = cleanList(order.Constraints)

	if order.Objective == "" {
		return Order{}, fmt.Errorf("no objective")
	}

	return order, nil
}

// FromText builds an order from free text. Text that already is a valid order
// document is used as one - this is what lets a dispatcher (zotui) carry either
// a plain mission or a full order in the same field - and anything else becomes
// the objective of a minimal order.
func FromText(text string) Order {
	if order, err := Parse([]byte(text)); err == nil {
		return order
	}

	return Order{Objective: strings.TrimSpace(text)}
}

// Encode renders the order back to YAML, for handing to another process.
func (o Order) Encode() string {
	// Order is plain strings and slices, which Marshal cannot fail on.
	data, _ := yaml.Marshal(o)

	return string(data)
}

// DisplayTitle is what to call this order on screen.
//
// A declared title wins. Failing that the file name is one: order files are
// named from their objective already, so fix-the-flaky-test.yaml is a
// perfectly good "Fix the flaky test" and deriving it costs the operator
// nothing. An order that is neither titled nor a file - one synthesized in
// memory by a dispatcher - has no name to show, and gets none: inventing a
// label from the objective would put a truncated sentence where a title goes,
// which is the thing having titles is meant to stop.
func (o Order) DisplayTitle() string {
	if o.Title != "" {
		return o.Title
	}

	if o.Path == "" {
		return ""
	}

	return titleFromFilename(o.Path)
}

// titleFromFilename turns an order's file name into a label: dashes and
// underscores become spaces, and the first word is capitalised. Sentence case
// rather than Title Case, because an objective-derived name is a sentence -
// "Fix The Flaky Test" reads like a headline for something that is not one.
func titleFromFilename(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))

	name = strings.Map(func(r rune) rune {
		if r == '-' || r == '_' {
			return ' '
		}

		return r
	}, name)

	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return ""
	}

	first, size := utf8.DecodeRuneInString(name)

	return string(unicode.ToUpper(first)) + name[size:]
}

// Task renders the order as the durable objective text placed in the system
// prompt: the objective, then the acceptance criteria and constraints as the
// terms the agent works - and is judged - against.
func (o Order) Task() string {
	var b strings.Builder

	b.WriteString(o.Objective)

	if len(o.Acceptance) > 0 {
		b.WriteString("\n\nAcceptance criteria - the objective is not met until every one of these holds:")

		for i, criterion := range o.Acceptance {
			fmt.Fprintf(&b, "\n%d. %s", i+1, criterion)
		}
	}

	if len(o.Constraints) > 0 {
		b.WriteString("\n\nConstraints - these hold for the whole run:")

		for _, constraint := range o.Constraints {
			b.WriteString("\n- " + constraint)
		}
	}

	return b.String()
}

// Scaffold writes the order as a new file under dir, creating the directory if
// needed, and returns its path. Sections the order does not fill are written as
// commented stubs that invite editing; an empty objective scaffolds the blank
// form, which will not run until it is filled in. An existing file is never
// overwritten; the name is uniquified instead, because scaffolding the same
// objective twice is routine, not an error.
func Scaffold(dir string, o Order) (string, error) {
	o.Objective = strings.TrimSpace(o.Objective)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create order directory: %w", err)
	}

	path := filepath.Join(dir, nameFor(o)+".yaml")
	for n := 2; exists(path); n++ {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.yaml", nameFor(o), n))
	}

	if err := os.WriteFile(path, []byte(template(o)), 0o644); err != nil {
		return "", fmt.Errorf("write order: %w", err)
	}

	return path, nil
}

// nameFor picks the file name for an order: its title when it has one, its
// objective otherwise.
//
// A title is a few deliberate words naming the change; an objective is however
// the thought arrived, and slugging one gives
// the-new-command-should-have-an-interactive-versi.yaml - a name that is hard
// to tell apart from its neighbours in a directory listing, which is where
// orders are actually browsed. The drafting survey proposes a title precisely
// so the file can be found by it later.
func nameFor(o Order) string {
	if o.Title != "" {
		return slug(o.Title)
	}

	return slug(o.Objective)
}

// template renders the scaffold. The objective is a literal block scalar, which
// carries any text without quoting rules getting a say; filled lists are
// rendered by the YAML encoder for the same reason.
func template(o Order) string {
	var b strings.Builder

	b.WriteString("# zot work order - what to do, and what \"done\" means.\n\n")

	// The title is optional and the file name stands in for it, so an untitled
	// order gets a commented stub: discoverable without implying the field is
	// expected.
	if o.Title != "" {
		b.WriteString(encodeScalar("title", o.Title))
	} else {
		b.WriteString("# An optional short label for this order, shown in the viewer. Without\n" +
			"# one the file name is used, with its dashes read as spaces.\n" +
			"# title:\n")
	}

	b.WriteString("\n")

	if o.Objective == "" {
		b.WriteString("# The durable goal of the run. The order will not run until this is filled in.\nobjective:\n")
	} else {
		b.WriteString("objective: |-\n")

		for _, line := range strings.Split(o.Objective, "\n") {
			if line == "" {
				b.WriteString("\n")

				continue
			}

			b.WriteString("  " + line + "\n")
		}
	}

	b.WriteString("\n# The objective is not met until every one of these holds.\n")

	if len(o.Acceptance) > 0 {
		b.WriteString(encodeList("acceptance", o.Acceptance))
	} else {
		b.WriteString(`# acceptance:
#   - the new behaviour is covered by a test that fails without the change
#   - the full test suite passes
`)
	}

	b.WriteString("\n# Rules that hold for the whole run.\n")

	if len(o.Constraints) > 0 {
		b.WriteString(encodeList("constraints", o.Constraints))
	} else {
		b.WriteString(`# constraints:
#   - do not change public API signatures
`)
	}

	return b.String()
}

// encodeScalar renders one named scalar as YAML, so any title text is quoted
// correctly rather than by hand.
func encodeScalar(name, value string) string {
	// a map of plain strings, which Marshal cannot fail on
	data, _ := yaml.Marshal(map[string]string{name: value})

	return string(data)
}

// encodeList renders one named list as YAML.
func encodeList(name string, items []string) string {
	// a map of plain strings, which Marshal cannot fail on
	data, _ := yaml.Marshal(map[string][]string{name: items})

	return string(data)
}

// slug turns an objective into a filename: lower-case words joined by dashes,
// bounded so a paragraph-long objective still names a manageable file.
func slug(objective string) string {
	const maxLen = 48

	var b strings.Builder

	dash := false

	for _, r := range strings.ToLower(objective) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}

			dash = false

			b.WriteRune(r)
		default:
			dash = true
		}

		if b.Len() >= maxLen {
			break
		}
	}

	if b.Len() == 0 {
		return "order"
	}

	return strings.TrimSuffix(b.String(), "-")
}

// cleanList trims entries and drops empty ones, so a stray "- " in the YAML
// does not become an empty criterion the agent is asked to satisfy.
func cleanList(items []string) []string {
	var out []string

	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}

	return out
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
