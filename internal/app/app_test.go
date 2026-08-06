package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/desktop"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
)

type fakeTrustResolver struct {
	decision trust.Decision
}

func (f *fakeTrustResolver) Resolve(workingDir string, hasProjectConfig bool) (trust.Decision, error) {
	return f.decision, nil
}

func (f *fakeTrustResolver) Record(workingDir string, decision trust.Decision) error {
	return nil
}

func TestStartRuntimeDoesNotRunTUI(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[project]
name = "runtime-test"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true

[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime() error = %v", err)
	}
	defer rt.Close(context.Background())
	if rt.State == nil || rt.SessionID == "" {
		t.Fatalf("runtime not initialized: %+v", rt)
	}
}

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
	err = Run(ctx, bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			loaderCalled = true
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			runnerCalled = true
			return ProgramResult{}
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
	err = Run(context.Background(), stdout,
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			called = true
			if output != stdout {
				t.Fatal("runner did not receive stdout buffer")
			}
			return ProgramResult{}
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
		errCh <- Run(ctx, bytes.NewBuffer(nil),
			WithNow(func() time.Time { return time.Unix(100, 0) }),
			WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
				return config.Default(), nil
			}),
			WithProgramRunner(func(runCtx context.Context, model tea.Model, output io.Writer) ProgramResult {
				close(runnerStarted)
				<-runCtx.Done()
				close(runnerObservedCancel)
				return ProgramResult{}
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

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(nil),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			called = true
			return ProgramResult{}
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

func TestResolveActionDecodingFallsBackGracefully(t *testing.T) {
	tests := []struct {
		name        string
		toolCalling string
		caps        schema.ProviderCapabilities
		wantNative  bool
		wantRF      string
	}{
		{
			name:        "native when provider supports tool calling",
			toolCalling: "native",
			caps:        schema.ProviderCapabilities{ToolCalling: true, StructuredOutput: true, JSONMode: true},
			wantNative:  true,
		},
		{
			name:        "native falls back to json_schema",
			toolCalling: "native",
			caps:        schema.ProviderCapabilities{StructuredOutput: true, JSONMode: true},
			wantRF:      "json_schema",
		},
		{
			name:        "native falls back to json_object",
			toolCalling: "native",
			caps:        schema.ProviderCapabilities{JSONMode: true},
			wantRF:      "json_object",
		},
		{
			name:        "native falls back to nil",
			toolCalling: "native",
			caps:        schema.ProviderCapabilities{},
		},
		{
			name:        "json_schema preferred",
			toolCalling: "json_schema",
			caps:        schema.ProviderCapabilities{StructuredOutput: true, JSONMode: true},
			wantRF:      "json_schema",
		},
		{
			name:        "json_schema falls back to json_object",
			toolCalling: "json_schema",
			caps:        schema.ProviderCapabilities{JSONMode: true},
			wantRF:      "json_object",
		},
		{
			name:        "json leaves decoding unconstrained",
			toolCalling: "json",
			caps:        schema.ProviderCapabilities{StructuredOutput: true, JSONMode: true},
		},
		{
			name:        "empty leaves decoding unconstrained",
			toolCalling: "",
			caps:        schema.ProviderCapabilities{ToolCalling: true, StructuredOutput: true, JSONMode: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveActionDecoding(tt.toolCalling, tt.caps)
			if got.Native != tt.wantNative {
				t.Fatalf("Native = %v, want %v", got.Native, tt.wantNative)
			}
			if tt.wantRF == "" {
				if got.ResponseFormat != nil {
					t.Fatalf("ResponseFormat = %+v, want nil", got.ResponseFormat)
				}
				return
			}
			if got.ResponseFormat == nil || got.ResponseFormat.Type != tt.wantRF {
				t.Fatalf("ResponseFormat = %+v, want type %q", got.ResponseFormat, tt.wantRF)
			}
		})
	}
}

func TestBuildAgentRunnerSetsNativeToolsFromProviderCapability(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("native-provider")

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if !runner.NativeTools {
		t.Fatalf("runner.NativeTools = false, want true")
	}
	if runner.ResponseFormat != nil {
		t.Fatalf("runner.ResponseFormat = %+v, want nil in native mode", runner.ResponseFormat)
	}
}

func TestBuildAgentRunnerFallsBackWhenProviderLacksToolCalling(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("non-native-provider")
	cfg.Providers["non-native-provider"] = config.ProviderConfig{
		Type:    "openai_compatible",
		BaseURL: "http://localhost:11434/v1",
		APIKey:  "test-key",
	}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.NativeTools {
		t.Fatalf("runner.NativeTools = true, want false when provider lacks capability")
	}
	if runner.ResponseFormat == nil || runner.ResponseFormat.Type != "json_schema" {
		t.Fatalf("runner.ResponseFormat = %+v, want json_schema fallback when provider lacks capability", runner.ResponseFormat)
	}
}

func TestBuildAgentRunnerBackgroundShellUsesConfiguredSandbox(t *testing.T) {
	// Background jobs must honour the configured sandbox backend. With
	// restricted mode and an explicit-empty env allowlist, parent env
	// secrets must not leak into the background job's environment.
	ctx := context.Background()
	cfg := nativeToolAgentConfig("test-provider")
	cfg.Tools.Shell.Sandbox.Backend = "restricted"
	cfg.Tools.Shell.Sandbox.EnvAllowlist = []string{} // explicit empty: only PATH

	t.Setenv("MARSHAL_TEST_SECRET", "super-secret-value-avoid-leak")

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, _, _, _, jobMgr, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	_ = runner
	_ = reg

	id, err := jobMgr.Start(ctx, "echo $MARSHAL_TEST_SECRET", 5*time.Second)
	if err != nil {
		t.Fatalf("jobMgr.Start: %v", err)
	}

	// Wait for the short-lived job to complete.
	time.Sleep(500 * time.Millisecond)

	info, output, err := jobMgr.Output(id, 0)
	if err != nil {
		t.Fatalf("jobMgr.Output: %v", err)
	}
	if info.Sandbox.Backend != "restricted" {
		t.Fatalf("job sandbox backend = %q, want %q", info.Sandbox.Backend, "restricted")
	}
	if strings.Contains(output, "super-secret-value-avoid-leak") {
		t.Fatal("background job output contains secret that should have been scrubbed by restricted sandbox with explicit-empty env allowlist")
	}
}

func TestBuildAgentRunnerUsesConfiguredOutputLimit(t *testing.T) {
	// Background jobs must honour MaxOutputBytes from config.
	ctx := context.Background()
	cfg := nativeToolAgentConfig("test-provider")
	cfg.Tools.Shell.MaxOutputBytes = 8

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, _, _, _, jobMgr, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	_ = runner
	_ = reg

	// Start a background job that produces more than 8 bytes of output.
	id, err := jobMgr.Start(ctx, "echo aaaaaaaa bbbbbbbb cccccccc dddddddd", 5*time.Second)
	if err != nil {
		t.Fatalf("jobMgr.Start: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	info, output, err := jobMgr.Output(id, 0)
	if err != nil {
		t.Fatalf("jobMgr.Output: %v", err)
	}
	if !info.OutputTruncated {
		t.Fatal("expected output truncation for noisy background job")
	}
	// Each stream (stdout, stderr) is bounded by MaxOutputBytes.
	// The combined output also has fixed overhead from formatCommandOutput
	// ("stdout:\n" + "\n\nstderr:\n" labels) plus the truncation marker.
	const (
		truncationMarkerLen = len("\n[output truncated]") // 19
		stdoutLabelLen      = len("stdout:\n")            // 8
		stderrLabelLen      = len("\n\nstderr:\n")        // 10
	)
	maxExpected := 2*cfg.Tools.Shell.MaxOutputBytes + stdoutLabelLen + stderrLabelLen + truncationMarkerLen
	if len(output) > maxExpected {
		t.Fatalf("output length = %d, want <= %d (2*MaxOutputBytes + format overhead + truncation marker)", len(output), maxExpected)
	}
}

func TestStartRuntimeOwnsJobManager(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[project]
name = "jm-test"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
tool_calling = true

[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true

[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.JobManager == nil {
		t.Fatal("Runtime.JobManager is nil")
	}

	// Prove the manager works: start a job and list it.
	id, err := rt.JobManager.Start(ctx, "echo job-manager-test", 5*time.Second)
	if err != nil {
		t.Fatalf("JobManager.Start: %v", err)
	}

	// The job.list tool must see the same job (proving pointer identity
	// between Runtime.JobManager and the toolset's job manager).
	tool, ok := rt.ToolRegistry.Lookup("job.list")
	if !ok {
		t.Fatal("job.list not registered in ToolRegistry")
	}
	result, err := tool.Handler(ctx, registry.ToolCall{Args: json.RawMessage("{}")})
	if err != nil {
		t.Fatalf("job.list handler: %v", err)
	}
	if !strings.Contains(result.Content, id) {
		t.Fatalf("job.list output %q does not contain job id %q", result.Content, id)
	}
}

func TestReloadAgentRuntimeReplacesReachableManagerWhenIdle(t *testing.T) {
	ctx := context.Background()

	initialCfg := nativeToolAgentConfig("test-provider")
	reloadedCfg := nativeToolAgentConfig("test-provider")

	state := session.New(initialCfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("initial buildAgentRunner: %v", err)
	}

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		MCPManager:   nil, // no MCP servers configured; use nil interface
		Snapshot:     nil,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	if rt.JobManager == nil {
		t.Fatal("initial JobManager is nil")
	}

	// Capture the old manager pointer before reload.
	oldMgr := rt.JobManager

	// Reload — must build a new manager and swap it in.
	if err := reloadAgentRuntime(ctx, reloadedCfg, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	// The runtime pointer must change.
	if rt.JobManager == oldMgr {
		t.Fatal("JobManager pointer did not change after reload")
	}

	// The old manager must reject new starts (it was shut down).
	_, err = oldMgr.Start(ctx, "echo should-fail", time.Second)
	if err == nil {
		t.Fatal("expected old JobManager to reject Start after shutdown")
	}
	if !errors.Is(err, native.ErrJobManagerClosed) {
		t.Fatalf("old JobManager error = %v, want ErrJobManagerClosed", err)
	}

	// The new manager must execute jobs.
	id, err := rt.JobManager.Start(ctx, "echo hello-new-manager", 5*time.Second)
	if err != nil {
		t.Fatalf("new JobManager.Start: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	info, _, err := rt.JobManager.Output(id, 0)
	if err != nil {
		t.Fatalf("new JobManager.Output: %v", err)
	}
	if info.Status != native.StatusCompleted {
		t.Fatalf("new job status = %s, want completed", info.Status)
	}
}

func nativeToolAgentConfig(providerName string) config.Config {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		providerName: {
			Type:        "openai_compatible",
			BaseURL:     "http://localhost:11434/v1",
			APIKey:      "test-key",
			ToolCalling: true,
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"native-coder": {
			Name:        "native-coder",
			Provider:    providerName,
			Model:       "test-model",
			ToolCalling: "native",
			LocalOnly:   true,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {
			Name: cfg.Profile.Default,
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "native-coder"},
				routing.RolePlanner:     {Preset: "native-coder"},
				routing.RoleRepoScout:   {Preset: "native-coder"},
				routing.RoleTester:      {Preset: "native-coder"},
				routing.RoleReviewer:    {Preset: "native-coder"},
			},
		},
	}
	return cfg
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
	runner, reg, swarmRunner, mcpMgr, _, jobMgr, _, _, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}
	if mcpMgr != nil {
		defer mcpMgr.Close()
	}
	if swarmRunner.MaxFixRounds != 1 {
		t.Fatalf("initial MaxFixRounds = %d, want 1", swarmRunner.MaxFixRounds)
	}

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		MCPManager:   nil, // no MCP servers configured; use nil interface
		Snapshot:     nil,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if rt.SwarmRunner.MaxFixRounds != 5 {
		t.Fatalf("reloaded MaxFixRounds = %d, want 5", rt.SwarmRunner.MaxFixRounds)
	}
	if rt.SwarmRunner.MaxTotalTokens != 90000 {
		t.Fatalf("reloaded MaxTotalTokens = %d, want 90000", rt.SwarmRunner.MaxTotalTokens)
	}
	impl, err := rt.SwarmRunner.NewRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("NewRunner implementer: %v", err)
	}
	if impl.MaxToolIterations != 25 {
		t.Fatalf("implementer MaxToolIterations = %d, want 25", impl.MaxToolIterations)
	}
	tester, err := rt.SwarmRunner.NewRunner(agent.RoleTester, swarm.ScopeTester)
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
			Trust:   "unrestricted",
		},
	}

	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, mcpMgr, _, jobMgr, _, _, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}
	if mcpMgr == nil {
		t.Fatal("MCP manager not initialized")
	}

	// Verify tool is registered
	if _, ok := reg.Lookup("mcp.mock.hello"); !ok {
		t.Fatal("MCP tool mcp.mock.hello not registered")
	}

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		MCPManager:   nil, // no MCP servers configured; use nil interface
		Snapshot:     nil,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	// Reload with empty MCP config (removes MCP server)
	reloaded := reloadableAgentConfig("provider")
	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	if rt.MCPManager != nil {
		t.Fatal("MCPManager should be nil after reload removes servers")
	}
}

func TestReloadAgentRuntimeRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	initialCfg := reloadableAgentConfig("test-provider")
	state := session.New(initialCfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("initial buildAgentRunner: %v", err)
	}

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	originalConfig := rt.State.Config
	originalRunner := rt.Runner
	originalReg := rt.ToolRegistry

	// Build a config that buildAgentRunner will reject: the preset
	// references a provider name that is not in the Providers map.
	badCfg := reloadableAgentConfig("nonexistent-provider")
	badCfg.Providers = map[string]config.ProviderConfig{
		"test-provider": {
			Type:    "openai_compatible",
			BaseURL: "http://localhost:11434/v1",
			APIKey:  "test-key",
		},
	}
	// The preset "coder" references "nonexistent-provider" which is not
	// in badCfg.Providers — buildAgentRunner will fail at Resolve.

	// Capture slog output to verify the Warn emission.
	var logBuf bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(oldLogger)

	err = reloadAgentRuntime(ctx, badCfg, rt)
	if err == nil {
		t.Fatal("expected reload to fail with bad provider config")
	}

	if !reflect.DeepEqual(rt.State.Config, originalConfig) {
		t.Fatal("state.Config was mutated despite build failure")
	}
	if rt.Runner != originalRunner {
		t.Fatal("Runner was replaced despite build failure")
	}
	if rt.ToolRegistry != originalReg {
		t.Fatal("ToolRegistry was replaced despite build failure")
	}

	// Assert the Warn log line was emitted.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "reload: dry-run build failed; keeping previous config") {
		t.Fatalf("expected warn log about reload failure, got: %s", logOutput)
	}

	// Assert the TUI message was added.
	msgs := rt.State.Messages()
	found := false
	for _, m := range msgs {
		if m.Role == session.RoleSystem && strings.Contains(m.Content, "Config reload failed; keeping previous settings.") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected TUI message about reload failure, none found")
	}
}

func TestReloadAgentRuntimeSwapsPipelineFactory(t *testing.T) {
	ctx := context.Background()
	initial := reloadableAgentConfig("old-provider")

	reloaded := reloadableAgentConfig("new-provider")

	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, pipelineFactory, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}
	if pipelineFactory == nil {
		t.Fatal("pipeline factory should not be nil")
	}

	rt := &Runtime{
		Runner:          runner,
		ToolRegistry:    reg,
		SwarmRunner:     swarmRunner,
		PipelineFactory: pipelineFactory,
		JobManager:      jobMgr,
		State:           state,
		workCtx:         ctx,
	}

	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if rt.PipelineFactory == nil {
		t.Fatal("pipeline factory should not be nil after reload")
	}
}

func TestBuildPipelineControllerReturnsAdapter(t *testing.T) {
	cfg := config.Default()
	cfg.SDD.PlansDir = t.TempDir()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "local_balanced"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleSDDImplementer: {Preset: "fast"},
		}},
	}
	planDir := t.TempDir()
	planPath := planDir + "/test-plan.md"
	if err := os.WriteFile(planPath, []byte("# Test Plan\n\n## Global Constraints\n\nNone.\n\n### Task 1: Do something\n\nDo the thing.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	state := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	resolver := newRoutedProviderResolver(cfg, t.TempDir())
	adapter := buildPipelineController(cfg, state, reg, pol, resolver, nil, 1, nil, planPath)
	if adapter == nil {
		t.Fatal("buildPipelineController returned nil")
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

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Config{}, wantErr
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			t.Fatal("program runner should not be called on config load failure")
			return ProgramResult{}
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
	runner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		runnerCalled = true
		return ProgramResult{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, bytes.NewBuffer(nil), WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithProgramRunner(runner), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !runnerCalled {
		t.Fatal("program runner was not called")
	}

	dbPath := db.Path(dir)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("database file was not created at %s", dbPath)
	}
}

// TestRunLogsToFileNotTerminal guards against logs corrupting the Bubble Tea
// alt-screen: the logger must write to .marshal/marshal.log, never to the
// stdout/stderr writers the TUI paints to.
func TestRunLogsToFileNotTerminal(t *testing.T) {
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

	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, stdout, WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		return ProgramResult{}
	}), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if strings.Contains(stdout.String(), "marshal started") || strings.Contains(stderr.String(), "marshal started") {
		t.Fatalf("log output leaked to the terminal writers:\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	}

	logPath := filepath.Join(dir, ".marshal", "marshal.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "marshal started") {
		t.Fatalf("expected startup log in %s, got %q", logPath, string(data))
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
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			view = updated.View().Content
			return ProgramResult{}
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
	cfg.Profile.Default = "single"
	cfg.Agent.Provider = ""
	cfg.Agent.Model = ""
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/qwen2.5-coder:14b": {
			Name:      "ollama/qwen2.5-coder:14b",
			Provider:  "ollama",
			Model:     "qwen2.5-coder:14b",
			LocalOnly: true,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"single": routing.SingleModelProfile("single", "ollama/qwen2.5-coder:14b"),
	}
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "local"},
	}

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return cfg, nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			view = updated.View().Content
			return ProgramResult{}
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
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	database, dberr := db.Open(db.Path(dir))
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
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			m := model.(tui.Model)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = updated.(tui.Model)
			updated, _ = m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
			m = updated.(tui.Model)
			view = m.View().Content
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(view, "Memory") {
		t.Fatalf("view missing memory browser after Ctrl+K:\n%s", view)
	}
}

