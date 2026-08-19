package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Error is a provider failure carrying the HTTP status that produced it.
type Error struct {
	// Status is the HTTP status, or 0 for a transport failure.
	Status int

	// Message is the provider's description of what went wrong.
	Message string

	// retryAfter is the delay the provider's Retry-After header advised, and
	// retryAdvised whether it advised anything at all. Two fields rather than a
	// sentinel because zero is a real answer - a Retry-After already in the past
	// means "try now", which is not the same as no advice.
	retryAfter   time.Duration
	retryAdvised bool

	cause error
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("provider: %s", e.Message)
	}

	return fmt.Sprintf("provider: %s (%d)", e.Message, e.Status)
}

func (e *Error) Unwrap() error {
	return e.cause
}

// retriablePatterns match transient failures that arrive without a usable
// status - a bare transport error, or a gateway that puts the real problem in
// the body of a 200.
//
// @note matching on prose is fragile, and that fragility has bitten: gateways
// word the same condition differently ("Service unavailable" versus "Service
// temporarily unavailable"), and a wording the list does not anticipate turns a
// transient blip into a hard failure. Prefer the status; these are the fallback
// for errors that carry none.
var retriablePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)provider returned error`),
	regexp.MustCompile(`(?i)internal server error`),
	regexp.MustCompile(`(?i)bad gateway`),
	// tolerate an adverb between the two words, as well as the bare form
	regexp.MustCompile(`(?i)service\s+(?:\w+\s+)?unavailable`),
	regexp.MustCompile(`(?i)temporarily unavailable`),
	regexp.MustCompile(`(?i)gateway timeout`),
	regexp.MustCompile(`(?i)\boverloaded\b`),
	regexp.MustCompile(`(?i)connection reset`),
	regexp.MustCompile(`(?i)EOF`),
	// zot's own wordings for a stream that died mid-turn - a truncated or
	// stalled body is the textbook case for trying again
	regexp.MustCompile(`(?i)the stream (?:ended|stalled)`),
}

// trailingStatusPattern matches the status some providers append to a message.
//
// Worth recovering even when the error carries no status field: a status is
// authoritative where prose is not, and a 404 whose message happens to contain a
// transient-sounding word must not be retried.
var trailingStatusPattern = regexp.MustCompile(`\((\d{3})\)\s*$`)

// statusOf recovers the HTTP status behind an error, if it carries one.
func statusOf(err error) (int, bool) {
	var providerErr *Error

	if errors.As(err, &providerErr) && providerErr.Status != 0 {
		return providerErr.Status, true
	}

	if found := trailingStatusPattern.FindStringSubmatch(err.Error()); len(found) == 2 {
		status, convErr := strconv.Atoi(found[1])

		if convErr == nil && status >= 100 && status <= 599 {
			return status, true
		}
	}

	return 0, false
}

// IsRetriable reports whether an error is a transient provider failure worth
// retrying.
//
// The status is authoritative in both directions when present: a 5xx retries and
// a 4xx does not, whatever the message happens to say. A 4xx is caused by the
// request itself - a bad key, a model the provider does not have - and retrying
// only burns the budget.
//
// 429 is deliberately excluded. A rate limit needs Retry-After backoff, not a
// tight retry loop, and retrying it aggressively makes the throttling worse.
func IsRetriable(err error) bool {
	if err == nil {
		return false
	}

	if status, ok := statusOf(err); ok {
		return status >= 500 && status <= 599
	}

	message := err.Error()

	if message == "" {
		return false
	}

	for _, pattern := range retriablePatterns {
		if pattern.MatchString(message) {
			return true
		}
	}

	return false
}

// IsRateLimited reports a 429, which the caller should back off from rather than
// retry immediately.
func IsRateLimited(err error) bool {
	var providerErr *Error

	return errors.As(err, &providerErr) && providerErr.Status == 429
}

// RetryAfter reports the delay the provider advised before trying again, and
// whether it advised one at all.
//
// This is the half of the rate-limit contract that makes excluding 429 from
// IsRetriable defensible: the caller does not loop, it waits for as long as the
// provider asked. Both forms of the header are understood, and a delay of zero
// with ok true means "now" rather than "no advice".
func RetryAfter(err error) (time.Duration, bool) {
	var providerErr *Error

	if !errors.As(err, &providerErr) {
		return 0, false
	}

	return providerErr.retryAfter, providerErr.retryAdvised
}

// parseRetryAfter reads the two forms the Retry-After header takes: a count of
// seconds, and an HTTP-date to wait until. A date already in the past is advice
// to retry now, not a negative sleep.
func parseRetryAfter(header string) (time.Duration, bool) {
	value := strings.TrimSpace(header)

	if value == "" {
		return 0, false
	}

	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, true
		}

		// saturate rather than overflow: a count of seconds too large for the
		// nanosecond arithmetic would wrap negative, and a negative delay slips
		// under every "longer than the cap" check - so the hostile header the
		// caller's cap exists for would strip the backoff to zero instead.
		// Positive-and-absurd is safe; the caller's cap brings it down.
		if int64(seconds) > math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64), true
		}

		return time.Duration(seconds) * time.Second, true
	}

	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}

	if delay := time.Until(when); delay > 0 {
		return delay, true
	}

	return 0, true
}

// readError turns a non-2xx response into a classified error.
//
// Shared by every transport, which is also why the Retry-After header is read
// here: a rate limit arrives the same way whichever wire format asked for it,
// and a backoff the caller never sees is no better than none.
func readError(response *http.Response) error {
	// the same silence bound the streaming path gets. Without it a server that
	// sends its status and then holds the body open parks this read - and the
	// run with it - indefinitely: no wall-clock cap bounds it any more, and the
	// request ctx of an unattended run carries no deadline. A stalled body
	// costs its text, not the classification - the status already arrived, and
	// the fallback below still names it.
	stream := newStallReader(response.Body, streamStallTimeout)

	defer stream.stop()

	body, _ := io.ReadAll(io.LimitReader(stream, 64*1024))

	message := strings.TrimSpace(string(body))

	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`

		Message string `json:"message"`
	}

	if err := json.Unmarshal(body, &parsed); err == nil {
		switch {
		case parsed.Error.Message != "":
			message = parsed.Error.Message
		case parsed.Message != "":
			message = parsed.Message
		}
	}

	if message == "" {
		message = http.StatusText(response.StatusCode)
	}

	delay, advised := parseRetryAfter(response.Header.Get("Retry-After"))

	return &Error{
		Status:       response.StatusCode,
		Message:      message,
		retryAfter:   delay,
		retryAdvised: advised,
	}
}

