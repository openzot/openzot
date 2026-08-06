package tui

import "github.com/charmbracelet/lipgloss"

// Theme is the colour identity an embedding application gives the viewer.
//
// Only the brand colours are themeable. The semantic and neutral colours - the
// status badges (running/done/failed), the diff gutters, the dim log text - stay
// fixed, so a done run reads green and a failure reads red whichever application
// is embedding the view. What an embedder owns is the accent: zot ships neutral,
// so a host (rook red, pion blue) can make the view its own without the meaning
// of the fixed colours shifting under it.
type Theme struct {
	// Accent is the brand colour: the title-badge background, the marker on a
	// tool the view does not have a dedicated colour for, and the primary
	// highlight.
	Accent lipgloss.Color

	// Secondary is a supporting brand colour, used for the plan/progress accents
	// and the meta line beneath the title.
	Secondary lipgloss.Color
}

// DefaultTheme is zot's own look: a neutral, near-monochrome identity that stays
// deliberately out of the way. An embedder that wants the view to carry its own
// brand passes its own Theme to Run instead.
func DefaultTheme() Theme {
	return Theme{
		Accent:    lipgloss.Color("#6B7280"), // neutral slate
		Secondary: lipgloss.Color("#9CA3AF"), // lighter slate
	}
}

// applyTheme rebuilds the brand-coloured styles from t. The semantic and neutral
// styles are left untouched.
//
// The styles are package-level and this reassigns them, which is safe because a
// viewer is one per process - each embedding binary runs a single Run - and this
// is called once, before the Bubble Tea program starts. An empty Theme falls
// back to DefaultTheme so a caller that leaves it unset gets zot's neutral look.
func applyTheme(t Theme) {
	if t.Accent == "" {
		t = DefaultTheme()
	}

	colAccent = t.Accent
	colSecondary = t.Secondary

	titleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(colAccent).
		Padding(0, 1)

	toolPlanStyle = lipgloss.NewStyle().Bold(true).Foreground(colSecondary)
	toolProgStyle = lipgloss.NewStyle().Foreground(colSecondary)
	toolOtherStyle = lipgloss.NewStyle().Foreground(colAccent)
}
