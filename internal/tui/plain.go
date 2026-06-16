package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"
)

// isInteractive reports whether stdout is a terminal capable of the full-screen
// UI. When it isn't (piped, redirected, run under another process, CI), zot
// falls back to plain mode instead of trying - and failing - to start an
// alt-screen program.
func isInteractive() bool {
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// runPlain streams the agent's activity as plain, unstyled lines. It is used in
// non-interactive shells and when --plain is set, so zot's output stays usable
// in pipes, logs, and CI without a TTY or escape codes.
func runPlain(ctx context.Context, client *sdk.Client, meta Meta, opts agent.ExecuteWithToolsOptions) error {
	fmt.Printf("zot: %s\n", meta.Task)
	fmt.Printf("model %s · dir %s\n", meta.Model, meta.Workdir)

	events, errs := agent.ExecuteWithTools(ctx, client, opts)

	var pending strings.Builder
	flush := func() {
		if s := strings.TrimSpace(pending.String()); s != "" {
			fmt.Printf("  • %s\n", s)
		}
		pending.Reset()
	}

	for ev := range events {
		switch e := ev.(type) {
		case agent.IterationEvent:
			flush()
			fmt.Printf("\n── iteration %d ──\n", e.Iteration)
		case agent.TokenAgentEvent:
			pending.WriteString(e.Token)
		case agent.ResultAgentEvent:
			flush()
		case agent.ToolCallStartEvent:
			flush()
			if e.Name == "exit" {
				continue
			}
			fmt.Printf("  %s %s\n", e.Name, plainArg(e.Name, e.Args))
			if meta.ShowDiff {
				if d := plainDiff(e.Name, e.Args); d != "" {
					fmt.Print(d)
				}
			}
		case agent.ToolCallEndEvent:
			if s := plainToolEnd(e.Name, e.Result); s != "" {
				fmt.Println(s)
			}
		case agent.ToolCallErrorEvent:
			fmt.Printf("    error: %s: %s\n", e.Name, e.Error)
		case agent.AgentExitEvent:
			flush()
			status := "done"
			if e.Code != 0 {
				status = fmt.Sprintf("failed (code %d)", e.Code)
			}
			fmt.Printf("\n%s: %s\n", status, e.Message)
		}
	}

	if err := <-errs; err != nil {
		return err
	}
	return nil
}

func plainArg(name string, args map[string]interface{}) string {
	switch name {
	case "read", "write", "edit":
		return str(args, "path")
	case "exec":
		return truncate(str(args, "command"), 200)
	case "plan":
		return fmt.Sprintf("%d steps", len(slice(args["steps"])))
	case "progress":
		return str(args, "current")
	default:
		return compactArgs(args)
	}
}

func plainToolEnd(name string, result interface{}) string {
	m, ok := result.(map[string]interface{})
	if !ok {
		return ""
	}
	if success, present := m["success"].(bool); present && !success {
		if e := str(m, "error"); e != "" {
			return "    error: " + truncate(e, 200)
		}
	}
	switch name {
	case "read":
		if n, ok := intish(m["totalLines"]); ok {
			return fmt.Sprintf("    %d lines", n)
		}
	case "exec":
		out := strings.TrimRight(str(m, "stdout"), "\n")
		if out == "" {
			return ""
		}
		lines := strings.Split(out, "\n")
		if len(lines) > maxOutputLines {
			lines = lines[:maxOutputLines]
		}
		var b strings.Builder
		for _, l := range lines {
			b.WriteString("    | " + l + "\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}
	return ""
}

// plainDiff renders an unstyled unified diff (no colour, no box) for log output.
func plainDiff(name string, args map[string]interface{}) string {
	var path, oldText, newText string
	switch name {
	case "edit":
		path, oldText, newText = str(args, "path"), str(args, "oldString"), str(args, "newString")
	case "write":
		path, oldText, newText = str(args, "path"), "", str(args, "content")
	default:
		return ""
	}
	if oldText == newText {
		return ""
	}

	oldLines, newLines := splitLines(oldText), splitLines(newText)
	ops := collapseContext(lineDiff(oldLines, newLines), diffContext)

	var b strings.Builder
	b.WriteString("    --- " + path + "\n")
	shown := 0
	for _, op := range ops {
		if shown >= maxDiffLines {
			break
		}
		switch op.kind {
		case diffGap:
			b.WriteString("      ⋯\n")
		case diffEqual:
			b.WriteString("      " + lineAt(newLines, op.newIdx) + "\n")
			shown++
		case diffDelete:
			b.WriteString("    - " + lineAt(oldLines, op.oldIdx) + "\n")
			shown++
		case diffInsert:
			b.WriteString("    + " + lineAt(newLines, op.newIdx) + "\n")
			shown++
		}
	}
	return b.String()
}
