package loop

import (
	"strings"
	"testing"
)

func TestCallBudgetLowThresholdIsProportionalButCapped(t *testing.T) {
	tests := []struct {
		maxCalls int
		want     int
	}{
		// proportional on a small budget
		{10, 2},
		{25, 5},
		{50, 10},

		// capped on a large one: a fifth of 1000 calls would warn so early the
		// notice would be noise
		{1000, CallBudgetLowCap},

		// never zero, or the warning could never fire
		{1, 1},
		{3, 1},
	}

	for _, test := range tests {
		if got := callBudgetLowThreshold(test.maxCalls); got != test.want {
			t.Errorf("callBudgetLowThreshold(%d) = %d, want %d", test.maxCalls, got, test.want)
		}
	}
}

// Every notice must be recognisable as an injected instruction rather than the
// model's own words - the cycle detector skips them by that prefix, so a notice
// without it would break loop detection.
func TestNoticesCarryThePrefix(t *testing.T) {
	notices := map[string]string{
		"cycle":      cycleNotice("you keep calling the same tool"),
		"empty":      emptyNotice(),
		"settle":     settleNotice(),
		"budget":     callBudgetLowNotice(3),
		"truncation": truncationNotice(),
	}

	for name, notice := range notices {
		if !strings.HasPrefix(notice, noticePrefix) {
			t.Errorf("%s notice does not carry the prefix: %q", name, notice)
		}

		if strings.TrimSpace(strings.TrimPrefix(notice, noticePrefix)) == "" {
			t.Errorf("%s notice has no content", name)
		}
	}
}

// A nudge that only says "you seem stuck" produces another lap. Naming the
// behaviour is what makes the model change approach.
func TestCycleNoticeNamesTheBehaviour(t *testing.T) {
	notice := cycleNotice("you have called the same tool with the same arguments")

	if !strings.Contains(notice, "same tool with the same arguments") {
		t.Errorf("the specific behaviour must survive into the notice: %q", notice)
	}

	// an unattributed cycle still produces something actionable
	if got := cycleNotice(""); !strings.Contains(got, "repeating") {
		t.Errorf("a detail-less cycle notice must still be actionable: %q", got)
	}
}

func TestSettleNoticeNamesBothTerminalTools(t *testing.T) {
	notice := settleNotice()

	for _, tool := range []string{SuccessTool, FailureTool} {
		if !strings.Contains(notice, tool) {
			t.Errorf("the settle notice must name %s: %q", tool, notice)
		}
	}
}

func TestCallBudgetLowNoticeStatesTheRemainder(t *testing.T) {
	if got := callBudgetLowNotice(4); !strings.Contains(got, "4 tool call") {
		t.Errorf("the notice must state how many calls are left: %q", got)
	}
}

func TestTerminalToolsAreWellFormed(t *testing.T) {
	tools := terminalTools()

	if len(tools) != 2 {
		t.Fatalf("got %d terminal tools, want 2", len(tools))
	}

	byName := map[string]ToolDefinition{}

	for _, tool := range tools {
		byName[tool.Name] = tool
	}

	for name, required := range map[string]string{SuccessTool: "summary", FailureTool: "reason"} {
		tool, ok := byName[name]

		if !ok {
			t.Fatalf("terminal tool %q missing", name)
		}

		if tool.Description == "" {
			t.Errorf("%s has no description; the model needs to know when to call it", name)
		}

		properties, _ := tool.Parameters["properties"].(map[string]any)

		if _, ok := properties[required]; !ok {
			t.Errorf("%s must accept a %q argument", name, required)
		}

		fields, _ := tool.Parameters["required"].([]string)

		if len(fields) != 1 || fields[0] != required {
			t.Errorf("%s must require %q, got %v", name, required, fields)
		}

		// the loop intercepts these rather than dispatching them
		if tool.Handler != nil {
			t.Errorf("%s must not carry a handler", name)
		}
	}
}

func TestCycleDetailCoversEveryHeuristic(t *testing.T) {
	// each heuristic fails differently, so each needs its own explanation - a
	// generic nudge would not tell the model what to change
	for _, heuristic := range []string{
		"repeated_suffix",
		"repeated_activity_tail",
		"repeated_result_run",
		"repeated_message_text_run",
	} {
		if detail := cycleDetail(heuristic); detail == "" {
			t.Errorf("heuristic %q has no explanation for the model", heuristic)
		}
	}

	if detail := cycleDetail("something-new"); detail != "" {
		t.Errorf("an unknown heuristic should fall back to the generic notice, got %q", detail)
	}
}

func TestStructuralSummaryCondensesWithoutAModel(t *testing.T) {
	messages := toCompactionMessages([]Message{
		{Type: TypeUser, Text: "please do the thing"},
		{Type: TypeBot, Text: strings.Repeat("x", 500)},
		{Type: TypeBot, Text: "   "},
	})

	summary := structuralSummary(messages)

	if !strings.Contains(summary, "please do the thing") {
		t.Errorf("the summary must retain the earlier turns: %q", summary)
	}

	// long turns are clipped so the summary cannot itself overflow the window
	if strings.Contains(summary, strings.Repeat("x", 400)) {
		t.Error("a long message must be clipped in the summary")
	}

	if !strings.Contains(summary, "…") {
		t.Error("clipping must be visible")
	}

	// a blank message contributes nothing
	if strings.Count(summary, "[bot]") != 1 {
		t.Errorf("a blank message should be skipped: %q", summary)
	}
}
