// Package configs embeds the example configuration files baked into the binaries,
// used to seed a user's config on first run.
package configs

import _ "embed"

// Zotui is the commented starter config for zotui.
//
//go:embed zotui.example.yaml
var Zotui []byte