// TestRunResumesExistingSession verifies that when the program runner asks
// to resume a different session, Run tears down the current runtime and
// restarts with the existing session loaded.
func TestRunResumesExistingSession(t *testing.T) {
	dir := t.TempDir()
	// Run() resolves the user config from the real home dir, so without an
	// isolated HOME the developer's own config leaks into the assertion —
	// a populated skills.autoload, for one, prepends skill bodies to the
	// transcript and pushes the resumed message out of the captured view.
	t.Setenv("HOME", t.TempDir())
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

	// Bootstrap the database and project by starting the runtime once,
	// then seed a second session with a persisted message to resume.
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := StartRuntime(ctx,
		WithWorkingDir(dir),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	)
	if err != nil {
		cancel()
		t.Fatalf("StartRuntime: %v", err)
	}
	database := must[*db.DB](rt.DB)
	projectID := rt.ProjectID
	resumeSessionID := "sess_to_resume"
	startedAt := time.Unix(1000, 0)
	if err := database.CreateSession(resumeSessionID, projectID, "Resumed session", startedAt); err != nil {
		_ = rt.Close(context.Background())
		cancel()
		t.Fatalf("create session: %v", err)
	}
	if _, err := database.SaveMessage(resumeSessionID, string(session.RoleUser), "hello from the past", string(session.ContentTypePlain), startedAt, "", 0, true, 0); err != nil {
		_ = rt.Close(context.Background())
		cancel()
		t.Fatalf("save message: %v", err)
	}
	if err := rt.Close(context.Background()); err != nil {
		cancel()
		t.Fatalf("close runtime: %v", err)
	}
	cancel()

	var runs int
	var resumedView string
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithWorkingDir(dir),
		WithNow(func() time.Time { return time.Unix(200, 0) }),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			runs++
			if runs == 1 {
				// First run: ask to resume the pre-seeded session.
				return ProgramResult{ResumeSession: resumeSessionID}
			}
			// Second run: render the model and capture the transcript view.
			m := model.(tui.Model)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			resumedView = updated.View().Content
			return ProgramResult{}
		}),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runs != 2 {
		t.Fatalf("expected Run to start the program twice, got %d", runs)
	}
	if !strings.Contains(ansi.Strip(resumedView), "hello from the past") {
		t.Fatalf("resumed session transcript not rendered; view:\n%s", resumedView)
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

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return initialCfg, nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "summarize this session", session.ContentTypePlain)

			m := model.(tui.Model)
			updated, _ := m.Update(settings.ChangedMsg{Cfg: reloadedCfg})
			_ = updated.(tui.Model)
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	database, dberr := db.Open(db.Path(dir))
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
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig(server.URL, "test-provider"), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
			return ProgramResult{Err: wantErr}
		}),
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}

	database, dberr := db.Open(db.Path(dir))
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
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig(server.URL, "test-provider"), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
			return ProgramResult{}
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
	if err := database.CreateSession("sess_1", projectID, "", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
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
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "implementer"},
				routing.RoleKnowledge:   {Preset: "knowledge"},
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
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "coder"},
				routing.RolePlanner:     {Preset: "coder"},
				routing.RoleRepoScout:   {Preset: "coder"},
				routing.RoleTester:      {Preset: "coder"},
				routing.RoleReviewer:    {Preset: "coder"},
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

	called := false
	err = Run(context.Background(), stdout,
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("program runner was not called")
	}
}

