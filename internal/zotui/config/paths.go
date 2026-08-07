package config

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultConfigPath resolves the zotui config file path using a fallback chain:
//
//  1. $ZOTUI_CONFIG (if set and non-empty)
//  2. $XDG_CONFIG_HOME/zotui/config.yaml (if XDG_CONFIG_HOME is set)
//  3. ~/.config/zotui/config.yaml
func DefaultConfigPath() string {
	if envPath := strings.TrimSpace(os.Getenv("ZOTUI_CONFIG")); envPath != "" {
		return envPath
	}
	return filepath.Join(xdgConfigHome(), "zotui", "config.yaml")
}

func xdgConfigHome() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".config")
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}
