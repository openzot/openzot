package thread

import (
	"strings"
	"unicode"
)

// runawayTextRunTailLimit bounds how much of a committed message the backstop
// inspects, so its cost does not grow with message length.
const runawayTextRunTailLimit = 4000

// TextRunOptions tunes HasRepeatedTextRun. The zero value is the production
// default.
type TextRunOptions struct {
	// MinUnits is the number of trailing sentence-like units required before a
	// runaway is even considered. Short repetitive snippets end on their own.
	// Clamped to at least 2.
	MinUnits *int

	// Window bounds how many trailing units are inspected, so a degenerate tail
	// is still caught after a long healthy prefix. Clamped to at least MinUnits.
	Window *int

	// MaxUniqueRatio is the unique-to-inspected ratio at or below which the text
	// counts as a runaway. Healthy prose almost never repeats whole normalised
	// sentences that densely.
	MaxUniqueRatio *float64
}

func (o TextRunOptions) minUnits() int {
	return clamp(o.MinUnits, 2, 8)
}

func (o TextRunOptions) window() int {
	minUnits := o.minUnits()

	fallback := 64
	if minUnits > fallback {
		fallback = minUnits
	}

	return clamp(o.Window, minUnits, fallback)
}

func (o TextRunOptions) maxUniqueRatio() float64 {
	if o.MaxUniqueRatio != nil && *o.MaxUniqueRatio > 0 && *o.MaxUniqueRatio <= 1 {
		return *o.MaxUniqueRatio
	}

	return 0.5
}

// segmentNormalizedUnits splits text into normalised sentence-like units.
//
// Lowercasing, stripping punctuation and collapsing whitespace means phrases
// differing only in spacing or trailing punctuation collapse to the same key,
// while genuinely different sentences stay apart.
func segmentNormalizedUnits(text string) []string {
	var units []string

	for _, part := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	}) {
		var builder strings.Builder

		// @note ASCII-only on purpose: this mirrors the normalisation the
		// corpus pins. Non-ASCII collapses to a separator, which is why the CJK
		// cases are covered by the streaming guard rather than here.
		for _, r := range strings.ToLower(part) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				builder.WriteRune(r)
			default:
				builder.WriteRune(' ')
			}
		}

		unit := strings.Join(strings.Fields(builder.String()), " ")

		if unit != "" {
			units = append(units, unit)
		}
	}

	return units
}

// HasRepeatedTextRun reports a runaway repetition inside a single block of text -
// a model stuck cycling the same handful of sentences.
//
// Unlike the thread-level heuristics this works within one message, so it
// catches a turn that loops in its own reasoning without ever emitting a tool
// call - which the thread-level checks never see.
func HasRepeatedTextRun(text string, options TextRunOptions) bool {
	if text == "" {
		return false
	}

	minUnits := options.minUnits()
	window := options.window()
	maxUniqueRatio := options.maxUniqueRatio()

	bounded := text

	if len(bounded) > runawayTextRunTailLimit {
		bounded = bounded[len(bounded)-runawayTextRunTailLimit:]
	}

	units := segmentNormalizedUnits(bounded)

	if len(units) < minUnits {
		return false
	}

	considered := units

	if len(considered) > window {
		considered = considered[len(considered)-window:]
	}

	if len(considered) < minUnits {
		return false
	}

	unique := map[string]struct{}{}

	for _, unit := range considered {
		unique[unit] = struct{}{}
	}

	return float64(len(unique)) <= float64(len(considered))*maxUniqueRatio
}

// Guard thresholds for the structural-enumeration exemption.
const (
	// structureAggressiveGate is the MaxUniqueRatio at or above which a caller
	// has explicitly opted into aggressive detection, lifting the exemption.
	structureAggressiveGate = 0.5

	// minStructureNewlines is how many newlines the window needs before it can
	// count as a multi-line block at all.
	minStructureNewlines = 2

	// structureHapaxFloor is the novelty ratio above which a repeating phrase is
	// treated as part of a progressing list.
	structureHapaxFloor = 0.1

	// minDistinctLineLeads is the complementary signal: a real list starts each
	// line with a different token, a loop repeats the same one.
	minDistinctLineLeads = 3
)

