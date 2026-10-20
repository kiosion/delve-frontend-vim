# delve-frontend-vim

delve-frontend-vim runs and debugs Go tests from Vim 9. It marks every test, subtest
and table case in a `_test.go` file with a sign, runs the one at the cursor with
`go test`, and debugs it with [dlv](https://github.com/go-delve/delve) in a tab of its
own.

<!-- Screenshot: the debug tab stopped at a breakpoint, showing inline values, the
goroutines pane and an expanded variable. -->

The plugin selects a single table case, including a case named by a struct field:

```go
func TestParse(t *testing.T) {
	tests := []struct {
		name string
		in   int
	}{
		{name: "empty input", in: 0}, // runs -run '^TestParse$/^empty_input$'
		{"negative", -1},             // runs -run '^TestParse$/^negative$'
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { ... })
	}
}
```

A case whose name the test computes at run time, for example with `fmt.Sprintf`, gets
no sign. With the cursor in such a case, the plugin runs the parent test and all its
cases.

A debug session opens in a tab of its own. The plugin puts the source of the selected
frame at the top of the tab, and adds each variable's value at the end of the lines
that use it. Below the source, it shows the call stack, the goroutines grouped by
location, watch expressions, an expandable tree of variables, and the program output.
Breakpoints and watches persist across Vim restarts.

The plugin needs only Go and dlv. A Go helper, which the plugin builds on first use,
finds the tests and drives dlv through its JSON-RPC API.

## Requirements

- Vim 9.1.0142 or later. Neovim is not supported. In Neovim and older versions of
  Vim, the plugin does not load and prints no message.
- macOS or Linux. Only macOS is tested.
- Go 1.25 or later in `PATH`.
- dlv in `PATH`, for debug sessions. Running tests works without it.
- Network access and write access to the plugin directory on first use, to build the
  helper.

## Installation

With [vim-plug](https://github.com/junegunn/vim-plug):

```vim
Plug 'kiosion/delve-frontend-vim'
```

With [minpac](https://github.com/k-takata/minpac):

```vim
call minpac#add('kiosion/delve-frontend-vim')
```

With Vim's built-in packages:

```sh
git clone https://github.com/kiosion/delve-frontend-vim ~/.vim/pack/plugins/start/delve-frontend-vim
vim -c 'helptags ~/.vim/pack/plugins/start/delve-frontend-vim/doc' -c q
```

Plugin managers need no build hook. On first use, and on the first use after an update
changes the helper's source, the plugin compiles the helper to `bin/delve-frontend` in
the plugin directory.

### First use

1. Open a `_test.go` file. Vim prints:

   ```
   delve-frontend: building the helper
   ```

   The first build downloads delve and the other Go modules the helper depends on.
   Vim stays usable while the build runs.

2. Wait for the build to finish. Vim prints:

   ```
   delve-frontend: built the helper
   ```

   A `▶` sign marks each test in the file.

When the build fails, Vim prints `delve-frontend: building the helper failed:` and the
output of `go build`. See [Troubleshooting](#troubleshooting).

## Quick start

The plugin defines no mappings. Add some to your vimrc, for example:

```vim
nmap <Leader>tt <Plug>(delve-frontend-run-test)
nmap <Leader>td <Plug>(delve-frontend-debug-test)
nmap <Leader>db <Plug>(delve-frontend-toggle-breakpoint)
nmap <Leader>dc <Plug>(delve-frontend-continue)
nmap <Leader>dn <Plug>(delve-frontend-next)
nmap <Leader>ds <Plug>(delve-frontend-step)
nmap <Leader>do <Plug>(delve-frontend-step-out)
nmap <Leader>dq <Plug>(delve-frontend-stop)
nmap <Leader>de <Plug>(delve-frontend-evaluate)
xmap <Leader>de <Plug>(delve-frontend-evaluate)
```

To run one table case:

1. Move the cursor into the case.
2. Run `:DelveFrontendRunTest`. A terminal window opens at the bottom and runs
   `go test -v` in the directory of the file. For the `negative` case above, it runs
   `go test -v -run '^TestParse$/^negative$' .`.

To debug it:

1. On a line in the test, run `:DelveFrontendToggleBreakpoint`. A `●` sign marks the line.
2. With the cursor in the case, run `:DelveFrontendDebugTest`. A debug tab opens, and
   dlv builds the test and runs it to the breakpoint.
3. Step with `:DelveFrontendNext`, `:DelveFrontendStep` and `:DelveFrontendStepOut`, or
   run `:DelveFrontendContinue` to go to the next breakpoint.
4. Run `:DelveFrontendStop`, or close the tab, to end the session.

A session without breakpoints runs the program to its end. To pause a running program,
run `:DelveFrontendPause`.

The plugin also debugs programs other than tests. `:DelveFrontendDebug [args]` debugs
the main package in the directory of the current file, `:DelveFrontendAttach [pid]`
attaches to a running Go process, and `:DelveFrontendConnect {host:port}` connects to
a headless dlv.

## Documentation

`:help delve-frontend` documents each command, mapping, option and highlight group,
the panes of the debug tab, how each kind of session ends, and the rules the helper
uses to find tests.

## Troubleshooting

### No signs appear in a test file

Run `:messages` and look for a line starting with `delve-frontend:`.

After `building the helper failed`, Vim prints the error from `go build`. The usual
causes are a Go version older than 1.25, a network that blocks the module download,
and a read-only plugin directory. After a failed build, the plugin makes no further attempt
until Vim restarts. Fix the cause, then restart Vim.

For a read-only plugin directory, build the helper elsewhere and point the plugin at
it:

```sh
cd /path/to/delve-frontend-vim
go build -o ~/.local/bin/delve-frontend ./cmd/delve-frontend
```

```vim
let g:delve_frontend_helper = expand('~/.local/bin/delve-frontend')
```

The plugin does not rebuild a helper at a path you set. Build it again after each
update.

`no helper at` means that `g:delve_frontend_helper` names a file that is missing or not
executable.

### Debugging fails with `executable file not found`

When Vim cannot find dlv, the plugin prints this error in the frames pane:

```
start dlv: exec: "dlv": executable file not found in $PATH
```

Install dlv with `go install github.com/go-delve/delve/cmd/dlv@latest`, and add
`$(go env GOPATH)/bin` to the `PATH` that Vim starts with. Or set `g:delve_frontend_dlv`
to the full path of dlv.

### Other session problems

`:help delve-frontend-troubleshooting` covers sessions that exit at once, attach
failures on Linux, processes missing from the attach menu, and breakpoints that a
remote dlv ignores.

## Contributing

Report bugs in the [issue tracker](https://github.com/kiosion/delve-frontend-vim/issues).
Include the output of `vim --version`, `go version` and `dlv version`, and the
`delve-frontend:` lines from `:messages`.

Run the tests from the repository root:

```sh
go test ./...
```

The `internal/session` tests debug the programs in `internal/session/testdata` with
dlv, and Go skips them when dlv is not in `PATH`. `go test -short ./...` skips them
too, and the locator test that builds the fixture package. The `internal/process`
tests build and start small programs in either mode.

To add a case to test discovery, add it to
`internal/locate/testdata/fixture/fixture_test.go` with a `// want: <test name>`
comment on its first line. The locator tests check that the helper finds exactly the
tests these comments name, and that each test's flags run it.

## License

[MIT](LICENSE)
