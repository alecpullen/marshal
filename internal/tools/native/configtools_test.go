package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestConfigReadReturnsMaskedConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"
	cfg.Providers = map[string]config.ProviderConfig{
		"openai": {BaseURL: "https://api.openai.com", APIKey: "sk-secret"},
	}

	reg := registry.New()
	tools, err := newConfigToolSet(toolSet{config: cfg})
	if err != nil {
		t.Fatalf("newConfigToolSet: %v", err)
	}
	if err := reg.Register(tools.configReadTool()); err != nil {
		t.Fatalf("register: %v", err)
	}

	tool, ok := reg.Lookup("config.read")
	if !ok {
		t.Fatal("config.read not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.read", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Content, "\"APIKey\": \"***\"") {
		t.Fatalf("expected masked APIKey in content, got: %s", res.Content)
	}
	if strings.Contains(res.Content, "sk-secret") {
		t.Fatalf("secret leaked into config.read output: %s", res.Content)
	}
	if !strings.Contains(res.Content, "gpt-4o") {
		t.Fatalf("expected model in output, got: %s", res.Content)
	}
}

func TestConfigAgentSetProjectScope(t *testing.T) {
	dir := t.TempDir()
	// SaveProjectConfig writes to the project config path under .marshal/.
	cfgPath := config.ProjectConfigPath(dir)

	cfg := config.Default()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"

	var reloaded *config.Config
	ts := toolSet{
		config:     cfg,
		configPath: cfgPath,
		configReloader: func(c config.Config) error {
			cc := c
			reloaded = &cc
			return nil
		},
	}
	reg := registry.New()
	tools, err := newConfigToolSet(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.configAgentSetTool()); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup("config.agent.set")
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.agent.set",
		Args: json.RawMessage(`{"model":"claude-3.5-sonnet","max_tool_iterations":30}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "reloaded") {
		t.Fatalf("expected reloaded receipt, got: %s", res.Summary)
	}
	if reloaded == nil || reloaded.Agent.Model != "claude-3.5-sonnet" {
		t.Fatalf("reloader did not see new model: %+v", reloaded)
	}
	if reloaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("max_tool_iterations not applied: %d", reloaded.Agent.MaxToolIterations)
	}
	if reloaded.Agent.Provider != "openai" {
		t.Fatalf("provider should be preserved, got: %s", reloaded.Agent.Provider)
	}
	// File on disk reflects the change.
	loaded, err := config.Load(config.LoadOptions{WorkingDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.Model != "claude-3.5-sonnet" {
		t.Fatalf("disk model = %q, want claude-3.5-sonnet", loaded.Agent.Model)
	}
}

func TestConfigAgentSetGlobalScopeForcesApproval(t *testing.T) {
	dir := t.TempDir()
	userPath := config.UserConfigPath(dir)

	cfg := config.Default()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"

	state := session.New(config.Config{}, dir, time.Now(), session.Persistence{})

	// Auto-approve in a goroutine: when a pending approval arrives, approve it.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := state.PendingApproval(); p != nil {
				p.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	var reloaded *config.Config
	ts := toolSet{
		config:         cfg,
		userConfigPath: userPath,
		configReloader: func(c config.Config) error {
			cc := c
			reloaded = &cc
			return nil
		},
		sessionState: state,
	}
	reg := registry.New()
	tools, err := newConfigToolSet(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.configAgentSetTool()); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup("config.agent.set")

	res, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.agent.set",
		Args: json.RawMessage(`{"scope":"global","model":"claude-3.5-sonnet"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "global") {
		t.Fatalf("expected global in receipt, got: %s", res.Summary)
	}
	if reloaded == nil || reloaded.Agent.Model != "claude-3.5-sonnet" {
		t.Fatalf("global write did not apply: %+v", reloaded)
	}
	// Global file on disk reflects the change.
	loaded, err := config.Load(config.LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.Model != "claude-3.5-sonnet" {
		t.Fatalf("global disk model = %q, want claude-3.5-sonnet", loaded.Agent.Model)
	}
}

func testSectionWrite(t *testing.T, name string, build func(*toolSet) registry.Tool, args string, check func(config.Config) bool) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	var reloaded *config.Config
	ts := toolSet{
		config:         cfg,
		configPath:     cfgPath,
		configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil },
	}
	_, _ = newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(build(&ts))
	tool, _ := reg.Lookup(name)
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: name, Args: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("%s handler: %v", name, err)
	}
	if !check(*reloaded) {
		t.Fatalf("%s: check failed on reloaded config %+v", name, reloaded)
	}
}

