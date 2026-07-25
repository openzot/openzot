package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chatbotkit/zot"
	"github.com/chatbotkit/zot/internal/config"
)

// runACP serves the agent over the Agent Client Protocol on stdin/stdout. It has
// its own flag set because the mode has a different shape from a normal run:
// there is no task, no working directory (the client supplies one per session)
// and no viewer, so the flags that drive those would be silently ignored.
func runACP(args []string) error {
	fs := flag.NewFlagSet("zot acp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	configPath := fs.String("config", "", "path to zot config (default: "+config.DefaultConfigPath()+", optional)")
	backend := fs.String("backend", "", "backend to run against (default: the configured default, cbk)")
	model := fs.String("model", "", "override the model name")
	maxIter := fs.Int("max-iterations", 0, "override the safety cap on agent iterations per turn")
	var featureFlags stringSlice
	fs.Var(&featureFlags, "feature", "enable a ChatBotKit feature by name (repeatable): "+strings.Join(config.AllowedFeatures, ", "))

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `zot acp - serve zot over the Agent Client Protocol

The protocol runs as JSON-RPC on stdin/stdout, so this is meant to be spawned by
an ACP client (an editor, or a harness such as Buzz's buzz-acp) rather than run
by hand. Each session works in the directory the client supplies, and each
prompt is one turn of a conversation.

Usage:
  zot acp [flags]

Flags:`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("acp takes no task argument - the client sends the prompts")
	}

	cfg, err := zot.Load(*configPath)
	if err != nil {
		return err
	}

	if *backend != "" {
		cfg.DefaultBackend = *backend
	}
	if *model != "" {
		cfg.Agent.Model = *model
	}
	if *maxIter > 0 {
		cfg.Agent.MaxIterations = *maxIter
	}
	if len(featureFlags) > 0 {
		features := make([]config.Feature, 0, len(featureFlags))
		for _, name := range featureFlags {
			features = append(features, config.Feature{Name: name})
		}
		cfg.Features = features
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// The config directory supplies global AGENT.md / skills; every session
	// layers its own workspace context on top. Unlike a normal run there is no
	// chdir here - the working directory arrives with each session.
	configDir := config.ConfigDir(*configPath)
	if abs, err := filepath.Abs(configDir); err == nil {
		configDir = abs
	}

	// A harness stops an agent by signalling it. Shutting down on that lets the
	// current turn unwind instead of dying mid-tool-call.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return zot.ServeACP(ctx, cfg, configDir, logACP)
}

// logACP writes diagnostics to stderr. Stdout carries the protocol and must stay
// clean.
func logACP(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "zot acp: "+format+"\n", args...)
}
