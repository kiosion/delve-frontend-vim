// Package session runs a debug session for the Vim plugin. It starts dlv in headless
// mode, drives it with delve's JSON-RPC client, and reports each change as an event that
// Vim can draw without further requests.
package session

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-delve/delve/pkg/goversion"
	"github.com/go-delve/delve/pkg/proc"
	"github.com/go-delve/delve/service/api"
	"github.com/go-delve/delve/service/rpc2"
)

// Config describes the program to debug.
type Config struct {
	// Mode is one of:
	//
	//   - test: debug the tests of the package.
	//   - debug: debug the main program of the package.
	//   - attach: attach to the running process PID.
	//   - connect: connect to the headless dlv listening at Addr.
	Mode string `json:"mode"`
	// PID is the process ID in attach mode.
	PID int `json:"pid"`
	// Addr is the host:port of dlv in connect mode.
	Addr string `json:"addr"`
	// Dir is the package directory.
	Dir string `json:"dir"`
	// Args are go test flags such as -run in test mode, and program arguments in debug
	// mode.
	Args []string `json:"args"`
	// Dlv is the dlv command.
	Dlv string `json:"dlv"`
	// Breakpoints are set before the program starts.
	Breakpoints []Breakpoint `json:"breakpoints"`
	// Watches are the expressions to evaluate at each stop.
	Watches []string `json:"watches"`
	// Grouping groups the goroutines of each stopped event.
	Grouping Grouping `json:"grouping"`
	// BuildFlags are passed to go build, in test and debug mode.
	BuildFlags []string `json:"build_flags"`
	// Env holds environment variables to set for dlv and the program it starts.
	Env map[string]string `json:"env"`
}

// Breakpoint is a breakpoint on a source line.
type Breakpoint struct {
	// ID is the ID of the Vim sign that marks the breakpoint.
	ID int `json:"id"`
	// File is the absolute path of the source file.
	File string `json:"file"`
	// Line is the 1-based line in File.
	Line int `json:"line"`
	// Cond is a Go expression. The program stops only when it is empty or true.
	Cond string `json:"cond"`
}

// Frame is one frame of the call stack.
type Frame struct {
	// Function is the full function name.
	Function string `json:"function"`
	// File is the absolute path of the source file.
	File string `json:"file"`
	// Line is the 1-based line in File.
	Line int `json:"line"`
}

// Node is one variable in the variables tree, shown as Name = Value.
type Node struct {
	// ID identifies the node in Children requests. It is 0 for the row that stands for
	// the elements that were not loaded.
	ID int `json:"id"`
	// Name is the variable name, or the key or index of an element.
	Name string `json:"name"`
	// Value is the formatted value.
	Value string `json:"value"`
	// Expandable is true when Children returns nodes below this one.
	Expandable bool `json:"expandable"`
}

// Event is a message to Vim that no request asked for.
type Event struct {
	// Event is the kind of event:
	//
	//   - output: the program or dlv printed the line Text.
	//   - stopped: Goroutine stopped at a breakpoint, after a step, or after Pause.
	//   - exited: the session ended with Status.
	//   - error: an error that no request is waiting for, with the message Text.
	Event string `json:"event"`
	// Text is the output line or the error message.
	Text string `json:"text,omitempty"`
	// Breakpoint is the ID of the breakpoint that an error event reports dlv rejected.
	Breakpoint int `json:"breakpoint,omitempty"`
	// Status is the exit status of the program, or of dlv when the program did not run.
	Status int `json:"status,omitempty"`
	// Goroutine is the ID of the stopped goroutine.
	Goroutine int64 `json:"goroutine,omitempty"`
	// Frames is the call stack of the stopped goroutine.
	Frames []Frame `json:"frames,omitempty"`
	// Goroutines are the goroutines, grouped as the Grouping of the session selects.
	Goroutines []GoroutineGroup `json:"goroutines,omitempty"`
	// Frame is the index of the selected frame in Frames.
	Frame int `json:"frame,omitempty"`
	// View shows the selected frame.
	View
}

