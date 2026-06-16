package tui

import "github.com/charmbracelet/lipgloss"

// The palette leans on the Charm look: a purple accent, a pink secondary, and a
// small set of semantic colors for state.
var (
	colAccent = lipgloss.Color("#7D56F4") // charm purple
	colPink   = lipgloss.Color("#F25D94")
	colFg     = lipgloss.Color("#E6E6E6")
	colDim    = lipgloss.Color("#7A7A7A")
	colFaint  = lipgloss.Color("#4A4A4A")
	colGreen  = lipgloss.Color("#2DD4A7")
	colRed    = lipgloss.Color("#FF5A5A")
	colYellow = lipgloss.Color("#F2C94C")
	colCyan   = lipgloss.Color("#56C2FF")
	colBlue   = lipgloss.Color("#5B8CFF")
)

var (
	// Header.
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colAccent).
			Padding(0, 1)

	taskStyle = lipgloss.NewStyle().Foreground(colFg)

	metaStyle  = lipgloss.NewStyle().Foreground(colDim)
	metaKey    = lipgloss.NewStyle().Foreground(colFaint)
	metaAccent = lipgloss.NewStyle().Foreground(colPink)

	// Footer / hints.
	footerStyle = lipgloss.NewStyle().Foreground(colDim)
	keyHint     = lipgloss.NewStyle().Foreground(colCyan)

	// Status badges. Bold coloured foreground (no background block) so the spinner
	// and label read as one solid colour - embedding the spinner's own ANSI inside
	// a background style otherwise breaks the fill.
	statusRunningStyle = lipgloss.NewStyle().Bold(true).Foreground(colYellow)
	statusDoneStyle    = lipgloss.NewStyle().Bold(true).Foreground(colGreen)
	statusFailStyle    = lipgloss.NewStyle().Bold(true).Foreground(colRed)

	// Activity log.
	dividerStyle = lipgloss.NewStyle().Foreground(colFaint)
	thoughtStyle = lipgloss.NewStyle().Foreground(colDim).Italic(true)
	bulletStyle  = lipgloss.NewStyle().Foreground(colDim)
	outputStyle  = lipgloss.NewStyle().Foreground(colFaint)
	okStyle      = lipgloss.NewStyle().Foreground(colGreen)
	errStyle     = lipgloss.NewStyle().Foreground(colRed)

	// Per-tool accents so the eye can scan the stream.
	toolReadStyle  = lipgloss.NewStyle().Foreground(colCyan)
	toolWriteStyle = lipgloss.NewStyle().Foreground(colGreen)
	toolEditStyle  = lipgloss.NewStyle().Foreground(colYellow)
	toolExecStyle  = lipgloss.NewStyle().Foreground(colBlue)
	toolPlanStyle  = lipgloss.NewStyle().Bold(true).Foreground(colPink)
	toolProgStyle  = lipgloss.NewStyle().Foreground(colPink)
	toolOtherStyle = lipgloss.NewStyle().Foreground(colAccent)

	// Diff panel (opt-in via --diff / ui.diff).
	diffPanelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colFaint).Padding(0, 1)
	diffTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(colFg)
	diffAddGutter     = lipgloss.NewStyle().Foreground(colGreen)
	diffDelGutter     = lipgloss.NewStyle().Foreground(colRed)
	diffContextGutter = lipgloss.NewStyle().Foreground(colFaint)
	diffGapStyle      = lipgloss.NewStyle().Foreground(colFaint)
)
