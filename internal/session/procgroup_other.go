//go:build !unix

package session

import "os/exec"

func startProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	cmd.Process.Kill()
}