// View is what Vim shows for one frame of the stopped goroutine.
type View struct {
	// Variables are the arguments and locals of the frame.
	Variables []Node `json:"variables,omitempty"`
	// Watches are the values of the watch expressions in the frame.
	Watches []Node `json:"watches,omitempty"`
	// Inline are the values to show at the end of the source lines of the frame.
	Inline []Inline `json:"inline,omitempty"`
}

// Grouping selects how the goroutines of a stopped event are grouped and which are
// listed.
type Grouping struct {
	// By is one of:
	//
	//   - user, the default: the innermost frame outside the runtime.
	//   - current: the innermost frame.
	//   - go: the go statement that started the goroutine.
	//   - start: the function the goroutine started in.
	//   - label: the value of the pprof label Key.
	By string `json:"by"`
	// Key is the pprof label that groups goroutines by label.
	Key string `json:"key"`
	// ShowRuntime lists the goroutines of the runtime with the user goroutines.
	ShowRuntime bool `json:"show_runtime"`
}

var groupings = map[string]api.GoroutineField{
	"":        api.GoroutineUserLoc,
	"user":    api.GoroutineUserLoc,
	"current": api.GoroutineCurrentLoc,
	"go":      api.GoroutineGoLoc,
	"start":   api.GoroutineStartLoc,
	"label":   api.GoroutineLabel,
}

// GoroutineGroup is the goroutines that share a location or a label value.
type GoroutineGroup struct {
	// Location is the shared location. It is empty when goroutines are grouped by label.
	Location Frame `json:"location"`
	// Label is the shared "key=value" label, when goroutines are grouped by label.
	Label string `json:"label,omitempty"`
	// Total is the number of goroutines in the group.
	Total int `json:"total"`
	// Goroutines are the first goroutines of the group, at most maxGroupMembers.
	Goroutines []Goroutine `json:"goroutines"`
}

// Goroutine is one goroutine of a GoroutineGroup.
type Goroutine struct {
	// ID is the goroutine ID.
	ID int64 `json:"id"`
	// Status is running, runnable, syscall or waiting, followed by the wait reason.
	Status string `json:"status"`
	// Location is the innermost frame of the goroutine outside the runtime.
	Location Frame `json:"location"`
	// Labels are the pprof labels of the goroutine.
	Labels map[string]string `json:"labels,omitempty"`
}

const (
	maxFrames       = 50
	maxValue        = 200
	maxGroups       = 50
	maxGroupMembers = 10
	haltTimeout     = 2 * time.Second
)

var loadConfig = api.LoadConfig{
	FollowPointers:     true,
	MaxVariableRecurse: 1,
	MaxStringLen:       maxValue,
	MaxArrayValues:     100,
	MaxStructFields:    -1,
}

var (
	listening = regexp.MustCompile(`^API server listening at: (.+)$`)
	exited    = regexp.MustCompile(`has exited with status (-?\d+)`)
	// unloadedValue matches the type and address that delve prints in place of a
	// value below the load depth.
	unloadedValue = regexp.MustCompile(`\*?\(\*("[^"]*"|[^()"]*)\)\(0x[0-9a-f]+\)`)
)

var commands = map[string]func(*rpc2.RPCClient) (*api.DebuggerState, error){
	"continue": func(c *rpc2.RPCClient) (*api.DebuggerState, error) {
		var state *api.DebuggerState
		for state = range c.Continue() {
		}
		return state, state.Err
	},
	"next":    (*rpc2.RPCClient).Next,
	"step":    (*rpc2.RPCClient).Step,
	"stepout": (*rpc2.RPCClient).StepOut,
}

