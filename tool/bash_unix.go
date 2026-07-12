//go:build unix

package tool

import (
	"os/exec"
	"syscall"
	"time"
)

// bashWaitDelay bounds how long Run waits for the output pipe to drain after
// the process is signalled. A backgrounded child that inherited the pipe would
// otherwise wedge the call; after this delay exec force-closes the pipe and
// Run returns.
const bashWaitDelay = 2 * time.Second

// configureProcessGroup puts the command in its own process group and makes
// context cancellation (an internal timeout or an external cancel) kill the
// entire group, so shell-backgrounded children are terminated too rather than
// leaking. A no-op fallback applies on non-unix platforms (see bash_other.go),
// where the default single-process kill stands until Windows process-tree
// termination lands in a later milestone.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative PID signals the whole process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = bashWaitDelay
}
