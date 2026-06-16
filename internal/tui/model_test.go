package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/chatbotkit/go-sdk/agent"
)

// editEvent is a sample edit tool call.
func editEvent() agent.ToolCallStartEvent {
	return agent.ToolCallStartEvent{
		Name: "edit",
		Args: map[string]interface{}{
			"path":      "x.go",
			"oldString": "a := 1\n",
			"newString": "a := 2\n",
		},
	}
}

func TestModelShowsDiffWhenEnabled(t *testing.T) {
	m := newModel("task", "model", "/tmp", true)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = sized.(model)

	updated, _ := m.Update(agentEventMsg{ev: editEvent()})
	m = updated.(model)

	if !strings.Contains(strings.Join(m.entries, "\n"), "╭") {
		t.Fatal("expected a diff panel in the log when ShowDiff is on")
	}
	// The panel must survive width-wrapping into the rendered viewport content.
	if !strings.Contains(m.committedWrapped, "╭") {
		t.Error("diff panel border was lost during wrapping")
	}
}

func TestModelNoDiffWhenDisabled(t *testing.T) {
	m := newModel("task", "model", "/tmp", false)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = sized.(model)

	updated, _ := m.Update(agentEventMsg{ev: editEvent()})
	m = updated.(model)

	if strings.Contains(strings.Join(m.entries, "\n"), "╭") {
		t.Error("did not expect a diff panel when ShowDiff is off")
	}
}