// Session is one connection to dlv and the program it debugs.
type Session struct {
	notify func(Event)
	mode   string
	// cmd is the dlv process, or nil in connect mode.
	cmd *exec.Cmd
	// dir holds the socket of dlv and the binary that dlv builds, or is empty in connect
	// mode.
	dir string
	// ready receives the address of dlv's API server, and is closed when dlv exits
	// before it listens.
	ready chan string
	// done is closed when read has reported the exit of dlv.
	done chan struct{}

	mu     sync.Mutex
	client *rpc2.RPCClient
	// pending holds breakpoint changes by ID until dlv can apply them. A zero
	// Breakpoint clears the breakpoint.
	pending     map[int]Breakpoint
	breakpoints map[int]int
	running     bool
	// command is the running command, and run counts the commands started.
	command string
	run     int
	// halting is true while Halt is sent to end the running command, and paused is true
	// when Pause asked for it. A halt that only applies pending continues the program.
	halting   bool
	paused    bool
	stopping  bool
	status    *int
	goVersion goversion.GoVersion
	watches   []string
	grouping  Grouping
	goroutine int64
	stack     []api.Stackframe
	frame     int
	nodes     []node
}

type node struct {
	v     api.Variable
	frame int
}

// Launch starts or connects to dlv for cfg and runs the program to the first
// breakpoint. notify receives each event, from any goroutine.
func Launch(cfg Config, notify func(Event)) (*Session, error) {
	var args []string
	switch cfg.Mode {
	case "test", "debug":
		args = []string{cfg.Mode}
	case "attach":
		args = []string{"attach", strconv.Itoa(cfg.PID)}
	case "connect":
	default:
		return nil, fmt.Errorf("unknown mode %q", cfg.Mode)
	}
	s := &Session{
		notify:      notify,
		mode:        cfg.Mode,
		ready:       make(chan string, 1),
		done:        make(chan struct{}),
		pending:     map[int]Breakpoint{},
		breakpoints: map[int]int{},
		watches:     cfg.Watches,
		grouping:    cfg.Grouping,
	}
	for _, bp := range cfg.Breakpoints {
		s.pending[bp.ID] = bp
	}
	if cfg.Mode == "connect" {
		s.ready <- cfg.Addr
		go s.start()
		return s, nil
	}
	// MkdirTemp creates the directory with mode 0700, which keeps the socket private.
	dir, err := os.MkdirTemp("", "delve-frontend-")
	if err != nil {
		return nil, err
	}
	s.dir = dir
	args = append(args, "--headless", "--api-version=2", "--listen=unix:"+filepath.Join(dir, "dlv.sock"))
	if cfg.Mode != "attach" {
		// dlv builds into the package directory by default, and leaves the binary there
		// when it is killed.
		args = append(args, "--output="+filepath.Join(dir, "debug"))
		if len(cfg.BuildFlags) > 0 {
			args = append(args, "--build-flags="+buildFlags(cfg.BuildFlags))
		}
	}
	if program := programArgs(cfg); len(program) > 0 {
		args = append(append(args, "--"), program...)
	}
	s.cmd = exec.Command(cfg.Dlv, args...)
	s.cmd.Dir = cfg.Dir
	if len(cfg.Env) > 0 {
		s.cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			s.cmd.Env = append(s.cmd.Env, k+"="+v)
		}
	}
	startProcessGroup(s.cmd)
	out, err := s.cmd.StdoutPipe()
	if err == nil {
		s.cmd.Stderr = s.cmd.Stdout
		err = s.cmd.Start()
	}
	if err != nil {
		os.RemoveAll(s.dir)
		return nil, fmt.Errorf("start dlv: %w", err)
	}
	go s.read(out)
	go s.start()
	return s, nil
}

// buildFlags joins flags into the value of --build-flags, which dlv splits at spaces
// outside single quotes. A backslash in single quotes escapes the next character.
func buildFlags(flags []string) string {
	escape := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	quoted := make([]string, len(flags))
	for i, f := range flags {
		quoted[i] = "'" + escape.Replace(f) + "'"
	}
	return strings.Join(quoted, " ")
}

func programArgs(cfg Config) []string {
	if cfg.Mode != "test" {
		return cfg.Args
	}
	args := []string{"-test.v"}
	for _, arg := range cfg.Args {
		if strings.HasPrefix(arg, "-") {
			arg = "-test." + arg[1:]
		}
		args = append(args, arg)
	}
	return args
}

