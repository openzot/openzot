package tui

import (
	"context"

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"

	tea "github.com/charmbracelet/bubbletea"
)

// These messages are how the background agent talks to the Bubble Tea program.
// The UI never calls the SDK directly; it only reacts to these.
type (
	// agentEventMsg carries one streamed event from agent.ExecuteWithTools.
	agentEventMsg struct{ ev agent.AgentEvent }
	// agentErrMsg reports a fatal error from the agent run.
	agentErrMsg struct{ err error }
	// agentDoneMsg signals that the event stream has been fully drained.
	agentDoneMsg struct{}
)

// runAgent runs the autonomous agent to completion, relaying every event into
// the program. It is meant to be launched in its own goroutine; it blocks until
// the agent's event channel closes.
//
// All the autonomy lives in agent.ExecuteWithTools - it loops the model through
// plan/act/observe/exit on its own. runAgent is a pure pump: SDK event in,
// tea.Msg out.
func runAgent(ctx context.Context, p *tea.Program, client *sdk.Client, opts agent.ExecuteWithToolsOptions) {
	events, errs := agent.ExecuteWithTools(ctx, client, opts)

	for ev := range events {
		p.Send(agentEventMsg{ev: ev})
	}

	if err := <-errs; err != nil {
		p.Send(agentErrMsg{err: err})
	}

	p.Send(agentDoneMsg{})
}
