package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SkillSource says where a skill's instructions live, and therefore how the
// model should read them.
//
// It exists because the two cases resolve to different paths. A skill on disk
// is read at its filesystem path; an embedded skill is compiled into the binary
// and has no such path, so it is addressed by an embedded-skill:// URL that the
// `read` tool resolves against the embedded set. Both are read with the same
// tool - the source only decides which kind of path the skill advertises.
type SkillSource string

const (
	// SkillSourceDirectory is a skill on the filesystem, read at its path.
	SkillSourceDirectory SkillSource = "directory"

	// SkillSourceEmbedded is a skill compiled into the binary, read at its
	// embedded-skill:// URL.
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

	// contents is the embedded body, held so the `read` tool can serve it from
	// memory when resolving the skill's embedded-skill:// URL.
	contents string
}

// SkillURLScheme is the scheme under which an embedded skill is addressed.
//
// An embedded skill is compiled into the binary and has no path on disk, so it
// cannot be named the way a file is. It is given a URL instead -
// embedded-skill:///name - which the `read` tool resolves against the embedded
// set rather than the filesystem. The scheme is deliberately self-describing:
// the model infers that this skill lives in the binary from the protocol alone,
// with no sentence in the prompt having to say so. Embedded and on-disk skills
// are otherwise reached the same way - one tool, one verb, the path the only
// difference.
const SkillURLScheme = "embedded-skill://"

// SkillURL is the address of an embedded skill, by name.
func SkillURL(name string) string {
	return SkillURLScheme + "/" + name
}

// SkillNameFromURL extracts the skill name from an embedded-skill:// URL,
// reporting whether the path was one. Tolerant of the slash count so
// embedded-skill://name and embedded-skill:///name both resolve.
func SkillNameFromURL(path string) (string, bool) {
	if !strings.HasPrefix(path, SkillURLScheme) {
		return "", false
	}

	name := strings.TrimLeft(strings.TrimPrefix(path, SkillURLScheme), "/")

	return name, name != ""
}

// Hint is the instruction appended to a skill's description telling the model
// how to reach its instructions.
//
// Every skill is read with the `read` tool; only the path differs - a
// filesystem path for a disk skill, an embedded-skill:// URL for an embedded one.
// Generated rather than authored, so a skill's own description never has to
// know which it is - the same SKILL.md works either way.
func (s SkillDefinition) Hint() string {
	return fmt.Sprintf("Read it with the `read` tool at %s.", s.Path)
}

// EmbeddedContents returns the bodies of the embedded skills in this set, keyed
// by name, for the `read` tool to serve against embedded-skill:// URLs. Nil when the set
// has none - a set that is entirely on-disk needs no registry.
func (r *SkillsResult) EmbeddedContents() map[string]string {
	contents := map[string]string{}

	for _, skill := range r.Skills {
		if skill.Source == SkillSourceEmbedded {
			contents[skill.Name] = skill.contents
		}
	}

	if len(contents) == 0 {
		return nil
	}

	return contents
}

// SkillsResult is a loaded skill set.
type SkillsResult struct {
	Skills []SkillDefinition
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
// no path on disk - the `read` tool serves it from this set when handed the
// skill's embedded-skill:// URL. Nest as deeply as you like: any directory
// containing a SKILL.md is a skill, so an embedded tree can be organised by
// category.
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

		// An embedded skill has no filesystem path; it is addressed by an
		// embedded-skill:// URL the `read` tool resolves against the set. Set
		// after parseSkill so the URL uses the resolved name (front matter may
		// override the directory name), which is also the registry key.
		skill.Path = SkillURL(skill.Name)

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

// SkillLoader serves a skill set that can change while a run is live.
//
// LoadSkills is a snapshot: it reads its directories once, so a skill added
// after startup - dropped in by the operator, or cloned by the agent itself
// mid-run - would never surface. The loader closes that gap by rescanning its
// directories on every call to Skills. The engine re-renders the system prompt
// each iteration, so handing it this method as the skills source makes a
// freshly added SKILL.md appear to the model on its very next turn, with no
// reload hook anyone has to remember to call.
//
// A rescan is a handful of small file reads - noise next to the model call
// that follows it - which is why there is no cache to invalidate and no
// watcher to wire up.
type SkillLoader struct {
	static      *SkillsResult
	directories []string

	mu   sync.Mutex
	last []SkillDefinition
}

// NewSkillLoader builds a loader over the given directories, layered on top of
// a static set - embedded skills, typically - that is never rescanned. Either
// part may be empty or nil. On a name clash a directory skill wins, so a skill
// on disk can override a shipped default rather than being stuck with it.
func NewSkillLoader(static *SkillsResult, directories ...string) *SkillLoader {
	return &SkillLoader{static: static, directories: directories}
}

// Skills rescans the directories and returns the current set, merged with the
// static one. Its signature matches the engine's dynamic Skills option, so a
// caller passes the method value itself.
//
// A scan that fails outright falls back to the last good set rather than to
// nothing: losing the skill list mid-run because a directory turned unreadable
// would silently cost the model capabilities it was already using.
func (l *SkillLoader) Skills() []SkillDefinition {
	l.mu.Lock()
	defer l.mu.Unlock()

	scanned, err := LoadSkills(l.directories)
	if err != nil {
		if l.last != nil {
			return l.last
		}

		scanned = &SkillsResult{}
	}

	l.last = Merge(scanned, l.static).Skills

	return l.last
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