func TestRunQuiescesBeforeKnowledgeAndClosesAfter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"quiesce-test\"\n"), 0644); err != nil {
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

	now := time.Unix(100, 0)

	// Program runner: start tracked work and add a message so knowledge
	// has something to summarise. The work ends when quiesce cancels
	// the work context (observed via state.Done()).
	var statePtr *session.State
	workEnded := make(chan struct{})

	programRunner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		statePtr = modelState(t, model)
		statePtr.AddMessage(session.RoleUser, "summarise this turn", session.ContentTypePlain)
		if err := statePtr.BeginWork(); err != nil {
			return ProgramResult{Err: err}
		}
		go func() {
			<-statePtr.Done() // closed by State.Shutdown during Quiesce
			statePtr.EndWork()
			close(workEnded)
		}()
		return ProgramResult{}
	}

	// Hook: verify during the knowledge window that the session is
	// quiesced (no new work can start) and the DB is still usable.
	knowledgeRan := make(chan struct{})
	knowledgeHook := func(hookCtx context.Context, st *session.State, database *db.DB) {
		// Verify quiesce: BeginWork should be rejected.
		if err := st.BeginWork(); !errors.Is(err, session.ErrSessionQuiescing) {
			t.Errorf("BeginWork during knowledge = %v, want ErrSessionQuiescing", err)
		}

		// Verify DB is usable.
		_, err := database.GetOrCreateProject(dir, "knowledge-hook-check")
		if err != nil {
			t.Errorf("DB unusable during knowledge: %v", err)
		}

		close(knowledgeRan)
	}

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithProgramRunner(programRunner),
		WithKnowledgeHook(knowledgeHook),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify work completed.
	select {
	case <-workEnded:
	default:
		t.Fatal("work was not completed before Run returned")
	}

	// Verify knowledge ran.
	select {
	case <-knowledgeRan:
	default:
		t.Fatal("knowledge hook was not called")
	}

	// Verify the database was usable for the knowledge pass: the
	// session should have been ended with the knowledge summary.
	database, dberr := db.Open(db.Path(dir))
	if dberr != nil {
		t.Fatalf("open db: %v", dberr)
	}
	defer database.Close()

	sessionID := fmt.Sprintf("sess_%d", now.UnixNano())
	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Summary != "" {
		t.Logf("session summary written: %q", got.Summary)
	}
	if got.EndedAt != nil {
		t.Logf("session ended at: %v", got.EndedAt)
	}
}