// contextLimitPatterns identify a prompt that exceeded the model's window.
//
// This is recoverable in a way most 4xx are not: the run can compact and try
// again rather than failing. Detecting it needs prose because providers report
// it as a generic 400 with no distinguishing code.
var contextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)context[ _-]?length`),
	regexp.MustCompile(`(?i)context window`),
	regexp.MustCompile(`(?i)maximum context`),
	regexp.MustCompile(`(?i)too many tokens`),
	regexp.MustCompile(`(?i)reduce the length`),
	regexp.MustCompile(`(?i)prompt is too long`),
	regexp.MustCompile(`(?i)input length and .* exceed`),
}

// maximumContextPattern pulls the real window out of the rejection.
//
// Worth the fragility: when a provider refuses a prompt for length it states the
// actual limit, and that is ground truth in a way the catalogue is not. A model
// nobody has catalogued, or a provider serving a smaller variant of a known one,
// is exactly the case where the local budget was wrong - so believing the error
// is how the retry actually fits instead of guessing again.
var maximumContextPattern = regexp.MustCompile(`(?i)maximum context length is\s+(\d+)\s+tokens`)

// usedTokensPattern pulls out what the rejected request actually cost.
var usedTokensPattern = regexp.MustCompile(`(?i)resulted in\s+(\d+)\s+tokens`)

// contextLimitSafetyRatio is how much of the stated window to aim for on retry.
//
// Not all of it: the stated number is the whole context window, which has to
// hold the answer too, and the rebuilt prompt carries slightly different
// overhead. Retrying at exactly the limit fails again for the same reason.
const contextLimitSafetyRatio = 0.85

// ContextLimit is what a length rejection revealed.
type ContextLimit struct {
	// MaxTokens is the window the provider stated, or zero if it did not.
	MaxTokens int

	// UsedTokens is what the rejected request cost, or zero if unstated.
	UsedTokens int

	// SuggestedLimit is the budget to retry under. Zero when the provider gave
	// no number, in which case the caller falls back to its own estimate.
	SuggestedLimit int
}

// DetectContextLimit reports whether a request was rejected for being too large,
// and what the provider said about it.
func DetectContextLimit(err error) (ContextLimit, bool) {
	if err == nil {
		return ContextLimit{}, false
	}

	message := err.Error()

	matched := false

	for _, pattern := range contextLimitPatterns {
		if pattern.MatchString(message) {
			matched = true

			break
		}
	}

	if !matched {
		return ContextLimit{}, false
	}

	limit := ContextLimit{}

	if found := maximumContextPattern.FindStringSubmatch(message); len(found) == 2 {
		if value, convErr := strconv.Atoi(found[1]); convErr == nil && value > 0 {
			limit.MaxTokens = value
			limit.SuggestedLimit = int(float64(value) * contextLimitSafetyRatio)
		}
	}

	if found := usedTokensPattern.FindStringSubmatch(message); len(found) == 2 {
		if value, convErr := strconv.Atoi(found[1]); convErr == nil {
			limit.UsedTokens = value
		}
	}

	return limit, true
}

// IsContextLimit reports whether the request was rejected for being too large,
// which the loop answers by compacting rather than by giving up.
func IsContextLimit(err error) bool {
	_, ok := DetectContextLimit(err)

	return ok
}

// IsAuth reports a credential problem, which never resolves by retrying.
func IsAuth(err error) bool {
	var providerErr *Error

	if !errors.As(err, &providerErr) {
		return false
	}

	if providerErr.Status == 401 || providerErr.Status == 403 {
		return true
	}

	return strings.Contains(strings.ToLower(providerErr.Message), "api key")
}
