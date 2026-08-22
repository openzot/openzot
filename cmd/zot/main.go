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
//	# write an order, then run it - orders and their records live under .zot/
//	zot new "add a /health endpoint to the Go server and a test for it"
//	zot
//
//	# a bare zot runs the whole book, in filename order, skipping what the
//	# ledger already records as done; naming orders runs exactly those
//	zot .zot/orders/add-a-health-endpoint-to-the-go-server-and-a-te.yaml
//
//	# every run is logged; pick one up where it stopped
//	zot sessions
//	zot --resume last
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
	ordersFlag := pflag.String("orders-dir", "", "where this project's orders live, run by a bare `zot` (default: <dir>/"+order.BookDir+"/orders)")
	watchFlag := pflag.Bool("watch", false, "stay up and run work orders as they arrive, instead of running once and exiting: bare --watch watches this project's orders directory, or name a folder or glob to watch instead")
	recordsDir := pflag.String("records-dir", "", "where run records are written (default: <dir>/"+order.BookDir+"/records)")
	rerun := pflag.Bool("rerun", false, "run orders even when the ledger already records a successful run of the same content")
	fresh := pflag.Bool("fresh", false, "start orders from scratch even when an unfinished run of the same order exists")
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

	// The ledger defaults to the book of the project being worked on, not the
	// book beside whatever order file was named: an order may be read from
	// anywhere, and its receipt belongs with the work, not with the brief.
	// Resolved here for the same reason the session directory is.
	records := *recordsDir
	if records == "" {
		records = order.RecordsDir(*dir)
	}

	if abs, err := filepath.Abs(records); err == nil {
		records = abs
	}

	ledger := order.Ledger{Root: records}

	// The other half of the book: where this project's own orders live. It is
	// what a bare `zot` runs and what a bare `--watch` watches, so it is
	// resolved here too - before the chdir, because a relative --orders-dir
	// means what was typed.
	ordersRoot := *ordersFlag
	if ordersRoot == "" {
		ordersRoot = order.OrdersDir(*dir)
	}

	if abs, err := filepath.Abs(ordersRoot); err == nil {
		ordersRoot = abs
	}

	var resumed *session.Session

	if *resume != "" {
		if *watchFlag {
			return fmt.Errorf("--watch runs orders as they arrive; --resume continues one specific session - use one, not both")
		}

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

	// The watch target is resolved like an order path: against the invoking
	// directory, while it is still current, so `zot --dir proj --watch orders`
	// watches the folder that was named rather than one that happens to exist
	// inside proj. Orders themselves are loaded the same way, in the other half.
	var (
		watchTarget string
		orders      []order.Order
	)

	if *watchFlag {
		// A watch has one target, not a batch of them: it is a place work
		// arrives at, and two places would interleave two streams of runs into
		// one screen. Bare --watch watches this project's own orders.
		if len(pflag.Args()) > 1 {
			return fmt.Errorf(
				"--watch takes one folder or glob to watch; %q names several",
				strings.Join(pflag.Args(), " "))
		}

		watchTarget = ordersRoot

		if len(pflag.Args()) == 1 {
			target, err := filepath.Abs(pflag.Args()[0])
			if err != nil {
				return fmt.Errorf("resolve --watch %q: %w", pflag.Args()[0], err)
			}

			watchTarget = target
		}
	} else {
		// Orders are loaded - all of them, so a bad batch fails before any run
		// starts - while the original working directory is still current,
		// because their paths mean what the user typed, not what they happen to
		// mean after the chdir below.
		loaded, err := resolveOrders(pflag.Args(), resumed != nil, ordersRoot)
		if err != nil {
			return err
		}

		orders = loaded
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

	// A signal cancels the run rather than killing the process outright, so the
	// engine records its aborted outcome - and, mid-failure, dumps the exchange -
	// before exiting. Without this a `kill` (or a supervisor stopping the
	// process) left the session with no ending at all. SIGKILL is uncatchable;
	// the incrementally-written conversation is the answer there.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A resumed run inherits its objective from the session it continues; it is
	// "keep going", never "start over".
	if resumed != nil {
		return zot.RunWith(ctx, cfg,
			resumed.Meta.Task, zot.RunOptions{SessionDir: sessions, Resume: resumed})
	}

	// Watch mode: stay up and run every order the target yields, as it arrives.
	// It shares the batch's per-order treatment (oneRun), so an order runs the
	// same whether it was named on the command line or dropped into the folder
	// after startup - and one failed order is one order's story, not the
	// watch's.

	if *watchFlag {
		return startWatch(ctx, watchTarget, newWatchRunner(ctx, cfg, sessions, ledger, *rerun, *fresh))
	}

	// Each order is its own run: a fresh conversation, its own session log, its
	// own recorded outcome. The batch stops at the first order that does not
	// end in success, because later orders usually assume the earlier ones
	// landed - running order three against the wreckage of order two produces
	// confident garbage.
	runs := oneRun{
		ctx:      ctx,
		cfg:      cfg,
		sessions: sessions,
		ledger:   ledger,
		rerun:    *rerun,
		fresh:    *fresh,
		run:      zot.RunWith,
	}

	for i, o := range orders {
		if len(orders) > 1 {
			fmt.Fprintf(os.Stderr, "zot: order %d/%d: %s\n", i+1, len(orders), o.Path)
		}

		if err := runs.executeAt(o, i < len(orders)-1, i+1, len(orders)); err != nil {
			// a deliberate stop - q or Ctrl-C - is the operator's decision,
			// not an order failing; report it as what it is
			if errors.Is(err, tui.ErrCancelled) {
				return fmt.Errorf("batch stopped: %w", err)
			}

			if len(orders) > 1 {
				return fmt.Errorf("order %s stopped the batch: %w", o.Path, err)
			}

			return err
		}
	}

	return nil
}

// oneRun is everything a single order's run needs. The batch loop and watch
// mode both go through execute, so an order runs identically however it was
// named: a fresh conversation, its own session log, its own recorded outcome.
type oneRun struct {
	ctx      context.Context
	cfg      zot.Config
	sessions string

	// ledger is where the run's outcome is recorded and where doneness is read
	// from. A zero Ledger records nothing and satisfies nothing.
	ledger order.Ledger

	// Doneness comes from the ledger, never from mutating the order; rerun and
	// fresh carry the operator's overrides of that, as on the command line.
	rerun bool
	fresh bool

	// run is the engine entry point - zot.RunWith everywhere in production,
	// replaced by tests so no provider is ever reached.
	run func(context.Context, zot.Config, string, zot.RunOptions) error
}

// execute runs one order as its own run and records how it ended.
//
// A satisfied order is skipped, so restarting a batch - or pointing a watch at
// a folder whose work already happened - picks up where things left off instead
// of re-executing finished work; editing the order changes its hash and
// re-queues it. An unfinished run of this order continues automatically: the
// order is the contract, and abandoning half its work because nobody typed
// --resume wastes everything the earlier run learned. Only a run that concluded
// - settled or declared failed - starts over; fresh forces a clean start.
func (r oneRun) execute(o order.Order, quitOnDone bool) error {
	return r.executeAt(o, quitOnDone, 0, 0)
}

// executeAt is execute with the order's position in a batch, which the viewer
// shows as "order 2/5" so a long queue reports how much of itself is left.
func (r oneRun) executeAt(o order.Order, quitOnDone bool, index, size int) error {
	if record, done := r.ledger.Satisfied(o); done && !r.rerun {
		fmt.Fprintf(os.Stderr,
			"zot: order %s already satisfied by run %s (%s); edit the order or pass --rerun to run it again\n",
			o.Path, record.Run, record.At.Format("2006-01-02 15:04"))

		return nil
	}

	// An unfinished run continues automatically unless --fresh says otherwise;
	// see execute's doc comment for why this is not opt-in.
	var seed *session.Session

	if !r.fresh && r.sessions != "" {
		if unfinished, ok := unfinishedRunOf(r.sessions, o.Task()); ok {
			seed = unfinished

			fmt.Fprintf(os.Stderr,
				"zot: continuing unfinished run %s of this order (--fresh to start over)\n",
				seed.Meta.ID)
		}
	}

	var runID, sessionPath string

	options := zot.RunOptions{
		SessionDir: r.sessions,
		Resume:     seed,

		// what a person calls this order: its own title, or its file name
		Title: o.DisplayTitle(),

		BatchIndex: index,
		BatchSize:  size,

		OnSession: func(path string) {
			sessionPath = path
			runID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		},

		// intermediate orders auto-advance: a held final screen would stall the
		// rest of the batch until a keypress nobody unattended will make. The
		// last order holds for review as usual - watch mode holds none.
		QuitOnDone: quitOnDone,
	}

	if err := r.run(r.ctx, r.cfg, o.Task(), options); err != nil {
		return err
	}

	// a successful run enters the ledger; a failed or aborted one does not, so
	// it runs again next time - failure is not doneness
	if runID == "" {
		runID = session.NewID(time.Now())
	}

	if err := r.ledger.Record(o, runID, "settled", time.Now(), r.evidenceOf(runID, sessionPath)); err != nil {
		fmt.Fprintf(os.Stderr, "zot: could not record the outcome for %s: %v\n", o.Path, err)
	}

	return nil
}

// evidenceOf reads back what the run recorded about itself, so the receipt
// carries proof rather than only a claim. The log the run just wrote is the
// only honest source: anything reconstructed here would be zot vouching for
// zot. A run with no log to read yields evidence that says so.
func (r oneRun) evidenceOf(runID, sessionPath string) order.Evidence {
	if sessionPath == "" {
		return order.EvidenceFrom(runID, nil, nil)
	}

	loaded, err := session.Load(sessionPath)

	return order.EvidenceFrom(runID, loaded, err)
}

// resolveOrders loads the orders this invocation is about: the ones named on
// the command line, or - when none are - everything in the project's own orders
// directory. It explains itself rather than failing silently either way.
func resolveOrders(args []string, resuming bool, ordersRoot string) ([]order.Order, error) {
	// A resume continues the order its session was started with. New orders
	// belong to new runs - silently mixing the two would blur which outcome
	// belongs to which order.
	if resuming {
		if len(args) > 0 {
			return nil, fmt.Errorf("--resume continues the order its session was started with; run new orders in a fresh invocation")
		}

		return nil, nil
	}

	// Nothing named: run the book. The ledger then decides what actually runs -
	// a satisfied order is skipped - so a bare `zot` in a project means "carry
	// out whatever work is still outstanding here", which is the shape of the
	// whole tool in one word. Naming orders explicitly still works exactly as
	// before, and still reads them from anywhere.
	if len(args) == 0 {
		found, err := listOrdersRoot(ordersRoot)
		if err != nil {
			return nil, err
		}

		fmt.Fprintf(os.Stderr, "zot: running %d order(s) from %s\n", len(found), ordersRoot)

		args = found
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

		// the run chdirs into --dir, so the order's path - and with it the
		// ledger derived from that path - must survive the move
		if abs, absErr := filepath.Abs(loaded.Path); absErr == nil {
			loaded.Path = abs
		}

		orders = append(orders, loaded)
	}

	return orders, nil
}

// listOrdersRoot lists the orders directory for a bare invocation, or explains
// what to do instead. An empty book is not an error state to decode - it is
// someone who has not written an order yet.
func listOrdersRoot(ordersRoot string) ([]string, error) {
	var found []string

	if ordersRoot != "" {
		var err error

		if found, err = order.List(ordersRoot); err != nil {
			return nil, err
		}
	}

	if len(found) == 0 {
		usage()

		where := "no orders directory is configured"
		if ordersRoot != "" {
			where = ordersRoot + " holds none"
		}

		return nil, fmt.Errorf("no order given, and %s (write one with `zot new \"the objective\"`)", where)
	}

	return found, nil
}

// newOrder scaffolds a work order under ./.zot/orders - or under
// <dir>/.zot/orders when --dir names another working directory - and says how
// to run it.
//
// Three levels of help, all landing in the same reviewable file: bare `zot new`
// writes the blank form, `zot new "objective"` fills the objective in, and
// `zot new --draft "objective"` additionally has the configured model propose
// the acceptance criteria and constraints. The model only ever drafts - the
// operator's edit is what makes the criteria a contract.
//
// --dir exists because the order is written for a project the invoker may not
// be standing in: `zot new --dir ~/work/api ...` run from anywhere scaffolds
// into that project and surveys that project, leaving the invoking directory
// untouched. --orders-dir files the order somewhere else again - a shared
// folder of briefs, a drop box a watcher is pointed at - while --dir still says
// which project it is for.
func newOrder(args []string, out io.Writer) error {
	set := pflag.NewFlagSet("new", pflag.ContinueOnError)

	draft := set.Bool("draft", false, "have the configured model draft the acceptance criteria and constraints")
	configPath := set.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	providerFlag := set.String("provider", "", "provider for --draft (default: the configured one)")
	modelFlag := set.String("model", "", "model for --draft (default: the configured one)")
	dir := set.String("dir", ".", "working directory the order is for: the scaffold lands under <dir>/"+order.BookDir+"/orders and a --draft survey reads there")
	ordersFlag := set.String("orders-dir", "", "where to write the order (default: <dir>/"+order.BookDir+"/orders)")

	if err := set.Parse(args); err != nil {
		return err
	}

	objective := strings.TrimSpace(strings.Join(set.Args(), " "))

	// A provider or model without --draft would be silently ignored - and the
	// operator who typed them almost certainly expected the drafting survey to
	// start. Refuse and say the missing word rather than scaffold something
	// other than what was asked for.
	if !*draft && (*providerFlag != "" || *modelFlag != "") {
		return fmt.Errorf("--provider and --model configure the drafting survey; add --draft to run one")
	}

	// The command is anchored at --dir: the scaffold lands in that project's
	// book, and a drafting survey reads the tree it drafts for. --orders-dir
	// separates the two, because where an order is filed and which project it
	// is about are different questions - a shared folder of briefs is written
	// from many projects, and a survey must still read the one being drafted
	// for. A relative path means what was typed here, in the invoking
	// directory, so it is joined before any chdir below - and the "then run:"
	// hint is a path that resolves from where the command was invoked.
	ordersDir := *ordersFlag
	if ordersDir == "" {
		ordersDir = order.OrdersDir(*dir)
	}

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

		// The survey reads the tree the order is about, so it runs with the
		// working directory set to --dir - exactly where a real run of the
		// finished order will work.
		restore, err := chdirInto(*dir)
		if err != nil {
			return err
		}

		drafted, draftErr := draftOrder(context.Background(), cfg, client, objective)

		restore()

		// A failed draft must not cost the operator what they typed. Drafting
		// is the optional half - the objective is the part only they can write,
		// and discarding it because a provider fell over loses the one thing
		// that was not recoverable. So the plain scaffold is written anyway and
		// the failure is reported loudly: the danger was ever handing it back
		// *silently*, as though the criteria had been drafted.
		if draftErr != nil {
			path, scaffoldErr := order.Scaffold(ordersDir, o)
			if scaffoldErr != nil {
				return fmt.Errorf("%w (and the objective could not be scaffolded either: %v)", draftErr, scaffoldErr)
			}

			fmt.Fprintf(out, "wrote %s\n", path)

			return fmt.Errorf(
				"%w - the objective was scaffolded to %s without criteria; edit them in, or retry with --draft",
				draftErr, path)
		}

		o = drafted

		edit = "review its drafted acceptance criteria"
	}

	path, err := order.Scaffold(ordersDir, o)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "wrote %s\n%s, then run:\n\n  zot %s\n", path, edit, path)

	return nil
}

