vim9script

const root = expand('<sfile>:p:h:h')
const builtHelper = root .. '/bin/delve-frontend'
const breakpointGroup = 'delve_frontend_breakpoint'
const stopGroup = 'delve_frontend_stop'

var channel: channel = null_channel
# helperState is 'building' while a helper build runs, and 'failed' after the plugin
# reported a failed build or a missing helper.
var helperState = ''
# testRuns numbers each test run. Output of a run that a newer run replaced is dropped.
var testRuns = 0
var testJob: job = null_job

# The debug session. launches numbers each launch, and session is the number of the
# current one, or 0 when no program is being debugged.
var launches = 0
var session = 0
# generation changes on each launch, stop, resume and session end. Replies requested
# before the change are dropped.
var generation = 0
# Window IDs of the debug tab, by pane: source, frames, goroutines, variables, output.
var tab: dict<number> = {}
var buffers: dict<number> = {}
var frames: list<dict<any>> = []
# The buffer of the selected frame, which shows the inline values.
var frameBuffer = -1
# The goroutine grouping that the helper applies at each stop.
var grouping = {by: 'user', key: '', show_runtime: false}
const groupingOrder = ['user', 'current', 'go', 'start', 'label']
const groupingNames = {
  user: 'location in your code',
  current: 'current location',
  go: 'go statement',
  start: 'start function',
  label: 'label',
}
# The goroutine groups of the last stop, the selected goroutine, and the groups that
# the user collapsed or expanded, by header.
var goroutineGroups: list<dict<any>> = []
var selectedGoroutine = 0
var collapsed: dict<bool> = {}
# One entry per line of the goroutines pane: {id} for a goroutine, {group, closed} for
# a group header, or {}.
var goroutineLines: list<dict<any>> = []
var lastLaunch: dict<any> = {}
# One node per line of the variables pane.
var nodes: list<dict<any>> = []
# Conditions of the conditional breakpoints, by sign ID.
var conditions: dict<string> = {}
# Sign IDs of the breakpoints sent to the helper in the current session.
var sent: dict<bool> = {}
# Sign IDs of the breakpoints sent since the last stop. The helper sets a breakpoint
# sent while the program runs when the program next stops, and reports a rejection as
# an error event.
var pending: dict<bool> = {}
var watches: list<any> = []
# The breakpoints and watches kept across Vim restarts. saved holds the breakpoints of
# each file by path, as lists of dicts with the keys line and cond, and savedWatches the
# watches, as this Vim last read or wrote them.
const stateFile = has_key(g:, 'delve_frontend_state_file') ? expand(g:delve_frontend_state_file)
  : (empty($XDG_STATE_HOME) ? expand('~/.local/state') : $XDG_STATE_HOME) .. '/delve-frontend/state.json'
var saved: dict<any> = {}
var savedWatches: list<any> = []
# stateCorrupt is true after the state file failed to decode. The plugin then keeps the
# file and saves nothing.
var stateCorrupt = false
var stateWriteWarned = false

# Channel returns the channel of the helper, and starts the helper when it is not
# running. It returns null_channel while a helper build runs or after a failure, and
# warns unless quiet is true. It also returns null_channel when it starts a build or
# finds no executable helper, and prints a message in both cases.
def Channel(quiet = false): channel
  if ch_status(channel) == 'open'
    return channel
  endif
  if !empty(helperState)
    if !quiet
      Warn(HelperProblem())
    endif
    return null_channel
  endif
  const helper = has_key(g:, 'delve_frontend_helper') ? expand(g:delve_frontend_helper) : builtHelper
  if helper == builtHelper && HelperStale()
    BuildHelper()
    return null_channel
  endif
  if !executable(helper)
    helperState = 'failed'
    Warn($'g:delve_frontend_helper: {helper} is missing or not executable')
    return null_channel
  endif
  # Vim can call exit_cb before the last output, and close_cb before the exit.
  final run = {stderr: [], status: -1, closed: false}
  const job = job_start([helper], {
    mode: 'json',
    callback: (_, msg) => Notify(msg),
    err_mode: 'nl',
    err_cb: (_, msg) => run.stderr->add(msg),
    exit_cb: (_, status) => HelperExited(extend(run, {status: status})),
    close_cb: (_) => HelperExited(extend(run, {closed: true})),
  })
  channel = job_getchannel(job)
  return channel
enddef

# HelperProblem returns why the helper cannot answer requests, or an empty string.
def HelperProblem(): string
  return helperState == 'building' ? 'the helper build is still running'
    : helperState == 'failed' ? 'the helper is unavailable (see :messages)' : ''
enddef

def HelperExited(run: dict<any>)
  if run.status < 0 || !run.closed
    return
  endif
  if session != 0
    EndSession($'The helper exited with status {run.status}')
  endif
  Warn($'the helper exited with status {run.status}' .. (empty(run.stderr) ? '' : ': ' .. join(run.stderr, ' ')))
enddef

# HelperStale reports whether the built helper is missing or older than its source.
def HelperStale(): bool
  const built = getftime(builtHelper)
  return built < 0 || globpath(root, '**/*.go', true, true)
    ->filter((_, f) => f !~ '/testdata/')
    ->extend([root .. '/go.mod', root .. '/go.sum'])
    ->indexof((_, f) => getftime(f) > built) >= 0
