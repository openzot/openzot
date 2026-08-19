// Command zot is an automated software factory you watch, not drive.
//
// An autonomous coding harness powers the factory: hand zot a single task on
// the command line and it works the problem on its own - reading files, editing
// them, and running shell commands - while the terminal streams a live,
// read-only view of everything it does.
//
// Usage:
//
//	export OPENAI_API_KEY="your-api-key"
//	zot "add a /health endpoint to the Go server and a test for it"
//
//	# operate inside a specific directory and cap the work
//	zot --dir ./scratch --max-iterations 40 "scaffold a snake game in python"
//
//	# every run is logged; pick one up where it stopped
//	zot sessions
//	zot --resume last "finish the part you ran out of budget for"
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/joho/godotenv"
	"github.com/spf13/pflag"

	"github.com/openzot/openzot"
	"github.com/openzot/openzot/internal/buildinfo"
	"github.com/openzot/openzot/internal/config"
	"github.com/openzot/openzot/internal/session"
	"github.com/openzot/openzot/internal/version"
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
	// rather than a flag because it takes no task and produces no run.
	if len(os.Args) > 1 && os.Args[1] == "sessions" {
		return listSessions(os.Args[2:])
	}

	configPath := pflag.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	provider := pflag.String("provider", "", "model provider to run against: zai (default), openai, anthropic, groq, ollama, or a provider named in the config")
	model := pflag.String("model", "", "override the model name (default: glm-5.2, which only the zai provider serves)")
	dir := pflag.String("dir", ".", "working directory the agent reads, writes and runs commands in")
	maxIter := pflag.Int("max-iterations", 0, "override the safety cap on agent iterations")
	taskFlag := pflag.String("task", "", "the durable objective, placed in the system prompt so it survives a long run (overrides a positional task)")
	taskFile := pflag.String("task-file", "", "read the durable objective from a file")
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

	var task, prompt string

	// A resumed run inherits its objective from the session it continues; any new
	// command-line text is a follow-up prompt, not a replacement objective - a
	// resume is "keep going, and also do this", not "start over". An explicit
	// --task / --task-file still overrides, for deliberately changing course.
	//
	// This is decided before resolveTask is consulted, because a resume with no
	// new text is a complete invocation: asking resolveTask first meant the
	// missing-task branch printed the whole usage block to stderr on the way to
	// working correctly.
	if resumed != nil && *taskFlag == "" && *taskFile == "" {
		task = resumed.Meta.Task
		prompt = strings.TrimSpace(strings.Join(pflag.Args(), " "))
	} else {
		var err error

		task, prompt, err = resolveTask(*taskFlag, *taskFile, pflag.Args())
		if err != nil {
			return err
		}
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

	options := zot.RunOptions{SessionDir: sessions, Resume: resumed}

	if *noSession {
		options.SessionDir = ""
	}

	options.Prompt = prompt

	// The release check runs alongside the whole run and is reported only once
	// the viewer has released the screen.
	report := checkForUpdate()

	err = zot.RunWith(context.Background(), cfg, task, options)

	report(os.Stderr)

	return err
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

// resolveTask splits the input into the durable task (the objective, which goes
// into the system prompt) and an optional opening prompt (a user message).
//
// --task / --task-file give the objective explicitly; a bare positional is
// treated as the objective too, so `zot "do X"` keeps working and stays durable.
// When an objective is given explicitly, positional text becomes the opening
// prompt beside it. There is intentionally no interactive input: zot is a
// viewer, not a chat client.
func resolveTask(taskFlag, taskFile string, args []string) (task, prompt string, err error) {
	// The durable objective comes from --task or --task-file; it lands in the
	// system prompt and stays there for the whole run.
	switch {
	case taskFlag != "":
		task = strings.TrimSpace(taskFlag)
	case taskFile != "":
		data, readErr := os.ReadFile(taskFile)
		if readErr != nil {
			return "", "", fmt.Errorf("cannot read --task-file: %w", readErr)
		}
		task = strings.TrimSpace(string(data))
		if task == "" {
			return "", "", fmt.Errorf("--task-file %q is empty", taskFile)
		}
	}

	positional := strings.TrimSpace(strings.Join(args, " "))

	switch {
	case task == "" && positional == "":
		usage()
		return "", "", fmt.Errorf("no task given")

	case task == "":
		// No explicit objective, so the positional text is the objective - this
		// is the common `zot "do X"`, which stays durable rather than becoming a
		// throwaway prompt.
		task = positional

	default:
		// An explicit objective was given, so the positional text is an opening
		// prompt alongside it rather than a second objective.
		prompt = positional
	}

	return task, prompt, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `zot - an automated software factory powered by an autonomous coding harness

Usage:
  zot [flags] "your task in plain english"
  zot config
  zot sessions

Examples:
  zot "add input validation to the signup handler and a test"
  zot --task-file TASK.md "start with the parser"
  zot --dir ./scratch "scaffold a tiny http server in go"
  zot --resume last "now add the tests you skipped"

The task is the durable objective: it goes into the system prompt and stays in
context for the whole run. A bare "..." is the task; with --task/--task-file the
"..." becomes an opening prompt alongside it.

Commands:
  config     edit the config file in $EDITOR (creates it on first run)
  sessions   list previous runs, newest first

Flags:`)
	pflag.PrintDefaults()
}
