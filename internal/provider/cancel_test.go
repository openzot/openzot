package provider

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"testing"
	"time"
)

// Cancelling a turn must surface as an error to a consumer that is still
// draining. The hazard is in the shape of the send: the terminal Err event is
// delivered under the same ctx that was just cancelled, and a select with both
// a ready receiver and a done ctx picks at random - so without a dedicated
// terminal path, roughly half of all cancelled turns returned a partial answer
// with a nil error. Repeated because the failure is probabilistic: one clean
// pass proves nothing, 25 losing none is the actual contract.
func TestACancelledTurnAlwaysSurfacesItsError(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%v", responses), func(t *testing.T) {
			frame := `{"choices":[{"delta":{"content":"x"}}]}`

			if responses {
				frame = `{"type":"response.output_text.delta","delta":"x"}`
			}

			client := serveTransport(t, responses, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")

				flusher, _ := w.(http.Flusher)

				// stream steadily until the client goes away, so the cancel
				// always lands mid-turn
				for {
					fmt.Fprintf(w, "data: %s\n\n", frame)

					if flusher != nil {
						flusher.Flush()
					}

					select {
					case <-time.After(time.Millisecond):
					case <-r.Context().Done():
						return
					}
				}
			})

			for i := 0; i < 25; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)

				message, _, _, err := client.Complete(ctx, Request{})

				cancel()

				if err == nil {
					t.Fatalf("iteration %d: the cancellation vanished - Complete returned %d bytes of partial turn with a nil error",
						i, len(message.Content))
				}
			}
		})
	}
}

// The loop's runaway guard abandons a turn mid-stream: it stops reading the
// channel and cancels. Every send in a transport therefore has to select on
// ctx.Done(), or the producer parks on an unbuffered channel for the life of the
// process, holding the response body - one leaked goroutine and one leaked
// connection per abandoned turn.
func TestAnAbandonedStreamReleasesItsGoroutine(t *testing.T) {
	for _, responses := range []bool{false, true} {
		t.Run(fmt.Sprintf("responses=%v", responses), func(t *testing.T) {
			hang := make(chan struct{})

			t.Cleanup(func() { close(hang) })

			frame := `{"choices":[{"delta":{"content":"x"}}]}`

			if responses {
				frame = `{"type":"response.output_text.delta","delta":"x"}`
			}

			client := serveTransport(t, responses, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")

				flusher, _ := w.(http.Flusher)

				for i := 0; i < 64; i++ {
					fmt.Fprintf(w, "data: %s\n\n", frame)

					if flusher != nil {
						flusher.Flush()
					}
				}

				// hold the body open, as a real provider mid-turn would
				select {
				case <-hang:
				case <-r.Context().Done():
				}
			})

			before := runtime.NumGoroutine()

			ctx, cancel := context.WithCancel(context.Background())

			events := client.Stream(ctx, Request{})

			// take one event, then walk away without draining - exactly what the
			// runaway guard does
			<-events

			cancel()

			settled := false

			for i := 0; i < 200; i++ {
				if runtime.NumGoroutine() <= before {
					settled = true

					break
				}

				time.Sleep(10 * time.Millisecond)
			}

			if !settled {
				t.Errorf("the transport goroutine is still parked after cancellation (%d goroutines, was %d)",
					runtime.NumGoroutine(), before)
			}
		})
	}
}