// GuardOptions tunes the incremental repetition guard. The zero value is the
// production default.
type GuardOptions struct {
	// Ngram is how many consecutive words make a tracked phrase. Longer phrases
	// recur by chance less often. Clamped to at least 2.
	Ngram *int

	// Window is how many words of history the guard keeps. Clamped to at least
	// Ngram.
	Window *int

	// MaxRepeats is how many times a phrase must recur within the window before
	// the guard latches. Clamped to at least 2.
	MaxRepeats *int

	// MaxUniqueRatio is the lexical-diversity ceiling. A stuck loop churns the
	// same few words; a progressing list keeps introducing new ones and must not
	// be cut off. Clamped to [0, 1].
	MaxUniqueRatio *float64

	// MinChars is the minimum output length before the guard may trip. Short
	// repetitive output is harmless - it ends on its own - and was the bulk of
	// the false positives this exists to prevent.
	MinChars *int
}

// GuardReason explains a trip.
type GuardReason struct {
	// Phrase is the normalised form that recurred - stable for grouping.
	Phrase string `json:"phrase"`

	// Count is how many times it recurred within the window.
	Count int `json:"count"`

	// Text is the phrase as the model actually emitted it, suitable for showing
	// back to a user or to the model.
	Text string `json:"text"`

	// UniqueRatio and HapaxRatio are the window's diversity and novelty at the
	// trip - the signals that separate a stuck loop (both low) from a wrongly
	// flagged progressing list (both higher). Reported so a stop can be triaged
	// from telemetry rather than re-derived by hand.
	UniqueRatio float64 `json:"uniqueRatio"`
	HapaxRatio  float64 `json:"hapaxRatio"`
}

// Guard is an incremental runaway-repetition detector.
//
// Where HasRepeatedTextRun re-scans a whole block, Guard maintains a rolling
// window of normalised words and a running count of every phrase in it. Each
// pushed chunk costs O(1) amortised, so it can run on every streamed token and
// latch within a few repeats - long before the heavier backstop would react.
type Guard struct {
	ngram          int
	window         int
	maxRepeats     int
	maxUniqueRatio float64
	minChars       int

	words     []string
	originals []string
	counts    map[string]int
	wordCount map[string]int

	newlinesBefore []int
	windowNewlines int

	pending       string
	carryNewlines int
	totalChars    int

	tripped bool
	reason  GuardReason
}

// NewGuard creates a repetition guard.
func NewGuard(options GuardOptions) *Guard {
	ngram := clamp(options.Ngram, 2, 4)

	windowFallback := 48
	if ngram > windowFallback {
		windowFallback = ngram
	}

	window := clamp(options.Window, ngram, windowFallback)

	maxRepeats := clamp(options.MaxRepeats, 2, 4)

	maxUniqueRatio := 0.4

	if options.MaxUniqueRatio != nil {
		maxUniqueRatio = *options.MaxUniqueRatio

		if maxUniqueRatio < 0 {
			maxUniqueRatio = 0
		}

		if maxUniqueRatio > 1 {
			maxUniqueRatio = 1
		}
	}

	minChars := clamp(options.MinChars, 0, 0)

	return &Guard{
		ngram:          ngram,
		window:         window,
		maxRepeats:     maxRepeats,
		maxUniqueRatio: maxUniqueRatio,
		minChars:       minChars,
		counts:         map[string]int{},
		wordCount:      map[string]int{},
	}
}

// hapaxRatio is the fraction of the window seen exactly once - the novelty
// signal separating a progressing list (many distinct keys) from a stuck loop
// (the same few words). Only ever called at a candidate trip.
func (g *Guard) hapaxRatio() float64 {
	hapax := 0

	for _, count := range g.wordCount {
		if count == 1 {
			hapax++
		}
	}

	return float64(hapax) / float64(len(g.words))
}

// distinctLineLeads counts distinct line-leading tokens. A stuck loop repeats
// the same line so has one or two; a progressing enumeration keeps starting
// lines with new keys. This rescues lists whose long shared suffix sinks the
// hapax ratio.
func (g *Guard) distinctLineLeads() int {
	leads := map[string]struct{}{}

	for index, word := range g.words {
		if g.newlinesBefore[index] >= 1 {
			leads[word] = struct{}{}
		}
	}

	return len(leads)
}

