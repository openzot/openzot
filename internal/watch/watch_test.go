package watch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openzot/openzot/internal/order"
)

// recorder is a Dispatcher that remembers what it was handed.
type recorder struct {
	mu     sync.Mutex
	orders []order.Order
}

func (r *recorder) Dispatch(o order.Order) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.orders = append(r.orders, o)
}

func (r *recorder) dispatched() []order.Order {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]order.Order(nil), r.orders...)
}

// collected waits until want orders have been dispatched or the deadline
// passes - a watcher is asynchronous by design, so a test waits on behaviour
// rather than sleeping on hope.
func collected(t *testing.T, r *recorder, want int) []order.Order {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if got := r.dispatched(); len(got) >= want {
			return got
		}

		time.Sleep(2 * time.Millisecond)
	}

	t.Fatalf("only %d of %d expected orders were dispatched: %+v",
		len(r.dispatched()), want, objectives(r.dispatched()))

	return nil
}

// writeOrder drops an order file into dir and returns its path.
func writeOrder(t *testing.T, dir, name, objective string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	if err := os.WriteFile(path, []byte("objective: "+objective+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return path
}

// mustWrite writes a file of any kind, creating parent directories.
func mustWrite(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func objectives(orders []order.Order) []string {
	var out []string

	for _, o := range orders {
		out = append(out, o.Objective)
	}

	return out
}

// syncBuffer is an output the watcher can write to from its own goroutine
// while the test reads it - strings.Builder alone would race.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// The two forms a watch target takes - a plain directory and a glob over one -
// must both yield exactly the order files: every *.yaml directly inside it, and
// nothing that is not an order, however much else shares the folder.
func TestSweepDispatchesEveryOrderInTheTarget(t *testing.T) {
	tests := []struct {
		name   string
		target func(dir string) string
		want   []string // objectives, in dispatch (filename) order
	}{
		{
			name:   "a plain directory watches every yaml in it",
			target: func(dir string) string { return dir },
			want:   []string{"first", "second"},
		},
		{
			name:   "a glob watches the files it names",
			target: func(dir string) string { return filepath.Join(dir, "*.yaml") },
			want:   []string{"first", "second"},
		},
		{
			name:   "a glob picks orders out of whatever else it matches",
			target: func(dir string) string { return filepath.Join(dir, "*") },
			want:   []string{"first", "second"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()

			writeOrder(t, dir, "a-first.yaml", "first")
			writeOrder(t, dir, "b-second.yaml", "second")

			// everything a watch must ignore: not an order file, an order
			// nested below the top level, and a directory named like one
			mustWrite(t, filepath.Join(dir, "notes.txt"), "# not an order\n")
			mustWrite(t, filepath.Join(dir, "nested", "c-third.yaml"), "objective: third\n")
			mustWrite(t, filepath.Join(dir, "d-folder.yaml", "keep"), "a directory, not a file\n")

			rec := &recorder{}

			w, err := New(test.target(dir), rec, WithOutput(io.Discard))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			w.sweep()

			got := rec.dispatched()
			if len(got) != len(test.want) {
				t.Fatalf("swept %d orders (%v), want %d: %v",
					len(got), objectives(got), len(test.want), test.want)
			}

			for i, objective := range test.want {
				if got[i].Objective != objective {
					t.Errorf("order %d = %q, want %q", i, got[i].Objective, objective)
				}
			}
		})
	}
}

// A new order written under a running watcher is dispatched without zot being
// restarted - that is watch mode's whole point, so it is the live loop that has
// to prove it, not just one sweep.
func TestRunDispatchesAnOrderAddedWhileWatching(t *testing.T) {
	dir := t.TempDir()

	rec := &recorder{}

	ctx, cancel := context.WithCancel(context.Background())

	w, err := New(dir, rec, WithInterval(time.Millisecond), WithOutput(io.Discard))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})

	go func() {
		w.Run(ctx)
		close(done)
	}()

	// written after the watch started: nothing but the sweeps can deliver it
	writeOrder(t, dir, "late-arrival.yaml", "arrived later")

	got := collected(t, rec, 1)

	if got[0].Objective != "arrived later" {
		t.Errorf("dispatched objective = %q", got[0].Objective)
	}

	if !strings.HasSuffix(got[0].Path, "late-arrival.yaml") {
		t.Errorf("dispatched path = %q", got[0].Path)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not stop when its context was cancelled")
	}
}

// An order already in the target when the watch starts runs too - pointing a
// watch at a folder is also how work left unfinished overnight gets picked back
// up, without a separate batch invocation first.
func TestRunDispatchesOrdersAlreadyPresentAtStartup(t *testing.T) {
	dir := t.TempDir()

	writeOrder(t, dir, "waiting.yaml", "was here first")

	rec := &recorder{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(dir, rec, WithInterval(time.Millisecond), WithOutput(io.Discard))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	go w.Run(ctx)

	got := collected(t, rec, 1)

	if got[0].Objective != "was here first" {
		t.Errorf("dispatched objective = %q, want the order that predated the watch", got[0].Objective)
	}
}

// An order is recognised by its content: a file rewritten with new work is a
// new order (editing the order re-queues it, as everywhere in zot), while an
// identical file - rewritten in place or copied to another name - is not run a
// second time.
func TestAnOrderIsRecognisedByItsContent(t *testing.T) {
	dir := t.TempDir()

	path := writeOrder(t, dir, "work.yaml", "the original")
	twin := writeOrder(t, dir, "copy.yaml", "the original")

	rec := &recorder{}

	w, err := New(dir, rec, WithInterval(time.Millisecond), WithOutput(io.Discard))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Run(ctx)

	collected(t, rec, 1)

	// both files carry the same content; only one of them may ever dispatch
	mustWrite(t, twin, "objective: the original\n")

	// editing the original is new work and runs again
	mustWrite(t, path, "objective: the sequel\n")

	got := collected(t, rec, 2)

	if got[1].Objective != "the sequel" {
		t.Fatalf("the edited order was not re-dispatched: %v", objectives(got))
	}

	time.Sleep(20 * time.Millisecond)

	if n := len(rec.dispatched()); n != 2 {
		t.Errorf("identical content dispatched again: %d orders (%v), want exactly the two distinct ones",
			n, objectives(got))
	}
}

// A file that does not parse yet - half written by an editor, say - costs one
// message and one message only, and starts working the moment its content
// becomes a valid order. Silence would leave an operator wondering why their
// order never ran; repeating the complaint every sweep would flood the log.
func TestAnUnparseableFileIsReportedOnceThenPickedUpWhenFixed(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "broken.yaml")

	mustWrite(t, path, "objectiv: typo\n")

	var out syncBuffer

	rec := &recorder{}

	w, err := New(dir, rec, WithInterval(time.Millisecond), WithOutput(&out))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Run(ctx)

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) && !strings.Contains(out.String(), "broken.yaml") {
		time.Sleep(2 * time.Millisecond)
	}

	if rec.dispatched() != nil {
		t.Errorf("a broken order was dispatched anyway: %+v", rec.dispatched())
	}

	// several sweeps pass before the fix; the complaint must not repeat
	time.Sleep(5 * w.interval)

	if n := strings.Count(out.String(), "broken.yaml"); n != 1 {
		t.Errorf("the broken order was reported %d times, want once:\n%s", n, out.String())
	}

	mustWrite(t, path, "objective: fixed now\n")

	if got := collected(t, rec, 1); got[0].Objective != "fixed now" {
		t.Errorf("dispatched objective = %q after the fix", got[0].Objective)
	}
}

