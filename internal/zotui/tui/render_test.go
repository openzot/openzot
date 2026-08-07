package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/openzot/openzot/internal/zotui/store"
)

// TestListFrameRenders renders one frame of the command center (no color) so the
// paneled layout can be eyeballed with `go test -v`, and asserts the panels and a
// job's content are present.
func TestListFrameRenders(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)

	m := newModel(nil)
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = sized.(model)

	now := time.Now()
	loaded, _ := m.Update(jobsMsg([]store.Job{
		{ID: "job_abc123def456", Source: "acme", Repository: "acme-corp/api", Environment: "go-ubuntu", Model: "glm", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now},
		{ID: "job_ff00 aa11", Source: "labs", Repository: "labs/tool", Environment: "go-ubuntu", Model: "sonnet", Status: store.StatusFailed, CreatedAt: now, UpdatedAt: now},
	}))
	m = loaded.(model)

	out := m.View()

	for _, want := range []string{"zotui", "JOBS", "DETAILS", "acme-corp/api", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("frame is missing %q", want)
		}
	}
}
