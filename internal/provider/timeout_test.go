package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A long turn is not a hung one. The whole-exchange http.Client.Timeout bounded
// the body read as well as the connect, so a stream that was still emitting
// tokens got killed at the cap with "Client.Timeout exceeded while reading
// body" - a message no retry pattern matches, so the run stopped outright. A
// reasoning model can legitimately stream for longer than any wall-clock cap, so
// only silence may be bounded.
func TestASlowButProgressingStreamIsNotCutOff(t *testing.T) {
	withStallTimeout(t, 300*time.Millisecond)

	client := serveTransport(t, false, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		flusher, _ := w.(http.Flusher)

		// well past the cap in total, but never quiet for a third of it
		for i := 0; i < 20; i++ {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")

			if flusher != nil {
				flusher.Flush()
			}

			time.Sleep(50 * time.Millisecond)
		}

		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	text, finish, err := collect(client)
	if err != nil {
		t.Fatalf("a stream that kept producing was cut off: %v", err)
	}

	if text != strings.Repeat("x", 20) {
		t.Errorf("text = %q, want all 20 tokens", text)
	}

	if finish != FinishStop {
		t.Errorf("finish = %q, want stop", finish)
	}
}

// What is actually pathological is a stream that goes quiet and stays quiet:
// without a bound the turn hangs on an open body until the process dies. It has
// to fail, and to fail retriably - a stalled connection is the textbook case for
// trying again.
func TestAStalledStreamFails(t *testing.T) {
	withStallTimeout(t, 100*time.Millisecond)

	hang := make(chan struct{})

	t.Cleanup(func() { close(hang) })

	client := serveTransport(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"x"}}]}`+"\n\n")

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		select {
		case <-hang:
		case <-r.Context().Done():
		}
	})

	started := time.Now()

	_, _, err := collect(client)

	if err == nil {
		t.Fatal("a stream that went silent must not hang forever")
	}

	if !IsRetriable(err) {
		t.Errorf("a stalled stream should be retriable: %v", err)
	}

	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the stall took %s to surface", elapsed)
	}
}

// The silence bound has to cover the error path too. A non-2xx response is read
// in full before it is classified, and a server that sends its status and then
// holds the body open would otherwise park that read - and the run with it -
// indefinitely: the wall-clock cap that used to bound it is gone, and the
// request ctx of an unattended run carries no deadline.
func TestAnErrorResponseWithAStalledBodyDoesNotWedge(t *testing.T) {
	withStallTimeout(t, 100*time.Millisecond)

	hang := make(chan struct{})

	client := serveTransport(t, false, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)

		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		// never send a body, never close
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	})

	// registered after serveTransport so it runs before the server's own
	// cleanup: Close waits for the handler, which waits for this release
	t.Cleanup(func() { close(hang) })

	done := make(chan error, 1)

	go func() {
		var failed error

		for event := range client.Stream(context.Background(), Request{}) {
			if event.Err != nil {
				failed = event.Err
			}
		}

		done <- failed
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a 500 must surface as an error")
		}

		// the status arrived and is what classifies the failure; the stalled
		// body must not cost the caller that information
		if status, ok := statusOf(err); !ok || status != http.StatusInternalServerError {
			t.Errorf("err = %v, want it to carry the 500 that was received", err)
		}

	case <-time.After(5 * time.Second):
		t.Fatal("the error path hung: a stalled error body must be bounded like a stalled stream")
	}
}

// withStallTimeout shortens the silence bound for the duration of a test.
func withStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()

	restore := streamStallTimeout

	streamStallTimeout = d

	t.Cleanup(func() { streamStallTimeout = restore })
}
