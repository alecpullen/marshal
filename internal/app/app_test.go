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
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/accesspanel"
	"marshal/internal/commands"
	"marshal/internal/contextpack"
	"marshal/internal/credentials"
	"marshal/internal/db"
	"marshal/internal/llm/pricing"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/limits"
	"marshal/internal/llm/provider/oauthworker"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/lsp"
	"marshal/internal/oauth"
	"marshal/internal/pipeline"
	"marshal/internal/tools/desktop"
	"marshal/internal/tools/desktop/browser"
	"marshal/internal/tools/mcp"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/trust"
	"marshal/internal/watch"
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

// TestPipelineRegistryFactoryExcludesWebTools pins the security boundary for
// unattended pipeline children: no scope the factory can return may expose a
// network tool. Pipeline role runners are built with childSession: true, so
// their policy clone auto-approves a web Confirm with no user watching — and
// web.fetch is an in-process HTTP client that would otherwise bypass the
// container sandbox's network isolation.
func TestPipelineRegistryFactoryExcludesWebTools(t *testing.T) {
	cfg := config.Default()
	// Register both web tools so the assertion is not vacuous: web.search
	// needs a provider URL, web.fetch only needs [web] enabled.
	cfg.Web.Enabled = true
	cfg.Web.SearchURL = "https://search.example.com"

	dir := t.TempDir()
	state := session.New(cfg, dir, time.Unix(100, 0), session.Persistence{})
	state.SetSDDFallbackAllowedFiles([]string{"internal/foo"})

	// Control: the same native toolset WITHOUT the filter does expose the
	// web tools, so the exclusion assertions below cannot pass vacuously.
	control := registry.New()
	if err := native.RegisterAll(control, native.Options{WorkspaceRoot: dir, Config: cfg}); err != nil {
		t.Fatalf("control RegisterAll: %v", err)
	}
	for _, name := range []string{"web.fetch", "web.search"} {
		if _, ok := control.Lookup(name); !ok {
			t.Fatalf("control registry lacks %s; the exclusion assertions would be vacuous", name)
		}
	}

	factory := makePipelineRegistryFactory(cfg, state, nil, nil, nil, 0, nil, nil, nil)
	ctx := pipeline.ExecutionContext{
		RepoRoot:      dir,
		WorkspaceRoot: dir,
		ArtifactRoot:  filepath.Join(dir, "run"),
		ArtifactAlias: "@run",
	}

	for _, scope := range []pipeline.RegistryScope{
		pipeline.ScopeFull,
		pipeline.ScopeReadOnly,
		pipeline.ScopeArtifactWriter,
		pipeline.ScopeFallback,
	} {
		reg, err := factory(ctx, scope, state)
		if err != nil {
			t.Fatalf("factory(%v): %v", scope, err)
		}
		for _, name := range []string{"web.fetch", "web.search"} {
			if _, ok := reg.Lookup(name); ok {
				t.Errorf("scope %v must not expose %s", scope, name)
			}
		}
	}

	// ScopeFull is the implementer/fixer: it must keep its write and shell
	// tools, so the exclusion is a network filter and not a blanket strip.
	reg, err := factory(ctx, pipeline.ScopeFull, state)
	if err != nil {
		t.Fatalf("factory(ScopeFull): %v", err)
	}
	for _, name := range []string{"file.write_patch", "shell.run"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("ScopeFull must retain %s", name)
		}
	}
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

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
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
			name:        "empty falls back to native when provider supports tool calling",
			toolCalling: "",
			caps:        schema.ProviderCapabilities{ToolCalling: true, StructuredOutput: true, JSONMode: true},
			wantNative:  true,
		},
		{
			name:        "empty falls back to json_schema when provider lacks tool calling",
			toolCalling: "",
			caps:        schema.ProviderCapabilities{StructuredOutput: true, JSONMode: true},
			wantRF:      "json_schema",
		},
		{
			name:        "empty falls back to json_object when only JSONMode advertised",
			toolCalling: "",
			caps:        schema.ProviderCapabilities{JSONMode: true},
			wantRF:      "json_object",
		},
		{
			name:        "empty falls back to nil when provider advertises nothing",
			toolCalling: "",
			caps:        schema.ProviderCapabilities{},
		},
		{
			name:        "none explicitly blocks tools regardless of provider capability",
			toolCalling: "none",
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
	runner, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
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

func TestBuildAgentRunnerPassesMaxToolIterationsThrough(t *testing.T) {
	ctx := context.Background()

	// 0 (default) -> runner gets 0 (unlimited).
	cfg := nativeToolAgentConfig("test-provider")
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.MaxToolIterations != 0 {
		t.Fatalf("MaxToolIterations = %d, want 0 (unlimited default)", runner.MaxToolIterations)
	}

	// Explicit cap -> runner gets it.
	cfg.Agent.MaxToolIterations = 50
	state = session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, _, _, err = buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.MaxToolIterations != 50 {
		t.Fatalf("MaxToolIterations = %d, want 50", runner.MaxToolIterations)
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
	runner, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
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

func TestBuildAgentRunnerWiresRouterClassifierWhenDistinct(t *testing.T) {
	ctx := context.Background()

	// Main route binds to the test-provider preset; RoleRouter binds to a
	// distinct provider+model, so the classifier must be wired (AI-07).
	cfg := nativeToolAgentConfig("test-provider")
	cfg.Providers["router-provider"] = config.ProviderConfig{
		Type:        "openai_compatible",
		BaseURL:     "http://localhost:11434/v1",
		APIKey:      "test-key",
		ToolCalling: true,
	}
	cfg.Models.Presets["router-model"] = routing.ModelPreset{
		Name:        "router-model",
		Provider:    "router-provider",
		Model:       "router-model",
		ToolCalling: "native",
		LocalOnly:   true,
	}
	cfg.AgentProfiles[cfg.Profile.Default].Roles[routing.RoleRouter] = routing.RoleBinding{Preset: "router-model"}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.Classifier == nil {
		t.Fatal("Classifier = nil, want non-nil when a distinct router role is bound")
	}
}

func TestBuildAgentRunnerSkipsRouterClassifierOnSameModel(t *testing.T) {
	ctx := context.Background()
	// RoleRouter falls back to the implementer preset (same provider+model
	// as the active turn route) — a single-model backend cannot interleave
	// two calls, so the classifier must stay nil (AI-07).
	cfg := nativeToolAgentConfig("test-provider")
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if runner.Classifier != nil {
		t.Fatal("Classifier != nil, want nil when router role resolves to the same provider+model")
	}
}

func TestBuildAgentRunnerExposesPlanAuthorFactory(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("test-provider")

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	_, _, _, _, _, _, _, _, _, _, planAuthorFactory, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if planAuthorFactory == nil {
		t.Fatal("plan author factory must be non-nil when the regular runner is available")
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
	runner, reg, _, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	_ = runner
	_ = reg

	id := launchDetachedJob(t, ctx, jobMgr, "echo $MARSHAL_TEST_SECRET")

	// Wait for the short-lived job to complete.
	waitForJobStatus(t, jobMgr, id, native.StatusCompleted, 5*time.Second)

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
	runner, reg, _, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	_ = runner
	_ = reg

	// Start a background job that produces more than 8 bytes of output.
	id := launchDetachedJob(t, ctx, jobMgr, "echo aaaaaaaa bbbbbbbb cccccccc dddddddd")
	waitForJobStatus(t, jobMgr, id, native.StatusCompleted, 5*time.Second)

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

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.JobManager == nil {
		t.Fatal("Runtime.JobManager is nil")
	}

	// Prove the manager works: start a job and list it.
	id := launchDetachedJob(t, ctx, rt.JobManager, "echo job-manager-test")

	// The model-facing job.* tools were retired in the context-diet Tier 2a
	// cut: agent.await/agent.kill reach the same manager through the
	// adapter. Assert the runtime hands out the manager the job landed in.
	found := false
	for _, j := range rt.JobManager.List() {
		if j.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("job %s is not visible from the runtime's job manager", id)
	}
}

func TestStartRuntimeOwnsWatchManager(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[project]
name = "wm-test"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
tool_calling = true

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.WatchManager == nil {
		t.Fatal("Runtime.WatchManager is nil")
	}

	// Prove the manager works: register a file watch on a temp path.
	watchPath := filepath.Join(tmp, "watch-target.txt")
	if err := os.WriteFile(watchPath, []byte("initial"), 0644); err != nil {
		t.Fatalf("write watch target: %v", err)
	}
	id, _, err := rt.WatchManager.Start(watch.Spec{
		Name: "wm-test-watch",
		Kind: watch.KindFile,
		Path: watchPath,
	})
	if err != nil {
		t.Fatalf("WatchManager.Start: %v", err)
	}

	// The model-facing watch.* tools were retired in the context-diet Tier 2a
	// cut, so the manager is reached through the runtime accessor now. Assert
	// the accessor hands back the very manager the runtime owns and that the
	// watch it just started is visible through it.
	if rt.CurrentWatchManager() != rt.WatchManager {
		t.Fatal("CurrentWatchManager must return the runtime's own manager")
	}
	found := false
	for _, w := range rt.CurrentWatchManager().List() {
		if w.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("watch %s is not visible from the runtime's watch manager", id)
	}
}

// TestReloadAgentRuntimePreservesCommandServiceWhileIdle pins the ownership
// rule for a config reload: the conversation's command service SURVIVES it.
//
// A reload changes future launches only. The same supervisor keeps its handle,
// its ids and its admission record across the reload, so an old command keeps
// running, the transcript's handles stay resolvable, and an explicit turn stop
// still reaches the service it always did. Before service reuse, a reload built
// a second manager and shut the first one down — killing running commands and
// orphaning every handle a client already held.
func TestReloadAgentRuntimePreservesCommandServiceWhileIdle(t *testing.T) {
	ctx := context.Background()

	initialCfg := nativeToolAgentConfig("test-provider")
	reloadedCfg := nativeToolAgentConfig("test-provider")
	// A reload must be observable as a NEW generation even though the service
	// is preserved: the capacity change below is recorded against it.
	reloadedCfg.Tools.Shell.MaxBackgroundJobs = 7

	state := session.New(initialCfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("initial buildAgentRunner: %v", err)
	}
	t.Cleanup(func() { _ = jobMgr.Shutdown(context.Background()) })

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
	oldMgr := rt.JobManager
	genBefore := oldMgr.Generation()

	// Reload — the SAME service must still be installed afterwards.
	if err := reloadAgentRuntime(ctx, reloadedCfg, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	if rt.JobManager != oldMgr {
		t.Fatal("reload replaced the command service; a reload must preserve it")
	}
	if got := rt.State.GetTurnStopService(); got != session.TurnStopService(oldMgr) {
		t.Fatalf("turn-stop service after reload = %#v, want the preserved service", got)
	}
	if got := oldMgr.Generation(); got <= genBefore {
		t.Fatalf("generation after reload = %d, want a newer generation than %d", got, genBefore)
	}
	// A reload commits new defaults for future launches only.
	if got := oldMgr.CurrentLaunchPolicy().MaxJobs; got != 7 {
		t.Fatalf("MaxJobs after reload = %d, want the reloaded config's 7", got)
	}

	// The preserved service still admits: it was never closed.
	id := launchDetachedJob(t, ctx, rt.JobManager, "echo hello-preserved-manager")
	_ = id
	waitForJobStatus(t, rt.JobManager, id, native.StatusCompleted, 5*time.Second)
}

// blockingRunner is a CommandRunner that publishes a PID, blocks until
// released, and then reports a clean exit. It stands in for a long ordinary
// command whose execution must survive a reload.
type blockingRunner struct {
	release chan struct{}
	pid     int
	calls   int32
}

func (r *blockingRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	atomic.AddInt32(&r.calls, 1)
	if req.OnStart != nil {
		rp := r.pid
		if rp == 0 {
			rp = 4242
		}
		req.OnStart(rp)
	}
	if req.Stdout != nil {
		_, _ = req.Stdout.Write([]byte("started\n"))
	}
	select {
	case <-r.release:
	case <-ctx.Done():
		return native.CommandResult{}, ctx.Err()
	}
	result := native.CommandResult{Stdout: "finished\n", ExitCode: 0}
	if req.Stdout != nil {
		_, _ = req.Stdout.Write([]byte("finished\n"))
	}
	return result, nil
}

// TestReloadAgentRuntimeKeepsRunningCommandsAndHandles pins the strongest form
// of the ownership rule: a command admitted BEFORE a reload keeps running
// through it, keeps its identity and PID, and settles afterwards through the
// preserved service's exit sink.
//
// This is what "reload changes future launches only" buys: the transcript's
// handle, the TUI's job lane and agent.await all keep resolving the same
// execution, and the reload neither kills the command nor orphans its record.
func TestReloadAgentRuntimeKeepsRunningCommandsAndHandles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initialCfg := nativeToolAgentConfig("test-provider")
	reloadedCfg := nativeToolAgentConfig("test-provider")
	// A changed output policy makes the "old command keeps what it captured"
	// claim falsifiable: the reloaded toolset would use a different cap.
	reloadedCfg.Tools.Shell.MaxOutputBytes = 8
	reloadedCfg.Tools.Shell.ForegroundWaitSeconds = 30

	state := session.New(initialCfg, dir, time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("initial buildAgentRunner: %v", err)
	}
	_ = runner
	t.Cleanup(func() { _ = jobMgr.Shutdown(context.Background()) })

	// Give the service a controlled runner: the real sandbox runner is fine
	// for a short command, but a blocking one makes the reload window exact.
	long := &blockingRunner{release: make(chan struct{}), pid: 909}
	jobMgr.CommitLaunchPolicy(native.LaunchPolicy{
		Generation:     jobMgr.Generation(),
		Runner:         long,
		Dir:            dir,
		MaxOutputBytes: 4096,
		MaxJobs:        25,
		Retention:      time.Hour,
	})

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	info, err := jobMgr.Launch(ctx, native.LaunchSpec{
		Command:  "long-running",
		Detached: true,
		Origin:   native.CommandOrigin{ConversationID: state.SessionID(), TurnID: "turn-1"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Wait for the PID so the reload happens while the process is alive.
	deadline := time.Now().Add(5 * time.Second)
	for {
		live, _, oerr := jobMgr.Output(info.ID, 0)
		if oerr != nil {
			t.Fatalf("Output: %v", oerr)
		}
		if live.PID == 909 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never published a PID; status %q", live.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The reload succeeds and preserves the service.
	if err := reloadAgentRuntime(ctx, reloadedCfg, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if rt.JobManager != jobMgr {
		t.Fatal("reload replaced the service that owns the running command")
	}

	// The running command is untouched: same handle, same identity, same PID,
	// still running, with the output policy it captured at admission.
	live, _, err := jobMgr.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output after reload: %v", err)
	}
	if live.Status != native.StatusRunning {
		t.Fatalf("status after reload = %q, want running (a reload must not end a command)", live.Status)
	}
	if live.ExecutionID != info.ExecutionID || live.PID != 909 {
		t.Fatalf("identity after reload = %q/pid %d, want %q/pid 909", live.ExecutionID, live.PID, info.ExecutionID)
	}
	if got := atomic.LoadInt32(&long.calls); got != 1 {
		t.Fatalf("runner calls after reload = %d, want exactly 1 (no relaunch)", got)
	}
	// The service keeps the defaults the command was admitted under; only the
	// committed policy (used by future launches) reflects the new config.
	if got := jobMgr.CurrentLaunchPolicy().MaxOutputBytes; got != 8 {
		t.Fatalf("committed output cap after reload = %d, want the reloaded 8", got)
	}

	// Release it: the preserved service settles it through its exit sink, so
	// the parent transcript gains exactly one durable completion.
	close(long.release)
	settled, err := jobMgr.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.Status != native.StatusCompleted {
		t.Fatalf("status = %q, want completed", settled.Status)
	}
	var exits []session.JobExit
	for _, item := range state.Transcript() {
		if item.JobExit != nil {
			exits = append(exits, *item.JobExit)
		}
	}
	if len(exits) != 1 {
		t.Fatalf("job exits after the reload completion = %d, want exactly 1", len(exits))
	}
	if exits[0].ExecutionID != info.ExecutionID {
		t.Fatalf("settled record identity = %q, want the admitted %q", exits[0].ExecutionID, info.ExecutionID)
	}
	if exits[0].ConversationID != state.SessionID() || exits[0].TurnID != "turn-1" {
		t.Fatalf("settled record lost its origin: %+v", exits[0])
	}
}

// TestReloadAgentRuntimeFailureLeavesServiceUntouched pins the rollback side of
// the same contract: a candidate that fails validation neither mutates the
// running service's defaults nor closes it.
func TestReloadAgentRuntimeFailureLeavesServiceUntouched(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initialCfg := nativeToolAgentConfig("test-provider")

	state := session.New(initialCfg, dir, time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("initial buildAgentRunner: %v", err)
	}
	t.Cleanup(func() { _ = jobMgr.Shutdown(context.Background()) })

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	before := jobMgr.CurrentLaunchPolicy()
	genBefore := jobMgr.Generation()

	// A preset naming an unconfigured provider fails provider resolution
	// inside the candidate build.
	bad := nativeToolAgentConfig("test-provider")
	bad.Models.Presets["native-coder"] = routing.ModelPreset{
		Name: "native-coder", Provider: "not-configured", Model: "test-model",
	}
	// Keep only the good provider, so the preset's provider is unknown.
	bad.Providers = map[string]config.ProviderConfig{
		"test-provider": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "k", ToolCalling: true},
	}

	if err := reloadAgentRuntime(ctx, bad, rt); err == nil {
		t.Fatal("expected the reload to fail")
	}
	if rt.JobManager != jobMgr {
		t.Fatal("a failed reload replaced the service")
	}
	if got := jobMgr.CurrentLaunchPolicy(); got.MaxOutputBytes != before.MaxOutputBytes ||
		got.Runner != before.Runner {
		t.Fatalf("a failed reload mutated the service policy: %+v (was %+v)", got, before)
	}
	if got := jobMgr.Generation(); got != genBefore {
		t.Fatalf("generation after a failed reload = %d, want the unchanged %d", got, genBefore)
	}

	// The service still admits: nothing closed it on the failure path.
	id := launchDetachedJob(t, ctx, jobMgr, "echo reload-failed")
	waitForJobStatus(t, jobMgr, id, native.StatusCompleted, 5*time.Second)
}

// TestSubagentChildRegistryBorrowsConversationService pins the child ownership
// rule: an agent.run child gets its own native toolset (so its output stays
// child-local) but BORROWS the conversation's command service, so its commands
// are counted, settled and stopped exactly like the parent's.
func TestSubagentChildRegistryBorrowsConversationService(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := nativeToolAgentConfig("test-provider")
	cfg.Tools.Shell.Sandbox.Backend = "passthrough"
	cfg.Tools.Shell.Sandbox.UnsafePassthrough = true

	parentState := session.New(cfg, dir, time.Unix(100, 0), session.Persistence{})
	runner, _, _, _, _, jobMgr, _, childFactory, _, _, _, _, err := buildAgentRunner(ctx, cfg, parentState, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	_ = runner
	t.Cleanup(func() { _ = jobMgr.Shutdown(context.Background()) })

	child, childState, err := childFactory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("child factory: %v", err)
	}
	if childState == nil || childState == parentState {
		t.Fatal("a child must get its own session state")
	}
	tool, ok := child.Registry.Lookup("shell.run")
	if !ok {
		t.Fatal("child registry should expose shell.run")
	}

	// Run a background command through the child's registry.
	res, err := tool.Handler(session.WithExecutionOrigin(ctx, session.ExecutionOrigin{
		ConversationID: parentState.SessionID(),
		Actor:          "subtask",
		TurnID:         "turn-child",
		ToolCallID:     "call-child",
	}), registry.ToolCall{Name: "shell.run", Args: []byte(`{"command":"echo child-command","background":true}`)})
	if err != nil {
		t.Fatalf("child shell.run: %v", err)
	}
	jobID := ""
	for _, field := range strings.Fields(res.Content) {
		if strings.HasPrefix(field, "job-") {
			jobID = field
		}
	}
	if jobID == "" {
		t.Fatalf("child result has no job id: %q", res.Content)
	}

	// The PARENT's service knows the execution: it was borrowed, not owned by
	// the child's registry.
	info, _, err := jobMgr.Output(jobID, 0)
	if err != nil {
		t.Fatalf("parent service does not own the child's command: %v", err)
	}
	if info.Origin.Actor != "subtask" || info.Origin.TurnID != "turn-child" {
		t.Fatalf("child command origin = %+v, want the child's actor/turn", info.Origin)
	}
	// The count callback is the parent's (installed at owner construction).
	if got := parentState.RunningJobsCount(); got != 1 {
		t.Fatalf("parent running count = %d, want 1", got)
	}

	// Waiting on it settles it through the parent's sink, so the parent's
	// transcript gains the durable completion.
	waitForJobStatus(t, jobMgr, jobID, native.StatusCompleted, 10*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		found := false
		for _, item := range parentState.Transcript() {
			if item.JobExit != nil && item.JobExit.ID == jobID {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the child's completion never reached the parent's transcript")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := parentState.RunningJobsCount(); got != 0 {
		t.Fatalf("parent running count after settlement = %d, want 0", got)
	}
}

// TestPipelineRegistryCommandsCaptureDispatchWorktree pins the pipeline cwd
// rule: a dispatch's commands capture THAT dispatch's worktree, so a later
// workspace change (or a sibling dispatch) cannot redirect a command that has
// already started.
func TestPipelineRegistryCommandsCaptureDispatchWorktree(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "k", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m", ToolCalling: "native"},
	}

	sessionRoot := t.TempDir() // the parent session's active root
	worktree := t.TempDir()    // the per-dispatch worktree
	repoRoot := t.TempDir()

	state := session.New(cfg, sessionRoot, time.Unix(100, 0), session.Persistence{})

	var mu sync.Mutex
	var dirs []string
	// The runner blocks until released, so the execution is still ACTIVE when
	// the assertions below inspect the service. A command that settled
	// immediately would only prove the record survived, not that the service
	// holds it while it runs.
	release := make(chan struct{})
	runner := &fakeRunner{run: func(c context.Context, req native.CommandRequest) (native.CommandResult, error) {
		mu.Lock()
		dirs = append(dirs, req.Dir)
		mu.Unlock()
		if req.OnStart != nil {
			req.OnStart(4321)
		}
		if req.Stdout != nil {
			_, _ = req.Stdout.Write([]byte("pipeline-out\n"))
		}
		select {
		case <-release:
		case <-c.Done():
			return native.CommandResult{}, c.Err()
		}
		return native.CommandResult{Stdout: "pipeline-out\n", ExitCode: 0}, nil
	}}
	service := newConversationCommandService(ctx, cfg, state, nil)
	service.manager.CommitLaunchPolicy(native.LaunchPolicy{
		Generation:     service.manager.Generation(),
		Runner:         runner,
		MaxOutputBytes: 1 << 20,
		MaxJobs:        25,
		Retention:      time.Hour,
	})
	t.Cleanup(func() { _ = service.manager.Shutdown(context.Background()) })

	childState := session.New(cfg, worktree, time.Unix(100, 0), session.Persistence{})
	factory := makePipelineRegistryFactory(cfg, state, runner, nil, nil, 0, nil, nil, service)
	reg, err := factory(pipeline.ExecutionContext{
		RepoRoot:      repoRoot,
		WorkspaceRoot: worktree,
		ArtifactRoot:  filepath.Join(repoRoot, "run"),
		ArtifactAlias: "@run",
	}, pipeline.ScopeFull, childState)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("ScopeFull pipeline registry must expose shell.run")
	}
	if _, err := tool.Handler(ctx, registry.ToolCall{Name: "shell.run", Args: []byte(`{"command":"echo pipeline-out"}`)}); err != nil {
		t.Fatalf("pipeline shell.run: %v", err)
	}

	mu.Lock()
	got := append([]string(nil), dirs...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(got))
	}
	if got[0] != worktree {
		t.Fatalf("pipeline command dir = %q, want the per-dispatch worktree %q (not the session root %q)", got[0], worktree, sessionRoot)
	}

	// The dispatch's commands belong to the conversation's service, and the
	// child state's output stayed child-local.
	active := service.manager.ActiveExecutions()
	if len(active) != 1 {
		t.Fatalf("ActiveExecutions = %d, want the pipeline command in the conversation's service", len(active))
	}
	if active[0].Dir != worktree {
		t.Fatalf("recorded cwd = %q, want %q", active[0].Dir, worktree)
	}
	if active[0].PID != 4321 {
		t.Fatalf("recorded PID = %d, want the published 4321", active[0].PID)
	}
	if len(childState.Transcript()) == 0 && childState.RunningJobsCount() == 0 {
		// Nothing to assert on the child beyond "no leak into the parent":
		// the parent's transcript must not gain the pipeline command's row.
		for _, item := range state.Transcript() {
			if item.JobExit != nil {
				t.Fatal("a foreground pipeline command must not post a job exit to the parent")
			}
		}
	}

	// Let it finish so the test leaves no live execution behind.
	close(release)
	if _, err := service.manager.Wait(ctx, active[0].ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestReloadAgentRuntimeDoesNotOwnShutdownAndCloseDoes pins the other half of
// the ownership rule: because a reload only borrows, the CONVERSATION's own
// teardown is the one place the service ends. A reload must leave it running;
// Quiesce (which Close calls) must end it, including every generation resource
// it retained across the reload.
func TestReloadAgentRuntimeDoesNotOwnShutdownAndCloseDoes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	initialCfg := nativeToolAgentConfig("test-provider")
	reloadedCfg := nativeToolAgentConfig("test-provider")

	state := session.New(initialCfg, dir, time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("initial buildAgentRunner: %v", err)
	}
	_ = runner

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	if err := reloadAgentRuntime(ctx, reloadedCfg, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	// A reload never closes the service: it still admits.
	afterReload := launchDetachedJob(t, ctx, jobMgr, "echo still-open")
	waitForJobStatus(t, jobMgr, afterReload, native.StatusCompleted, 5*time.Second)

	// The runtime's own teardown is the one that ends it.
	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if _, err := jobMgr.Launch(ctx, native.LaunchSpec{Command: "echo after-quiesce", Detached: true}); !errors.Is(err, native.ErrJobManagerClosed) {
		t.Fatalf("launch after Quiesce = %v, want ErrJobManagerClosed", err)
	}
}

// newOwnedCommandService builds a test-OWNED conversation command service. A
// fixture that exposes shell.run/test.run injects one explicitly: the native
// toolset no longer creates a supervisor implicitly, so a dispatch through an
// ownerless registry fails rather than running commands nothing can count,
// settle or stop.
//
// countingState, when non-nil, receives the same running-count callback the
// production owner installs; sinkState, when non-nil, receives the detached
// completion sink. Cleanup shuts the service down so no test leaks a process.
func newOwnedCommandService(t *testing.T, runner native.CommandRunner, dir string, countingState, sinkState *session.State) *native.JobManager {
	t.Helper()
	jm := native.NewCommandService(context.Background(), native.LaunchPolicy{
		Runner:         runner,
		Dir:            dir,
		MaxOutputBytes: 1 << 20,
	})
	if countingState != nil {
		jm.SetOnChange(countingState.SetRunningJobsCount)
	}
	if sinkState != nil {
		jm.SetOnExit(func(info native.JobInfo, ran time.Duration, output string) error {
			return sinkState.AddJobExit(session.JobExitFromExecution(info.Execution(), info.Command, output, ran, info.ExitCode))
		})
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jm.Shutdown(ctx)
	})
	return jm
}

// launchDetachedJob admits command as a detached execution through the launch
// API and returns its display id. Start — the removed wrapper whose timeout
// argument was ignored — is replaced everywhere by this: a managed command has
// no execution deadline, so there is no duration to pass.
func launchDetachedJob(t *testing.T, ctx context.Context, jm *native.JobManager, command string) string {
	t.Helper()
	info, err := jm.Launch(ctx, native.LaunchSpec{Command: command, Detached: true})
	if err != nil {
		t.Fatalf("Launch(%q): %v", command, err)
	}
	return info.ID
}

// waitForJobStatus polls until the execution reaches want, failing the test on
// timeout. A terminal status is the settled snapshot, so no fixed sleep is
// needed.
func waitForJobStatus(t *testing.T, jm *native.JobManager, id string, want native.JobStatus, timeout time.Duration) native.JobInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last native.JobInfo
	for {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%s): %v", id, err)
		}
		last = info
		if info.Status == want {
			return info
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s status = %q, want %q", id, last.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
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
	runner, reg, swarmRunner, mcpMgr, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
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
	runner, reg, swarmRunner, mcpMgr, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
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

// Reloading with a config containing one unstartable MCP server must not
// fail, and must not disturb the healthy server's tool.
func TestReloadAgentRuntimeToleratesBadMCPServer(t *testing.T) {
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
	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, mcpMgr, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}
	if mcpMgr != nil {
		defer mcpMgr.Close()
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

	// Reload with one good server and one that cannot start.
	reloaded := reloadableAgentConfig("provider")
	reloaded.MCP.Servers = map[string]config.MCPServerConfig{
		"good": {
			Command: exe,
			Args:    []string{"-test.run=TestReloadAgentRuntimeToleratesBadMCPServer"},
			Env:     map[string]string{"BE_MOCK_SERVER": "1"},
			Trust:   "unrestricted",
		},
		"bad": {
			// Not in the allow-list and no trust flag: rejected before spawn.
			Command: "/nonexistent/marshal-mcp-server",
		},
	}
	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime must tolerate a bad MCP server, got: %v", err)
	}

	// The healthy server's tool must have survived the reload.
	if _, ok := rt.ToolRegistry.Lookup("mcp.good.hello"); !ok {
		t.Error("healthy server's tool missing after reload with a bad server")
	}
	// The failure must be surfaced, not swallowed. Notice() returns
	// (Notice, bool) — verified at internal/app/session/notice.go:64.
	if n, ok := state.Notice(); !ok || !strings.Contains(n.Message, "bad") {
		t.Errorf("expected a notice naming the failed server, got %+v (ok=%v)", n, ok)
	}
}

// A server whose start failed because it needs OAuth must render an
// actionable notice naming the /mcp auth command, while every other failure
// keeps the existing "unavailable" wording (regression guard).
func TestMCPFailureMessageOAuthAndGeneric(t *testing.T) {
	authErr := &oauth.ErrAuthRequired{ServerName: "linear", Reason: "no stored tokens"}
	got := mcpFailureMessage([]mcp.ServerFailure{{
		Name:      "linear",
		Transport: "http",
		Err:       authErr,
	}})
	want := "MCP server 'linear' needs OAuth — run /mcp auth linear"
	if got != want {
		t.Errorf("oauth failure notice = %q, want %q", got, want)
	}

	// The sentinel must match through the initialize-handshake wrap the
	// HTTP client applies, not just the bare error.
	wrapped := mcpFailureMessage([]mcp.ServerFailure{{
		Name: "linear",
		Err:  fmt.Errorf("initialize handshake: %w", authErr),
	}})
	if wrapped != want {
		t.Errorf("wrapped oauth notice = %q, want %q", wrapped, want)
	}

	// Regression guard: a generic failure still uses the existing wording.
	generic := mcpFailureMessage([]mcp.ServerFailure{{
		Name: "bad",
		Err:  errors.New("connection refused"),
	}})
	if !strings.Contains(generic, "MCP server(s) unavailable") || !strings.Contains(generic, "connection refused") {
		t.Errorf("generic failure notice = %q, want the existing unavailable wording", generic)
	}
}

// TestReloadAgentRuntimeAddsRemoteMCPServer covers the same-session loop the
// spec calls out: a remote MCP server is added to the config, the runtime is
// reloaded, and its tool becomes callable without restarting the process.
func TestReloadAgentRuntimeAddsRemoteMCPServer(t *testing.T) {
	mcp.AllowInsecureHTTP(true)
	t.Cleanup(func() { mcp.AllowInsecureHTTP(false) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "remote", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "hello",
				"description": "says hello",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "hello from remote"}}}
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	ctx := context.Background()
	initial := reloadableAgentConfig("provider")
	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}

	rt := &Runtime{
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		JobManager:   jobMgr,
		State:        state,
		workCtx:      ctx,
	}

	// Add the remote server the way /settings would.
	reloaded := reloadableAgentConfig("provider")
	reloaded.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {
			URL:   srv.URL,
			Trust: "unrestricted",
		},
	}
	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if rt.MCPManager == nil {
		t.Fatal("MCPManager should be live after adding a remote server")
	}

	tool, ok := rt.ToolRegistry.Lookup("mcp.remote.hello")
	if !ok {
		t.Fatal("remote MCP tool mcp.remote.hello not registered after reload")
	}
	res, err := tool.Handler(ctx, registry.ToolCall{Name: "mcp.remote.hello", Args: []byte("{}")})
	if err != nil {
		t.Fatalf("remote tool call: %v", err)
	}
	if !strings.Contains(res.Content, "hello from remote") {
		t.Errorf("content = %q, want containing 'hello from remote'", res.Content)
	}
}

func TestReloadAgentRuntimeRollsBackOnFailure(t *testing.T) {
	ctx := context.Background()
	initialCfg := reloadableAgentConfig("test-provider")
	state := session.New(initialCfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(ctx, initialCfg, state, nil, 0, nil, "", nil, nil, nil, "")
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
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, pipelineFactory, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "")
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
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {Name: "local_balanced", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleSDDImplementer: {Preset: "ollama/gpt-4o-mini"},
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
	adapter := buildPipelineController(cfg, state, reg, pol, resolver, nil, 1, nil, &fakeRunner{}, planPath, nil, nil, nil, nil)
	if adapter == nil {
		t.Fatal("buildPipelineController returned nil")
	}
}

func TestRoutedProviderResolverPassesLimitCacheDir(t *testing.T) {
	dataDir := t.TempDir()
	if err := limits.Save(dataDir, limits.Cache{
		Table: map[string]limits.Limit{
			"cached-model": {ContextWindow: 128000, MaxOutputTokens: 8192},
		},
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save limits cache: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"cached-model"}]}`))
	}))
	defer server.Close()

	cfg := config.Config{
		Providers: map[string]config.ProviderConfig{
			"test": {Type: "openai_compatible", BaseURL: server.URL},
		},
	}
	resolver := newRoutedProviderResolver(cfg, dataDir)
	p, err := resolver.providerFor(routing.Route{Preset: routing.ModelPreset{Provider: "test"}})
	if err != nil {
		t.Fatalf("providerFor: %v", err)
	}
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ContextWindow != 128000 || models[0].MaxOutputTokens != 8192 {
		t.Fatalf("model limits = %d/%d, want 128000/8192", models[0].ContextWindow, models[0].MaxOutputTokens)
	}
}

// The resolver must thread the session's stable ID into OpenCode-compatible
// providers as Options.SessionID so chat requests carry x-opencode-session
// (required by OpenCode Zen; see provider package).
func TestRoutedProviderResolverThreadsSessionID(t *testing.T) {
	cfg := config.Config{
		Providers: map[string]config.ProviderConfig{
			"opencode-go": {Type: "openai_compatible", BaseURL: "https://opencode.ai/zen/go/v1"},
		},
	}
	resolver := newRoutedProviderResolver(cfg, "").withSessionID("sess_thread_test")
	p, err := resolver.providerFor(routing.Route{Preset: routing.ModelPreset{Provider: "opencode-go"}})
	if err != nil {
		t.Fatalf("providerFor: %v", err)
	}
	oc, ok := p.(*provider.OpenAICompatible)
	if !ok {
		t.Fatalf("providerFor returned %T, want *provider.OpenAICompatible", p)
	}
	if got := oc.SessionID(); got != "sess_thread_test" {
		t.Fatalf("provider SessionID = %q, want %q", got, "sess_thread_test")
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
	// Explicit fixtures: a test-owned working directory carrying its own
	// project config, plus a test-owned home. The test asserts database
	// creation, not config loading, so it must never reach the developer's
	// real config or data directories. No provider is configured, so
	// startup constructs neither a provider nor an OAuth refresh worker.
	dir := t.TempDir()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	runnerCalled := false
	runner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		runnerCalled = true
		return ProgramResult{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, bytes.NewBuffer(nil), WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithWorkingDir(dir), WithHomeDir(home),
		WithProgramRunner(runner), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
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
	// Explicit fixtures: test-owned working directory and home. Logging goes
	// to <working dir>/.marshal/marshal.log, so the assertion does not need —
	// and must not touch — the developer's real config or data directories.
	dir := t.TempDir()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := Run(ctx, stdout, WithNow(func() time.Time {
		return time.Unix(1000, 0)
	}), WithWorkingDir(dir), WithHomeDir(home),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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

// TestLoaderIsolationIgnoresAmbientUserConfig is the loader-level regression
// for the isolation contract. The package runs with HOME pointed at a
// temporary directory (internal/testenv.Run), so a synthetic "developer"
// config planted at the process-default user path is exactly what an
// unisolated load would have picked up.
//
// The ambient fixture is deliberately credential-shaped — an OAuth Codex
// provider and an OAuth MCP server with non-routable endpoints — and the
// control load proves an unisolated load would have merged them. Loading
// configuration never constructs a provider or opens a credential store
// (internal/app/config does not depend on either package), so this test
// stays off the real keychain even if the isolation it asserts were broken.
func TestLoaderIsolationIgnoresAmbientUserConfig(t *testing.T) {
	const ambientUserConfig = `[project]
name = "ambient-project"

[providers.ambient-codex]
type = "openai_codex"
base_url = "https://example.invalid/backend-api"
auth = "oauth"

[mcp.servers.ambient-mcp]
url = "https://example.invalid/mcp"
type = "http"
auth = "oauth"
`

	const fixtureUserConfig = `[project]
name = "fixture-project"

[commands]
test = "fixture-test-cmd"
`

	// The premise of the regression: the process-default config location is
	// the isolated temporary home, not a developer override.
	for _, key := range []string{"MARSHAL_CONFIG_DIR", "MARSHAL_DATA_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		if got := os.Getenv(key); got != "" {
			t.Fatalf("%s = %q, want the isolated empty value", key, got)
		}
	}
	processHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	// Safety guard: the ambient fixture may only be planted in a test-owned
	// home. If the package isolation (internal/testenv.Run in main_test.go)
	// is ever dropped, the process-default home is the developer's real home
	// and this test must fail rather than overwrite their user config.
	if !strings.HasPrefix(processHome, os.TempDir()) {
		t.Fatalf("process home %q is not a test-owned temporary directory; refusing to plant an ambient config there", processHome)
	}

	ambientPath := config.UserConfigPath(processHome)
	if err := os.MkdirAll(filepath.Dir(ambientPath), 0o755); err != nil {
		t.Fatalf("mkdir ambient config dir: %v", err)
	}
	priorAmbient, priorErr := os.ReadFile(ambientPath)
	if err := os.WriteFile(ambientPath, []byte(ambientUserConfig), 0o644); err != nil {
		t.Fatalf("write ambient config: %v", err)
	}
	t.Cleanup(func() {
		// Restore whatever was at the process-default path so the synthetic
		// ambient config cannot leak into other tests in this package.
		if priorErr == nil {
			_ = os.WriteFile(ambientPath, priorAmbient, 0o644)
			return
		}
		_ = os.Remove(ambientPath)
	})

	// Control: a load that resolves the process-default home sees the
	// ambient fixture. Without this the regression would be vacuous.
	control, err := config.Load(config.LoadOptions{HomeDir: processHome, WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatalf("load ambient config: %v", err)
	}
	if got := control.Providers["ambient-codex"].Auth; got != "oauth" {
		t.Fatalf("control load did not see the ambient OAuth provider: providers=%+v", control.Providers)
	}
	if got := control.MCP.Servers["ambient-mcp"].Auth; got != "oauth" {
		t.Fatalf("control load did not see the ambient OAuth MCP server: servers=%+v", control.MCP.Servers)
	}

	// Injected fixtures: a separate home and working directory. Only their
	// own layer may contribute.
	fixtureHome := t.TempDir()
	fixtureWork := t.TempDir()
	fixturePath := config.UserConfigPath(fixtureHome)
	if err := os.MkdirAll(filepath.Dir(fixturePath), 0o755); err != nil {
		t.Fatalf("mkdir fixture config dir: %v", err)
	}
	if err := os.WriteFile(fixturePath, []byte(fixtureUserConfig), 0o644); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}

	cfg, err := config.Load(config.LoadOptions{HomeDir: fixtureHome, WorkingDir: fixtureWork})
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	if cfg.Project.Name != "fixture-project" {
		t.Errorf("Project.Name = %q, want the fixture value", cfg.Project.Name)
	}
	if cfg.Commands.Test != "fixture-test-cmd" {
		t.Errorf("Commands.Test = %q, want the fixture value", cfg.Commands.Test)
	}
	if _, ok := cfg.Providers["ambient-codex"]; ok {
		t.Errorf("injected load inherited the ambient provider: %+v", cfg.Providers)
	}
	if _, ok := cfg.MCP.Servers["ambient-mcp"]; ok {
		t.Errorf("injected load inherited the ambient MCP server: %+v", cfg.MCP.Servers)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("injected load Providers = %+v, want none declared by the fixture", cfg.Providers)
	}
	if len(cfg.MCP.Servers) != 0 {
		t.Errorf("injected load MCP servers = %+v, want none declared by the fixture", cfg.MCP.Servers)
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

func TestRunWiresMemoryBrowserOpensWithCtrlK(t *testing.T) {
	dir := t.TempDir()
	// Keep the developer's real config and trust store out of the test.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// First-run detection stats the filesystem directly rather than going
	// through the injected config loader, so an empty HOME reads as a first
	// run and opens the connect panel over the TUI, which swallows Ctrl+K.
	// Write a *user* config to clear it: a project-local config would instead
	// make the untrusted temp dir raise the trust prompt, which swallows the
	// key just the same. Without this the test silently depends on the
	// machine running it already having marshal configured.
	if err := os.MkdirAll(config.UserDir(home), 0o755); err != nil {
		t.Fatalf("mkdir user config: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(home), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
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

// TestRunWiresSharedWorkService pins that Run hands the TUI the same Work
// facade the runtime and ACP use: the runtime pointer itself. With a Work
// service attached, the now bar's project-owned work lane is active, so Ctrl+T
// opens the Tasks panel on the Work tab and renders its empty state. A nil
// service (the pre-change wiring) leaves the work lane inactive and produces a
// notice instead of the panel, so the "No work yet" assertion is what proves
// WithWork(rt) reached the model.
func TestRunWiresSharedWorkService(t *testing.T) {
	dir := t.TempDir()
	// Run() resolves the user config and first-run state from the real home
	// dir. Point HOME at a temp dir and write a user config so first-run
	// detection does not open the connect panel over the TUI and swallow the
	// Ctrl+T that opens the Work tab.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(config.UserDir(home), 0o755); err != nil {
		t.Fatalf("mkdir user config: %v", err)
	}
	if err := os.WriteFile(config.UserConfigPath(home), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	// An active legacy route: the runtime must build a runner, because a
	// provider-less startup leaves a NoticeProvider notice that suppresses the
	// whole runner-options block (and with it WithWork). This fixture config
	// needs no network; it only has to resolve a route.
	cfg := config.Default()
	cfg.Profile.Default = "single"
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
			m := model.(tui.Model)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m = updated.(tui.Model)
			updated, _ = m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
			m = updated.(tui.Model)
			view = m.View().Content
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(view, "No work yet") {
		t.Fatalf("view did not render the runtime-backed Work tab:\n%s", view)
	}
	if strings.Contains(view, "Work is unavailable in this runtime.") {
		t.Fatalf("the Work tab rendered the nil-service unavailable notice:\n%s", view)
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
	// The default autoload now ships the using-skills entry point, so the
	// project config opts out to keep the transcript deterministic.
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"), []byte("[project]\nname = \"test\"\n[skills]\nautoload = []\n"), 0644); err != nil {
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

func TestRunMarksSessionEndedEvenWhenProgramRunnerFails(t *testing.T) {
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
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			modelState(t, model).AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
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
	got, err := database.GetSession(fmt.Sprintf("sess_%d", now.UnixNano()))
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt == nil {
		t.Fatal("EndedAt is nil: exit must mark the session ended even with no provider")
	}
	if got.Summary != "" {
		t.Fatalf("Summary = %q, want empty (no knowledge pass)", got.Summary)
	}
}

// Only memories a person curated reach the prompt: confirmed project
// memories, or anything promoted to workspace/global scope. Tentative
// auto-learned rows and stale rows never do.
func TestDBMemoryProviderInjectsOnlyCuratedMemories(t *testing.T) {
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
	for _, text := range []string{"tentative note", "confirmed note", "stale note"} {
		if err := database.SaveMemory(projectID, "fact", text, "sess_1", now); err != nil {
			t.Fatalf("SaveMemory %q: %v", text, err)
		}
	}
	stored, err := database.GetMemories(projectID)
	if err != nil || len(stored) != 3 {
		t.Fatalf("GetMemories = %v, %v; want 3 rows", stored, err)
	}
	if err := database.SetMemoryConfidence(stored[1].ID, db.MemoryConfidenceConfirmed, now.Add(time.Second)); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if err := database.SetMemoryConfidence(stored[2].ID, db.MemoryConfidenceStale, now.Add(time.Second)); err != nil {
		t.Fatalf("stale: %v", err)
	}

	memories, err := (&dbMemoryProvider{db: database}).Memories(projectID)
	if err != nil {
		t.Fatalf("Memories failed: %v", err)
	}
	if len(memories) != 1 || memories[0].Content != "confirmed note" {
		t.Fatalf("memories = %+v, want only the confirmed note", memories)
	}
}

func TestDBMemoryProviderReadsScopesForWorkspace(t *testing.T) {
	t.Setenv("MARSHAL_WORKSPACE", "ws1")
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	mine, _ := database.GetOrCreateProject("/mine", "mine")
	other, _ := database.GetOrCreateProject("/other", "other")
	now := time.Unix(100, 0)
	for _, c := range []struct {
		project int64
		text    string
	}{{other, "global note"}, {other, "workspace note"}, {other, "other ws note"}, {mine, "project note"}} {
		if err := database.SaveMemory(c.project, "fact", c.text, "", now); err != nil {
			t.Fatalf("SaveMemory %q: %v", c.text, err)
		}
	}
	rows, _ := database.GetMemories(other)
	for i, scope := range [][2]string{{"global", ""}, {"workspace", "ws1"}, {"workspace", "ws2"}} {
		if err := database.PromoteMemory(rows[i].ID, scope[0], scope[1], now); err != nil {
			t.Fatalf("PromoteMemory: %v", err)
		}
	}
	notes, err := newDBMemoryProvider(database).Memories(mine)
	if err != nil {
		t.Fatalf("Memories failed: %v", err)
	}
	var got []string
	for _, n := range notes {
		got = append(got, n.Content)
	}
	// "project note" is tentative and project-scoped: not curated, not injected.
	want := []string{"workspace note", "global note"}
	if len(got) != len(want) {
		t.Fatalf("notes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notes = %v, want %v", got, want)
		}
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

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
router = "mock/mock-model"
knowledge = "mock/mock-model"
summarizer = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
planner = "mock/mock-model"
implementer = "mock/mock-model"
reviewer = "mock/mock-model"
security_reviewer = "mock/mock-model"
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
		WithHomeDir(t.TempDir()),
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

func TestRunQuiescesBeforeShutdownHookAndClosesAfter(t *testing.T) {
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

	// Program runner: start tracked work and add a message so the runtime
	// has a turn to quiesce. The work ends when quiesce cancels
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

	// Hook: verify during the shutdown window that the session is
	// quiesced (no new work can start) and the DB is still usable.
	hookRan := make(chan struct{})
	shutdownHook := func(hookCtx context.Context, st *session.State, database *db.DB) {
		// Verify quiesce: BeginWork should be rejected.
		if err := st.BeginWork(); !errors.Is(err, session.ErrSessionQuiescing) {
			t.Errorf("BeginWork during shutdown hook = %v, want ErrSessionQuiescing", err)
		}

		// Verify DB is usable.
		_, err := database.GetOrCreateProject(dir, "knowledge-hook-check")
		if err != nil {
			t.Errorf("DB unusable during shutdown hook: %v", err)
		}

		close(hookRan)
	}

	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithProgramRunner(programRunner),
		WithShutdownHook(shutdownHook),
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

	// Verify the shutdown hook ran.
	select {
	case <-hookRan:
	default:
		t.Fatal("shutdown hook was not called")
	}

	// Verify the database was usable after the hook: the session should be
	// marked ended before Close.
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
	if got.EndedAt == nil {
		t.Fatal("session must be marked ended before Close")
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
	// adds a system message and throws off the transcript count. The
	// default autoload now ships the using-skills entry point, so the
	// project config opts out to keep the transcript deterministic.
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	configContent := `[project]
name = "existing-test"
[skills]
autoload = []
[profile]
default = "mock_profile"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
[models.presets."mock/mock-model"]
local_only = true
[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
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
[models.presets."mock/mock-model"]
local_only = true
[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
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
		WithHomeDir(t.TempDir()),
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
[models.presets."mock/mock-model"]
local_only = true
[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
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
		WithHomeDir(t.TempDir()),
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
[models.presets."mock/mock-model"]
local_only = true
[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx := context.Background()
	_, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
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
	_, toolReg, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
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

type namedScriptedProvider struct {
	*agenttest.ScriptedProvider
	providerName string
}

func (p *namedScriptedProvider) Name() string { return p.providerName }

func TestBuildSubagentFactoryCrossProvider(t *testing.T) {
	parent := &namedScriptedProvider{ScriptedProvider: &agenttest.ScriptedProvider{}, providerName: "parent"}
	other := &namedScriptedProvider{ScriptedProvider: &agenttest.ScriptedProvider{}, providerName: "other"}

	cfg := config.Default()
	cfg.Project.Name = "cross-provider-test"
	cfg.Profile.Default = "cross"
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"parent": {Type: "openai_compatible", BaseURL: "http://parent/v1", APIKey: "parent-key"},
		"other":  {Type: "openai_compatible", BaseURL: "http://other/v1", APIKey: "other-key"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"parent/parent-model": {Provider: "parent", Model: "parent-model"},
		"other/other-model":   {Provider: "other", Model: "other-model"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"cross": {
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "parent/parent-model"},
			},
		},
	}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	resolver := newRoutedProviderResolver(cfg, "")
	resolver.providers["parent"] = parent
	resolver.providers["other"] = other
	router := routing.NewStaticRouter(cfg.RoutingConfig())

	factory, _ := buildSubagentFactory(cfg, state, parent, reg, pol, "parent/parent-model", router, resolver, nil, 0, pricing.ModelPricing{})

	// Same provider keeps the parent instance.
	child, _, err := factory(agent.SubagentRequest{Model: "parent/parent-model"})
	if err != nil {
		t.Fatalf("same-provider factory error = %v", err)
	}
	if child.Provider != parent {
		t.Fatal("same-provider request should keep parent provider instance")
	}

	// Cross-provider resolves to the other provider instance.
	child, _, err = factory(agent.SubagentRequest{Model: "other/other-model"})
	if err != nil {
		t.Fatalf("cross-provider factory error = %v", err)
	}
	if child.Provider != other {
		t.Fatalf("cross-provider request got provider %v, want other", child.Provider)
	}
	if child.Provider.Name() != "other" {
		t.Fatalf("provider name = %q, want other", child.Provider.Name())
	}

	// Unconfigured provider pair returns a clear error.
	_, _, err = factory(agent.SubagentRequest{Model: "missing/missing-model"})
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
	if !strings.Contains(err.Error(), "routing provider") && !strings.Contains(err.Error(), "model") {
		t.Fatalf("error = %v, want provider/model rejection", err)
	}
}

func TestBuildSubagentFactoryCrossProviderFallsBackToParentWhenResolverNil(t *testing.T) {
	cfg := config.Default()
	cfg.Project.Name = "cross-provider-nil-test"
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"other": {Type: "openai_compatible", BaseURL: "http://other/v1", APIKey: "other-key"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"other/other-model": {Provider: "other", Model: "other-model"},
	}
	parent := &namedScriptedProvider{ScriptedProvider: &agenttest.ScriptedProvider{}, providerName: "parent"}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	router := routing.NewStaticRouter(cfg.RoutingConfig())

	factory, _ := buildSubagentFactory(cfg, state, parent, reg, pol, "parent/parent-model", router, nil, nil, 0, pricing.ModelPricing{})
	// With a nil resolver and a cross-provider pair, routing still resolves
	// but the factory keeps the parent provider instead of calling the resolver.
	child, _, err := factory(agent.SubagentRequest{Model: "other/other-model"})
	if err != nil {
		t.Fatalf("nil resolver factory error = %v", err)
	}
	if child.Provider != parent {
		t.Fatal("nil resolver should keep parent provider")
	}
}

func TestBuildSubagentFactoryRolePinning(t *testing.T) {
	parent := &namedScriptedProvider{ScriptedProvider: &agenttest.ScriptedProvider{}, providerName: "parent"}
	other := &namedScriptedProvider{ScriptedProvider: &agenttest.ScriptedProvider{}, providerName: "other"}

	cfg := config.Default()
	cfg.Project.Name = "role-pinning-test"
	cfg.Profile.Default = "pin"
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"parent": {Type: "openai_compatible", BaseURL: "http://parent/v1", APIKey: "parent-key"},
		"other":  {Type: "openai_compatible", BaseURL: "http://other/v1", APIKey: "other-key"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"parent/parent-model":  {Provider: "parent", Model: "parent-model"},
		"other/reviewer-model": {Provider: "other", Model: "reviewer-model"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"pin": {
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "parent/parent-model"},
				routing.RoleReviewer:    {Preset: "other/reviewer-model"},
			},
		},
	}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	resolver := newRoutedProviderResolver(cfg, "")
	resolver.providers["parent"] = parent
	resolver.providers["other"] = other
	router := routing.NewStaticRouter(cfg.RoutingConfig())

	factory, _ := buildSubagentFactory(cfg, state, parent, reg, pol, "parent/parent-model", router, resolver, nil, 0, pricing.ModelPricing{})

	// Explicitly bound reviewer role pins model and provider.
	child, _, err := factory(agent.SubagentRequest{Role: routing.RoleReviewer})
	if err != nil {
		t.Fatalf("role-pinned factory error = %v", err)
	}
	if child.Model != "reviewer-model" {
		t.Fatalf("child model = %q, want reviewer-model", child.Model)
	}
	if child.Provider != other {
		t.Fatal("cross-provider reviewer binding should switch provider")
	}

	// Unbound role keeps the default model.
	child, _, err = factory(agent.SubagentRequest{Role: routing.RoleSubtask})
	if err != nil {
		t.Fatalf("unbound role factory error = %v", err)
	}
	if child.Model != "parent/parent-model" {
		t.Fatalf("unbound role child model = %q, want parent/parent-model", child.Model)
	}
	if child.Provider != parent {
		t.Fatal("unbound role should keep parent provider")
	}

	// Explicit model beats role binding.
	child, _, err = factory(agent.SubagentRequest{Role: routing.RoleReviewer, Model: "parent/parent-model"})
	if err != nil {
		t.Fatalf("explicit-model factory error = %v", err)
	}
	if child.Model == "reviewer-model" {
		t.Fatal("explicit model should beat role binding")
	}
	if child.Provider != parent {
		t.Fatal("explicit model should keep same provider")
	}
}

func TestBuildSubagentFactorySubtaskIterationsCap(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()

	// Unset config → unlimited; users must explicitly opt into a child cap.
	factory, _ := buildSubagentFactory(cfg, state, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.MaxToolIterations != 0 {
		t.Fatalf("MaxToolIterations = %d, want 0 (unlimited)", child.MaxToolIterations)
	}

	// Explicit value set in config.
	cfg.Agent.SubtaskIterations = 5
	state = session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetLayers(config.Layers{SubtaskIterationsSet: true})
	factory, _ = buildSubagentFactory(cfg, state, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err = factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.MaxToolIterations != 5 {
		t.Fatalf("MaxToolIterations = %d, want 5", child.MaxToolIterations)
	}

	// Explicit 0 in config with SubtaskIterationsSet → unlimited.
	cfg.Agent.SubtaskIterations = 0
	state = session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetLayers(config.Layers{SubtaskIterationsSet: true})
	factory, _ = buildSubagentFactory(cfg, state, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err = factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.MaxToolIterations != 0 {
		t.Fatalf("MaxToolIterations = %d, want 0 (unlimited)", child.MaxToolIterations)
	}

	// Custom agent max_iterations still wins.
	cfg.Agent.SubtaskIterations = 5
	state = session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetLayers(config.Layers{SubtaskIterationsSet: true})
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"reviewer": {Preset: "m", MaxIterations: 99},
	}
	factory, _ = buildSubagentFactory(cfg, state, nil, reg, nil, "m", routing.NewStaticRouter(cfg.RoutingConfig()), nil, nil, 0, pricing.ModelPricing{})
	child, _, err = factory(agent.SubagentRequest{Agent: "reviewer"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.MaxToolIterations != 99 {
		t.Fatalf("MaxToolIterations = %d, want 99 (custom agent override)", child.MaxToolIterations)
	}
}

// TestBuildSubagentFactoryVerificationGate pins the global fallback: with
// [agent] verification_gate=true, every agent.run child inherits the gate so
// subagents verify before finishing just like the parent runner. The default
// must stay off.
func TestBuildSubagentFactoryVerificationGate(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	gate := true
	cfg.Agent.VerificationGate = &gate

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	factory, _ := buildSubagentFactory(cfg, state, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !child.VerificationGate {
		t.Fatal("child.VerificationGate = false, want true (global fallback)")
	}

	cfgDefault := config.Default()
	cfgDefault.Privacy.RemoteProvidersAllowed = true
	cfgDefault.Providers = cfg.Providers
	cfgDefault.Models.Presets = cfg.Models.Presets
	stateDefault := session.New(cfgDefault, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factoryDefault, _ := buildSubagentFactory(cfgDefault, stateDefault, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	childDefault, _, err := factoryDefault(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory (default): %v", err)
	}
	if childDefault.VerificationGate {
		t.Fatal("child.VerificationGate = true, want false by default")
	}
}

// TestRoleRunnerVerificationGateFallback pins the same global fallback for
// pipeline/swarm role runners built via roleRunnerSpec.newRunner.
func TestRoleRunnerVerificationGateFallback(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleSDDImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	gate := true
	cfg.Agent.VerificationGate = &gate

	resolver := newRoutedProviderResolver(cfg, "")
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	spec := roleRunnerSpec{
		cfg:         cfg,
		resolver:    resolver,
		reg:         reg,
		readOnlyReg: registry.ReadOnlyView(reg),
		pol:         policy.NewEngine(&cfg, nil),
		state:       session.New(cfg, t.TempDir(), time.Now(), session.Persistence{}),
	}
	runner, err := spec.newRunner(agent.RoleSDDImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if !runner.VerificationGate {
		t.Fatal("role runner VerificationGate = false, want true (global fallback)")
	}
}

// TestAgentRunSystemFlagInherits pins the AND at child construction: a parent
// with system access plus an explicit system:true request yields a child whose
// own session state carries the flag.
func TestAgentRunSystemFlagInherits(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	parentState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	parentState.SetSystemAccess(true)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, registry.New(), nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	_, childState, err := factory(agent.SubagentRequest{System: true})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !childState.SystemAccess() {
		t.Fatal("child SystemAccess = false, want true (parent has it and the request asked)")
	}
}

// TestAgentRunSystemFlagDefaultOff pins default containment: a parent with
// system access that does NOT pass system:true yields a child without it.
func TestAgentRunSystemFlagDefaultOff(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	parentState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	parentState.SetSystemAccess(true)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, registry.New(), nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	_, childState, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if childState.SystemAccess() {
		t.Fatal("child SystemAccess = true, want false (the request did not ask for it)")
	}
}

// TestAgentRunSystemFlagParentWithoutSystem pins the other half of the AND: a
// request that asks for system access cannot manufacture it when the parent
// does not have it. The handler rejects this earlier with a clean tool error;
// this test covers the construction-time guard directly.
func TestAgentRunSystemFlagParentWithoutSystem(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	parentState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	if parentState.SystemAccess() {
		t.Fatal("precondition: parent must not have system access")
	}
	factory, _ := buildSubagentFactory(cfg, parentState, nil, registry.New(), nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	_, childState, err := factory(agent.SubagentRequest{System: true})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if childState.SystemAccess() {
		t.Fatal("child SystemAccess = true, want false (the parent does not have it)")
	}
}

func TestBuildSubagentFactorySeedsChildSessionContext(t *testing.T) {
	// Regression: a child session built without the parent's layer snapshot
	// makes layer-aware project saves (config.SaveProjectConfig) fall back
	// to write-everything, so a subagent that saves config would
	// bake user-global values into the committable .marshal/config.toml.
	cfg := config.Default()
	cfg.Project.Name = "child-context-test"
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	layers := config.Layers{Default: config.Default(), User: config.Default(), Merged: cfg}
	layers.User.Commands.Test = "user-scoped-test-command"
	state.SetLayers(layers)
	state.SetHomeDir("/home/child-context-test")
	state.SetTrusted(true)

	factory, _ := buildSubagentFactory(cfg, state, nil, registry.New(), nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	_, childState, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if got := childState.Layers().User.Commands.Test; got != "user-scoped-test-command" {
		t.Errorf("child Layers().User.Commands.Test = %q, want the parent's snapshot", got)
	}
	if got := childState.HomeDir(); got != "/home/child-context-test" {
		t.Errorf("child HomeDir = %q, want the parent's", got)
	}
	if !childState.Trusted() {
		t.Error("child Trusted = false, want the parent's true")
	}
}

func TestBuildSubagentFactoryCustomAgentSamplingOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	presetTemp := 0.2
	agentTemp := 0.7
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m", Temperature: &presetTemp, Thinking: "high"},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"reviewer": {Preset: "m", Temperature: &agentTemp, Thinking: "low"},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	router := routing.NewStaticRouter(cfg.RoutingConfig())

	factory, _ := buildSubagentFactory(cfg, state, nil, reg, pol, "m", router, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Agent: "reviewer"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.TemperatureOverride == nil || *child.TemperatureOverride != 0.7 {
		t.Fatalf("TemperatureOverride = %v, want 0.7", child.TemperatureOverride)
	}
	if child.ThinkingOverride != "low" {
		t.Fatalf("ThinkingOverride = %q, want low", child.ThinkingOverride)
	}

	// Ad-hoc agent.run parameters win over the named agent's config.
	adhocTemp := 0.9
	child, _, err = factory(agent.SubagentRequest{Agent: "reviewer", Temperature: &adhocTemp, Thinking: "medium"})
	if err != nil {
		t.Fatalf("factory (ad-hoc): %v", err)
	}
	if child.TemperatureOverride == nil || *child.TemperatureOverride != 0.9 {
		t.Fatalf("TemperatureOverride (ad-hoc) = %v, want 0.9", child.TemperatureOverride)
	}
	if child.ThinkingOverride != "medium" {
		t.Fatalf("ThinkingOverride (ad-hoc) = %q, want medium", child.ThinkingOverride)
	}

	// No overrides anywhere → nil/"" on the child.
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"plain": {Preset: "m"},
	}
	router = routing.NewStaticRouter(cfg.RoutingConfig())
	state = session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory, _ = buildSubagentFactory(cfg, state, nil, reg, pol, "m", router, nil, nil, 0, pricing.ModelPricing{})
	child, _, err = factory(agent.SubagentRequest{Agent: "plain"})
	if err != nil {
		t.Fatalf("factory (plain): %v", err)
	}
	if child.TemperatureOverride != nil {
		t.Fatalf("TemperatureOverride (plain) = %v, want nil", child.TemperatureOverride)
	}
	if child.ThinkingOverride != "" {
		t.Fatalf("ThinkingOverride (plain) = %q, want empty", child.ThinkingOverride)
	}
}

func TestBuildSubagentFactoryReviewerInheritedKeepsDefault(t *testing.T) {
	parent := &namedScriptedProvider{ScriptedProvider: &agenttest.ScriptedProvider{}, providerName: "parent"}

	cfg := config.Default()
	cfg.Project.Name = "reviewer-inherited-test"
	cfg.Profile.Default = "inherit"
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"parent": {Type: "openai_compatible", BaseURL: "http://parent/v1", APIKey: "parent-key"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"parent/parent-model": {Provider: "parent", Model: "parent-model"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"inherit": {
			Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "parent/parent-model"},
			},
		},
	}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	pol := policy.NewEngine(&cfg, nil)
	router := routing.NewStaticRouter(cfg.RoutingConfig())

	factory, _ := buildSubagentFactory(cfg, state, parent, reg, pol, "parent/parent-model", router, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Role: routing.RoleReviewer})
	if err != nil {
		t.Fatalf("inherited reviewer factory error = %v", err)
	}
	if child.Model != "parent/parent-model" {
		t.Fatalf("inherited reviewer child model = %q, want parent/parent-model", child.Model)
	}
	if child.Provider != parent {
		t.Fatal("inherited reviewer should keep parent provider")
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
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
			routing.RoleReviewer:    {CustomAgent: "strict"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"strict": {Name: "strict", Preset: "ollama/gpt-4o-mini", SystemPrompt: "be strict", ToolDenylist: []string{"file.write_patch"}, ApprovalMode: "plan", MaxIterations: 5},
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
			routing.RoleSDDImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
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
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true, ToolCalling: "native"},
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
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"my-scout": {Name: "my-scout", Preset: "ollama/gpt-4o-mini"},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, nil, 1, pricing.ModelPricing{})
	child, childState, err := factory(agent.SubagentRequest{Agent: "my-scout"})
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
		t.Fatal("child.UsageObserver should be set so subagent usage is recorded on the child session")
	}
	// Child usage lands on the child's own state (visible when drilled in)
	// and must NOT inflate the parent's turn-usage counter, which the
	// status bar shows as live parent-turn context use.
	child.UsageObserver(schema.TokenUsage{PromptTokens: 100, CompletionTokens: 50})
	if used, _ := parentState.TurnUsage(); used != 0 {
		t.Fatalf("parent turn usage = %d, want 0 — child tokens must not move the parent counter", used)
	}
	if used, _ := childState.TurnUsage(); used != 150 {
		t.Fatalf("child turn usage = %d, want 150", used)
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
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, nil, 1, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.UsageObserver == nil || child.MetricsObserver == nil {
		t.Fatal("ad-hoc subagent children must also carry UsageObserver + MetricsObserver")
	}
}

// An agent.run child's todo.write must write to the child's own session,
// not the parent's visible list.
func TestSubagentChildTodosAreIsolated(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "todo.write", Risk: registry.RiskWorkspaceWrite, Schema: json.RawMessage(`{"type":"object"}`), Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil // parent stand-in; must be replaced in the child view
	}})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, nil, 1, pricing.ModelPricing{})

	_, childState, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	// The child's registry is internal to the factory, so assert through
	// the observable contract: writing via a child-bound tool lands on the
	// child state, and the parent list stays empty.
	tool := native.TodoWriteTool(childState)
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Name: "todo.write", Args: []byte(`{"todos":[{"content":"child task","status":"pending"}]}`)}); err != nil {
		t.Fatalf("child todo.write: %v", err)
	}
	if len(parentState.Todos()) != 0 {
		t.Fatalf("parent todos = %+v, want empty (isolation)", parentState.Todos())
	}
	if len(childState.Todos()) != 1 {
		t.Fatalf("child todos = %+v, want the written item", childState.Todos())
	}
}

// TestSubagentFactoryExplicitModelOverridesNamedAgent verifies that an
// explicit model on a named-agent request replaces only the preset
// provider/model while retaining the agent's other overrides (addendum,
// tool denylist, max iterations).
func TestSubagentFactoryExplicitModelOverridesNamedAgent(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
		"other":  {Type: "openai_compatible", BaseURL: "http://localhost:11435/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
		"other/x":            {Provider: "other", Model: "x", LocalOnly: true},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"my-scout": {Name: "my-scout", Preset: "ollama/gpt-4o-mini", SystemPrompt: "scout addendum", ToolDenylist: []string{"web.fetch"}, MaxIterations: 7},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	stub := func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: stub})
	_ = reg.Register(registry.Tool{Name: "web.fetch", Risk: registry.RiskNetwork, Handler: stub})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, nil, 1, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Agent: "my-scout", Model: "other/x"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Model != "x" {
		t.Fatalf("child.Model = %q, want x (explicit override)", child.Model)
	}
	if child.SystemPromptAddendum == "" || !strings.Contains(child.SystemPromptAddendum, "scout addendum") {
		t.Fatalf("child.SystemPromptAddendum should retain the agent addendum, got %q", child.SystemPromptAddendum)
	}
	if child.MaxToolIterations != 7 {
		t.Fatalf("child.MaxToolIterations = %d, want 7 (agent override)", child.MaxToolIterations)
	}
	if child.Registry == nil {
		t.Fatal("child.Registry should be set")
	}
	if _, ok := child.Registry.Lookup("web.fetch"); ok {
		t.Fatal("child registry must apply the agent's tool denylist (web.fetch excluded)")
	}
	if _, ok := child.Registry.Lookup("file.read"); !ok {
		t.Fatal("child registry should retain non-denylisted tools (file.read)")
	}
}

// TestSubagentFactoryExplicitModelAdHoc verifies an explicit model on an
// ad-hoc request selects that model instead of the default.
func TestSubagentFactoryExplicitModelAdHoc(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini":       {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
		"ollama/qwen2.5-coder:32b": {Provider: "ollama", Model: "qwen2.5-coder:32b", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, nil, 1, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Model: "ollama/qwen2.5-coder:32b"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Model != "qwen2.5-coder:32b" {
		t.Fatalf("child.Model = %q, want qwen2.5-coder:32b", child.Model)
	}
}

// TestSubagentFactoryInvalidPairErrors verifies an invalid provider/model
// pair fails clearly before any child is built.
func TestSubagentFactoryInvalidPairErrors(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, nil, reg, pol, "fallback", router, nil, nil, 1, pricing.ModelPricing{})
	if _, _, err := factory(agent.SubagentRequest{Model: "bogus"}); err == nil {
		t.Fatal("expected an error for a malformed provider/model pair")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error should name the pair, got: %v", err)
	}
}

// namedProvider is a minimal provider implementation used by cross-provider
// subagent tests to assert the factory switches providers.
type namedProvider struct{ name string }

func (p namedProvider) Name() string { return p.name }
func (p namedProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (p namedProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	return nil, errors.New("not implemented")
}
func (p namedProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}

func TestSubagentFactoryCrossProviderSwitchesProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
		"other":  {Type: "openai_compatible", BaseURL: "http://localhost:11435/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
		"other/x":            {Provider: "other", Model: "x", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	parentProvider := namedProvider{name: "ollama"}
	resolver := newRoutedProviderResolver(cfg, "")
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, parentProvider, reg, pol, "fallback", router, resolver, nil, 1, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Model: "other/x"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Provider == nil {
		t.Fatal("child.Provider is nil")
	}
	if child.Provider.Name() != "other" {
		t.Fatalf("child.Provider.Name() = %q, want other", child.Provider.Name())
	}
	if child.Model != "x" {
		t.Fatalf("child.Model = %q, want x", child.Model)
	}
}

func TestSubagentFactoryCrossProviderUnconfiguredErrors(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini": {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
		"other/x":            {Provider: "other", Model: "x", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	parentProvider := namedProvider{name: "ollama"}
	resolver := newRoutedProviderResolver(cfg, "")
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, parentProvider, reg, pol, "fallback", router, resolver, nil, 1, pricing.ModelPricing{})
	_, _, err := factory(agent.SubagentRequest{Model: "other/x"})
	if err == nil {
		t.Fatal("expected error for unconfigured provider")
	}
	if !strings.Contains(err.Error(), "other") {
		t.Fatalf("error should mention provider, got: %v", err)
	}
}

func TestSubagentFactorySameProviderKeepsParentInstance(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Profile.Default = "p"
	cfg.Providers = map[string]config.ProviderConfig{
		"ollama": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1", APIKey: "test", ToolCalling: true},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"ollama/gpt-4o-mini":       {Provider: "ollama", Model: "gpt-4o-mini", LocalOnly: true},
		"ollama/qwen2.5-coder:32b": {Provider: "ollama", Model: "qwen2.5-coder:32b", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "ollama/gpt-4o-mini"},
		}},
	}
	parentProvider := namedProvider{name: "ollama"}
	resolver := newRoutedProviderResolver(cfg, "")
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	parentState := session.New(cfg, t.TempDir(), time.Now(), session.Persistence{})
	reg := registry.New()
	_ = reg.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly})
	pol := policy.NewEngine(&cfg, nil)
	factory, _ := buildSubagentFactory(cfg, parentState, parentProvider, reg, pol, "fallback", router, resolver, nil, 1, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Model: "ollama/qwen2.5-coder:32b"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Provider.Name() != parentProvider.Name() {
		t.Fatal("same-provider child should reuse the parent provider instance")
	}
}

func TestBuildSubagentFactorySetsNativeToolsFromProviderCapability(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"local/m": {Provider: "local", Model: "m"},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()

	// Provider with tool calling → NativeTools = true.
	parent := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{ToolCalling: true},
	}
	factory, _ := buildSubagentFactory(cfg, state, parent, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if !child.NativeTools {
		t.Fatalf("NativeTools = false, want true when provider supports tool calling")
	}
	if child.ResponseFormat != nil {
		t.Fatalf("ResponseFormat = %+v, want nil in native mode", child.ResponseFormat)
	}

	// Provider without tool calling → NativeTools = false, ResponseFormat set.
	parentNoTools := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{ToolCalling: false, StructuredOutput: true},
	}
	factory, _ = buildSubagentFactory(cfg, state, parentNoTools, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err = factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.NativeTools {
		t.Fatalf("NativeTools = true, want false when provider lacks tool calling")
	}
	if child.ResponseFormat == nil {
		t.Fatalf("ResponseFormat = nil, want non-nil fallback when provider lacks tool calling")
	}
}

func TestBuildSubagentFactorySetsRouteResolver(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"local/m": {Provider: "local", Model: "m", ContextWindow: 32000, MaxOutputTokens: 4096},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()

	parent := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{ToolCalling: true},
	}
	resolver := newRoutedProviderResolver(cfg, "")
	factory, _ := buildSubagentFactory(cfg, state, parent, reg, nil, "m", nil, resolver, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.RouteResolver == nil {
		t.Fatal("RouteResolver should be set on child runner")
	}
	// Verify it resolves a route carrying the preset's context window.
	route, _, err := child.RouteResolver.Resolve("question")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if route.Preset.ContextWindow == 0 {
		t.Fatal("resolved route should carry a non-zero context window on its preset")
	}
}

func TestBuildSubagentFactoryCustomAgentMaxIterationsUnlimited(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	cfg.CustomAgents = map[string]routing.CustomAgent{
		"scout": {Preset: "m", MaxIterationsUnlimited: true},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()

	factory, _ := buildSubagentFactory(cfg, state, nil, reg, nil, "m", routing.NewStaticRouter(cfg.RoutingConfig()), nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Agent: "scout"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.MaxToolIterations != 0 {
		t.Fatalf("MaxToolIterations = %d, want 0 (unlimited) for custom agent with max_iterations_unlimited=true", child.MaxToolIterations)
	}
}

func TestBuildSubagentFactoryExplicitModelUsesStaticRouteResolver(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local":  {Type: "ollama", BaseURL: "http://local/v1"},
		"remote": {Type: "openai_compatible", BaseURL: "http://remote/v1", APIKey: "k"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"local/m":    {Provider: "local", Model: "m", ContextWindow: 32000, MaxOutputTokens: 4096},
		"remote/big": {Provider: "remote", Model: "big", ContextWindow: 128000, MaxOutputTokens: 8192},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	router := routing.NewStaticRouter(cfg.RoutingConfig())
	resolver := newRoutedProviderResolver(cfg, "")

	parent := &agenttest.ScriptedProvider{
		ProviderCaps: schema.ProviderCapabilities{ToolCalling: true},
	}
	factory, _ := buildSubagentFactory(cfg, state, parent, reg, nil, "m", router, resolver, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{Model: "remote/big"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.RouteResolver == nil {
		t.Fatal("RouteResolver should be set for explicit model")
	}
	route, _, err := child.RouteResolver.Resolve("question")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if route.Preset.Model != "big" {
		t.Fatalf("resolved model = %q, want 'big'", route.Preset.Model)
	}
	if route.Preset.ContextWindow != 128000 {
		t.Fatalf("resolved context window = %d, want 128000", route.Preset.ContextWindow)
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

func TestBuildAgentRunnerRegistersOnlyConfigRead(t *testing.T) {
	// config.read is registered whatever the reloader; the setter tools are
	// gone, so no reloader can add one to the registry.
	ctx := context.Background()
	cfg := nativeToolAgentConfig("test-provider")
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	_, reg, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if _, ok := reg.Lookup("config.read"); !ok {
		t.Fatal("config.read not registered")
	}
	for _, tool := range reg.List() {
		if strings.HasPrefix(tool.Name, "config.") && tool.Name != "config.read" {
			t.Fatalf("config setter %s must not be registered", tool.Name)
		}
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
	_, reg, _, _, _, _, closer, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
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

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"

[lsp]
enabled = true

# The server language key must be one the active root actually contains:
# the manager starts servers only for languages present in the repo. The
# stub script below is lsp-stub.sh, so "shell" is the key that matches.
[lsp.servers.shell]
command = "` + stub + `"
`
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(configToml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt, err := StartRuntime(ctx, WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime() error = %v", err)
	}
	defer rt.Close(context.Background())

	if rt.LSPManager == nil {
		t.Fatal("LSPManager was not constructed even though [lsp.servers.shell] was configured")
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

func TestRuntimeNewSessionResetsState(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()
	cfg := nativeToolAgentConfig("session-test")
	// The default autoload injects the using-skills body into a fresh
	// session; opt out so the reset assertion sees an empty transcript.
	cfg.Skills.Autoload = nil

	rt, err := StartRuntime(ctx,
		WithWorkingDir(tmp),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)

	oldID := rt.SessionID
	oldDB := rt.DB
	oldProjectID := rt.ProjectID

	rt.State.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	rt.State.AddMessage(session.RoleAssistant, "hi", session.ContentTypePlain)
	rt.State.SetContextPack(contextpack.Pack{
		Sections:   []contextpack.Section{{Title: "ctx", EstimatedTokens: 1000}},
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 1000, MaxTokens: 2000},
	})
	rt.State.SetTurnUsage(123)
	rt.State.SetTurnContextWindow(1000)

	newState, _, _, _, _, _, _, err := rt.NewSession("")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if newState.SessionID() == oldID {
		t.Fatalf("expected new session id, got %s", newState.SessionID())
	}
	if rt.SessionID != newState.SessionID() {
		t.Fatalf("runtime.SessionID not updated: %s", rt.SessionID)
	}
	if rt.ProjectID != oldProjectID {
		t.Fatalf("project id changed: %d -> %d", oldProjectID, rt.ProjectID)
	}
	if rt.DB != oldDB {
		t.Fatal("database handle changed")
	}
	if len(newState.Transcript()) != 0 {
		t.Fatalf("expected empty transcript, got %d items", len(newState.Transcript()))
	}
	if pack := newState.ContextPack(); !pack.IsEmpty() {
		t.Fatalf("expected empty context pack, got %+v", pack)
	}
	if used, window := newState.TurnUsage(); used != 0 || window != 0 {
		t.Fatalf("expected zero turn usage, got used=%d window=%d", used, window)
	}

	database := must[*db.DB](rt.DB)
	sessions, _, err := database.ListSessions(ctx, tmp, "", 100)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	foundOld := false
	for _, s := range sessions {
		if s.SessionID == oldID {
			foundOld = true
			break
		}
	}
	if !foundOld {
		t.Fatalf("old session %s not persisted", oldID)
	}
}

func TestRoleRunnerWiredWithResolverAndLimits(t *testing.T) {
	cfg := config.Default()
	cfg.Project.Name = "role-runner-wiring-test"
	cfg.Profile.Default = "main"
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "openai_compatible", BaseURL: "http://local/v1", APIKey: "k"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"local/impl": {Provider: "local", Model: "impl", ContextWindow: 32768},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"main": {Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "local/impl"},
		}},
	}

	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	lt := limits.NewTable(nil)
	spec := roleRunnerSpec{
		cfg:         cfg,
		state:       state,
		pol:         policy.NewEngine(&cfg, nil),
		resolver:    newRoutedProviderResolver(cfg, ""),
		reg:         registry.New(),
		memory:      &dbMemoryProvider{db: database},
		projectID:   1,
		limitsTable: &lt,
		modelProbe: func(provider, model string) agent.ModelProbe {
			return agent.ModelProbe{Window: 16384, MaxOutput: 4096, OK: true}
		},
		database: database,
	}

	r, err := spec.newRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if r.RouteResolver == nil {
		t.Fatal("RouteResolver not wired on role runner")
	}
	if r.ModelProbe == nil {
		t.Fatal("ModelProbe not wired on role runner")
	}
	route, _, err := r.RouteResolver.Resolve("edit")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	probe := r.ModelProbe(route.Preset.Provider, route.Preset.Model)
	if !probe.OK || probe.Window != 16384 {
		t.Fatalf("role runner probe = %+v, want the provider-reported 16384", probe)
	}
	if got := agent.ResolveModelLimits(route.Preset, r.LimitsTable, probe, nil).Window; got != 16384 {
		t.Fatalf("role runner window = %d, want the provider-reported 16384", got)
	}
	if route.Preset.Model != "impl" {
		t.Fatalf("model = %q, want impl", route.Preset.Model)
	}
	if r.LimitsTable != &lt {
		t.Fatal("LimitsTable not wired on role runner")
	}
	window := agent.ResolveModelLimits(route.Preset, r.LimitsTable, agent.ModelProbe{}, nil).Window
	if window != 32768 {
		t.Fatalf("window = %d, want 32768 from preset config", window)
	}
}

func TestPipelineRoleRunnerGetsRollover(t *testing.T) {
	newSpec := func(t *testing.T, rolloverEnabled, childSession bool) (roleRunnerSpec, *db.DB) {
		t.Helper()
		cfg := config.Default()
		cfg.Project.Name = "role-runner-rollover-test"
		cfg.Profile.Default = "main"
		cfg.Privacy.RemoteProvidersAllowed = true
		cfg.Providers = map[string]config.ProviderConfig{
			"local": {Type: "openai_compatible", BaseURL: "http://local/v1", APIKey: "k"},
		}
		cfg.Models.Presets = map[string]routing.ModelPreset{
			"local/impl": {Provider: "local", Model: "impl", ContextWindow: 32768},
		}
		cfg.AgentProfiles = map[string]routing.AgentProfile{
			"main": {Roles: map[routing.AgentRole]routing.RoleBinding{
				routing.RoleImplementer: {Preset: "local/impl"},
			}},
		}
		cfg.Session.Rollover.Enabled = rolloverEnabled

		database, err := db.Open(":memory:")
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		t.Cleanup(func() { database.Close() })
		if err := database.Migrate(); err != nil {
			t.Fatalf("migrate db: %v", err)
		}
		projectID, err := database.GetOrCreateProject(t.TempDir(), "rollover-test")
		if err != nil {
			t.Fatalf("create project: %v", err)
		}

		state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		return roleRunnerSpec{
			cfg:          cfg,
			state:        state,
			pol:          policy.NewEngine(&cfg, nil),
			resolver:     newRoutedProviderResolver(cfg, ""),
			reg:          registry.New(),
			memory:       &dbMemoryProvider{db: database},
			projectID:    projectID,
			database:     database,
			childSession: childSession,
		}, database
	}

	// Pipeline (child-session) runner with rollover enabled: controller set.
	spec, _ := newSpec(t, true, true)
	r, err := spec.newRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if r.Rollover == nil || r.Rollover.Controller == nil {
		t.Fatal("pipeline role runner should get a rollover controller")
	}
	if !r.CloseRolloverOnDone {
		t.Fatal("pipeline role runner should close its rollover controller when done")
	}

	// Swarm (shared-session) runner: never — concurrent controllers on the
	// shared parent session would corrupt generation sequencing.
	spec, _ = newSpec(t, true, false)
	r, err = spec.newRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if r.Rollover != nil {
		t.Fatal("swarm role runner must not get per-runner rollover")
	}
	if r.CloseRolloverOnDone {
		t.Fatal("swarm role runner must not close a rollover controller it does not own")
	}

	// Rollover disabled in config: no controller even for pipeline runners.
	spec, _ = newSpec(t, false, true)
	r, err = spec.newRunner(agent.RoleImplementer, swarm.ScopeFull)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	if r.Rollover != nil {
		t.Fatal("rollover disabled in config must mean no controller")
	}
}

// fakeSnapshotter is a minimal Snapshotter for testing child-runner wiring.
type fakeSnapshotter struct{ tracked bool }

func (f *fakeSnapshotter) Track(ctx context.Context) (string, error) {
	f.tracked = true
	return "fake-hash", nil
}
func (f *fakeSnapshotter) Diff(ctx context.Context, hash string) (string, error) {
	return "", nil
}
func (f *fakeSnapshotter) Restore(ctx context.Context, hash string) error { return nil }

// TestBuildSubagentFactoryWiresSnapshotter verifies C-1: when the parent
// session has a Snapshotter, the child runner gets it (and the DB recorder)
// so pre-write snapshots are recorded against the child's session ID.
func TestBuildSubagentFactoryWiresSnapshotter(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	snap := &fakeSnapshotter{}
	state.SetSnapshotter(snap)
	reg := registry.New()

	factory, _ := buildSubagentFactory(cfg, state, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Snapshotter == nil {
		t.Fatal("child.Snapshotter = nil, want the parent's snapshot service")
	}
	if child.SnapshotRecorder == nil {
		t.Fatal("child.SnapshotRecorder = nil, want the DB recorder")
	}
}

// TestBuildSubagentFactoryNoSnapshotterWhenParentHasNone verifies that when
// the parent session has no Snapshotter, the child also gets none (no nil
// dereference).
func TestBuildSubagentFactoryNoSnapshotterWhenParentHasNone(t *testing.T) {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"local": {Type: "ollama", BaseURL: "http://local/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "local", Model: "m"},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()

	factory, _ := buildSubagentFactory(cfg, state, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{})
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Snapshotter != nil {
		t.Fatal("child.Snapshotter should be nil when parent has none")
	}
	if child.SnapshotRecorder != nil {
		t.Fatal("child.SnapshotRecorder should be nil when parent has no snapshotter")
	}
}

// A child's shell output must reach the child's own active tool call, never
// the parent's — which during a wait is agent.await, where it appeared as
// stray go-test output under the spinner.
func TestSubagentShellOutputDoesNotReachParent(t *testing.T) {
	cfg := config.Default()
	parentState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	childReg := registry.New()
	// Build a minimal native toolset for the child. Its commands belong to the
	// conversation, so the child registry borrows an explicit owner: the
	// parent's running-count callback is the one this fixture is separating
	// OUTPUT from, and output follows SessionState, not the service.
	childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	dir := t.TempDir()
	childRunner := &fakeRunner{run: func(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		if req.Stdout != nil {
			_, _ = req.Stdout.Write([]byte("marshal-scope-probe\n"))
		}
		return native.CommandResult{Stdout: "marshal-scope-probe\n", ExitCode: 0}, nil
	}}
	jm := newOwnedCommandService(t, childRunner, dir, childState, nil)
	nativeOpts := native.Options{
		WorkspaceRoot:  dir,
		CommandRunner:  childRunner,
		SessionState:   childState,
		WorkspaceState: parentState,
		Config:         cfg,
		JobManager:     jm,
	}
	if err := native.RegisterAll(childReg, nativeOpts); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Set an active tool call on the parent (simulating agent.await in flight).
	parentState.SetActiveToolCall(session.ActiveToolCall{Name: "agent.await"})
	// Run shell.run "echo marshal-scope-probe" through the child's registry.
	tool, ok := childReg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run not found in child registry")
	}
	// Execute the tool — we just need the output to land on the child.
	_, err := tool.Handler(context.Background(), registry.ToolCall{Name: "shell.run", Args: []byte(`{"command":"echo marshal-scope-probe"}`)})
	if err != nil {
		t.Fatalf("shell.run: %v", err)
	}
	parentOut, _ := parentState.ActiveToolCall()
	if strings.Contains(parentOut.Output, "marshal-scope-probe") {
		t.Fatalf("parent got child's output: %q", parentOut.Output)
	}
}

// The scope split must not move backups off the parent.
func TestSubagentEditsStillBackUpToParent(t *testing.T) {
	cfg := config.Default()
	parentState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	childReg := registry.New()
	childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	dir := t.TempDir()
	nativeOpts := native.Options{
		WorkspaceRoot:  dir,
		SessionState:   childState,
		WorkspaceState: parentState,
		Config:         cfg,
	}
	if err := native.RegisterAll(childReg, nativeOpts); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Write a file through the child's file.write tool.
	tool, ok := childReg.Lookup("file.write")
	if !ok {
		t.Fatal("file.write not found in child registry")
	}
	_, err := tool.Handler(context.Background(), registry.ToolCall{Name: "file.write", Args: []byte(`{"path":"probe.txt","content":"hello"}`)})
	if err != nil {
		t.Fatalf("file.write: %v", err)
	}
	if !parentState.HasBackup() {
		t.Fatal("parent should have a backup for /undo, got 0")
	}
}

// TestSubagentChildRegistryKeepsSymbolsTool pins the Tier 2b integration
// note: agent.run children are built from the parent's native options, so
// they register the same `symbols` tool the parent has.
func TestSubagentChildRegistryKeepsSymbolsTool(t *testing.T) {
	cfg := config.Default()
	parentState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := registry.New()
	factory, _ := buildSubagentFactoryWithLock(cfg, parentState, nil, reg, nil, "m", nil, nil, nil, 0, pricing.ModelPricing{}, &swarm.WriteLock{}, native.Options{
		WorkspaceRoot: t.TempDir(),
		Symbols:       lsp.NewSymbolQuery(nil, nil, 0),
	}, nil, nil)
	child, _, err := factory(agent.SubagentRequest{})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if child.Registry == nil {
		t.Fatal("child.Registry should be set")
	}
	if _, ok := child.Registry.Lookup("symbols"); !ok {
		t.Fatal("child registry must retain the parent's symbols tool")
	}
}

func TestRunShutdownQuiesceBoundedToOneSecond(t *testing.T) {
	// Slow server that never responds — simulates a mid-turn quit
	// where the agent loop is blocked on an in-flight HTTP call.
	//
	// handlerDone releases the parked handler at cleanup. httptest's Close
	// waits for outstanding requests to finish, and Run deliberately returns
	// without joining workers — in the real binary they are reclaimed by
	// process exit. Inside a test the process keeps running, so the in-flight
	// request can outlive Run; without this escape hatch Close deadlocks
	// against the handler until the package test timeout fires.
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-handlerDone:
		}
	}))
	// LIFO: close(handlerDone) runs first, then server.Close() can complete.
	defer server.Close()
	defer close(handlerDone)

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

	start := time.Now()
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig(server.URL, "test-provider"), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// Quiesce should be bounded to ~1 s. Allow 2.5 s headroom for
	// process startup, DB migration, and the knowledge pass.
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("Run took %s, want quiesce bounded to ~1s", elapsed)
	}
}

func TestRunShutdownDoesNotWaitForWorkers(t *testing.T) {
	// This is a regression guard: if the worker wait block is
	// reintroduced, this test will still pass (workers are cancelled
	// by Quiesce), but it documents the intent that Run must not
	// block on workerWG at shutdown.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	start := time.Now()
	err = Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return now }),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig(server.URL, "test-provider"), nil
		}),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			state := modelState(t, model)
			state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("Run took %s, want shutdown not blocked on workers", elapsed)
	}
}

func TestStartRuntimeDefaultConfigReloader(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	rt, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return nativeToolAgentConfig("reload-default"), nil
		}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	// (a) startRuntime must self-wire a reloader when the caller supplied none.
	if rt.ConfigReloader == nil {
		t.Fatal("Runtime.ConfigReloader is nil; expected startRuntime to self-wire a default reloader")
	}

	oldRegistry := rt.ToolRegistry
	oldState := rt.State

	// (b) the default reloader must succeed for a config the build accepts.
	if err := rt.ConfigReloader(rt.Config); err != nil {
		t.Fatalf("rt.ConfigReloader(rt.Config): %v", err)
	}

	// (c) the reload swaps in a fresh tool registry pointer.
	if rt.ToolRegistry == oldRegistry {
		t.Fatal("ToolRegistry pointer did not change after reload")
	}

	// (d) the session state pointer is stable across the reload.
	if rt.State != oldState {
		t.Fatal("State pointer changed after reload")
	}
}

func TestStartRuntimePreservesExplicitConfigReloader(t *testing.T) {
	ctx := context.Background()

	called := false
	wantErr := errors.New("explicit reloader sentinel")
	explicit := func(config.Config) error {
		called = true
		return wantErr
	}

	runOpts := options{
		now:                    time.Now,
		configWithLayersLoader: config.LoadWithLayers,
		trustResolver:          &fakeTrustResolver{decision: trust.DecisionTrustPermanent},
		workingDir:             t.TempDir(),
		homeDir:                t.TempDir(),
		configReloader:         explicit,
	}

	rt, err := startRuntime(ctx, runOpts)
	if err != nil {
		t.Fatalf("startRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.ConfigReloader == nil {
		t.Fatal("Runtime.ConfigReloader is nil")
	}

	// The explicit closure must survive; the default closure would swallow the
	// sentinel error and never set called.
	if err := rt.ConfigReloader(config.Default()); !errors.Is(err, wantErr) {
		t.Fatalf("rt.ConfigReloader error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Fatal("explicit reloader was not called; startRuntime replaced it with the default")
	}
}

// TestReloadAgentRuntimeSerializesOnReloadMu pins the whole-reload gate. The
// test holds rt.reloadMu itself before starting the reload, so the blocked
// state is observable regardless of goroutine scheduling: a reload that did
// not take the gate would sail past the timeout below.
func TestReloadAgentRuntimeSerializesOnReloadMu(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	rt, err := StartRuntime(ctx, WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return nativeToolAgentConfig("reload-gate"), nil
		}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	// Hold the reload gate so the reload below cannot pass it.
	rt.reloadMu.Lock()

	done := make(chan error, 1)
	go func() {
		done <- reloadAgentRuntime(context.Background(), rt.State.Config, rt)
	}()

	select {
	case err := <-done:
		rt.reloadMu.Unlock()
		t.Fatalf("reloadAgentRuntime returned while reloadMu was held (err = %v)", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: the reload is parked on the gate.
	}

	rt.reloadMu.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reloadAgentRuntime: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reloadAgentRuntime did not complete after reloadMu was released")
	}
}

// Review focus 1: marking a session ended at exit must not stop it being
// resumed.
func TestEndedSessionStillResumes(t *testing.T) {
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)
	now := time.Unix(100, 0)
	run := func(opts ...Option) {
		base := []Option{
			WithNow(func() time.Time { return now }),
			WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
			WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
			WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
				modelState(t, model).AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
				return ProgramResult{}
			}),
		}
		if err := Run(context.Background(), bytes.NewBuffer(nil), append(base, opts...)...); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	run()
	id := fmt.Sprintf("sess_%d", now.UnixNano())
	run(WithExistingSession(id))

	database, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	msgs, err := database.GetMessages(id)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}
	var users int
	for _, m := range msgs {
		if m.Role == string(session.RoleUser) {
			users++
		}
	}
	if users != 2 {
		t.Fatalf("resumed session has %d user messages, want 2 (one per run)", users)
	}
}

// Review focus 4: a config that still binds the retired knowledge role
// starts normally.
func TestRuntimeStartsWithKnowledgeRoleBound(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
knowledge = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
}

func TestRuntimeRegistersNoRetiredTools(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	for _, name := range []string{
		"watch.start", "watch.stop", "watch.status", "watch.list",
		"sessions.list", "session.send", "ask_user", "skill.install", "skill.unload",
		"repo.map", "repo.card", "repo.index", "csv.inspect", "json.query", "file.page",
		"scratchpad.write", "scratchpad.read", "scratchpad.list", "scratchpad.delete",
	} {
		if _, ok := rt.Runner.Registry.Lookup(name); ok {
			t.Errorf("retired tool %s is still registered", name)
		}
	}
	for _, name := range []string{"skill.load", "file.read", "file.edit", "agent.await"} {
		if _, ok := rt.Runner.Registry.Lookup(name); !ok {
			t.Errorf("kept tool %s is missing", name)
		}
	}
}

// retiredToolNames are tool names (and name-shaped fragments) the model must
// never be taught again. Shared by TestNoToolDescriptionNamesARetiredTool and
// TestToolsDocumentationDrift so the two cannot drift apart.
var retiredToolNames = []string{"file.page", "repo.map", "repo.card", "repo.index", "ask_user", "skill.install", "skill.unload", "csv.inspect", "json.query", "sessions.list", "session.send", "watch.start", "agent.output", "job.output", "job.kill", "job.list", "recall_history", "scratchpad.", "parent.respond", "parent.ask", "parent.correct", "file.write_patch", "watch_id", "symbols.find", "diagnostics.check", "repo.search kind"}

// No model-facing text may teach a tool the model no longer has.
func TestNoToolDescriptionNamesARetiredTool(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	for _, tl := range rt.Runner.Registry.List() {
		if tl.Internal {
			continue
		}
		text := tl.Description + string(tl.Schema)
		for _, name := range retiredToolNames {
			if strings.Contains(text, name) {
				t.Errorf("tool %s still mentions retired tool %s", tl.Name, name)
			}
		}
	}

	// Builtin skill bodies are model-facing prompt text too: the skill index
	// and any active skill body ship in the same request as the tool list.
	// This scan is how the postmortem skill's `recall_history` and the
	// parallel-agents skill's `agent.output` were found. Handler-emitted error
	// strings cannot be scanned this way; the native tests cover those.
	skills := rt.Runner.SkillIndex.List()
	if len(skills) == 0 {
		t.Fatal("no skills loaded; the skill-body scan below would be vacuous")
	}
	for _, sk := range skills {
		text := sk.Description + "\n" + sk.Body
		for _, name := range retiredToolNames {
			if strings.Contains(text, name) {
				t.Errorf("skill %s still mentions retired tool %s", sk.Name, name)
			}
		}
	}
}

func TestNoSubagentQuestionChannel(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[profile]
default = "p"
[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "k"
[models.presets."mock/m"]
local_only = true
[agent_profiles.p]
implementer = "mock/m"
planner = "mock/m"
repo_scout = "mock/m"
tester = "mock/m"
reviewer = "mock/m"
`), 0644); err != nil {
		t.Fatal(err)
	}
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	for _, name := range []string{"parent.respond", "parent.correct", "parent.ask"} {
		if _, ok := rt.Runner.Registry.Lookup(name); ok {
			t.Errorf("%s must not be registered", name)
		}
	}
	if _, ok := rt.Runner.Registry.Lookup("agent.steer"); !ok {
		t.Error("agent.steer must be registered")
	}
}

// ── Task 5: auth-resolver threading ───────────────────────────────────

// countingAuthResolver is the isolation boundary for app-level fixtures that
// are NOT about credentials. Every entry point records the call and then
// refuses, so a test that unexpectedly reaches a credential path fails with
// the operation named rather than opening the developer's OS keychain. Its
// store/engine fields let an auth fixture script real in-memory answers.
type countingAuthResolver struct {
	mu         sync.Mutex
	storeOpens []string
	engines    []string
	store      credentials.Store
	engine     *oauth.Engine
	storeErr   error
	engineErr  error
}

var _ provider.AuthResolver = (*countingAuthResolver)(nil)

func (r *countingAuthResolver) OpenStore(service string) (credentials.Store, error) {
	r.mu.Lock()
	r.storeOpens = append(r.storeOpens, service)
	store, err := r.store, r.storeErr
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("counting auth resolver: no credential store is scripted for this fixture")
	}
	return store, nil
}

func (r *countingAuthResolver) CodexOAuthEngine(name string) (*oauth.Engine, error) {
	r.mu.Lock()
	r.engines = append(r.engines, name)
	engine, err := r.engine, r.engineErr
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, errors.New("counting auth resolver: no codex engine is scripted for this fixture")
	}
	return engine, nil
}

func (r *countingAuthResolver) openedStores() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.storeOpens...)
}

func (r *countingAuthResolver) enginesRequested() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.engines...)
}

// memoryAuthResolver builds a real resolver over a private in-memory store.
// This is the shape an auth fixture uses: the engine and token store are real
// objects with real behavior, but nothing can reach the OS keychain, and the
// resolver's own cache keeps two runtimes' tokens apart.
func memoryAuthResolver(t *testing.T) (provider.AuthResolver, *credentials.MemStore) {
	t.Helper()
	store := credentials.NewMemStore()
	resolver, err := provider.NewAuthResolver(func(service string) (credentials.Store, error) {
		return store, nil
	})
	if err != nil {
		t.Fatalf("NewAuthResolver: %v", err)
	}
	return resolver, store
}

// codexOAuthProviderConfig is a single openai_codex provider whose refresh
// worker and provider both draw from the runtime's selected resolver.
func codexOAuthProviderConfig(name, baseURL string) config.Config {
	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		name: {
			Type:    "openai_codex",
			BaseURL: baseURL,
			Auth:    "oauth",
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {
			Name:      "coder",
			Provider:  name,
			Model:     "gpt-5.6-luna",
			LocalOnly: false,
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

// TestWithAuthResolverRejectsExplicitNil pins the omitted-vs-injected
// distinction: an omitted option selects the production default, an explicitly
// nil resolver is a caller bug and must fail rather than silently pointing an
// isolated runtime at the real keychain.
func TestWithAuthResolverRejectsExplicitNil(t *testing.T) {
	// Omitted: the production adapter. It is not exercised here — only its
	// type is asserted, so no keychain is opened.
	omitted := options{}
	got, err := resolveAuthResolver(omitted)
	if err != nil {
		t.Fatalf("resolveAuthResolver with no option: %v", err)
	}
	if got == nil {
		t.Fatal("an omitted resolver must select the production default, not nil")
	}
	if got != provider.AuthResolver(provider.DefaultAuthResolver()) {
		t.Fatalf("omitted resolver = %T, want provider.DefaultAuthResolver()", got)
	}

	// Explicitly nil injection: rejected.
	explicit := options{}
	WithAuthResolver(nil)(&explicit)
	if _, err := resolveAuthResolver(explicit); !errors.Is(err, ErrAuthResolverRequired) {
		t.Fatalf("resolveAuthResolver with an explicit nil = %v, want ErrAuthResolverRequired", err)
	}

	// StartRuntime must surface the same rejection.
	if _, err := StartRuntime(context.Background(), WithWorkingDir(t.TempDir()), WithHomeDir(t.TempDir()), WithAuthResolver(nil)); !errors.Is(err, ErrAuthResolverRequired) {
		t.Fatalf("StartRuntime with an explicit nil resolver = %v, want ErrAuthResolverRequired", err)
	}

	// An injected resolver is carried through unchanged.
	injected := &countingAuthResolver{}
	opts := options{}
	WithAuthResolver(injected)(&opts)
	got, err = resolveAuthResolver(opts)
	if err != nil {
		t.Fatalf("resolveAuthResolver with an injected resolver: %v", err)
	}
	if got != provider.AuthResolver(injected) {
		t.Fatalf("injected resolver = %T, want the injected value", got)
	}
}

// TestRunPassesAuthResolverToTUI closes the one fail-open seam in the
// isolation design. The app builders refuse an omitted resolver, but the
// TUI's own constructors substitute provider.DefaultAuthResolver() when no
// option supplies one — so app.Run's WithAuthResolver wiring is the single
// place where an isolated runtime could silently regain the real OS keychain,
// and nothing else in the suite would notice if it were dropped.
func TestRunPassesAuthResolverToTUI(t *testing.T) {
	auth := &countingAuthResolver{}
	var got provider.AuthResolver
	err := Run(context.Background(), bytes.NewBuffer(nil),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithAuthResolver(auth),
		WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
			got = model.(tui.Model).AuthResolver()
			return ProgramResult{}
		}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got == nil {
		t.Fatal("the TUI model carries no auth resolver")
	}
	if got != provider.AuthResolver(auth) {
		t.Fatalf("TUI auth resolver = %T, want the runtime's injected resolver", got)
	}
	// The wiring must not itself reach a credential path: this fixture
	// configures no provider, so a correct startup asks for neither a store
	// nor an engine.
	if opens := auth.openedStores(); len(opens) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none for a provider-less startup", opens)
	}
	if engines := auth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want none for a provider-less startup", engines)
	}
}

// TestStartRuntimeRetainsAuthResolverWithoutProvider covers the
// inactive-provider startup path: a project with no configured provider still
// gets a Runtime carrying the selected resolver, so a later /connect or config
// reload cannot fall back to the production default.
func TestStartRuntimeRetainsAuthResolverWithoutProvider(t *testing.T) {
	auth := &countingAuthResolver{}
	rt, err := StartRuntime(context.Background(),
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		}),
		WithAuthResolver(auth),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.Runner != nil {
		t.Fatal("no provider is configured; the runner should not have been built")
	}
	if rt.AuthResolver == nil {
		t.Fatal("Runtime.AuthResolver is nil after an injected startup with no provider")
	}
	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("Runtime.AuthResolver is not the injected resolver")
	}
	if opens := auth.openedStores(); len(opens) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none for a provider-less startup", opens)
	}
	if engines := auth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want none for a provider-less startup", engines)
	}
}

// TestRuntimeCarriesAuthResolverThroughReloadNewAndResume walks the whole
// lifecycle with a resolver that fails every entry point: startup, config
// reload, /session new and a resumed session must all keep the injected
// resolver, and none of them may reach a credential path this fixture did not
// script. The provider here is a plain openai_compatible endpoint, so a
// correctly isolated runtime never asks for an engine or a store at all.
func TestRuntimeCarriesAuthResolverThroughReloadNewAndResume(t *testing.T) {
	ctx := context.Background()
	auth := &countingAuthResolver{}
	home := t.TempDir()

	initCfg := reloadableAgentConfig("lifecycle-provider")
	reloadCfg := reloadableAgentConfig("lifecycle-provider")
	reloadCfg.Privacy.RemoteProvidersAllowed = true

	work := t.TempDir()
	rt, err := StartRuntime(ctx,
		WithWorkingDir(work),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return initCfg, nil }),
		WithAuthResolver(auth),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)

	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("startup did not retain the injected resolver")
	}

	if err := reloadAgentRuntime(ctx, reloadCfg, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("config reload replaced the injected resolver")
	}

	if _, _, _, _, _, _, _, err := rt.NewSession("auth-lifecycle"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("NewSession replaced the injected resolver")
	}
	newSessionID := rt.SessionID

	// Resumed session: a fresh runtime in existing-session mode gets the same
	// resolver when it is injected again.
	resumed, err := StartRuntime(ctx,
		WithWorkingDir(work),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return reloadCfg, nil }),
		WithExistingSession(newSessionID),
		WithAuthResolver(auth),
	)
	if err != nil {
		t.Fatalf("StartRuntime (resumed): %v", err)
	}
	defer resumed.Close(ctx)
	if resumed.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("a resumed session did not retain the injected resolver")
	}
	if resumed.SessionID != newSessionID {
		t.Fatalf("resumed session id = %q, want %q", resumed.SessionID, newSessionID)
	}

	// Nothing above declared an OAuth provider or an OAuth MCP server, so a
	// correctly threaded runtime must never have asked this resolver for a
	// credential.
	if opens := auth.openedStores(); len(opens) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none for non-credential fixtures", opens)
	}
	if engines := auth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want none for non-credential fixtures", engines)
	}
}

