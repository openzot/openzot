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

	"github.com/openzot/openzot/internal/imaging"
	"github.com/openzot/openzot/internal/provider"
)

// DefaultMaxToolOutput is the byte ceiling on a single tool result when the
// caller does not set its own. See toolSet.truncate for why a bound exists.
const DefaultMaxToolOutput = 100_000

// DefaultTools returns the standard filesystem and shell tool set, with the
// default output ceiling.
//
// These run with the privileges of the process. That is the point - an agent
// that cannot touch the machine is not much use to a CLI - but it means the
// caller decides what to expose, and a caller running untrusted instructions
// should hand over a narrower set.
func DefaultTools() Tools {
	return DefaultToolsWith(DefaultMaxToolOutput)
}

// DefaultToolsWith returns the standard tool set with a specific ceiling on a
// single tool result. A model served by an endpoint with a small context
// window needs a tighter bound than one with a large one - a single result
// that overflows the window is rejected wholesale, and the run cannot recover
// from a message it cannot even send. Zero or negative uses the default.
func DefaultToolsWith(maxOutput int) Tools {
	return DefaultToolsFor(ToolOptions{MaxOutput: maxOutput})
}

// ToolOptions configures the standard tool set.
type ToolOptions struct {
	// MaxOutput is the ceiling on a single tool result. Zero uses the default.
	MaxOutput int

	// Vision offers the model a tool for looking at images.
	//
	// Off by default, and deliberately expressed by leaving the tool out rather
	// than by refusing calls to it: a tool the model cannot use is a tool it
	// will try anyway, spending a round to find out. What a model can be shown
	// is not something zot can discover at runtime, so the caller says - from
	// the catalogue, or from what the operator configured for the model.
	Vision bool
}

// DefaultToolsFor returns the standard tool set for a set of options.
func DefaultToolsFor(options ToolOptions) Tools {
	maxOutput := options.MaxOutput

	if maxOutput <= 0 {
		maxOutput = DefaultMaxToolOutput
	}

	s := toolSet{maxOutput: maxOutput, vision: options.Vision}

	tools := Tools{
		"read": {
			Description: "Read a range of lines from a file. startLine and endLine are required: read a bounded section, not the whole file, so a large file cannot flood the context. The result is line-numbered and reports the file's total length, so you can read a further range to see more.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "The file path to read"},
					"startLine": map[string]any{"type": "integer", "description": "First line to read, 1-indexed"},
					"endLine":   map[string]any{"type": "integer", "description": "Last line to read, inclusive, 1-indexed"},
				},
				"required": []string{"path", "startLine", "endLine"},
			},
			Handler: s.read,
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
			Handler: s.write,
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
			Handler: s.list,
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
			Handler: s.shell,
		},

		"plan": {
			Description: "Lay out an ordered plan for the task. Call this at the start, and again whenever you change approach. The plan is recorded so your strategy is visible and you can hold yourself to it.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"steps": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "The steps, in the order you will do them",
					},
					"rationale": map[string]any{"type": "string", "description": "Why this approach"},
				},
				"required": []string{"steps"},
			},
			Handler: planHandler,
		},

		"progress": {
			Description: "Record progress on the task: what is done, what you are doing now, and anything blocking you. Call this as you complete steps so your state stays visible across a long run.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"completed": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Steps finished so far"},
					"current":   map[string]any{"type": "string", "description": "What you are working on now"},
					"blockers":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Anything preventing progress"},
					"nextSteps": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "What comes next"},
				},
			},
			Handler: progressHandler,
		},
	}

	if options.Vision {
		tools["view"] = ToolDefinition{
			Description: "Look at an image file - a screenshot, a diagram, a rendering - and see it rather than reason about its bytes. Use it to check that something you produced actually looks right.",
			Parameters: FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "The image file to look at"},
					"detail": map[string]any{"type": "string", "description": "Resolution hint: auto, low or high", "enum": []string{"auto", "low", "high"}},
				},
				"required": []string{"path"},
			},
			Handler: s.view,
		}
	}

	return tools
}

// planHandler records the model's plan.
//
// Plan and progress are reflective tools: they change nothing on disk. Their
// value is that the model has to state its approach and its status in a
// structured form, which both organises its own reasoning and makes the run
// followable in the viewer and the session log. The step content lives in the
// call's arguments; the result only has to acknowledge it.
func planHandler(_ context.Context, args map[string]any) (any, error) {
	steps, _ := args["steps"].([]any)

	if len(steps) == 0 {
		return nil, fmt.Errorf("a plan needs at least one step")
	}

	message := fmt.Sprintf("plan recorded: %d step(s)", len(steps))

	if rationale, _ := args["rationale"].(string); rationale != "" {
		message += " - " + rationale
	}

	return message, nil
}

// progressHandler records a progress checkpoint. Like plan, the content is in
// the arguments and the result is an acknowledgement.
func progressHandler(_ context.Context, args map[string]any) (any, error) {
	if current, _ := args["current"].(string); current != "" {
		return "progress recorded: " + current, nil
	}

	return "progress recorded", nil
}

// toolSet carries the configuration the filesystem and shell tools share -
// currently just the output ceiling. The handlers are its methods so the
// ceiling is captured per tool set rather than read from a package global,
// which a per-run or per-model override could not vary.
type toolSet struct {
	maxOutput int

	// vision is whether this run's model can be shown images. read consults it
	// to explain itself when it is handed one.
	vision bool
}