enddef

def BuildHelper()
  helperState = 'building'
  echomsg 'delve-frontend: building the helper'
  # Vim can call exit_cb before the last output, and close_cb before the exit.
  final build = {output: [], status: -1, closed: false}
  job_start(get(g:, 'delve_frontend_go', ['go']) + ['build', '-o', builtHelper, './cmd/delve-frontend'], {
    cwd: root,
    callback: (_, msg) => build.output->add(msg),
    exit_cb: (_, status) => HelperBuilt(extend(build, {status: status})),
    close_cb: (_) => HelperBuilt(extend(build, {closed: true})),
  })
enddef

def HelperBuilt(build: dict<any>)
  if build.status < 0 || !build.closed
    return
  endif
  if build.status != 0
    helperState = 'failed'
    Warn($'building the helper failed: {join(build.output, ' ')}')
    return
  endif
  helperState = ''
  echomsg 'delve-frontend: built the helper'
  if expand('%') =~ '_test\.go$'
    Refresh(bufnr())
  endif
enddef

def Warn(msg: string)
  echohl WarningMsg
  echomsg $'delve-frontend: {msg}'
  echohl None
enddef

# Request sends method to the helper, and passes the result to Done.
def Request(method: string, params: dict<any>, Done: func(any) = null_function)
  const ch = Channel()
  if ch_status(ch) != 'open'
    return
  endif
  ch_sendexpr(ch, {method: method, params: params}, {callback: (_, resp) => {
    if !empty(get(resp, 'error', ''))
      Warn(resp.error)
    elseif Done != null_function
      Done(resp.result)
    endif
  }})
enddef

# Refresh asks the helper for the tests in buffer buf and marks each one in the sign
# column.
export def Refresh(buf: number)
  const ch = Channel(true)
  if ch_status(ch) != 'open'
    return
  endif
  const tick = getbufvar(buf, 'changedtick')
  ch_sendexpr(ch, {method: 'locate', params: {
    path: fnamemodify(bufname(buf), ':p'),
    text: join(getbufline(buf, 1, '$'), "\n"),
  }}, {callback: (_, resp) => Located(buf, tick, resp)})
enddef

def Located(buf: number, tick: number, resp: dict<any>)
  # A reply for an older version of the buffer is dropped, because TextChanged or
  # InsertLeave requests the newer one.
  if !bufexists(buf) || getbufvar(buf, 'changedtick') != tick || !empty(get(resp, 'error', ''))
    return
  endif
  setbufvar(buf, 'delve_frontend_tests', resp.result)
  PlaceTestSigns(buf)
enddef

# PlaceTestSigns marks each test in buffer buf with the result of its last run, or with
# the test sign when it has not run.
def PlaceTestSigns(buf: number)
  const results = getbufvar(buf, 'delve_frontend_results', {})
  const signs = {pass: 'DelveFrontendTestPassed', fail: 'DelveFrontendTestFailed'}
  sign_unplace('delve_frontend', {buffer: buf})
  const priority = get(g:, 'delve_frontend_sign_priority', 5)
  sign_placelist(getbufvar(buf, 'delve_frontend_tests', [])->mapnew((_, test) => ({
    buffer: buf,
    group: 'delve_frontend',
    name: get(signs, get(results, test.name, ''), 'DelveFrontendTest'),
    lnum: test.line,
    priority: priority,
  })))
enddef

# TestAtCursor returns the innermost test, subtest or table case that spans the cursor
# line, or an empty dict when there is none. Each test has the keys name, line,
# end_line and args.
export def TestAtCursor(): dict<any>
  const lnum = line('.')
  var best = {}
  for test in get(b:, 'delve_frontend_tests', [])
    if test.line <= lnum && lnum <= test.end_line
        && (empty(best) || test.end_line - test.line <= best.end_line - best.line)
      best = test
    endif
  endfor
  return best
enddef

# RunTest runs the test at the cursor with go test, and shows its output in the results
# window at the bottom. When the run ends, the window shows a summary and the output of
# each failed test above the full output, and the sign of each test that ran shows its
# result. A run started while another runs stops the other.
export def RunTest()
  const test = TestAtCursor()
  if empty(test)
    Warn(HelperProblem() ?? 'no test at the cursor')
    return
  endif
  const dir = expand('%:p:h')
  const env = Setting('delve_frontend_env', {})
  const cmd = get(g:, 'delve_frontend_go', ['go']) + ['test', '-json']
    + Setting('delve_frontend_build_flags', []) + test.args + ['.']
  if job_status(testJob) == 'run'
    job_stop(testJob)
  endif
  testRuns += 1
  final run = {
    id: testRuns,
    test: test,
    buf: bufnr(),
    package: '',
    results: {},
    elapsed: {},
    failed: [],
    outputs: {},
    unparsed: [],
    lines: [],
  }
  ShowResults([$'Running {test.name}', ''])
  testJob = job_start(cmd, {
    cwd: dir,
    env: env,
    callback: (_, line) => TestEvent(run, line),
    close_cb: (_) => TestFinished(run),
  })
  if job_status(testJob) == 'fail'
    SetLines('results', [$'Cannot run {join(cmd)}'])
  endif
