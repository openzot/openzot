// Command zotui serves the browser command center over the zot engine. Workers,
// runs, and output live in a store, so closing the browser loses no state.
//
// The only other command is `zotui config`, which opens the config in $EDITOR
// (seeding it from an embedded template on first run). The config file locates the
// repos, compute, model providers and environments; override its path
// with $ZOTUI_CONFIG.
//
// Local Docker and GitHub-backed Vercel Sandbox runs work end to end.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/openzot/openzot/configs"
	"github.com/openzot/openzot/internal/zotui/app"
	"github.com/openzot/openzot/internal/zotui/config"
	"github.com/openzot/openzot/internal/zotui/store"
	"github.com/openzot/openzot/internal/zotui/web"
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

	// Everything else serves the command center.
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

	addr := firstNonEmpty(os.Getenv("ZOTUI_ADDR"), "127.0.0.1:8080")
	// Extra Host-header names the server answers to, beyond loopback and the
	// bind host. "*" disables the check. Binding a wildcard address does not
	// widen this: which interfaces accept connections and which names a browser
	// page may aim at the API are separate questions.
	allowedHosts := strings.Split(os.Getenv("ZOTUI_ALLOWED_HOSTS"), ",")
	fmt.Fprintf(os.Stderr, "zotui: command center at http://%s\n", addr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	commandCenter := app.New(cfg, st)

	// Runs a previous process left mid-flight have no sandbox to return to.
	if reconciled, err := commandCenter.Reconcile(context.Background()); err != nil {
		return err
	} else if reconciled > 0 {
		fmt.Fprintf(os.Stderr, "zotui: failed %d run(s) interrupted by an earlier shutdown\n", reconciled)
	}

	go commandCenter.RunScheduler(ctx)
	err = web.Serve(ctx, addr, web.New(commandCenter, addr, allowedHosts...))

	// Draining before exit is what stops a signal from orphaning sandboxes that
	// still hold the run's repository token.
	drain, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	if drainErr := commandCenter.Shutdown(drain); drainErr != nil {
		fmt.Fprintln(os.Stderr, "zotui: "+drainErr.Error())
	}
	return err
}

// drainTimeout bounds how long a shutdown waits for in-flight runs to tear their
// sandboxes down before giving up and reporting what is left.
const drainTimeout = 30 * time.Second

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
