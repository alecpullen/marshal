package tui

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/castlist"
	"marshal/internal/app/tui/docpanel"
	"marshal/internal/commands"
)

// settleCalls serves m's call queue until it is idle, running each command
// and feeding each message back through Update, and returns the model. cmds
// are commands the step that made the call returned — Update's own epilogue
// starts a queued call, so a test that dropped that command passes it here.
// It fails t if the queue is not idle within five seconds.
func settleCalls(t *testing.T, m Model, cmds ...tea.Cmd) Model {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	pending := append([]tea.Cmd(nil), cmds...)
	for {
		if m.callsIdle() && len(pending) == 0 {
			return m
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the call queue did not settle within five seconds")
		}
		pending = append(pending, m.serveCalls())
		for len(pending) > 0 {
			cmd := pending[0]
			pending = pending[1:]
			for _, msg := range runCmdsBounded(t, cmd, deadline) {
				updated, next := m.Update(msg)
				m = mustModel(t, updated)
				pending = append(pending, next)
			}
		}
	}
}

// blockingCalls are the seam methods R6 says must not run on the Update
// goroutine.
var blockingCalls = []string{
	"Command", "NewSession", "PlanPreflight", "RefreshSheet",
	"ReloadConfig", "ReconnectMCP", "CanProbeModel", "Postmortem",
}

// TestBlockingCallsLeaveTheUpdateGoroutine fails when a non-test file under
// internal/app/tui calls one of blockingCalls on a receiver ending in sess or
// host outside a func() tea.Msg literal, a function literal returning
// callOutcome, or a go statement. It is a syntax check on the tree, so a
// converted call cannot drift back onto the Update goroutine unnoticed.
func TestBlockingCallsLeaveTheUpdateGoroutine(t *testing.T) {
	t.Helper()
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		offenders = append(offenders, blockingCallSites(path, fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/app/tui: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("these calls run on the Update goroutine (P3 R6); move them into the call queue:\n%s",
			strings.Join(offenders, "\n"))
	}
}

// blockingCallSites returns the file's offending calls, one "path:line:
// receiver.method" line each.
func blockingCallSites(path string, fset *token.FileSet, f *ast.File) []string {
	// Everything inside an allowed region runs off the Update goroutine: a
	// tea.Cmd literal, a converted call's run or result, or a go statement.
	type region struct{ from, to token.Pos }
	var allowed []region
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			if literalReturnsAsync(v) {
				allowed = append(allowed, region{v.Pos(), v.End()})
			}
		case *ast.GoStmt:
			allowed = append(allowed, region{v.Pos(), v.End()})
		}
		return true
	})

	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !isBlockingCall(sel.Sel.Name) {
			return true
		}
		if !receiverEndsInSessionOrHost(sel.X) {
			return true
		}
		for _, r := range allowed {
			if call.Pos() >= r.from && call.End() <= r.to {
				return true
			}
		}
		out = append(out, fmt.Sprintf("%s:%d: %s.%s", path, fset.Position(call.Pos()).Line, exprName(sel.X), sel.Sel.Name))
		return true
	})
	return out
}

// literalReturnsAsync reports whether fn is a tea.Cmd literal or a converted
// call's run or result.
func literalReturnsAsync(fn *ast.FuncLit) bool {
	if fn.Type == nil || fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		switch typ := field.Type.(type) {
		case *ast.Ident:
			if typ.Name == "callOutcome" || typ.Name == "callRun" {
				return true
			}
		case *ast.SelectorExpr:
			if ident, ok := typ.X.(*ast.Ident); ok && ident.Name == "tea" && typ.Sel.Name == "Msg" {
				return true
			}
		}
	}
	return false
}

func isBlockingCall(name string) bool {
	for _, want := range blockingCalls {
		if name == want {
			return true
		}
	}
	return false
}

