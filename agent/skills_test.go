package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()

	dir := filepath.Join(root, name)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSkillsReadsFrontMatter(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "deploy", `---
name: deploy-service
description: Ship a service to production
---

# Deploy

Long instructions the model reads only when it decides the skill is relevant.
`)

	result, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	if len(result.Skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(result.Skills))
	}

	skill := result.Skills[0]

	if skill.Name != "deploy-service" {
		t.Errorf("name = %q, want the front-matter name to win over the directory", skill.Name)
	}

	if skill.Description != "Ship a service to production" {
		t.Errorf("description = %q", skill.Description)
	}

	// the path is what the model reads; the body is not loaded into context
	if skill.Path == "" {
		t.Error("a skill must carry the path to its instructions")
	}
}

func TestLoadSkillsFallsBackToTheBody(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "review", "# Review\n\nReview a pull request carefully.\n")

	result, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	if len(result.Skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(result.Skills))
	}

	skill := result.Skills[0]

	// without front matter the directory names it and the first prose line
	// describes it
	if skill.Name != "review" {
		t.Errorf("name = %q, want the directory name", skill.Name)
	}

	if skill.Description != "Review a pull request carefully." {
		t.Errorf("description = %q, want the first prose line", skill.Description)
	}
}

func TestLoadSkillsSkipsNonSkillDirectories(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "real", "---\nname: real\n---\n")

	// a skills folder routinely holds other things; they are skipped rather
	// than treated as an error
	os.MkdirAll(filepath.Join(root, "notaskill"), 0o755)
	os.WriteFile(filepath.Join(root, "loose.md"), []byte("x"), 0o644)

	result, err := LoadSkills([]string{root})
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	if len(result.Skills) != 1 {
		t.Fatalf("got %d skills, want just the real one: %+v", len(result.Skills), result.Skills)
	}
}

func TestLoadSkillsToleratesAMissingDirectory(t *testing.T) {
	result, err := LoadSkills([]string{filepath.Join(t.TempDir(), "nope")})
	if err != nil {
		t.Fatalf("a missing skills directory must not be an error: %v", err)
	}

	if len(result.Skills) != 0 {
		t.Errorf("got %d skills, want none", len(result.Skills))
	}
}

func TestLoadSkillsFromFS(t *testing.T) {
	fsys := fstest.MapFS{
		"deploy/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: deploy\ndescription: ship it\n---\n"),
		},
		"other/README.md": &fstest.MapFile{Data: []byte("not a skill")},
	}

	result, err := LoadSkillsFromFS(fsys)
	if err != nil {
		t.Fatalf("LoadSkillsFromFS: %v", err)
	}

	if len(result.Skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(result.Skills))
	}

	if result.Skills[0].Description != "ship it" {
		t.Errorf("description = %q", result.Skills[0].Description)
	}
}

func TestParseSkillStripsQuotes(t *testing.T) {
	skill := parseSkill("dir", "p", "---\nname: \"quoted name\"\ndescription: 'quoted desc'\n---\n")

	if skill.Name != "quoted name" {
		t.Errorf("name = %q, want the quotes stripped", skill.Name)
	}

	if skill.Description != "quoted desc" {
		t.Errorf("description = %q, want the quotes stripped", skill.Description)
	}
}

func TestEmbeddedSkillsCarryTheirContents(t *testing.T) {
	fsys := fstest.MapFS{
		"deploy/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: deploy\ndescription: ship it\n---\n\nStep one: build.\n"),
		},
		"nested/category/review/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: review\ndescription: review a change\n---\n"),
		},
	}

	result, err := LoadSkillsFromFS(fsys)
	if err != nil {
		t.Fatalf("LoadSkillsFromFS: %v", err)
	}

	if len(result.Skills) != 2 {
		t.Fatalf("got %d skills, want 2 - a nested tree must be walked", len(result.Skills))
	}

	for _, skill := range result.Skills {
		if skill.Source != SkillSourceEmbedded {
			t.Errorf("%s: source = %q, want embedded", skill.Name, skill.Source)
		}
	}
}

// The two sources need different verbs: a file the model can read, versus a
// skill only the tool can serve.
func TestSkillHintsDependOnSource(t *testing.T) {
	directory := SkillDefinition{Name: "d", Path: "/skills/d/SKILL.md", Source: SkillSourceDirectory}

	if hint := directory.Hint(); !strings.Contains(hint, "/skills/d/SKILL.md") || !strings.Contains(hint, "read") {
		t.Errorf("a directory skill must point at its path: %q", hint)
	}

	embedded := SkillDefinition{Name: "e", Path: "e/SKILL.md", Source: SkillSourceEmbedded}

	hint := embedded.Hint()

	if !strings.Contains(hint, "skill") || !strings.Contains(hint, `"e"`) {
		t.Errorf("an embedded skill must name the tool and itself: %q", hint)
	}

	if strings.Contains(hint, "read` tool") {
		t.Errorf("an embedded skill has no readable path: %q", hint)
	}
}

func TestSkillToolServesEmbeddedContents(t *testing.T) {
	fsys := fstest.MapFS{
		"deploy/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: deploy\n---\n\nThe deploy instructions.\n"),
		},
	}

	result, _ := LoadSkillsFromFS(fsys)

	tool := result.Tool()

	if tool == nil {
		t.Fatal("a set with embedded skills must expose the skill tool")
	}

	got, err := tool.Handler(context.Background(), map[string]any{"name": "deploy"})
	if err != nil {
		t.Fatalf("skill tool: %v", err)
	}

	if !strings.Contains(got.(string), "The deploy instructions.") {
		t.Errorf("the tool must serve the body: %q", got)
	}

	if _, err := tool.Handler(context.Background(), map[string]any{"name": "nope"}); err == nil {
		t.Error("an unknown skill must be reported")
	}
}

// A tool that can never succeed is worse than no tool.
func TestSkillToolAbsentWithoutEmbeddedSkills(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "ondisk", "---\nname: ondisk\n---\n")

	result, _ := LoadSkills([]string{root})

	if result.Tool() != nil {
		t.Error("a directory-only skill set must not expose the skill tool")
	}
}

// A project skill must be able to override a shipped one of the same name.
func TestMergePrefersEarlierSets(t *testing.T) {
	project := &SkillsResult{Skills: []SkillDefinition{
		{Name: "deploy", Description: "the project's own"},
	}}

	builtin := &SkillsResult{Skills: []SkillDefinition{
		{Name: "deploy", Description: "the shipped default"},
		{Name: "review", Description: "also shipped"},
	}}

	merged := Merge(project, builtin, nil)

	if len(merged.Skills) != 2 {
		t.Fatalf("got %d skills, want 2", len(merged.Skills))
	}

	if merged.Skills[0].Description != "the project's own" {
		t.Errorf("the earlier set must win: %q", merged.Skills[0].Description)
	}
}
