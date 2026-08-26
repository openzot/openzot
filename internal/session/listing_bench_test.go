package session

import (
	"fmt"
	"testing"
)

// These two state what the scanning listing buys over reading every log whole:
// the same directory of long runs, once through List and once through Load.
// Run them together to see the gap:
//
//	go test ./internal/session -bench 'OverHistory' -benchtime 10x
func benchLogs(b *testing.B) string {
	b.Helper()

	dir := b.TempDir()

	const (
		logs      = 50
		payloads  = 100
		textBytes = 4096
	)

	payload := string(make([]byte, textBytes))

	for i := range logs {
		id := fmt.Sprintf("20260805-%06d", i)

		writer, err := Create(dir, id, Meta{ID: id, Task: "run " + id})
		if err != nil {
			b.Fatalf("Create: %v", err)
		}

		for range payloads {
			if err := writer.Event(Event{Kind: "tool_end", Text: payload}); err != nil {
				b.Fatalf("Event: %v", err)
			}
		}

		if err := writer.Result(Result{Reason: "settled"}); err != nil {
			b.Fatalf("Result: %v", err)
		}

		if err := writer.Close(); err != nil {
			b.Fatalf("Close: %v", err)
		}
	}

	return dir
}

// BenchmarkListOverHistory is what `zot sessions` - and every dispatch's check
// for an unfinished run - costs.
func BenchmarkListOverHistory(b *testing.B) {
	dir := benchLogs(b)
	b.ResetTimer()

	for range b.N {
		entries, err := List(dir)
		if err != nil || len(entries) != 50 {
			b.Fatalf("List: %v (%d entries)", err, len(entries))
		}
	}
}

// BenchmarkLoadOverHistory reads each log whole, as a resume or an export must.
// It is the ceiling the listing is measured against, not a path to optimise.
func BenchmarkLoadOverHistory(b *testing.B) {
	dir := benchLogs(b)

	paths := make([]string, 0, 50)

	entries, err := List(dir)
	if err != nil {
		b.Fatalf("List: %v", err)
	}

	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}

	b.ResetTimer()

	for range b.N {
		for _, path := range paths {
			if _, err := Load(path); err != nil {
				b.Fatalf("Load: %v", err)
			}
		}
	}
}