// receiverEndsInSessionOrHost reports whether the expression's last identifier
// is sess or host, which is what makes a call one of the queue's.
func receiverEndsInSessionOrHost(x ast.Expr) bool {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name == "sess" || v.Name == "host"
	case *ast.SelectorExpr:
		return v.Sel.Name == "sess" || v.Sel.Name == "host"
	case *ast.ParenExpr:
		return receiverEndsInSessionOrHost(v.X)
	}
	return false
}

// exprName is the receiver rendered for the failure message.
func exprName(x ast.Expr) string {
	var b strings.Builder
	if printer.Fprint(&b, token.NewFileSet(), x) != nil {
		return "?"
	}
	return b.String()
}

// runCmdsBounded runs a command tree and returns every message it produces,
// failing t if it has not returned by the deadline. A converted call under
// test blocks until the test releases it, so the bound is the settle deadline
// rather than a command's own timeout.
func runCmdsBounded(t *testing.T, cmd tea.Cmd, deadline time.Time) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	out := make(chan []tea.Msg, 1)
	go func() { out <- runCallCmds(cmd) }()
	select {
	case msgs := <-out:
		return msgs
	case <-time.After(time.Until(deadline)):
		t.Fatal("a command did not return before the settle deadline")
		return nil
	}
}

// feedCallMsg handles one message through Update and follows every command
// the update returns.
func feedCallMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, next := m.Update(msg)
	m = mustModel(t, updated)
	for _, follow := range runCallCmds(next) {
		m = feedCallMsg(t, m, follow)
	}
	return m
}

// runCallCmds runs a command tree and returns every message its leaves
// produce. Unlike runCmdChain it has no timeout: a converted call under test
// blocks until the test releases it.
func runCallCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, sub := range batch {
			out = append(out, runCallCmds(sub)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// modelOf is the model Update returned, whichever form it took.
func mustModel(t *testing.T, updated tea.Model) Model {
	t.Helper()
	switch next := updated.(type) {
	case Model:
		return next
	case *Model:
		return *next
	}
	t.Fatalf("Update returned %T, not a Model", updated)
	return Model{}
}

// callRecorder records call order. Runs happen off the Update goroutine, so
// every access takes the lock (the tests run under -race).
type callRecorder struct {
	mu      sync.Mutex
	started []string
	handled []string
}

func (r *callRecorder) start(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, name)
}

func (r *callRecorder) handle(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handled = append(r.handled, name)
}

func (r *callRecorder) startedList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

func (r *callRecorder) handledList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.handled...)
}

// TestQueuedCallsRunInOrder proves the queue runs one call at a time, in the
// order the calls were made, and handles the results in that order too.
func TestQueuedCallsRunInOrder(t *testing.T) {
	m := newTestModel(t)
	rec := &callRecorder{}
	release := make(chan struct{})
	started := make(chan string, 2)

	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			rec.start("first")
			started <- "first"
			<-release
			return func(*Model) tea.Cmd {
				rec.handle("first")
				return nil
			}
		}
	})
	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			rec.start("second")
			started <- "second"
			return func(*Model) tea.Cmd {
				rec.handle("second")
				return nil
			}
		}
	})

	cmd := m.serveCalls()
	if cmd == nil {
		t.Fatal("serving the queue started no command")
	}
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(cmd) }()

	if got := <-started; got != "first" {
		t.Fatalf("the first call to start was %q, want first", got)
	}
	// The second call must not start while the first is in flight, however
	// long the first takes.
	time.Sleep(20 * time.Millisecond)
	select {
	case got := <-started:
		t.Fatalf("%q started while the first call was in flight", got)
	default:
	}
	if extra := m.serveCalls(); extra != nil {
		t.Fatal("serving the queue while a call was in flight started another")
	}

	close(release)
	msgs := <-done
	if len(msgs) != 1 {
		t.Fatalf("the first call produced %d messages, want 1", len(msgs))
	}
	// Handling the first result starts the second call from Update's
	// epilogue.
	m = feedCallMsg(t, m, msgs[0])
	if got := <-started; got != "second" {
		t.Fatalf("the second call to start was %q, want second", got)
	}
	m = settleCalls(t, m)

	if got, want := rec.startedList(), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls started in order %v, want %v", got, want)
	}
	if got, want := rec.handledList(), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("results handled in order %v, want %v", got, want)
	}
	if !m.callsIdle() {
		t.Fatal("the queue was not idle after both results were handled")
	}
}

