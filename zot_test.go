package zot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectContext(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()

	// A global AGENT.md in the config dir and a project one in the work dir.
	mustWrite(t, filepath.Join(configDir, "AGENT.md"), "GLOBAL CONVENTIONS")
	mustWrite(t, filepath.Join(workDir, "AGENT.md"), "PROJECT CONVENTIONS")

	// A skill in each location: plain "skills/" in the config dir and hidden
	// ".skills/" in the project dir - both layouts must be picked up.
	mustWrite(t, filepath.Join(configDir, "skills", "greet", "SKILL.md"),
		"---\nname: greet\ndescription: say hello\n---\nbody")
	mustWrite(t, filepath.Join(workDir, ".skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: ship it\n---\nbody")

	cfg := Config{}
	if err := LoadProjectContext(&cfg, configDir, workDir); err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}

	// Backstory keeps the default and appends both AGENT.md files in order.
	for _, want := range []string{DefaultBackstory[:20], "GLOBAL CONVENTIONS", "PROJECT CONVENTIONS"} {
		if !strings.Contains(cfg.Agent.Backstory, want) {
			t.Errorf("backstory missing %q", want)
		}
	}
	if i, j := strings.Index(cfg.Agent.Backstory, "GLOBAL"), strings.Index(cfg.Agent.Backstory, "PROJECT"); i > j {
		t.Error("expected config-dir AGENT.md to appear before work-dir AGENT.md")
	}

	// A single skills feature carrying both skills.
	var skills *Feature
	for i := range cfg.Features {
		if cfg.Features[i].Name == "skills" {
			skills = &cfg.Features[i]
		}
	}
	if skills == nil {
		t.Fatal("expected a skills feature to be added")
	}
	list, _ := skills.Options["skills"].([]map[string]string)
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d (%v)", len(list), skills.Options["skills"])
	}
}

func TestLoadProjectContextNoFiles(t *testing.T) {
	cfg := Config{}
	if err := LoadProjectContext(&cfg, t.TempDir()); err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}
	if cfg.Agent.Backstory != "" {
		t.Error("expected backstory untouched when no AGENT.md is present")
	}
	if len(cfg.Features) != 0 {
		t.Error("expected no features when no skills are present")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
