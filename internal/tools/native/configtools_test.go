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
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

func TestConfigReadReturnsMaskedConfig(t *testing.T) {
	cfg := config.Default()
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
	if !strings.Contains(res.Content, "api.openai.com") {
		t.Fatalf("expected provider base URL in output, got: %s", res.Content)
	}
}

func TestConfigAgentSetProjectScope(t *testing.T) {
	dir := t.TempDir()
	// SaveProjectConfig writes to the project config path under .marshal/.
	cfgPath := config.ProjectConfigPath(dir)

	cfg := config.Default()

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
		Args: json.RawMessage(`{"max_tool_iterations":30}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "reloaded") {
		t.Fatalf("expected reloaded receipt, got: %s", res.Summary)
	}
	if reloaded == nil {
		t.Fatal("reloader never ran")
	}
	if reloaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("max_tool_iterations not applied: %d", reloaded.Agent.MaxToolIterations)
	}
}

func TestConfigAgentSetRejectsProviderModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()

	ts := toolSet{config: cfg, configPath: cfgPath}
	reg := registry.New()
	tools, err := newConfigToolSet(ts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(tools.configAgentSetTool()); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Lookup("config.agent.set")
	_, err = tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.agent.set",
		Args: json.RawMessage(`{"provider":"openai","model":"gpt-4o"}`),
	})
	if err == nil {
		t.Fatal("config.agent.set must reject provider/model args")
	}
}

func TestConfigAgentSetGlobalScopeForcesApproval(t *testing.T) {
	dir := t.TempDir()
	userPath := config.UserConfigPath(dir)

	cfg := config.Default()

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
		Args: json.RawMessage(`{"scope":"global","max_tool_iterations":30}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(res.Summary, "global") {
		t.Fatalf("expected global in receipt, got: %s", res.Summary)
	}
	if reloaded == nil {
		t.Fatal("reloader never ran")
	}
	if reloaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("global write did not apply: %+v", reloaded.Agent)
	}
	// Global file on disk reflects the change.
	loaded, err := config.Load(config.LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Agent.MaxToolIterations != 30 {
		t.Fatalf("global disk agent scalar missing: %+v", loaded.Agent)
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
		Args: json.RawMessage(`{"scope":"global","max_tool_iterations":30}`),
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

func TestCommandRiskTools(t *testing.T) {
	st := newAutoApproveSessionState()
	testSectionWriteWithState(t, "config.diagnostics.set", (*toolSet).configDiagnosticsSetTool, `{"commands":{"go vet":"go vet ./..."}}`, func(c config.Config) bool { return c.Diagnostics.Commands["go vet"] == "go vet ./..." }, st)
	st = newAutoApproveSessionState()
	testSectionWriteWithState(t, "config.tools.shell.sandbox.set", (*toolSet).configToolsShellSandboxSetTool, `{"memory_limit_mb":512}`, func(c config.Config) bool { return c.Tools.Shell.Sandbox.MemoryLimitMB == 512 }, st)
	st = newAutoApproveSessionState()
	testSectionWriteWithState(t, "config.tools.shell.set", (*toolSet).configToolsShellSetTool, `{"default_timeout_seconds":30}`, func(c config.Config) bool { return c.Tools.Shell.DefaultTimeoutSeconds == 30 }, st)
	st = newAutoApproveSessionState()
	testSectionWriteWithState(t, "config.hooks.set", (*toolSet).configHooksSetTool, `{"fail_closed":true,"entries":[{"event":"pre_tool","command":"echo hi"}]}`, func(c config.Config) bool {
		return c.Hooks.FailClosed && len(c.Hooks.Entries) == 1 && c.Hooks.Entries[0].Command == "echo hi"
	}, st)
}

func newAutoApproveSessionState() *session.State {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
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
	return state
}

func testSectionWriteWithState(t *testing.T, name string, build func(*toolSet) registry.Tool, args string, check func(config.Config) bool, st *session.State) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	var reloaded *config.Config
	ts := toolSet{
		config:         cfg,
		configPath:     cfgPath,
		configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil },
		sessionState:   st,
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

func TestPermissionsSetFiltersSelfGoverningRules(t *testing.T) {
	dir := t.TempDir()
	cfgPath := config.ProjectConfigPath(dir)
	st := newAutoApproveSessionState()
	cfg := config.Default()
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { return nil }, sessionState: st}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configPermissionsSetTool())
	tool, _ := reg.Lookup("config.permissions.set")
	// Include a self-governing rule (config.read) and a normal rule.
	args := `{"rules":[{"permission":"config.read","action":"allow"},{"permission":"shell.run","pattern":"ls","action":"allow"}]}`
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.permissions.set", Args: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	loaded, _ := config.Load(config.LoadOptions{WorkingDir: dir})
	for _, r := range loaded.Permissions.Rules {
		if strings.HasPrefix(r.Permission, "config.") {
			t.Fatalf("self-governing config.* rule must be filtered out, found: %+v", r)
		}
	}
	found := false
	for _, r := range loaded.Permissions.Rules {
		if r.Permission == "shell.run" && r.Pattern == "ls" {
			found = true
		}
	}
	if !found {
		t.Fatal("normal rule must survive filtering")
	}
}

func TestMCPSetPersists(t *testing.T) {
	st := newAutoApproveSessionState()
	testSectionWriteWithState(t, "config.mcp.set", (*toolSet).configMCPSetTool, `{"servers":{"github":{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}}}`, func(c config.Config) bool {
		s, ok := c.MCP.Servers["github"]
		return ok && s.Command == "npx"
	}, st)
}

func TestShellAutoApproveEscalates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	state := session.New(cfg, dir, time.Now(), session.Persistence{})
	approvalRequested := false
	// Auto-approve in a goroutine and record that approval was requested.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := state.PendingApproval(); p != nil {
				approvalRequested = true
				p.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { return nil }, sessionState: state}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configToolsShellSetTool())
	tool, _ := reg.Lookup("config.tools.shell.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.tools.shell.set", Args: json.RawMessage(`{"auto_approve":true}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !approvalRequested {
		t.Fatal("auto_approve=true must force an approval request")
	}
}

func TestSandboxUnsafePassthroughEscalates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	state := session.New(cfg, dir, time.Now(), session.Persistence{})
	approvalRequested := false
	// Auto-approve in a goroutine and record that approval was requested.
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p := state.PendingApproval(); p != nil {
				approvalRequested = true
				p.Respond(session.UserApprovalDecision{Approved: true})
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { return nil }, sessionState: state}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configToolsShellSandboxSetTool())
	tool, _ := reg.Lookup("config.tools.shell.sandbox.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.tools.shell.sandbox.set", Args: json.RawMessage(`{"unsafe_passthrough":true}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !approvalRequested {
		t.Fatal("unsafe_passthrough=true must force an approval request")
	}
}

func TestProvidersSetRejectsLiteralAPIKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configProvidersSetTool())
	tool, _ := reg.Lookup("config.providers.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.providers.set", Args: json.RawMessage(`{"name":"openai","base_url":"https://api.openai.com","api_key":"sk-secret"}`)})
	if err == nil {
		t.Fatal("expected error for literal api_key")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error should mention api_key, got: %v", err)
	}
}

