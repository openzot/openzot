package zot

import _ "embed"

// ExampleConfigYAML is the commented starter config baked into the binary, used
// by `zot config` to seed ~/.config/zot/config.yaml when it does not exist yet.
//
//go:embed configs/zot.example.yaml
var ExampleConfigYAML []byte
