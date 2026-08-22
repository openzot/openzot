package order

import (
	"strings"
	"testing"
)

func TestParseDraftFillsTheOrder(t *testing.T) {
	order, err := ParseDraft("  add rate limiting  ",
		"acceptance:\n  - the suite passes\n  - 429 beyond the limit\nconstraints:\n  - no new dependencies\n")
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}

	if order.Objective != "add rate limiting" {
		t.Errorf("Objective = %q", order.Objective)
	}

	if len(order.Acceptance) != 2 || order.Acceptance[1] != "429 beyond the limit" {
		t.Errorf("Acceptance = %q", order.Acceptance)
	}

	if len(order.Constraints) != 1 {
		t.Errorf("Constraints = %q", order.Constraints)
	}
}

// Models preface the document with prose despite being told not to; the
// document is what was asked for, so it is anchored rather than the whole
// draft failing over a pleasantry.
func TestParseDraftToleratesLeadingProse(t *testing.T) {
	order, err := ParseDraft("do it",
		"Here is the draft, based on the Makefile:\n\nacceptance:\n  - make test passes\nconstraints:\n  - no new dependencies\n")
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}

	if len(order.Acceptance) != 1 || order.Acceptance[0] != "make test passes" {
		t.Errorf("Acceptance = %q", order.Acceptance)
	}
}

// The regression that motivated the line-based format: a natural criterion
// quotes the command it names, which is prose to a YAML parser. Every
// character of the line must come through verbatim.
func TestParseDraftKeepsQuotedCommandsVerbatim(t *testing.T) {
	order, err := ParseDraft("check token counting",
		`acceptance:
- "go test ./internal/tokenizer/... -count=1" exits 0, verifying real tokenization counts match known values
- estimates use the cached count, checked by reading internal/loop/budget_test.go
constraints:
- do not change the tokenizer's public API
`)
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}

	if len(order.Acceptance) != 2 {
		t.Fatalf("Acceptance = %q", order.Acceptance)
	}

	if order.Acceptance[0] != `"go test ./internal/tokenizer/... -count=1" exits 0, verifying real tokenization counts match known values` {
		t.Errorf("Acceptance[0] = %q, want the quoted command kept verbatim", order.Acceptance[0])
	}
}

// A model told the lines are verbatim may still wrap a whole item in quotes;
// the wrapping goes, interior quoting stays.
func TestParseDraftUnwrapsWhollyQuotedItems(t *testing.T) {
	order, err := ParseDraft("do it",
		"acceptance:\n- \"make test passes\"\n- \"a\" and \"b\" both exist\n")
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}

	if order.Acceptance[0] != "make test passes" {
		t.Errorf("Acceptance[0] = %q, want the wrapping quotes stripped", order.Acceptance[0])
	}

	if order.Acceptance[1] != `"a" and "b" both exist` {
		t.Errorf("Acceptance[1] = %q, want interior quoting untouched", order.Acceptance[1])
	}
}

// Models wrap replies in code fences despite being told not to; a fence must
// not fail the draft.
func TestParseDraftToleratesACodeFence(t *testing.T) {
	order, err := ParseDraft("do it", "```yaml\nacceptance:\n  - it works\n```")
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}

	if len(order.Acceptance) != 1 || order.Acceptance[0] != "it works" {
		t.Errorf("Acceptance = %q", order.Acceptance)
	}
}

func TestParseDraftErrors(t *testing.T) {
	tests := map[string]struct{ objective, reply string }{
		"no objective":           {"   ", "acceptance:\n  - x\n"},
		"prose without the list": {"do it", "Sure! Here are some ideas..."},
		"no criteria proposed":   {"do it", "acceptance:\nconstraints:\n  - x\n"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDraft(test.objective, test.reply); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// The draft run's instructions are the contract that keeps a draft grounded
// and read-only: they must carry the objective, demand observable criteria,
// point at the survey tools, and name the terminal tool that delivers the
// draft.
func TestDraftInstructions(t *testing.T) {
	instructions := DraftInstructions("add rate limiting")

	for _, want := range []string{
		"add rate limiting",
		"observable",
		`"read"`,
		`"list"`,
		`"success"`,
		"Do not attempt the objective",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions are missing %q:\n%s", want, instructions)
		}
	}
}

// The survey can name the order as well as define it: a title is a label a
// person scanning a list of orders reads, and the model that just surveyed the
// tree is well placed to propose one.
func TestParseDraftReadsATitle(t *testing.T) {
	tests := []struct {
		name  string
		reply string
		want  string
	}{
		{
			name:  "a proposed title is read",
			reply: "title: Rate limiting\nacceptance:\n- the suite passes\n",
			want:  "Rate limiting",
		},
		{
			name:  "however the model cases the heading",
			reply: "Title: Rate limiting\nacceptance:\n- the suite passes\n",
			want:  "Rate limiting",
		},
		{
			name:  "quoted, as a model told about verbatim lines may do",
			reply: "title: \"Rate limiting\"\nacceptance:\n- the suite passes\n",
			want:  "Rate limiting",
		},
		{
			name:  "a draft without one is still a good draft",
			reply: "acceptance:\n- the suite passes\n",
			want:  "",
		},
		{
			name:  "a criterion that merely mentions a title is not one",
			reply: "acceptance:\n- title: is shown in the viewer\n- the suite passes\n",
			want:  "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drafted, err := ParseDraft("add rate limiting", test.reply)
			if err != nil {
				t.Fatalf("ParseDraft: %v", err)
			}

			if drafted.Title != test.want {
				t.Errorf("Title = %q, want %q", drafted.Title, test.want)
			}

			// the draft is still the draft whatever the title did
			if len(drafted.Acceptance) == 0 {
				t.Error("the acceptance criteria must survive title parsing")
			}
		})
	}
}

// The survey has to be asked for a title, or it will not propose one.
func TestDraftInstructionsAskForATitle(t *testing.T) {
	instructions := DraftInstructions("add rate limiting")

	if !strings.Contains(instructions, "title:") {
		t.Errorf("the drafting prompt does not ask for a title:\n%s", instructions)
	}
}
