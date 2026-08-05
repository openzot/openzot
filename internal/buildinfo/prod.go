//go:build !dev

package buildinfo

// Dev reports whether this is a developer build.
//
// False for every build that does not explicitly ask to be a developer build,
// which is what makes forgetting the tag safe rather than dangerous.
const Dev = false

// Kind names the build, for the version line.
const Kind = "release"
