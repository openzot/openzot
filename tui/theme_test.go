package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestDefaultThemeDefinesBothBrandColours(t *testing.T) {
	d := DefaultTheme()

	if d.Accent == "" || d.Secondary == "" {
		t.Errorf("DefaultTheme must define an accent and a secondary, got %+v", d)
	}
}

// An embedder's theme repaints the brand-coloured styles - the title badge and
// the accent markers - while the semantic colours stay put.
func TestApplyThemeRepaintsTheBrandStyles(t *testing.T) {
	// package-level styles are global; restore the neutral default so this does
	// not bleed into the other view tests
	t.Cleanup(func() { applyTheme(DefaultTheme()) })

	red := lipgloss.Color("#FF0000")
	green := lipgloss.Color("#00FF00")

	applyTheme(Theme{Accent: red, Secondary: green})

	if got := titleStyle.GetBackground(); got != red {
		t.Errorf("the title badge must take the theme accent, got %v", got)
	}

	if got := toolOtherStyle.GetForeground(); got != red {
		t.Errorf("the unknown-tool marker must take the theme accent, got %v", got)
	}

	if got := toolPlanStyle.GetForeground(); got != green {
		t.Errorf("the plan accent must take the theme secondary, got %v", got)
	}

	// a fixed semantic colour is untouched
	if got := statusFailStyle.GetForeground(); got != colRed {
		t.Errorf("the failure badge must stay the fixed semantic red, got %v", got)
	}
}

// A caller that leaves the theme unset gets zot's neutral look, not an empty
// colour that would render nothing.
func TestApplyThemeEmptyFallsBackToNeutralDefault(t *testing.T) {
	t.Cleanup(func() { applyTheme(DefaultTheme()) })

	applyTheme(Theme{})

	if got := toolOtherStyle.GetForeground(); got != DefaultTheme().Accent {
		t.Errorf("an empty theme must fall back to the neutral default accent, got %v", got)
	}
}