// TestAQueuedCallReadsTheModelWhenItStarts proves a call reads the model when
// it reaches the head of the queue, not when it was made.
func TestAQueuedCallReadsTheModelWhenItStarts(t *testing.T) {
	m := newTestModel(t)
	m.rawWidth = 100

	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			return func(mm *Model) tea.Cmd {
				mm.rawWidth = 4242
				return nil
			}
		}
	})
	var saw int
	m.enqueueCall(func(mm *Model) callRun {
		// The start runs on the Update goroutine when the call reaches the
		// head, so it sees what the first result left behind.
		saw = mm.rawWidth
		return func(context.Context) callOutcome { return nil }
	})

	m = settleCalls(t, m)

	if saw != 4242 {
		t.Fatalf("the second call read rawWidth %d when it started, want 4242", saw)
	}
}

// TestAStepRunsAtTheHeadAndTheQueueMovesOn proves a step runs on the Update
// goroutine after the call ahead of it is handled, and that the call behind
// it starts in the same serveCalls.
func TestAStepRunsAtTheHeadAndTheQueueMovesOn(t *testing.T) {
	m := newTestModel(t)
	var order []string

	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			order = append(order, "first started")
			return func(*Model) tea.Cmd {
				order = append(order, "first handled")
				return nil
			}
		}
	})
	m.enqueueStep(func(*Model) tea.Cmd {
		order = append(order, "step")
		return nil
	})
	m.enqueueCall(func(*Model) callRun {
		return func(context.Context) callOutcome {
			order = append(order, "second started")
			return func(*Model) tea.Cmd {
				order = append(order, "second handled")
				return nil
			}
		}
	})

	// One serveCalls starts the first call and stops at it: nothing behind it
	// runs yet, and a call's own run waits for the command.
	cmd := m.serveCalls()
	if len(order) != 0 {
		t.Fatalf("serving the queue ran %v before any call ran", order)
	}

	// Handling the first result runs the step and starts the second call, in
	// that order, in one epilogue.
	msgs := runCallCmds(cmd)
	if len(msgs) != 1 {
		t.Fatalf("the first call produced %d messages, want 1", len(msgs))
	}
	updated, next := m.Update(msgs[0])
	m = mustModel(t, updated)
	want := []string{"first started", "first handled", "step"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("after the first result the queue ran %v, want %v", order, want)
	}

	// The second call is in flight, its command in next: running it and
	// handling its result drains the queue.
	for _, msg := range runCallCmds(next) {
		updated, _ := m.Update(msg)
		m = mustModel(t, updated)
	}

	want = append(want, "second started", "second handled")
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("the queue ran %v, want %v", order, want)
	}
	if !m.callsIdle() {
		t.Fatal("the queue did not drain")
	}
}

// closeRecorder records that the session was closed on the way out.
type closeRecorder struct {
	acpclient.Session
	closed chan struct{}
	once   sync.Once
}

func (c *closeRecorder) Close(ctx context.Context) error {
	c.once.Do(func() { close(c.closed) })
	return c.Session.Close(ctx)
}

