//go:build dev

package buildinfo

// Dev reports whether this is a developer build.
//
// True only when compiled with `-tags dev` - `make dev`, or `go build -tags
// dev`. Released binaries are built without it.
const Dev = true

// Kind names the build, for the version line.
const Kind = "dev"
