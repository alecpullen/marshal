package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/settings"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
)

func TestRunSkipsProgramAndConfigLoadWhenContextIsCancelled(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runnerCalled := false
	loaderCalled := false
	err = Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			loaderCalled = true
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			runnerCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if loaderCalled {
		t.Fatal("config loader was called after context cancellation")
	}
	if runnerCalled {
		t.Fatal("program runner was called after context cancellation")
	}
}

func TestRunStartsProgram(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	stdout := bytes.NewBuffer(nil)

	called := false
	err = Run(context.Background(), stdout, bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			called = true
			if output != stdout {
				t.Fatal("runner did not receive stdout buffer")
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("program runner was not called")
	}
}

func TestRunPassesAppContextToRunner(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runnerStarted := make(chan struct{})
	runnerObservedCancel := make(chan struct{})

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil),
			WithNow(func() time.Time { return time.Unix(100, 0) }),
			WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
				return config.Default(), nil
			}),
			WithProgramRunner(func(runCtx context.Context, model tea.Model, output io.Writer) error {
				close(runnerStarted)
				<-runCtx.Done()
				close(runnerObservedCancel)
				return nil
			}),
		)
	}()

	<-runnerStarted
	cancel()

	select {
	case <-runnerObservedCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not observe context cancellation")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestWithProgramRunnerNilLeavesRunnerConfigurable(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	called := false

	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(nil),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			called = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("program runner was not called")
	}
}

func TestRoleToolIterationsFallsBack(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.MaxToolIterations = 16
	cfg.Swarm.Budget.ToolIters = map[string]int{"implementer": 25}

	if got := roleToolIterations(cfg, agent.RoleImplementer); got != 25 {
		t.Errorf("implementer cap = %d, want 25 (role-specific)", got)
	}
	if got := roleToolIterations(cfg, agent.RoleTester); got != 16 {
		t.Errorf("tester cap = %d, want 16 (fallback to agent default)", got)
	}
}

func TestReloadAgentRuntimeUpdatesSwarmConfig(t *testing.T) {
	ctx := context.Background()
	initial := reloadableAgentConfig("old-provider")
	initial.Agent.MaxToolIterations = 8
	initial.Swarm.Budget.MaxFixRounds = 1
	initial.Swarm.Budget.MaxTotalTokens = 100

	reloaded := reloadableAgentConfig("new-provider")
	reloaded.Agent.MaxToolIterations = 16
	reloaded.Swarm.Budget.MaxFixRounds = 5
	reloaded.Swarm.Budget.MaxTotalTokens = 90000
	reloaded.Swarm.Budget.ToolIters = map[string]int{"implementer": 25}

	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, swarmRunner, mcpMgr, err := buildAgentRunner(ctx, initial, state, nil, 0, nil)
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}
	if mcpMgr != nil {
		defer mcpMgr.Close()
	}
	if swarmRunner.MaxFixRounds != 1 {
		t.Fatalf("initial MaxFixRounds = %d, want 1", swarmRunner.MaxFixRounds)
	}

	if err := reloadAgentRuntime(ctx, reloaded, state, nil, 0, nil, runner, swarmRunner, &mcpMgr); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if swarmRunner.MaxFixRounds != 5 {
		t.Fatalf("reloaded MaxFixRounds = %d, want 5", swarmRunner.MaxFixRounds)
	}
	if swarmRunner.MaxTotalTokens != 90000 {
		t.Fatalf("reloaded MaxTotalTokens = %d, want 90000", swarmRunner.MaxTotalTokens)
	}
	impl, err := swarmRunner.NewRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("NewRunner implementer: %v", err)
	}
	if impl.MaxToolIterations != 25 {
		t.Fatalf("implementer MaxToolIterations = %d, want 25", impl.MaxToolIterations)
	}
	tester, err := swarmRunner.NewRunner(agent.RoleTester, swarm.ScopeTester)
	if err != nil {
		t.Fatalf("NewRunner tester: %v", err)
	}
	if tester.MaxToolIterations != 16 {
		t.Fatalf("tester MaxToolIterations = %d, want 16", tester.MaxToolIterations)
	}
}