func TestRunReloadsAfterInlineTrust(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"),
		[]byte("[project]\nname = \"trusted-inline\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)
	t.Setenv("HOME", t.TempDir()) // keep the real trust store out of the test

	var runs int
	programRunner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		runs++
		st := modelState(t, model)
		switch runs {
		case 1:
			if st.Config.Project.Name == "trusted-inline" {
				t.Error("phase 1 applied the project config before trust")
			}
			// Answer "1" (trust permanently) through the model's Update.
			if _, cmd := model.(interface {
				Update(tea.Msg) (tea.Model, tea.Cmd)
			}).Update(tea.KeyPressMsg{Code: '1', Text: "1"}); cmd != nil {
				_ = cmd() // would be tea.Quit under a real program
			}
		case 2:
			if st.Config.Project.Name != "trusted-inline" {
				t.Errorf("phase 2 config name = %q, want trusted-inline", st.Config.Project.Name)
			}
		}
		return ProgramResult{}
	}

	var nowUnix int64 = 100
	err := Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { nowUnix++; return time.Unix(nowUnix, 0) }),
		WithProgramRunner(programRunner),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs != 2 {
		t.Fatalf("program ran %d times, want 2 (initial + trust reload)", runs)
	}
}

// ── Task 3: existing-session mode ──────────────────────────────────────