// read forwards the output of dlv and the program until dlv exits, then reports the
// exit.
func (s *Session) read(r io.Reader) {
	br := bufio.NewReader(r)
	listened := false
	for {
		line, err := br.ReadString('\n')
		if line = strings.TrimRight(line, "\r\n"); line != "" || err == nil {
			if m := listening.FindStringSubmatch(line); m != nil && !listened {
				listened = true
				s.ready <- m[1]
			} else {
				s.notify(Event{Event: "output", Text: line})
			}
		}
		if err != nil {
			break
		}
	}
	if !listened {
		close(s.ready)
	}
	s.cmd.Wait()
	os.RemoveAll(s.dir)
	s.mu.Lock()
	status := s.cmd.ProcessState.ExitCode()
	if s.status != nil {
		status = *s.status
	}
	s.mu.Unlock()
	s.notify(Event{Event: "exited", Status: status})
	close(s.done)
}

func (s *Session) start() {
	addr, ok := <-s.ready
	if !ok {
		return
	}
	network := "unix"
	if s.cmd == nil {
		network = "tcp"
	}
	conn, err := net.Dial(network, addr)
	if err != nil {
		s.fail(fmt.Errorf("connect to dlv: %w", err))
		return
	}
	client := rpc2.NewClientFromConn(conn)
	// In connect mode, another client can have left the program running.
	if state, err := client.GetStateNonBlocking(); err == nil && state.Running {
		if !halt(client, func() bool {
			state, err := client.GetStateNonBlocking()
			return err != nil || !state.Running
		}, haltTimeout) {
			client.Disconnect(false)
			s.fail(errors.New("halt: the program did not stop"))
			return
		}
	}
	s.mu.Lock()
	if s.stopping {
		s.release(client, 0)
		s.mu.Unlock()
		return
	}
	s.client = client
	s.goVersion = goversion.ParseProducer(client.GetVersion().TargetGoVersion)
	s.applyLocked()
	s.mu.Unlock()
	if err := s.Resume("continue"); err != nil {
		s.fail(err)
	}
}

func (s *Session) fail(err error) {
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if !stopping {
		s.notify(Event{Event: "error", Text: err.Error()})
	}
	if s.cmd == nil {
		s.notify(Event{Event: "exited", Status: 1})
		return
	}
	killProcessGroup(s.cmd)
}