// TestFailedRebuildKeepsAuthResolver pins the failure path: a reload whose
// dry-run build fails must leave the runtime's resolver (and its provider
// wiring) untouched, so the recovery path through /connect cannot inherit a
// different credential backend.
func TestFailedRebuildKeepsAuthResolver(t *testing.T) {
	ctx := context.Background()
	auth := &countingAuthResolver{}

	initial := reloadableAgentConfig("good-provider")
	state := session.New(initial, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, pipelineFactory, _, _, err := buildAgentRunner(ctx, initial, state, nil, 0, nil, "", nil, nil, nil, "", auth)
	if err != nil {
		t.Fatalf("buildAgentRunner initial: %v", err)
	}

	rt := &Runtime{
		Runner:          runner,
		ToolRegistry:    reg,
		SwarmRunner:     swarmRunner,
		PipelineFactory: pipelineFactory,
		JobManager:      jobMgr,
		State:           state,
		workCtx:         ctx,
		AuthResolver:    auth,
	}
	oldRunner := rt.Runner

	// A config that cannot build: the preset names a provider that is not
	// configured, so Resolve fails before any credential is touched.
	bad := config.Default()
	bad.Privacy.RemoteProvidersAllowed = true
	bad.Profile.Default = "p"
	bad.Models.Presets = map[string]routing.ModelPreset{
		"missing/m": {Provider: "missing", Model: "m", LocalOnly: true},
	}
	bad.AgentProfiles = map[string]routing.AgentProfile{
		"p": {Name: "p", Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "missing/m"},
		}},
	}

	if err := reloadAgentRuntime(ctx, bad, rt); err == nil {
		t.Fatal("expected the reload to fail")
	}
	if rt.Runner != oldRunner {
		t.Fatal("a failed reload replaced the runner")
	}
	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("a failed reload replaced the auth resolver")
	}
	if opens := auth.openedStores(); len(opens) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none", opens)
	}
	if engines := auth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want none", engines)
	}
}

