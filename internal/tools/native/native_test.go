package native

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestRegisterAllRegistersExpectedTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()

	// codebase_search is registered only when an embedding route resolves,
	// so the full-surface assertion needs one configured.
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, Config: embeddingConfig(), Symbols: &fakeQuerier{}}); err != nil {
		t.Fatalf("RegisterAll returned error: %v", err)
	}

	want := map[string]registry.RiskLevel{
		"file.read":          registry.RiskReadOnly,
		"file.write_patch":   registry.RiskWorkspaceWrite,
		"file.edit":          registry.RiskWorkspaceWrite,
		"file.write":         registry.RiskWorkspaceWrite,
		"repo.search":        registry.RiskReadOnly,
		"symbols":            registry.RiskReadOnly,
		"git.status":         registry.RiskReadOnly,
		"git.diff":           registry.RiskReadOnly,
		"git.log":            registry.RiskReadOnly,
		"git.blame":          registry.RiskReadOnly,
		"shell.run":          registry.RiskCommand,
		"test.run":           registry.RiskCommand,
		"todo.write":         registry.RiskWorkspaceWrite,
		"question.ask":       registry.RiskReadOnly,
		"mode.request":       registry.RiskReadOnly,
		"tools.select":       registry.RiskReadOnly,
		"codebase_search":    registry.RiskReadOnly,
		"config.read":        registry.RiskReadOnly,
		"workspace.worktree": registry.RiskWorkspaceWrite,
		"workspace.finish":   registry.RiskWorkspaceWrite,
	}
	// shell.sudo is registered only on non-Windows platforms (sudo.go is
	// //go:build !windows), so the expectation must be conditional or this
	// test fails on Windows.
	if runtime.GOOS != "windows" {
		want["shell.sudo"] = registry.RiskDestructive
	}
	if got := reg.List(); len(got) != len(want) {
		t.Fatalf("len(List()) = %d, want %d", len(got), len(want))
	}
	for name, risk := range want {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) ok=false", name)
		}
		if tool.Risk != risk {
			t.Fatalf("%s risk = %q, want %q", name, tool.Risk, risk)
		}
		if tool.Handler == nil {
			t.Fatalf("%s Handler is nil", name)
		}
		if len(tool.Schema) == 0 {
			t.Fatalf("%s Schema is empty", name)
		}
	}
}

// TestRegisterAll_NeverRegistersScratchpad pins the Tier 2a removal: the
// scratchpad tools and their context-pack projection are gone from the
// model's surface, whatever session state the registry is given.
func TestRegisterAll_NeverRegistersScratchpad(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, name := range []string{"scratchpad.write", "scratchpad.read", "scratchpad.list", "scratchpad.delete"} {
		if _, ok := reg.Lookup(name); ok {
			t.Errorf("%s must not be registered", name)
		}
	}
}

// TestRegisterAll_NeverRegistersRecallHistory pins the Tier 2a merge:
// recall_history was folded into transcript_read's "query", so it must not
// be registered whatever the rollover config says. The rollover settings
// still exist (they gate archiving and the compaction policy); only the
// separate tool name is gone.
func TestRegisterAll_NeverRegistersRecallHistory(t *testing.T) {
	for _, cfg := range []config.RolloverConfig{
		{Enabled: false, RecallToolEnabled: "always"},
		{Enabled: true, RecallToolEnabled: "always"},
		{Enabled: true, RecallToolEnabled: "auto", Policy: "context_percent"},
	} {
		root := t.TempDir()
		reg := registry.New()
		if err := RegisterAll(reg, Options{
			WorkspaceRoot: root,
			CommandRunner: &fakeRunner{},
			SessionState:  session.New(config.Config{}, root, time.Now(), session.Persistence{}),
			Config: config.Config{
				Session: config.SessionConfig{Rollover: cfg},
			},
		}); err != nil {
			t.Fatalf("RegisterAll returned error: %v", err)
		}
		if _, ok := reg.Lookup("recall_history"); ok {
			t.Errorf("recall_history must not be registered (config %+v)", cfg)
		}
	}
}

func TestRegisterAllRequiresWorkspaceRoot(t *testing.T) {
	err := RegisterAll(registry.New(), Options{CommandRunner: &fakeRunner{}})
	if err == nil {
		t.Fatal("RegisterAll returned nil error, want workspace root error")
	}
	if !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("error = %q, want workspace root", err.Error())
	}
}

func TestFileWritePatchDescriptionMentionsCreationAndChaining(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := reg.Lookup("file.write_patch")
	if !ok {
		t.Fatal("file.write_patch not registered")
	}
	for _, want := range []string{"create", "empty SEARCH", "read before editing", "chain", ">>>>>>> REPLACE"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("file.write_patch description missing %q: %s", want, tool.Description)
		}
	}
}

func TestShellRunDescriptionMentionsWorkspaceCwdAndNativeSearch(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run not registered")
	}
	for _, want := range []string{"workspace root", "current working directory", "repo.search", "symbols", "file.read", "Never use shell.run to create or modify files"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("shell.run description missing %q: %s", want, tool.Description)
		}
	}
}

func TestResolveWorkspacePathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()

	if _, err := resolveWorkspacePath(root, "../outside"); err == nil {
		t.Fatal("resolveWorkspacePath traversal returned nil error")
	}
	if _, err := resolveWorkspacePath(root, root); err == nil {
		t.Fatal("resolveWorkspacePath absolute path returned nil error")
	}
}

func TestLimitOutputTruncatesWithMarker(t *testing.T) {
	got := limitOutput("abcdef", 3)
	if got != "abc\n[output truncated]" {
		t.Fatalf("limitOutput = %q", got)
	}
}