enddef

# TestEvent records one line of go test -json output. Lines that are not test events,
# such as compiler errors, are kept as they are.
def TestEvent(run: dict<any>, line: string)
  if run.id != testRuns
    return
  endif
  var event: any
  try
    event = json_decode(line)
  catch
  endtry
  if type(event) != v:t_dict || !has_key(event, 'Action')
    run.unparsed->add(line)
    run.lines->add(line)
    Append('results', line)
    return
  endif
  const name = get(event, 'Test', '')
  if event.Action == 'output' || event.Action == 'build-output'
    const text = substitute(event.Output, '\n$', '', '')
    if !empty(name)
      run.outputs[name] = get(run.outputs, name, [])->add(text)
    elseif event.Action == 'build-output'
      run.unparsed->add(text)
    endif
    run.lines->add(text)
    Append('results', text)
  elseif index(['pass', 'fail', 'skip'], event.Action) < 0
    return
  elseif empty(name)
    run.package = event.Action
  else
    run.results[name] = event.Action
    run.elapsed[name] = get(event, 'Elapsed', 0.0)
    if event.Action == 'fail'
      run.failed->add(name)
    endif
  endif
enddef

def TestFinished(run: dict<any>)
  if run.id != testRuns
    return
  endif
  const verdict = run.package == 'pass' ? 'PASS' : 'FAIL'
  var header = $'{verdict}  {run.test.name}'
  if has_key(run.elapsed, run.test.name)
    header ..= printf('  (%.2fs)', run.elapsed[run.test.name])
  endif
  const results = values(run.results)
  var lines = [header, printf('%d passed, %d failed, %d skipped',
    results->count('pass'), results->count('fail'), results->count('skip')), '']
  # A parent test fails with its subtests, so only the innermost failures have output
  # worth showing.
  for name in run.failed
    if run.failed->indexof((_, other) => stridx(other, name .. '/') == 0) < 0
      lines += get(run.outputs, name, [])->copy()->filter((_, l) => l !~ '^=== \u\+ ') + ['']
    endif
  endfor
  if verdict == 'FAIL' && empty(run.failed) && !empty(run.unparsed)
    lines += run.unparsed + ['']
  endif
  SetLines('results', lines + ['Output', ''] + run.lines)
  echo header
  if bufexists(run.buf)
    setbufvar(run.buf, 'delve_frontend_results',
      extend(getbufvar(run.buf, 'delve_frontend_results', {}), run.results))
    PlaceTestSigns(run.buf)
  endif
enddef

# ShowResults opens the results window at the bottom of the current tab page unless it
# is open there, and replaces its text with lines. The cursor stays in its window.
def ShowResults(lines: list<string>)
  const buf = ScratchBuffer('results')
  if win_findbuf(buf)->indexof((_, win) => win_id2tabwin(win)[0] == tabpagenr()) < 0
    const win = win_getid()
    execute $'botright :{get(g:, 'delve_frontend_results_rows', 15)}split'
    execute 'buffer' buf
    setlocal nonumber norelativenumber signcolumn=no foldcolumn=0 nowrap nolist nospell winfixheight
    syntax clear
    syntax match DelveFrontendTestPassed /^\s*\(--- \)\=\(PASS\|ok\)\>.*/
    syntax match DelveFrontendTestFailed /^\s*\(--- \)\=\(FAIL\|panic:\).*/
    win_gotoid(win)
  endif
  SetLines('results', lines)
enddef

def Setting(name: string, default: any): any
  return get(b:, name, get(g:, name, default))
enddef

# DebugTest debugs the test at the cursor in the debug tab.
export def DebugTest()
  const test = TestAtCursor()
  if empty(test)
    Warn(HelperProblem() ?? 'no test at the cursor')
    return
  endif
  Launch('test', test.args, $'Building {test.name}')
enddef

# Debug debugs the main package in the directory of the current file, and passes it
# the arguments in args.
export def Debug(args: string)
  Launch('debug', [], 'Building', {arg_line: args})
enddef

# Restart repeats the last launch, with its directory, build flags and environment, and
# with the current breakpoints and watches.
export def Restart()
  if empty(lastLaunch)
    Warn('no session to restart')
    return
  endif
  Launch(lastLaunch.mode, lastLaunch.args, lastLaunch.title, lastLaunch.extra)
enddef

# Attach attaches to the running process with ID pid. When pid is empty, it lists the
# Go processes of the user in a menu, and attaches to the chosen one.
export def Attach(pid: string)
  if !empty(pid)
    Launch('attach', [], $'Attaching to {pid}', {pid: str2nr(pid)})
    return
  endif
  Request('processes', {}, (processes) => ChooseProcess(processes))
enddef

def ChooseProcess(processes: list<dict<any>>)
  if empty(processes)
    Warn('no Go processes to attach to')
    return
  endif
  popup_menu(processes->mapnew((_, p) => $'{p.pid}  {p.name}  {p.command}'[: &columns - 12]), {
    title: ' Attach to process ',
    callback: (_, choice) => ProcessChosen(processes, choice),
  })
enddef

