// Package zot is the embeddable core of the zot automated software factory. It
// turns a work order - a durable objective, rendered to a task - into an
// agentic run - plan, act, observe, exit - and renders the run in a read-only
// terminal UI.
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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openzot/openzot/agent"

	"github.com/openzot/openzot/internal/catalogue"
	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/version"
	"github.com/openzot/openzot/tui"
)

// Names zot looks for under each context directory.
const (
	agentFile      = "AGENTS.md"
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
//
// It is assembled rather than written out because its closing half is not the
// caller's to drop - see nonInteractiveContract.
const DefaultInstructions = baseInstructions + "\n\n" + nonInteractiveContract

// baseInstructions is the overridable half: who the agent is, what it can call,
// and how it is expected to work. A configuration that sets its own
// instructions replaces this and nothing else.
const baseInstructions = `You are zot, a fully autonomous software engineering agent operating inside a real working directory on the user's machine.

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
- Act, do not narrate. The deliverable is the changed working tree, not an explanation of it; there is no reader to address. Do not pause to summarise, interpret, or analyse tool output - keep working, and use "progress" for status.`

// nonInteractiveContract is the half no configuration may leave out. Every
// other prompt rule is a preference; this one is a fact about the machine the
// agent is running on. zot has no input channel at all - a run is a work order,
// a provider and a read-only viewer - so an agent that asks a question is not
// answered tersely, it is not answered at all: it waits until a guard kills the
// run, and everything it had not yet written is lost. That failure is silent
// and expensive, and it costs a whole run to discover, so the contract is
// re-attached to whatever instructions a run resolves to rather than left to
// whoever wrote them.
//
// It is written to stand alone, naming the terminal tools itself, because the
// custom instructions it may be appended to need not mention them at all.
const nonInteractiveContract = `## Non-interactive contract

Nothing you address to the user is delivered. There is no reader, no reply, and no approval on its way. A question you ask is discarded unheard, and a run that stops to wait for an answer waits until a guard kills it, losing the work it had not yet finished.

- Never stop to wait for input, approval, permission or confirmation. No one can grant what you asked for, so asking and waiting is the one certain way to fail the task.
- Never end your turn with a question, an offer, or a promise to continue once told to. Continue now instead.
- Where the task is ambiguous or underspecified, decide it the way a careful engineer would, act on the decision, and record the assumption with "progress" and again in your final summary. A stated assumption is reviewable afterwards; an unasked question is not.
- Only a terminal tool call ends the task: "success" with a summary when the objective is met, or "failure" with the reason when it genuinely cannot be. Uncertainty is not a reason to stop - it is a reason to choose, act, and say what you chose. Do not simply stop.`

// taskHeading introduces the task inside the instructions. The task lives in the
// system prompt rather than as a user message so it survives compaction: a user
// message can be summarised away on a long run, and an autonomous agent that
// forgets its own objective is the worst way for a run to fail. The instructions
// are never summarised and always ordered first.
const taskHeading = "\n\n## Your task\n\n"

// taskKickoff is the user message that starts a run. The objective is in the
// instructions; this only has to get the agent moving.
const taskKickoff = "Begin working on your task. Start by calling the plan tool to lay out your approach, then carry it through to completion."

// resumeKickoff starts a resumed run. The replayed conversation carries what
// already happened; this points the agent back at the gap between its plan and
// the tree, rather than telling it to begin as though nothing had.
const resumeKickoff = "Continue your task from where the session left off. Reconcile your plan with the current state of the working tree, then carry the task through to completion."

// withNonInteractiveContract guarantees the no-questions contract reaches the
// model whatever the instructions say. Custom instructions replace the built-in
// prompt wholesale - that is what an override is for - but they cannot opt a run
// into an interactivity zot does not have. Instructions that already carry the
// contract (the defaults, or the defaults plus an AGENTS.md) are left untouched,
// so the common path is unchanged and the text is never repeated.
func withNonInteractiveContract(instructions string) string {
	if strings.Contains(instructions, nonInteractiveContract) {
		return instructions
	}

	return strings.TrimRight(instructions, "\n") + "\n\n" + nonInteractiveContract
}

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

// NewClient resolves the configuration into a provider-backed client, for
// engine work outside the main run path - the read-only survey that drafts a
// work order's acceptance criteria, say. A run of record never takes this
// path: it goes through Run or RunWith, which render, log, and record an
// outcome.
func NewClient(cfg Config) (*agent.Client, error) {
	client, _, err := resolve(cfg, DefaultInstructions)

	return client, err
}

// Load reads configuration, layering defaults < file < env. A missing default
// file is fine (env vars alone can configure zot).
func Load(path string) (Config, error) { return config.Load(path) }

// DefaultConfigPath is the default config file location.
func DefaultConfigPath() string { return config.DefaultConfigPath() }

// LoadProjectContext augments cfg with on-disk context discovered under the
// given directories, searched in order (typically the config directory first,
// then the working directory):
//
//   - <dir>/AGENTS.md  - appended to the agent instructions
//   - <dir>/skills/   - loaded via the SDK and added as a "skills" feature
//
// Missing files and directories are ignored, and duplicate directories are
// searched once. AGENTS.md content augments (never replaces) the base instructions.
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

	// Probe the directories once so a broken one - unreadable, say - fails at
	// load rather than being silently skipped mid-run. The scan's result is
	// deliberately discarded: the run rescans the directories at every
	// iteration, so a skill added while the agent works (including by the
	// agent itself) is picked up without a restart.
	if _, err := agent.LoadSkills(skillDirs); err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	// @note skills are described in the system prompt rather than shipped to a
	// server as a feature. The engine renders them locally now, so a skill is
	// inert text plus a path the model may choose to read.
	cfg.SkillDirectories = append(cfg.SkillDirectories, skillDirs...)

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

	// BatchIndex and BatchSize place this run in a batch - order 2 of 5 - so
	// the viewer can show how much of the queue is left. Zero for a run that is
	// not part of a batch.
	BatchIndex int
	BatchSize  int

	// Title is a short label for the work, shown in the viewer instead of the
	// task text. A work order's title, or one derived from its file name;
	// empty falls back to the task. It is presentation only and never reaches
	// the model - the objective is the contract, a title is a label.
	Title string

	// QuitOnDone closes the viewer as soon as the run ends instead of holding
	// the final screen. Set for the intermediate orders of a batch, where a
	// held screen would stall the orders behind it until a keypress nobody
	// unattended will make; the batch's last order still holds for review.
	QuitOnDone bool
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
	config.ScrubProviderSecrets(cfg)

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
	// opening user message only has to get the agent moving. This is what keeps
	// the objective in context however long the run grows - see withTask.
	//
	// @note there is deliberately no way to open a run with a prompt of the
	// caller's own. zot takes a work order, not a conversation; anything worth
	// saying to the agent belongs in the order, where it is durable.
	opts.Instructions = withTask(opts.Instructions, task)

	kickoff := taskKickoff
	if options.Resume != nil {
		kickoff = resumeKickoff
	}

	opts.Messages = append(opts.Messages, agent.Message{Type: agent.TypeUser, Text: kickoff})

	workdir, _ := os.Getwd()

	// Captures the run's final totals so the end-of-run digest can report them;
	// the viewer's returned Outcome carries only the reason and message.
	summaryRec := &agent.SummaryRecorder{}
	opts.Recorder = summaryRec

	var sessionID string

	if options.SessionDir != "" {
		meta := session.Meta{
			Task:     task,
			Model:    client.Model(),
			Provider: cfg.DefaultProvider,
			Driver:   client.Provider(),
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

			sessionID = writer.ID()
			opts.Recorder = agent.MultiRecorder(session.NewRecorder(writer), summaryRec)
		}
	}

	meta := viewerMeta(cfg, task, workdir, opts)
	meta.Title = options.Title
	meta.BatchIndex = options.BatchIndex
	meta.BatchSize = options.BatchSize
	meta.QuitOnDone = options.QuitOnDone

	outcome, err := tui.Run(ctx, client, meta, opts)

	printDigest(os.Stderr, "zot", sessionID, outcome, summaryRec.Summary)

	return err
}

