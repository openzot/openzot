package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultTools returns the standard filesystem and shell tool set.
//
// These run with the privileges of the process. That is the point - an agent
// that cannot touch the machine is not much use to a CLI - but it means the
// caller decides what to expose, and a caller running untrusted instructions
// should hand over a narrower set.
func DefaultTools() Tools {
	return Tools{
		"read": {
			Description: "Read the contents of a file. Supports an optional line range to read a specific section.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "The file path to read"},
					"startLine": map[string]any{"type": "integer", "description": "First line to read, 1-indexed"},
					"endLine":   map[string]any{"type": "integer", "description": "Last line to read, inclusive, 1-indexed"},
				},
				"required": []string{"path"},
			},
			Handler: readHandler,
		},

		"write": {
			Description: "Write content to a file. Without line parameters the whole file is replaced. With startLine only, content is inserted before that line. With both, the range is replaced.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "The file path to write"},
					"content":   map[string]any{"type": "string", "description": "The content to write"},
					"startLine": map[string]any{"type": "integer", "description": "First line to write at, 1-indexed"},
					"endLine":   map[string]any{"type": "integer", "description": "Last line to replace, inclusive, 1-indexed"},
				},
				"required": []string{"path", "content"},
			},
			Handler: writeHandler,
		},

		"list": {
			Description: "List the entries of a directory.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "The directory to list"},
				},
				"required": []string{"path"},
			},
			Handler: listHandler,
		},

		"shell": {
			Description: "Run a shell command and return its combined output.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "The command to run"},
					"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds, default 120"},
				},
				"required": []string{"command"},
			},
			Handler: shellHandler,
		},
	}
}

// maxToolOutput bounds what a tool may return.
//
// An unbounded result is a context-window hazard: one `cat` of a large file can
// consume the whole budget and evict the conversation that explains why it was
// read. Truncation is visible so the model knows it is seeing a fragment.
const maxToolOutput = 100_000

func truncate(text string) string {
	if len(text) <= maxToolOutput {
		return text
	}

	return text[:maxToolOutput] + fmt.Sprintf("\n\n[truncated: %d bytes total]", len(text))
}

func stringArg(args map[string]any, key string) (string, error) {
	value, ok := args[key].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("missing required argument %q", key)
	}

	return value, nil
}

func intArg(args map[string]any, key string) (int, bool) {
	switch value := args[key].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}

func readHandler(_ context.Context, args map[string]any) (any, error) {
	path, err := stringArg(args, "path")
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	start, hasStart := intArg(args, "startLine")
	end, hasEnd := intArg(args, "endLine")

	if !hasStart && !hasEnd {
		return truncate(string(content)), nil
	}

	lines := strings.Split(string(content), "\n")

	if !hasStart || start < 1 {
		start = 1
	}

	if !hasEnd || end > len(lines) {
		end = len(lines)
	}

	if start > len(lines) || end < start {
		return "", nil
	}

	return truncate(strings.Join(lines[start-1:end], "\n")), nil
}

func writeHandler(_ context.Context, args map[string]any) (any, error) {
	path, err := stringArg(args, "path")
	if err != nil {
		return nil, err
	}

	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("missing required argument %q", "content")
	}

	start, hasStart := intArg(args, "startLine")

	if !hasStart {
		if directory := filepath.Dir(path); directory != "" {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				return nil, err
			}
		}

		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil, err
		}

		return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
	}

	existing, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(existing), "\n")

	if start < 1 {
		start = 1
	}

	if start > len(lines)+1 {
		start = len(lines) + 1
	}

	end, hasEnd := intArg(args, "endLine")

	if !hasEnd || end < start {
		end = start - 1
	}

	if end > len(lines) {
		end = len(lines)
	}

	updated := make([]string, 0, len(lines)+1)
	updated = append(updated, lines[:start-1]...)
	updated = append(updated, strings.Split(content, "\n")...)
	updated = append(updated, lines[end:]...)

	if err := os.WriteFile(path, []byte(strings.Join(updated, "\n")), 0o644); err != nil {
		return nil, err
	}

	return fmt.Sprintf("updated %s", path), nil
}

func listHandler(_ context.Context, args map[string]any) (any, error) {
	path, err := stringArg(args, "path")
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var builder strings.Builder

	for _, entry := range entries {
		if entry.IsDir() {
			fmt.Fprintf(&builder, "%s/\n", entry.Name())
		} else {
			fmt.Fprintf(&builder, "%s\n", entry.Name())
		}
	}

	return truncate(builder.String()), nil
}

func shellHandler(ctx context.Context, args map[string]any) (any, error) {
	command, err := stringArg(args, "command")
	if err != nil {
		return nil, err
	}

	timeout := 120 * time.Second

	if seconds, ok := intArg(args, "timeout"); ok && seconds > 0 {
		timeout = time.Duration(seconds) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)

	defer cancel()

	shell, flag := "/bin/sh", "-c"

	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}

	output, err := exec.CommandContext(ctx, shell, flag, command).CombinedOutput()

	// @note a non-zero exit is returned to the model as output rather than as an
	// error. A failing command is information - a compiler error, a failing test -
	// and the model is usually the thing best placed to act on it.
	if err != nil && ctx.Err() == nil {
		return truncate(fmt.Sprintf("%s\n[exit: %v]", output, err)), nil
	}

	if ctx.Err() != nil {
		return truncate(fmt.Sprintf("%s\n[timed out after %s]", output, timeout)), nil
	}

	return truncate(string(output)), nil
}
