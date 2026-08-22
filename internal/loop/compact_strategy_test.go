package loop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openzot/openzot/internal/provider"
)

// countingClient is a provider that returns a fixed summary and counts how many
// times it was called, so a test can prove the compact strategy did (or did not)
// make a model call.
func countingClient(t *testing.T, reply string) (*provider.Client, *int) {
	t.Helper()

	calls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", text(reply))
		fmt.Fprintf(w, "data: %s\n\n", stop())
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	return client, &calls
}

// erroringClient is a provider whose every call fails, for exercising the
// structural fallback when the summariser is unavailable.
func erroringClient(t *testing.T) *provider.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	return client
}

// a long-enough conversation to cross a low trigger ratio of the window and the
// message floor. The system prompt is never summarised, so it must survive.
func seededConversation() []Message {
	messages := []Message{{Type: TypeInstructions, Text: "you are a coding agent"}}

	for i := 0; i < 16; i++ {
		messages = append(messages,
			Message{Type: TypeUser, Text: fmt.Sprintf("please look into task number %d and report back in detail", i)},
			Message{Type: TypeBot, Text: fmt.Sprintf("I examined task %d and here is a fairly long explanation of what I found", i)},
		)
	}

	return messages
}

func compactEngine(t *testing.T, client *provider.Client, tweak func(*Options)) *Engine {
	t.Helper()

	options := Options{
		Client:  client,
		Compact: true,
		// the window is pinned rather than taken from the catalogue default, so
		// the numeric expectations below survive the default changing: input
		// budget = 128k - 32k = 96k
		ContextWindow: 128_000,
		// tiny ratio + floors so a modest seeded conversation crosses the trigger;
		// with the 96k budget, 0.001 puts the threshold near 96 tokens.
		CompactTriggerRatio: 0.001,
		CompactMinTokens:    1,
		CompactMinMessages:  2,
	}

	if tweak != nil {
		tweak(&options)
	}

	engine, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return engine
}

func hasText(messages []Message, needle string) bool {
	for _, message := range messages {
		if strings.Contains(message.Text, needle) {
			return true
		}
	}

	return false
}

// The compact strategy summarises older history into a checkpoint with a model
// call, shrinking the conversation while keeping the system prompt and the
// recent tail.
func TestMaybeCompactSummarisesWithTheModel(t *testing.T) {
	client, calls := countingClient(t, "CONDENSED SUMMARY OF THE EARLY TURNS")

	engine := compactEngine(t, client, nil)

	before := seededConversation()

	after, compacted := engine.maybeCompact(context.Background(), before, 0, func(Event) {})

	if !compacted {
		t.Error("a compaction that shrank the conversation must report that it ran, so the caller can drop the usage reading that triggered it")
	}

	if *calls != 1 {
		t.Fatalf("the compact strategy must make exactly one summary call, made %d", *calls)
	}

	if len(after) >= len(before) {
		t.Errorf("compaction must shrink the conversation: before %d, after %d", len(before), len(after))
	}

	if !hasText(after, "CONDENSED SUMMARY OF THE EARLY TURNS") {
		t.Error("the model's summary must appear in the compacted conversation")
	}

	// the system prompt survives, and the most recent turn is kept verbatim
	if !hasText(after, "you are a coding agent") {
		t.Error("the system prompt must never be summarised away")
	}

	if last := before[len(before)-1].Text; !hasText(after, last) {
		t.Errorf("the most recent turn must be preserved verbatim: %q", last)
	}
}

// The truncate strategy never summarises: maybeCompact is an untouched pass-through
// and makes no model call.
func TestMaybeCompactIsANoOpUnderTruncate(t *testing.T) {
	client, calls := countingClient(t, "should never be produced")

	engine := compactEngine(t, client, func(o *Options) { o.Compact = false })

	before := seededConversation()

	after, compacted := engine.maybeCompact(context.Background(), before, 0, func(Event) {})

	if compacted {
		t.Error("the truncate strategy never compacts, so it must not report that it did")
	}

	if *calls != 0 {
		t.Errorf("the truncate strategy must not call the model, made %d calls", *calls)
	}

	if !reflect.DeepEqual(before, after) {
		t.Error("truncate must leave the conversation exactly as it was")
	}
}

// A summariser outage degrades to the no-model structural summary rather than
// stalling the run - the conversation is still compacted.
func TestMaybeCompactFallsBackToStructuralSummary(t *testing.T) {
	engine := compactEngine(t, erroringClient(t), nil)

	before := seededConversation()

	after, _ := engine.maybeCompact(context.Background(), before, 0, func(Event) {})

	if len(after) >= len(before) {
		t.Error("compaction must still shrink the conversation when the summariser fails")
	}

	// the structural summary carries this fixed preamble
	if !hasText(after, "Earlier turns, condensed") {
		t.Error("a failed model call must fall back to the structural summary")
	}
}

