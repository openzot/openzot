// Command zot is an autonomous coding agent you watch, not drive.
//
// It flips the usual coding-TUI model: there is no prompt and no chat box. You
// hand zot a single task on the command line, and it works the problem on its
// own - reading files, editing them, and running shell commands - while the
// terminal streams a live, read-only view of everything it does.
//
// Usage:
//
//	export CHATBOTKIT_API_SECRET="your-api-key"
//	zot "add a /health endpoint to the Go server and a test for it"
//
//	# operate inside a specific directory and cap the work
//	zot --dir ./scratch --max-iterations 40 "scaffold a snake game in python"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
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

func run() error {
	// Load a .env from the working directory if present, so CHATBOTKIT_API_SECRET
	// (and friends) can live alongside the project being worked on.
	_ = godotenv.Load()

	configPath := flag.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	model := flag.String("model", "", "override the ChatBotKit model alias")
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

	// Sandbox the coding tools to --dir before the agent starts. DefaultTools()
	// operates relative to the process working directory, so a chdir is the
	// simplest way to scope the agent to the target project.
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

// resolveTask determines the single task string from --task-file or the
// positional arguments. There is intentionally no interactive prompt: zot is a
// viewer, not a chat client.
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
	fmt.Fprintln(os.Stderr, `zot - an autonomous coding agent you watch, not drive

Usage:
  zot [flags] "your task in plain english"

Examples:
  zot "add input validation to the signup handler and a test"
  zot --dir ./scratch "scaffold a tiny http server in go"

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
