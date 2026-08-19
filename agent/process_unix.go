//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup runs the command in its own process group, and makes
// cancellation kill that whole group rather than just the shell.
//
// A shell command routinely starts children, and the default cancellation kills
// only the process that was started - so `go test ./...` interrupted at its
// deadline leaves the test binaries running, and anything backgrounded outlives
// the run entirely. Addressing the group is what makes "stop this command" mean
// the command and everything it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}

		// a negative pid addresses the group rather than the single process
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
