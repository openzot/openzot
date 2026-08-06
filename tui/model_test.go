package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/openzot/openzot/agent"
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

// The app name identifies the embedding application throughout the view, so a
// host (rook, pion) reads as itself rather than "zot". It appears in the startup
// line before the first frame and in the title badge once sized.
func TestAppNameRendersInStartupAndTitle(t *testing.T) {
	m := newModel("rook", "hunt bugs", "model", "openai", "/tmp", false)

	if got := m.View(); !strings.Contains(got, "starting rook…") {
		t.Errorf("startup line must carry the app name, got %q", got)
	}

	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = sized.(model)

	if title := m.titleBar(); !strings.Contains(title, "rook") {
		t.Errorf("title badge must carry the app name, got %q", title)
	}
}

func TestModelShowsDiffWhenEnabled(t *testing.T) {
	m := newModel("zot", "task", "model", "openai", "/tmp", true)
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
	m := newModel("zot", "task", "model", "openai", "/tmp", false)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = sized.(model)

	updated, _ := m.Update(agentEventMsg{ev: editEvent()})
	m = updated.(model)

	if strings.Contains(strings.Join(m.entries, "\n"), "╭") {
		t.Error("did not expect a diff panel when ShowDiff is off")
	}
}
