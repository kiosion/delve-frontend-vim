# delve-frontend-vim

Personal (hacked-together) Vim 9 front-end for
[dlv](https://github.com/go-delve/delve)-backed Go test running and debugging.
Integrates (ish) with ALE/CoC; provides interactive debugging with breakpoints,
stack traces, goroutine groupings and variable inspection.

<!-- TODO: screenshot; debug w/ goroutines pane and expanded var or something -->

Selection supports single table-cases and cases named via struct fields:

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

Cases with runtime-computed names (e.g., using `fmt.Sprintf`) don't get Run
symbols/code actions. Running invokes their parent and its subtests.

Debug sessions spawn their own tab with split layout; close or call
`:DelveFrontendStop` to kill the session.

Programs besides tests may be debugged. `:DelveFrontendDebug [args]` debugs
the main package from current buffer's dir, `:DelveFrontendAttach [pid]`
attaches to a supported running Go process, and
`:DelveFrontendConnect {host:post}` connents to a headless `dlv` instance.

## Requirements

- Vim >= 9.1
- Go >= 1.25
- `dlv` required for debug; via mise or go

## Installation

[vim-plug](https://github.com/junegunn/vim-plug):

1. Add to vimrc:

   ```vim
   Plug 'kiosion/delve-frontend-vim'
   ```

2. Restart Vim and run `:PlugInstall`.

[minpac](https://github.com/k-takata/minpac):

1. Add to vimrc:

   ```vim
   call minpac#add('kiosion/delve-frontend-vim')
   ```

2. Restart Vim and run `:call minpac#update()`.

w/ Vim's built-in packages:

```sh
git clone https://github.com/kiosion/delve-frontend-vim ~/.vim/pack/plugins/start/delve-frontend-vim
vim -c 'helptags ~/.vim/pack/plugins/start/delve-frontend-vim/doc' -c q
```

First use builds its Go helper in Vim's plugin dir, under `bin/delve-frontend`.

## Key mappings

No default mappings are set. Set desired `<Plug>` mappings in vimrc:

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

`:help delve-frontend-mappings` lists all

## Configuration

Set build flags and environment variables for tests in vimrc:

```vim
let g:delve_frontend_build_flags = ['-tags=e2e']
let g:delve_frontend_env = {'DB_URI': 'postgres://localhost/test'}
```

## License

[MIT](LICENSE)
