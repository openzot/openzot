package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// SkillSource says where a skill's instructions live, and therefore how the
// model should read them.
//
// It exists because the two cases need different verbs. A skill on disk is read
// with the ordinary file tool; an embedded skill is compiled into the binary and
// has no path the shell can reach, so the model has to be told to ask for it by
// name instead. Without the distinction an embedded skill looks like a file that
// does not exist, and the model spends a tool call finding that out.
type SkillSource string

const (
	// SkillSourceDirectory is a skill on the filesystem, readable with `read`.
	SkillSourceDirectory SkillSource = "directory"

	// SkillSourceEmbedded is a skill compiled into the binary, readable only
	// with the `skill` tool.
	SkillSourceEmbedded SkillSource = "embedded"
)

// SkillDefinition is a capability advertised to the model.
//
// Path points at the instructions rather than containing them. The model reads
// it only when it decides the skill is relevant, which keeps a large library
// cheap: until then, just the name and description occupy context.
type SkillDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Path        string      `json:"path"`
	Source      SkillSource `json:"source,omitempty"`

	// contents is the embedded body, held so the skill tool can serve it
	// without touching the filesystem.
	contents string
}

// Hint is the instruction appended to a skill's description telling the model
// how to reach its instructions.
//
// Generated rather than authored, so a skill's own description never has to
// know whether it was loaded from disk or embedded - the same SKILL.md works
// either way.
func (s SkillDefinition) Hint() string {
	switch s.Source {
	case SkillSourceEmbedded:
		return fmt.Sprintf("Read it with the `skill` tool: skill(name=%q).", s.Name)
	default:
		return fmt.Sprintf("Read it with the `read` tool at %s.", s.Path)
	}
}

// SkillsResult is a loaded skill set.
type SkillsResult struct {
	Skills []SkillDefinition
}

// Tool returns a `skill` tool that serves the embedded skills in this set.
//
// Returns nil when the set has no embedded skills - a tool that can never
// succeed is worse than no tool, because the model will try it anyway.
func (r *SkillsResult) Tool() *ToolDefinition {
	embedded := map[string]string{}

	var names []string

	for _, skill := range r.Skills {
		if skill.Source == SkillSourceEmbedded {
			embedded[skill.Name] = skill.contents

			names = append(names, skill.Name)
		}
	}

	if len(embedded) == 0 {
		return nil
	}

	sort.Strings(names)

	return &ToolDefinition{
		Description: fmt.Sprintf(
			"Read the instructions for a built-in skill. Available: %s.",
			strings.Join(names, ", "),
		),
		Parameters: FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The skill to read",
					"enum":        names,
				},
			},
			"required": []string{"name"},
		},
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			name, _ := args["name"].(string)

			contents, ok := embedded[name]
			if !ok {
				return nil, fmt.Errorf("no built-in skill named %q; available: %s",
					name, strings.Join(names, ", "))
			}

			return contents, nil
		},
	}
}

// Merge combines skill sets, later entries losing to earlier ones on a name
// clash.
//
// The order matters and is deliberate: a project's own skill should win over a
// built-in one with the same name, which is what lets a repository override a
// shipped default rather than being stuck with it.
func Merge(sets ...*SkillsResult) *SkillsResult {
	merged := &SkillsResult{}

	seen := map[string]bool{}

	for _, set := range sets {
		if set == nil {
			continue
		}

		for _, skill := range set.Skills {
			if seen[skill.Name] {
				continue
			}

			seen[skill.Name] = true

			merged.Skills = append(merged.Skills, skill)
		}
	}

	return merged
}

// LoadSkills discovers skills in the given directories.
//
// A skill is a directory containing a SKILL.md whose front matter supplies the
// name and description. A directory without one is skipped rather than treated
// as an error - a skills folder routinely contains other things.
func LoadSkills(directories []string) (*SkillsResult, error) {
	result := &SkillsResult{}

	for _, directory := range directories {
		entries, err := os.ReadDir(directory)

		if err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return nil, err
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			file := filepath.Join(directory, entry.Name(), "SKILL.md")

			content, err := os.ReadFile(file)
			if err != nil {
				continue
			}

			skill := parseSkill(entry.Name(), file, string(content))

			skill.Source = SkillSourceDirectory

			result.Skills = append(result.Skills, skill)
		}
	}

	return result, nil
}

// LoadSkillsFromFS discovers skills in a filesystem, for skills compiled into
// the binary with go:embed.
//
// The body is retained rather than only the path, because an embedded file has
// no path the model can reach - the `skill` tool serves it from memory. Nest as
// deeply as you like: any directory containing a SKILL.md is a skill, so an
// embedded tree can be organised by category.
func LoadSkillsFromFS(fsys fs.FS) (*SkillsResult, error) {
	result := &SkillsResult{}

	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() || path.Base(name) != "SKILL.md" {
			return nil
		}

		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			// a skill that cannot be read is skipped, not fatal: one broken
			// entry must not cost the caller its whole library
			return nil
		}

		directory := path.Base(path.Dir(name))

		if directory == "." || directory == "" {
			directory = strings.TrimSuffix(path.Base(name), ".md")
		}

		skill := parseSkill(directory, name, string(content))

		skill.Source = SkillSourceEmbedded
		skill.contents = string(content)

		result.Skills = append(result.Skills, skill)

		return nil
	})

	if err != nil {
		return result, nil
	}

	sort.Slice(result.Skills, func(i, j int) bool {
		return result.Skills[i].Name < result.Skills[j].Name
	})

	return result, nil
}

// parseSkill reads the name and description out of a SKILL.md.
//
// Front matter is preferred; the first heading and paragraph are the fallback so
// a skill written without front matter still works.
func parseSkill(directory, location, content string) SkillDefinition {
	skill := SkillDefinition{Name: directory, Path: location}

	lines := strings.Split(content, "\n")

	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for _, line := range lines[1:] {
			trimmed := strings.TrimSpace(line)

			if trimmed == "---" {
				break
			}

			key, value, found := strings.Cut(trimmed, ":")

			if !found {
				continue
			}

			value = strings.Trim(strings.TrimSpace(value), `"'`)

			switch strings.ToLower(strings.TrimSpace(key)) {
			case "name":
				if value != "" {
					skill.Name = value
				}
			case "description":
				skill.Description = value
			}
		}
	}

	if skill.Description == "" {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)

			if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "#") {
				continue
			}

			skill.Description = trimmed

			break
		}
	}

	return skill
}
