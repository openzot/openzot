package order

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/openzot/openzot/internal/session"
)

// Record is one completed run of an order, kept in the book's ledger.
//
// Orders are never deleted or marked up when they finish - the file is the
// contract, and mutating it to track state would destroy the thing being
// tracked. Doneness is derived instead: an order is satisfied when the ledger
// holds a record of a successful run of this exact content. Editing the order
// changes its hash, so an edited order stops being satisfied and runs again -
// re-queueing is a diff, exactly like everything else in the book.
type Record struct {
	// Run identifies the run - the session id, which names its full log.
	Run string `yaml:"run"`

	// Reason is the run's stop reason. Only a settled run satisfies an order.
	Reason string `yaml:"reason"`

	// At is when the run concluded.
	At time.Time `yaml:"at"`

	// OrderSHA256 is the hash of the order file the run executed. A record
	// only counts toward the order content it actually ran.
	OrderSHA256 string `yaml:"order_sha256"`

	// Evidence is what the run actually did, read back from its session log.
	Evidence Evidence `yaml:"evidence"`
}

// Evidence is the proof half of a receipt: enough of what the run did that
// someone can judge the claim without opening the .jsonl session log.
//
// A record that says only "settled" asks to be trusted. The run already wrote
// everything needed to check it - its own closing summary, how many rounds and
// tool calls it took - so a receipt that omits them is throwing away proof it
// was handed. None of it is computed here: every field is copied from what the
// run recorded, which is what keeps a receipt evidence rather than a claim.
type Evidence struct {
	// Session is the id of the session log this was read from - the pointer to
	// the full record, for when the summary is not enough.
	Session string `yaml:"session,omitempty"`

	// Reason is how the run itself said it ended, which is not the same claim
	// as the ledger's: the ledger records that the run returned without error,
	// the engine records what it decided. They should agree, and a receipt is
	// worth little if it cannot show that they did.
	Reason string `yaml:"reason,omitempty"`

	// Summary is the run's own closing words - what it says it accomplished.
	Summary string `yaml:"summary,omitempty"`

	// Iterations and Calls are the shape of the work: an order settled in two
	// rounds and one settled in ninety are different stories, and the
	// difference is usually the interesting part of a receipt.
	Iterations int `yaml:"iterations,omitempty"`
	Calls      int `yaml:"calls,omitempty"`

	// InputTokens and OutputTokens are the run's billed totals, so a receipt
	// carries what the order cost as well as what it did. Copied from the
	// session log's Result; zero (and omitted) for a run whose log predates the
	// fields or recorded no outcome.
	InputTokens  int `yaml:"input_tokens,omitempty"`
	OutputTokens int `yaml:"output_tokens,omitempty"`

	// Missing says why there is no proof, when there is none. A receipt with
	// nothing to show has to say so: silence reads identically to a run that
	// did nothing, and a ledger that quietly implies work it cannot evidence is
	// worse than one that admits the gap.
	Missing string `yaml:"missing,omitempty"`
}

// Proven reports whether this evidence shows anything.
func (e Evidence) Proven() bool { return e.Missing == "" && e.Session != "" }

// EvidenceFrom reads a run's proof out of the session log it already wrote.
// Nothing is invented: a session that is absent, unreadable, or ended without
// recording a result yields evidence that says exactly that.
func EvidenceFrom(runID string, s *session.Session, err error) Evidence {
	switch {
	case err != nil:
		return Evidence{Session: runID, Missing: "the session log could not be read back: " + err.Error()}

	case s == nil:
		return Evidence{Missing: "this run was not recorded to a session log, so nothing here proves what it did"}

	case s.Result == nil:
		missing := "the run recorded no outcome in its session log"
		if s.Truncated {
			missing = "the session log ends mid-record, so the run left no outcome to read"
		}

		return Evidence{Session: runID, Missing: missing}
	}

	return Evidence{
		Session:      runID,
		Reason:       s.Result.Reason,
		Summary:      strings.TrimSpace(s.Result.Message),
		Iterations:   s.Result.Iterations,
		Calls:        s.Result.Calls,
		InputTokens:  s.Result.InputTokens,
		OutputTokens: s.Result.OutputTokens,
	}
}

// Ledger is where finished runs are recorded: one directory per order slug
// under Root, one append-only file per run inside it.
//
// The root is the caller's to choose, because an order is advisory input that
// may live anywhere - in the repository being worked on, in a shared folder of
// briefs, in a temp directory a dispatcher wrote it to - while the ledger is
// the operator's own record of what their factory has done. Deriving one from
// the other tied the two together: running an order from somewhere else wrote
// the receipt somewhere else too, and an order run from a read-only or
// throwaway location had nowhere to record at all.
type Ledger struct {
	// Root is the directory the per-order record folders live under. Empty
	// disables the ledger: nothing is recorded, and nothing is ever satisfied,
	// which is what an embedding caller that keeps its own history wants.
	Root string
}

// dir is where this order's records live: one folder per order slug, named for
// the order file rather than its content, so a run's receipts stay together
// across the edits that re-queue it.
func (l Ledger) dir(orderPath string) string {
	slug := strings.TrimSuffix(filepath.Base(orderPath), filepath.Ext(orderPath))

	return filepath.Join(l.Root, slug)
}

// hashFile hashes the order's current content.
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

// Record appends a run's outcome, and the evidence for it, to the ledger. One
// append-only file per run, so concurrent runs cannot conflict and history
// accumulates.
func (l Ledger) Record(o Order, runID, reason string, at time.Time, proof Evidence) error {
	if l.Root == "" {
		return fmt.Errorf("record: no records directory configured")
	}

	if o.Path == "" {
		return fmt.Errorf("record: the order has no path")
	}

	hash, err := hashFile(o.Path)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	dir := l.dir(o.Path)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	data, err := yaml.Marshal(Record{
		Run:         runID,
		Reason:      reason,
		At:          at.UTC(),
		OrderSHA256: hash,
		Evidence:    proof,
	})
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, runID+".yaml"), data, 0o644)
}

// Satisfied reports whether the ledger holds a successful run of this exact
// order content, returning the newest such record.
func (l Ledger) Satisfied(o Order) (Record, bool) {
	if l.Root == "" || o.Path == "" {
		return Record{}, false
	}

	hash, err := hashFile(o.Path)
	if err != nil {
		return Record{}, false
	}

	dir := l.dir(o.Path)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return Record{}, false
	}

	var newest Record

	var found bool

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		var record Record

		if yaml.Unmarshal(data, &record) != nil {
			continue
		}

		if record.Reason != "settled" || record.OrderSHA256 != hash {
			continue
		}

		if !found || record.At.After(newest.At) {
			newest = record
			found = true
		}
	}

	return newest, found
}

// SortRecords returns records ordered newest-first by At, for listing. The
// ledger on disk is already append-only; this only orders what was read back.
func SortRecords(records []Record) []Record {
	out := make([]Record, 0, len(records))
	for _, r := range records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}
