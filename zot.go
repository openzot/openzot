// Package zot is the embeddable core of the zot automated software factory. It
// turns a single plain-English task into an agentic run - plan, act, observe,
// exit - and renders the run in a read-only terminal UI.
//
// The agentic loop is zot's own (see the agent package): thread assembly,
// compaction, loop detection and the tool-call cycle all run locally, against
// any OpenAI-compatible provider. zot needs no account and no hosted engine.
//
// The standalone binary is cmd/zot; an embedding program can import this package
// and call Run directly while zot's internals stay internal.
package zot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openzot/openzot/agent"

	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/tui"
	"github.com/openzot/openzot/internal/version"
)

// Names zot looks for under each context directory.
const (
	agentFile      = "AGENT.md"
	projectContext = "# Project context"
)

// skillSubdirs are the folder names searched for skills under each context
// directory. Both the hidden ".skills" (typical at a project root) and the plain
// "skills" (e.g. directly in the config directory) are accepted.
var skillSubdirs = []string{".skills", "skills"}

// Config is the fully-resolved zot configuration. Load it with Load; its
// Validate method enforces the same checks the standalone binary runs.
type Config = config.Config

// DefaultInstructions is the system prompt handed to the agent when the
// configuration does not override it. It establishes the fully-autonomous,
// no-questions-asked contract: zot has no input channel, so the agent must
// never wait for the user.
//
// The opening and the "act, don't narrate" rule imitate the batch-mode prompt of
// the engine zot was derived from: a run is a non-interactive background session
// whose deliverable is the changed working tree, not prose, and which ends only
// by recording an outcome with a terminal tool.
const DefaultInstructions = `You are zot, a fully autonomous software engineering agent operating inside a real working directory on the user's machine.

This is a non-interactive session running in the background. No one is watching, and no questions or further guidance can be answered - you will receive NO further input. Complete the assigned task end to end on your own, using your tools.

Your tools:
- "plan": lay out an ordered plan before you start, and revise it whenever your approach changes.
- "read" and "list": inspect files and directories before you change them.
- "write": create a file, or replace part of one.
- "shell": run builds, tests, linters and any other non-interactive command. Never run interactive or long-lived ones.
- "progress": record what you have done and what is left, so your state stays visible on a long run.

Operating rules:
- Begin by calling "plan" to lay out concrete, ordered steps.
- Read before you write. After you change anything, build and run the tests, and fix what you broke.
- Call "progress" as you complete steps.
- Act, do not narrate. The deliverable is the changed working tree, not an explanation of it; there is no reader to address. Do not pause to summarise, interpret, or analyse tool output - keep working, and use "progress" for status.
- Make reasonable assumptions instead of stopping. Do not ask for clarification.
- The task is not finished until you record an outcome: call "_success" with a summary when the objective is met, or "_failure" with the reason when it genuinely cannot be. Do not simply stop.`

// taskHeading introduces the task inside the instructions. The task lives in the
// system prompt rather than as a user message so it survives compaction: a user
// message can be summarised away on a long run, and an autonomous agent that
// forgets its own objective is the worst way for a run to fail. The instructions
// are never summarised and always ordered first.
const taskHeading = "\n\n## Your task\n\n"

// taskKickoff is the user message that starts a run when the caller gave no
// prompt of its own. The objective is in the instructions; this only has to get the
// agent moving.
const taskKickoff = "Begin working on your task. Start by calling the plan tool to lay out your approach, then carry it through to completion."

// withTask appends the task to the instructions, or returns them unchanged when
// there is no task.
func withTask(instructions, task string) string {
	task = strings.TrimSpace(task)
	if task == "" {
		return instructions
	}

	return instructions + taskHeading + task
}

// Version reports the build version of the linked zot core.
func Version() string { return version.Version }

// Load reads configuration, layering defaults < file < env. A missing default
// file is fine (env vars alone can configure zot).
func Load(path string) (Config, error) { return config.Load(path) }

// DefaultConfigPath is the default config file location.
func DefaultConfigPath() string { return config.DefaultConfigPath() }