func TestScalarWorkspaceWriteTools(t *testing.T) {
	testSectionWrite(t, "config.privacy.set", (*toolSet).configPrivacySetTool, `{"redact_secrets":true}`, func(c config.Config) bool { return c.Privacy.RedactSecrets })
	testSectionWrite(t, "config.indexing.set", (*toolSet).configIndexingSetTool, `{"use_treesitter":true}`, func(c config.Config) bool { return c.Indexing.UseTreesitter })
	testSectionWrite(t, "config.web.set", (*toolSet).configWebSetTool, `{"enabled":true}`, func(c config.Config) bool { return c.Web.Enabled })
	testSectionWrite(t, "config.desktop.set", (*toolSet).configDesktopSetTool, `{"enabled":true}`, func(c config.Config) bool { return c.Desktop.Enabled })
	testSectionWrite(t, "config.swarm.set", (*toolSet).configSwarmSetTool, `{"budget":{"max_fix_rounds":9}}`, func(c config.Config) bool { return c.Swarm.Budget.MaxFixRounds == 9 })
	testSectionWrite(t, "config.sdd.set", (*toolSet).configSDDSetTool, `{"auto_worktree":true}`, func(c config.Config) bool { return c.SDD.AutoWorktree })
	testSectionWrite(t, "config.snapshots.set", (*toolSet).configSnapshotsSetTool, `{"enabled":true}`, func(c config.Config) bool { return c.Snapshots.Enabled })
	testSectionWrite(t, "config.tui.set", (*toolSet).configTUISetTool, `{"theme":"dark"}`, func(c config.Config) bool { return c.TUI.Theme == "dark" })
	testSectionWrite(t, "config.session.rollover.set", (*toolSet).configSessionRolloverSetTool, `{"enabled":true}`, func(c config.Config) bool { return c.Session.Rollover.Enabled })
	testSectionWrite(t, "config.lsp.set", (*toolSet).configLSPSetTool, `{"enabled":true}`, func(c config.Config) bool { return c.LSP.Enabled != nil && *c.LSP.Enabled })
	testSectionWrite(t, "config.project.set", (*toolSet).configProjectSetTool, `{"name":"myproj"}`, func(c config.Config) bool { return c.Project.Name == "myproj" })
	testSectionWrite(t, "config.commands.set", (*toolSet).configCommandsSetTool, `{"test":"go test ./..."}`, func(c config.Config) bool { return c.Commands.Test == "go test ./..." })
	testSectionWrite(t, "config.profile.set", (*toolSet).configProfileSetTool, `{"default":"fast"}`, func(c config.Config) bool { return c.Profile.Default == "fast" })
}

func TestConfigAgentSetGlobalScopeDeniedAborts(t *testing.T) {
	dir := t.TempDir()
	userPath := config.UserConfigPath(dir)

	cfg := config.Default()
	cfg.Agent.Model = "gpt-4o"

	state := session.New(config.Config{}, dir, time.Now(), session.Persistence{})

	// Deny in a goroutine.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := state.PendingApproval(); p != nil {
				p.Respond(session.UserApprovalDecision{Approved: false})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	called := false
	ts := toolSet{
		config:         cfg,
		userConfigPath: userPath,
		configReloader: func(c config.Config) error {
			called = true
			return nil
		},
		sessionState: state,
	}
	reg := registry.New()
	tools, err := newConfigToolSet(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.configAgentSetTool()); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup("config.agent.set")

	res, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.agent.set",
		Args: json.RawMessage(`{"scope":"global","model":"claude-3.5-sonnet"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called {
		t.Fatal("reloader must not be called on denial")
	}
	if !strings.Contains(res.Summary, "denied") {
		t.Fatalf("expected denied receipt, got: %s", res.Summary)
	}
}
