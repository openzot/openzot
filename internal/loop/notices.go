package loop

import "fmt"

// The notices the loop injects into the conversation when it detects a problem.
//
// They are written as instructions to the model rather than as diagnostics,
// because that is what they are for: the loop has noticed something the model
// cannot see about itself, and the only lever it has is the next prompt.
//
// Each is prefixed so it can be recognised later. A notice must never be
// mistaken for the model's own output, and the cycle detector deliberately skips
// them - a nudge injected to break a loop must not itself break the detection
// run and mask the loop it was meant to end.
const noticePrefix = "!NB:"

// cycleNotice tells the model it is repeating itself.
//
// Naming the specific behaviour matters. "You appear to be stuck" produces
// another lap; "you have called the same tool with the same arguments and got
// the same answer" produces a different approach.
func cycleNotice(detail string) string {
	if detail == "" {
		detail = "you appear to be repeating the same steps"
	}

	return fmt.Sprintf(
		"%s %s. Do not repeat that step again. Either try a materially different "+
			"approach, or stop and explain what is blocking you.",
		noticePrefix, detail,
	)
}

// emptyNotice answers a turn that produced nothing.
func emptyNotice() string {
	return noticePrefix + " your last turn produced no answer and no tool call. " +
		"Either take a concrete next step, or state plainly that you are finished and why."
}

// settleNotice is the nudge in settle mode: the model stopped talking without
// declaring an outcome, which is not an ending.
func settleNotice() string {
	return fmt.Sprintf(
		"%s the task is not finished until you record an outcome. Call %s when the "+
			"objective is met, or %s when it cannot be. Do not simply stop.",
		noticePrefix, SuccessTool, FailureTool,
	)
}

// callBudgetLowNotice warns that tool calls are running out, while there are
// still enough left to act on the warning.
func callBudgetLowNotice(remaining int) string {
	return fmt.Sprintf(
		"%s you have %d tool call(s) left in this run. Prioritise finishing: stop "+
			"exploring and complete the objective with what you already know.",
		noticePrefix, remaining,
	)
}

// truncationNotice follows an answer the provider cut off at the token limit.
func truncationNotice() string {
	return noticePrefix + " your previous answer was cut off at the output limit. " +
		"Continue from exactly where it stopped, without repeating what you already wrote."
}

// terminalTools are the tool definitions injected in settle mode.
//
// They are given to the model as ordinary tools because that is the mechanism it
// already understands. The loop intercepts them rather than dispatching to a
// handler.
func terminalTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        SuccessTool,
			Description: "Record that the objective has been met, and end the run. Call this exactly once, when the task is genuinely complete.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"summary": map[string]any{
						"type":        "string",
						"description": "What was accomplished.",
					},
				},
				"required": []string{"summary"},
			},
		},
		{
			Name:        FailureTool,
			Description: "Record that the objective cannot be met, and end the run. Call this when you are blocked and further attempts would not help.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "What is blocking completion.",
					},
				},
				"required": []string{"reason"},
			},
		},
	}
}