// halt sends Halt every 100ms until stopped reports true, and reports whether that
// happened within timeout. dlv discards a Halt that arrives before a command starts to
// run the program, and Halt returns only when the command ends.
func halt(client *rpc2.RPCClient, stopped func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for i := 0; !stopped(); i++ {
		if time.Now().After(deadline) {
			return false
		}
		if i%10 == 0 {
			go client.Halt()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// haltLocked ends the running command, unless a halt is in progress. handleStop reports
// the stop.
func (s *Session) haltLocked() {
	if s.halting {
		return
	}
	s.halting = true
	client, run := s.client, s.run
	go func() {
		if halt(client, func() bool { return !s.runningCommand(run) }, haltTimeout) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.run == run && !s.stopping {
			s.halting = false
			s.notify(Event{Event: "error", Text: "halt: the program did not stop"})
		}
	}()
}

// runningCommand reports whether command run is running.
func (s *Session) runningCommand(run int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running && s.run == run
}

func (s *Session) createLocked(bp Breakpoint) error {
	created, err := s.client.CreateBreakpoint(&api.Breakpoint{File: bp.File, Line: bp.Line, Cond: bp.Cond})
	if err != nil {
		return fmt.Errorf("breakpoint at %s:%d: %w", filepath.Base(bp.File), bp.Line, err)
	}
	s.breakpoints[bp.ID] = created.ID
	return nil
}

// SetBreakpoint sets bp, or replaces the breakpoint with the same ID. While the
// program runs, it halts the program, sets bp and continues, and reports errors as
// error events.
func (s *Session) SetBreakpoint(bp Breakpoint) error {
	return s.changeBreakpoint(bp.ID, bp)
}

// ClearBreakpoint clears the breakpoint with the given ID, halting a running program
// as SetBreakpoint does.
func (s *Session) ClearBreakpoint(id int) error {
	return s.changeBreakpoint(id, Breakpoint{})
}

func (s *Session) changeBreakpoint(id int, bp Breakpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.status != nil || s.stopping:
		return nil
	case s.client != nil && !s.running:
		if err := s.clearLocked(id); err != nil || bp.File == "" {
			return err
		}
		return s.createLocked(bp)
	}
	// dlv holds its target lock while the program runs, and a breakpoint request waits
	// for it. handleStop applies pending at the next stop. A change during a step waits
	// for the step to end, because a Halt cancels the step.
	s.pending[id] = bp
	if s.client != nil && s.command == "continue" {
		s.haltLocked()
	}
	return nil
}

func (s *Session) applyLocked() {
	for id, bp := range s.pending {
		err := s.clearLocked(id)
		if err == nil && bp.File != "" {
			err = s.createLocked(bp)
		}
		if err != nil {
			s.notify(Event{Event: "error", Text: err.Error(), Breakpoint: id})
		}
	}
	clear(s.pending)
}

func (s *Session) clearLocked(id int) error {
	bp, ok := s.breakpoints[id]
	if !ok {
		return nil
	}
	if _, err := s.client.ClearBreakpoint(bp); err != nil {
		return err
	}
	delete(s.breakpoints, id)
	return nil
}

// Resume runs command, one of continue, next, step and stepout. It returns at once, and
// a Stopped or Exited event follows.
func (s *Session) Resume(command string) error {
	if _, ok := commands[command]; !ok {
		return fmt.Errorf("unknown command %q", command)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return err
	}
	s.runLocked(command)
	return nil
}

func (s *Session) runLocked(command string) {
	s.running = true
	s.command = command
	s.run++
	s.nodes = nil
	client, run := s.client, commands[command]
	go func() { s.handleStop(run(client)) }()
}

func (s *Session) stoppedLocked() error {
	switch {
	case s.client == nil || s.status != nil || s.stopping:
		return errors.New("the program is not running")
	case s.running:
		return errors.New("the program is running")
	}
	return nil
}

func (s *Session) handleStop(state *api.DebuggerState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	halting, paused := s.halting, s.paused
	s.halting, s.paused = false, false
	if s.stopping {
		return
	}
	if err != nil && !exited.MatchString(err.Error()) {
		s.notify(Event{Event: "error", Text: err.Error()})
		if !errors.As(err, new(rpc.ServerError)) {
			// The connection to dlv failed. In launch modes read reports the exit of dlv.
			if s.cmd == nil {
				s.exitLocked(1)
			} else {
				killProcessGroup(s.cmd)
			}
			return
		}
		// dlv returns an empty state with an error, and the program stays stopped.
		state, err = s.client.GetState()
	}
	switch {
	case state != nil && state.Exited:
		s.exitLocked(state.ExitStatus)
		return
	case err != nil:
		if m := exited.FindStringSubmatch(err.Error()); m != nil {
			status, _ := strconv.Atoi(m[1])
			s.exitLocked(status)
		} else {
			s.notify(Event{Event: "error", Text: err.Error()})
		}
		return
	}
	s.applyLocked()
	if halting && !paused && (state.CurrentThread == nil || state.CurrentThread.Breakpoint == nil) {
		s.runLocked("continue")
		return
	}
	s.goroutine = -1
	if state.SelectedGoroutine != nil {
		s.goroutine = state.SelectedGoroutine.ID
	} else if gs, _, _, _, err := s.client.ListGoroutinesWithFilter(0, 1,
		[]api.ListGoroutinesFilter{{Kind: api.GoroutineUser}}, nil, nil); err == nil && len(gs) > 0 {
		s.goroutine = gs[0].ID
	}
	s.notify(s.stoppedEventLocked(s.goroutine != -1 && state.SelectedGoroutine == nil))
}

// stoppedEventLocked describes the goroutine s.goroutine and selects its first frame,
// or with user its first frame outside the runtime. It reports errors as error events,
// and leaves out what it failed to load.
func (s *Session) stoppedEventLocked(user bool) Event {
	event := Event{Event: "stopped", Goroutine: s.goroutine}
	var err error
	if s.stack, err = s.client.Stacktrace(s.goroutine, maxFrames, 0, 0, nil); err != nil {
		s.notify(Event{Event: "error", Text: err.Error()})
	}
	for _, f := range s.stack {
		event.Frames = append(event.Frames, frame(f.Location))
	}
	if user {
		event.Frame = max(0, slices.IndexFunc(event.Frames, func(f Frame) bool {
			return !strings.HasPrefix(f.Function, "runtime.")
		}))
	}
	if event.Goroutines, err = s.goroutinesLocked(); err != nil {
		s.notify(Event{Event: "error", Text: err.Error()})
	}
	if event.View, err = s.viewLocked(event.Frame); err != nil {
		s.notify(Event{Event: "error", Text: err.Error()})
	}
	return event
}

func frame(loc api.Location) Frame {
	name := "?"
	if loc.Function != nil {
		name = loc.Function.Name()
	}
	return Frame{Function: name, File: loc.File, Line: loc.Line}
}

// SetGrouping changes how goroutines are grouped. When the program is stopped, it
// returns the goroutines grouped the new way.
func (s *Session) SetGrouping(g Grouping) ([]GoroutineGroup, error) {
	if _, ok := groupings[g.By]; !ok {
		return nil, fmt.Errorf("unknown grouping %q", g.By)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grouping = g
	if s.stoppedLocked() != nil {
		return nil, nil
	}
	return s.goroutinesLocked()
}

func (s *Session) goroutinesLocked() ([]GoroutineGroup, error) {
	var filters []api.ListGoroutinesFilter
	if !s.grouping.ShowRuntime {
		filters = append(filters, api.ListGoroutinesFilter{Kind: api.GoroutineUser})
	}
	by := groupings[s.grouping.By]
	gs, groups, _, _, err := s.client.ListGoroutinesWithFilter(0, 0, filters,
		&api.GoroutineGroupingOptions{GroupBy: by, GroupByKey: s.grouping.Key, MaxGroupMembers: maxGroupMembers, MaxGroups: maxGroups},
		nil)
	if err != nil {
		return nil, err
	}
	result := []GoroutineGroup{}
	for _, group := range groups {
		if group.Count <= 0 || group.Offset < 0 || group.Offset+group.Count > len(gs) {
			continue
		}
		members := gs[group.Offset : group.Offset+group.Count]
		g := GoroutineGroup{Total: group.Total}
		switch by {
		case api.GoroutineUserLoc:
			g.Location = frame(members[0].UserCurrentLoc)
		case api.GoroutineCurrentLoc:
			g.Location = frame(members[0].CurrentLoc)
		case api.GoroutineGoLoc:
			g.Location = frame(members[0].GoStatementLoc)
		case api.GoroutineStartLoc:
			g.Location = frame(members[0].StartLoc)
		case api.GoroutineLabel:
			g.Label = group.Name
		}
		for _, m := range members {
			g.Goroutines = append(g.Goroutines, Goroutine{
				ID:       m.ID,
				Status:   s.goroutineStatus(m),
				Location: frame(m.UserCurrentLoc),
				Labels:   m.Labels,
			})
		}
		result = append(result, g)
	}
	return result, nil
}

func (s *Session) goroutineStatus(g *api.Goroutine) string {
	var status string
	switch {
	case g.ThreadID != 0 || g.Status == proc.Grunning:
		return "running"
	case g.Status == proc.Grunnable:
		return "runnable"
	case g.Status == proc.Gsyscall:
		status = "syscall"
	case g.Status == proc.Gwaiting:
		status = "waiting"
	default:
		return ""
	}
	if g.WaitReason != 0 {
		status += ": " + api.WaitReasonString(&s.goVersion, g.WaitReason)
	}
	return status
}

// exitLocked records the exit status of the program and ends the session. read
// reports the exit when dlv has exited and all output is forwarded.
func (s *Session) exitLocked(status int) {
	s.status = &status
	s.release(s.client, status)
}

// release ends the connection to dlv. In connect mode no dlv process exits, and
// release reports the exit with status.
func (s *Session) release(client *rpc2.RPCClient, status int) {
	switch s.mode {
	case "connect":
		for _, id := range s.breakpoints {
			client.ClearBreakpoint(id)
		}
		client.Disconnect(true)
		s.notify(Event{Event: "exited", Status: status})
	case "attach":
		client.Detach(false)
	default:
		client.Detach(true)
	}
}

// Pause stops the running program. A Stopped event follows.
func (s *Session) Pause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return errors.New("the program is not running")
	}
	s.paused = true
	s.haltLocked()
	return nil
}

// Stop ends the session. It kills a program that dlv started, and leaves other
// programs running. When the program does not stop for the detach, Stop kills dlv, or
// closes the connection in connect mode.
func (s *Session) Stop() {
	s.mu.Lock()
	if s.stopping || s.status != nil {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	client, running, run := s.client, s.running, s.run
	s.mu.Unlock()
	if client == nil {
		if s.cmd != nil {
			killProcessGroup(s.cmd)
		}
		return
	}
	if running && !halt(client, func() bool { return !s.runningCommand(run) }, haltTimeout) {
		if s.cmd != nil {
			killProcessGroup(s.cmd)
			return
		}
		client.Disconnect(false)
		s.notify(Event{Event: "exited"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.release(client, 0)
}

// Wait waits up to timeout for dlv to exit after Stop. It returns at once in connect
// mode.
func (s *Session) Wait(timeout time.Duration) {
	if s.cmd == nil {
		return
	}
	select {
	case <-s.done:
	case <-time.After(timeout):
	}
}

// Frame selects frame index of the stopped goroutine, and returns its view.
func (s *Session) Frame(index int) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return View{}, err
	}
	return s.viewLocked(index)
}

// Goroutine selects goroutine id, which the next step follows, and returns a stopped
// event for it.
func (s *Session) Goroutine(id int64) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return Event{}, err
	}
	if _, err := s.client.SwitchGoroutine(id); err != nil {
		return Event{}, err
	}
	s.goroutine = id
	return s.stoppedEventLocked(true), nil
}

// SetWatches replaces the watch expressions. When the program is stopped, it returns
// the view of the selected frame with the new watches.
func (s *Session) SetWatches(exprs []string) (*View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watches = exprs
	if s.stoppedLocked() != nil {
		return nil, nil
	}
	view, err := s.viewLocked(s.frame)
	return &view, err
}

// Evaluate evaluates the Go expression expr in the selected frame.
func (s *Session) Evaluate(expr string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return Node{}, err
	}
	v, err := s.client.EvalVariable(api.EvalScope{GoroutineID: s.goroutine, Frame: s.frame}, expr, loadConfig)
	if err != nil {
		return Node{}, err
	}
	return s.addLocked(s.frame, expr, *v), nil
}

