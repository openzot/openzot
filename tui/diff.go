package tui

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

const (
	// maxDiffLines caps how many changed/context rows a single panel renders, so
	// a large rewrite doesn't flood the log.
	maxDiffLines = 200
	// diffContext is how many unchanged lines to keep around each change.
	diffContext = 3
	// chromaStyleName is the syntax-highlighting theme (dark, sits well next to
	// the rest of the UI).
	chromaStyleName = "monokai"
)

// diff op kinds.
const (
	diffEqual = iota
	diffDelete
	diffInsert
	diffGap // a collapsed run of unchanged lines
)

type diffOp struct {
	kind   int
	oldIdx int // index into the old line slice, or -1
	newIdx int // index into the new line slice, or -1
}

// diffForTool produces a diff panel for an edit/write tool call from its args,
// or "" when there is nothing to show. For an edit the natural before/after is
// oldString → newString; a write has no prior content in the args, so its new
// content is shown as additions.
func diffForTool(name string, args map[string]interface{}, width int) string {
	switch name {
	case "edit":
		return renderDiff(str(args, "path"), str(args, "oldString"), str(args, "newString"), width)
	case "write":
		return renderDiff(str(args, "path"), "", str(args, "content"), width)
	}
	return ""
}

// renderDiff builds a framed, syntax-highlighted before/after panel for a file
// change. width is the columns available to the whole panel (border included).
func renderDiff(path, oldText, newText string, width int) string {
	contentWidth := width - 4 // rounded border (2) + horizontal padding (2)
	if contentWidth < 10 || oldText == newText {
		return ""
	}
	codeWidth := contentWidth - 2 // the "+ " / "- " gutter

	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	// Highlight each side once so multi-line constructs (strings, comments) keep
	// their context, then index the coloured lines by number.
	oldHi := highlightLines(oldText, path)
	newHi := highlightLines(newText, path)

	ops := collapseContext(lineDiff(oldLines, newLines), diffContext)

	var b strings.Builder
	var shown, added, removed, changes int
	for _, op := range ops {
		if op.kind != diffGap {
			changes++
		}
	}
	for _, op := range ops {
		switch op.kind {
		case diffGap:
			b.WriteString(diffGapStyle.Render("  ⋯") + "\n")
		case diffEqual:
			if shown < maxDiffLines {
				b.WriteString(diffContextGutter.Render("  ") + clip(lineAt(newHi, op.newIdx), codeWidth) + "\n")
			}
			shown++
		case diffDelete:
			if shown < maxDiffLines {
				b.WriteString(diffDelGutter.Render("- ") + clip(lineAt(oldHi, op.oldIdx), codeWidth) + "\n")
			}
			shown++
			removed++
		case diffInsert:
			if shown < maxDiffLines {
				b.WriteString(diffAddGutter.Render("+ ") + clip(lineAt(newHi, op.newIdx), codeWidth) + "\n")
			}
			shown++
			added++
		}
	}
	if changes > maxDiffLines {
		b.WriteString(diffGapStyle.Render(fmt.Sprintf("  … %d more lines", changes-maxDiffLines)))
	}

	title := clip(
		diffTitleStyle.Render(path)+"  "+
			diffAddGutter.Render(fmt.Sprintf("+%d", added))+" "+
			diffDelGutter.Render(fmt.Sprintf("-%d", removed)),
		contentWidth,
	)

	body := strings.TrimRight(b.String(), "\n")
	return diffPanelStyle.Width(contentWidth).Render(title + "\n" + body)
}

// highlightLines tokenises source with chroma and returns the ANSI-coloured
// lines. It degrades to the raw lines if a lexer or formatter is unavailable.
func highlightLines(source, filename string) []string {
	if strings.TrimSpace(source) == "" {
		return splitLines(source)
	}

	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(source)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}

	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return splitLines(source)
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, styles.Get(chromaStyleName), iterator); err != nil {
		return splitLines(source)
	}
	return splitLines(buf.String())
}

// lineDiff computes a line-level diff via a longest-common-subsequence table.
// Inputs are small (edit hunks, or a written file), so the O(n*m) table is fine;
// a guard falls back to a wholesale replace for pathologically large inputs.
func lineDiff(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)
	if n*m > 4_000_000 {
		ops := make([]diffOp, 0, n+m)
		for i := range oldLines {
			ops = append(ops, diffOp{diffDelete, i, -1})
		}
		for j := range newLines {
			ops = append(ops, diffOp{diffInsert, -1, j})
		}
		return ops
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{diffEqual, i, j})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{diffDelete, i, -1})
			i++
		default:
			ops = append(ops, diffOp{diffInsert, -1, j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{diffDelete, i, -1})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{diffInsert, -1, j})
	}
	return ops
}

// collapseContext keeps only unchanged lines within ctx rows of a change,
// replacing each dropped run with a single gap marker.
func collapseContext(ops []diffOp, ctx int) []diffOp {
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == diffEqual {
			continue
		}
		keep[i] = true
		for d := 1; d <= ctx; d++ {
			if i-d >= 0 {
				keep[i-d] = true
			}
			if i+d < len(ops) {
				keep[i+d] = true
			}
		}
	}

	var out []diffOp
	gap := false
	for i, op := range ops {
		if keep[i] {
			out = append(out, op)
			gap = false
		} else if !gap {
			out = append(out, diffOp{kind: diffGap, oldIdx: -1, newIdx: -1})
			gap = true
		}
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func lineAt(lines []string, idx int) string {
	if idx >= 0 && idx < len(lines) {
		return lines[idx]
	}
	return ""
}

// clip truncates an ANSI-styled string to w display columns, adding an ellipsis.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}
