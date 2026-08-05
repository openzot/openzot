// Package zot is the embeddable core of the zot automated software factory. It
// turns a single plain-English task into an agentic run - plan, act, observe,
// exit - driven entirely by the ChatBotKit Go SDK's agent package, and renders
// the run in a read-only terminal UI.
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

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"
	"github.com/chatbotkit/go-sdk/types"

	"github.com/chatbotkit/zot/internal/acp"
	"github.com/chatbotkit/zot/internal/config"
	"github.com/chatbotkit/zot/internal/tui"
	"github.com/chatbotkit/zot/internal/version"
)

// Names zot looks for under each context directory.
const (
	agentFile      = "AGENT.md"
	skillsFeature  = "skills"
	projectContext = "# Project context"
)

// skillSubdirs are the folder names searched for skills under each context
// directory. Both the hidden ".skills" (typical at a project root) and the plain
// "skills" (e.g. directly in the config directory) are accepted.
var skillSubdirs = []string{".skills", "skills"}

// Config is the fully-resolved zot configuration. Load it with Load; its
// Validate method enforces the same checks the standalone binary runs.
type Config = config.Config

// Feature is a ChatBotKit conversation feature (a name/options pair) enabled for
// the run.
type Feature = config.Feature

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

// DefaultACPBackstory is the system instruction for ACP mode, used when the
// configuration does not override it. It differs from DefaultBackstory in the
// one way that matters: an ACP session is a conversation, so the agent is
// finishing a turn rather than a whole engagement, and the client - not zot -
// decides what comes next.
const DefaultACPBackstory = `You are zot, an autonomous software engineering agent working inside a real directory on the user's machine. A client drives you over the Agent Client Protocol.

Each message you receive is one turn of an ongoing conversation, and more may follow. Work the request end to end with your tools before the turn ends.

Operating rules:
- Use "plan" whenever the request takes more than a couple of steps, and "progress" as you complete them.
- Use "read" to understand existing code before changing it. Prefer "edit" for surgical changes and "write" for new files.
- Use "exec" to run builds, tests, linters, scaffolding and any non-interactive shell command. Never run interactive or long-lived commands.
- Verify your work: after making changes, build and/or run the tests and fix what you broke.
- The client may include its own instructions in a message - for example how to post a reply back to a chat channel. Follow them; they take priority over these defaults.
- Call "exit" with code 0 once the turn's work is done, or a non-zero code if you genuinely could not proceed, with a short summary as the message.
- You cannot block waiting for an answer mid-turn. Make reasonable assumptions instead; if something is truly ambiguous, say so in your reply and end the turn.`

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
	if skills := res.GetSkills(); len(skills) > 0 {
		feature := agent.CreateSkillsFeature(skills)
		options, _ := feature["options"].(map[string]interface{})
		cfg.Features = append(cfg.Features, config.Feature{Name: skillsFeature, Options: options})
	}

	return nil
}

// Run executes one autonomous coding task, rendering the agent's activity in the
// read-only TUI. The agent's file and shell tools operate on the current working
// directory, so callers should chdir into the target project first. Run blocks
// until the user quits the viewer or the program errors.
func Run(ctx context.Context, cfg Config, task string) error {
	config.ScrubBackendSecrets(cfg)

	client, opts, err := resolve(cfg, DefaultBackstory)
	if err != nil {
		return err
	}
	opts.Messages = []agent.Message{{Type: "user", Text: task}}

	workdir, _ := os.Getwd()

	return tui.Run(ctx, client, tui.Meta{
		Task:     task,
		Model:    cfg.Agent.Model,
		Backend:  cfg.DefaultBackend,
		Workdir:  workdir,
		ShowDiff: cfg.UI.Diff,
		Plain:    cfg.UI.Plain,
	}, opts)
}