// TestInactiveRouteRecoveryKeepsAuthResolver covers the recovery path: a
// runtime that started with no usable provider (inactive route) adopts a
// rebuilt runner on reload, and that rebuild must use the runtime's injected
// resolver rather than the production default.
func TestInactiveRouteRecoveryKeepsAuthResolver(t *testing.T) {
	ctx := context.Background()
	auth := &countingAuthResolver{}
	home := t.TempDir()
	work := t.TempDir()

	// Startup with an empty config: no provider, inactive route.
	rt, err := StartRuntime(ctx,
		WithWorkingDir(work),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
		WithAuthResolver(auth),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)
	if rt.Runner != nil {
		t.Fatal("test premise broken: startup built a runner without a provider")
	}

	// Recover with a configured (non-OAuth) provider.
	if err := reloadAgentRuntime(ctx, reloadableAgentConfig("recovered-provider"), rt); err != nil {
		t.Fatalf("recovery reload: %v", err)
	}
	if rt.Runner == nil {
		t.Fatal("recovery reload did not adopt a runner")
	}
	if rt.AuthResolver != provider.AuthResolver(auth) {
		t.Fatal("recovery reload replaced the injected resolver")
	}

	// The recovered provider is openai_compatible: no credential was needed.
	if opens := auth.openedStores(); len(opens) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none", opens)
	}
	if engines := auth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want none", engines)
	}
}