// TestAQuitWaitsForTheCallInFlight proves a quit cancels the call in flight,
// drops everything queued behind it, and finishes when its result arrives.
func TestAQuitWaitsForTheCallInFlight(t *testing.T) {
	host, sess := inproc.New(inproc.Deps{
		State: session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{}),
	})
	closed := make(chan struct{})
	m := New(host, &closeRecorder{Session: sess, closed: closed}, WithHomeDir(t.TempDir()))

	cancelled := make(chan struct{})
	m.enqueueCall(func(*Model) callRun {
		return func(ctx context.Context) callOutcome {
			<-ctx.Done()
			close(cancelled)
			return func(*Model) tea.Cmd { return nil }
		}
	})
	queuedStarted := false
	m.enqueueCall(func(*Model) callRun {
		queuedStarted = true
		return nil
	})

	cmd := m.serveCalls()
	if cmd == nil {
		t.Fatal("serving the queue started no command")
	}
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(cmd) }()

	// A quit does not finish while a call is in flight; it cancels the call
	// and waits for its result.
	if quit := m.finishQuit(); quit != nil {
		t.Fatal("finishQuit returned a command while a call was in flight")
	}
	if !m.callQuitWaiting {
		t.Fatal("finishQuit did not record that a quit is waiting")
	}
	<-cancelled

	msgs := <-done
	if len(msgs) != 1 {
		t.Fatalf("the in-flight call produced %d messages, want 1", len(msgs))
	}
	updated, quitCmd := m.Update(msgs[0])
	m = mustModel(t, updated)

	var sawQuit bool
	for _, msg := range runCallCmds(quitCmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			sawQuit = true
		}
	}
	if !sawQuit {
		t.Fatal("the quit did not finish when the in-flight result arrived")
	}
	if queuedStarted {
		t.Fatal("a call queued behind an in-flight call started after the quit")
	}
	if m.callQuitWaiting || m.callInFlight {
		t.Fatal("the quit left the queue or the in-flight mark set")
	}
	select {
	case <-closed:
	default:
		t.Fatal("the quit did not close the session")
	}
}

// sessionTextCount counts the session messages that contain want. A converted
// call's notes are written when its result is handled, so this is how a test
// sees whether the result has landed yet.
func sessionTextCount(m Model, want string) int {
	n := 0
	for _, msg := range stateOf(m).Messages() {
		if strings.Contains(msg.Content, want) {
			n++
		}
	}
	return n
}

// blockingCommand is a registry command whose handler waits for release, so a
// test can observe the model while its call is in flight.
func blockingCommand(t *testing.T, entered chan<- struct{}, release <-chan struct{}) *commands.Registry {
	t.Helper()
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "slowstub",
		Handler: func(*session.State, []string) commands.Result {
			entered <- struct{}{}
			<-release
			return commands.Text("slow result")
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

// TestACommandsResultLandsWhenItArrives pins P3 R6: a command's handler runs
// off the Update goroutine, so its text lands when the result arrives and the
// screen keeps routing while it runs.
func TestACommandsResultLandsWhenItArrives(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	m := testModelWith(inproc.Deps{
		State:    modelTestState(t),
		Commands: blockingCommand(t, entered, release),
	})
	m.resize(80, 24)

	updated, _ := m.dispatchCommand("/slowstub")
	m = asModel(t, updated)
	// The handler is queued by the dispatch; serving the queue starts it.
	serve := m.serveCalls()
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(serve) }()
	<-entered

	if n := sessionTextCount(m, "slow result"); n != 0 {
		t.Fatalf("the command's text landed %d times before its result arrived", n)
	}
	// Update keeps routing while the call runs: a resize still renders.
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = asModel(t, updated)
	if m.rawWidth != 100 {
		t.Fatalf("a resize during the call did not render: rawWidth = %d, want 100", m.rawWidth)
	}

	close(release)
	for _, msg := range <-done {
		m = feedCallMsg(t, m, msg)
	}
	if n := sessionTextCount(m, "slow result"); n != 1 {
		t.Fatalf("the command's text appears %d times after settling, want 1", n)
	}
}

// TestTrustShowsTheHandlerTextBeforeItsPanel pins P3 R6's ordering for a
// command that has both a handler and a TUI effect: the handler's panel is
// docked from the result, and the effect's note follows it.
func TestTrustShowsTheHandlerTextBeforeItsPanel(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	reg := commands.New()
	_ = commands.RegisterAll(reg, nil)
	m := testModelWith(inproc.Deps{State: state, Commands: reg},
		WithHomeDir(t.TempDir()), WithWorkingDir("/some/project"))

	updated, _ := m.dispatchCommand("/trust")
	m = asModel(t, updated)
	// Nothing the handler produces may land inside the dispatch itself.
	if m.dock.IsOpen() {
		t.Fatal("the handler's panel opened before its result arrived")
	}
	if n := sessionTextCount(m, "marshal --trust"); n != 0 {
		t.Fatalf("the effect's note was written %d times before the handler's result", n)
	}

	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*docpanel.Panel); !ok {
		t.Fatalf("expected the handler's docpanel after settling, got %T", m.dock.Panel())
	}
	msgs := stateOf(m).Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "marshal --trust") {
		t.Fatal("the effect's note should follow the handler's panel")
	}
}