// truncate bounds what a tool may return.
//
// An unbounded result is a context-window hazard: one read of a large file can
// consume the whole budget and evict the conversation that explains why it was
// read - or, on an endpoint with a small window, be rejected wholesale so the
// run cannot even send it. Truncation is visible so the model knows it is
// seeing a fragment.
func (s toolSet) truncate(text string) string {
	if len(text) <= s.maxOutput {
		return text
	}

	return text[:s.maxOutput] + fmt.Sprintf("\n\n[truncated: %d bytes total]", len(text))
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

func (s toolSet) read(_ context.Context, args map[string]any) (any, error) {
	path, err := stringArg(args, "path")
	if err != nil {
		return nil, err
	}

	// The range is required, not optional. An optional range invites the model
	// to read whole files by default, and one large file can overflow a small
	// context window and be rejected wholesale. Forcing a bounded range makes
	// the model state what it wants to see and keeps each read small.
	start, hasStart := intArg(args, "startLine")
	end, hasEnd := intArg(args, "endLine")

	if !hasStart || !hasEnd {
		return nil, fmt.Errorf("read requires startLine and endLine: read a bounded range, not the whole file")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// An image split on newlines is mojibake: thousands of replacement
	// characters that cost the same context as real content and tell the model
	// nothing. Say what the file is instead, and where to go from there - which
	// depends on whether this model can be shown one at all.
	if mediaType := imaging.MediaType(content); mediaType != "" {
		description := fmt.Sprintf("%s is a %s image, %d bytes", path, mediaType, len(content))

		if width, height, ok := imaging.Dimensions(content); ok {
			description = fmt.Sprintf("%s is a %s image, %dx%d, %d bytes", path, mediaType, width, height, len(content))
		}

		if s.vision {
			return description + ". Use view to look at it.", nil
		}

		return description + ". This model cannot be shown images, so read cannot render it; " +
			"inspect it with a command instead if you need to know what it contains.", nil
	}

	lines := strings.Split(string(content), "\n")
	total := len(lines)

	if start < 1 {
		start = 1
	}

	if end > total {
		end = total
	}

	if start > total || end < start {
		return fmt.Sprintf("[%s has %d lines; requested range is empty]", path, total), nil
	}

	// Line-numbered so the model can target a precise next range, with a header
	// stating the whole file's length so it knows how much it has not seen.
	var b strings.Builder

	fmt.Fprintf(&b, "[%s lines %d-%d of %d]\n", path, start, end, total)

	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, lines[i-1])
	}

	return s.truncate(b.String()), nil
}

// view attaches an image to the conversation.
//
// The handler returns the bytes and nothing else: it does no I/O beyond reading
// the file, so it stays testable, and it knows nothing about session logs - the
// recorder is what decides where the bytes are kept. Normalisation happens here
// rather than at the wire because the digest, the blob and what the model is
// shown must all be the same object, and downscaling later would make them
// three different ones.
func (s toolSet) view(_ context.Context, args map[string]any) (any, error) {
	path, err := stringArg(args, "path")
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if imaging.MediaType(content) == "" {
		return nil, fmt.Errorf("%s is not an image; use read for text files", path)
	}

	normalized, err := imaging.Normalize(content, imaging.DefaultMaxEdge)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	image := provider.NewImage(normalized.Data, normalized.MediaType, normalized.Width, normalized.Height)
	image.Origin = path

	if detail, _ := args["detail"].(string); detail == "auto" || detail == "low" || detail == "high" {
		image.Detail = detail
	}

	text := fmt.Sprintf("attached %s (%s", path, image.MediaType)

	if image.Width > 0 && image.Height > 0 {
		text += fmt.Sprintf(", %dx%d", image.Width, image.Height)
	}

	text += ")"

	if normalized.Resized {
		text += fmt.Sprintf("; reduced from the file on disk to fit %dpx", imaging.DefaultMaxEdge)
	}

	return ToolResult{Text: text, Images: []provider.Image{image}}, nil
}

func (s toolSet) write(_ context.Context, args map[string]any) (any, error) {
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

func (s toolSet) list(_ context.Context, args map[string]any) (any, error) {
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

	return s.truncate(builder.String()), nil
}

func (s toolSet) shell(ctx context.Context, args map[string]any) (any, error) {
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

	cmd := exec.CommandContext(ctx, shell, flag, command)

	// Killing the shell is not enough. A command that leaves a process behind -
	// `npm start &`, anything that daemonises - hands the inherited output pipe
	// to a grandchild, and reading that pipe blocks until every holder of it is
	// gone. Without a WaitDelay the tool call simply never returns, and nothing
	// upstream can recover: the run's time budget is only checked between
	// iterations, and cancelling the run kills the shell, not the process
	// holding the pipe. WaitDelay gives up on the pipe shortly after the process
	// is killed, so a wedged command costs a timeout instead of the whole run.
	cmd.WaitDelay = 2 * time.Second

	setProcessGroup(cmd)

	output, err := cmd.CombinedOutput()

	// @note a non-zero exit is returned to the model as output rather than as an
	// error. A failing command is information - a compiler error, a failing test -
	// and the model is usually the thing best placed to act on it.
	if err != nil && ctx.Err() == nil {
		return s.truncate(fmt.Sprintf("%s\n[exit: %v]", output, err)), nil
	}

	if ctx.Err() != nil {
		return s.truncate(fmt.Sprintf("%s\n[timed out after %s]", output, timeout)), nil
	}

	return s.truncate(string(output)), nil
}