// TestParallelRuntimesKeepTokensPrivate is the two-runtimes regression: two
// runtimes in the same process configuring the SAME provider name must not
// observe each other's tokens or engines. Each runtime gets its own resolver
// over its own in-memory store, and each provider is asked (through the
// engine the resolver handed it) for the token only that runtime stored.
func TestParallelRuntimesKeepTokensPrivate(t *testing.T) {
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The catalog endpoint is never reached in this fixture: the engine
		// fails its read first. Answer defensively anyway.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	const providerName = "shared-name-codex"

	buildRuntime := func(t *testing.T, token string) (*Runtime, *credentials.MemStore) {
		t.Helper()
		resolver, store := memoryAuthResolver(t)
		seedCodexToken(t, store, providerName, token)
		cfg := codexOAuthProviderConfig(providerName, server.URL)
		rt, err := StartRuntime(ctx,
			WithWorkingDir(t.TempDir()),
			WithHomeDir(t.TempDir()),
			WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
			WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
			WithAuthResolver(resolver),
		)
		if err != nil {
			t.Fatalf("StartRuntime: %v", err)
		}
		return rt, store
	}

	rtA, storeA := buildRuntime(t, "token-runtime-a")
	defer rtA.Close(ctx)
	rtB, storeB := buildRuntime(t, "token-runtime-b")
	defer rtB.Close(ctx)

	if storeA == storeB {
		t.Fatal("test premise broken: both runtimes share one store")
	}
	if rtA.AuthResolver == rtB.AuthResolver {
		t.Fatal("two runtimes with the same provider name share one resolver")
	}

	// Each runtime's provider must be built over its own runtime's resolver:
	// reading through the engine that resolver caches yields only that
	// runtime's token.
	assertRuntimeToken(t, rtA, providerName, "token-runtime-a")
	assertRuntimeToken(t, rtB, providerName, "token-runtime-b")

	// And the synthetic tokens must never have been written to the other
	// runtime's store.
	if raw, err := storeB.Get("marshal:provider:" + providerName); err != nil {
		t.Fatalf("runtime B store read: %v", err)
	} else if strings.Contains(string(raw), "token-runtime-a") {
		t.Fatal("runtime A's token appeared in runtime B's store")
	}
	if raw, err := storeA.Get("marshal:provider:" + providerName); err != nil {
		t.Fatalf("runtime A store read: %v", err)
	} else if strings.Contains(string(raw), "token-runtime-b") {
		t.Fatal("runtime B's token appeared in runtime A's store")
	}

	// The provider's engine and the worker's engine must be the same object:
	// that identity is what serializes an inline refresh against a proactive
	// one on a single mutex.
	assertProviderWorkerShareEngine(t, rtA, providerName)
}