// Below the floors the compact strategy leaves the conversation whole, even over
// the trigger ratio - summarising a short thread costs more than carrying it.
func TestMaybeCompactRespectsFloors(t *testing.T) {
	client, calls := countingClient(t, "unused")

	// message floor far above the seeded conversation
	engine := compactEngine(t, client, func(o *Options) { o.CompactMinMessages = 1000 })

	before := seededConversation()

	after, _ := engine.maybeCompact(context.Background(), before, 0, func(Event) {})

	if *calls != 0 || !reflect.DeepEqual(before, after) {
		t.Error("under the message floor, compaction must not run")
	}

	// token floor above what the seeded conversation can reach
	engine = compactEngine(t, client, func(o *Options) { o.CompactMinTokens = 10_000_000 })

	if after, _ := engine.maybeCompact(context.Background(), before, 0, func(Event) {}); !reflect.DeepEqual(before, after) {
		t.Error("under the token floor, compaction must not run")
	}
}

// The trigger keys on the provider's real prompt-token usage, not the local
// estimate - the estimate undercounts (it misses the system prompt, tool schemas
// and wire overhead), so triggering on it would compact too late. The same
// conversation stays whole under its estimate but compacts once real usage
// reported by the provider crosses the threshold.
func TestMaybeCompactTriggersOnRealUsageNotEstimate(t *testing.T) {
	client, calls := countingClient(t, "SUMMARY OF EARLY TURNS")

	// threshold = 0.5 * 96k window = 48000, well above this small conversation's
	// own token estimate
	engine := compactEngine(t, client, func(o *Options) { o.CompactTriggerRatio = 0.5 })

	before := seededConversation()

	// no real usage yet: the estimate is far below the threshold, so nothing happens
	if after, _ := engine.maybeCompact(context.Background(), before, 0, func(Event) {}); !reflect.DeepEqual(before, after) {
		t.Fatal("under the estimate the conversation must be left whole")
	}

	if *calls != 0 {
		t.Fatalf("no summary call expected while under threshold, made %d", *calls)
	}

	// the provider reports 60k prompt tokens - over the 48k threshold - so it compacts
	after, _ := engine.maybeCompact(context.Background(), before, 60_000, func(Event) {})

	if len(after) >= len(before) {
		t.Error("real usage over the threshold must trigger compaction")
	}

	if *calls != 1 {
		t.Errorf("real-usage trigger must make exactly one summary call, made %d", *calls)
	}
}

// A compaction has to clear the usage reading that triggered it. It did not,
// and an error reports no usage of its own - so a turn that failed right after
// a compaction left the stale pre-compaction count in place, and the retry
// compacted the already-compacted thread again. During a provider outage that
// repeats per retry, condensing the tail over and over and spending a
// summariser call each time, until the run's history is summaries of summaries.
func TestCompactionDoesNotRepeatAfterAFailedTurn(t *testing.T) {
	var (
		mu        sync.Mutex
		turns     int
		summaries int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		defer mu.Unlock()

		if strings.Contains(string(body), "You are summarising a conversation") {
			summaries++

			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", text("CONDENSED EARLY TURNS"))
			fmt.Fprintf(w, "data: %s\n\n", stop())
			fmt.Fprint(w, "data: [DONE]\n\n")

			return
		}

		turns++

		switch turns {
		case 1:
			// a turn whose reported usage crosses the compaction trigger, so the
			// next iteration compacts
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", text("still working"))
			fmt.Fprintf(w, "data: %s\n\n", usageFrame(60_000, 10))
			fmt.Fprint(w, "data: [DONE]\n\n")

		case 2:
			// a transient provider failure: retriable, and it carries no usage of
			// its own, so whatever reading the loop is holding survives it
			w.WriteHeader(http.StatusInternalServerError)

		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", tool("c1", SuccessTool, `{"summary":"done"}`))
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	// threshold = 0.1 * 96k window = 9600: above this conversation's own estimate
	// and above the compacted thread's, but well below the 60k the first turn
	// reports - so exactly one compaction is warranted.
	result := run(t, Options{
		Client:              client,
		Messages:            seededConversation(),
		Compact:             true,
		ContextWindow:       128_000, // pin the budget the numbers above assume
		CompactTriggerRatio: 0.1,
		CompactMinTokens:    1,
		CompactMinMessages:  1,
		MaxIterations:       10,
		MaxSettles:          5,
	})

	mu.Lock()
	defer mu.Unlock()

	if result.Budget.Recoveries != 1 {
		t.Fatalf("continuations = %d, want 1 - the retriable failure must have been retried", result.Budget.Recoveries)
	}

	if summaries != 1 {
		t.Errorf("summariser calls = %d, want 1 - the retry re-compacted an already-compacted thread", summaries)
	}
}

// scriptedClient returns a different reply per call and records each request
// body, so a test can both vary the summaries and inspect what was sent to the
// summariser.
func scriptedClient(t *testing.T, replies ...string) (*provider.Client, *[]string) {
	t.Helper()

	var bodies []string

	call := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))

		reply := replies[len(replies)-1]
		if call < len(replies) {
			reply = replies[call]
		}
		call++

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", text(reply))
		fmt.Fprintf(w, "data: %s\n\n", stop())
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	t.Cleanup(server.Close)

	client, err := provider.New(provider.Config{
		Provider: provider.Custom,
		Model:    "test-model",
		APIKey:   "k",
		BaseURL:  server.URL,
	})
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}

	return client, &bodies
}