def ProcessChosen(processes: list<dict<any>>, choice: number)
  if choice > 0
    Attach(string(processes[choice - 1].pid))
  endif
enddef

# Connect connects to the headless dlv listening at addr, a host:port.
export def Connect(addr: string)
  Launch('connect', [], $'Connecting to {addr}', {addr: addr})
enddef

# Launch starts a session with the breakpoints in the module of the session directory,
# or in that directory when it is outside a module. It loads the saved files there
# first, because Breakpoints reads signs, and only loaded buffers have signs.
def Launch(mode: string, args: list<string>, title: string, extra: dict<any> = {})
  const ch = Channel()
  if ch_status(ch) != 'open'
    return
  endif
  final params = {
    session: launches + 1,
    mode: mode,
    dir: expand('%:p:h'),
    args: args,
    dlv: get(g:, 'delve_frontend_dlv', 'dlv'),
    watches: watches,
    grouping: grouping,
    build_flags: Setting('delve_frontend_build_flags', []),
    env: Setting('delve_frontend_env', {}),
  }->extend(extra)
  const gomod = findfile('go.mod', escape(params.dir, ' ,') .. ';')
  const moduleDir = (empty(gomod) ? params.dir : fnamemodify(gomod, ':p:h')) .. '/'
  for path in keys(saved)
    if stridx(path, moduleDir) == 0 && !bufloaded(path) && filereadable(path)
      bufload(bufadd(path))
    endif
  endfor
  params.breakpoints = Breakpoints()->filter((_, bp) => stridx(bp.file, moduleDir) == 0)
  lastLaunch = {mode: mode, args: args, title: title, extra: extend(copy(extra), {
    dir: params.dir,
    build_flags: params.build_flags,
    env: params.env,
  })}
  launches += 1
  session = launches
  ++generation
  sent = {}
  for bp in params.breakpoints
    sent[string(bp.id)] = true
  endfor
  pending = {}
  OpenTab()
  frames = []
  ShowView({})
  SetLines('frames', [title])
  SetLines('goroutines', [])
  SetLines('output', [])
  ch_sendexpr(ch, {method: 'launch', params: params}, {callback: (_, resp) => {
    if !empty(get(resp, 'error', '')) && session == params.session
      session = 0
      SetLines('frames', [resp.error])
    endif
  }})
enddef

# Command sends command to the debug session: continue, next, step, stepout, pause or
# stop.
export def Command(command: string)
  if session == 0
    Warn('no debug session')
    return
  endif
  if command != 'pause' && command != 'stop'
    ++generation
    nodes = []
    sign_unplace(stopGroup)
    ClearInline()
    if command == 'continue'
      SetLines('frames', ['Running'])
    endif
  endif
  Request(command, {})
enddef

def Notify(msg: dict<any>)
  if get(msg, 'session', 0) != session
    return
  endif
  if msg.event == 'output'
    Append('output', msg.text)
  elseif msg.event == 'stopped'
    pending = {}
    Stopped(msg, true)
  elseif msg.event == 'exited'
    EndSession($'Exited with status {get(msg, 'status', 0)}')
  elseif msg.event == 'error'
    RejectBreakpoint(msg)
    Warn(msg.text)
  endif
enddef

# EndSession clears the current line, the inline values and every pane except the
# output pane, and shows text in the frames pane.
def EndSession(text: string)
  session = 0
  ++generation
  sent = {}
  pending = {}
  sign_unplace(stopGroup)
  frames = []
  ShowView({})
  SetLines('frames', [text])
  SetLines('goroutines', [])
enddef

# RejectBreakpoint removes the pending breakpoint that the error event msg reports
# dlv rejected.
def RejectBreakpoint(msg: dict<any>)
  const id = get(msg, 'breakpoint', 0)
  if has_key(pending, string(id))
    RemoveBreakpoint(id)
    SaveState()
  endif
enddef

# Stopped shows a stopped event: the goroutines, the frames of the stopped goroutine,
# and its selected frame. focus moves the cursor to the source window.
def Stopped(msg: dict<any>, focus: bool)
  ++generation
  frames = get(msg, 'frames', [])
  ShowGoroutines(get(msg, 'goroutines', []), msg.goroutine)
  ShowFrame(get(msg, 'frame', 0), focus)
  ShowView(msg)
enddef

# Expression returns the selected text in visual mode, and ends visual mode. In other
# modes it returns the expression at the cursor.
def Expression(): string
  if mode() !~ "^[vV\<C-v>]"
    return expand('<cexpr>')
  endif
  const text = join(getregion(getpos('v'), getpos('.'), {type: mode()}), "\n")
  execute "normal! \<Esc>"
  return text
enddef

# Evaluate shows the value of the Go expression expr in the selected frame, in a popup
# at the cursor. When expr is empty, it evaluates the selected text in visual mode, or
# the expression at the cursor.
export def Evaluate(expr: string)
  const e = empty(expr) ? Expression() : expr
  if session == 0
    Warn('no debug session')
    return
  endif
  Request('evaluate', {expr: e}, (node) => {
    popup_atcursor(split($'{e} = {node.value}', "\n"), {moved: 'any', padding: [0, 1, 0, 1], border: []})
  })
enddef