// TestAPromptWaitsForTheCommandBeforeIt pins P3 R6's step rule: a prompt
// submitted while a command is in flight waits behind it and starts after the
// command's result is handled.
func TestAPromptWaitsForTheCommandBeforeIt(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	m := testModelWith(inproc.Deps{
		State:    modelTestState(t),
		Commands: blockingCommand(t, entered, release),
		Runner:   runnerOf(runner),
	})
	m.resize(80, 24)

	updated, _ := m.dispatchCommand("/slowstub")
	m = asModel(t, updated)
	serve := m.serveCalls()
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(serve) }()
	<-entered

	// A prompt submitted while the command is in flight waits behind it.
	m.input.SetValue("hello there")
	updated, promptCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, updated)
	if promptCmd != nil {
		t.Fatal("the prompt started while the command was still in flight")
	}
	select {
	case goal := <-runner.called:
		t.Fatalf("the prompt ran %q before the command's result", goal)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	for _, msg := range <-done {
		m = feedCallMsg(t, m, msg)
	}
	select {
	case goal := <-runner.called:
		if goal != "hello there" {
			t.Fatalf("the prompt ran %q, want %q", goal, "hello there")
		}
	case <-time.After(time.Second):
		t.Fatal("the prompt did not start after the command's result")
	}
}

// TestASetReportsWhenItsReloadReturns pins P3 R6 for /set: the save happens at
// once, and the receipt is written when the reload returns.
func TestASetReportsWhenItsReloadReturns(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		d.ReloadConfig = func(config.Config) error {
			entered <- struct{}{}
			<-release
			return nil
		}
	})
	stateOf(m).WorkingDir = t.TempDir()

	m.dispatchCommand("/set shell.allow_network on")
	// The effect runs at dispatch: it saves synchronously and queues the
	// reload. Serving the queue starts that call, which blocks until released,
	// so the test runs it off this goroutine.
	reloadCmd := m.serveCalls()

	// The save is synchronous, so the file is written and the registry
	// invalidated before any reload runs.
	if _, err := os.Stat(config.ProjectConfigPath(stateOf(m).WorkingDir)); err != nil {
		t.Fatalf("the save did not happen before the reload: %v", err)
	}
	if n := sessionTextCount(m, "✓ shell.allow_network"); n != 0 {
		t.Fatalf("the receipt was written %d times before the reload returned", n)
	}

	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(reloadCmd) }()
	<-entered
	if n := sessionTextCount(m, "✓ shell.allow_network"); n != 0 {
		t.Fatalf("the receipt was written %d times while the reload ran", n)
	}

	close(release)
	for _, msg := range <-done {
		m = feedCallMsg(t, m, msg)
	}
	if n := sessionTextCount(m, "✓ shell.allow_network"); n != 1 {
		t.Fatalf("the receipt appears %d times after the reload, want 1", n)
	}
	if m.setReg != nil {
		t.Fatal("the reload should have invalidated the cached settings registry")
	}
	// It rebuilds from the live config on the next read, carrying the change.
	if reg := m.settingsRegistry(); reg == nil || !reg.Config().Tools.Shell.AllowNetwork {
		t.Fatal("the settings registry should rebuild from the config the reload applied")
	}
}