func TestReloadAgentRuntimeManagesMCP(t *testing.T) {
	if os.Getenv("BE_MOCK_SERVER") == "1" {
		mockMCPServer()
		return
	}

	ctx := context.Background()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	initial := reloadableAgentConfig("provider")
	initial.MCP.Servers = map[string]config.MCPServerConfig{
		"mock": {
			Command: exe,
			Args:    []string{"-test.run=TestReloadAgentRuntimeManagesMCP"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
		},
	}

	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, mcpMgr, err := buildAgentRunner(ctx, initial, state, nil, 0, nil)
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}
	if mcpMgr == nil {
		t.Fatal("MCP manager not initialized")
	}
	defer func() {
		if mcpMgr != nil {
			mcpMgr.Close()
		}
	}()

	// Verify tool is registered
	if _, ok := reg.Lookup("mcp.mock.hello"); !ok {
		t.Fatal("MCP tool mcp.mock.hello not registered")
	}

	// Reload with empty MCP config (removes MCP server)
	reloaded := reloadableAgentConfig("provider")
	if err := reloadAgentRuntime(ctx, reloaded, state, nil, 0, nil, runner, swarmRunner, &mcpMgr); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	if mcpMgr != nil {
		t.Fatal("mcpMgr should be nil after reload removes servers")
	}
}