// assertRuntimeToken reads the token the runtime's own resolver hands out for
// provider name and asserts it equals want. It goes through the resolver, not
// the provider, so the assertion covers the dependency the runtime actually
// selected (the provider holds the very engine the resolver caches).
func assertRuntimeToken(t *testing.T, rt *Runtime, name, want string) {
	t.Helper()
	engine, err := rt.AuthResolver.CodexOAuthEngine(name)
	if err != nil {
		t.Fatalf("resolver CodexOAuthEngine(%q): %v", name, err)
	}
	got, err := engine.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("TokenSource(%q): %v", name, err)
	}
	if got != want {
		t.Fatalf("runtime token = %q, want %q", got, want)
	}
}

// assertProviderWorkerShareEngine proves the provider and its refresh worker
// hold the exact same *oauth.Engine. Only one worker is built per OAuth
// provider, so identity is the whole assertion: two engines would mean two
// mutexes and two racing refreshes.
func assertProviderWorkerShareEngine(t *testing.T, rt *Runtime, name string) {
	t.Helper()
	workers := buildOAuthWorkers(rt.Config, rt.Logger, rt.AuthResolver)
	var worker *oauthworker.Worker
	for _, w := range workers {
		if ow, ok := w.(*oauthworker.Worker); ok && ow.Provider == name {
			worker = ow
			break
		}
	}
	if worker == nil {
		t.Fatalf("no refresh worker was built for provider %q (workers=%d)", name, len(workers))
	}

	// The provider's engine, reached through the resolver the runtime used.
	engine, err := rt.AuthResolver.CodexOAuthEngine(name)
	if err != nil {
		t.Fatalf("resolver CodexOAuthEngine(%q): %v", name, err)
	}
	if worker.Engine != engine {
		t.Fatal("the provider and its refresh worker do not share one engine")
	}

	// And the provider built at startup holds that same engine: compare the
	// token the provider's own engine yields with the worker's.
	token, err := worker.Engine.TokenSource(context.Background())
	if err != nil {
		t.Fatalf("worker engine TokenSource: %v", err)
	}
	if token == "" {
		t.Fatal("the shared engine yielded an empty token")
	}
}

