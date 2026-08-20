// Command zot is an automated software factory you watch, not drive.
//
// zot takes work orders, not prompts. A work order is a small YAML file - the
// durable objective, the acceptance criteria that define "done", the
// constraints the work must hold to - and each order becomes one autonomous
// run: the agent reads files, edits them, and runs shell commands on its own
// while the terminal streams a live, read-only view of everything it does.
//
// Usage:
//
//	export OPENAI_API_KEY="your-api-key"
//
//	# write an order, then run it
//	zot new "add a /health endpoint to the Go server and a test for it"
//	zot orders/add-a-health-endpoint-to-the-go-server-and-a-te.yaml
//
//	# run a batch: each order is its own run, in sequence, stopping at the
//	# first that fails
//	zot orders/*.yaml
//
//	# every run is logged; pick one up where it stopped
//	zot sessions
//	zot --resume last
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joho/godotenv"
	"github.com/openzot/openzot/agent"
	"github.com/spf13/pflag"

	"github.com/openzot/openzot"
	"github.com/openzot/openzot/internal/buildinfo"
	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/order"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/version"
	"github.com/openzot/openzot/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zot: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	// `zot config` opens the config file in $EDITOR, seeding it from the embedded
	// template on first run. `zot config path` prints its location.
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) > 2 && os.Args[2] == "path" {
			fmt.Println(config.DefaultConfigPath())
			return nil
		}
		return editConfig()
	}

	// `zot sessions` lists what previous runs left behind. Its own subcommand
	// rather than a flag because it takes no order and produces no run.
	if len(os.Args) > 1 && os.Args[1] == "sessions" {
		return listSessions(os.Args[2:])
	}

	// `zot new` scaffolds a work order. The two-step shape is deliberate: the
	// pause between writing the order and running it is where acceptance
	// criteria get written, and it is what keeps zot from feeling like a
	// prompt box.
	if len(os.Args) > 1 && os.Args[1] == "new" {
		return newOrder(os.Args[2:], os.Stdout)
	}

	configPath := pflag.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	provider := pflag.String("provider", "", "model provider to run against: zai (default), openai, anthropic, groq, ollama, or a provider named in the config")
	model := pflag.String("model", "", "override the model name (default: glm-5.2, which only the zai provider serves)")
	dir := pflag.String("dir", ".", "working directory the agent reads, writes and runs commands in")
	maxIter := pflag.Int("max-iterations", 0, "override the safety cap on agent iterations")
	diffFlag := pflag.Bool("diff", false, "show a syntax-highlighted diff panel under each edit/write")
	plainFlag := pflag.Bool("plain", false, "stream unstyled output instead of the full-screen UI (auto-enabled when not a TTY)")
	colorFlag := pflag.String("color", "", "colorize non-interactive output: auto, always, or never")
	resume := pflag.String("resume", "", "continue an earlier session: an id, a path, or \"last\"")
	sessionDir := pflag.String("session-dir", "", "where session logs are written (default: "+config.DefaultSessionDir()+")")
	noSession := pflag.Bool("no-session", false, "do not record a session log for this run")
	showVersion := pflag.Bool("version", false, "print version and exit")
	pflag.Usage = usage
	pflag.Parse()

	if *showVersion {
		// the build kind is on the version line because it changes what the
		// binary will read from disk, and "why is it not picking up my .env"
		// should be answerable without reading the source
		kind := buildinfo.Kind
		if config.Portable() {
			// a portable build carries its own config and overrides the runtime
			// file/env, so "why is it ignoring my config" is answerable here too
			kind += ", portable config"
		}
		fmt.Printf("zot %s (%s)\n", version.Version, kind)
		return nil
	}

	// Load credentials from the directory the agent will work in. This must
	// happen after --dir is parsed but before configuration resolves env-backed
	// provider secrets - and only on a developer build.
	loadEnv(*dir)

	sessions := *sessionDir
	if sessions == "" {
		sessions = config.DefaultSessionDir()
	}

	// Resolved to an absolute path while the original working directory is still
	// current, so a relative --session-dir means what the user typed rather than
	// what it happens to mean after the chdir below.
	if abs, err := filepath.Abs(sessions); err == nil {
		sessions = abs
	}

	var resumed *session.Session

	if *resume != "" {
		path, err := session.Resolve(sessions, *resume)
		if err != nil {
			return err
		}

		resumed, err = session.Load(path)
		if err != nil {
			return fmt.Errorf("read session: %w", err)
		}

		fmt.Fprintf(os.Stderr, "zot: resuming %s (%d messages)\n", resumed.Meta.ID, len(resumed.Messages))
	}

	// Orders are loaded - all of them, so a bad batch fails before any run
	// starts - while the original working directory is still current, because
	// their paths mean what the user typed, not what they happen to mean after
	// the chdir below.
	orders, err := resolveOrders(pflag.Args(), resumed != nil)
	if err != nil {
		return err
	}

	cfg, err := zot.Load(*configPath)
	if err != nil {
		return err
	}

	passed := map[string]bool{}

	pflag.Visit(func(f *pflag.Flag) { passed[f.Name] = true })

	applyOverrides(&cfg, overrides{
		Provider:      *provider,
		Model:         *model,
		MaxIterations: *maxIter,
		Diff:          *diffFlag,
		Plain:         *plainFlag,
		Color:         *colorFlag,
		Passed:        passed,
	})

	if err := cfg.Validate(); err != nil {
		return err
	}

	// Resolve the config directory (source of any global AGENT.md / skills) while
	// the original working directory is still current, so a relative --config
	// resolves correctly before the chdir below.
	configDir := config.ConfigDir(*configPath)
	if abs, err := filepath.Abs(configDir); err == nil {
		configDir = abs
	}

	// Set the default working directory before the agent starts. This is not a
	// filesystem sandbox: absolute paths and shell commands retain the process's
	// host permissions.
	if err := os.Chdir(*dir); err != nil {
		return fmt.Errorf("cannot enter --dir %q: %w", *dir, err)
	}

	// Fold in AGENT.md and skills from the config directory, then the working
	// directory (project-level context wins / appends last).
	workDir, _ := os.Getwd()
	if err := zot.LoadProjectContext(&cfg, configDir, workDir); err != nil {
		return err
	}

	if *noSession {
		sessions = ""
	}

	// The release check runs alongside the whole batch and is reported only once
	// the viewer has released the screen.
	report := checkForUpdate()
	defer report(os.Stderr)

	// A resumed run inherits its objective from the session it continues; it is
	// "keep going", never "start over".
	if resumed != nil {
		return zot.RunWith(context.Background(), cfg,
			resumed.Meta.Task, zot.RunOptions{SessionDir: sessions, Resume: resumed})
	}

	// Each order is its own run: a fresh conversation, its own session log, its
	// own recorded outcome. The batch stops at the first order that does not
	// end in success, because later orders usually assume the earlier ones
	// landed - running order three against the wreckage of order two produces
	// confident garbage.
	for i, o := range orders {
		if len(orders) > 1 {
			fmt.Fprintf(os.Stderr, "zot: order %d/%d: %s\n", i+1, len(orders), o.Path)
		}

		options := zot.RunOptions{SessionDir: sessions}

		if err := zot.RunWith(context.Background(), cfg, o.Task(), options); err != nil {
			if len(orders) > 1 {
				return fmt.Errorf("order %s stopped the batch: %w", o.Path, err)
			}

			return err
		}
	}

	return nil
}

