package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui"
	"marshal/internal/credentials"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/oauth"
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

// servedPair records the pass's served service pair, for the tests that drive
// agent-side state. The pair is the one launch built before the program ran.
type servedPair struct {
	mu   sync.Mutex
	host *service.Host
	sess *service.Session
}

func (p *servedPair) option() Option {
	return withServed(func(host *service.Host, sess *service.Session) {
		p.mu.Lock()
		p.host, p.sess = host, sess
		p.mu.Unlock()
	})
}

func (p *servedPair) state() *session.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess == nil {
		return nil
	}
	return p.sess.State()
}

func (p *servedPair) serviceHost() *service.Host {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.host
}

// modelState is the pass's served state, or a fatal when it served none.
func modelState(t *testing.T, state *session.State) *session.State {
	t.Helper()
	if state == nil {
		t.Fatal("the pass served no session state")
	}
	return state
}

type workerFunc struct {
	name string
	run  func(context.Context) error
}

func (w workerFunc) Name() string { return w.name }

func (w workerFunc) Run(ctx context.Context) error { return w.run(ctx) }

type countingAuthResolver struct {
	mu         sync.Mutex
	storeOpens []string
	engines    []string
	store      credentials.Store
	engine     *oauth.Engine
	storeErr   error
	engineErr  error
}

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
	err = Run(ctx, bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		loaderCalled = true
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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
	err = Run(context.Background(), stdout, WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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
		errCh <- Run(ctx, bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return config.Default(), nil
		})), WithProgramRunner(func(runCtx context.Context, model tea.Model, output io.Writer) ProgramResult {
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

	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(nil),
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

	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Config{}, wantErr
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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

	err := Run(ctx, bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time {
		return time.Unix(1000, 0)
	})), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(home)), WithProgramRunner(runner), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})))
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

	err := Run(ctx, stdout, WithApp(app.WithNow(func() time.Time {
		return time.Unix(1000, 0)
	})), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(home)), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		return ProgramResult{}
	}), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})))
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
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return cfg, nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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

	var view string
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
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

// TestRunWiresSharedWorkService pins that Run's session serves the same Work
// facade the runtime and ACP use: the service host's Deps.Work is the runtime
// pointer itself. With a Work service attached, the now bar's project-owned
// work lane is active, so Ctrl+T opens the Tasks panel on the Work tab and
// renders its empty state. A nil service (the pre-change wiring) leaves the
// work lane inactive and produces a notice instead of the panel, so the "No
// work yet" assertion is what proves the service host's work service reached
// the model.
func TestRunWiresSharedWorkService(t *testing.T) {
	dir := t.TempDir()

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
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return cfg, nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		m := model.(tui.Model)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = updated.(tui.Model)

		updated, _ = m.Update(tea.PasteMsg{Content: "/work"})
		m = updated.(tui.Model)
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

		if p, ok := updated.(*tui.Model); ok {
			m = *p
		} else {
			m = updated.(tui.Model)
		}
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

	ctx, cancel := context.WithCancel(context.Background())
	rt, err := app.StartRuntime(ctx, app.WithWorkingDir(dir), app.WithNow(func() time.Time { return time.Unix(100, 0) }), app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		cancel()
		t.Fatalf("StartRuntime: %v", err)
	}
	database := rt.DB.(*db.DB)
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
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithNow(func() time.Time { return time.Unix(200, 0) })), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		runs++
		if runs == 1 {

			return ProgramResult{ResumeSession: resumeSessionID}
		}

		m := model.(tui.Model)
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		resumedView = updated.View().Content
		return ProgramResult{}
	}), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})),
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
	served := &servedPair{}
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return now })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil })), served.option(), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		modelState(t, served.state()).AddMessage(session.RoleUser, "keep session history", session.ContentTypePlain)
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
	served := &servedPair{}
	err = Run(context.Background(), stdout, WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithHomeDir(t.TempDir())), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), served.option(), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		called = true
		state := modelState(t, served.state())
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
	served := &servedPair{}

	// Program runner: start tracked work and add a message so the runtime
	// has a turn to quiesce. The work ends when quiesce cancels
	// the work context (observed via state.Done()).
	var statePtr *session.State
	workEnded := make(chan struct{})

	programRunner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		statePtr = modelState(t, served.state())
		statePtr.AddMessage(session.RoleUser, "summarise this turn", session.ContentTypePlain)
		if err := statePtr.BeginWork(); err != nil {
			return ProgramResult{Err: err}
		}
		go func() {
			<-statePtr.Done()
			statePtr.EndWork()
			close(workEnded)
		}()
		return ProgramResult{}
	}

	hookRan := make(chan struct{})
	shutdownHook := func(hookCtx context.Context, st *session.State, database *db.DB) {

		if err := st.BeginWork(); !errors.Is(err, session.ErrSessionQuiescing) {
			t.Errorf("BeginWork during shutdown hook = %v, want ErrSessionQuiescing", err)
		}

		_, err := database.GetOrCreateProject(dir, "knowledge-hook-check")
		if err != nil {
			t.Errorf("DB unusable during shutdown hook: %v", err)
		}

		close(hookRan)
	}

	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return now })), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), served.option(), WithProgramRunner(programRunner), WithApp(app.WithShutdownHook(shutdownHook)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	select {
	case <-workEnded:
	default:
		t.Fatal("work was not completed before Run returned")
	}

	select {
	case <-hookRan:
	default:
		t.Fatal("shutdown hook was not called")
	}

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
	t.Setenv("HOME", t.TempDir())

	var runs int
	served := &servedPair{}
	programRunner := func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		runs++
		st := modelState(t, served.state())
		switch runs {
		case 1:
			if st.Config.Project.Name == "trusted-inline" {
				t.Error("phase 1 applied the project config before trust")
			}

			if _, cmd := model.(interface {
				Update(tea.Msg) (tea.Model, tea.Cmd)
			}).Update(tea.KeyPressMsg{Code: '1', Text: "1"}); cmd != nil {
				_ = cmd()
			}
		case 2:
			if st.Config.Project.Name != "trusted-inline" {
				t.Errorf("phase 2 config name = %q, want trusted-inline", st.Config.Project.Name)
			}
		}
		return ProgramResult{}
	}

	var nowUnix int64 = 100
	err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { nowUnix++; return time.Unix(nowUnix, 0) })), served.option(), WithProgramRunner(programRunner))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if runs != 2 {
		t.Fatalf("program ran %d times, want 2 (initial + trust reload)", runs)
	}
}