// SetValue assigns the Go expression value to the variable of node id, and returns
// the view of the selected frame.
func (s *Session) SetValue(id int, value string) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return View{}, err
	}
	if id < 1 || id > len(s.nodes) {
		return View{}, fmt.Errorf("no variable %d", id)
	}
	n := s.nodes[id-1]
	symbol := n.v.Name
	if n.v.Addr != 0 && n.v.Type != "" {
		symbol = fmt.Sprintf("*(*%q)(%#x)", n.v.Type, n.v.Addr)
	}
	if err := s.client.SetVariable(api.EvalScope{GoroutineID: s.goroutine, Frame: n.frame}, symbol, value); err != nil {
		return View{}, err
	}
	return s.viewLocked(s.frame)
}

func (s *Session) viewLocked(frame int) (View, error) {
	s.frame = frame
	scope := api.EvalScope{GoroutineID: s.goroutine, Frame: frame}
	view := View{}
	for _, expr := range s.watches {
		v, err := s.client.EvalVariable(scope, expr, loadConfig)
		if err != nil {
			view.Watches = append(view.Watches, Node{Name: expr, Value: "error: " + err.Error()})
		} else {
			view.Watches = append(view.Watches, s.addLocked(frame, expr, *v))
		}
	}
	args, err := s.client.ListFunctionArgs(scope, loadConfig)
	if err != nil {
		return view, err
	}
	locals, err := s.client.ListLocalVariables(scope, loadConfig)
	if err != nil {
		return view, err
	}
	vars := append(args, locals...)
	for _, v := range vars {
		view.Variables = append(view.Variables, s.addLocked(frame, v.Name, v))
	}
	if frame < len(s.stack) {
		view.Inline = inlineValues(s.stack[frame].File, s.stack[frame].Line, vars)
	}
	return view, nil
}