func TestStartRuntimeLoadsExistingSessionWithoutDuplicateInsert(t *testing.T) {
	tmp := t.TempDir()
	// Isolate from the developer's real user config: an autoloaded skill
	// adds a system message and throws off the transcript count.
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	configContent := `[project]
name = "existing-test"
[profile]
default = "mock_profile"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true
[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	now := time.Unix(100, 0)

	// First runtime: create a session and persist messages.
	rt1, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithNow(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatalf("first StartRuntime: %v", err)
	}

	rt1.State.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	rt1.State.AddMessage(session.RoleAssistant, "hi there", session.ContentTypePlain)
	expectedLeaf := rt1.State.LeafID()
	expectedMessages := rt1.State.Messages()
	sessionID := rt1.SessionID

	rt1.Close(ctx)

	// Count rows via direct DB.
	countDB, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db for counting: %v", err)
	}
	var initialProjectCount, initialSessionCount, initialMessageCount int
	countDB.SQLDB().QueryRow("SELECT COUNT(*) FROM projects").Scan(&initialProjectCount)
	countDB.SQLDB().QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&initialSessionCount)
	countDB.SQLDB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&initialMessageCount)
	countDB.Close()

	// Second runtime with WithExistingSession.
	rt2, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithNow(func() time.Time { return time.Unix(200, 0) }),
		WithExistingSession(sessionID),
	)
	if err != nil {
		t.Fatalf("second StartRuntime with WithExistingSession: %v", err)
	}
	defer rt2.Close(ctx)

	gotMessages := rt2.State.Messages()
	if len(gotMessages) != len(expectedMessages) {
		t.Fatalf("existing session messages = %d, want %d", len(gotMessages), len(expectedMessages))
	}
	for i, m := range gotMessages {
		if m.Content != expectedMessages[i].Content || m.Role != expectedMessages[i].Role {
			t.Fatalf("message[%d] = %+v, want %+v", i, m, expectedMessages[i])
		}
	}
	if got := rt2.State.LeafID(); got != expectedLeaf {
		t.Fatalf("LeafID = %d, want %d", got, expectedLeaf)
	}
	if rt2.SessionID != sessionID {
		t.Fatalf("SessionID = %q, want %q", rt2.SessionID, sessionID)
	}

	// Verify counts unchanged.
	countDB2, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db for recount: %v", err)
	}
	defer countDB2.Close()
	var projectCount, sessionCount, messageCount int
	countDB2.SQLDB().QueryRow("SELECT COUNT(*) FROM projects").Scan(&projectCount)
	countDB2.SQLDB().QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&sessionCount)
	countDB2.SQLDB().QueryRow("SELECT COUNT(*) FROM messages").Scan(&messageCount)
	if projectCount != initialProjectCount {
		t.Fatalf("project count = %d, want %d (unchanged)", projectCount, initialProjectCount)
	}
	if sessionCount != initialSessionCount {
		t.Fatalf("session count = %d, want %d (unchanged)", sessionCount, initialSessionCount)
	}
	if messageCount != initialMessageCount {
		t.Fatalf("message count = %d, want %d (unchanged)", messageCount, initialMessageCount)
	}
}

func TestStartRuntimeExistingSessionMissingDoesNotCreate(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	configContent := `[project]
name = "missing-test"
[profile]
default = "mock_profile"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true
[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Create the project row via direct DB (no session).
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	if _, err := database.GetOrCreateProject(tmp, "missing-test"); err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	database.Close()

	// Count sessions before.
	countDB, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db for count: %v", err)
	}
	var initialSessionCount int
	countDB.SQLDB().QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&initialSessionCount)
	countDB.Close()

	ctx := context.Background()
	_, err = StartRuntime(ctx, WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithExistingSession("nonexistent-session-id"),
	)
	if err == nil {
		t.Fatal("expected error for nonexistent existing session, got nil")
	}

	// Verify no session was created.
	countDB2, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db for second count: %v", err)
	}
	defer countDB2.Close()
	var sessionCount int
	countDB2.SQLDB().QueryRow("SELECT COUNT(*) FROM agent_sessions").Scan(&sessionCount)
	if sessionCount != initialSessionCount {
		t.Fatalf("session count = %d, want %d (unchanged)", sessionCount, initialSessionCount)
	}
}

