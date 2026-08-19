package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// rateLimited serves a 429 carrying the given Retry-After header (omitted when
// empty) on the requested wire format, and returns the error the stream raised.
func rateLimited(t *testing.T, responses bool, header string) error {
	t.Helper()

	client := serveTransport(t, responses, func(w http.ResponseWriter, _ *http.Request) {
		if header != "" {
			w.Header().Set("Retry-After", header)
		}

		w.WriteHeader(http.StatusTooManyRequests)

		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	})

	var failed error

	for event := range client.Stream(context.Background(), Request{}) {
		if event.Err != nil {
			failed = event.Err
		}
	}

	if failed == nil {
		t.Fatal("a 429 must surface as an error")
	}

	return failed
}

// A 429 is deliberately not retriable, on the promise that the caller backs off
// by what the provider advised. That promise is empty unless the header actually
// reaches the caller, so the delay has to survive from the response onto the
// error - on both transports, since the rate limit hits whichever one is in use.
func TestRetryAfterReachesTheCaller(t *testing.T) {
	for _, responses := range []bool{false, true} {
		err := rateLimited(t, responses, "30")

		if !IsRateLimited(err) {
			t.Fatalf("responses=%v: a 429 must be identifiable as a rate limit: %v", responses, err)
		}

		delay, advised := RetryAfter(err)

		if !advised {
			t.Fatalf("responses=%v: the provider advised a delay and it was lost", responses)
		}

		if delay != 30*time.Second {
			t.Errorf("responses=%v: delay = %s, want 30s", responses, delay)
		}
	}
}

// Retry-After has two forms and providers use both; an HTTP-date must become the
// same kind of answer as delta-seconds rather than being discarded.
func TestRetryAfterAcceptsAnHTTPDate(t *testing.T) {
	when := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)

	delay, advised := RetryAfter(rateLimited(t, false, when))

	if !advised {
		t.Fatal("an HTTP-date Retry-After must be understood")
	}

	if delay < 40*time.Second || delay > 46*time.Second {
		t.Errorf("delay = %s, want roughly 45s", delay)
	}
}

// A date already in the past means "now", not a negative sleep.
func TestRetryAfterInThePastIsZero(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)

	delay, advised := RetryAfter(rateLimited(t, false, past))

	if !advised || delay != 0 {
		t.Errorf("delay = %s/%v, want 0/true", delay, advised)
	}
}

// A delta-seconds value too large for the nanosecond arithmetic wrapped
// negative, and a negative delay slips under every "longer than the cap" check -
// so the exact hostile header the cap exists for stripped the backoff to zero
// instead. Saturate: stay positive and huge, and let the caller's cap decide.
func TestAHugeRetryAfterSaturatesRatherThanOverflowing(t *testing.T) {
	// 1e10 seconds is ~317 years: enough to overflow int64 nanoseconds, small
	// enough for Atoi to parse it as advice
	delay, advised := RetryAfter(rateLimited(t, false, "9999999999"))

	if !advised {
		t.Fatal("a parseable delta-seconds header is advice, however absurd")
	}

	if delay <= 0 {
		t.Fatalf("delay = %v, want a large positive duration - negative reads as no wait at all", delay)
	}

	if delay < 24*time.Hour {
		t.Errorf("delay = %v, want saturation to stay above any real cap", delay)
	}
}

// No header, or one that is neither form, must be reported as "no advice" so the
// caller falls back to its own backoff rather than sleeping for zero.
func TestRetryAfterIsAbsentWhenUnadvised(t *testing.T) {
	for _, header := range []string{"", "soon"} {
		if _, advised := RetryAfter(rateLimited(t, false, header)); advised {
			t.Errorf("header %q must not read as advice", header)
		}
	}

	if _, advised := RetryAfter(errors.New("not a provider error")); advised {
		t.Error("a plain error advises nothing")
	}

	if _, advised := RetryAfter(nil); advised {
		t.Error("nil advises nothing")
	}
}