// resolveOrders loads every order named on the command line, or explains why it
// will not.
func resolveOrders(args []string, resuming bool) ([]order.Order, error) {
	// A resume continues the order its session was started with. New orders
	// belong to new runs - silently mixing the two would blur which outcome
	// belongs to which order.
	if resuming {
		if len(args) > 0 {
			return nil, fmt.Errorf("--resume continues the order its session was started with; run new orders in a fresh invocation")
		}

		return nil, nil
	}

	if len(args) == 0 {
		usage()

		return nil, fmt.Errorf("no order given (write one with `zot new \"the objective\"`)")
	}

	orders := make([]order.Order, 0, len(args))

	for _, path := range args {
		loaded, err := order.Load(path)
		if err != nil {
			// The retraining moment: someone typed prose where an order file
			// goes. The error has to teach the new shape, not just report a
			// missing file.
			if _, statErr := os.Stat(path); statErr != nil && strings.ContainsAny(path, " \t") {
				return nil, fmt.Errorf("work orders are files, not prose - write the order first:\n\n  zot new %q", path)
			}

			return nil, err
		}

		orders = append(orders, loaded)
	}

	return orders, nil
}

// newOrder scaffolds a work order under ./orders and says how to run it.
//
// Three levels of help, all landing in the same reviewable file: bare `zot new`
// writes the blank form, `zot new "objective"` fills the objective in, and
// `zot new --draft "objective"` additionally has the configured model propose
// the acceptance criteria and constraints. The model only ever drafts - the
// operator's edit is what makes the criteria a contract.
func newOrder(args []string, out io.Writer) error {
	set := pflag.NewFlagSet("new", pflag.ContinueOnError)

	draft := set.Bool("draft", false, "have the configured model draft the acceptance criteria and constraints")
	configPath := set.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	providerFlag := set.String("provider", "", "provider for --draft (default: the configured one)")
	modelFlag := set.String("model", "", "model for --draft (default: the configured one)")

	if err := set.Parse(args); err != nil {
		return err
	}

	objective := strings.TrimSpace(strings.Join(set.Args(), " "))

	o := order.Order{Objective: objective}

	edit := "edit its acceptance criteria"
	if objective == "" {
		edit = "edit its objective and acceptance criteria"
	}

	if *draft {
		if objective == "" {
			return fmt.Errorf(`--draft needs an objective to draft from: zot new --draft "the objective"`)
		}

		cfg, err := zot.Load(*configPath)
		if err != nil {
			return err
		}

		if *providerFlag != "" {
			cfg.DefaultProvider = *providerFlag
		}

		if *modelFlag != "" {
			cfg.Agent.Model = *modelFlag
		}

		if err := cfg.Validate(); err != nil {
			return err
		}

		client, err := zot.NewClient(cfg)
		if err != nil {
			return err
		}

		o, err = draftOrder(context.Background(), cfg, client, objective)
		if err != nil {
			// a failed draft writes nothing: the operator asked for a drafted
			// order, and silently handing back the plain scaffold would hide
			// that they did not get one
			return fmt.Errorf("%w (or scaffold without --draft and write the criteria yourself)", err)
		}

		edit = "review its drafted acceptance criteria"
	}

	path, err := order.Scaffold("orders", o)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "wrote %s\n%s, then run:\n\n  zot %s\n", path, edit, path)

	return nil
}

