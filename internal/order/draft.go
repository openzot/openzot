package order

import (
	"fmt"
	"strings"
)

// DraftInstructions is the system prompt for a draft run: a small, read-only
// agentic run whose deliverable is the proposed acceptance criteria and
// constraints for an objective, grounded in the actual working tree rather
// than guessed from the objective alone. The run ends - as every run does - by
// calling the success tool; here its summary IS the draft.
//
// The summary's format is deliberately NOT YAML: a natural criterion quotes
// commands and filenames (`"go test ./..." exits 0`), which is prose to a YAML
// parser, and real drafts died on exactly that. Plain bulleted lines have no
// quoting rules to violate; the scaffold re-encodes them as proper YAML.
func DraftInstructions(objective string) string {
	return `You are drafting a work order for a fully autonomous software agent. The operator gave this objective:

` + objective + `

Explore the working directory first - the build files, the test setup, the project layout - so the criteria you propose name the project's real commands and files, not guesses. You have only "read" and "list": this is a survey, not the work itself. Do not attempt the objective.

Then end the run by calling "success" with a summary in exactly this shape:

acceptance:
- <criterion>
- <criterion>
constraints:
- <constraint>

Rules:
- One item per line after "- ". Each line is read verbatim - quote commands and filenames freely, no escaping is needed.
- Each acceptance criterion must be observable: checkable by running a command or reading the working tree, never a judgement of taste.
- Give 3 to 6 acceptance criteria.
- List only constraints that genuinely bound the work; an empty list is fine.`
}

// ParseDraft turns a draft run's deliverable into the full order for the
// operator to review. The model only ever drafts - the operator's edit of the
// scaffolded file is what makes the criteria a contract.
//
// The deliverable is read as plain lines - section headings and dash bullets -
// so a criterion may contain anything at all. Prose around the lists is
// ignored rather than fatal: the lists are what was asked for, and a model's
// pleasantries should not kill a draft that contains them.
func ParseDraft(objective, reply string) (Order, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return Order{}, fmt.Errorf("no objective to draft from")
	}

	var acceptance, constraints []string
	var current *[]string

	for _, line := range strings.Split(stripFences(reply), "\n") {
		line = strings.TrimSpace(line)

		switch {
		case strings.EqualFold(line, "acceptance:") || strings.EqualFold(line, "acceptance"):
			current = &acceptance

		case strings.EqualFold(line, "constraints:") || strings.EqualFold(line, "constraints"):
			current = &constraints

		case current != nil && (strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")):
			if item := unquote(strings.TrimSpace(line[2:])); item != "" {
				*current = append(*current, item)
			}
		}
	}

	if len(acceptance) == 0 {
		// the excerpt is what makes this failure debuggable: without it "no
		// criteria" reads as zot failing, not the model
		return Order{}, fmt.Errorf("draft: the model proposed no acceptance criteria (got %q)", excerpt(reply))
	}

	return Order{
		Objective:   objective,
		Acceptance:  acceptance,
		Constraints: constraints,
	}, nil
}

// unquote strips a quote pair wrapping a whole item - a model that was told
// lines are verbatim may still wrap them - and leaves any interior quoting
// alone: `"go test ./..." exits 0` starts with a quote but is not wrapped.
func unquote(item string) string {
	if len(item) < 2 {
		return item
	}

	first := item[0]
	if first != '"' && first != '\'' {
		return item
	}

	if item[len(item)-1] != first || strings.ContainsRune(item[1:len(item)-1], rune(first)) {
		return item
	}

	return strings.TrimSpace(item[1 : len(item)-1])
}

// excerpt bounds a reply for an error message.
func excerpt(reply string) string {
	const limit = 120

	if len(reply) > limit {
		return reply[:limit] + "…"
	}

	return reply
}

// stripFences tolerates a reply wrapped in a Markdown code fence, which models
// add despite being told not to.
func stripFences(reply string) string {
	reply = strings.TrimSpace(reply)

	if !strings.HasPrefix(reply, "```") {
		return reply
	}

	reply = strings.TrimPrefix(reply, "```")

	// drop the info string ("yaml") on the opening fence's line
	if i := strings.IndexByte(reply, '\n'); i >= 0 {
		reply = reply[i+1:]
	}

	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(reply), "```"))
}
