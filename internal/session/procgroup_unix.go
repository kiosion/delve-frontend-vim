//go:build unix

package session

import (
	"os/exec"
	"syscall"
)

// startProcessGroup makes cmd start in a process group of its own, which also holds the
// go build that dlv runs.
func startProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