func TestRunOpensConnectOnFirstRun(t *testing.T) {
	dir := t.TempDir()
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

	if err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(programRunner),
	); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sawConnect {
		t.Fatal("first run should open the TUI with the connect panel")
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

	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		return ProgramResult{}
	}), WithApp(app.WithWorker(fake)),
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

func TestRunShutdownQuiesceBoundedToOneSecond(t *testing.T) {

	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-handlerDone:
		}
	}))

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
	served := &servedPair{}
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return now })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return knowledgeEnabledConfig(server.URL, "test-provider"), nil
	})), served.option(), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		state := modelState(t, served.state())
		state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
		return ProgramResult{}
	}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("Run took %s, want quiesce bounded to ~1s", elapsed)
	}
}

func TestRunShutdownDoesNotWaitForWorkers(t *testing.T) {

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
	served := &servedPair{}
	err = Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return now })), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return knowledgeEnabledConfig(server.URL, "test-provider"), nil
	})), served.option(), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		state := modelState(t, served.state())
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
	run := func(extra ...app.Option) {
		served := &servedPair{}
		base := []app.Option{
			app.WithNow(func() time.Time { return now }),
			app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return config.Default(), nil }),
			app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		}
		opts := []Option{
			WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
				modelState(t, served.state()).AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
				return ProgramResult{}
			}),
			served.option(),
			WithApp(base...),
			WithApp(extra...),
		}
		if err := Run(context.Background(), bytes.NewBuffer(nil), opts...); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	run()
	id := fmt.Sprintf("sess_%d", now.UnixNano())
	run(app.WithExistingSession(id))

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