// draftOrder runs the draft as what it is: a small autonomous run through the
// same engine as real work, with a read-only toolbox, so the proposed criteria
// are grounded in the actual working tree - its build files, its test setup -
// rather than guessed from the objective alone. It renders in the same viewer
// as any run - watching a run work is zot's whole interface - and it ends the
// way every run ends, by calling the success tool; here its summary is the
// draft, handed back as the run's recorded outcome.
//
// It is a survey, not a run of record: it leaves no session log, and its
// budgets are a fraction of a real run's because reading a tree does not take
// a thousand iterations.
func draftOrder(ctx context.Context, cfg zot.Config, client *agent.Client, objective string) (order.Order, error) {
	opts := agent.ExecuteWithToolsOptions{
		Instructions:  order.DraftInstructions(objective),
		Text:          []string{"Survey the working directory, then record the draft."},
		Tools:         draftTools(),
		MaxIterations: draftMaxIterations,
		MaxDuration:   3 * time.Minute,
		// a draft is a convenience, not a run of record: fail fast rather than
		// sitting out a provider outage the way a real run rightly would
		MaxContinuations: 3,
	}

	workdir, _ := os.Getwd()

	outcome, err := tui.Run(ctx, client, tui.Meta{
		Task:          "draft: " + objective,
		Model:         cfg.Agent.Model,
		Provider:      cfg.DefaultProvider,
		Workdir:       workdir,
		Plain:         cfg.UI.Plain,
		Color:         cfg.UI.Color,
		MaxScrollback: cfg.UI.Scrollback,
		Stats:         cfg.UI.Stats,
		MaxIterations: draftMaxIterations,
		MaxDuration:   3 * time.Minute,
		// the draft's deliverable is the scaffolded file, not the screen: close
		// the viewer when the run ends so the write it feeds is not held
		// hostage to a keypress
		QuitOnDone: true,
	}, opts)
	if err != nil {
		return order.Order{}, fmt.Errorf("draft: %w", err)
	}

	if outcome.Reason != agent.ReasonSettled {
		return order.Order{}, fmt.Errorf("draft: the survey ended without a draft (%s: %s)", outcome.Reason, outcome.Message)
	}

	return order.ParseDraft(objective, outcome.Message)
}

