if has('nvim') || !has('patch-9.1.0142')
  finish
endif

vim9script

if exists('g:loaded_delve_frontend')
  finish
endif
g:loaded_delve_frontend = 1

highlight default link DelveFrontendTest Special
highlight default link DelveFrontendBreakpoint ErrorMsg
highlight default link DelveFrontendCurrentLineSign WarningMsg
highlight default link DelveFrontendCurrentLine CursorLine
highlight default link DelveFrontendInline Comment
prop_type_add('DelveFrontendInline', {highlight: 'DelveFrontendInline'})
sign_define('DelveFrontendTest', {
  text: get(g:, 'delve_frontend_test_sign', "▶"),
  texthl: 'DelveFrontendTest',
})
sign_define('DelveFrontendBreakpoint', {text: "●", texthl: 'DelveFrontendBreakpoint'})
sign_define('DelveFrontendCondition', {text: "◆", texthl: 'DelveFrontendBreakpoint'})
sign_define('DelveFrontendCurrentLine', {text: "→", texthl: 'DelveFrontendCurrentLineSign', linehl: 'DelveFrontendCurrentLine'})

command DelveFrontendRunTest delve_frontend#RunTest()
command DelveFrontendDebugTest delve_frontend#DebugTest()
command -nargs=* DelveFrontendDebug delve_frontend#Debug(<q-args>)
command -nargs=? DelveFrontendAttach delve_frontend#Attach(<q-args>)
command -nargs=1 DelveFrontendConnect delve_frontend#Connect(<q-args>)
command DelveFrontendBreakpoint delve_frontend#ToggleBreakpoint()
command DelveFrontendConditionalBreakpoint delve_frontend#ConditionalBreakpoint()
command DelveFrontendContinue delve_frontend#Command('continue')
command DelveFrontendNext delve_frontend#Command('next')
command DelveFrontendStep delve_frontend#Command('step')
command DelveFrontendStepOut delve_frontend#Command('stepout')
command DelveFrontendPause delve_frontend#Command('pause')
command DelveFrontendStop delve_frontend#Command('stop')
command DelveFrontendRestart delve_frontend#Restart()
command DelveFrontendBreakpoints delve_frontend#ListBreakpoints()
command -nargs=* DelveFrontendEvaluate delve_frontend#Evaluate(<q-args>)
command -nargs=* DelveFrontendWatch delve_frontend#AddWatch(<q-args>)

nnoremap <Plug>(delve-frontend-run-test) <Cmd>DelveFrontendRunTest<CR>
nnoremap <Plug>(delve-frontend-debug-test) <Cmd>DelveFrontendDebugTest<CR>
nnoremap <Plug>(delve-frontend-breakpoint) <Cmd>DelveFrontendBreakpoint<CR>
nnoremap <Plug>(delve-frontend-conditional-breakpoint) <Cmd>DelveFrontendConditionalBreakpoint<CR>
nnoremap <Plug>(delve-frontend-continue) <Cmd>DelveFrontendContinue<CR>
nnoremap <Plug>(delve-frontend-next) <Cmd>DelveFrontendNext<CR>
nnoremap <Plug>(delve-frontend-step) <Cmd>DelveFrontendStep<CR>
nnoremap <Plug>(delve-frontend-step-out) <Cmd>DelveFrontendStepOut<CR>
nnoremap <Plug>(delve-frontend-pause) <Cmd>DelveFrontendPause<CR>
nnoremap <Plug>(delve-frontend-stop) <Cmd>DelveFrontendStop<CR>
nnoremap <Plug>(delve-frontend-restart) <Cmd>DelveFrontendRestart<CR>
nnoremap <Plug>(delve-frontend-breakpoints) <Cmd>DelveFrontendBreakpoints<CR>
nnoremap <Plug>(delve-frontend-evaluate) <Cmd>DelveFrontendEvaluate<CR>
xnoremap <Plug>(delve-frontend-evaluate) <Cmd>DelveFrontendEvaluate<CR>
nnoremap <Plug>(delve-frontend-watch) <Cmd>DelveFrontendWatch<CR>
xnoremap <Plug>(delve-frontend-watch) <Cmd>DelveFrontendWatch<CR>

augroup delve_frontend
  autocmd!
  autocmd BufEnter,TextChanged,InsertLeave *_test.go delve_frontend#Refresh(expand('<abuf>')->str2nr())
  autocmd BufReadPost *.go delve_frontend#RestoreBreakpoints(expand('<abuf>')->str2nr())
  autocmd BufWritePost *.go delve_frontend#SaveState()
augroup END
