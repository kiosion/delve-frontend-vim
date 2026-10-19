// Package process lists the Go processes that a debugger can attach to.
package process

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Process is a running Go program.
type Process struct {
	// PID is the process ID.
	PID int `json:"pid"`
	// Name is the base name of the executable.
	Name string `json:"name"`
	// Command is the command line.
	Command string `json:"command"`
}

// List returns the Go processes of the current user that a debugger can attach to,
// other than the calling process, in descending PID order.
func List() ([]Process, error) {
	uid := strconv.Itoa(os.Getuid())
	paths, err := ps(uid, "comm")
	if err != nil {
		return nil, err
	}
	commands, err := ps(uid, "args")
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" {
		// ps on macOS prints the name or relative path that started the process.
		var relative []string
		for pid, path := range paths {
			if !filepath.IsAbs(path) {
				relative = append(relative, strconv.Itoa(pid))
			}
		}
		if len(relative) > 0 {
			maps.Copy(paths, executables(relative))
		}
	}
	attachable := map[string]bool{}
	var processes []Process
	for pid, path := range paths {
		if pid == os.Getpid() {
			continue
		}
		// ps prints a truncated name on Linux, and /proc holds the full path.
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			path = exe
		}
		canAttach, ok := attachable[path]
		if !ok {
			_, err := buildinfo.ReadFile(path)
			canAttach = err == nil && !refusesDebugger(path)
			attachable[path] = canAttach
		}
		if canAttach {
			processes = append(processes, Process{PID: pid, Name: filepath.Base(path), Command: commands[pid]})
		}
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID > processes[j].PID })
	return processes, nil
}

// ps returns the field of each process of user uid, by process ID.
func ps(uid, field string) (map[int]string, error) {
	out, err := exec.Command("ps", "-U", uid, "-o", "pid=,"+field+"=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	fields := map[int]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		pid, value, _ := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if n, err := strconv.Atoi(pid); err == nil {
			fields[n] = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	return fields, nil
}

// executables returns the executable path of each process in pids, by process ID. lsof
// lists the executable first among the txt files of a process.
func executables(pids []string) map[int]string {
	// lsof exits with an error when a process has exited, and prints the others.
	out, _ := exec.Command("lsof", "-b", "-w", "-n", "-P", "-a", "-d", "txt", "-Fn", "-p", strings.Join(pids, ",")).Output()
	paths := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && paths[pid] == "":
			paths[pid] = line[1:]
		}
	}
	return paths
}
