// Command delve-frontend is the helper process of the delve-frontend Vim plugin. Vim
// starts it as a job and exchanges messages with it over stdin and stdout in Vim's JSON
// channel mode.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kiosion/delve-frontend-vim/internal/locate"
	"github.com/kiosion/delve-frontend-vim/internal/process"
	"github.com/kiosion/delve-frontend-vim/internal/session"
)

func main() {
	// SIGPIPE from a write to a closed stdout would kill the helper before it stops the
	// session.
	signal.Ignore(syscall.SIGPIPE)
	if err := serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
}

type server struct {
	mu  sync.Mutex
	enc *json.Encoder
	// sessionMu guards the assignment of session, which the signal handler reads.
	sessionMu sync.Mutex
	session   *session.Session
}

// serve answers each [id, request] message from r with an [id, response] message on w,
// until r ends. Events from the debug session go to w as [0, event] messages.
func serve(r io.Reader, w io.Writer) error {
	s := &server{enc: json.NewEncoder(w)}
	defer s.stopSession()
	// Vim sends SIGTERM to its jobs when it exits.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer signal.Stop(signals)
	go func() {
		<-signals
		s.stopSession()
		os.Exit(0)
	}()
	dec := json.NewDecoder(r)
	for {
		var msg [2]json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read message: %w", err)
		}
		var id int
		var req request
		if err := json.Unmarshal(msg[0], &id); err != nil {
			return fmt.Errorf("read message id: %w", err)
		}
		resp := response{}
		if err := json.Unmarshal(msg[1], &req); err != nil {
			resp.Error = fmt.Sprintf("read request: %v", err)
		} else if result, err := s.handle(req); err != nil {
			resp.Error = err.Error()
		} else {
			resp.Result = result
		}
		if err := s.send(id, resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
}

func (s *server) send(id int, msg any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode([]any{id, msg})
}

type params struct {
	session.Breakpoint
	// Index is the frame index of a frame request.
	Index int `json:"index"`
	// Goroutine is the goroutine ID of a goroutine request.
	Goroutine int64 `json:"goroutine"`
	// Expr is the expression of an evaluate request.
	Expr string `json:"expr"`
	// Watches are the expressions of a watches request.
	Watches []string `json:"watches"`
	// Grouping is the goroutine grouping of a grouping request.
	Grouping session.Grouping `json:"grouping"`
	// Value is the Go expression that a set request assigns to the variable of node ID.
	Value string `json:"value"`
	// Path and Text are the file path and buffer text of a locate request.
	Path string `json:"path"`
	Text string `json:"text"`
}

func (s *server) handle(req request) (any, error) {
	if req.Method == "launch" {
		// Vim numbers each launch, and ignores the events of earlier sessions.
		var p struct {
			session.Config
			Session int `json:"session"`
			// ArgLine is the program arguments as one line, which splitArgs splits as a
			// POSIX shell does. It replaces Args when set.
			ArgLine string `json:"arg_line"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("read launch params: %w", err)
		}
		if p.ArgLine != "" {
			args, err := splitArgs(p.ArgLine)
			if err != nil {
				return nil, err
			}
			p.Args = args
		}
		s.sessionMu.Lock()
		defer s.sessionMu.Unlock()
		if s.session != nil {
			s.session.Stop()
		}
		var err error
		s.session, err = session.Launch(p.Config, func(event session.Event) {
			s.send(0, struct {
				session.Event
				Session int `json:"session"`
			}{event, p.Session})
		})
		return nil, err
	}
	var p params
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("read %s params: %w", req.Method, err)
		}
	}
	switch req.Method {
	case "locate":
		tests, err := locate.File(p.Path, []byte(p.Text))
		if tests == nil {
			tests = []locate.Test{}
		}
		return tests, err
	case "processes":
		processes, err := process.List()
		if processes == nil {
			processes = []process.Process{}
		}
		return processes, err
	case "breakpoint", "clear":
		// Vim keeps the breakpoints and sends them all with the next launch.
		if s.session == nil {
			return nil, nil
		}
		if req.Method == "clear" {
			return nil, s.session.ClearBreakpoint(p.ID)
		}
		return nil, s.session.SetBreakpoint(p.Breakpoint)
	case "watches":
		// Vim keeps the watches and sends them with the next launch.
		if s.session == nil {
			return nil, nil
		}
		return s.session.SetWatches(p.Watches)
	case "grouping":
		// Vim keeps the grouping and sends it with the next launch.
		if s.session == nil {
			return nil, nil
		}
		return s.session.SetGrouping(p.Grouping)
	case "continue", "next", "step", "stepout", "pause", "stop", "frame", "children", "goroutine", "evaluate", "set":
		if s.session == nil {
			return nil, errors.New("no debug session")
		}
	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
	switch req.Method {
	case "pause":
		return nil, s.session.Pause()
	case "stop":
		s.session.Stop()
		return nil, nil
	case "frame":
		return s.session.Frame(p.Index)
	case "children":
		return s.session.Children(p.ID)
	case "goroutine":
		return s.session.Goroutine(p.Goroutine)
	case "evaluate":
		return s.session.Evaluate(p.Expr)
	case "set":
		return s.session.SetValue(p.ID, p.Value)
	}
	return nil, s.session.Resume(req.Method)
}

func (s *server) stopSession() {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.session != nil {
		s.session.Stop()
		s.session.Wait(5 * time.Second)
	}
}

// splitArgs splits line into words as a POSIX shell does, without expansions. Single
// quotes keep every character, double quotes keep all but a backslash before " or \,
// and an unquoted backslash keeps the next character.
func splitArgs(line string) ([]string, error) {
	var args []string
	var word strings.Builder
	inWord, quote := false, rune(0)
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'' && r == '\'', quote == '"' && r == '"':
			quote = 0
		case quote == '\'':
			word.WriteRune(r)
		case r == '\\' && i+1 < len(runes) && (quote == 0 || runes[i+1] == '"' || runes[i+1] == '\\'):
			i++
			word.WriteRune(runes[i])
			inWord = true
		case quote == '"':
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				args = append(args, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote in %q", quote, line)
	}
	if inWord {
		args = append(args, word.String())
	}
	return args, nil
}