// chdirInto enters dir - as a real run would - and returns the function that
// puts the process back where it was. Callers scaffold from paths joined
// against the invoking directory, so the restore has to happen before that,
// error paths included.
func chdirInto(dir string) (func(), error) {
	previous, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	if err := os.Chdir(dir); err != nil {
		return nil, fmt.Errorf("cannot enter --dir %q: %w", dir, err)
	}

	return func() { _ = os.Chdir(previous) }, nil
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
		Tools:         draftTools(cfg.Agent.MaxToolOutput),
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
func draftTools(maxOutput int) agent.Tools {
	tools := agent.Tools{}

	full := agent.DefaultToolsWith(maxOutput)

	for _, name := range []string{"read", "list"} {
		tools[name] = full[name]
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
  zot [flags] [<order.yaml> ...]
  zot new [--draft] [--dir <dir>] [--orders-dir <dir>] ["the objective"]
  zot config
  zot sessions
  zot --watch [<folder-or-glob>]

Examples:
  zot
  zot new "add input validation to the signup handler and a test"
  zot new --draft "add rate limiting to the API"
  zot new --dir ~/work/api "fix the flaky test"
  zot new --orders-dir ~/briefs "fix the flaky test"
  zot .zot/orders/add-input-validation-to-the-signup-handler-and.yaml
  zot --dir ./scratch .zot/orders/*.yaml
  zot --resume last

The book: a project keeps its orders and the record of what has been run from
them under .zot in its root - .zot/orders/<slug>.yaml written by zot new, and
.zot/records/<slug>/<run>.yaml written by a successful run. Bare zot runs
that book: every order in it, in filename order, with the ledger deciding which
are still outstanding - so writing an order and typing zot is the whole loop.
Naming order files instead runs exactly those, read from any path in any tree;
running an order needs no book at all.

Both halves of the book are yours to move: --orders-dir is where zot new files
an order and where a bare zot looks for work, --records-dir is where the ledger
is written. The ledger defaults to <dir>/.zot/records - the book of the project
being worked on, not one beside whatever order file was named, because the
receipt belongs with the work rather than with the brief.

A batch runs each order as its own run, in sequence, and stops at the first
order that does not end in success. A successful run enters the ledger, and a
satisfied order is skipped on the next invocation - editing the order re-queues
it, --rerun forces it. An order whose last run did not conclude is continued
automatically; --fresh starts it from scratch instead.

Watch mode keeps zot up and runs work as it arrives instead of exiting when the
batch ends. Bare zot --watch watches the orders directory; name a folder
(zot --watch ~/inbox) or a glob (zot --watch "~/inbox/*.yaml") to watch that
instead. Every *.yaml that shows up - including orders already sitting there -
runs as its own run, one at a time. The ledger applies, so an order that already
ran is skipped; a failed order is reported and the watch goes on; Ctrl-C stops
watching.

Commands:
  new        scaffold a work order under ./.zot/orders - under <dir>/.zot/orders
             with --dir, or anywhere with --orders-dir. Bare for the blank
             form, with an objective to fill it in, --draft to have the
             configured model propose the acceptance criteria for your review
             (surveying --dir, when given)
  config     edit the config file in $EDITOR (creates it on first run)
  sessions   list previous runs, newest first

Flags:`)
	pflag.PrintDefaults()
}

// unfinishedRunOf finds the newest session of this exact order that did not
// conclude - no recorded outcome (killed, crashed), or an interruption
// (aborted, a guard stop, a provider error). A settled run is done and a
// declared failure is a conclusion; neither is resumed.
func unfinishedRunOf(dir, task string) (*session.Session, bool) {
	entries, err := session.List(dir)
	if err != nil {
		return nil, false
	}

	// newest first: only the most recent run of the order counts - older
	// unfinished runs were superseded by whatever came after them
	for _, entry := range entries {
		if entry.Task != task {
			continue
		}

		if entry.Complete && (entry.Reason == "settled" || entry.Reason == "failed") {
			return nil, false
		}

		resumed, err := session.Load(entry.Path)
		if err != nil {
			return nil, false
		}

		return resumed, true
	}

	return nil, false
}
