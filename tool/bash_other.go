//go:build !unix

package tool

import "os/exec"

// configureProcessGroup is a no-op where POSIX process groups are unavailable;
// cancellation falls back to exec.CommandContext's default single-process kill.
func configureProcessGroup(cmd *exec.Cmd) {}
