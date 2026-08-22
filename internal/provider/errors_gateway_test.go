package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A gateway wraps the upstream provider's failure: OpenRouter's "Provider
// returned error" carries the actual diagnosis in error.metadata. Surfacing
// only the wrapper turned a named cause - a context overflow, a quota, a
// malformed request - into a shrug at the operator.
func TestReadErrorSurfacesGatewayMetadata(t *testing.T) {
	body := `{"error":{"message":"Provider returned error","code":400,` +
		`"metadata":{"raw":"{\"error\":\"This model's maximum context length is 32768 tokens\"}","provider_name":"Chutes"}}}`

	response := &http.Response{
		StatusCode: 400,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	err := readError(response)

	for _, want := range []string{"Provider returned error", "maximum context length", "upstream: Chutes", "(400)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// A plain error body without metadata keeps its shape.
func TestReadErrorWithoutMetadataIsUnchanged(t *testing.T) {
	response := &http.Response{
		StatusCode: 401,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid api key"}}`)),
	}

	if got := readError(response).Error(); got != "provider: invalid api key (401)" {
		t.Errorf("error = %q", got)
	}
}

// A provider refusal must carry the wire evidence out of the transport: the
// raw response body and the size of the request that was refused, for the
// session's failure record.
func TestRefusalCarriesWireEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"Provider returned error","metadata":{"raw":"ERROR","provider_name":"Stealth"}}}`, http.StatusBadRequest)
	}))

	defer server.Close()

	client, err := New(Config{Provider: "custom", Model: "m", APIKey: "k", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}

	_, _, _, err = client.Complete(context.Background(), Request{
		Messages: []ChatMessage{{Role: RoleUser, Content: "hello"}},
	})
	if err == nil {
		t.Fatal("expected the refusal to surface")
	}

	var providerErr *Error

	if !errors.As(err, &providerErr) {
		t.Fatalf("err = %T, want *Error", err)
	}

	if !strings.Contains(providerErr.Body, `"raw":"ERROR"`) {
		t.Errorf("Body = %q, want the raw response kept", providerErr.Body)
	}

	if providerErr.RequestBytes == 0 {
		t.Error("RequestBytes = 0, want the refused request's size")
	}

	if !strings.Contains(providerErr.Message, "upstream: Stealth") {
		t.Errorf("Message = %q, want the upstream named", providerErr.Message)
	}
}

// A gateway-wrapped upstream failure retries whatever status the gateway put
// on the envelope: OpenRouter's "Provider returned error" arrives as a 400
// but means the provider behind it failed - and flaky upstreams refuse
// intermittently, so treating it as a request defect killed runs a single
// retry would have saved. A plain 400 stays non-retriable.
func TestWrappedUpstreamFailureIsRetriable(t *testing.T) {
	wrapped := &http.Response{
		StatusCode: 400,
		Header:     http.Header{},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"ERROR","provider_name":"Stealth"}}}`)),
	}

	if !IsRetriable(readError(wrapped)) {
		t.Error("a wrapped upstream failure must be retriable")
	}

	plain := &http.Response{
		StatusCode: 400,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid request: unknown field"}}`)),
	}

	if IsRetriable(readError(plain)) {
		t.Error("a plain 400 is a request defect and must not retry")
	}
}