// LoadProjectContext augments cfg with on-disk context discovered under the
// given directories, searched in order (typically the config directory first,
// then the working directory):
//
//   - <dir>/AGENT.md  - appended to the agent instructions
//   - <dir>/skills/   - loaded via the SDK and added as a "skills" feature
//
// Missing files and directories are ignored, and duplicate directories are
// searched once. AGENT.md content augments (never replaces) the base instructions.
func LoadProjectContext(cfg *Config, dirs ...string) error {
	seen := map[string]bool{}
	var search []string
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		search = append(search, d)
	}

	base := cfg.Agent.Instructions
	if base == "" {
		base = DefaultInstructions
	}

	var instructions []string
	var skillDirs []string
	for _, d := range search {
		if data, err := os.ReadFile(filepath.Join(d, agentFile)); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				instructions = append(instructions, s)
			}
		}
		for _, sub := range skillSubdirs {
			skillDirs = append(skillDirs, filepath.Join(d, sub))
		}
	}

	if len(instructions) > 0 {
		cfg.Agent.Instructions = base + "\n\n" + projectContext + "\n\n" + strings.Join(instructions, "\n\n---\n\n")
	}

	res, err := agent.LoadSkills(skillDirs)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	// @note skills are described in the system prompt rather than shipped to a
	// server as a feature. The engine renders them locally now, so a skill is
	// inert text plus a path the model may choose to read.
	cfg.Skills = append(cfg.Skills, res.Skills...)

	return nil
}

// RunOptions configures a run beyond the configuration itself.
type RunOptions struct {
	// SessionDir is where the run's log is written. Empty disables recording.
	SessionDir string

	// Resume seeds the conversation from an earlier session, so a run that ran
	// out of budget or died overnight continues rather than starting again.
	Resume *session.Session

	// OnSession is called once the log is open, with its path. Used to tell the
	// operator where the record is before the run takes over the screen.
	OnSession func(path string)

	// Prompt is an optional user message that opens the run, distinct from the
	// task. The task (in the instructions) is the durable objective; the prompt is
	// an ephemeral nudge - "start with the error handling" - and may be
	// compacted away later, which is fine because it is not the objective.
	Prompt string
}

// Run executes one autonomous coding task, rendering the agent's activity in the
// read-only TUI. The agent's file and shell tools operate on the current working
// directory, so callers should chdir into the target project first. Run blocks
// until the user quits the viewer or the program errors.
func Run(ctx context.Context, cfg Config, task string) error {
	return RunWith(ctx, cfg, task, RunOptions{})
}

// RunWith is Run with session recording and resume.
func RunWith(ctx context.Context, cfg Config, task string, options RunOptions) error {
	config.ScrubBackendSecrets(cfg)

	client, opts, err := resolve(cfg, DefaultInstructions)
	if err != nil {
		return err
	}

	// A resumed run replays the earlier conversation and then adds the new
	// instruction, so the agent picks up with everything it already knew rather
	// than rediscovering it.
	if options.Resume != nil {
		opts.Messages = options.Resume.AgentMessages()
	}

	// The task is the durable objective and goes into the system prompt; the
	// opening user message is the prompt if the caller gave one, otherwise a
	// kickoff. This is what keeps the objective in context however long the run
	// grows - see withTask.
	opts.Instructions = withTask(opts.Instructions, task)

	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" {
		prompt = taskKickoff
	}

	opts.Messages = append(opts.Messages, agent.Message{Type: agent.TypeUser, Text: prompt})

	workdir, _ := os.Getwd()

	if options.SessionDir != "" {
		meta := session.Meta{
			Task:     task,
			Model:    client.Model(),
			Provider: client.Provider(),
			Backend:  cfg.DefaultBackend,
			Workdir:  workdir,
		}

		if options.Resume != nil {
			meta.ResumedFrom = options.Resume.Meta.ID
		}

		writer, err := session.Start(options.SessionDir, time.Now(), meta)

		// @note a log that cannot be opened is reported but not fatal: the run
		// is the point, and refusing to work because a directory is read-only
		// would be a worse failure than losing the record of it.
		if err != nil {
			fmt.Fprintf(os.Stderr, "zot: session log unavailable: %v\n", err)
		} else {
			defer writer.Close()

			if options.OnSession != nil {
				options.OnSession(writer.Path())
			}

			opts.Recorder = session.NewRecorder(writer)
		}
	}

	return tui.Run(ctx, client, tui.Meta{
		Task:     task,
		Model:    cfg.Agent.Model,
		Backend:  cfg.DefaultBackend,
		Workdir:  workdir,
		ShowDiff: cfg.UI.Diff,
		Plain:    cfg.UI.Plain,
	}, opts)
}

