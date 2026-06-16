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

	"github.com/chatbotkit/go-sdk/agent"
	"github.com/chatbotkit/go-sdk/sdk"

	"github.com/chatbotkit/zot/internal/config"
	"github.com/chatbotkit/zot/internal/tui"
	"github.com/chatbotkit/zot/internal/version"
)

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

	return tui.Run(ctx, client, tui.Meta{
		Task:     task,
		Model:    cfg.Agent.Model,
		Workdir:  workdir,
		ShowDiff: cfg.UI.Diff,
		Plain:    cfg.UI.Plain,
	}, opts)
}
