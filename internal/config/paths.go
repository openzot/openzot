package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultConfigPath returns the resolved config file path using a fallback
// chain:
//
//  1. $ZOT_CONFIG environment variable (if set and non-empty)
//  2. $XDG_CONFIG_HOME/zot/config.yaml (if XDG_CONFIG_HOME is set)
//  3. ~/.config/zot/config.yaml
func DefaultConfigPath() string {
	if envPath := strings.TrimSpace(os.Getenv("ZOT_CONFIG")); envPath != "" {
		return envPath
	}

	return filepath.Join(xdgConfigHome(), "zot", "config.yaml")
}

// ConfigDir returns the directory that holds the config file - and any global
// AGENTS.md / skills - for the given --config value ("" uses the default path).
func ConfigDir(path string) string {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigPath()
	}
	return filepath.Dir(path)
}

// DefaultSessionDir returns where session logs are written:
//
//  1. $ZOT_SESSION_DIR (if set and non-empty)
//  2. $XDG_STATE_HOME/zot/sessions (if XDG_STATE_HOME is set)
//  3. ~/.local/state/zot/sessions
//
// State rather than config, because a session log is something zot produces and
// may prune, not something the user edits. Keeping run logs out of the config
// directory also means a synced dotfiles setup does not carry transcripts of
// every run to every machine.
func DefaultSessionDir() string {
	if dir := strings.TrimSpace(os.Getenv("ZOT_SESSION_DIR")); dir != "" {
		return dir
	}

	return filepath.Join(xdgStateHome(), "zot", "sessions")
}

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".config")
}

func xdgStateHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return dir
	}

	return filepath.Join(homeDir(), ".local", "state")
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}

	// Fallback for unusual environments.
	return "/tmp/zot-" + strconv.Itoa(os.Getuid())
}