// TestRunPassesAuthResolverToTheSeam closes the one fail-open seam in the
// isolation design. The service host builds every provider client and sign-in
// the TUI asks for, so launch.Run's Auth wiring is the single place where an
// isolated runtime could silently regain the real OS keychain, and nothing
// else in the suite would notice if it were dropped.
func TestRunPassesAuthResolverToTheSeam(t *testing.T) {
	auth := &countingAuthResolver{}
	served := &servedPair{}
	var got provider.AuthResolver
	var isPipe bool
	err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithWorkingDir(t.TempDir())), WithApp(app.WithHomeDir(t.TempDir())), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithApp(app.WithAuthResolver(auth)), served.option(), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		_, isPipe = model.(tui.Model).Host().(*rpc.Host)
		if isPipe {
			got = served.serviceHost().AuthResolver()
		}
		return ProgramResult{}
	}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !isPipe {
		t.Fatal("the TUI model does not hold the pipe's client")
	}
	if got == nil {
		t.Fatal("the seam carries no auth resolver")
	}
	if got != provider.AuthResolver(auth) {
		t.Fatalf("seam auth resolver = %T, want the runtime's injected resolver", got)
	}

	if opens := auth.openedStores(); len(opens) != 0 {
		t.Fatalf("resolver OpenStore calls = %v, want none for a provider-less startup", opens)
	}
	if engines := auth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("resolver CodexOAuthEngine calls = %v, want none for a provider-less startup", engines)
	}
}

// TestRunWiresToolNames pins the trust and tools half of Run's Deps wiring: the
// session names no tools while no provider is built, and the host reports no
// pending trust decision for a directory with no project config. Both values
// reach the session from Run, so a dropped field would leave the trust panel
// and the tool roster reading nothing.
func TestRunWiresToolNames(t *testing.T) {
	dir := t.TempDir()
	var (
		tools       []string
		hasTools    bool
		trustPrompt string
	)
	err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(t.TempDir())), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		tools, hasTools = model.(tui.Model).Session().Tools()
		trustPrompt = model.(tui.Model).Host().TrustPrompt().Dir
		return ProgramResult{}
	}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if hasTools {
		t.Errorf("Session().Tools() = %v, true; want false with no provider configured", tools)
	}
	if trustPrompt != "" {
		t.Errorf("Host().TrustPrompt() = %q, want \"\" for a directory with no project config", trustPrompt)
	}
}

// TestRunWiresTheSkillIndex pins the skill-index half of Run's Deps wiring: a
// skill written under the user config's skills directory reaches the session's
// listing with nothing blocking its load. Dropping Deps.SkillIndex from Run
// leaves the session with no index, so every entry would read "this session has
// no skill index" instead.
func TestRunWiresTheSkillIndex(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()

	store := filepath.Join(config.UserDir(home), "skills")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatalf("create skill store: %v", err)
	}
	const raw = "+++\nname = \"wired\"\ndescription = \"wiring fixture\"\n+++\n\n# wired\n"
	if err := os.WriteFile(filepath.Join(store, "wired.md"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	var (
		entries []acpwire.SkillEntry
		listErr error
	)
	err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(home)), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		entries, listErr = model.(tui.Model).Session().Skills(ctx)
		return ProgramResult{}
	}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if listErr != nil {
		t.Fatalf("Session().Skills() error: %v", listErr)
	}
	found := false
	for _, e := range entries {
		if e.Scope != "global" || e.Source != "wired.md" {
			continue
		}
		found = true
		if e.Name != "wired" {
			t.Errorf("skill entry name = %q, want wired", e.Name)
		}
		if e.LoadBlocked != "" {
			t.Errorf("skill entry LoadBlocked = %q, want \"\" (the runtime's index holds it)", e.LoadBlocked)
		}
	}
	if !found {
		t.Fatalf("Session().Skills() = %+v, want the global wired.md entry", entries)
	}
}

// TestRunWiresTheFileIndex pins the @ popup's seam wiring: Run gives the
// session the index reader, and that reader answers from the project database
// Run itself opened. The files below are what the startup index pass saves, so
// a session without the FileIndex dep reports no paths at all.
func TestRunWiresTheFileIndex(t *testing.T) {
	dir := t.TempDir()

	if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = resolved
	}
	for _, name := range []string{"a.go", "b.go"} {
		body := "package x\n\nfunc " + strings.TrimSuffix(name, ".go") + "() {}\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	var (
		paths []string
		ioErr error
	)
	runErr := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(t.TempDir())), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {

		deadline := time.Now().Add(5 * time.Second)
		for {
			paths, ioErr = model.(tui.Model).Session().FileIndex(ctx)
			if ioErr != nil || len(paths) > 0 || !time.Now().Before(deadline) {
				return ProgramResult{}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}),
	)
	if runErr != nil {
		t.Fatalf("Run returned error: %v", runErr)
	}
	if ioErr != nil {
		t.Fatalf("Session().FileIndex() error = %v, want nil", ioErr)
	}
	if want := []string{"a.go", "b.go"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("Session().FileIndex() = %v, want the indexed files %v", paths, want)
	}
}

