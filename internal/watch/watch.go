// Package watch keeps a running zot pointed at a folder - or at a glob naming
// the orders in one - so work orders are dispatched as they arrive. It is watch
// mode's engine: the CLI hands it a dispatcher that runs each order exactly as
// a batch position would, and it sweeps the target forever, so an order dropped
// into the folder at three in the morning starts without anyone restarting zot.
//
// Sweeping is deliberate polling rather than filesystem events: it is
// cross-platform by construction, immune to the missed-event races of rename
// storms, and cheap at the scale a folder of small YAML files lives at.
package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openzot/openzot/internal/order"
)

// DefaultInterval is how often a watched target is swept when WithInterval does
// not say otherwise.
const DefaultInterval = time.Second

// Dispatcher receives every order the watcher picks up. It owns the order's
// fate - run it, skip it as satisfied, report a failed run - and its errors are
// its own to report: the watcher keeps watching whatever Dispatch does, because
// one bad order must not end a factory that is meant to run unattended. The
// watcher stops only when its context is cancelled.
type Dispatcher interface {
	Dispatch(order.Order)
}

// Watcher sweeps a target for work orders and dispatches each new one.
type Watcher struct {
	target   string
	interval time.Duration
	out      io.Writer
	dispatch Dispatcher

	// handled holds the SHA-256 of every order content already dispatched, so a
	// file is recognised by what it says rather than where it sits: an edited
	// order re-queues (editing the order is the diff, as everywhere in zot),
	// while a rewritten identical file and a copy elsewhere do not re-run.
	handled map[string]bool

	// reported holds the hashes whose parse failure has already been complained
	// about, so half-written or invalid YAML costs one message, not one per
	// sweep for the life of the watch.
	reported map[string]bool

	// lastProblem deduplicates sweep-level trouble - a target directory that
	// does not exist yet, say - which would otherwise repeat every interval.
	lastProblem string
}

// Option adjusts a Watcher.
type Option func(*Watcher)

// WithInterval sets how often the target is swept.
func WithInterval(d time.Duration) Option {
	return func(w *Watcher) { w.interval = d }
}

// WithOutput sets where status lines go. Status belongs on stderr - stdout is
// the transcript of whatever runs - but a test wants to read them, so the
// default of io.Discard is only right for callers with nothing to hear.
func WithOutput(out io.Writer) Option {
	return func(w *Watcher) { w.out = out }
}

// New targets a watch at target - either a plain directory, whose directly
// contained *.yaml files are all candidates, or a glob pattern such as
// orders/*.yaml, whose matches are filtered down to *.yaml files. The target is
// used exactly as given, so a caller resolving against the invoking directory
// (as the CLI does before its chdir) stays in charge of what it means.
func New(target string, d Dispatcher, opts ...Option) (*Watcher, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("watch: no target given (--watch orders or --watch \"orders/*.yaml\")")
	}

	if d == nil {
		return nil, fmt.Errorf("watch: no dispatcher for %s", target)
	}

	w := &Watcher{
		target:   target,
		interval: DefaultInterval,
		out:      io.Discard,
		dispatch: d,
		handled:  map[string]bool{},
		reported: map[string]bool{},
	}

	for _, opt := range opts {
		opt(w)
	}

	return w, nil
}

// Run sweeps until ctx is cancelled: once immediately - so orders already
// sitting in the target start without waiting an interval - then once per
// interval. One order runs at a time; Dispatch is called sequentially, so a
// long run simply delays the next sweep rather than racing it.
func (w *Watcher) Run(ctx context.Context) {
	fmt.Fprintf(w.out, "zot: watching %s - *.yaml orders run as they arrive, one at a time; Ctrl-C stops\n", w.target)

	for ctx.Err() == nil {
		w.sweep()

		select {
		case <-ctx.Done():
			return
		case <-time.After(w.interval):
		}
	}
}

// sweep reads the target once and dispatches every order it has not seen
// before. Nothing here is fatal: a target that cannot be read yet is reported
// once and retried next sweep - pointing a watch at a folder that does not
// exist until later is a legitimate way to use it.
func (w *Watcher) sweep() {
	paths, err := w.candidates()
	if err != nil {
		if problem := err.Error(); problem != w.lastProblem {
			w.lastProblem = problem
			fmt.Fprintf(w.out, "zot: watch: %v (keeping watch)\n", err)
		}

		return
	}

	w.lastProblem = ""

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue // vanished between listing and reading - gone either way
		}

		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])

		if w.handled[hash] {
			continue
		}

		o, err := order.Parse(data)
		if err != nil {
			if !w.reported[hash] {
				w.reported[hash] = true
				fmt.Fprintf(w.out, "zot: watch: skipping %s: %v\n", path, err)
			}

			continue
		}

		o.Path = path

		// Marked before dispatching: an order gets one attempt per content, so
		// a failed run is reported by the dispatcher but not retried into a
		// loop - the operator edits the order to try again.
		w.handled[hash] = true

		w.dispatch.Dispatch(o)
	}
}

// candidates lists the files the target currently names, sorted so a sweep
// picks up several new orders deterministically.
func (w *Watcher) candidates() ([]string, error) {
	if !hasMeta(w.target) {
		entries, err := os.ReadDir(w.target)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", w.target, err)
		}

		var paths []string

		for _, entry := range entries {
			// A plain directory watches only its own top level, matching the
			// shell glob `zot orders/*.yaml` was named after.
			if entry.IsDir() || !isOrderName(entry.Name()) {
				continue
			}

			paths = append(paths, filepath.Join(w.target, entry.Name()))
		}

		sort.Strings(paths)

		return paths, nil
	}

	matches, err := filepath.Glob(w.target)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %s: %w", w.target, err)
	}

	var paths []string

	for _, match := range matches {
		if info, statErr := os.Stat(match); statErr != nil || info.IsDir() {
			continue
		}

		// A looser glob (`orders/*`) still yields only orders: the filter is
		// the same for both forms, so a stray notes.txt never becomes a run.
		if !isOrderName(match) {
			continue
		}

		paths = append(paths, match)
	}

	sort.Strings(paths)

	return paths, nil
}

// hasMeta reports whether a target is a glob pattern rather than a literal
// path.
func hasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// isOrderName reports whether a file name names a work order.
func isOrderName(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".yaml")
}
