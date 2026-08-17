// Package tui is zotui's command center: a full-screen, paneled terminal app to
// see scheduled tasks, inspect them, cancel one, and create a new one. It reads
// and drives everything through the app facade, refreshing from the store on a
// tick, so a job scheduled here is visible again the next time you open the tool.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/store"
)

// Run launches the command center and blocks until the user quits.
func Run(a *app.App) error {
	p := tea.NewProgram(newModel(a), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type mode int

const (
	modeList mode = iota
	modeCreate
)

// palette
const (
	ink      = lipgloss.Color("#E7EBF2")
	inkMuted = lipgloss.Color("#8B96A6")
	inkFaint = lipgloss.Color("#5A6472")
	ground   = lipgloss.Color("#0C0F14")
	lineIdle = lipgloss.Color("#232B37")
	lineOn   = lipgloss.Color("#6B7280")
	green    = lipgloss.Color("#46C79A")
	amber    = lipgloss.Color("#F0A63C")
	red      = lipgloss.Color("#EC5B5B")
)

var (
	brand     = lipgloss.NewStyle().Bold(true).Foreground(ground).Background(ink).Padding(0, 1)
	brandSub  = lipgloss.NewStyle().Foreground(inkMuted).Padding(0, 1)
	paneTitle = lipgloss.NewStyle().Bold(true).Foreground(inkMuted)
	keyLabel  = lipgloss.NewStyle().Foreground(inkMuted).Width(12)
	footerSty = lipgloss.NewStyle().Foreground(inkFaint)
	noticeSty = lipgloss.NewStyle().Foreground(green)
	failSty   = lipgloss.NewStyle().Foreground(red)
)

type model struct {
	app  *app.App
	mode mode

	table table.Model
	jobs  []store.Job

	inputs  []textinput.Model // repo, repository, environment, model, task
	focus   int
	formErr string

	status               string
	width, height        int
	leftW, rightW, bodyH int
}

type jobsMsg []store.Job
type errMsg struct{ err error }
type tickMsg time.Time
type scheduledMsg struct{ id string }
type actionMsg struct{ note string }

func newModel(a *app.App) model {
	t := table.New(table.WithFocused(true))
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(inkMuted).BorderStyle(lipgloss.NormalBorder()).BorderForeground(lineIdle).BorderBottom(true)
	s.Selected = s.Selected.Foreground(ground).Background(ink).Bold(true)
	t.SetStyles(s)
	return model{app: a, mode: modeList, table: t}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.loadJobs(), tickCmd()) }

// --- layout -----------------------------------------------------------------

func (m *model) layout() {
	if m.width < 24 || m.height < 8 {
		return
	}
	m.bodyH = m.height - 2 // header (1) + footer (1)
	if m.bodyH < 6 {
		m.bodyH = 6
	}
	m.leftW = m.width * 58 / 100
	if m.leftW < 24 {
		m.leftW = 24
	}
	m.rightW = m.width - m.leftW

	panelInner := m.leftW - 2 // panel border
	tableH := m.bodyH - 3     // panel border (2) + title line (1)
	if panelInner < 16 {
		panelInner = 16
	}
	if tableH < 3 {
		tableH = 3
	}

	// Each column carries 2 cells of horizontal padding, so the columns must sum to
	// panelInner - (2 * columns) or the header underline overflows the panel.
	idW, stW := 10, 10
	repoW := panelInner - 6 - idW - stW
	if repoW < 8 {
		repoW = 8
	}
	m.table.SetColumns([]table.Column{
		{Title: "ID", Width: idW},
		{Title: "REPOSITORY", Width: repoW},
		{Title: "STATUS", Width: stW},
	})
	m.table.SetWidth(panelInner)
	m.table.SetHeight(tableH)
}

// --- commands ---------------------------------------------------------------

func (m model) loadJobs() tea.Cmd {
	a := m.app
	return func() tea.Msg {
		jobs, err := a.Jobs(context.Background())
		if err != nil {
			return errMsg{err}
		}
		return jobsMsg(jobs)
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) cancel(id string) tea.Cmd {
	a := m.app
	return func() tea.Msg {
		if err := a.Cancel(context.Background(), id); err != nil {
			return errMsg{err}
		}
		return actionMsg{note: "cancelled " + shortID(id)}
	}
}

func (m model) submit() tea.Cmd {
	a := m.app
	p := app.ScheduleParams{
		Repo:        strings.TrimSpace(m.inputs[0].Value()),
		Repository:  strings.TrimSpace(m.inputs[1].Value()),
		Environment: strings.TrimSpace(m.inputs[2].Value()),
		Model:       strings.TrimSpace(m.inputs[3].Value()),
		Task:        strings.TrimSpace(m.inputs[4].Value()),
	}
	return func() tea.Msg {
		id, err := a.Schedule(context.Background(), p)
		if err != nil {
			return errMsg{err}
		}
		return scheduledMsg{id}
	}
}

// --- update -----------------------------------------------------------------

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case jobsMsg:
		m.jobs = []store.Job(msg)
		m.table.SetRows(rows(m.jobs))
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.loadJobs(), tickCmd())

	case scheduledMsg:
		m.mode = modeList
		m.status = noticeSty.Render("scheduled " + shortID(msg.id))
		return m, m.loadJobs()

	case actionMsg:
		m.status = noticeSty.Render(msg.note)
		return m, m.loadJobs()

	case errMsg:
		if m.mode == modeCreate {
			m.formErr = msg.err.Error()
		} else {
			m.status = failSty.Render(msg.err.Error())
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.mode == modeCreate {
			return m.updateCreate(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "n":
		m.startCreate()
		return m, textinput.Blink
	case "r":
		m.status = ""
		return m, m.loadJobs()
	case "c":
		if j := m.selectedJob(); j != nil {
			return m, m.cancel(j.ID)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m model) updateCreate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "tab", "down":
		m.setFocus((m.focus + 1) % len(m.inputs))
		return m, nil
	case "shift+tab", "up":
		m.setFocus((m.focus - 1 + len(m.inputs)) % len(m.inputs))
		return m, nil
	case "enter":
		return m, m.submit()
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m *model) startCreate() {
	m.mode = modeCreate
	m.formErr = ""
	m.focus = 0
	placeholders := []string{
		strings.Join(m.app.Repos(), " | "),
		"owner/name",
		strings.Join(m.app.Environments(), " | "),
		strings.Join(m.app.Models(), " | ") + "  (blank = environment default)",
		"what should the agent do?",
	}
	m.inputs = make([]textinput.Model, len(placeholders))
	for i := range m.inputs {
		ti := textinput.New()
		ti.Placeholder = placeholders[i]
		ti.CharLimit = 800
		ti.Width = 52
		m.inputs[i] = ti
	}
	m.inputs[0].Focus()
}

func (m *model) setFocus(i int) {
	m.inputs[m.focus].Blur()
	m.focus = i
	m.inputs[m.focus].Focus()
}

func (m model) selectedJob() *store.Job {
	i := m.table.Cursor()
	if i >= 0 && i < len(m.jobs) {
		return &m.jobs[i]
	}
	return nil
}

// --- views ------------------------------------------------------------------

func (m model) View() string {
	if m.width < 24 || m.height < 8 {
		return "terminal too small"
	}
	if m.mode == modeCreate {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.header(),
			m.panel("NEW JOB", m.createBody(), m.width, m.bodyH, true),
			m.footer("tab/↑↓ move · enter schedule · esc cancel"),
		)
	}

	left := m.panel("JOBS", m.listBody(), m.leftW, m.bodyH, true)
	right := m.panel("DETAILS", m.detailBody(), m.rightW, m.bodyH, false)
	return lipgloss.JoinVertical(lipgloss.Left,
		m.header(),
		lipgloss.JoinHorizontal(lipgloss.Top, left, right),
		m.footer("↑/↓ move · n new · c cancel · r refresh · q quit"),
	)
}

func (m model) header() string {
	left := brand.Render("◆ zotui") + brandSub.Render("command center")
	right := brandSub.Render(m.summary())
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m model) footer(keys string) string {
	line := footerSty.Render(keys)
	if m.status != "" {
		line = m.status + footerSty.Render("   ·   ") + line
	}
	return line
}

func (m model) summary() string {
	var running, done, failed int
	for _, j := range m.jobs {
		switch j.Status {
		case store.StatusRunning, store.StatusScheduled:
			running++
		case store.StatusSettled:
			done++
		case store.StatusFailed, store.StatusCancelled:
			failed++
		}
	}
	return fmt.Sprintf("%d active · %d settled · %d ended", running, done, failed)
}

// panel renders content in a bordered, titled box occupying totalW x totalH cells.
func (m model) panel(titleText, content string, totalW, totalH int, active bool) string {
	col := lineIdle
	if active {
		col = lineOn
	}
	inner := paneTitle.Render(titleText) + "\n" + content
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(col).
		Width(totalW - 2).
		Height(totalH - 2).
		Render(inner)
}

func (m model) listBody() string {
	if len(m.jobs) == 0 {
		return brandSub.Render("\n  no jobs yet — press n to schedule one")
	}
	return m.table.View()
}

func (m model) detailBody() string {
	j := m.selectedJob()
	if j == nil {
		return brandSub.Render("\n  select a job to see its details")
	}
	w := m.rightW - 4
	if w < 10 {
		w = 10
	}

	lines := []string{
		kv("id", shortID(j.ID)),
		kv("repo", j.Repo),
		kv("repository", j.Repository),
		kv("environment", j.Environment),
		kv("model", j.Model),
		kvStatus(j.Status),
		kv("created", j.CreatedAt.Format("Jan 02 15:04:05")),
		kv("updated", j.UpdatedAt.Format("Jan 02 15:04:05")),
	}
	if j.ExitCode != nil {
		lines = append(lines, kv("exit", fmt.Sprintf("%d", *j.ExitCode)))
	}
	task := lipgloss.NewStyle().Width(w).Foreground(ink).Render(j.Task)
	return strings.Join(lines, "\n") + "\n\n" + keyLabel.Render("task") + "\n" + task
}

func (m model) createBody() string {
	names := []string{"repo", "repository", "environment", "model", "task"}
	var b strings.Builder
	for i, in := range m.inputs {
		b.WriteString(keyLabel.Render(names[i]) + "\n" + in.View() + "\n\n")
	}
	if m.formErr != "" {
		b.WriteString(failSty.Render(m.formErr))
	}
	return b.String()
}

// --- helpers ----------------------------------------------------------------

func kv(k, v string) string {
	return keyLabel.Render(k) + lipgloss.NewStyle().Foreground(ink).Render(v)
}

func kvStatus(s store.Status) string {
	col := inkMuted
	switch s {
	case store.StatusRunning, store.StatusScheduled:
		col = amber
	case store.StatusSettled:
		col = green
	case store.StatusFailed, store.StatusCancelled:
		col = red
	}
	return keyLabel.Render("status") + lipgloss.NewStyle().Foreground(col).Bold(true).Render(string(s))
}

func rows(jobs []store.Job) []table.Row {
	out := make([]table.Row, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, table.Row{shortID(j.ID), j.Repo + "/" + j.Repository, string(j.Status)})
	}
	return out
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "job_")
	if len(id) > 10 {
		return id[:10]
	}
	return id
}