// fakeRunner is the package's simplest runner seam: it records every request
// and returns the canned result. It imposes no execution deadline of its own,
// because managed commands arrive with req.Timeout == 0 and must run to their
// own conclusion.
//
// Like the real runner it publishes a process through OnStart — a managed
// command's outcome depends on whether a process ever started, so a fixture
// that stayed silent would make every non-zero exit look like a startup
// failure. A canned error with no result at all stands for a runner that never
// got that far (an unstartable binary), which is the one case that publishes
// nothing.
type fakeRunner struct {
	requests []CommandRequest
	result   CommandResult
	err      error
}

func (f *fakeRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	f.requests = append(f.requests, req)
	if req.OnStart != nil && !(f.err != nil && f.result == (CommandResult{})) {
		req.OnStart(4242)
	}
	// Write to observers if set, simulating live streaming.
	if req.Stdout != nil && f.result.Stdout != "" {
		req.Stdout.Write([]byte(f.result.Stdout))
	}
	if req.Stderr != nil && f.result.Stderr != "" {
		req.Stderr.Write([]byte(f.result.Stderr))
	}
	return f.result, f.err
}

func invokeTool(t *testing.T, reg *registry.Registry, name string, args string) (registry.ToolResult, error) {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) ok=false", name)
	}
	return tool.Handler(context.Background(), registry.ToolCall{Name: name, Args: []byte(args)})
}

// steeringToolContext opens one steering turn and one open tool operation on
// state and returns the context a native tool handler must run under for that
// operation to be interruptible, plus the turn and operation identities a Send
// now request needs.
//
// interrupt, when non-nil, is installed as the session's operation-interruption
// handler — the seam a conversation uses to reach the command service. A nil
// interrupt installs a handler that returns immediately: these tests drive
// cancellation through SubmitSendNow, which marks the captured operation
// requested (so admission is refused from that instant) and, through the
// interruption worker, cancels the operation's context with ErrSendNow.
func steeringToolContext(t *testing.T, state *session.State, interrupt session.OperationInterrupt) (context.Context, string, session.OperationRef) {
	t.Helper()
	if interrupt == nil {
		interrupt = func(context.Context, session.OperationRef) (session.OperationStop, error) {
			return session.OperationStop{}, nil
		}
	}
	state.SetOperationInterrupt(interrupt)
	turn, err := state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	opCtx, ref, err := state.BeginSteeringOperation(context.Background(), turn, session.OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation: %v", err)
	}
	return opCtx, turn, ref
}

// steerNow submits one Send now request for an open turn, failing the test on
// rejection: acceptance is the precondition every cancellation test here is
// built on.
func steerNow(t *testing.T, state *session.State, turn, requestID string) session.SteeringReceipt {
	t.Helper()
	receipt, err := state.SubmitSendNow(session.SendNowRequest{TurnID: turn, RequestID: requestID, Text: "steer"})
	if err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	if receipt.Status != session.SteeringAccepted {
		t.Fatalf("receipt status = %q, want accepted", receipt.Status)
	}
	return receipt
}

func assertTimeout(t *testing.T, got time.Duration, want time.Duration) {
	t.Helper()
	if got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
}

func TestRegisterAllDefersOnlyLowUseTools(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	deferred := make(map[string]bool)
	for _, tool := range reg.ListDeferred() {
		deferred[tool.Name] = true
	}
	want := map[string]bool{"config.read": true}
	if runtime.GOOS != "windows" {
		want["shell.sudo"] = true
	}
	if len(deferred) != len(want) {
		t.Fatalf("ListDeferred() = %v, want exactly %v", deferred, want)
	}
	for name := range want {
		if !deferred[name] {
			t.Fatalf("expected %s to be deferred", name)
		}
	}
	for _, tool := range reg.List() {
		if strings.HasPrefix(tool.Name, "config.") && tool.Name != "config.read" {
			t.Fatalf("config setter %s must not be registered", tool.Name)
		}
	}
	for _, name := range []string{"file.read", "file.write_patch", "shell.run", "test.run", "repo.search", "tools.select"} {
		if deferred[name] {
			t.Fatalf("expected %s to stay non-deferred", name)
		}
	}
}

// Review focus 3: a resumed session may still name a deleted setter.
func TestToolsSelectRejectsRemovedConfigSetter(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := invokeTool(t, reg, "tools.select", `{"names":["config.agent.set"]}`)
	if err == nil || !strings.Contains(err.Error(), "unknown or non-deferred tools: config.agent.set") {
		t.Fatalf("tools.select error = %v, want unknown-tool rejection", err)
	}
}

func TestToolsSelectLoadsDeferredNativeTool(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		CommandRunner: &fakeRunner{},
		SessionState:  state,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := invokeTool(t, reg, "tools.select", `{"names":["config.read"]}`); err != nil {
		t.Fatalf("tools.select: %v", err)
	}
	found := false
	for _, name := range state.LoadedToolNames() {
		if name == "config.read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("LoadedToolNames() = %v, want config.read present", state.LoadedToolNames())
	}

	// A deferred tool dispatches normally once selected — deferral is a
	// prompt-level concern, not an execution gate.
	if _, err := invokeTool(t, reg, "config.read", `{}`); err != nil {
		t.Fatalf("config.read dispatch after select: %v", err)
	}
}

func TestRegisterAllGatesSymbolsToolOnQuerier(t *testing.T) {
	// Without a querier the symbols tool is not registered at all — it
	// could only ever fail.
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Lookup("symbols"); ok {
		t.Fatal("expected symbols to be omitted when the symbol querier is nil")
	}

	// With a querier it registers normally.
	reg = registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Symbols: &fakeQuerier{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.Lookup("symbols"); !ok {
		t.Fatal("expected symbols to be registered when the symbol querier is set")
	}
}