// printDigest writes the end-of-run digest: the outcome, what the run spent,
// and - when the run was recorded - the session id and the exact command that
// resumes it. Kept to stderr so it never mixes into a piped deliverable, and
// skipped entirely when there is nothing to say (a run that never produced a
// summary, e.g. a setup failure before the first turn).
func printDigest(w io.Writer, app, sessionID string, outcome tui.Outcome, summary *agent.Summary) {
	if summary == nil {
		return
	}

	digest := tui.Digest{
		Status:       tui.DigestStatus(summary.Reason, summary.Code),
		Session:      sessionID,
		Iterations:   summary.Iterations,
		Calls:        summary.Calls,
		InputTokens:  summary.InputTokens,
		OutputTokens: summary.OutputTokens,
		Message:      outcome.Message,
	}

	if sessionID != "" {
		digest.Resume = app + " --resume " + sessionID
	}

	fmt.Fprintf(w, "\n%s", tui.RenderDigest(digest))
}

// viewerMeta describes the run to the viewer.
//
// The budgets it carries are the ones the run was resolved with, not the raw
// configuration: a per-model max_iterations lowers the limit the engine
// enforces, and a meta bar counting up to a number the run will never reach is
// worse than no number at all.
func viewerMeta(cfg Config, task, workdir string, opts agent.ExecuteWithToolsOptions) tui.Meta {
	// Show the iteration progress denominator only for a real user-set limit -
	// the default is a 1,000,000 backstop, which is not a budget worth displaying.
	iterLimit := 0
	if opts.MaxIterations != config.Defaults().Agent.MaxIterations {
		iterLimit = opts.MaxIterations
	}

	return tui.Meta{
		Task:          task,
		Model:         cfg.Agent.Model,
		Provider:      cfg.DefaultProvider,
		Workdir:       workdir,
		ShowDiff:      cfg.UI.Diff,
		Plain:         cfg.UI.Plain,
		Color:         cfg.UI.Color,
		MaxScrollback: cfg.UI.Scrollback,
		MaxIterations: iterLimit,
		MaxCalls:      opts.MaxCalls,
		MaxDuration:   opts.MaxDuration,
		Stats:         cfg.UI.Stats,
	}
}

