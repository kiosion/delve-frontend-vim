package session

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-delve/delve/pkg/config"
	"github.com/go-delve/delve/service/rpc2"
)

func TestDebugTest(t *testing.T) {
	dlv := requireDlv(t)
	file, lineOf := source(t, "testdata/fixture/fixture_test.go")
	line := lineOf("breakpoint")

	log := &eventLog{t: t, events: make(chan Event, 1000)}
	s, err := Launch(Config{
		Mode:        "test",
		Dir:         "testdata/fixture",
		Args:        []string{"-run", "^TestShape$"},
		BuildFlags:  []string{"-tags=fixture"},
		Env:         map[string]string{"FIXTURE_ENV": "set"},
		Dlv:         dlv,
		Breakpoints: []Breakpoint{{ID: 1, File: file, Line: line, Cond: "i == 1"}},
	}, log.add)
	if err != nil {
		t.Fatal(err)
	}
	defer stopAndWait(s)

	stop := log.next("stopped")
	if f := stop.Frames[0]; !strings.HasSuffix(f.Function, ".TestShape") || f.File != file || f.Line != line {
		t.Fatalf("stopped in %+v, want TestShape at line %d", f, line)
	}
	got := tree(t, s, stop.Variables, "")
	want := []string{
		`t = ` + valueOf(t, stop.Variables, "t"),
		`s = ` + valueOf(t, stop.Variables, "s"),
		`s.name = "square"`,
		`s.corner = *fixture.point {x: 1, y: 2}`,
		`s.corner.x = 1`,
		`s.corner.y = 2`,
		`s.none = *fixture.point nil`,
		`s.tags = map[string]int ["sides": 4, ]`,
		`s.tags.["sides"] = 4`,
		`s.points = []fixture.point len: 2, cap: 2, [{...},{...}]`,
		`s.points.[0] = fixture.point {x: 0, y: 0}`,
		`s.points.[0].x = 0`,
		`s.points.[0].y = 0`,
		`s.points.[1] = fixture.point {x: 1, y: 1}`,
		`s.points.[1].x = 1`,
		`s.points.[1].y = 1`,
		`s.value = interface {}(fixture.point) {x: 3, y: 4}`,
		`s.value.x = 3`,
		`s.value.y = 4`,
		`n = 1`,
		`env = "set"`,
		`done = ` + valueOf(t, stop.Variables, "done"),
		`started = ` + valueOf(t, stop.Variables, "started"),
		`i = 1`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("variables differ\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	inline := map[int]string{}
	for _, in := range stop.Inline {
		inline[in.Line] = in.Text
	}
	for _, c := range []struct {
		line int
		want string
	}{
		{lineOf("shadow"), "n = 1"},
		{line - 1, "i = 1"},
	} {
		if inline[c.line] != c.want {
			t.Errorf("inline values on line %d are %q, want %q", c.line, inline[c.line], c.want)
		}
	}
	if text := inline[lineOf("inner")]; !strings.HasPrefix(text, "t = ") || strings.Contains(text, "n = ") {
		t.Errorf("inline values on line %d are %q, want t without the shadowed n", lineOf("inner"), text)
	}
	if text := inline[line]; !strings.HasPrefix(text, "t = ") || !strings.HasSuffix(text, `, i = 1, env = "set"`) {
		t.Errorf("inline values on line %d are %q, want t, s, i and env", line, text)
	}

	waiting := slices.IndexFunc(stop.Goroutines, func(g GoroutineGroup) bool { return g.Location.Line == lineOf("waiting") })
	if waiting < 0 || stop.Goroutines[waiting].Total != 2 {
		t.Errorf("no group of 2 goroutines at line %d in %+v", lineOf("waiting"), stop.Goroutines)
	}
	if !slices.ContainsFunc(stop.Goroutines, func(g GoroutineGroup) bool {
		return slices.ContainsFunc(g.Goroutines, func(m Goroutine) bool { return m.ID == stop.Goroutine && m.Status == "running" })
	}) {
		t.Errorf("goroutine %d is not running in %+v", stop.Goroutine, stop.Goroutines)
	}

	if waiting >= 0 {
		g, err := s.Goroutine(stop.Goroutines[waiting].Goroutines[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if f := g.Frames[g.Frame]; f.Line != lineOf("waiting") {
			t.Errorf("selected frame %+v of goroutine %d, want line %d", f, g.Goroutine, lineOf("waiting"))
		}
		if _, err := s.Goroutine(stop.Goroutine); err != nil {
			t.Fatal(err)
		}
	}

	view, err := s.SetWatches([]string{"s.corner.x + i", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Watches) != 2 || view.Watches[0].Value != "2" || !strings.HasPrefix(view.Watches[1].Value, "error: ") {
		t.Errorf("watches are %+v, want 2 and an error", view.Watches)
	}
	if v, err := s.Evaluate("len(s.points)"); err != nil || v.Value != "2" {
		t.Errorf("len(s.points) is %q, %v, want 2", v.Value, err)
	}
	if v, err := s.Evaluate("tag"); err != nil || v.Value != `"on"` {
		t.Errorf("tag is %q, %v, want \"on\" from the build tag", v.Value, err)
	}

	groups, err := s.SetGrouping(Grouping{By: "label", Key: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(groups, func(g GoroutineGroup) bool {
		return g.Label == "worker=waiter" && g.Total == 2 && g.Goroutines[0].Labels["worker"] == "waiter"
	}) {
		t.Errorf("no group of 2 goroutines labeled worker=waiter in %+v", groups)
	}
	if _, err := s.SetGrouping(Grouping{By: "start", ShowRuntime: true}); err != nil {
		t.Fatal(err)
	}

	set, err := s.SetValue(nodeID(t, view.Variables, "n"), "5")
	if err != nil {
		t.Fatal(err)
	}
	if v := valueOf(t, set.Variables, "n"); v != "5" {
		t.Errorf("n is %s after SetValue, want 5", v)
	}

	if err := s.Resume("next"); err != nil {
		t.Fatal(err)
	}
	if stop := log.next("stopped"); stop.Frames[0].Line != line-1 {
		t.Errorf("next stopped at line %d, want %d", stop.Frames[0].Line, line-1)
	}
	if err := s.Resume("continue"); err != nil {
		t.Fatal(err)
	}
	if exit := log.next("exited"); exit.Status != 0 {
		t.Errorf("exit status %d, want 0", exit.Status)
	}
	if !slices.ContainsFunc(log.output, func(l string) bool { return strings.HasPrefix(l, "--- PASS: TestShape") }) {
		t.Errorf("no PASS line in the output:\n%s", strings.Join(log.output, "\n"))
	}
}

// tree returns a "path = value" line for each node, with the top-level variable s
// expanded in full.
func tree(t *testing.T, s *Session, nodes []Node, prefix string) []string {
	t.Helper()
	var lines []string
	for _, n := range nodes {
		path := prefix + n.Name
		lines = append(lines, path+" = "+n.Value)
		if n.Expandable && (prefix != "" || n.Name == "s") {
			children, err := s.Children(n.ID)
			if err != nil {
				t.Fatalf("children of %s: %v", path, err)
			}
			lines = append(lines, tree(t, s, children, path+".")...)
		}
	}
	return lines
}

func valueOf(t *testing.T, nodes []Node, name string) string {
	t.Helper()
	for _, n := range nodes {
		if n.Name == name {
			return n.Value
		}
	}
	t.Fatalf("no variable %s", name)
	return ""
}

func TestAttach(t *testing.T) {
	dlv := requireDlv(t)
	file, lineOf := source(t, "testdata/sleeper/main.go")
	bin := filepath.Join(t.TempDir(), "sleeper")
	if out, err := exec.Command("go", "build", "-gcflags=all=-N -l", "-o", bin, "./testdata/sleeper").CombinedOutput(); err != nil {
		t.Fatalf("build sleeper: %v\n%s", err, out)
	}
	sleeper := exec.Command(bin)
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	exited := make(chan error, 1)
	go func() { exited <- sleeper.Wait() }()

	log := &eventLog{t: t, events: make(chan Event, 1000)}
	s, err := Launch(Config{
		Mode:        "attach",
		PID:         sleeper.Process.Pid,
		Dlv:         dlv,
		Breakpoints: []Breakpoint{{ID: 1, File: file, Line: lineOf("breakpoint")}},
	}, log.add)
	if err != nil {
		t.Fatal(err)
	}
	if f := log.next("stopped").Frames[0]; f.File != file || f.Line != lineOf("breakpoint") {
		t.Errorf("stopped in %+v, want line %d", f, lineOf("breakpoint"))
	}
	s.Stop()
	log.next("exited")
	select {
	case err := <-exited:
		t.Errorf("the program exited after Stop: %v", err)
	case <-time.After(time.Second):
	}
}

func TestConnect(t *testing.T) {
	dlv := requireDlv(t)
	file, lineOf := source(t, "testdata/sleeper/main.go")
	headless := exec.Command(dlv, "debug", "--headless", "--accept-multiclient", "--api-version=2",
		"--listen=127.0.0.1:0", "--output="+filepath.Join(t.TempDir(), "sleeper"), "./testdata/sleeper")
	out, err := headless.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := headless.Start(); err != nil {
		t.Fatal(err)
	}
	defer headless.Process.Kill()
	first, _ := bufio.NewReader(out).ReadString('\n')
	m := listening.FindStringSubmatch(strings.TrimSpace(first))
	if m == nil {
		t.Fatalf("dlv printed %q, want its address", first)
	}

	log := &eventLog{t: t, events: make(chan Event, 1000)}
	s, err := Launch(Config{
		Mode:        "connect",
		Addr:        m[1],
		Breakpoints: []Breakpoint{{ID: 1, File: file, Line: lineOf("breakpoint")}},
	}, log.add)
	if err != nil {
		t.Fatal(err)
	}
	if f := log.next("stopped").Frames[0]; f.File != file || f.Line != lineOf("breakpoint") {
		t.Errorf("stopped in %+v, want line %d", f, lineOf("breakpoint"))
	}
	s.Stop()
	log.next("exited")

	conn, err := net.Dial("tcp", m[1])
	if err != nil {
		t.Fatal(err)
	}
	client := rpc2.NewClientFromConn(conn)
	running := func() bool {
		state, err := client.GetStateNonBlocking()
		return err == nil && state.Running
	}
	deadline := time.Now().Add(time.Minute)
	for !running() {
		if time.Now().After(deadline) {
			t.Fatal("the program is not running after Stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !halt(client, func() bool { return !running() }, time.Minute) {
		t.Fatal("the program did not stop after Halt")
	}
	client.Detach(true)
	headless.Wait()
}

func requireDlv(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and debugs a program in testdata")
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Skip("no dlv in PATH")
	}
	return dlv
}

// source returns the absolute path of the file name, and a function that returns the
// line that ends with the comment "// marker".
func source(t *testing.T, name string) (string, func(marker string) int) {
	t.Helper()
	file, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	return file, func(marker string) int {
		return slices.IndexFunc(lines, func(l string) bool { return strings.HasSuffix(l, "// "+marker) }) + 1
	}
}

type eventLog struct {
	t      *testing.T
	events chan Event
	output []string
}

func (l *eventLog) add(e Event) { l.events <- e }

// next returns the next event of the given kind. It records output events, and fails
// on events of other kinds.
func (l *eventLog) next(kind string) Event {
	l.t.Helper()
	timeout := time.After(time.Minute)
	for {
		select {
		case e := <-l.events:
			switch e.Event {
			case kind:
				return e
			case "output":
				l.output = append(l.output, e.Text)
			default:
				l.t.Fatalf("got event %+v while waiting for %s; output:\n%s", e, kind, strings.Join(l.output, "\n"))
			}
		case <-timeout:
			l.t.Fatalf("no %s event; output:\n%s", kind, strings.Join(l.output, "\n"))
		}
	}
}

func TestRunningProgram(t *testing.T) {
	dlv := requireDlv(t)
	file, lineOf := source(t, "testdata/sleeper/main.go")
	log := &eventLog{t: t, events: make(chan Event, 1000)}
	s, err := Launch(Config{Mode: "debug", Dir: "testdata/sleeper", Dlv: dlv}, log.add)
	if err != nil {
		t.Fatal(err)
	}
	defer stopAndWait(s)
	deadline := time.Now().Add(time.Minute)
	for s.Pause() != nil {
		if time.Now().After(deadline) {
			t.Fatal("the program did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if stop := log.next("stopped"); strings.HasPrefix(stop.Frames[stop.Frame].Function, "runtime.") {
		t.Errorf("paused in frame %+v, want a frame outside the runtime", stop.Frames[stop.Frame])
	}

	line := lineOf("breakpoint")
	bp := Breakpoint{ID: 1, File: file, Line: line}
	if err := s.Resume("continue"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBreakpoint(bp); err != nil {
		t.Fatal(err)
	}
	if f := log.next("stopped").Frames[0]; f.File != file || f.Line != line {
		t.Errorf("stopped in %+v, want line %d", f, line)
	}

	if err := s.ClearBreakpoint(bp.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume("next"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBreakpoint(bp); err != nil {
		t.Fatal(err)
	}
	if f := log.next("stopped").Frames[0]; f.Line != line-1 {
		t.Errorf("next with a breakpoint change stopped at line %d, want %d", f.Line, line-1)
	}

	if err := s.ClearBreakpoint(bp.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume("continue"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBreakpoint(bp); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(); err != nil {
		t.Fatal(err)
	}
	log.next("stopped")
	if err := s.Resume("continue"); err != nil {
		t.Fatal(err)
	}
	if f := log.next("stopped").Frames[0]; f.File != file || f.Line != line {
		t.Errorf("continue after Pause with a breakpoint change stopped in %+v, want line %d", f, line)
	}
}

func TestBuildFlags(t *testing.T) {
	flags := []string{"-tags=a b", `-ldflags=-X 'main.v=it's'`, `a\b`, ""}
	if got := config.SplitQuotedFields(buildFlags(flags), '\''); !slices.Equal(got, flags) {
		t.Errorf("dlv splits %q into %q, want %q", buildFlags(flags), got, flags)
	}
}

func stopAndWait(s *Session) {
	s.Stop()
	s.Wait(10 * time.Second)
}

func nodeID(t *testing.T, nodes []Node, name string) int {
	t.Helper()
	for _, n := range nodes {
		if n.Name == name {
			return n.ID
		}
	}
	t.Fatalf("no variable %s", name)
	return 0
}