# AddWatch adds the Go expression expr to the watches. When expr is empty, it adds the
# selected text in visual mode, and otherwise asks for an expression.
export def AddWatch(expr: string)
  var e = expr
  if empty(e)
    e = mode() =~ "^[vV\<C-v>]" ? Expression() : input('Watch: ', expand('<cexpr>'))
  endif
  if !empty(e)
    SetWatches(watches + [e])
  endif
enddef

def RemoveWatch()
  const lnum = line('.')
  if lnum <= len(nodes) && has_key(nodes[lnum - 1], 'watch')
    SetWatches(watches->copy()->filter((i, _) => i != nodes[lnum - 1].watch))
  endif
enddef

# SetValue asks for a Go expression and assigns it to the variable on the cursor line.
def SetValue()
  const lnum = line('.')
  if session == 0 || lnum > len(nodes) || nodes[lnum - 1].id == 0
    return
  endif
  const n = nodes[lnum - 1]
  const value = input($'{n.name} = ', n.value)
  if empty(value)
    return
  endif
  const gen = generation
  Request('set', {id: n.id, value: value}, (view) => {
    if gen == generation
      ShowView(view)
    endif
  })
enddef

def SetWatches(exprs: list<any>)
  watches = exprs
  SaveState()
  if session == 0
    return
  endif
  const gen = generation
  Request('watches', {watches: watches}, (view) => {
    if gen == generation && type(view) == v:t_dict
      ShowView(view)
    endif
  })
enddef

# ToggleBreakpoint sets a breakpoint on the cursor line, or clears the one there.
export def ToggleBreakpoint()
  const placed = BreakpointAtCursor()
  if empty(placed)
    SetBreakpoint('')
  else
    ClearBreakpoint(placed.id)
  endif
enddef

# SetConditionalBreakpoint sets a breakpoint on the cursor line that stops only when a
# Go expression is true. It asks for the expression.
export def SetConditionalBreakpoint()
  const placed = BreakpointAtCursor()
  const cond = input('Stop when: ', empty(placed) ? '' : get(conditions, string(placed.id), ''))
  if empty(cond)
    return
  endif
  if !empty(placed)
    ClearBreakpoint(placed.id)
  endif
  SetBreakpoint(cond)
enddef

def BreakpointAtCursor(): dict<any>
  const placed = sign_getplaced(bufnr(), {group: breakpointGroup, lnum: line('.')})[0].signs
  return empty(placed) ? {} : placed[0]
enddef

def PlaceBreakpoint(buf: number, lnum: number, cond: string): number
  const id = sign_place(0, breakpointGroup,
    empty(cond) ? 'DelveFrontendBreakpoint' : 'DelveFrontendConditionalBreakpoint',
    buf, {lnum: lnum, priority: get(g:, 'delve_frontend_breakpoint_priority', 40)})
  if !empty(cond)
    conditions[string(id)] = cond
  endif
  return id
enddef

def SetBreakpoint(cond: string)
  const id = PlaceBreakpoint(bufnr(), line('.'), cond)
  SaveState()
  if session != 0
    SendBreakpoint(id, expand('%:p'), line('.'), cond)
  endif
enddef

# SendBreakpoint sends a breakpoint to the debug session, and removes it when dlv
# rejects it.
def SendBreakpoint(id: number, file: string, lnum: number, cond: string)
  const ch = Channel()
  if ch_status(ch) != 'open'
    return
  endif
  sent[string(id)] = true
  pending[string(id)] = true
  ch_sendexpr(ch, {method: 'breakpoint', params: {id: id, file: file, line: lnum, cond: cond}},
    {callback: (_, resp) => {
      if !empty(get(resp, 'error', ''))
        RemoveBreakpoint(id)
        SaveState()
        Warn(resp.error)
      endif
    }})
enddef

def ClearBreakpoint(id: number)
  RemoveBreakpoint(id)
  SaveState()
  if session != 0
    Request('clear', {id: id})
  endif
enddef

def RemoveBreakpoint(id: number)
  sign_unplace(breakpointGroup, {id: id})
  for byID in [conditions, sent, pending]
    if has_key(byID, string(id))
      remove(byID, string(id))
    endif
  endfor
enddef

def Breakpoints(): list<dict<any>>
  var breakpoints = []
  for buf in getbufinfo({bufloaded: 1})
    for sign in sign_getplaced(buf.bufnr, {group: breakpointGroup})[0].signs
      breakpoints->add({
        id: sign.id,
        file: fnamemodify(buf.name, ':p'),
        line: sign.lnum,
        cond: get(conditions, string(sign.id), ''),
      })
    endfor
  endfor
  return breakpoints
enddef

# ListBreakpoints puts every breakpoint, saved ones included, in the quickfix list and
# opens it.
export def ListBreakpoints()
  var items = Breakpoints()
  for [path, saves] in items(saved)
    if !bufloaded(path)
      items += saves->mapnew((_, bp) => extend({file: path}, bp))
    endif
  endfor
  setqflist([], ' ', {
    title: 'delve-frontend breakpoints',
    items: items->sort((a, b) => a.file == b.file ? a.line - b.line : a.file < b.file ? -1 : 1)
      ->mapnew((_, bp) => ({
        filename: bp.file,
        lnum: bp.line,
        text: empty(bp.cond) ? 'breakpoint' : $'breakpoint if {bp.cond}',
      })),
  })
  copen
