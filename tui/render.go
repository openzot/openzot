package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// maxOutputLines caps how much command output we echo into the log so a chatty
// build doesn't bury the rest of the activity.
const maxOutputLines = 8

// renderToolStart turns a tool invocation into one or more styled log lines.
//
// The built-in tools each get a tailored, scannable representation; anything a
// caller has added falls through to a generic one. The names here are the names
// in agent.DefaultTools - a mismatch is not a compile error, it just quietly
// renders the agent's most-used tool as an anonymous key/value dump.
func renderToolStart(name string, args map[string]interface{}) string {
	switch name {
	case "read":
		return toolReadStyle.Render("  read   ") + dimPath(args, "path") + lineRange(args)
	case "write":
		return toolWriteStyle.Render("  write  ") + dimPath(args, "path") + lineRange(args)
	case "edit":
		// the before/after text is what diffForTool renders; naming the file is
		// all this line has to do, and the generic branch would print both whole
		// versions of it here instead
		return toolWriteStyle.Render("  edit   ") + dimPath(args, "path")
	case "list":
		return toolReadStyle.Render("  list   ") + dimPath(args, "path")
	case "shell":
		return toolExecStyle.Render("  shell  ") + taskStyle.Render(truncate(str(args, "command"), 200))
	case "skill":
		return toolOtherStyle.Render("  skill  ") + taskStyle.Render(str(args, "name"))
	case "plan":
		return renderPlan(args)
	case "progress":
		return renderProgress(args)
	default:
		return toolOtherStyle.Render("  "+pad(name, 6)+" ") + outputStyle.Render(compactArgs(args))
	}
}

// renderToolEnd produces an optional follow-up line summarising a tool result.
// It returns "" when there is nothing worth showing.
//
// zot's tools return plain strings, so that is the case handled first; the map
// form is kept for a caller whose own tool returns something structured.
func renderToolEnd(name string, result interface{}) string {
	if text, ok := result.(string); ok {
		return renderTextResult(name, text)
	}

	m, ok := result.(map[string]interface{})
	if !ok {
		return ""
	}

	if success, present := m["success"].(bool); present && !success {
		if e := str(m, "error"); e != "" {
			out := errStyle.Render("    ✗ " + truncate(e, 200))
			if tail := commandOutput(m); tail != "" {
				out += "\n" + tail
			}
			return out
		}
	}

	if tail := commandOutput(m); tail != "" {
		return okStyle.Render("    ✓ done") + "\n" + tail
	}

	return okStyle.Render("    ✓ done")
}

// renderTextResult summarises a string result.
//
// A shell command's output is the thing the operator most wants to see, so it is
// echoed (capped); a file read is summarised by size instead, because dumping a
// whole file into the log buries everything around it.
func renderTextResult(name string, text string) string {
	trimmed := strings.TrimRight(text, "\n")

	switch name {
	case "shell":
		if trimmed == "" {
			return okStyle.Render("    ✓ done")
		}

		return okStyle.Render("    ✓ done") + "\n" + renderOutputLines(trimmed)

	case "read", "list":
		lines := 0

		if trimmed != "" {
			lines = strings.Count(trimmed, "\n") + 1
		}

		return outputStyle.Render(fmt.Sprintf("    ✓ %d lines", lines))

	case "write":
		return okStyle.Render("    ✓ saved")

	default:
		if trimmed == "" {
			return ""
		}

		return renderOutputLines(trimmed)
	}
}

// renderOutputLines renders captured output, capped so one noisy command cannot
// scroll the rest of the run off the screen.
func renderOutputLines(text string) string {
	lines := strings.Split(text, "\n")

	clipped := false

	if len(lines) > maxOutputLines {
		lines = lines[:maxOutputLines]
		clipped = true
	}

	var b strings.Builder

	for i, l := range lines {
		if i > 0 {
			b.WriteString("\n")
		}

		b.WriteString(outputStyle.Render("    │ " + truncate(l, 200)))
	}

	if clipped {
		b.WriteString("\n" + outputStyle.Render("    │ …"))
	}

	return b.String()
}

// commandOutput renders the stdout/stderr of a structured result.
func commandOutput(m map[string]interface{}) string {
	text := strings.TrimRight(str(m, "stdout"), "\n")

	if text == "" {
		text = strings.TrimRight(str(m, "stderr"), "\n")
	}

	if text == "" {
		return ""
	}

	return renderOutputLines(text)
}

// --- small helpers over the loosely-typed arg/result maps -------------------

// renderPlan lays the plan out as a numbered list, because the plan is the one
// piece of the run worth reading in full - it is the map the agent is following,
// and seeing it is how the operator knows whether the approach is sound.
func renderPlan(args map[string]interface{}) string {
	head := toolOtherStyle.Render("  plan   ")
	if rationale := str(args, "rationale"); rationale != "" {
		head += thoughtStyle.Render(truncate(rationale, 200))
	}

	steps := strList(args, "steps")
	if len(steps) == 0 {
		return head
	}

	var b strings.Builder
	b.WriteString(head)
	for i, step := range steps {
		b.WriteString("\n")
		b.WriteString(outputStyle.Render(fmt.Sprintf("    %d. ", i+1)))
		b.WriteString(taskStyle.Render(truncate(step, 200)))
	}
	return b.String()
}

// renderProgress shows the current step, then what is done, blocked and next -
// a live status the operator can glance at on a long run.
func renderProgress(args map[string]interface{}) string {
	head := toolOtherStyle.Render("  update ")
	if current := str(args, "current"); current != "" {
		head += taskStyle.Render(truncate(current, 200))
	} else {
		head += outputStyle.Render("progress")
	}

	var b strings.Builder
	b.WriteString(head)
	for _, section := range []struct {
		label string
		key   string
		style lipgloss.Style
	}{
		{"done", "completed", okStyle},
		{"blocked", "blockers", errStyle},
		{"next", "nextSteps", outputStyle},
	} {
		for _, item := range strList(args, section.key) {
			b.WriteString("\n")
			b.WriteString(outputStyle.Render("    " + pad(section.label, 8)))
			b.WriteString(section.style.Render(truncate(item, 180)))
		}
	}
	return b.String()
}

// strList reads a JSON string array from a tool's arguments, tolerating the
// non-string entries a model occasionally emits.
func strList(args map[string]interface{}, key string) []string {
	raw, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func dimPath(args map[string]interface{}, key string) string {
	return taskStyle.Render(str(args, key))
}

func lineRange(args map[string]interface{}) string {
	start, hasStart := intish(args["startLine"])
	end, hasEnd := intish(args["endLine"])
	switch {
	case hasStart && hasEnd:
		return outputStyle.Render(fmt.Sprintf(" :%d-%d", start, end))
	case hasStart:
		return outputStyle.Render(fmt.Sprintf(" :%d", start))
	default:
		return ""
	}
}

func compactArgs(args map[string]interface{}) string {
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncate(fmt.Sprint(v), 40)))
	}
	return strings.Join(parts, " ")
}

func str(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// intish coerces JSON numbers (float64) and ints into an int.
func intish(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// truncate flattens a string to one line and caps it at max characters.
//
// Characters, not bytes: slicing bytes cuts a multi-byte rune in half, so a task
// or tool argument in CJK or emoji rendered a replacement character - and the
// cap bit far earlier than the width it was given, since one glyph can be four
// bytes.
func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")

	if utf8.RuneCountInString(s) <= max {
		return s
	}

	return string([]rune(s)[:max-1]) + "…"
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}