// The target does not have to exist when the watch starts - `zot --watch
// orders` before anyone creates orders/ is a reasonable way to work - so a
// missing folder keeps watching and picks the folder up when it appears.
func TestAMissingTargetIsKeptUnderWatch(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, "orders")

	rec := &recorder{}

	w, err := New(dir, rec, WithInterval(time.Millisecond), WithOutput(io.Discard))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go w.Run(ctx)

	time.Sleep(5 * w.interval)

	if got := rec.dispatched(); len(got) != 0 {
		t.Fatalf("a missing directory produced orders: %v", objectives(got))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeOrder(t, dir, "late-dir.yaml", "from a folder created later")

	if got := collected(t, rec, 1); got[0].Objective != "from a folder created later" {
		t.Errorf("dispatched objective = %q", got[0].Objective)
	}
}

// A watch that cannot read its target says so once rather than once per sweep -
// a typo in the target should be diagnosable without drowning in repeats.
func TestARepeatedProblemIsReportedOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nowhere")

	var out syncBuffer

	w, err := New(dir, &recorder{}, WithInterval(time.Millisecond), WithOutput(&out))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := 0; i < 3; i++ {
		w.sweep()
	}

	reports := 0

	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "nowhere") {
			reports++
		}
	}

	if reports != 1 {
		t.Errorf("the same problem was reported %d times, want once:\n%s", reports, out.String())
	}
}

func TestNewRejectsAnEmptyTarget(t *testing.T) {
	if _, err := New("   ", &recorder{}); err == nil {
		t.Error("an empty target must be refused")
	}

	if _, err := New("orders/*.yaml", nil); err == nil {
		t.Error("a watcher with no dispatcher must be refused")
	}
}

// Status lines go wherever WithOutput points them; this pins the announce line,
// which is how whoever started the watch can tell it is actually watching.
func TestRunAnnouncesTheTarget(t *testing.T) {
	var out syncBuffer

	dir := t.TempDir()

	w, err := New(filepath.Join(dir, "*.yaml"), &recorder{}, WithOutput(&out))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})

	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()

	<-done

	if !strings.Contains(out.String(), w.target) || !strings.Contains(out.String(), "watching") {
		t.Errorf("the announce line should name the target:\n%s", out.String())
	}
}
