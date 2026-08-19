//go:build windows

package agent

import "os/exec"

// setProcessGroup is a no-op on Windows, which has no process groups in the
// POSIX sense. The WaitDelay on the command still bounds the wedge a lingering
// child can cause: the tool call gives up on the inherited pipe rather than
// blocking on it for the life of the process.
func setProcessGroup(*exec.Cmd) {}