func (s *Session) addLocked(frame int, name string, v api.Variable) Node {
	s.nodes = append(s.nodes, node{v: v, frame: frame})
	return Node{ID: len(s.nodes), Name: name, Value: value(&v), Expandable: expandable(&v)}
}

// Children returns the nodes below node id. It loads the variable again from its
// address, one level deeper than the first load.
func (s *Session) Children(id int) ([]Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return nil, err
	}
	if id < 1 || id > len(s.nodes) {
		return nil, fmt.Errorf("no variable %d", id)
	}
	n := s.nodes[id-1]
	v := n.v
	if v.Addr != 0 && v.Type != "" {
		scope := api.EvalScope{GoroutineID: s.goroutine, Frame: n.frame}
		if loaded, err := s.client.EvalVariable(scope, fmt.Sprintf("*(*%q)(%#x)", v.Type, v.Addr), loadConfig); err == nil {
			v = *loaded
		}
	}
	nodes := []Node{}
	for _, c := range children(&v) {
		nodes = append(nodes, s.addLocked(n.frame, c.name, c.v))
	}
	if more := unloaded(&v); more > 0 {
		nodes = append(nodes, Node{Name: fmt.Sprintf("(%d more)", more)})
	}
	return nodes, nil
}

type child struct {
	name string
	v    api.Variable
}

