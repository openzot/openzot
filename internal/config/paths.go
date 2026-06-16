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

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".config")
}

func homeDir() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}

	// Fallback for unusual environments.
	return "/tmp/zot-" + strconv.Itoa(os.Getuid())
}
