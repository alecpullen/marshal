package native

import (
	"context"
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

	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll returned error: %v", err)
	}

	want := map[string]registry.RiskLevel{
		"file.read":          registry.RiskReadOnly,
		"file.page":          registry.RiskReadOnly,
		"file.write_patch":   registry.RiskWorkspaceWrite,
		"file.write":         registry.RiskWorkspaceWrite,
		"repo.search":        registry.RiskReadOnly,
		"repo.index":         registry.RiskReadOnly,
		"repo.map":           registry.RiskReadOnly,
		"repo.card":          registry.RiskReadOnly,
		"symbols.find":       registry.RiskReadOnly,
		"git.status":         registry.RiskReadOnly,
		"git.diff":           registry.RiskReadOnly,
		"git.log":            registry.RiskReadOnly,
		"git.blame":          registry.RiskReadOnly,
		"shell.run":          registry.RiskCommand,
		"test.run":           registry.RiskCommand,
		"todo.write":         registry.RiskWorkspaceWrite,
		"job.output":         registry.RiskReadOnly,
		"job.kill":           registry.RiskCommand,
		"job.list":           registry.RiskReadOnly,
		"question.ask":       registry.RiskReadOnly,
		"ask_user":           registry.RiskReadOnly,
		"mode.request":       registry.RiskReadOnly,
		"diagnostics.check":  registry.RiskReadOnly,
		"tools.select":       registry.RiskReadOnly,
		"codebase_search":    registry.RiskReadOnly,
		"json.query":         registry.RiskReadOnly,
		"csv.inspect":        registry.RiskReadOnly,
		"workspace.worktree": registry.RiskWorkspaceWrite,
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

func TestRegisterAllOmitsScratchpadWithoutSessionState(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, name := range []string{"scratchpad.write", "scratchpad.read", "scratchpad.list", "scratchpad.delete"} {
		if _, ok := reg.Lookup(name); ok {
			t.Fatalf("expected %s to be omitted when SessionState is nil", name)
		}
	}
}

func TestRegisterAllIncludesScratchpadWithSessionState(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, name := range []string{"scratchpad.write", "scratchpad.read", "scratchpad.list", "scratchpad.delete"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("expected %s to be registered when SessionState is set", name)
		}
	}
}

func TestRegisterAll_RecallHistoryGatedByRolloverConfig(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		policy  string
		rollPol string
		want    bool
	}{
		{"rollover disabled", false, "context_percent", "context_percent", false},
		{"rollover enabled + never", true, "never", "context_percent", false},
		{"rollover enabled + always", true, "always", "context_percent", true},
		{"rollover enabled + auto + context_percent", true, "auto", "context_percent", true},
		{"rollover enabled + auto + turn_count", true, "auto", "turn_count", true},
		{"rollover enabled + auto + caller_checkpoint", true, "auto", "caller_checkpoint", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			reg := registry.New()
			opts := Options{
				WorkspaceRoot: root,
				CommandRunner: &fakeRunner{},
				Config: config.Config{
					Session: config.SessionConfig{
						Rollover: config.RolloverConfig{
							Enabled:           tt.enabled,
							RecallToolEnabled: tt.policy,
							Policy:            tt.rollPol,
						},
					},
				},
			}
			if err := RegisterAll(reg, opts); err != nil {
				t.Fatalf("RegisterAll returned error: %v", err)
			}
			_, ok := reg.Lookup("recall_history")
			if ok != tt.want {
				t.Errorf("recall_history present = %v, want %v", ok, tt.want)
			}
		})
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
	for _, want := range []string{"workspace root", "current working directory", "repo.search", "symbols.find", "file.read", "Never use shell.run to create or modify files"} {
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

type fakeRunner struct {
	requests []CommandRequest
	result   CommandResult
	err      error
}

func (f *fakeRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	f.requests = append(f.requests, req)
	if req.Timeout <= 0 {
		return CommandResult{}, context.DeadlineExceeded
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

func assertTimeout(t *testing.T, got time.Duration, want time.Duration) {
	t.Helper()
	if got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
}

type fakeLSPQuerier struct{}

func (fakeLSPQuerier) Definition(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return nil, false
}

func (fakeLSPQuerier) References(ctx context.Context, filePath string, line, col int) ([]string, bool) {
	return nil, false
}

func (fakeLSPQuerier) Hover(ctx context.Context, filePath string, line, col int) (string, bool) {
	return "", false
}

func TestRegisterAllDefersConfigAndDataTools(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  t.TempDir(),
		CommandRunner:  &fakeRunner{},
		SessionState:   state,
		ConfigReloader: func(config.Config) error { return nil },
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	deferred := make(map[string]bool)
	for _, tool := range reg.ListDeferred() {
		deferred[tool.Name] = true
	}

	// Derive the expected deferred set from the registry instead of
	// hard-coding counts, so adding/removing a config.* tool fails with an
	// actionable message rather than a stale magic number. The invariant is:
	// every registered config.* tool is deferred, and the deferred set is
	// exactly {all config.*} ∪ {csv.inspect, json.query}.
	all := make(map[string]bool)
	for _, tool := range reg.List() {
		all[tool.Name] = true
	}
	configNames := []string{}
	for name := range all {
		if strings.HasPrefix(name, "config.") {
			configNames = append(configNames, name)
			if !deferred[name] {
				t.Fatalf("expected config.* tool %s to be deferred", name)
			}
		}
	}

	wantDeferred := map[string]bool{"csv.inspect": true, "json.query": true}
	for _, name := range configNames {
		wantDeferred[name] = true
	}
	if len(deferred) != len(wantDeferred) {
		t.Fatalf("len(ListDeferred()) = %d, want %d (config.* + csv.inspect + json.query)", len(deferred), len(wantDeferred))
	}
	for name := range wantDeferred {
		if !deferred[name] {
			t.Fatalf("expected %s to be deferred", name)
		}
	}

	// Core tools are never deferred.
	for _, name := range []string{"file.read", "file.write_patch", "shell.run", "test.run", "repo.search", "tools.select"} {
		if deferred[name] {
			t.Fatalf("expected %s to stay non-deferred", name)
		}
	}
}

func TestToolsSelectLoadsDeferredNativeTool(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:  t.TempDir(),
		CommandRunner:  &fakeRunner{},
		SessionState:   state,
		ConfigReloader: func(config.Config) error { return nil },
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

func TestRegisterAllGatesLSPToolsOnQuerier(t *testing.T) {
	lspNames := []string{"references", "definition", "hover"}

	// Without a querier the LSP tools are not registered at all — they
	// could only ever return "no lsp".
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, name := range lspNames {
		if _, ok := reg.Lookup(name); ok {
			t.Fatalf("expected %s to be omitted when LSP querier is nil", name)
		}
	}

	// With a querier they register normally.
	reg = registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, LSP: fakeLSPQuerier{}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, name := range lspNames {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("expected %s to be registered when LSP querier is set", name)
		}
	}
}
