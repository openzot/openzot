package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/openzot/openzot"
	"github.com/openzot/openzot/internal/config"
	"github.com/spf13/pflag"
)

// The operator's references have to keep step with the binary, and nothing
// else fails when they drift: a flag the CLI takes but the documentation omits,
// or a config field the seeded template never names, ships silently. These
// guards read what a person actually reads - docs/configuration.md's flags
// table and the template `zot config` seeds - and hold both against what this
// binary really accepts.

// repoFile resolves a repository file from this package's directory, failing
// the test when it cannot be read - a guard that cannot see its subject says
// so rather than passing vacuously.
func repoFile(t *testing.T, rel ...string) string {
	t.Helper()

	path := filepath.Join(append([]string{"..", ".."}, rel...)...)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

// registeredFlags returns the parser's view of the top-level flags: every long
// name, plus the shorthand each resolves from. The globals register themselves
// into pflag.CommandLine while run() parses, so a throwaway invocation
// enumerates them exactly as ./zot --help shows them.
func registeredFlags(t *testing.T) (names []string, shorthands map[string]string) {
	t.Helper()

	withArgs(t, "--version")

	if _, err := captureStdout(t, run); err != nil {
		t.Fatalf("run --version: %v", err)
	}

	shorthands = map[string]string{}

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		names = append(names, f.Name)

		if f.Shorthand != "" {
			shorthands[f.Shorthand] = f.Name
		}
	})

	if len(names) == 0 {
		t.Fatal("no flags were registered by a real invocation; the guard is reading nothing")
	}

	sort.Strings(names)

	return names, shorthands
}

// flagRows parses the Flags table out of the markdown and returns each data
// row's flag tokens as written (`--rerun`, `-h`), preserving row boundaries so
// duplication stays visible.
//
// Only a row's first cell is read: an effect cell may mention anything in
// backticks, but the flag column is where a flag is declared. A blank line
// sitting between two rows is reported, because that is what splits a markdown
// table in two and renders everything below it as raw pipe text.
func flagRows(t *testing.T, markdown string) [][]string {
	t.Helper()

	const heading = "## Flags"

	start := strings.Index(markdown, heading)
	if start < 0 {
		t.Fatalf("docs/configuration.md has no %q section - restore it or update this guard", heading)
	}

	body := markdown[start+len(heading):]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}

	flagPattern := regexp.MustCompile(`-{1,2}[A-Za-z0-9][A-Za-z0-9-]*`)

	lines := strings.Split(body, "\n")

	isRow := func(line string) bool { return strings.HasPrefix(line, "|") }

	for i, line := range lines {
		if isRow(line) || strings.TrimSpace(line) != "" || !anyRow(lines[:i]) || !anyRow(lines[i+1:]) {
			continue
		}

		// A gap between two rows breaks the table in two; before the first row
		// or after the last one, a blank line is ordinary markdown.
		t.Fatal("the Flags table is split by a blank line - everything below the gap renders as plain text, not a table")
	}

	var (
		rows         [][]string
		sawHeader    bool
		sawDelimiter bool
	)

	for _, line := range lines {
		if !isRow(line) {
			continue
		}

		first := strings.Split(strings.TrimPrefix(line, "|"), "|")[0]

		switch {
		case !sawHeader:
			sawHeader = true // "Flag" carries no dashes, so it yields no tokens either way

		case !sawDelimiter:
			if flagPattern.MatchString(first) {
				t.Fatalf("the Flags table's second line is not a delimiter row: %q", line)
			}

			sawDelimiter = true

		default:
			rows = append(rows, flagPattern.FindAllString(first, -1))
		}
	}

	if !sawHeader || !sawDelimiter {
		t.Fatal("docs/configuration.md's Flags section holds no well-formed table")
	}

	return rows
}

// anyRow reports whether any line in lines is a table row.
func anyRow(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "|") {
			return true
		}
	}

	return false
}

// canonical maps a written flag reference to the parser's long name: `--rerun`
// spells itself, `-h` names whichever flag carries that shorthand.
func canonical(token string, shorthands map[string]string) string {
	if strings.HasPrefix(token, "--") {
		return strings.TrimPrefix(token, "--")
	}

	if strings.HasPrefix(token, "-") {
		return shorthands[strings.TrimPrefix(token, "-")]
	}

	return token
}

// Every flag the binary takes must be named in the reference, exactly once -
// and only those. A duplicated --dir row sat above blank lines that split this
// table in two once already, and half the flags were missing from it.
func TestTheFlagsReferenceListsEveryFlagExactlyOnce(t *testing.T) {
	names, shorthands := registeredFlags(t)
	rows := flagRows(t, repoFile(t, "docs", "configuration.md"))

	counts := map[string]int{}

	for _, row := range rows {
		// one row may name a flag both ways (`-h` / `--help`); that is one
		// mention of one flag, so count each row's flags once
		seen := map[string]bool{}

		for _, token := range row {
			name := canonical(token, shorthands)

			if !seen[name] {
				seen[name] = true
				counts[name]++
			}
		}
	}

	var missing, duplicated []string

	for _, name := range names {
		switch counts[name] {
		case 0:
			missing = append(missing, name)
		case 1:
		default:
			duplicated = append(duplicated, name)
		}
	}

	documented := make(map[string]bool, len(counts))
	for name := range counts {
		documented[name] = true
	}

	var extra []string

	for name := range documented {
		index := sort.SearchStrings(names, name)
		if index >= len(names) || names[index] != name {
			extra = append(extra, name)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("flags the binary takes but the reference never lists (add a row for each): %s",
			strings.Join(missing, ", "))
	}

	if len(duplicated) > 0 {
		sort.Strings(duplicated)
		t.Errorf("flags listed on more than one row (one row per flag): %s",
			strings.Join(duplicated, ", "))
	}

	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("rows naming flags the binary does not take: %s", strings.Join(extra, ", "))
	}
}

// `zot config` seeds its file from the embedded template, and
// docs/configuration.md points readers at that template as the field
// reference - so a provider connection field the binary accepts must be named
// there, or it is undiscoverable at precisely the moment an operator is
// editing a provider block. responses: went missing exactly that way.
func TestTheSeededTemplateNamesEveryProviderField(t *testing.T) {
	template := string(zot.ExampleConfigYAML)

	typ := reflect.TypeOf(config.ProviderConfig{})

	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("yaml")
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}

		if tag == "" || tag == "-" {
			continue
		}

		if !strings.Contains(template, tag+":") {
			t.Errorf("configs/zot.example.yaml never mentions %q - the template is what `zot config` seeds and what the docs point to as the field reference", tag+":")
		}
	}
}