// TestRunWiresTheSessionsToolsFromTheRuntime pins the positive half of the
// tools wiring, which TestRunWiresToolNames cannot: with a provider built, the
// session names the runtime's registry, so dropping `Tools: rt.ToolNames` from
// Run leaves the roster with nothing to validate against.
func TestRunWiresTheSessionsToolsFromTheRuntime(t *testing.T) {
	dir := t.TempDir()
	var (
		names []string
		known bool
	)
	err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(t.TempDir())), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
		return nativeToolAgentConfig("test-provider"), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		names, known = model.(tui.Model).Session().Tools()
		return ProgramResult{}
	}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !known {
		t.Fatalf("Session().Tools() = %v, false; want the runtime's registry", names)
	}
	if !slices.Contains(names, "file.read") {
		t.Fatalf("Session().Tools() = %v, want the runtime's tools including file.read", names)
	}
	if !slices.IsSorted(names) {
		t.Fatalf("Session().Tools() = %v, want sorted names", names)
	}
}

// TestRunWiresTheConfigSeam pins the config half of Run's Deps wiring: the
// session names the files a config write changes, the runtime's reload path
// reaches ReloadConfig, SaveVerifyCommands' file becomes the session's layers
// (ReloadLayers), and a project save advances the trust record's hash
// (TrustRefresh). Each of the three hooks is load-bearing for /settings, the
// plan-run verify offer and the next launch's trust check, and nothing else in
// the suite would notice if one were dropped.
func TestRunWiresTheConfigSeam(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()

	store := trust.NewStore(config.DataDir(home))
	abs := trust.Canonicalize(dir)
	if err := store.SetTrust(abs, true, "stale"); err != nil {
		t.Fatalf("seed the trust record: %v", err)
	}

	var (
		userConfigPath string
		reloaded       bool
		verifyErr      error
		layers         config.Layers
		saveErr        error
	)
	err := Run(context.Background(), bytes.NewBuffer(nil), WithApp(app.WithNow(func() time.Time { return time.Unix(100, 0) })), WithApp(app.WithWorkingDir(dir)), WithApp(app.WithHomeDir(home)), WithApp(app.WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent})), WithApp(app.WithConfigLoader(func(lo config.LoadOptions) (config.Config, error) {

		if lo.Trusted != nil {
			*lo.Trusted = true
		}
		return config.Default(), nil
	})), WithProgramRunner(func(ctx context.Context, model tea.Model, output io.Writer) ProgramResult {
		sess := model.(tui.Model).Session()
		userConfigPath = sess.Status().UserConfigPath

		reloaded, _ = sess.ReloadConfig(ctx, sess.Config())
		verifyErr = sess.SaveVerifyCommands(ctx, "go build ./...", "go test ./...")
		layers = sess.Layers()
		saveErr = sess.SaveProjectConfig(ctx, sess.Config())
		return ProgramResult{}
	}),
	)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if want := config.UserConfigPath(home); userConfigPath != want {
		t.Errorf("Status().UserConfigPath = %q, want %q", userConfigPath, want)
	}
	if !reloaded {
		t.Error("ReloadConfig reported no reload path, want the runtime's reloader wired")
	}
	if verifyErr != nil {
		t.Fatalf("SaveVerifyCommands: %v", verifyErr)
	}
	if got := layers.Merged.SDD.Verify.Build; got != "go build ./..." {
		t.Errorf("Layers().Merged.SDD.Verify.Build = %q, want the saved command: ReloadLayers is not wired", got)
	}
	if got := layers.Merged.SDD.Verify.Test; got != "go test ./..." {
		t.Errorf("Layers().Merged.SDD.Verify.Test = %q, want the saved command: ReloadLayers is not wired", got)
	}
	if saveErr != nil {
		t.Fatalf("SaveProjectConfig: %v", saveErr)
	}
	stored, err := store.StoredConfigHash(abs)
	if err != nil {
		t.Fatalf("read the trust record: %v", err)
	}
	hash, err := trust.ConfigHashFor(dir)
	if err != nil {
		t.Fatalf("hash the project config: %v", err)
	}
	if stored != hash {
		t.Errorf("stored config hash = %q, want the current %q: TrustRefresh is not wired", stored, hash)
	}
}