// resolve turns a configuration into a provider client and the agent options a
// run uses. The returned options carry no messages; callers supply those.
func resolve(cfg Config, defaultInstructions string) (*agent.Client, agent.ExecuteWithToolsOptions, error) {
	var empty agent.ExecuteWithToolsOptions

	backend, ok := cfg.Backends[cfg.DefaultBackend]
	if !ok {
		return nil, empty, fmt.Errorf("backend %q is not configured", cfg.DefaultBackend)
	}

	// Resolve the model against the backend's custom model definitions. A custom
	// entry's settings take priority over the run defaults.
	model := cfg.Agent.Model
	maxIterations := cfg.Agent.MaxIterations
	provider := config.BackendProvider(cfg.DefaultBackend, backend)
	credential := config.BackendCredential(backend)

	if mc, ok := backend.Models[model]; ok {
		if mc.Model != "" {
			model = mc.Model
		}
		if mc.MaxIterations > 0 {
			maxIterations = mc.MaxIterations
		}
		if mc.Provider != "" {
			provider = mc.Provider
		}
		if mc.APIKey != "" {
			credential = mc.APIKey
		}
	}

	if provider == "" {
		return nil, empty, fmt.Errorf(
			"backend %q does not name a model provider (set provider: on the backend or the model - one of %s)",
			cfg.DefaultBackend, strings.Join(agent.Providers(), ", "))
	}

	instructions := cfg.Agent.Instructions
	if instructions == "" {
		instructions = defaultInstructions
	}

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: provider,
		Model:    model,
		APIKey:   credential,
		BaseURL:  backend.BaseURL,
	})
	if err != nil {
		return nil, empty, fmt.Errorf("backend %q: %w", cfg.DefaultBackend, err)
	}

	// max_time was validated at load, so a parse error here would be a bug; treat
	// it as unbounded rather than failing a run that already passed validation.
	maxDuration, _ := cfg.Agent.MaxDuration()

	opts := agent.ExecuteWithToolsOptions{
		Instructions:     instructions,
		Tools:            agent.DefaultTools(),
		Skills:           cfg.Skills,
		MaxIterations:    maxIterations,
		MaxSettles:       cfg.Agent.MaxSettles,
		MaxCalls:         cfg.Agent.MaxCalls,
		MaxContinuations: cfg.Agent.MaxContinuations,
		MaxCycles:        cfg.Agent.MaxCycles,
		MaxEmpties:       cfg.Agent.MaxEmpties,
		MaxDuration:      maxDuration,
		LimitCheckpoints: cfg.Agent.LimitCheckpoints,
		// Empty is the default (compact); the agent layer resolves the string.
		ContextStrategy:     cfg.Agent.ContextStrategy,
		CompactMinTokens:    cfg.Agent.CompactMinTokens,
		CompactMinMessages:  cfg.Agent.CompactMinMessages,
		CompactTriggerRatio: cfg.Agent.CompactTriggerRatio,
	}

	// MaxTokens is a pointer so that "unset" (provider decides) is distinct from
	// a deliberate zero; the config uses a positive value to mean "cap here".
	if cfg.Agent.MaxTokens > 0 {
		limit := cfg.Agent.MaxTokens
		opts.MaxTokens = &limit
	}

	return client, opts, nil
}