// seedCodexToken writes a valid, unexpired synthetic token set into store
// under the provider-scoped key the codex engine uses.
func seedCodexToken(t *testing.T, store credentials.Store, name, accessToken string) {
	t.Helper()
	raw, err := json.Marshal(oauth.StoredTokens{
		AccessToken:  accessToken,
		RefreshToken: "refresh-" + accessToken,
		ExpiresAt:    time.Now().Add(time.Hour),
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal stored tokens: %v", err)
	}
	if err := store.Set("marshal:provider:"+name, raw); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
}

// TestAgentRunnerPassesRuntimeOpenerToMCPManager pins step 4's MCP wiring: the
// manager the runtime builds must reach its OAuth token store through the
// runtime's selected resolver, not through the production credentials.New.
//
// A closed loopback endpoint keeps the fixture hermetic: startup opens the
// store (which the counting resolver records and then refuses to satisfy
// beyond its own error) before it can reach the network, so the assertion does
// not depend on any listening service.
func TestAgentRunnerPassesRuntimeOpenerToMCPManager(t *testing.T) {
	ctx := context.Background()

	storeErr := errors.New("injected store failure")
	auth := &countingAuthResolver{storeErr: storeErr}

	cfg := config.Default()
	cfg.Privacy.RemoteProvidersAllowed = true
	cfg.Providers = map[string]config.ProviderConfig{
		"mcp-fixture-provider": {
			Type:    "openai_compatible",
			BaseURL: "http://localhost:11434/v1",
			APIKey:  "test-key",
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "mcp-fixture-provider", Model: "m", LocalOnly: true},
	}
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		cfg.Profile.Default: {Name: cfg.Profile.Default, Roles: map[routing.AgentRole]routing.RoleBinding{
			routing.RoleImplementer: {Preset: "coder"},
		}},
	}
	cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"oauth-remote": {
			URL:   "https://127.0.0.1:9/mcp",
			Type:  "http",
			Auth:  "oauth",
			Trust: "unrestricted",
		},
	}

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_, _, _, mcpMgr, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "", auth)
	if err != nil {
		t.Fatalf("an OAuth MCP store failure must be a per-server failure, not a build failure: %v", err)
	}
	if mcpMgr != nil {
		defer func() { _ = mcpMgr.Close() }()
	}
	// The failure must be surfaced: the runtime keeps running with a warning
	// notice naming the server, exactly as it does for any unstartable MCP
	// entry.
	n, ok := state.Notice()
	if !ok {
		t.Fatal("the MCP failure was not surfaced as a session notice")
	}
	if !strings.Contains(n.Message, "oauth-remote") {
		t.Fatalf("notice = %q, want it to name the failing server", n.Message)
	}

	// The store was requested through THIS resolver, exactly once, and the
	// injected error was not retried against any other backend.
	opens := auth.openedStores()
	if len(opens) != 1 {
		t.Fatalf("resolver OpenStore calls = %v, want exactly 1 for the one OAuth server", opens)
	}
	if opens[0] != "marshal" {
		t.Fatalf("resolver OpenStore service = %q, want %q", opens[0], "marshal")
	}
}

