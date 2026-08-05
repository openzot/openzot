package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFromTargetDirectory(t *testing.T) {
	const key = "ZOT_TEST_TARGET_DOTENV"

	original, hadOriginal := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOriginal {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"=loaded\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loadEnv(dir)

	if got := os.Getenv(key); got != "loaded" {
		t.Errorf("%s = %q, want loaded", key, got)
	}
}
