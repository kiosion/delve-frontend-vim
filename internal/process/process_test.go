package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestList(t *testing.T) {
	exe := buildProgram(t)
	child := exec.Command(exe)
	relative := exec.Command("./" + filepath.Base(exe))
	relative.Dir = filepath.Dir(exe)
	sleep := exec.Command("sleep", "10")
	for _, cmd := range []*exec.Cmd{child, relative, sleep} {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
	}

	processes, err := List()
	if err != nil {
		t.Fatal(err)
	}
	has := func(pid int) bool {
		return slices.ContainsFunc(processes, func(p Process) bool { return p.PID == pid })
	}
	for _, cmd := range []*exec.Cmd{child, relative} {
		if !has(cmd.Process.Pid) {
			t.Errorf("no Go process %d started as %s in %+v", cmd.Process.Pid, cmd.Args[0], processes)
		}
	}
	if has(sleep.Process.Pid) {
		t.Errorf("sleep, which is not a Go program, is in the list")
	}
	if has(os.Getpid()) {
		t.Errorf("the calling process is in the list")
	}
}