func TestStartRuntimeExistingSessionRejectsProjectMismatch(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	configContent := `[project]
name = "mismatch-test"
[profile]
default = "mock_profile"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true
[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Create two project rows: one for tmp, one for another path.
	// Attach the session to the OTHER project.
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	// Create project for tmp (this is what GetProjectByRoot will return).
	if _, err := database.GetOrCreateProject(tmp, "mismatch-test"); err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject tmp: %v", err)
	}
	// Create another project row.
	otherPID, err := database.GetOrCreateProject("/other/path", "other")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject other: %v", err)
	}
	// Create a session for the OTHER project.
	if err := database.CreateSession("mismatch-session", otherPID, "", time.Now().UTC()); err != nil {
		database.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	database.Close()

	ctx := context.Background()
	_, err = StartRuntime(ctx, WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithExistingSession("mismatch-session"),
	)
	if err == nil {
		t.Fatal("expected error for project mismatch, got nil")
	}
}

func TestStartRuntimeRejectsConflictingSessionModes(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	configContent := `[project]
name = "conflict-test"
[profile]
default = "mock_profile"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true
[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	_, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithSessionID("new-session"),
		WithExistingSession("old-session"),
	)
	if err == nil {
		t.Fatal("expected error for conflicting session modes, got nil")
	}
}

// ── Task 3: Runtime.BeginWork ─────────────────────────────────────────

func TestRuntimeBeginWorkCancelledByRuntimeQuiesce(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	workCtx2, finish, err := rt.BeginWork(ctx)
	if err != nil {
		t.Fatalf("BeginWork: %v", err)
	}

	quiesceDone := make(chan struct{})
	go func() {
		rt.Quiesce(ctx)
		close(quiesceDone)
	}()

	// Work context should be cancelled by Quiesce.
	select {
	case <-workCtx2.Done():
	case <-time.After(time.Second):
		t.Fatal("work context was not cancelled after Quiesce")
	}

	// Quiesce should be blocked until we call finish.
	select {
	case <-quiesceDone:
		t.Fatal("Quiesce returned before finish was called")
	case <-time.After(500 * time.Millisecond):
	}

	finish()

	select {
	case <-quiesceDone:
	case <-time.After(time.Second):
		t.Fatal("Quiesce did not return after finish")
	}
}

func TestRuntimeBeginWorkFinishIsIdempotent(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	_, finish, err := rt.BeginWork(ctx)
	if err != nil {
		t.Fatalf("BeginWork: %v", err)
	}

	// Calling finish twice must not panic or produce negative counter.
	finish()
	finish()
}

func TestRuntimeBeginWorkRejectsAfterQuiesce(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}

	_, _, err := rt.BeginWork(ctx)
	if !errors.Is(err, session.ErrSessionQuiescing) {
		t.Fatalf("BeginWork after Quiesce = %v, want ErrSessionQuiescing", err)
	}
}

func TestRuntimeBeginWorkCancelledByParent(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	parentCtx, parentCancel := context.WithCancel(ctx)
	workCtx2, finish, err := rt.BeginWork(parentCtx)
	if err != nil {
		t.Fatalf("BeginWork: %v", err)
	}

	// Cancel the parent context.
	parentCancel()

	select {
	case <-workCtx2.Done():
	case <-time.After(time.Second):
		t.Fatal("work context was not cancelled after parent cancellation")
	}

	// Runtime should not be quiesced — new work can still start.
	if err := rt.State.BeginWork(); err != nil {
		t.Fatalf("BeginWork on state after parent cancel = %v, want nil", err)
	}
	rt.State.EndWork()

	finish()
}

// ── end Task 3 tests ────────────────────────────────────────────────────

func TestCommandsRegisteredEvenWhenBuildAgentRunnerFails(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"broken": {
			Type:      "openai_compatible",
			BaseURL:   "http://localhost:11434/v1",
			APIKeyEnv: "MARSHAL_UNSET_VAR_THAT_DOES_NOT_EXIST",
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"broken-preset": {
			Name:      "broken-preset",
			Provider:  "broken",
			Model:     "test-model",
			LocalOnly: true,
		},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {
			Name: cfg.Profile.Default,
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "broken-preset"},
				routing.RoleRepoScout:   {Preset: "broken-preset"},
				routing.RoleKnowledge:   {Preset: "broken-preset"},
			},
		},
	}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	_, toolReg, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err == nil {
		t.Fatalf("buildAgentRunner should fail when api_key_env points at an unset var")
	}
	if toolReg != nil {
		t.Fatal("toolReg should be nil when buildAgentRunner fails")
	}

	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, toolReg); err != nil {
		t.Fatalf("RegisterAll with nil toolReg: %v", err)
	}
	if _, ok := cmdReg.Lookup("exit"); !ok {
		t.Fatal("exit command not registered — user cannot quit when agent fails to initialise")
	}
	if len(cmdReg.List()) < 10 {
		t.Fatalf("expected at least 10 commands, got %d", len(cmdReg.List()))
	}
}

// (onboarding tests removed — onboarding is deleted)