func mockMCPServer() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			Method string      `json:"method"`
			ID     interface{} `json:"id"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		var result interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "mock", "version": "1.0"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "hello", "description": "says hello", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			}
		}
		res := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
		}
		if result != nil {
			res["result"] = result
		}
		_ = enc.Encode(res)
	}
}

func TestRunReturnsInjectedConfigLoadError(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	wantErr := errors.New("load failed")

	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Config{}, wantErr
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			t.Fatal("program runner should not be called on config load failure")
			return nil
		}),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestRunCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	runnerCalled := false
	runner := func(ctx context.Context, model tea.Model, output io.Writer) error {
		runnerCalled = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, bytes.NewBuffer(nil), bytes.NewBuffer(nil), WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithProgramRunner(runner))
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !runnerCalled {
		t.Fatal("program runner was not called")
	}

	dbPath := filepath.Join(dir, ".marshal", "marshal.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("database file was not created at %s", dbPath)
	}
}

func TestRunDisplaysInactiveRouteWhenNoProviderConfigured(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			view = updated.View()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(view, "no model") {
		t.Fatalf("view missing inactive route in status line:\n%s", view)
	}
}

func TestRunDisplaysActiveLegacyRouteWhenAgentConfigured(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	cfg := config.Default()
	cfg.Agent.Provider = "ollama"
	cfg.Agent.Model = "qwen2.5-coder:14b"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "local"},
	}

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return cfg, nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			view = updated.View()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, want := range []string{
		"qwen2.5-coder:14b @ ollama",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
}

func TestRunTriggersKnowledgeEndSessionButSkipsWithNoMessages(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	now := time.Unix(100, 0)
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	database, dberr := db.Open(dbPath(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()

	sessionID := fmt.Sprintf("sess_%d", now.UnixNano())
	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil || got.Summary != "" {
		t.Fatalf("expected no session-end write with no messages, got %#v", got)
	}
}

func TestRunWiresMemoryBrowserOpensWithCtrlK(t *testing.T) {
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			m := model.(tui.Model)
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
			m = updated.(tui.Model)
			view = m.View()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(view, "Project Memories") {
		t.Fatalf("view missing memory browser after Ctrl+K:\n%s", view)
	}
}

func TestRunUsesLiveConfigForShutdownKnowledgePass(t *testing.T) {
	oldServer := newKnowledgeTestServer(t, `{"session_summary":"used old config"}`)
	newServer := newKnowledgeTestServer(t, `{"session_summary":"used reloaded config"}`)

	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	now := time.Unix(100, 0)
	initialCfg := knowledgeEnabledConfig(oldServer.URL, "old-provider")
	reloadedCfg := knowledgeEnabledConfig(newServer.URL, "new-provider")

	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return initialCfg, nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "summarize this session", session.ContentTypePlain)

			m := model.(tui.Model)
			updated, _ := m.Update(settings.SavedMsg{Cfg: reloadedCfg})
			_ = updated.(tui.Model)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	database, dberr := db.Open(dbPath(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()

	sessionID := fmt.Sprintf("sess_%d", now.UnixNano())
	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.Summary != "used reloaded config" {
		t.Fatalf("Summary = %q, want %q", got.Summary, "used reloaded config")
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt is nil, want set")
	}
}

func TestRunReturnsProgramRunnerErrorAfterKnowledgeEndSession(t *testing.T) {
	server := newKnowledgeTestServer(t, `{"session_summary":"runner failed but knowledge ran"}`)

	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	now := time.Unix(100, 0)
	wantErr := errors.New("program runner failed")
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig(server.URL, "test-provider"), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
			return wantErr
		}),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}

	database, dberr := db.Open(dbPath(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()

	sessionID := fmt.Sprintf("sess_%d", now.UnixNano())
	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.Summary != "runner failed but knowledge ran" {
		t.Fatalf("Summary = %q, want %q", got.Summary, "runner failed but knowledge ran")
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt is nil, want set")
	}
}

func TestRunBoundsShutdownKnowledgePass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	now := time.Unix(100, 0)
	previousTimeout := shutdownKnowledgeTimeout
	shutdownKnowledgeTimeout = 25 * time.Millisecond
	defer func() { shutdownKnowledgeTimeout = previousTimeout }()

	start := time.Now()
	err = Run(context.Background(), bytes.NewBuffer(nil), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig(server.URL, "test-provider"), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Run took %s, want shutdown knowledge pass bounded", elapsed)
	}
}

func TestDBMemoryProviderFiltersStaleMemories(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	now := time.Unix(100, 0)
	if err := database.SaveMemory(projectID, "fact", "keep me", "sess_1", now); err != nil {
		t.Fatalf("SaveMemory confirmed failed: %v", err)
	}
	if err := database.SaveMemory(projectID, "fact", "drop me", "sess_1", now); err != nil {
		t.Fatalf("SaveMemory stale failed: %v", err)
	}
	stored, err := database.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("len(stored) = %d, want 2", len(stored))
	}
	staleID := stored[1].ID
	if err := database.SetMemoryConfidence(staleID, db.MemoryConfidenceStale, now.Add(time.Second)); err != nil {
		t.Fatalf("SetMemoryConfidence failed: %v", err)
	}

	provider := dbMemoryProvider{db: database}
	memories, err := provider.Memories(projectID)
	if err != nil {
		t.Fatalf("Memories failed: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("len(memories) = %d, want 1", len(memories))
	}
	if memories[0].Content != "keep me" {
		t.Fatalf("memory content = %q, want %q", memories[0].Content, "keep me")
	}
}

func knowledgeEnabledConfig(baseURL, providerName string) config.Config {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		providerName: {
			Type:    "openai_compatible",
			BaseURL: baseURL,
			APIKey:  "test-key",
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"implementer": {
			Name:      "implementer",
			Provider:  providerName,
			Model:     "impl-model",
			LocalOnly: true,
		},
		"knowledge": {
			Name:      "knowledge",
			Provider:  providerName,
			Model:     "knowledge-model",
			LocalOnly: true,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {
			Name: cfg.Profile.Default,
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "implementer",
				routing.RoleKnowledge:   "knowledge",
			},
		},
	}
	return cfg
}

func reloadableAgentConfig(providerName string) config.Config {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		providerName: {
			Type:    "openai_compatible",
			BaseURL: "http://localhost:11434/v1",
			APIKey:  "test-key",
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:      "coder",
			Provider:  providerName,
			Model:     "test-model",
			LocalOnly: true,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {
			Name: cfg.Profile.Default,
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
				routing.RolePlanner:     "coder",
				routing.RoleRepoScout:   "coder",
				routing.RoleTester:      "coder",
				routing.RoleReviewer:    "coder",
			},
		},
	}
	return cfg
}

func newKnowledgeTestServer(t *testing.T, extraction string) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model == "" {
			t.Fatal("expected model in request")
		}
		if req.Stream {
			t.Fatal("expected non-streaming chat request")
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, extraction)
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func modelState(t *testing.T, model tea.Model) *session.State {
	t.Helper()
	value := reflect.ValueOf(model)
	field := value.FieldByName("state")
	if !field.IsValid() || field.IsNil() {
		t.Fatal("tui model has no state")
	}
	return (*session.State)(unsafe.Pointer(field.Pointer()))
}

func TestRunMockRepoVerification(t *testing.T) {
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer os.Chdir(origWd)

	mockRepoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mockRepoDir, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}

	configToml := `
[project]
name = "mock-project"

[commands]
test = "go test ./..."

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:9999/v1"
api_key = "mock-key"

[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true

[agent_profiles.mock_profile]
router = "mock_preset"
knowledge = "mock_preset"
summarizer = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
planner = "mock_preset"
implementer = "mock_preset"
reviewer = "mock_preset"
security_reviewer = "mock_preset"
`

	if err := os.WriteFile(filepath.Join(mockRepoDir, ".marshal", "config.toml"), []byte(configToml), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chdir(mockRepoDir); err != nil {
		t.Fatalf("chdir to mockRepoDir failed: %v", err)
	}

	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)

	called := false
	err = Run(context.Background(), stdout, stderr,
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) error {
			called = true
			state := modelState(t, model)
			if state == nil {
				t.Fatal("expected non-nil session.State in TUI model")
			}
			if state.Config.Project.Name != "mock-project" {
				t.Errorf("expected project name 'mock-project', got %q", state.Config.Project.Name)
			}
			if state.Config.Commands.Test != "go test ./..." {
				t.Errorf("expected test command 'go test ./...', got %q", state.Config.Commands.Test)
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("program runner was not called")
	}
}