// TestAFailedSaveReportsAtOnce pins the other half of R6's rule for /set: a
// save that fails is reported before any reload is queued, and none runs.
func TestAFailedSaveReportsAtOnce(t *testing.T) {
	reloads := 0
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		d.ReloadConfig = func(config.Config) error {
			reloads++
			return nil
		}
	})
	// A path that cannot hold .marshal/config.toml, so the save fails.
	blockingPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingPath, nil, 0o644); err != nil {
		t.Fatalf("create blocking file: %v", err)
	}
	stateOf(m).WorkingDir = blockingPath

	m.dispatchCommand("/set shell.allow_network on")
	m = settleCalls(t, m)

	if reloads != 0 {
		t.Fatalf("a failed save ran %d reloads, want 0", reloads)
	}
	if n := sessionTextCount(m, "save failed"); n != 1 {
		t.Fatalf("the failed save was reported %d times, want 1", n)
	}
}

// TestReloadsRunInTheOrderMade pins the queue's order for two /set commands in
// a row: their reloads reach the session in the order the commands were made,
// and their receipts appear in that order.
func TestReloadsRunInTheOrderMade(t *testing.T) {
	var order []string
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		d.ReloadConfig = func(cfg config.Config) error {
			order = append(order, fmt.Sprintf("allow=%v redact=%v",
				cfg.Tools.Shell.AllowNetwork, cfg.Privacy.RedactSecrets))
			return nil
		}
	})
	stateOf(m).WorkingDir = t.TempDir()

	m.dispatchCommand("/set shell.allow_network on")
	m.dispatchCommand("/set privacy.redact_secrets off")
	m = settleCalls(t, m)

	if len(order) != 2 {
		t.Fatalf("the reloads ran %d times, want 2: %v", len(order), order)
	}
	// The receipts follow the reloads, so their order is the reloads' order.
	var receipts []string
	for _, msg := range stateOf(m).Messages() {
		if strings.Contains(msg.Content, "shell.allow_network") ||
			strings.Contains(msg.Content, "privacy.redact_secrets") {
			if strings.Contains(msg.Content, "✓") {
				receipts = append(receipts, msg.Content)
			}
		}
	}
	if len(receipts) != 2 {
		t.Fatalf("found %d receipts, want 2: %v", len(receipts), receipts)
	}
	if !strings.Contains(receipts[0], "shell.allow_network") ||
		!strings.Contains(receipts[1], "privacy.redact_secrets") {
		t.Fatalf("receipts out of order: %v", receipts)
	}
}

// TestAQuitWaitsForAReloadInFlight pins P3 R6's quit rule for a reload: the
// quit finishes only once the reload it waited for has returned.
func TestAQuitWaitsForAReloadInFlight(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	m := newTestModelDeps(t, func(d *inproc.Deps) {
		d.ReloadConfig = func(config.Config) error {
			entered <- struct{}{}
			<-release
			return nil
		}
	})
	stateOf(m).WorkingDir = t.TempDir()

	m.dispatchCommand("/set shell.allow_network on")
	// The effect already ran and queued the reload; serving the queue starts
	// it, so run that off this goroutine.
	reloadCmd := m.serveCalls()
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(reloadCmd) }()
	<-entered

	if quit := m.finishQuit(); quit != nil {
		t.Fatal("finishQuit returned a command while a reload was in flight")
	}
	if !m.callQuitWaiting {
		t.Fatal("finishQuit did not record that a quit is waiting")
	}

	close(release)
	msgs := <-done
	if len(msgs) != 1 {
		t.Fatalf("the reload produced %d messages, want 1", len(msgs))
	}
	updated, quitCmd := m.Update(msgs[0])
	m = asModel(t, updated)

	var sawQuit bool
	for _, msg := range runCallCmds(quitCmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			sawQuit = true
		}
	}
	if !sawQuit {
		t.Fatal("the quit did not finish when the reload returned")
	}
	if m.callQuitWaiting || m.callInFlight {
		t.Fatal("the quit left the queue or the in-flight mark set")
	}
}

