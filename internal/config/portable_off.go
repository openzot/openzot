//go:build !portable

package config

// portableConfig returns the configuration compiled into the binary.
//
// Off by default: a standard build carries none, so zot is configured entirely
// at runtime from the file and the environment. A portable build (`-tags
// portable`) replaces this with a version that embeds internal/config/
// portable.yaml - see portable_on.go and docs/portable-config.md.
func portableConfig() []byte { return nil }
