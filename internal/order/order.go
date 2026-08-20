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
	"strings"

	"gopkg.in/yaml.v3"
)

// Order is one work order: a single run's brief.
type Order struct {
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

	base := slug(o.Objective)

	path := filepath.Join(dir, base+".yaml")
	for n := 2; exists(path); n++ {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.yaml", base, n))
	}

	if err := os.WriteFile(path, []byte(template(o)), 0o644); err != nil {
		return "", fmt.Errorf("write order: %w", err)
	}

	return path, nil
}

// template renders the scaffold. The objective is a literal block scalar, which
// carries any text without quoting rules getting a say; filled lists are
// rendered by the YAML encoder for the same reason.
func template(o Order) string {
	var b strings.Builder

	b.WriteString("# zot work order - what to do, and what \"done\" means.\n\n")

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