// resolve turns a configuration into a provider client and the agent options a
// run uses. The returned options carry no messages; callers supply those.
func resolve(cfg Config, defaultInstructions string) (*agent.Client, agent.ExecuteWithToolsOptions, error) {
	var empty agent.ExecuteWithToolsOptions

	providerConfig, ok := cfg.Providers[cfg.DefaultProvider]
	if !ok {
		return nil, empty, fmt.Errorf("provider %q is not configured", cfg.DefaultProvider)
	}

	// Resolve the model against the provider's custom model definitions. A custom
	// entry's settings take priority over the run defaults.
	model := cfg.Agent.Model
	maxIterations := cfg.Agent.MaxIterations
	driver := config.ProviderDriver(cfg.DefaultProvider, providerConfig)
	credential := config.ProviderCredential(providerConfig)

	contextWindow := 0

	// What the model can do: the catalogue's answer, then whatever the operator
	// stated for this model. An uncatalogued model resolves to the conservative
	// default - tools yes, images no - so a model zot has never heard of is
	// never offered a tool for looking at pictures it may not be able to see.
	capabilities := catalogue.Lookup(model)

	if mc, ok := providerConfig.Models[model]; ok {
		if mc.Model != "" {
			model = mc.Model

			// the override names a real model, so start from what the catalogue
			// knows about that one before applying the rest
			capabilities = catalogue.Lookup(model)
		}
		if mc.MaxIterations > 0 {
			maxIterations = mc.MaxIterations
		}
		if mc.Driver != "" {
			driver = mc.Driver
		}
		if mc.APIKey != "" {
			credential = mc.APIKey
		}
		if mc.Context > 0 {
			contextWindow = mc.Context
		}

		capabilities = mc.Capabilities(capabilities)
	}

	if driver == "" {
		return nil, empty, fmt.Errorf(
			"provider %q does not name a driver (set driver: on the provider or the model - one of %s)",
			cfg.DefaultProvider, strings.Join(agent.Providers(), ", "))
	}

	instructions := cfg.Agent.Instructions
	if instructions == "" {
		instructions = defaultInstructions
	}

	instructions = withNonInteractiveContract(instructions)

	client, err := agent.NewClient(agent.ClientOptions{
		Provider: driver,
		Model:    model,
		APIKey:   credential,
		BaseURL:  providerConfig.BaseURL,

		Attribution: agent.Attribution{
			Name:     cfg.Attribution.Name,
			URL:      cfg.Attribution.URL,
			Disabled: cfg.Attribution.Disabled,
		},
	})
	if err != nil {
		return nil, empty, fmt.Errorf("provider %q: %w", cfg.DefaultProvider, err)
	}

	// max_time was validated at load, so a parse error here would be a bug; treat
	// it as unbounded rather than failing a run that already passed validation.
	maxDuration, _ := cfg.Agent.MaxDuration()

	// The loader rescans the context directories every time the engine renders
	// the system prompt, so a SKILL.md that appears mid-run is described to the
	// model on its next turn. Programmatic skills ride along as the static layer.
	skills := agent.NewSkillLoader(&agent.SkillsResult{Skills: cfg.Skills}, cfg.SkillDirectories...)

	opts := agent.ExecuteWithToolsOptions{
		Instructions: instructions,
		Tools: agent.DefaultToolsFor(agent.ToolOptions{
			MaxOutput: cfg.Agent.MaxToolOutput,
			Vision:    capabilities.SupportsVision,
		}),
		Skills:           skills.Skills,
		MaxIterations:    maxIterations,
		MaxSettles:       cfg.Agent.MaxSettles,
		MaxCalls:         cfg.Agent.MaxCalls,
		MaxContinuations: cfg.Agent.MaxContinuations,
		MaxRecoveries:    cfg.Agent.MaxRecoveries,
		MaxCycles:        cfg.Agent.MaxCycles,
		MaxEmpties:       cfg.Agent.MaxEmpties,
		MaxDuration:      maxDuration,
		LimitCheckpoints: cfg.Agent.LimitCheckpoints,
		// Empty is the default (compact); the agent layer resolves the string.
		ContextStrategy:     cfg.Agent.ContextStrategy,
		CompactMinTokens:    cfg.Agent.CompactMinTokens,
		CompactMinMessages:  cfg.Agent.CompactMinMessages,
		CompactTriggerRatio: cfg.Agent.CompactTriggerRatio,
		ContextWindow:       contextWindow,
	}

	// MaxTokens is a pointer so that "unset" (provider decides) is distinct from
	// a deliberate zero; the config uses a positive value to mean "cap here".
	if cfg.Agent.MaxTokens > 0 {
		limit := cfg.Agent.MaxTokens
		opts.MaxTokens = &limit
	}

	return client, opts, nil
}
