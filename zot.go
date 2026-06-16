// Package zot is the embeddable core of the zot autonomous coding agent. It
// turns a single plain-English task into an agentic run — plan, act, observe,
// exit — driven entirely by the ChatBotKit Go SDK's agent package, and renders
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
//   - <dir>/AGENT.md  — appended to the agent backstory
//   - <dir>/skills/   — loaded via the SDK and added as a "skills" feature
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
	if cfg.ChatBotKit.APISecret == "" {
		return fmt.Errorf("ChatBotKit API secret is not set (export CHATBOTKIT_API_SECRET or set chatbotkit.api_secret)")
	}

	client := sdk.New(sdk.Options{
		Secret:  cfg.ChatBotKit.APISecret,
		BaseURL: cfg.ChatBotKit.BaseURL,
	})

	backstory := cfg.Agent.Backstory
	if backstory == "" {
		backstory = DefaultBackstory
	}

	workdir, _ := os.Getwd()

	opts := agent.ExecuteWithToolsOptions{
		Model:         cfg.Agent.Model,
		Messages:      []agent.Message{{Type: "user", Text: task}},
		Backstory:     backstory,
		Tools:         agent.DefaultTools(),
		MaxIterations: cfg.Agent.MaxIterations,
	}
	if feats := sdkFeatures(cfg.Features); len(feats) > 0 {
		opts.Extensions = &types.ConversationCompleteRequestExtensions{Features: feats}
	}

	return tui.Run(ctx, client, tui.Meta{
		Task:     task,
		Model:    cfg.Agent.Model,
		Workdir:  workdir,
		ShowDiff: cfg.UI.Diff,
		Plain:    cfg.UI.Plain,
	}, opts)
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