// ---- system-access approval: production TUI wiring ----

// accessTUIRequest publishes a typed request on state and returns it with the
// canonical decision channel.
func accessTUIRequest(t *testing.T, state *session.State, intent session.AccessIntent) (session.AccessRequest, chan session.UserApprovalDecision) {
	t.Helper()
	call := &session.PendingToolCall{
		ID:           "call-1",
		Name:         "mode.request",
		Reason:       intent.Reason,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	req, waiting, err := state.RegisterAccess(context.Background(), call, intent)
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	if !waiting {
		t.Fatalf("RegisterAccess published no request for intent %+v", intent)
	}
	return req, call.ResponseChan
}

// accessTUIChoice builds the exported message the access panel emits.
func accessTUIChoice(req session.AccessRequest, approved bool, mode string) accesspanel.ChoiceMsg {
	return accesspanel.ChoiceMsg{Choice: session.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Current:   req.Current,
		Approved:  approved,
		Mode:      mode,
	}}
}

// TestAccessTUIOptionsResolveWithoutConfigReload pins the production wiring:
// the options accessTUIOptions hands the TUI resolve a typed request through
// the runtime, in memory, without touching the config reloader.
func TestAccessTUIOptionsResolveWithoutConfigReload(t *testing.T) {
	cfg := nativeToolAgentConfig("access-tui-options")
	rt, state := newAccessTestRuntime(t, cfg)

	var reloads atomic.Int64
	rt.ConfigReloader = func(config.Config) error {
		reloads.Add(1)
		return nil
	}

	m := tui.New(state, accessTUIOptions(rt)...)

	// The size gate needs a real frame first, exactly as the production
	// program gets one at startup.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(tui.Model)

	req, decisions := accessTUIRequest(t, state, session.AccessIntent{
		System: true, Reason: "The agent requests system access.",
	})

	// The request must surface on a runtime update that is neither a keypress
	// nor a resize; a focus change stands in for the tick that drives it in
	// production. A regression back to needing a keypress fails here.
	updated, _ = m.Update(tea.FocusMsg{})
	m = updated.(tui.Model)

	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Allow system access?") {
		t.Fatalf("view does not show the system-access panel:\n%s", view)
	}

	updated, _ = m.Update(accessTUIChoice(req, true, ""))
	m = updated.(tui.Model)

	select {
	case d := <-decisions:
		if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
			t.Fatalf("delivered decision = %+v, want an approved typed grant", d)
		}
		if want := (session.AccessState{Mode: "default", System: true}); d.Access.Effective != want {
			t.Fatalf("effective = %+v, want %+v", d.Access.Effective, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request was never settled")
	}
	if !state.SystemAccess() {
		t.Fatal("the grant did not reach the session")
	}
	if n := reloads.Load(); n != 0 {
		t.Fatalf("the grant triggered %d config reloads, want 0", n)
	}
}