enddef

# RestoreBreakpoints places the saved breakpoints of buffer buf. During a session, it
# replaces the breakpoints of buf that the helper has with the restored ones.
export def RestoreBreakpoints(buf: number)
  var resend = false
  for sign in sign_getplaced(buf, {group: breakpointGroup})[0].signs
    if session != 0 && has_key(sent, string(sign.id))
      resend = true
      Request('clear', {id: sign.id})
    endif
    RemoveBreakpoint(sign.id)
  endfor
  const path = fnamemodify(bufname(buf), ':p')
  const last = getbufinfo(buf)[0].linecount
  for bp in get(saved, path, [])
    if bp.line <= last
      const id = PlaceBreakpoint(buf, bp.line, bp.cond)
      if resend
        SendBreakpoint(id, path, bp.line, bp.cond)
      endif
    endif
  endfor
enddef

# SaveState writes the breakpoints and watches to the state file. It reads the file
# again, because another Vim can have written it, and replaces the breakpoints of a
# file, or the watches, only where they differ from what this Vim last read or wrote.
export def SaveState()
  if stateCorrupt || !filereadable(stateFile) && empty(saved) && empty(watches) && empty(Breakpoints())
    return
  endif
  var state = {breakpoints: {}, watches: []}
  if filereadable(stateFile)
    try
      state = ReadState()
    catch
      stateCorrupt = true
      Warn($'cannot read {stateFile}, and will not write it: {v:exception}')
      return
    endtry
  endif
  var changed: dict<any> = {}
  for buf in getbufinfo({bufloaded: 1})
    const path = fnamemodify(buf.name, ':p')
    const placed = sign_getplaced(buf.bufnr, {group: breakpointGroup})[0].signs
      ->mapnew((_, sign) => ({line: sign.lnum, cond: get(conditions, string(sign.id), '')}))
    if placed != get(saved, path, [])
      changed[path] = placed
    endif
  endfor
  state.breakpoints->extend(changed)->filter((_, bps) => !empty(bps))
  if watches != savedWatches
    state.watches = watches
  endif
  const temp = $'{stateFile}.{getpid()}'
  try
    mkdir(fnamemodify(stateFile, ':h'), 'p')
    writefile([json_encode(state)], temp)
    if rename(temp, stateFile) != 0
      throw $'cannot rename {temp}'
    endif
  catch
    delete(temp)
    if !stateWriteWarned
      stateWriteWarned = true
      Warn($'cannot write {stateFile}: {v:exception}')
    endif
    return
  endtry
  saved = saved->filter((path, _) => bufloaded(path))->extend(changed)
    ->extend(state.breakpoints->copy()->filter((path, _) => !bufloaded(path)))
    ->filter((_, bps) => !empty(bps))
  savedWatches = watches
enddef

# ReadState returns the breakpoints and watches in the state file, without breakpoints
# that lack a line number. It returns an empty state for an empty file, and throws on
# invalid JSON or a value of another shape.
def ReadState(): dict<any>
  const text = join(readfile(stateFile), '')
  const state = empty(text) ? {} : json_decode(text)
  if type(state) != v:t_dict || type(get(state, 'breakpoints', {})) != v:t_dict
      || type(get(state, 'watches', [])) != v:t_list
    throw 'not a delve-frontend state file'
  endif
  var breakpoints = {}
  for [path, bps] in items(get(state, 'breakpoints', {}))
    if type(bps) == v:t_list
      breakpoints[path] = bps->copy()
        ->filter((_, bp) => type(bp) == v:t_dict && type(get(bp, 'line', '')) == v:t_number)
        ->map((_, bp) => ({line: bp.line, cond: get(bp, 'cond', '')}))
    endif
  endfor
  return {
    breakpoints: breakpoints->filter((_, bps) => !empty(bps)),
    watches: get(state, 'watches', [])->copy()->filter((_, w) => type(w) == v:t_string),
  }
enddef

def LoadState()
  if !filereadable(stateFile)
    return
  endif
  try
    final state = ReadState()
    saved = state.breakpoints
    watches = state.watches
    savedWatches = watches
  catch
    stateCorrupt = true
    Warn($'cannot read {stateFile}, and will not write it: {v:exception}')
  endtry
enddef

# OpenTab opens the debug tab, or goes to it. The source window is at the top. Below
# it, from left to right, are the frames pane over the goroutines pane, the variables
# pane and the output pane.
def OpenTab()
  if win_gotoid(get(tab, 'source', 0))
    return
  endif
  const rows = get(g:, 'delve_frontend_pane_rows', 15)
  tab split
  tab = {source: win_getid()}
  execute $'botright :{rows}new'
  Pane('frames')
  rightbelow vnew
  Pane('variables')
  rightbelow vnew
  Pane('output')
  win_gotoid(tab.frames)
  belowright new
  Pane('goroutines')
  win_execute(tab.variables, $'resize {rows}')
  win_execute(tab.frames, $'resize {rows / 2}')
  for name in ['frames', 'variables']
    win_execute(tab[name], $'vertical resize {&columns / 3}')
  endfor
  win_gotoid(tab.source)
  augroup delve_frontend_session
    autocmd!
    autocmd TabClosed * TabClosed()
  augroup END
