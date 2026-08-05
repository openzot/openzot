package config

import (
	"path/filepath"
	"testing"
)

// Session logs are state, not configuration: zot writes them, prunes them, and
// nobody edits them. Putting them under the config directory would mean a
// synced dotfiles setup carried every run's transcript to every machine.
func TestDefaultSessionDir(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "an explicit override wins",
			env:  map[string]string{"ZOT_SESSION_DIR": "/runs"},
			want: "/runs",
		},
		{
			name: "a blank override is ignored",
			env:  map[string]string{"ZOT_SESSION_DIR": "   ", "HOME": "/home/someone"},
			want: filepath.Join("/home/someone", ".local", "state", "zot", "sessions"),
		},
		{
			name: "XDG state home",
			env:  map[string]string{"XDG_STATE_HOME": "/xdg/state"},
			want: filepath.Join("/xdg/state", "zot", "sessions"),
		},
		{
			name: "the home fallback",
			env:  map[string]string{"HOME": "/home/someone"},
			want: filepath.Join("/home/someone", ".local", "state", "zot", "sessions"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, key := range []string{"ZOT_SESSION_DIR", "XDG_STATE_HOME", "HOME"} {
				t.Setenv(key, "")
			}

			for key, value := range test.env {
				t.Setenv(key, value)
			}

			if got := DefaultSessionDir(); got != test.want {
				t.Errorf("DefaultSessionDir = %q, want %q", got, test.want)
			}
		})
	}
}

// Config and sessions must not collide: one is edited by hand and backed up,
// the other is generated and disposable.
func TestSessionsAreNotUnderTheConfigDirectory(t *testing.T) {
	t.Setenv("ZOT_SESSION_DIR", "")
	t.Setenv("ZOT_CONFIG", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/someone")

	sessions := DefaultSessionDir()

	if config := ConfigDir(""); filepath.Dir(sessions) == config || sessions == config {
		t.Errorf("session dir %q must not sit inside the config dir %q", sessions, config)
	}
}