func TestRunOpensConnectOnFirstRun(t *testing.T) {
	dir := t.TempDir() // no config anywhere → first run
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)
	t.Setenv("HOME", t.TempDir())

	var sawConnect bool
	programRunner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		if strings.Contains(updated.View().Content, "Connect a provider") {
			sawConnect = true
		}
		return ProgramResult{}
	}

	if err := Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(programRunner),
	); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawConnect {
		t.Fatal("first run should open the TUI with the connect panel")
	}
}

func TestRoleRunnerAppliesCustomAgentOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
			routing.RoleReviewer:    {CustomAgent: "strict"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "fast", SystemPrompt: "be strict", ToolDenylist: []string{"file.write_patch"}, ApprovalMode: "plan", MaxIterations: 5},
	}
	resolver := newRoutedProviderResolver(cfg, "")
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	_ = reg.Register(registry.Tool{Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite})
	_ = reg.Register(registry.Tool{Name: "shell.run", Risk: registry.RiskCommand})
	pol := policy.NewEngine(&cfg, nil)
	spec := roleRunnerSpec{
		cfg:         cfg,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		pol:         pol,
		state:       session.New(cfg, t.TempDir(), time.Now(), session.Persistence{}),
	}
	runner, err := spec.newRunner(agent.RoleReviewer, swarm.ScopeReadOnly)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if runner.SystemPromptAddendum != "be strict" {
		t.Fatalf("addendum = %q, want be strict", runner.SystemPromptAddendum)
	}
	if runner.MaxToolIterations != 5 {
		t.Fatalf("iterations = %d, want 5", runner.MaxToolIterations)
	}
	if _, ok := runner.Registry.Lookup("file.write_patch"); ok {
		t.Fatal("file.write_patch should be denylisted")
	}
	if runner.Pricing.InputPerMTokCents == 0 && runner.Pricing.OutputPerMTokCents == 0 {
		t.Fatal("Pricing should be set from the resolved preset (non-zero for a priced preset)")
	}
}

// TestPipelineRoleRunnerAutoApprovesInChildSession pins the SDD approval
// contract: pipeline role runners stream into child sessions that no UI
// watches, so an interactive Confirm would wait out the approval timeout and
// fail the turn with "agent: request timed out". Child-session runners must
// therefore evaluate under an auto-approving clone of the shared engine,
// leaving the parent's mode untouched. Swarm runners share the parent
// session (their approvals reach the TUI) and keep the shared engine.
func TestPipelineRoleRunnerAutoApprovesInChildSession(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleSDDImplementer: {Preset: "fast"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	resolver := newRoutedProviderResolver(cfg, "")
	reg := registry.New()
	handler := func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{Summary: "ran"}, nil
	}
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: handler})
	_ = reg.Register(registry.Tool{Name: "file.write_patch", Risk: registry.RiskWorkspaceWrite, Handler: handler})
	pol := policy.NewEngine(&cfg, nil)
	pol.WithRegistry(reg)
	pol.SetApprovalMode(policy.ModeEdit)
	spec := roleRunnerSpec{
		cfg:          cfg,
		resolver:     resolver,
		reg:          reg,
		readOnlyReg:  registry.ReadOnlyView(reg),
		pol:          pol,
		state:        session.New(cfg, t.TempDir(), time.Now(), session.Persistence{}),
		childSession: true,
	}
	runner, err := spec.newRunner(agent.RoleSDDImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	args := map[string]interface{}{"patch": "File: a\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}
	dec, reason, err := runner.Policy.Evaluate("file.write_patch", args)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if dec != policy.DecisionAllow {
		t.Fatalf("pipeline role runner file.write_patch = %v (%q), want Allow (unattended subagent must not wait on approval)", dec, reason)
	}
	if pol.ApprovalMode() != policy.ModeEdit {
		t.Fatalf("parent engine mode = %q, want %q (subagent clone must not mutate the session mode)", pol.ApprovalMode(), policy.ModeEdit)
	}
	if dec, _, err := pol.Evaluate("file.write_patch", args); err != nil || dec != policy.DecisionConfirm {
		t.Fatalf("parent engine file.write_patch = %v, %v; want Confirm", dec, err)
	}
	if runner.Policy == pol {
		t.Fatal("child-session runner shares the parent policy engine; want an independent clone")
	}
}

// TestSwarmRoleRunnerSharesParentPolicy pins the other half of the contract:
// swarm runners share the parent session, so their approvals are visible in
// the TUI and they keep the shared engine (mode switches apply to them).
func TestSwarmRoleRunnerSharesParentPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	resolver := newRoutedProviderResolver(cfg, "")
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	spec := roleRunnerSpec{
		cfg:         cfg,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		pol:         pol,
		state:       session.New(cfg, t.TempDir(), time.Now(), session.Persistence{}),
	}
	runner, err := spec.newRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if runner.Policy != pol {
		t.Fatal("swarm role runner should share the parent policy engine")
	}
}

func TestSubagentFactoryWiresTokenTracking(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"my-scout": {Name: "my-scout", Preset: "fast"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, 1)
	child, _, err := factory("my-scout")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Pricing.InputPerMTokCents == 0 && child.Pricing.OutputPerMTokCents == 0 {
		t.Fatal("child.Pricing should be resolved from the custom agent's preset (gpt-4o-mini is priced)")
	}
	if child.MetricsObserver == nil {
		t.Fatal("child.MetricsObserver should be set so subagent turns persist to turn_metrics")
	}
	if child.UsageObserver == nil {
		t.Fatal("child.UsageObserver should be set so subagent usage rolls up to the parent session")
	}
	// The UsageObserver folds into the parent session's running total.
	child.UsageObserver(schema.TokenUsage{PromptTokens: 100, CompletionTokens: 50})
	used, _ := parentState.TurnUsage()
	if used != 150 {
		t.Fatalf("parent usage after child observe = %d, want 150", used)
	}
}

func TestSubagentFactoryAdHocHasObserversToo(t *testing.T) {
	// The ad-hoc path (no agent name) must ALSO wire observers, closing
	// the gap for today's plain agent.run children, not just named agents.
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"fast": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "fast"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, 1)
	child, _, err := factory("")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.UsageObserver == nil || child.MetricsObserver == nil {
		t.Fatal("ad-hoc subagent children must also carry UsageObserver + MetricsObserver")
	}
}