// TestAccessTUIOptionsFollowNewSession pins that the same options keep working
// after a session swap: the bound methods read the runtime's current state, so
// a decision for the retired conversation cannot reach the replacement.
func TestAccessTUIOptionsFollowNewSession(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("access-tui-follow")
	// The default autoload injects a skill body into a fresh session; opt out
	// so the fixture stays about the access seam.
	cfg.Skills.Autoload = nil

	rt, err := StartRuntime(ctx,
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)
	if rt.Runner == nil {
		t.Fatal("the fixture provider was not built")
	}

	// The options are captured once, before the swap: production builds them
	// once and keeps them for the life of the program.
	opts := accessTUIOptions(rt)

	oldState := rt.State
	oldReq, oldDecisions := accessTUIRequest(t, oldState, session.AccessIntent{
		System: true, Reason: "The agent requests system access.",
	})
	oldModel := tui.New(oldState, opts...)
	updated, _ := oldModel.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	oldModel = updated.(tui.Model)
	if view := ansi.Strip(oldModel.View().Content); !strings.Contains(view, "Allow system access?") {
		t.Fatalf("the pre-swap model does not show the panel:\n%s", view)
	}

	newState, _, _, _, _, _, _, err := rt.NewSession("")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if newState == oldState {
		t.Fatal("NewSession did not replace the session")
	}

	// The retired conversation's request is settled exactly once, as a
	// cancellation, so its tool cannot block on a session that is gone. The
	// channel is then closed: a further receive yields the zero value with
	// ok=false, which is what distinguishes "settled once" from a second
	// settlement.
	select {
	case d := <-oldDecisions:
		if d.Access == nil || d.Access.Status != session.AccessCanceled {
			t.Fatalf("pre-swap decision = %+v, want a typed cancellation", d)
		}
		if d.Approved {
			t.Fatal("the retired request was acknowledged as approved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pre-swap request was never settled")
	}
	if _, ok := <-oldDecisions; ok {
		t.Fatal("the retired request's channel is still open; it must be closed after one settlement")
	}

	// The same options now drive the replacement session.
	newModel := tui.New(newState, opts...)
	newReq, newDecisions := accessTUIRequest(t, newState, session.AccessIntent{
		System: true, Reason: "The agent requests system access.",
	})
	updated, _ = newModel.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	newModel = updated.(tui.Model)
	if view := ansi.Strip(newModel.View().Content); !strings.Contains(view, "Allow system access?") {
		t.Fatalf("the replacement model does not show the panel:\n%s", view)
	}

	// A decision for the retired conversation must not reach the replacement.
	updated, _ = newModel.Update(accessTUIChoice(oldReq, true, ""))
	newModel = updated.(tui.Model)
	if newState.SystemAccess() {
		t.Fatal("a retired choice granted access on the replacement session")
	}
	if _, pending := newState.AccessRequest(); !pending {
		t.Fatal("a retired choice settled the replacement's request")
	}

	// The replacement's own request still resolves.
	updated, _ = newModel.Update(accessTUIChoice(newReq, true, ""))
	newModel = updated.(tui.Model)
	select {
	case d := <-newDecisions:
		if !d.Approved || d.Access == nil || d.Access.Status != session.AccessGranted {
			t.Fatalf("replacement decision = %+v, want an approved typed grant", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement request was never settled")
	}
	if !newState.SystemAccess() {
		t.Fatal("the replacement grant did not reach the new session")
	}
}

// facadeTurnRunner stands in for the agent process in the Send now facade
// test. It opens the conversation's one eligible turn exactly as RunTask does,
// reports the turn ID it opened, and then blocks until its context is
// cancelled — keeping the turn busy and steerable for the whole frame without a
// provider call or a real child process. It satisfies tui.AgentRunner.
type facadeTurnRunner struct {
	state   *session.State
	started chan string
}

func (r *facadeTurnRunner) Run(ctx context.Context, goal string) error {
	turnID, err := r.state.BeginSteeringTurn(ctx)
	if err != nil {
		return err
	}
	select {
	case r.started <- turnID:
	default:
	}
	<-ctx.Done()
	r.state.EndSteeringTurn(turnID, "turn complete")
	return ctx.Err()
}

func (r *facadeTurnRunner) SetForceClass(string)                   {}
func (r *facadeTurnRunner) SetPolicyRules([]config.PermissionRule) {}
func (r *facadeTurnRunner) SetApprovalMode(policy.ApprovalMode)    {}
func (r *facadeTurnRunner) AnswerGate(string)                      {}

// TestRuntimeSendNowTUIOptionsExerciseRealFacade drives the production TUI
// facade end to end: the runtime's own snapshot and submit callbacks are wired
// into a real tui.Model, and the public Send now key is pressed during a live
// turn. The accepted receipt is observable in the session queue, and the model
// delivers it back as the accepted state on the status line — with no reload,
// provider call or real process.
//
// The composer variant of the same key (a panel owning the input) is covered by
// the tui package's own Send now tests: the composer's identity is not exposed
// outside that package, so it cannot be addressed from here.
func TestRuntimeSendNowTUIOptionsExerciseRealFacade(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()

	cfg := nativeToolAgentConfig("send-now-tui-facade")
	cfg.Skills.Autoload = nil
	auth, _ := memoryAuthResolver(t)
	rt, err := StartRuntime(runCtx,
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
		WithAuthResolver(auth),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	state := rt.State
	agentProcess := &facadeTurnRunner{state: state, started: make(chan string, 1)}

	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	model := tui.New(state,
		tui.WithCommandRegistry(reg),
		tui.WithRunner(runCtx, agentProcess),
		tui.WithSendNow(rt.SteeringSnapshot, rt.SubmitSendNow),
	)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(tui.Model)

	// Start the turn through the public prompt path. The fake process blocks, so
	// the turn stays in flight while the frame is exercised.
	updated, _ = model.Update(tea.PasteMsg{Content: "do the thing"})
	model = updated.(tui.Model)
	updated, runCmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(tui.Model)
	if runCmd == nil {
		t.Fatal("the prompt produced no command; the turn never started")
	}
	// A Bubble Tea batch returns its sub-commands without running them; execute
	// the agent run (the first) in a goroutine so it blocks for the whole frame.
	batch, ok := runCmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("the prompt command returned %T, want a tea.BatchMsg", runCmd())
	}
	if len(batch) == 0 {
		t.Fatal("the prompt batch carried no commands")
	}
	go func() { _ = batch[0]() }()

	var turnID string
	select {
	case turnID = <-agentProcess.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the facade turn never started")
	}

	// Type steering text and press the public Send now key.
	updated, _ = model.Update(tea.PasteMsg{Content: "steer this"})
	model = updated.(tui.Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	model = updated.(tui.Model)

	// Accepted by the runtime against the live turn.
	if q := state.SteeringQueue(); len(q) != 1 || q[0] != "steer this" {
		t.Fatalf("queue = %v, want the one accepted steering entry on turn %s", q, turnID)
	}
	// Delivered back to the model: the cached receipt and the refreshed runtime
	// snapshot drive the status line.
	if frame := ansi.Strip(model.View().Content); !strings.Contains(frame, "send now: accepted") {
		t.Fatalf("the model did not render the accepted receipt:\n%s", frame)
	}
}

// ── Task 10: the TUI's end adapter ──────────────────────────────────────

// startEndRuntime builds a runtime with a resolvable provider, so the runner
// exists and the exit-analysis pass has something to run on. The provider is
// never called: these tests inspect elevation and membership, not model output.
func startEndRuntime(t *testing.T) *Runtime {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[project]
name = "end-controller-test"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rt, err := StartRuntime(context.Background(),
		WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

// TestTUIEndControllerAdapterRoundTrips pins the structural seam between the TUI
// and the runtime: the adapter converts the runtime's shapes into the neutral
// mirrors the model uses, and every decision still lands on the runtime.
func TestTUIEndControllerAdapterRoundTrips(t *testing.T) {
	rt := startEndRuntime(t)
	ctrl := &tuiEndController{rt: rt}

	if got := ctrl.ActiveWork(); len(got.Items) != 0 {
		t.Fatalf("ActiveWork() = %+v, want an empty membership on a fresh runtime", got)
	}

	// Register real conversation work, so the adapter reports a real membership
	// rather than a synthesised one.
	_, finish, err := rt.BeginWork(context.Background())
	if err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	defer finish()

	work := ctrl.ActiveWork()
	if len(work.Items) != 1 {
		t.Fatalf("ActiveWork() items = %d, want the registered operation", len(work.Items))
	}
	if work.Items[0].ID == "" || !work.Items[0].Parent {
		t.Fatalf("work item = %+v, want a parent operation with an opaque ID", work.Items[0])
	}
	if work.Phase != rt.EndPhase() {
		t.Fatalf("phase = %q, want the runtime's own phase %q", work.Phase, rt.EndPhase())
	}
	if ctrl.EndAccepted() {
		t.Fatal("EndAccepted() = true before any acceptance")
	}

	// An acceptance requiring a displayed set must refuse when the membership
	// was not displayed — and the adapter must relay the refusal faithfully.
	refused, err := ctrl.AcceptEnd(tui.EndAcceptRequest{
		Intent:           tui.EndIntentExit,
		DisplayedIDs:     []string{"something-else"},
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if refused.Accepted {
		t.Fatal("the adapter reported an acceptance of an undisplayed membership")
	}
	if len(refused.Expanded) != 1 || refused.Expanded[0] != work.Items[0].ID {
		t.Fatalf("Expanded = %v, want the undisplayed ID %q", refused.Expanded, work.Items[0].ID)
	}
	if rt.EndAccepted() {
		t.Fatal("a refused acceptance froze the runtime")
	}

	// The real acceptance succeeds and reports the membership it stopped.
	accepted, err := ctrl.AcceptEnd(tui.EndAcceptRequest{
		Intent:           tui.EndIntentExit,
		DisplayedIDs:     []string{work.Items[0].ID},
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !accepted.Accepted {
		t.Fatalf("accepted = false, want true (expanded: %v)", accepted.Expanded)
	}
	if len(accepted.Work.Items) != 1 {
		t.Fatalf("accepted membership = %d items, want 1", len(accepted.Work.Items))
	}
	if !ctrl.EndAccepted() {
		t.Fatal("EndAccepted() = false after an acceptance")
	}
}

// TestExitAnalysisElevationIsRestored pins the app-side ownership of the exit
// analysis' temporary elevation: it is in place while the pass runs and is
// undone whatever the outcome, so a failed pass can never leave an ordinary
// prompt elevated.
func TestExitAnalysisElevationIsRestored(t *testing.T) {
	rt := startEndRuntime(t)
	if rt.Runner == nil {
		t.Fatal("rt.Runner = nil")
	}

	// Seed a read-only posture: the pass must not have it, and must get it back.
	rt.State.SetSystemAccess(false)
	rt.Runner.SetApprovalMode(policy.ModePlan)
	rt.Config.Agent.ApprovalMode = string(policy.ModePlan)

	seenSystem, seenMode := false, policy.ModePlan
	err := runWithExitAnalysisElevation(rt, func() error {
		seenSystem = rt.State.SystemAccess()
		if rt.Runner.Policy != nil {
			seenMode = rt.Runner.Policy.ApprovalMode()
		}
		return errors.New("the pass failed")
	})
	if err == nil {
		t.Fatal("runWithExitAnalysisElevation swallowed the pass error")
	}
	if !seenSystem {
		t.Fatal("the pass ran WITHOUT system access; the elevation was not applied")
	}
	if seenMode != policy.ModeEdit {
		t.Fatalf("the pass ran in mode %q, want edit so its write is not mode-denied", seenMode)
	}
	if rt.State.SystemAccess() {
		t.Fatal("a failed pass left system access granted")
	}
	if rt.Runner.Policy != nil && rt.Runner.Policy.ApprovalMode() != policy.ModePlan {
		t.Fatalf("a failed pass left the mode at %q, want the prior plan", rt.Runner.Policy.ApprovalMode())
	}
}

// TestTUIEndControllerIsTheOnlyEndSeam pins that app.Run wires the adapter, so
// the TUI never decides policy or reaches the runtime directly. It runs the
// program runner just far enough to inspect the model's end seam.
func TestTUIEndControllerIsTheOnlyEndSeam(t *testing.T) {
	tmp := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWd)

	programRunner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		value := reflect.ValueOf(model)
		field := value.FieldByName("endController")
		if !field.IsValid() {
			t.Fatal("the model has no endController field")
		}
		if field.IsNil() {
			t.Fatal("app.Run did not wire the TUI's end controller")
		}
		// The field is an unexported interface, so Interface() is unavailable.
		// Elem() reads the value inside it without that restriction.
		if got, want := field.Elem().Type(), reflect.TypeOf(&tuiEndController{}); got != want {
			t.Fatalf("endController = %v, want the runtime adapter %v", got, want)
		}
		return ProgramResult{}
	}

	if err := Run(context.Background(), bytes.NewBuffer(nil),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithProgramRunner(programRunner),
	); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// ── Task 12: the composed lifecycle through the real runtime ─────────────

// startLifecycleRuntime starts a runtime whose conversation owns a working
// command service, using only the production seams (a project config, an
// explicit home dir and a trust resolver) so no ambient user configuration is
// consulted.
func startLifecycleRuntime(t *testing.T) *Runtime {
	t.Helper()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".marshal", "config.toml"), []byte(`[project]
name = "lifecycle-runtime"

[profile]
default = "mock_profile"

[providers.mock]
type = "openai_compatible"
base_url = "http://localhost:11434/v1"
api_key = "mock-key"
tool_calling = true

[models.presets."mock/mock-model"]
local_only = true

[agent_profiles.mock_profile]
implementer = "mock/mock-model"
planner = "mock/mock-model"
repo_scout = "mock/mock-model"
tester = "mock/mock-model"
reviewer = "mock/mock-model"
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rt, err := StartRuntime(context.Background(),
		WithWorkingDir(tmp),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	if rt.JobManager == nil {
		t.Fatal("the runtime owns no command service, so the lifecycle assertions would be vacuous")
	}
	return rt
}

// TestRuntimeCommandLifecycleSurvivesReloadAndSettlesOnce is Task 12's
// app-level composition regression: a command admitted through the runtime's own
// service is awaited repeatedly, survives a real config reload with its identity
// and membership intact, and then finishes naturally — one launch, one process
// identity, one durable completion the parent transcript can show.
func TestRuntimeCommandLifecycleSurvivesReloadAndSettlesOnce(t *testing.T) {
	ctx := context.Background()
	rt := startLifecycleRuntime(t)
	service := rt.JobManager

	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	runner := &fakeRunner{run: func(runCtx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		if req.OnStart != nil {
			req.OnStart(7100)
		}
		if req.Stdout != nil {
			_, _ = req.Stdout.Write([]byte("lifecycle-progress\n"))
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
			// A real process streams its last line before exiting; the fake does
			// the same so the completion carries the command's final output.
			if req.Stdout != nil {
				_, _ = req.Stdout.Write([]byte("lifecycle-final\n"))
			}
			return native.CommandResult{Stdout: "lifecycle-final\n", ExitCode: 0}, nil
		case <-runCtx.Done():
			return native.CommandResult{}, runCtx.Err()
		}
	}}
	// The committed policy's runner is the one future admissions use; the
	// command below is admitted after this commit, exactly as a command admitted
	// during a turn is.
	policy := service.CurrentLaunchPolicy()
	policy.Runner = runner
	service.CommitLaunchPolicy(policy)

	info, err := service.Launch(ctx, native.LaunchSpec{
		Command:  "long lifecycle command",
		Detached: true,
		Origin: native.CommandOrigin{
			ConversationID: rt.SessionID,
			Actor:          "parent",
			TurnID:         "turn-lifecycle",
		},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the command never started")
	}

	// Repeated awaits: each ends the caller's wait and leaves the command alone.
	for i := 0; i < 3; i++ {
		res, err := service.WaitForeground(ctx, info.ID, 10*time.Millisecond)
		if err != nil {
			t.Fatalf("await %d: %v", i, err)
		}
		if res.Terminal {
			t.Fatalf("await %d = %+v, want a continuation", i, res)
		}
		if res.Info.PID != 7100 || res.Info.ExecutionID != info.ExecutionID {
			t.Fatalf("await %d identity = pid %d/%q, want 7100/%q", i, res.Info.PID, res.Info.ExecutionID, info.ExecutionID)
		}
	}

	// A real reload through the runtime's own entry point. The reload must not
	// touch the running command, its handle or its identity.
	if err := reloadAgentRuntime(ctx, rt.Config, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}
	if rt.JobManager != service {
		t.Fatal("a reload replaced the service that owns the running command")
	}
	live, _, err := service.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output after the reload: %v", err)
	}
	if live.Status != native.StatusRunning || live.PID != 7100 {
		t.Fatalf("live after the reload = %q/pid %d, want running/7100", live.Status, live.PID)
	}
	if live.StopCause != "" {
		t.Fatalf("StopCause after the reload = %q, want none", live.StopCause)
	}
	// The user-visible work membership still names the command, so an end would
	// still warn about it.
	var sawCommand bool
	for _, item := range rt.ActiveWork().Items {
		if item.Label == "long lifecycle command" {
			sawCommand = true
		}
	}
	if !sawCommand {
		t.Fatalf("membership after the reload = %+v, want the running command", rt.ActiveWork().Items)
	}

	// Natural completion: the transcript gains exactly one completion for this
	// execution, with the command's own exit code and its final output.
	close(release)
	settled, err := service.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.Status != native.StatusCompleted || settled.ExitCode == nil || *settled.ExitCode != 0 {
		t.Fatalf("settled = %q/%v, want completed with exit 0", settled.Status, settled.ExitCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	var exits []session.JobExit
	for {
		exits = nil
		for _, item := range rt.State.Transcript() {
			if item.JobExit != nil && item.JobExit.ExecutionID == info.ExecutionID {
				exits = append(exits, *item.JobExit)
			}
		}
		if len(exits) == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(exits) != 1 {
		t.Fatalf("durable completions for the execution = %d, want exactly 1", len(exits))
	}
	if exits[0].Outcome != string(native.OutcomeCompleted) {
		t.Fatalf("stored outcome = %q, want completed", exits[0].Outcome)
	}
	if !strings.Contains(exits[0].Output, "lifecycle-final") {
		t.Fatalf("stored output = %q, want the command's final output", exits[0].Output)
	}
	// The count the status line reads is back to zero: no phantom running work.
	if got := rt.State.RunningJobsCount(); got != 0 {
		t.Fatalf("running jobs after settlement = %d, want 0", got)
	}
}
