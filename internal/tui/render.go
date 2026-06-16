package tui

import (
	"fmt"
	"strings"
)

// maxOutputLines caps how much command output we echo into the log so a chatty
// build doesn't bury the rest of the activity.
const maxOutputLines = 8

// renderToolStart turns a tool invocation into one or more styled log lines. The
// built-in coding tools (read/write/edit/exec) and the agent's system tools
// (plan/progress) each get a tailored, scannable representation.
func renderToolStart(name string, args map[string]interface{}) string {
	switch name {
	case "read":
		return toolReadStyle.Render("  read   ") + dimPath(args, "path") + lineRange(args)
	case "write":
		return toolWriteStyle.Render("  write  ") + dimPath(args, "path")
	case "edit":
		return toolEditStyle.Render("  edit   ") + dimPath(args, "path")
	case "exec":
		return toolExecStyle.Render("  exec   ") + taskStyle.Render(truncate(str(args, "command"), 200))
	case "plan":
		return renderPlan(args)
	case "progress":
		return renderProgress(args)
	default:
		return toolOtherStyle.Render("  "+pad(name, 6)+" ") + outputStyle.Render(compactArgs(args))
	}
}

// renderToolEnd produces an optional follow-up line summarising a tool result.
// It returns "" when there is nothing worth showing (e.g. a successful write).
func renderToolEnd(name string, result interface{}) string {
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

	switch name {
	case "read":
		if n, ok := intish(m["totalLines"]); ok {
			return outputStyle.Render(fmt.Sprintf("    ✓ %d lines", n))
		}
	case "exec":
		line := okStyle.Render("    ✓ done")
		if tail := commandOutput(m); tail != "" {
			line += "\n" + tail
		}
		return line
	case "write":
		return okStyle.Render("    ✓ saved")
	case "edit":
		return okStyle.Render("    ✓ applied")
	}
	return ""
}

func renderPlan(args map[string]interface{}) string {
	var b strings.Builder
	b.WriteString(toolPlanStyle.Render("  plan"))
	if r := str(args, "rationale"); r != "" {
		b.WriteString(thoughtStyle.Render("  " + r))
	}
	for i, step := range slice(args["steps"]) {
		b.WriteString("\n")
		b.WriteString(bulletStyle.Render(fmt.Sprintf("    %d. ", i+1)) + taskStyle.Render(fmt.Sprint(step)))
	}
	return b.String()
}

func renderProgress(args map[string]interface{}) string {
	var b strings.Builder
	current := str(args, "current")
	if current == "" {
		current = "working…"
	}
	b.WriteString(toolProgStyle.Render("  ▸ ") + taskStyle.Render(current))
	for _, done := range slice(args["completed"]) {
		b.WriteString("\n" + okStyle.Render("    ✓ ") + outputStyle.Render(fmt.Sprint(done)))
	}
	for _, blk := range slice(args["blockers"]) {
		b.WriteString("\n" + errStyle.Render("    ! ") + outputStyle.Render(fmt.Sprint(blk)))
	}
	return b.String()
}

// commandOutput renders the stdout/stderr of an exec result, trimmed and capped.
func commandOutput(m map[string]interface{}) string {
	text := strings.TrimRight(str(m, "stdout"), "\n")
	if text == "" {
		text = strings.TrimRight(str(m, "stderr"), "\n")
	}
	if text == "" {
		return ""
	}

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
		b.WriteString(outputStyle.Render("    │ " + l))
	}
	if clipped {
		b.WriteString("\n" + outputStyle.Render("    │ …"))
	}
	return b.String()
}

// --- small helpers over the loosely-typed arg/result maps -------------------

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

func slice(v interface{}) []interface{} {
	if s, ok := v.([]interface{}); ok {
		return s
	}
	return nil
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

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}
