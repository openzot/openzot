//go:build portable

package config

import _ "embed"

// portableYAML is the configuration compiled into a portable build.
//
// The file must exist at build time: copy internal/config/portable.example.yaml
// to internal/config/portable.yaml, fill in the providers and keys you want
// baked, and build with `-tags portable`. If it is missing the embed fails the
// build with "no matching files found" - which is the intended guardrail, since
// a portable binary with no baked config would be a silent mistake.
//
// portable.yaml is gitignored: it typically holds credentials, and a portable
// binary is only as safe to distribute as those keys are to hand out.
//
//go:embed portable.yaml
var portableYAML []byte

// portableConfig returns the compiled-in configuration overlay.
func portableConfig() []byte { return portableYAML }