func (g *Guard) addWord(word, original string, newlines int) {
	g.words = append(g.words, word)
	g.originals = append(g.originals, original)
	g.newlinesBefore = append(g.newlinesBefore, newlines)

	g.windowNewlines += newlines
	g.wordCount[word]++

	if len(g.words) >= g.ngram {
		gram := strings.Join(g.words[len(g.words)-g.ngram:], " ")

		g.counts[gram]++

		next := g.counts[gram]

		// a phrase recurring often enough is necessary but not sufficient: the
		// surrounding window must also lack diversity

		uniqueRatio := float64(len(g.wordCount)) / float64(len(g.words))

		if g.totalChars >= g.minChars && next >= g.maxRepeats && uniqueRatio <= g.maxUniqueRatio {
			// structural-enumeration exemption: a recurring phrase inside a
			// multi-line block that still introduces novel tokens is a
			// progressing list or table. The cheap newline check short-circuits
			// the hapax scan, so a single-line loop never pays for it.
			enumerated := g.maxUniqueRatio < structureAggressiveGate &&
				g.windowNewlines >= minStructureNewlines &&
				(g.hapaxRatio() >= structureHapaxFloor ||
					g.distinctLineLeads() >= minDistinctLineLeads)

			if !enumerated {
				if !g.tripped {
					g.reason = GuardReason{
						Phrase:      gram,
						Count:       next,
						Text:        strings.Join(g.originals[len(g.originals)-g.ngram:], " "),
						UniqueRatio: uniqueRatio,
						HapaxRatio:  g.hapaxRatio(),
					}
				}

				g.tripped = true
			}
		}
	}

	// evict the oldest word and drop the phrase leaving the window

	if len(g.words) > g.window {
		leaving := strings.Join(g.words[:g.ngram], " ")

		if g.counts[leaving]--; g.counts[leaving] <= 0 {
			delete(g.counts, leaving)
		}

		leavingWord := g.words[0]

		if g.wordCount[leavingWord]--; g.wordCount[leavingWord] <= 0 {
			delete(g.wordCount, leavingWord)
		}

		g.windowNewlines -= g.newlinesBefore[0]

		g.newlinesBefore = g.newlinesBefore[1:]
		g.words = g.words[1:]
		g.originals = g.originals[1:]
	}
}

// normalizeWord strips a token to letters and digits, preserving Unicode.
//
// Keeping non-ASCII letters is deliberate: an ASCII-only strip left CJK text as
// a run of bare digits, which read as a phantom loop.
func normalizeWord(raw string) string {
	var builder strings.Builder

	for _, r := range strings.ToLower(raw) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
		}
	}

	return builder.String()
}

// Push feeds streamed text into the guard and reports whether a runaway has been
// detected. Once tripped it stays tripped.
func (g *Guard) Push(text string) bool {
	if g.tripped {
		return true
	}

	if text == "" {
		return false
	}

	g.totalChars += len(text)
	g.pending += text

	// split keeping the separators, so newlines between words survive to feed
	// the structural-enumeration exemption

	parts := splitKeepingSeparators(g.pending)

	// the final part may be a word still streaming, so hold it back until a
	// whitespace boundary completes it

	if len(parts) > 0 {
		g.pending = parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	} else {
		g.pending = ""
	}

	for index, part := range parts {
		if index%2 == 1 {
			g.carryNewlines += strings.Count(part, "\n")

			continue
		}

		if word := normalizeWord(part); word != "" {
			g.addWord(word, part, g.carryNewlines)

			g.carryNewlines = 0
		}

		if g.tripped {
			return true
		}
	}

	return g.tripped
}

// Reason returns why the guard tripped, or nil while it has not.
func (g *Guard) Reason() *GuardReason {
	if !g.tripped {
		return nil
	}

	reason := g.reason

	if reason.Text == "" {
		reason.Text = reason.Phrase
	}

	return &reason
}

// splitKeepingSeparators splits on whitespace runs, keeping them, so the result
// alternates word, separator, word, separator, ... exactly as the JavaScript
// `split(/(\s+)/)` it mirrors.
func splitKeepingSeparators(text string) []string {
	var (
		parts   []string
		current strings.Builder
		inSpace bool
	)

	for index, r := range text {
		space := unicode.IsSpace(r)

		if index > 0 && space != inSpace {
			parts = append(parts, current.String())

			current.Reset()
		}

		current.WriteRune(r)

		inSpace = space
	}

	if text != "" {
		parts = append(parts, current.String())
	}

	// @note JavaScript's split with a capturing group yields a leading empty
	// string when the input starts with a separator, which keeps the
	// word/separator alternation aligned. Reproduce it.
	if len(parts) > 0 && strings.TrimSpace(parts[0]) == "" && parts[0] != "" {
		parts = append([]string{""}, parts...)
	}

	return parts
}