type blockingNewSession struct {
	newState *session.State
	entered  chan struct{}
	release  chan struct{}
	called   int
}

func (b *blockingNewSession) newSession(string) (*session.State, error) {
	b.called++
	b.entered <- struct{}{}
	<-b.release
	return b.newState, nil
}

// TestNewSessionSwapsWhenItsResultArrives pins P3 R6: the replacement's swap
// happens when its result arrives, and a prompt submitted meanwhile waits for
// it and lands on the new session.
func TestNewSessionSwapsWhenItsResultArrives(t *testing.T) {
	oldState := postmortemTestState(t, "sess-old")
	newState := postmortemTestState(t, "sess-new")
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	block := &blockingNewSession{
		newState: newState,
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	host, sess := inproc.New(inproc.Deps{
		State:      oldState,
		NewSession: block.newSession,
		End:        &endTestController{},
		Commands:   setupCmdReg(t),
		Runner:     runnerOf(runner),
	})
	m := New(host, sess, WithHomeDir(t.TempDir()))
	m.resize(80, 24)

	if cmd := startNewSession(&m, "named"); cmd != nil {
		t.Fatal("startNewSession returned a command")
	}
	// A call queued behind the replacement reads the session when it starts,
	// so it must see the swapped one — the start of a queued call is what the
	// queue re-reads the model for.
	var sawSession string
	m.enqueueCall(func(mm *Model) callRun {
		sawSession = mm.sess.ID()
		return func(context.Context) callOutcome { return nil }
	})
	serve := m.serveCalls()
	if serve == nil {
		t.Fatal("serving the queue started no command")
	}
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(serve) }()
	<-block.entered

	// The old session stays current while the runtime builds the replacement.
	if got := m.sess.ID(); got != "sess-old" {
		t.Fatalf("session ID while the replacement runs = %q, want sess-old", got)
	}
	// A prompt submitted now waits behind the replacement.
	m.input.SetValue("hello there")
	updated, promptCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(t, updated)
	if promptCmd != nil {
		t.Fatal("the prompt started while the replacement was still being built")
	}

	close(block.release)
	msgs := <-done
	if len(msgs) != 1 {
		t.Fatalf("the replacement produced %d messages, want 1", len(msgs))
	}
	updated, settleAgain := m.Update(msgs[0])
	m = asModel(t, updated)

	if got := m.sess.ID(); got != "sess-new" {
		t.Fatalf("session ID after the result = %q, want sess-new", got)
	}
	if sawSession != "sess-new" {
		t.Fatalf("the call queued behind the replacement saw session %q, want sess-new", sawSession)
	}
	if !m.busy {
		// The prompt still waits behind the call queued after the
		// replacement: run that call and feed its result back.
		for _, msg := range runCmdChain(t, settleAgain) {
			if _, ok := msg.(callDoneMsg); !ok {
				continue
			}
			updated, promptStart := m.Update(msg)
			m = asModel(t, updated)
			runCmdChain(t, promptStart)
		}
	}
	if !m.busy {
		t.Fatal("the queued prompt did not start on the new session")
	}
	select {
	case goal := <-runner.called:
		if goal != "hello there" {
			t.Fatalf("the queued prompt ran %q, want %q", goal, "hello there")
		}
	case <-time.After(time.Second):
		t.Fatal("the queued prompt never reached the runner")
	}
}