func TestProvidersSetAllowsAPIKeyEnv(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configProvidersSetTool())
	tool, _ := reg.Lookup("config.providers.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.providers.set", Args: json.RawMessage(`{"name":"openai","base_url":"https://api.openai.com","api_key_env":"OPENAI_API_KEY"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if reloaded.Providers["openai"].APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("api_key_env not set: %+v", reloaded.Providers["openai"])
	}
}

func TestProvidersDeleteRemovesKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{"openai": {BaseURL: "https://api.openai.com"}}
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configProvidersDeleteTool())
	tool, _ := reg.Lookup("config.providers.delete")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.providers.delete", Args: json.RawMessage(`{"name":"openai"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, ok := reloaded.Providers["openai"]; ok {
		t.Fatal("provider not deleted")
	}
}

func TestModelsPresetSetRoundTrip(t *testing.T) {
	testSectionWrite(t, "config.models.preset.set", (*toolSet).configModelsPresetSetTool, `{"name":"ollama/qwen2.5-coder","provider":"ollama","model":"qwen2.5-coder"}`, func(c config.Config) bool {
		p, ok := c.Models.Presets["ollama/qwen2.5-coder"]
		return ok && p.Provider == "ollama"
	})
}

func TestModelsPresetSetRejectsNonCanonicalName(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	ts := toolSet{config: cfg, configPath: cfgPath}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configModelsPresetSetTool())
	tool, _ := reg.Lookup("config.models.preset.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "1",
		Name: "config.models.preset.set",
		Args: json.RawMessage(`{"name":"fast","provider":"ollama","model":"qwen"}`),
	})
	if err == nil {
		t.Fatal("config.models.preset.set must reject non-canonical preset names")
	}
}

func TestModelsPresetDeleteRemovesKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{"fast": {Name: "fast", Provider: "ollama", Model: "qwen2.5-coder"}}
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configModelsPresetDeleteTool())
	tool, _ := reg.Lookup("config.models.preset.delete")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.models.preset.delete", Args: json.RawMessage(`{"name":"fast"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, ok := reloaded.Models.Presets["fast"]; ok {
		t.Fatal("preset not deleted")
	}
}

func TestAgentProfilesSetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configAgentProfilesSetTool())
	tool, _ := reg.Lookup("config.agent_profiles.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.agent_profiles.set", Args: json.RawMessage(`{"name":"dev","roles":{"implementer":"fast","reviewer":{"custom_agent":"senior_reviewer"}}}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ap, ok := reloaded.AgentProfiles["dev"]
	if !ok {
		t.Fatal("agent profile not created")
	}
	if ap.Roles[routing.RoleImplementer].Preset != "fast" {
		t.Fatalf("implementer role not set: %+v", ap.Roles)
	}
	if ap.Roles[routing.RoleReviewer].CustomAgent != "senior_reviewer" {
		t.Fatalf("reviewer custom_agent not set: %+v", ap.Roles)
	}
}

func TestAgentProfilesSetOverlaysRoles(t *testing.T) {
	// Regression: setting one role must not wipe the profile's other roles.
	// The tool description promises "Omitted fields are preserved".
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"single": {Name: "single", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/impl"},
			routing.RoleFast:        {Preset: "ollama/fast"},
		}},
	}
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configAgentProfilesSetTool())
	tool, _ := reg.Lookup("config.agent_profiles.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.agent_profiles.set", Args: json.RawMessage(`{"name":"single","roles":{"planner":"ollama/plan"}}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ap, ok := reloaded.AgentProfiles["single"]
	if !ok {
		t.Fatal("agent profile not found")
	}
	if ap.Roles[routing.RoleImplementer].Preset != "ollama/impl" {
		t.Errorf("implementer role was dropped: %+v", ap.Roles)
	}
	if ap.Roles[routing.RoleFast].Preset != "ollama/fast" {
		t.Errorf("fast role was dropped: %+v", ap.Roles)
	}
	if ap.Roles[routing.RolePlanner].Preset != "ollama/plan" {
		t.Errorf("planner role not set: %+v", ap.Roles)
	}
}

func TestAgentProfilesDeleteRemovesKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.AgentProfiles = map[string]routing.AgentProfile{"dev": {Name: "dev"}}
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configAgentProfilesDeleteTool())
	tool, _ := reg.Lookup("config.agent_profiles.delete")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.agent_profiles.delete", Args: json.RawMessage(`{"name":"dev"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, ok := reloaded.AgentProfiles["dev"]; ok {
		t.Fatal("agent profile not deleted")
	}
}

func TestCustomAgentsSetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configCustomAgentsSetTool())
	tool, _ := reg.Lookup("config.custom_agents.set")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.custom_agents.set", Args: json.RawMessage(`{"name":"senior_reviewer","preset":"fast","system_prompt":"You are a senior reviewer","approval_mode":"always"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	ca, ok := reloaded.CustomAgents["senior_reviewer"]
	if !ok {
		t.Fatal("custom agent not created")
	}
	if ca.Preset != "fast" {
		t.Fatalf("preset not set: %+v", ca)
	}
	if ca.SystemPrompt != "You are a senior reviewer" {
		t.Fatalf("system_prompt not set: %+v", ca)
	}
	if ca.ApprovalMode != "always" {
		t.Fatalf("approval_mode not set: %+v", ca)
	}
}

func TestCustomAgentsDeleteRemovesKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	cfg := config.Default()
	cfg.CustomAgents = map[string]routing.CustomAgent{"senior_reviewer": {Name: "senior_reviewer", Preset: "fast"}}
	var reloaded *config.Config
	ts := toolSet{config: cfg, configPath: cfgPath, configReloader: func(c config.Config) error { cc := c; reloaded = &cc; return nil }}
	tools, _ := newConfigToolSet(ts)
	reg := registry.New()
	reg.Register(tools.configCustomAgentsDeleteTool())
	tool, _ := reg.Lookup("config.custom_agents.delete")
	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "config.custom_agents.delete", Args: json.RawMessage(`{"name":"senior_reviewer"}`)})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if _, ok := reloaded.CustomAgents["senior_reviewer"]; ok {
		t.Fatal("custom agent not deleted")
	}
}