enddef

def TabClosed()
  if session != 0 && values(tab)->indexof((_, win) => win_id2tabwin(win)[0] > 0) < 0
    Command('stop')
  endif
enddef

# ScratchBuffer returns the buffer of the pane name, and creates it when it is not
# loaded.
def ScratchBuffer(name: string): number
  if !bufloaded(get(buffers, name, -1))
    const buf = bufadd($'delve-frontend://{name}')
    setbufvar(buf, '&buftype', 'nofile')
    setbufvar(buf, '&bufhidden', 'hide')
    setbufvar(buf, '&swapfile', 0)
    bufload(buf)
    setbufvar(buf, '&buflisted', 0)
    setbufvar(buf, '&modifiable', 0)
    buffers[name] = buf
  endif
  return buffers[name]
enddef

def Pane(name: string)
  execute 'buffer' ScratchBuffer(name)
  tab[name] = win_getid()
  setlocal nonumber norelativenumber signcolumn=no foldcolumn=0 nowrap nolist nospell winfixheight
  if name == 'frames'
    nnoremap <buffer> <CR> <ScriptCmd>SelectFrame()<CR>
  elseif name == 'goroutines'
    nnoremap <buffer> <CR> <ScriptCmd>SelectGoroutine()<CR>
    nnoremap <buffer> <Tab> <ScriptCmd>CycleGrouping()<CR>
    nnoremap <buffer> r <ScriptCmd>SetGrouping(extend(copy(grouping), {show_runtime: !grouping.show_runtime}))<CR>
  elseif name == 'variables'
    nnoremap <buffer> <CR> <ScriptCmd>ToggleNode()<CR>
    nnoremap <buffer> dd <ScriptCmd>RemoveWatch()<CR>
    nnoremap <buffer> e <ScriptCmd>SetValue()<CR>
  endif
enddef

def SetLines(name: string, lines: list<string>)
  const buf = get(buffers, name, -1)
  if !bufloaded(buf)
    return
  endif
  setbufvar(buf, '&modifiable', 1)
  silent deletebufline(buf, 1, '$')
  setbufline(buf, 1, lines)
  setbufvar(buf, '&modifiable', 0)
enddef

# Append adds text to the end of the pane name, and scrolls each window that shows it
# to the end.
def Append(name: string, text: string)
  const buf = get(buffers, name, -1)
  if !bufloaded(buf)
    return
  endif
  setbufvar(buf, '&modifiable', 1)
  if getbufinfo(buf)[0].linecount == 1 && getbufoneline(buf, 1) == ''
    setbufline(buf, 1, text)
  else
    appendbufline(buf, '$', text)
  endif
  setbufvar(buf, '&modifiable', 0)
  for win in win_findbuf(buf)
    win_execute(win, 'normal! G')
  endfor
enddef

def SelectFrame()
  const index = line('.') - 1
  if session == 0 || index >= len(frames)
    return
  endif
  const gen = generation
  Request('frame', {index: index}, (view) => {
    if gen == generation
      ShowFrame(index, false)
      ShowView(view)
    endif
  })
enddef

# SelectGoroutine selects the goroutine on the cursor line, or collapses or expands the
# group on it.
def SelectGoroutine()
  const lnum = line('.')
  const entry = get(goroutineLines, lnum - 1, {})
  if has_key(entry, 'group')
    collapsed[entry.group] = !entry.closed
    RenderGoroutines()
    cursor(lnum, 1)
    return
  endif
  if session == 0 || !has_key(entry, 'id')
    return
  endif
  const gen = generation
  Request('goroutine', {goroutine: entry.id}, (event) => {
    if gen == generation
      Stopped(event, false)
    endif
  })
enddef

def CycleGrouping()
  var by = groupingOrder[(index(groupingOrder, grouping.by) + 1) % len(groupingOrder)]
  var key = ''
  if by == 'label'
    key = input('Group by label: ', grouping.key)
    if empty(key)
      by = 'user'
    endif
  endif
  SetGrouping(extend(copy(grouping), {by: by, key: key}))
enddef

def SetGrouping(g: dict<any>)
  grouping = g
  collapsed = {}
  if session == 0
    return
  endif
  const gen = generation
  Request('grouping', {grouping: grouping}, (groups) => {
    if gen == generation && type(groups) == v:t_list
      ShowGoroutines(groups, selectedGoroutine)
    endif
  })
enddef

def ShowGoroutines(groups: list<dict<any>>, selected: number)
  goroutineGroups = groups
  selectedGoroutine = selected
  RenderGoroutines()
enddef

