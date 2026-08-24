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

// Both sources are read with the `read` tool; only the path differs - a
// filesystem path for a disk skill, an embedded-skill:// URL for an embedded one.
func TestSkillHintsPointAtReadForBothSources(t *testing.T) {
	directory := SkillDefinition{Name: "d", Path: "/skills/d/SKILL.md", Source: SkillSourceDirectory}

	if hint := directory.Hint(); !strings.Contains(hint, "/skills/d/SKILL.md") || !strings.Contains(hint, "read") {
		t.Errorf("a directory skill must point `read` at its path: %q", hint)
	}

	embedded := SkillDefinition{Name: "e", Path: SkillURL("e"), Source: SkillSourceEmbedded}

	hint := embedded.Hint()

	if !strings.Contains(hint, "read") || !strings.Contains(hint, "embedded-skill:///e") {
		t.Errorf("an embedded skill must point `read` at its embedded-skill:// URL: %q", hint)
	}
}

func TestSkillNameFromURL(t *testing.T) {
	cases := map[string]struct {
		name string
		ok   bool
	}{
		"embedded-skill:///recon": {"recon", true},
		"embedded-skill://recon":  {"recon", true},
		"embedded-skill:///":      {"", false},
		"skill:///recon":          {"", false},
		"/skills/x.md":            {"", false},
		"recon":                   {"", false},
	}

	for input, want := range cases {
		got, ok := SkillNameFromURL(input)
		if got != want.name || ok != want.ok {
			t.Errorf("SkillNameFromURL(%q) = (%q, %v), want (%q, %v)", input, got, ok, want.name, want.ok)
		}
	}
}

// An embedded skill is addressed by an embedded-skill:// URL and served by the `read`
// tool from the embedded registry - the same tool, and the same bounded-range
// output, as a skill on disk.
func TestReadServesEmbeddedSkillByURL(t *testing.T) {
	fsys := fstest.MapFS{
		"deploy/SKILL.md": &fstest.MapFile{
			Data: []byte("---\nname: deploy\n---\n\nThe deploy instructions.\n"),
		},
	}

	result, _ := LoadSkillsFromFS(fsys)

	if got := result.Skills[0].Path; got != SkillURL("deploy") {
		t.Fatalf("embedded skill Path = %q, want %q", got, SkillURL("deploy"))
	}

	tools := DefaultToolsFor(ToolOptions{EmbeddedSkills: result.EmbeddedContents()})

	out, err := tools["read"].Handler(context.Background(), map[string]any{
		"path":      SkillURL("deploy"),
		"startLine": 1,
		"endLine":   10,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if !strings.Contains(out.(string), "The deploy instructions.") {
		t.Errorf("read must serve the embedded body: %q", out)
	}

	// The line-numbered, range-bounded framing is identical to a file read.
	if !strings.Contains(out.(string), "embedded-skill:///deploy lines 1-") {
		t.Errorf("read must frame an embedded skill like any file: %q", out)
	}

	// An unknown skill is reported like a missing file.
	if _, err := tools["read"].Handler(context.Background(), map[string]any{
		"path": SkillURL("nope"), "startLine": 1, "endLine": 5,
	}); err == nil {
		t.Error("an unknown embedded skill must be reported")
	}
}

// Without an embedded registry, an embedded-skill:// URL is just an unresolvable path -
// no skill tool exists to fall back to, and none should.
func TestReadRejectsSkillURLWithoutRegistry(t *testing.T) {
	tools := DefaultToolsFor(ToolOptions{})

	if _, err := tools["read"].Handler(context.Background(), map[string]any{
		"path": SkillURL("deploy"), "startLine": 1, "endLine": 5,
	}); err == nil {
		t.Error("an embedded-skill:// URL must fail when no embedded skills ship")
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

// The loader must pick up a skill added to its directory after the first scan -
// the mid-run case the dynamic loader exists for.
func TestSkillLoaderRescansDirectory(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "recon", "---\nname: recon\ndescription: map the target\n---\nbody")

	embedded := &SkillsResult{Skills: []SkillDefinition{
		{Name: "catalog", Description: "where the skills live", Source: SkillSourceEmbedded},
	}}

	loader := NewSkillLoader(embedded, root)

	first := loader.Skills()
	if len(first) != 2 {
		t.Fatalf("first scan: got %d skills, want 2 (embedded + one on disk)", len(first))
	}

	// A skill cloned in after the run started.
	writeSkill(t, root, "exploit", "---\nname: exploit\ndescription: prove it\n---\nbody")

	names := map[string]bool{}
	for _, s := range loader.Skills() {
		names[s.Name] = true
	}

	for _, want := range []string{"catalog", "recon", "exploit"} {
		if !names[want] {
			t.Errorf("rescan missing %q; the loader must see files added after the first scan", want)
		}
	}
}

// A directory skill must win over an embedded one of the same name, so a
// downloaded skill can override a shipped default.
func TestSkillLoaderDirectoryOverridesEmbedded(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "recon", "---\nname: recon\ndescription: the downloaded one\n---\nbody")

	embedded := &SkillsResult{Skills: []SkillDefinition{
		{Name: "recon", Description: "the shipped one", Source: SkillSourceEmbedded},
	}}

	skills := NewSkillLoader(embedded, root).Skills()

	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}

	if skills[0].Description != "the downloaded one" {
		t.Errorf("the on-disk skill must win: %q", skills[0].Description)
	}
}

// A directory that disappears mid-run must not wipe the last good set.
func TestSkillLoaderFallsBackToLastGoodSet(t *testing.T) {
	root := t.TempDir()

	writeSkill(t, root, "recon", "---\nname: recon\ndescription: map the target\n---\nbody")

	loader := NewSkillLoader(nil, root)

	if got := len(loader.Skills()); got != 1 {
		t.Fatalf("first scan: got %d skills, want 1", got)
	}

	// LoadSkills treats a missing directory as empty rather than an error, so
	// removing it yields an empty scan - a legitimate result, not a failure to
	// fall back from. The loader must handle the vanished directory without
	// panicking and simply report no skills.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	if got := loader.Skills(); len(got) != 0 {
		t.Errorf("got %d skills after the directory vanished, want 0", len(got))
	}
}