// ServeACP serves the agent over the Agent Client Protocol on stdin/stdout,
// until the client disconnects or ctx is cancelled. There is no viewer and no
// task: an ACP client - an editor, or a harness like Buzz's buzz-acp - opens
// sessions and drives them turn by turn.
//
// Unlike Run, the working directory comes from the client with each session, so
// callers must not chdir first. configDir is where global AGENT.md and skills
// live; each session layers its own workspace context on top. Diagnostics go to
// logf, which must not write to stdout - the protocol owns it.
func ServeACP(ctx context.Context, cfg Config, configDir string, logf func(string, ...any)) error {
	config.ScrubBackendSecrets(cfg)

	// Fail fast on credentials and model resolution, rather than at the first
	// prompt when a client is already connected.
	client, _, err := resolve(cfg, DefaultACPBackstory)
	if err != nil {
		return err
	}

	return acp.Serve(ctx, acp.Options{
		Client:  client,
		Name:    "zot",
		Version: version.Version,
		Log:     logf,
		Prepare: func(cwd string) (agent.ExecuteWithToolsOptions, error) {
			// Resolve project context per session so a client working across
			// several repositories gets each one's AGENT.md and skills.
			sessionCfg := cfg
			sessionCfg.Features = append([]config.Feature{}, cfg.Features...)
			if err := LoadProjectContext(&sessionCfg, configDir, cwd); err != nil {
				return agent.ExecuteWithToolsOptions{}, err
			}
			_, opts, err := resolve(sessionCfg, DefaultACPBackstory)
			return opts, err
		},
	})
}

// resolve turns a configuration into a backend client and the agent options a
// run uses. The returned options carry no messages; callers supply those.
func resolve(cfg Config, defaultBackstory string) (*sdk.Client, agent.ExecuteWithToolsOptions, error) {
	backend, ok := cfg.Backends[cfg.DefaultBackend]
	if !ok {
		return nil, agent.ExecuteWithToolsOptions{}, fmt.Errorf("backend %q is not configured", cfg.DefaultBackend)
	}

	// Resolve the model against the backend's custom model definitions. A custom
	// entry's settings take priority over the run defaults.
	model := cfg.Agent.Model
	maxIterations := cfg.Agent.MaxIterations
	features := cfg.Features
	auth := backend.Authorization
	if mc, ok := backend.Models[model]; ok {
		if mc.Model != "" {
			model = mc.Model
		}
		if mc.MaxIterations > 0 {
			maxIterations = mc.MaxIterations
		}
		if mc.Authorization != "" {
			auth = mc.Authorization
		}
		features = append(append([]config.Feature{}, features...), mc.Features...)
	}

	// Turn the backend choice into concrete client auth. The relay authenticates
	// each provider per model, inside the model string; the ChatBotKit backends
	// send a Bearer credential.
	switch config.BackendStyle(cfg.DefaultBackend) {
	case config.AuthModelParam:
		// Respect a key the caller already inlined into --model.
		if !strings.Contains(model, "/authorization=") {
			if auth == "" {
				return nil, agent.ExecuteWithToolsOptions{}, fmt.Errorf(
					"no authorization for model %q on backend %q (set authorization on the model or the backend in config, or inline it as %s/authorization=KEY)",
					model, cfg.DefaultBackend, model)
			}
			model = model + "/authorization=" + auth
		}
	default: // AuthBearer
		if backend.APISecret == "" {
			return nil, agent.ExecuteWithToolsOptions{}, fmt.Errorf(
				"no API secret for backend %q (set %s in the environment or api_secret in config)",
				cfg.DefaultBackend, config.SecretEnvName(cfg.DefaultBackend))
		}
	}

	backstory := cfg.Agent.Backstory
	if backstory == "" {
		backstory = defaultBackstory
	}

	client := sdk.New(sdk.Options{
		Secret:  backend.APISecret,
		BaseURL: backend.BaseURL,
	})

	opts := agent.ExecuteWithToolsOptions{
		Model:         model,
		Backstory:     backstory,
		Tools:         agent.DefaultTools(),
		MaxIterations: maxIterations,
	}
	if feats := sdkFeatures(features); len(feats) > 0 {
		opts.Extensions = &types.ConversationCompleteRequestExtensions{Features: feats}
	}

	return client, opts, nil
}

// sdkFeatures converts the configured features into the SDK's feature type.
func sdkFeatures(features []config.Feature) []types.CompleteFeature {
	if len(features) == 0 {
		return nil
	}
	out := make([]types.CompleteFeature, 0, len(features))
	for _, f := range features {
		out = append(out, types.CompleteFeature{Name: f.Name, Options: f.Options})
	}
	return out
}
