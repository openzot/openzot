package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "0.1.1", true},
		{"v0.1.0", "v0.2.0", true},
		{"1.0.0", "2.0.0", true},
		{"1.0.0", "1.0.0", false},
		{"1.2.0", "1.1.9", false},
		{"0.1.0", "not-a-version", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.current, c.latest); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestIsDevDefault(t *testing.T) {
	// Built without ldflags (as in tests), Version defaults to "dev".
	if !IsDev() {
		t.Errorf("IsDev() = false for Version %q, want true", Version)
	}
}

// The update notice is only shown when there is genuinely something to update
// to; a spurious one is worse than none, because it trains people to ignore it.
func TestFormatUpdateNotice(t *testing.T) {
	notice := FormatUpdateNotice(&CheckResult{
		Current:   "v1.0.0",
		Latest:    "v1.2.0",
		UpdateURL: "https://example.com/releases/v1.2.0",
		Outdated:  true,
	})

	// The notice has to name both versions, say how to upgrade - the installer
	// replaces the binary in place, so it is the upgrade command - and link the
	// release for anyone who wants to read the notes first.
	for _, want := range []string{"v1.0.0", "v1.2.0", InstallCommand, "https://example.com/releases/v1.2.0"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice is missing %q: %q", want, notice)
		}
	}

	if got := FormatUpdateNotice(nil); got != "" {
		t.Errorf("a nil result must produce no notice, got %q", got)
	}

	if got := FormatUpdateNotice(&CheckResult{Outdated: false}); got != "" {
		t.Errorf("an up-to-date result must produce no notice, got %q", got)
	}
}

// A development build has no release to compare against, so the check is skipped
// rather than reporting nonsense - and, importantly, makes no network call.
func TestCheckSkipsDevelopmentBuilds(t *testing.T) {
	original := Version

	t.Cleanup(func() { Version = original })

	for _, dev := range []string{"dev", ""} {
		Version = dev

		result, err := Check()

		if err != nil {
			t.Errorf("Version=%q: Check returned %v", dev, err)
		}

		if result != nil {
			t.Errorf("Version=%q: Check returned %+v, want nil", dev, result)
		}
	}
}

func TestIsDev(t *testing.T) {
	original := Version

	t.Cleanup(func() { Version = original })

	for _, value := range []string{"dev", ""} {
		Version = value

		if !IsDev() {
			t.Errorf("Version=%q should be a development build", value)
		}
	}

	Version = "v1.2.3"

	if IsDev() {
		t.Error("a tagged version is not a development build")
	}
}

// withAPI points the release check at a local server for the duration of a test.
func withAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()

	server := httptest.NewServer(handler)

	original := APIBase

	APIBase = server.URL

	t.Cleanup(func() {
		APIBase = original

		server.Close()
	})
}

func TestLatestRelease(t *testing.T) {
	withAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}

		json.NewEncoder(w).Encode(map[string]string{
			"tag_name": "v2.0.0",
			"html_url": "https://example.com/releases/v2.0.0",
		})
	})

	tag, url, err := LatestRelease()
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}

	if tag != "v2.0.0" {
		t.Errorf("tag = %q", tag)
	}

	if url != "https://example.com/releases/v2.0.0" {
		t.Errorf("url = %q", url)
	}
}

// An update check is a convenience, never a reason to fail - so every failure
// mode has to come back as an error the caller can ignore rather than a panic.
func TestLatestReleaseFailures(t *testing.T) {
	t.Run("a non-200 response", func(t *testing.T) {
		withAPI(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})

		if _, _, err := LatestRelease(); err == nil {
			t.Error("a rate-limited API must report an error")
		}
	})

	t.Run("a malformed body", func(t *testing.T) {
		withAPI(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "not json")
		})

		if _, _, err := LatestRelease(); err == nil {
			t.Error("an unparseable body must report an error")
		}
	})

	t.Run("an unreachable host", func(t *testing.T) {
		original := APIBase

		APIBase = "http://127.0.0.1:1"

		t.Cleanup(func() { APIBase = original })

		if _, _, err := LatestRelease(); err == nil {
			t.Error("an unreachable API must report an error")
		}
	})
}

func TestCheckComparesAgainstTheCurrentVersion(t *testing.T) {
	originalVersion := Version

	t.Cleanup(func() { Version = originalVersion })

	withAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"tag_name": "v2.0.0",
			"html_url": "https://example.com/v2.0.0",
		})
	})

	Version = "v1.0.0"

	result, err := Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if !result.Outdated {
		t.Error("v1.0.0 is older than v2.0.0")
	}

	if notice := FormatUpdateNotice(result); notice == "" {
		t.Error("an outdated build should produce a notice")
	}

	Version = "v2.0.0"

	result, err = Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if result.Outdated {
		t.Error("v2.0.0 is not older than v2.0.0")
	}

	if notice := FormatUpdateNotice(result); notice != "" {
		t.Errorf("an up-to-date build must not nag: %q", notice)
	}
}

func TestCheckPropagatesFailures(t *testing.T) {
	originalVersion := Version

	t.Cleanup(func() { Version = originalVersion })

	Version = "v1.0.0"

	withAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := Check(); err == nil {
		t.Error("Check must report a failed lookup")
	}
}