// TestThePreflightOpensWhenItArrives pins P3 R6 for /sdd: nothing opens while
// the preflight runs, and its result opens the cast list or reports the plan
// it rejected.
func TestThePreflightOpensWhenItArrives(t *testing.T) {
	accepting := testModelWith(inproc.Deps{
		State:  session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}),
		Runner: runnerOf(&testAgentRunner{}),
		PlanPreflight: preflightOf(acpwire.PlanPreflight{
			Inspection: acpwire.PlanInspection{Strategy: acpwire.StrategyAgent},
		}),
		Commands: setupCmdReg(t),
	})
	accepting.resize(100, 40)

	updated, _ := accepting.dispatchCommand("/sdd plan.md")
	m := asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("the cast list opened before the preflight returned")
	}
	m = settleCalls(t, m)
	if _, ok := m.dock.Panel().(*castlist.Panel); !ok {
		t.Fatalf("the preflight did not open the cast list, dock holds %T", m.dock.Panel())
	}

	rejecting := testModelWith(inproc.Deps{
		State:  session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}),
		Runner: runnerOf(&testAgentRunner{}),
		PlanPreflight: func(context.Context, acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
			return acpwire.PlanPreflight{}, errors.New("no such plan")
		},
		Commands: setupCmdReg(t),
	})
	rejecting.resize(100, 40)

	updated, _ = rejecting.dispatchCommand("/sdd missing.md")
	rejecting = asModel(t, updated)
	rejecting = settleCalls(t, rejecting)
	if rejecting.dock.IsOpen() {
		t.Fatal("a rejected preflight must not open the dock")
	}
	if n := sessionTextCount(rejecting, "Cannot start plan run: no such plan"); n != 1 {
		t.Fatalf("a rejected preflight was reported %d times, want 1", n)
	}
}

// TestTheSheetOpensWhenItsRefreshReturns pins P3 R6 for Ctrl+B: the sheet
// opens when its refresh returns, and a second Ctrl+B while that open is
// queued cancels it.
func TestTheSheetOpensWhenItsRefreshReturns(t *testing.T) {
	stub := &stubSession{}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	stub.onRefreshSheet = func(acpwire.SheetRefresh) {
		entered <- struct{}{}
		<-release
	}
	state := seamState(t, "s1")
	state.AddMessage(session.RoleUser, "a first turn", session.ContentTypePlain)
	m := testModelStub(state, stub)
	m.resize(100, 40)

	updated, serve := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = asModel(t, updated)
	if m.dock.IsOpen() {
		t.Fatal("the sheet opened before its refresh returned")
	}
	done := make(chan []tea.Msg, 1)
	go func() { done <- runCallCmds(serve) }()
	<-entered

	// A second Ctrl+B while the open is queued cancels it.
	updated, cancel := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = asModel(t, updated)
	if cancel != nil {
		t.Fatal("a cancelling Ctrl+B returned a command")
	}
	close(release)
	for _, msg := range <-done {
		m = feedCallMsg(t, m, msg)
	}
	if m.dock.IsOpen() {
		t.Fatal("a cancelled open must not open the sheet")
	}

	// Without the cancellation the sheet opens on its refresh's result.
	stub2 := &stubSession{}
	entered2 := make(chan struct{}, 1)
	release2 := make(chan struct{})
	stub2.onRefreshSheet = func(acpwire.SheetRefresh) {
		entered2 <- struct{}{}
		<-release2
	}
	state2 := seamState(t, "s2")
	state2.AddMessage(session.RoleUser, "a first turn", session.ContentTypePlain)
	m2 := testModelStub(state2, stub2)
	m2.resize(100, 40)

	updated, serve2 := m2.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m2 = asModel(t, updated)
	done2 := make(chan []tea.Msg, 1)
	go func() { done2 <- runCallCmds(serve2) }()
	<-entered2
	close(release2)
	for _, msg := range <-done2 {
		m2 = feedCallMsg(t, m2, msg)
	}
	if !m2.sheetOpen() {
		t.Fatalf("the sheet did not open when its refresh returned, dock holds %T", m2.dock.Panel())
	}
}
