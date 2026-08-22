//go:build !dev

package session

import "github.com/openzot/openzot/agent"

// dumpFailure is a no-op in a released build: the request body is the whole
// prompt, and a binary someone downloads must not spill it to disk beside
// every failed run. Only `make dev` (or `-tags dev`) enables the dump.
func (r *Recorder) dumpFailure(*agent.Failure) {}
