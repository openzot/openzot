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

// DefaultBackstory is the system instruction handed to the agent when the
// configuration does not override it. It establishes the fully-autonomous,
// no-questions-asked contract: zot has no input channel, so the agent must
// never wait for the user.
const DefaultBackstory = `You are zot, a fully autonomous software engineering agent operating inside a real working directory on the user's machine.

You have NO way to ask the user questions and you will receive NO further input. You must complete the task end to end on your own using your tools.

Operating rules:
- Begin by calling the "plan" tool to lay out concrete, ordered steps.
- Use "read" to understand existing code before changing it. Prefer "edit" for surgical changes and "write" for new files.
- Use "exec" to run builds, tests, linters, scaffolding and any non-interactive shell command. Never run interactive or long-lived commands.
- Verify your work: after making changes, build and/or run the tests and fix what you broke.
- Call "progress" as you complete steps so your reasoning is visible.
- When the task is genuinely done (or truly cannot proceed), call "exit" with code 0 for success or a non-zero code for failure, and a short summary message.
- Make reasonable assumptions instead of stopping. Do not ask for clarification.`

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
//   - <dir>/AGENT.md  - appended to the agent backstory
//   - <dir>/skills/   - loaded via the SDK and added as a "skills" feature
//
// Missing files and directories are ignored, and duplicate directories are
// searched once. AGENT.md content augments (never replaces) the base backstory.
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

	base := cfg.Agent.Backstory
	if base == "" {
		base = DefaultBackstory
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
		cfg.Agent.Backstory = base + "\n\n" + projectContext + "\n\n" + strings.Join(instructions, "\n\n---\n\n")
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

	client, opts, err := resolve(cfg, DefaultBackstory)
	if err != nil {
		return err
	}

	// A resumed run replays the earlier conversation and then adds the new
	// instruction, so the agent picks up with everything it already knew rather
	// than rediscovering it.
	if options.Resume != nil {
		opts.Messages = options.Resume.AgentMessages()
	}

	opts.Messages = append(opts.Messages, agent.Message{Type: agent.TypeUser, Text: task})

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
func resolve(cfg Config, defaultBackstory string) (*agent.Client, agent.ExecuteWithToolsOptions, error) {
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

	backstory := cfg.Agent.Backstory
	if backstory == "" {
		backstory = defaultBackstory
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

	opts := agent.ExecuteWithToolsOptions{
		Backstory:     backstory,
		Tools:         agent.DefaultTools(),
		Skills:        cfg.Skills,
		MaxIterations: maxIterations,
	}

	return client, opts, nil
}
