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

	// Body is the raw response body (bounded), kept verbatim for the session's
	// failure record: an opaque upstream "ERROR" and a proper context-length
	// message classify the same, but only the raw exchange lets an operator
	// troubleshoot the difference after the fact.
	Body string

	// RequestBytes is the size of the request that was refused. Against a
	// suspected context ceiling it is the number that turns a correlation into
	// a diagnosis.
	RequestBytes int

	// RequestBody is the JSON that was refused, kept (bounded) so a developer
	// build can dump the exact exchange. An opaque upstream 400 is only
	// diagnosable from the request that provoked it. Held in memory on the one
	// error that ends a run; persisted only by a dev build.
	RequestBody string

	// midstream marks a failure that arrived inside an already-open stream:
	// the request was accepted, the response began, and the error was written
	// into the body instead of a delta.
	//
	// That shape is the classification, and it is worth more than the prose. A
	// request the provider objects to never gets a stream at all - it is
	// refused with a 4xx before a byte of body exists - so an error that got
	// this far is about the generation rather than the asking, and asking again
	// is the right move. Gateways word these endlessly ("JSON error injected
	// into SSE stream" cost an unattended shift nineteen minutes with no retry
	// attempted), and matching each new wording is a losing game the structure
	// wins outright.
	midstream bool

	// upstream marks a gateway-wrapped upstream failure: the gateway reported
	// that the provider behind it failed, not that the request was wrong. The
	// distinction decides retriability - see IsRetriable.
	upstream bool

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

// stallPatterns match a stream that stopped producing - the upstream went
// quiet, rather than the request being wrong.
//
// These are the one wording allowed to outrank the status, because gateways
// report a stall with whatever status they please and at least one of them
// picks a 4xx: OpenRouter answers "Upstream idle timeout exceeded", and the
// status verdict below would refuse that forever. A stall is transient by
// definition - nothing about the request changes by trying it again, and the
// next attempt routes to whatever the gateway has available.
//
// Deliberately narrow. A bare "timeout" is not enough: "timeout must be a
// positive integer" is a rejected parameter, and retrying it is the burnt
// budget the status rule exists to prevent. Each pattern names the thing that
// went quiet, or says the wait itself elapsed.
var stallPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:idle|stream|read|upstream|inactivity)[ _-]?timeout\b`),
	regexp.MustCompile(`(?i)\btimeout exceeded\b`),
	regexp.MustCompile(`(?i)\b(?:request|upstream|connection) timed out\b`),
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
// only burns the budget. Two carve-outs, both about time rather than the
// request: a 408, and a message that names a stalled stream (see stallPatterns).
// A failure delivered inside an open stream bypasses it too, on structure
// rather than wording - see midstream.
//
// 429 is deliberately excluded. A rate limit needs Retry-After backoff, not a
// tight retry loop, and retrying it aggressively makes the throttling worse.
func IsRetriable(err error) bool {
	if err == nil {
		return false
	}

	// A gateway-wrapped upstream failure retries whatever status the gateway
	// chose for the envelope. OpenRouter reports "Provider returned error" as
	// a 400, but it means the provider behind the gateway failed - a 502 in a
	// 400 costume - and flaky upstreams (stealth endpoints especially) refuse
	// intermittently: a run died mid-flight to a hiccup one retry outlives.
	// A wrapped context overflow is not lost to the retry loop: the loop
	// checks DetectContextLimit before retriability, and the upstream's raw
	// body is folded into this error's message where those patterns match.
	var providerErr *Error

	if errors.As(err, &providerErr) && (providerErr.upstream || providerErr.midstream) {
		return true
	}

	message := err.Error()

	// A stall outranks the status; see stallPatterns for why this one wording
	// is allowed to, and why it is kept this narrow.
	for _, pattern := range stallPatterns {
		if pattern.MatchString(message) {
			return true
		}
	}

	// 408 joins the 5xx range: Request Timeout is the one 4xx that says the
	// exchange ran out of time rather than that the request was wrong, and a
	// gateway reaches for it when the upstream it is proxying went quiet.
	if status, ok := statusOf(err); ok {
		return status == http.StatusRequestTimeout || (status >= 500 && status <= 599)
	}

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

// maxDumpBody bounds a captured request or response body. Generous, because a
// developer dump wants the whole exchange, but capped so a pathological
// request cannot pin arbitrary memory on the error that ends a run.
const maxDumpBody = 1 << 20 // 1 MiB

// clip bounds a wrapped upstream body for an error message.
func clip(s string, limit int) string {
	if len(s) > limit {
		return s[:limit] + "…"
	}

	return s
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

			// Gateways wrap the upstream provider's failure: OpenRouter's
			// "Provider returned error" carries the actual diagnosis - the
			// upstream body and which provider produced it - in metadata.
			// Dropping it turns a named cause into a shrug.
			Metadata struct {
				Raw          string `json:"raw"`
				ProviderName string `json:"provider_name"`
			} `json:"metadata"`
		} `json:"error"`

		Message string `json:"message"`
	}

	upstream := false

	if err := json.Unmarshal(body, &parsed); err == nil {
		switch {
		case parsed.Error.Message != "":
			message = parsed.Error.Message

			if raw := strings.TrimSpace(parsed.Error.Metadata.Raw); raw != "" {
				message += ": " + clip(raw, 500)
			}

			if name := strings.TrimSpace(parsed.Error.Metadata.ProviderName); name != "" {
				message += " (upstream: " + name + ")"

				upstream = true
			}
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
		Body:         clip(strings.TrimSpace(string(body)), maxDumpBody),
		upstream:     upstream,
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
// IsProviderError reports whether err is a failure the provider returned, as
// opposed to a local one (a cancellation, a context deadline). It is how an
// abort tells the exchange worth preserving from the bare cancellation that
// ended it.
func IsProviderError(err error) bool {
	var providerErr *Error

	return errors.As(err, &providerErr)
}

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

// streamFailure is an error the provider wrote into a stream it had already
// begun answering on. See Error.midstream for why that alone makes it worth
// retrying, whatever the gateway called it.
func streamFailure(message string) *Error {
	return &Error{Status: 0, Message: message, midstream: true}
}

// nativeFailurePattern matches an upstream stop reason that names a failure.
//
// A gateway can carry the provider's own stop reason in native_finish_reason
// while presenting a normal finish_reason of "stop" to the caller. When a
// stealth upstream drops a tool-bearing request the pair is
// finish_reason:"stop" / native_finish_reason:"network_error" over an empty
// turn - a failure wearing a success's clothes. Read literally it reaches the
// loop as a silent empty turn: it nudges a dead provider and then reports that
// the model produced nothing, blaming the model for the gateway's fault. This
// is matched only against an otherwise-empty turn, so a real answer that
// happens to carry an unusual native reason is never touched.
var nativeFailurePattern = regexp.MustCompile(`(?i)error|fail|abort|cancel|timeout|network`)

// isNativeFailure reports whether a gateway's native_finish_reason names an
// upstream failure rather than a normal completion.
func isNativeFailure(reason string) bool {
	reason = strings.TrimSpace(reason)

	return reason != "" && nativeFailurePattern.MatchString(reason)
}