// draftMaxIterations bounds the survey. Reading a tree does not take a
// thousand rounds; a draft that needs more than this is lost.
const draftMaxIterations = 25

// draftTools is the read-only subset of the default toolbox. A draft surveys
// the tree; a survey that can edit files or run commands is not a survey.
func draftTools() agent.Tools {
	tools := agent.Tools{}

	for _, name := range []string{"read", "list"} {
		tools[name] = agent.DefaultTools()[name]
	}

	return tools
}

// checkForUpdate starts the GitHub release check and returns the function that
// reports its outcome.
//
// The check is a convenience and never more than that: it runs concurrently with
// the run so it costs no wall-clock time, and every failure - an unreachable
// GitHub, a rate limit, a malformed body - is dropped rather than surfaced. zot
// runs unattended, and there is nobody there to act on "the update check
// failed". A development build makes no call at all (see version.Check), and the
// notice goes to stderr so it cannot corrupt the transcript on stdout.
//
// Reporting waits on the lookup, which the HTTP client bounds to a few seconds -
// by the time a real run ends the answer has long since arrived.
func checkForUpdate() func(io.Writer) {
	notice := make(chan string, 1)

	go func() {
		result, err := version.Check()
		if err != nil {
			notice <- ""

			return
		}

		notice <- version.FormatUpdateNotice(result)
	}()

	return func(w io.Writer) {
		if text := <-notice; text != "" {
			fmt.Fprintln(w, text)
		}
	}
}

// listSessions prints previous runs, newest first.
func listSessions(args []string) error {
	set := pflag.NewFlagSet("sessions", pflag.ContinueOnError)

	dir := set.String("session-dir", config.DefaultSessionDir(), "directory to list")

	if err := set.Parse(args); err != nil {
		return err
	}

	entries, err := session.List(*dir)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		fmt.Printf("no sessions in %s\n", *dir)

		return nil
	}

	for _, entry := range entries {
		status := "running/interrupted"
		if entry.Complete {
			status = entry.Reason
		}

		fmt.Printf("%-17s  %-20s  %s\n", entry.ID, status, oneLine(entry.Task, 60))
	}

	return nil
}

// oneLine flattens a task to a single truncated line, so a multi-line brief does
// not turn the listing into a wall of text.
//
// The cap counts characters, not bytes: a brief written in CJK or carrying an
// emoji would otherwise be cut inside a rune and print as a replacement glyph,
// and would be truncated far earlier than the column it is given.
func oneLine(text string, width int) string {
	text = strings.Join(strings.Fields(text), " ")

	if utf8.RuneCountInString(text) > width {
		return string([]rune(text)[:width-1]) + "\u2026"
	}

	return text
}

// loadEnv reads a `.env` from the working directory, on developer builds only.
//
// Reading credentials out of whatever directory zot was pointed at is a
// developer convenience and a released binary's liability: it means running zot
// against a repository you cloned to review is enough to load a stray committed
// `.env` into the process that is about to run shell commands. Released builds
// take their credentials from the config file and the real environment, both of
// which the operator chose deliberately.
func loadEnv(dir string) {
	if !buildinfo.Dev {
		return
	}

	_ = godotenv.Load(filepath.Join(dir, ".env"))
}

