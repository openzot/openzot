// Command zot is an automated software factory you watch, not drive.
//
// An autonomous coding harness powers the factory: hand zot a single task on
// the command line and it works the problem on its own - reading files, editing
// them, and running shell commands - while the terminal streams a live,
// read-only view of everything it does.
//
// Usage:
//
//	export CHATBOTKIT_API_SECRET="your-api-key"
//	zot "add a /health endpoint to the Go server and a test for it"
//
//	# operate inside a specific directory and cap the work
//	zot --dir ./scratch --max-iterations 40 "scaffold a snake game in python"
//
// The "acp" subcommand serves the same agent over the Agent Client Protocol
// instead, so an editor or agent harness can drive it:
//
//	zot acp
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"

	"github.com/chatbotkit/zot"
	"github.com/chatbotkit/zot/internal/config"
	"github.com/chatbotkit/zot/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zot: "+err.Error())
		os.Exit(1)
	}
}

// acpCommand is the argv[0]-after-the-binary word that switches zot from
// "run one task" to "serve the Agent Client Protocol". It is matched before
// flags are parsed, so a literal task of "acp" needs --task-file.
const acpCommand = "acp"

func run() error {
	if len(os.Args) > 1 && os.Args[1] == acpCommand {
		loadEnv(".")
		return runACP(os.Args[2:])
	}

	// `zot config` opens the config file in $EDITOR, seeding it from the embedded
	// template on first run. `zot config path` prints its location.
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if len(os.Args) > 2 && os.Args[2] == "path" {
			fmt.Println(config.DefaultConfigPath())
			return nil
		}
		return editConfig()
	}

	configPath := flag.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	backend := flag.String("backend", "", "backend to run against: relay (default), cbk, or chatbotkit")
	model := flag.String("model", "", "override the model name")
	dir := flag.String("dir", ".", "working directory the agent reads, writes and runs commands in")
	maxIter := flag.Int("max-iterations", 0, "override the safety cap on agent iterations")
	taskFile := flag.String("task-file", "", "read the task from this file instead of the command line")
	diffFlag := flag.Bool("diff", false, "show a syntax-highlighted diff panel under each edit/write")
	plainFlag := flag.Bool("plain", false, "stream unstyled output instead of the full-screen UI (auto-enabled when not a TTY)")
	var featureFlags stringSlice
	flag.Var(&featureFlags, "feature", "enable a ChatBotKit feature by name (repeatable): "+strings.Join(config.AllowedFeatures, ", "))
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("zot %s\n", version.Version)
		return nil
	}

	// Load credentials from the directory the agent will work in. This must
	// happen after --dir is parsed but before configuration resolves env-backed
	// backend secrets.
	loadEnv(*dir)

	task, err := resolveTask(*taskFile, flag.Args())
	if err != nil {
		return err
	}

	cfg, err := zot.Load(*configPath)
	if err != nil {
		return err
	}

	// CLI overrides win over file and env. The bool --diff only overrides when it
	// was actually passed, so a config-enabled diff stays on without it.
	if *backend != "" {
		cfg.DefaultBackend = *backend
	}
	if *model != "" {
		cfg.Agent.Model = *model
	}
	if *maxIter > 0 {
		cfg.Agent.MaxIterations = *maxIter
	}
	// --feature (repeatable) replaces the configured features when given.
	if len(featureFlags) > 0 {
		features := make([]config.Feature, 0, len(featureFlags))
		for _, name := range featureFlags {
			features = append(features, config.Feature{Name: name})
		}
		cfg.Features = features
	}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "diff":
			cfg.UI.Diff = *diffFlag
		case "plain":
			cfg.UI.Plain = *plainFlag
		}
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

	return zot.Run(context.Background(), cfg, task)
}

func loadEnv(dir string) {
	_ = godotenv.Load(filepath.Join(dir, ".env"))
}

// resolveTask determines the single task string from --task-file or the
// positional arguments. There is intentionally no interactive prompt: zot is a
// viewer, not a chat client.
// editConfig ensures the config file exists - seeding it from the embedded
// template on first run - and opens it in the user's editor. This is the setup
// path: configure the backend, model and provider key by editing the file.
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func resolveTask(taskFile string, args []string) (string, error) {
	if taskFile != "" {
		data, err := os.ReadFile(taskFile)
		if err != nil {
			return "", fmt.Errorf("cannot read --task-file: %w", err)
		}
		task := strings.TrimSpace(string(data))
		if task == "" {
			return "", fmt.Errorf("--task-file %q is empty", taskFile)
		}
		return task, nil
	}

	task := strings.TrimSpace(strings.Join(args, " "))
	if task == "" {
		usage()
		return "", fmt.Errorf("no task given")
	}
	return task, nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `zot - an automated software factory powered by an autonomous coding harness

Usage:
  zot [flags] "your task in plain english"
  zot config
  zot acp [flags]

Examples:
  zot "add input validation to the signup handler and a test"
  zot --dir ./scratch "scaffold a tiny http server in go"

Commands:
  config   edit the config file in $EDITOR (creates it on first run)
  acp      serve the agent over the Agent Client Protocol (see zot acp -h)

Flags:`)
	flag.PrintDefaults()
}

// stringSlice is a flag.Value that accumulates a repeatable string flag.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }

func (s *stringSlice) Set(v string) error {
	*s = append(*s, strings.TrimSpace(v))
	return nil
}