# RenderGoroutines draws the goroutines pane. When there are more than 5 groups, the
# groups without the selected goroutine start collapsed.
def RenderGoroutines()
  var lines = [$'by {groupingNames[grouping.by]}{grouping.by == 'label' ? ' ' .. grouping.key : ''}, runtime goroutines {grouping.show_runtime ? 'shown' : 'hidden'}']
  goroutineLines = [{}]
  for group in goroutineGroups
    const header = !has_key(group, 'label') ? Location(group.location)
      : group.label =~ '=$' ? $'{grouping.key} not set' : group.label
    const selected = indexof(group.goroutines, (_, g) => g.id == selectedGoroutine) >= 0
    const closed = get(collapsed, header, len(goroutineGroups) > 5 && !selected)
    lines->add($'{closed ? "▸" : "▾"} {header}  ({group.total})')
    goroutineLines->add({group: header, closed: closed})
    if closed
      continue
    endif
    for g in group.goroutines
      var line = $'  {g.id == selectedGoroutine ? "→" : " "} {g.id}  {g.status}'
      if grouping.by != 'user'
        line ..= '  ' .. Location(g.location)
      endif
      if !empty(get(g, 'labels', {}))
        line ..= '  {' .. join(items(g.labels)->sort()->mapnew((_, kv) => $'{kv[0]}={kv[1]}'), ', ') .. '}'
      endif
      lines->add(line)
      goroutineLines->add({id: g.id})
    endfor
    if group.total > len(group.goroutines)
      lines->add($'    ({group.total - len(group.goroutines)} more)')
      goroutineLines->add({})
    endif
  endfor
  SetLines('goroutines', lines)
enddef

def Location(f: dict<any>): string
  return $'{substitute(f.function, '^.*/', '', '')}  {fnamemodify(f.file, ':t')}:{f.line}'
enddef

# ShowFrame marks frame index in the frames pane, and shows its line in the source
# window. focus moves the cursor to the source window.
def ShowFrame(index: number, focus: bool)
  SetLines('frames', frames->mapnew((i, f) => $'{i == index ? "→" : " "} {Location(f)}'))
  sign_unplace(stopGroup)
  frameBuffer = -1
  if index >= len(frames)
    return
  endif
  const frame = frames[index]
  if !filereadable(frame.file) || win_id2tabwin(get(tab, 'source', 0))[0] == 0
    return
  endif
  if fnamemodify(bufname(winbufnr(tab.source)), ':p') != frame.file
    win_execute(tab.source, $'keepjumps hide edit {fnameescape(frame.file)}')
  endif
  win_execute(tab.source, [$'cursor({frame.line}, 1)', 'normal! zz'])
  frameBuffer = winbufnr(tab.source)
  sign_place(0, stopGroup, 'DelveFrontendCurrentLine', frameBuffer, {lnum: frame.line, priority: 50})
  if focus
    win_gotoid(tab.source)
  endif
enddef

# ShowView shows the watches and variables of the selected frame in the variables pane,
# and its inline values in the source window.
def ShowView(view: dict<any>)
  nodes = get(view, 'watches', [])->mapnew((i, w) => extend(w, {watch: i}))
    + get(view, 'variables', [])
  nodes->map((_, n) => extend(n, {depth: 0, expanded: false}))
  SetLines('variables', nodes->mapnew((_, n) => NodeLine(n)))
  ClearInline()
  if !bufloaded(frameBuffer)
    return
  endif
  const last = getbufinfo(frameBuffer)[0].linecount
  for inline in get(view, 'inline', [])
    if inline.line <= last
      prop_add(inline.line, 0, {type: 'DelveFrontendInlineValue', bufnr: frameBuffer, text: '  ' .. inline.text, text_align: 'after'})
    endif
  endfor
enddef

def ClearInline()
  for buf in getbufinfo({bufloaded: 1})
    prop_remove({type: 'DelveFrontendInlineValue', bufnr: buf.bufnr, all: true})
  endfor
enddef

def NodeLine(n: dict<any>): string
  const marker = !n.expandable ? '  ' : n.expanded ? '▾ ' : '▸ '
  return repeat('  ', n.depth) .. marker .. n.name .. (empty(n.value) ? '' : ' = ' .. n.value)
enddef

# ToggleNode expands or collapses the variable on the cursor line.
def ToggleNode()
  const lnum = line('.')
  if lnum > len(nodes) || !nodes[lnum - 1].expandable
    return
  endif
  final n = nodes[lnum - 1]
  const buf = buffers.variables
  if n.expanded
    var end = lnum
    while end < len(nodes) && nodes[end].depth > n.depth
      end += 1
    endwhile
    n.expanded = false
    setbufvar(buf, '&modifiable', 1)
    if end > lnum
      remove(nodes, lnum, end - 1)
      deletebufline(buf, lnum + 1, end)
    endif
    setbufline(buf, lnum, NodeLine(n))
    setbufvar(buf, '&modifiable', 0)
    return
  endif
  const gen = generation
  Request('children', {id: n.id}, (children) => {
    const at = indexof(nodes, (_, v) => v is n)
    if gen != generation || at < 0 || n.expanded
      return
    endif
    n.expanded = true
    const added = children->mapnew((_, c) => extend(c, {depth: n.depth + 1, expanded: false}))
    nodes->extend(added, at + 1)
    setbufvar(buf, '&modifiable', 1)
    setbufline(buf, at + 1, NodeLine(n))
    appendbufline(buf, at + 1, added->mapnew((_, c) => NodeLine(c)))
    setbufvar(buf, '&modifiable', 0)
  })
enddef

LoadState()