func messagesOfType(messages []Message, kind MessageType) []Message {
	var out []Message

	for _, message := range messages {
		if message.Type == kind {
			out = append(out, message)
		}
	}

	return out
}

func turns(n int) []Message {
	var messages []Message

	for i := 0; i < n; i++ {
		messages = append(messages,
			Message{Type: TypeUser, Text: fmt.Sprintf("follow-up question %d with enough words to matter", i)},
			Message{Type: TypeBot, Text: fmt.Sprintf("answer %d, also with a reasonable amount of detail to count", i)},
		)
	}

	return messages
}

// The summary lands as a checkpoint, which renders to the provider as system
// context - ahead of the conversation, like the instructions.
func TestCheckpointRendersAsSystemContext(t *testing.T) {
	chat := toChatMessages([]Message{{Type: TypeCheckpoint, Text: "condensed history"}})

	if len(chat) != 1 || chat[0].Role != provider.RoleSystem || chat[0].Content != "condensed history" {
		t.Errorf("a checkpoint must render as a system message, got %+v", chat)
	}
}

// The whole point of the checkpoint design: an earlier summary is preserved
// verbatim and is never fed back into the summariser, so repeated compactions
// build a chain of checkpoints rather than a lossy summary-of-a-summary.
func TestCheckpointsArePreservedNotReSummarised(t *testing.T) {
	client, bodies := scriptedClient(t, "SUMMARY-ONE", "SUMMARY-TWO")

	engine := compactEngine(t, client, nil)

	// first compaction: one checkpoint holding the first summary
	first, _ := engine.maybeCompact(context.Background(), seededConversation(), 0, func(Event) {})

	if cps := messagesOfType(first, TypeCheckpoint); len(cps) != 1 || !strings.Contains(cps[0].Text, "SUMMARY-ONE") {
		t.Fatalf("first compaction must produce exactly one checkpoint holding SUMMARY-ONE, got %+v", cps)
	}

	if first[0].Type != TypeInstructions {
		t.Errorf("the system prompt must stay first, got %q", first[0].Type)
	}

	// grow the conversation and compact again
	second := append(append([]Message(nil), first...), turns(16)...)
	second, _ = engine.maybeCompact(context.Background(), second, 0, func(Event) {})

	// two checkpoints now, and the first survives verbatim
	if cps := messagesOfType(second, TypeCheckpoint); len(cps) != 2 {
		t.Fatalf("second compaction must add a checkpoint and keep the first, got %d", len(cps))
	}

	if !hasText(second, "SUMMARY-ONE") || !hasText(second, "SUMMARY-TWO") {
		t.Error("both checkpoints must be present after the second compaction")
	}

	// the crux: the second summariser call was NOT given the first checkpoint
	if len(*bodies) != 2 {
		t.Fatalf("expected exactly two summariser calls, got %d", len(*bodies))
	}

	if strings.Contains((*bodies)[1], "SUMMARY-ONE") {
		t.Error("the earlier checkpoint was fed back into the summariser - the summary-of-a-summary this design exists to prevent")
	}
}

// The provider-reported prompt count measures the previous request AFTER the
// thread builder trimmed it, so on its own it can sit below the trigger
// forever while the trimmer silently drops ever more history - the model then
// re-reads what it lost, and the run burns its budget on amnesia (a live run
// re-read the same file sections three times over). The estimate of the whole
// conversation must fire compaction even while reported usage looks small.
func TestCompactionFiresOnTheEstimateWhenTrimmingHidesUsage(t *testing.T) {
	engine, err := New(Options{
		// the summariser call answers with a checkpoint summary
		Client:        stub(t, []string{text("a summary of the early work"), stop()}),
		Compact:       true,
		ContextWindow: MinInputTokens * 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	// a conversation whose estimate is far over the trigger
	messages := []Message{{Type: TypeUser, Text: "the kickoff"}}

	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("c%d", i)
		messages = append(messages,
			Message{Type: TypeActivity, Activity: &Activity{Kind: ActivityRequest, ID: id, Name: "read", Arguments: `{"path":"x"}`}},
			Message{Type: TypeActivity, Activity: &Activity{Kind: ActivityResponse, ID: id, Name: "read", Result: strings.Repeat("file content ", 300)}},
		)
	}

	// the provider reported a small trimmed request - the deadlock signal
	compacted, ok := engine.maybeCompact(context.Background(), messages, MinInputTokens, func(Event) {})

	if !ok {
		t.Fatal("compaction must fire on the whole-conversation estimate, not wait for reported usage that trimming keeps low")
	}

	if len(compacted) >= len(messages) {
		t.Error("compaction fired but did not shrink the conversation")
	}
}