// editConfig ensures the config file exists - seeding it from the embedded
// template on first run - and opens it in the user's editor. This is the setup
// path: configure the provider, model and key by editing the file.
func editConfig() error {
	path := config.DefaultConfigPath()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, zot.ExampleConfigYAML, 0o600); err != nil {
			return fmt.Errorf("write config template: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Created %s from the template.\n", path)
	}

	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		for _, candidate := range []string{"nano", "vi", "vim"} {
			if _, err := exec.LookPath(candidate); err == nil {
				editor = candidate
				break
			}
		}
	}
	if editor == "" {
		fmt.Println(path)
		return fmt.Errorf("no editor found; set $EDITOR and re-run (the config is at the path above)")
	}

	cmd := exec.Command(editor, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// overrides are the command-line values that take precedence over the config
// file and the environment.
type overrides struct {
	Provider      string
	Model         string
	MaxIterations int
	Diff          bool
	Plain         bool
	Color         string

	// Passed names the flags actually given, so a boolean can tell "false
	// because it was passed" from "false because it was never set". Without it
	// an unset --diff would silently turn off a diff the config had enabled.
	Passed map[string]bool
}

// applyOverrides layers command-line values over a loaded configuration.
func applyOverrides(cfg *zot.Config, o overrides) {
	if o.Provider != "" {
		cfg.DefaultProvider = o.Provider
	}

	if o.Model != "" {
		cfg.Agent.Model = o.Model
	}

	if o.MaxIterations > 0 {
		cfg.Agent.MaxIterations = o.MaxIterations
	}

	// A per-model max_iterations is applied when the run resolves, after this,
	// and would otherwise leave the config file beating the command line: the
	// engine would stop at the model's cap while the viewer counted up to the
	// flag's. An explicitly passed --max-iterations is the operator's last word,
	// so the model's own cap goes.
	if o.Passed["max-iterations"] {
		clearModelIterations(cfg)
	}

	if o.Passed["diff"] {
		cfg.UI.Diff = o.Diff
	}

	if o.Passed["plain"] {
		cfg.UI.Plain = o.Plain
	}
	if o.Color != "" {
		cfg.UI.Color = o.Color
	}
}

// clearModelIterations drops the per-model iteration cap for the model the run
// will actually use, so nothing is left to override the command line later. The
// provider and model have already been overridden by the time this is called, so
// it looks up the pair the run resolves to.
func clearModelIterations(cfg *zot.Config) {
	provider, ok := cfg.Providers[cfg.DefaultProvider]
	if !ok {
		return
	}

	model, ok := provider.Models[cfg.Agent.Model]
	if !ok {
		return
	}

	model.MaxIterations = 0

	provider.Models[cfg.Agent.Model] = model
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func usage() {
	fmt.Fprintln(os.Stderr, `zot - an automated software factory powered by an autonomous coding harness

zot takes work orders, not prompts. A work order is a small YAML file: the
durable objective, the acceptance criteria that define "done", and the
constraints the work must hold to. Each order is one autonomous run.

Usage:
  zot [flags] <order.yaml> ...
  zot new [--draft] ["the objective"]
  zot config
  zot sessions

Examples:
  zot new "add input validation to the signup handler and a test"
  zot new --draft "add rate limiting to the API"
  zot orders/add-input-validation-to-the-signup-handler-and.yaml
  zot --dir ./scratch orders/*.yaml
  zot --resume last

A batch runs each order as its own run, in sequence, and stops at the first
order that does not end in success.

Commands:
  new        scaffold a work order under ./orders - bare for the blank form,
             with an objective to fill it in, --draft to have the configured
             model propose the acceptance criteria for your review
  config     edit the config file in $EDITOR (creates it on first run)
  sessions   list previous runs, newest first

Flags:`)
	pflag.PrintDefaults()
}