func TestInjectedWorkerStartsAndStops(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	fake := workerFunc{name: "fake", run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil
	}}

	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			return ProgramResult{}
		}),
		WithWorker(fake),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker not stopped on shutdown")
	}
}

type workerFunc struct {
	name string
	run  func(context.Context) error
}

func (w workerFunc) Name() string                  { return w.name }
func (w workerFunc) Run(ctx context.Context) error { return w.run(ctx) }

func TestDesktopRegisterAllRegistersTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `
[project]
name = "test"

[desktop]
enabled = true
mode = "standalone"
headless = true
`
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg, err := config.Load(config.LoadOptions{HomeDir: dir, WorkingDir: dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Desktop.Enabled {
		t.Fatal("desktop should be enabled")
	}

	reg := registry.New()
	fakeFactory := func() (browser.BrowserBackend, error) {
		return &browser.FakeBackend{}, nil
	}
	if _, err := desktop.RegisterAll(reg, desktop.Options{Config: cfg.Desktop, BackendFactory: fakeFactory}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tools := reg.List()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, expected := range []string{"browser.navigate", "browser.read", "browser.click", "browser.fill", "browser.submit", "browser.screenshot"} {
		if !names[expected] {
			t.Errorf("tool %q not registered", expected)
		}
	}
}

func TestRunResolvesOptionsOnce(t *testing.T) {
	// Regression test for F-BUG-154: options must be applied exactly once,
	// not once in Run and again in StartRuntime.
	dir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	applyCount := 0
	customOpt := func(opts *options) {
		applyCount++
	}

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			return ProgramResult{}
		}),
		customOpt,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if applyCount != 1 {
		t.Fatalf("option applied %d times, want 1", applyCount)
	}
}

func TestBuildAgentRunnerRegistersConfigToolsWhenReloaderProvided(t *testing.T) {
	// Verify that buildAgentRunner wires config.* tools into the registry
	// when configReloader is non-nil, and omits them when nil.
	ctx := context.Background()
	cfg := nativeToolAgentConfig("test-provider")

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	// With configReloader: config.read should be registered.
	_, regWith, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, func(config.Config) error { return nil }, "")
	if err != nil {
		t.Fatalf("buildAgentRunner (with reloader): %v", err)
	}
	if _, ok := regWith.Lookup("config.read"); !ok {
		t.Fatal("config.read not registered when configReloader is non-nil")
	}

	// Without configReloader: config.read should NOT be registered.
	_, regWithout, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner (without reloader): %v", err)
	}
	if _, ok := regWithout.Lookup("config.read"); ok {
		t.Fatal("config.read registered when configReloader is nil, want absent")
	}
}

func TestBuildAgentRunnerRegistersDesktopToolsWhenEnabled(t *testing.T) {
	// Verify that buildAgentRunner wires desktop tools into the registry
	// when cfg.Desktop.Enabled is true. This tests the wiring, not just the
	// desktop.RegisterAll package function.
	ctx := context.Background()
	cfg := nativeToolAgentConfig("test-provider")
	cfg.Desktop.Enabled = true
	cfg.Desktop.Mode = "standalone"
	cfg.Desktop.Headless = true

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	_, reg, _, _, _, _, closer, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}

	if closer == nil {
		t.Fatal("desktop closer is nil, want non-nil when desktop is enabled")
	}

	tools := reg.List()
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, expected := range []string{"browser.navigate", "browser.read", "browser.click", "browser.fill", "browser.submit", "browser.screenshot"} {
		if !names[expected] {
			t.Errorf("tool %q not registered", expected)
		}
	}
}

// TestNoLegacyAgentSDDPackage asserts the old internal/agent/sdd/ prototype
// package has been removed. It was replaced by internal/sdd/ (now also
// removed; preserved at tag sdd-prototype-v1).
func TestNoLegacyAgentSDDPackage(t *testing.T) {
	_, err := os.Stat(filepath.Join("..", "..", "internal", "agent", "sdd", "orchestrator.go"))
	if err == nil {
		t.Fatal("internal/agent/sdd/orchestrator.go still exists — prototype not removed")
	}
}

// StartRuntime is the entry point ACP headless sessions use (internal/acp
// calls app.StartRuntime, never app.Run). Live testing over ACP found that
// hover/references/definition always returned "no lsp" regardless of config
// or whether the language server binary was installed -- traced to
// LSPManager being *constructed* in startRuntime but only ever *started*
// (its Run() worker loop, which spawns the actual subprocess) inside Run()'s
// TUI-only "Worker lifecycle" block. StartRuntime must start it too.
func TestStartRuntimeStartsLSPManagerWorker(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}

	marker := filepath.Join(tmp, "lsp-stub-invoked")
	stub := filepath.Join(tmp, "lsp-stub.sh")
	stubScript := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	configToml := `[project]
name = "lsp-worker-test"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets.mock_preset]
provider = "mock"
model = "mock-model"
local_only = true

[agent_profiles.mock_profile]
implementer = "mock_preset"
planner = "mock_preset"
repo_scout = "mock_preset"
tester = "mock_preset"
reviewer = "mock_preset"

[lsp]
enabled = true

[lsp.servers.stub]
command = "` + stub + `"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configToml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime() error = %v", err)
	}
	defer rt.Close(context.Background())

	if rt.LSPManager == nil {
		t.Fatal("LSPManager was not constructed even though [lsp.servers.stub] was configured")
	}

	// Generous because the whole suite competes for CPU: the loop below
	// returns the moment the marker appears, so a longer deadline costs
	// nothing on a healthy run and only buys headroom on a loaded one.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return // stub was invoked -- Run() was started
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("LSP stub was never invoked -- LSPManager.Run() was not started by StartRuntime")
}