// children lists the elements of v under the names the variables tree shows. The
// tree shows the target of a pointer or interface in place of the pointer, when the
// target has elements of its own.
func children(v *api.Variable) []child {
	var cs []child
	switch v.Kind {
	case reflect.Pointer, reflect.Interface:
		if len(v.Children) == 1 {
			target := v.Children[0]
			if expandable(&target) && target.Kind != reflect.Pointer {
				return children(&target)
			}
			cs = append(cs, child{"*", target})
		}
	case reflect.Map:
		for i := 0; i+1 < len(v.Children); i += 2 {
			cs = append(cs, child{"[" + value(&v.Children[i]) + "]", v.Children[i+1]})
		}
	case reflect.Array, reflect.Slice:
		for i, c := range v.Children {
			cs = append(cs, child{fmt.Sprintf("[%d]", i), c})
		}
	default:
		for _, c := range v.Children {
			cs = append(cs, child{c.Name, c})
		}
	}
	return cs
}

func unloaded(v *api.Variable) int64 {
	switch v.Kind {
	case reflect.Map:
		return v.Len - int64(len(v.Children)/2)
	case reflect.Array, reflect.Slice:
		return v.Len - int64(len(v.Children))
	}
	return 0
}

func expandable(v *api.Variable) bool {
	switch v.Kind {
	case reflect.Struct, reflect.Array, reflect.Slice, reflect.Map:
		return v.Len > 0
	case reflect.Pointer:
		return len(v.Children) == 1 && v.Children[0].Addr != 0
	case reflect.Interface:
		return len(v.Children) == 1 && v.Children[0].Kind != reflect.Invalid
	}
	return false
}

func value(v *api.Variable) string {
	s := []rune(unloadedValue.ReplaceAllString(v.StringWithOptions("", "", api.PrettyShortenType), "{...}"))
	if len(s) > maxValue {
		return string(s[:maxValue]) + "..."
	}
	return string(s)
}
