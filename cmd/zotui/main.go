// Command zotui is the command center over the zot engine. Running it opens a
// full-screen terminal app to see scheduled tasks, inspect them, cancel one, and
// create a new one; jobs and their progress live in a store, so you can close the
// tool and reopen it later to see where things got to.
//
// The only other command is `zotui config`, which opens the config in $EDITOR
// (seeding it from an embedded template on first run). The config file locates the
// repos, compute, models and environments; override its path
// with $ZOTUI_CONFIG.
//
// This is an early scaffold: the config, store, scheduling flow and the TUI are in
// place; the GitHub token exchange and Cloudflare compute are stubbed, so a
// dispatched job currently fails fast at those seams.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openzot/openzot/configs"
	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/store"
	"github.com/openzot/openzot/internal/zotui/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "zotui: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]

	// `zotui config` opens the config in $EDITOR (seeding it on first run);
	// `zotui config path` prints its location.
	if len(args) > 0 && args[0] == "config" {
		if len(args) > 1 && args[1] == "path" {
			fmt.Println(config.DefaultConfigPath())
			return nil
		}
		return editConfig()
	}

	// Everything else opens the command center.
	path := config.DefaultConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("no config at %s - run 'zotui config' to create one", path)
	}

	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	st, err := store.Open(store.Config{Driver: cfg.Store.Driver, DSN: cfg.Store.DSN})
	if err != nil {
		return err
	}
	defer st.Close()

	return tui.Run(app.New(cfg, st))
}

// editConfig ensures the config file exists - seeding it from the embedded
// template on first run - and opens it in the user's editor.
func editConfig() error {
	path := config.DefaultConfigPath()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, configs.Zotui, 0o600); err != nil {
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
