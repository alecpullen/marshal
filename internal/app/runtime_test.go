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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/credentials"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/oauthworker"
	"marshal/internal/llm/routing"
	"marshal/internal/oauth"
	"marshal/internal/tools/native"
	"marshal/internal/trust"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// fakeRunner blocks until the context is cancelled, then returns.
type fakeRunner struct {
	run func(ctx context.Context, req native.CommandRequest) (native.CommandResult, error)
}

func (f *fakeRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	return f.run(ctx, req)
}

func recordOrder(mu *sync.Mutex, order *[]string, s string) {
	mu.Lock()
	*order = append(*order, s)
	mu.Unlock()
}

// fakeCloser is a simple close function that records when it was called.
type fakeCloser struct {
	called atomic.Int64
	err    error
}

func (f *fakeCloser) close() error {
	f.called.Add(1)
	return f.err
}

// ---- fakes for Runtime.Close cleanup stages ----

// recordingMCP satisfies MCPCloser and records the stage name.
type recordingMCP struct {
	name   string
	record func(string)
	err    error
}

func (m *recordingMCP) Close() error {
	if m.record != nil {
		m.record(m.name)
	}
	return m.err
}

// recordingBroker satisfies BrokerCloser and records the stage name.
type recordingBroker struct {
	name   string
	record func(string)
}

func (b *recordingBroker) Close() {
	if b.record != nil {
		b.record(b.name)
	}
}

// recordingSnapshot satisfies SnapshotCloser and records the stage name.
//
// There is deliberately no Prune method: the close path must NOT prune. The
// fake records the lifecycle stage it was actually asked to run, so a test can
// assert that shutdown joined snapshot work and ran no storage maintenance.
type recordingSnapshot struct {
	record func(string)
	err    error
}

func (s *recordingSnapshot) CloseSnapshots(_ context.Context) error {
	if s.record != nil {
		s.record("snapshotClose")
	}
	return s.err
}

// recordingDB satisfies DBCloser and records separate close and prune events.
type recordingDB struct {
	record   func(string)
	closeErr error
	pruneErr error
}

func (d *recordingDB) Close() error {
	if d.record != nil {
		d.record("dbClose")
	}
	return d.closeErr
}

func (d *recordingDB) PruneSnapshotsOlderThan(_ int) error {
	if d.record != nil {
		d.record("dbPrune")
	}
	return d.pruneErr
}

func TestRuntimeQuiesceCancelsAndJoinsTurnBeforeReturning(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	workEnded := make(chan struct{})
	go func() {
		<-workCtx.Done()
		state.EndWork()
		close(workEnded)
	}()

	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}

	select {
	case <-workEnded:
		// Work was joined — correct.
	default:
		t.Fatal("work was not completed before Quiesce returned")
	}

	// Subsequent BeginWork must be rejected.
	if err := state.BeginWork(); !errors.Is(err, session.ErrSessionQuiescing) {
		t.Fatalf("BeginWork after Quiesce = %v, want ErrSessionQuiescing", err)
	}
}

func TestRuntimeQuiesceJoinsBackgroundJobs(t *testing.T) {
	prevTimeout := jobShutdownTimeout
	jobShutdownTimeout = 100 * time.Millisecond
	defer func() { jobShutdownTimeout = prevTimeout }()

	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	// Use a fake runner that blocks until cancelled.
	jobStarted := make(chan struct{})
	fr := &fakeRunner{
		run: func(runCtx context.Context, req native.CommandRequest) (native.CommandResult, error) {
			close(jobStarted)
			<-runCtx.Done()
			return native.CommandResult{}, runCtx.Err()
		},
	}
	jobManager := native.NewJobManager(ctx, fr, t.TempDir(), 5, time.Hour, 1024*1024)

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:      state,
		JobManager: jobManager,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	// Start a background job that will block until cancelled.
	if _, err := jobManager.Launch(ctx, native.LaunchSpec{Command: "blocking-job", Detached: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-jobStarted

	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}

	// After Quiesce, the job manager must have zero running jobs.
	if n := jobManager.RunningCount(); n != 0 {
		t.Fatalf("RunningCount after Quiesce = %d, want 0", n)
	}
}

func TestRuntimeQuiesceLeavesDatabaseOpen(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}

	projectID, err := database.GetOrCreateProject(dir, "quiesce-test")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}

	ctx := context.Background()
	state := session.New(config.Default(), dir, time.Now(), session.Persistence{DB: database, SessionID: "sess_1", Logger: nil})

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		Config:     config.Default(),
		State:      state,
		DB:         database,
		ProjectID:  projectID,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}

	// Database must still be usable after Quiesce.
	_, err = database.GetOrCreateProject(dir, "after-quiesce")
	if err != nil {
		t.Fatalf("database unusable after Quiesce: %v", err)
	}

	database.Close()
}

func TestRuntimeCloseClosesResourcesAfterQuiesce(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	var orderMu sync.Mutex
	var order []string
	record := func(s string) {
		orderMu.Lock()
		order = append(order, s)
		orderMu.Unlock()
	}

	// Fake every cleanup stage so we can verify the full prescribed order:
	//   Quiesce → MCP → jobBroker → steeringBroker → eventBroker →
	//   dbPrune → snapshotClose → dbClose → resourceClosers (reversed) →
	//   state shutdown.
	mcp := &recordingMCP{name: "mcp", record: record}
	jobBroker := &recordingBroker{name: "jobBroker", record: record}
	steeringBroker := &recordingBroker{name: "steeringBroker", record: record}
	eventBroker := &recordingBroker{name: "eventBroker", record: record}
	snap := &recordingSnapshot{record: record}
	database := &recordingDB{record: record}

	closeFn1 := func() { record("closeFn-1") }
	closeFn2 := func() { record("closeFn-2") }

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		Config:          config.Default(),
		State:           state,
		workCtx:         workCtx,
		workCancel:      workCancel,
		MCPManager:      mcp,
		JobBroker:       jobBroker,
		SteeringBroker:  steeringBroker,
		EventBroker:     eventBroker,
		Snapshot:        snap,
		DB:              database,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		resourceClosers: []func(){closeFn1, closeFn2},
	}

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	workEnded := make(chan struct{})
	go func() {
		<-workCtx.Done()
		state.EndWork()
		close(workEnded)
	}()

	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 1. Quiesce must cancel and join work.
	select {
	case <-workEnded:
	default:
		t.Fatal("work was not completed before Close returned (Quiesce not called)")
	}

	// 9. State must be shut down (position at end is inferred from code).
	select {
	case <-state.Done():
	default:
		t.Error("session state was not shut down after Close")
	}

	// Verify the recorded order of stages 2-8.
	orderMu.Lock()
	defer orderMu.Unlock()
	expected := []string{
		"mcp",            // 2. MCP manager
		"jobBroker",      // 3. job broker
		"steeringBroker", // 4. steering broker
		"eventBroker",    // 5. event broker
		"dbPrune",        // 6a. DB prune (bounded bookkeeping, inside snapshot stage)
		"snapshotClose",  // 6b. snapshot cancellation/join (NO filesystem prune)
		"dbClose",        // 7. database
		"closeFn-2",      // 8. resourceClosers reversed (fn2 before fn1)
		"closeFn-1",
	}
	if len(order) != len(expected) {
		t.Fatalf("close order = %v (len %d), want %v (len %d)", order, len(order), expected, len(expected))
	}
	for i := range expected {
		if order[i] != expected[i] {
			t.Fatalf("close order[%d] = %q, want %q; full order: %v", i, order[i], expected[i], order)
		}
	}
}

func TestRuntimeCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	var calls atomic.Int64
	closeFn := func() { calls.Add(1) }

	workCtx, workCancel := context.WithCancel(ctx)
	rt := &Runtime{
		State:           state,
		workCtx:         workCtx,
		workCancel:      workCancel,
		resourceClosers: []func(){closeFn},
	}

	// Call Close twice.
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if n := calls.Load(); n != 1 {
		t.Fatalf("closeFn called %d times, want 1", n)
	}
}

func TestRuntimeCloseRunsResourceClosersInOrder(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	workCtx, workCancel := context.WithCancel(ctx)

	var orderMu sync.Mutex
	var order []string

	rt := &Runtime{
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
	}

	// Pre-populate resourceClosers to verify they all run in reverse order.
	rt.resourceClosers = []func(){
		func() { recordOrder(&orderMu, &order, "fn1") },
		func() { recordOrder(&orderMu, &order, "fn2") },
		func() { recordOrder(&orderMu, &order, "fn3") },
	}

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	go func() {
		<-workCtx.Done()
		state.EndWork()
	}()

	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// resourceClosers run in reverse order: fn3, fn2, fn1.
	orderMu.Lock()
	defer orderMu.Unlock()
	if len(order) != 3 {
		t.Fatalf("expected 3 resourceCloser calls, got %d: %v", len(order), order)
	}
	if order[0] != "fn3" || order[1] != "fn2" || order[2] != "fn1" {
		t.Fatalf("resourceCloser order = %v, want [fn3 fn2 fn1]", order)
	}
}

func TestRuntimeAdditionalDirectories(t *testing.T) {
	ctx := context.Background()
	rt, err := StartRuntime(ctx,
		WithWorkingDir(t.TempDir()),
		WithAdditionalDirectories([]string{"/tmp/extra1", "/tmp/extra2"}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)

	got := rt.AdditionalDirectories()
	if len(got) != 2 || got[0] != "/tmp/extra1" || got[1] != "/tmp/extra2" {
		t.Fatalf("AdditionalDirectories = %v, want [/tmp/extra1 /tmp/extra2]", got)
	}
}

func TestAdditionalDirectoriesDefaultsToNil(t *testing.T) {
	ctx := context.Background()
	rt, err := StartRuntime(ctx, WithWorkingDir(t.TempDir()))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)

	if got := rt.AdditionalDirectories(); got != nil {
		t.Fatalf("AdditionalDirectories = %v, want nil when option not supplied", got)
	}
}

func TestStartRuntimeDefersTrustPromptForTUI(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"),
		[]byte("[project]\nname = \"deferred\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	// No resolver: the interactive TUI path must defer, not prompt on stdin
	// and not apply the project config.
	rt, err := StartRuntime(context.Background(),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		withDeferTrustPrompt(),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	if !rt.TrustPromptPending {
		t.Fatal("TrustPromptPending = false, want true for an untrusted project config")
	}
	if rt.State.Config.Project.Name == "deferred" {
		t.Fatal("project config was applied before the trust answer")
	}
	if rt.State.Trusted() {
		t.Fatal("session must start untrusted while the prompt is pending")
	}
}

func TestStartRuntimeAppliesStoredTrustWithoutPrompt(t *testing.T) {
	// Same project config, but trust recorded with a matching hash:
	// phase 1 applies it with no prompt pending.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".marshal", "config.toml"),
		[]byte("[project]\nname = \"pre-trusted\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	home := t.TempDir()
	t.Setenv("HOME", home)

	// trust.Evaluate calls filepath.Abs(workingDir) which resolves /var → /private/var on macOS.
	// Use the resolved path for SetTrust so the store key matches.
	resolvedDir, _ := os.Getwd()
	hash, _ := trust.ConfigHashFor(dir)
	store := trust.NewStore(filepath.Join(home, ".local", "share", "marshal"))
	if err := store.SetTrust(resolvedDir, true, hash); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}

	rt, err := StartRuntime(context.Background(),
		WithNow(func() time.Time { return time.Unix(100, 0) }),
		withDeferTrustPrompt(),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	if rt.TrustPromptPending {
		t.Fatal("TrustPromptPending = true with valid stored trust")
	}
	if rt.State.Config.Project.Name != "pre-trusted" {
		t.Fatal("stored trust should apply the project config in phase 1")
	}
}

func TestStartRuntimePopulatesCommandRegistry(t *testing.T) {
	dir := t.TempDir()
	origWd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	home := t.TempDir()
	t.Setenv("HOME", home)

	rt, err := StartRuntime(context.Background(), WithNow(func() time.Time { return time.Unix(200, 0) }))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.CommandRegistry == nil {
		t.Fatal("CommandRegistry is nil, want a populated registry")
	}
	if _, ok := rt.CommandRegistry.Lookup("diff"); !ok {
		t.Fatal(`CommandRegistry.Lookup("diff") = false, want true (built-in command missing)`)
	}
	if _, ok := rt.CommandRegistry.Lookup("swarm"); !ok {
		t.Fatal(`CommandRegistry.Lookup("swarm") = false, want true (swarm is TUIOnly but still registered)`)
	}
}

func TestRuntimeNewSessionWithName(t *testing.T) {
	ctx := context.Background()
	// NewSession needs a configured model. Without an injected config this
	// falls back to the real user config, so the test passes only on a
	// machine that already has marshal set up. Pin HOME and supply a config.
	t.Setenv("HOME", t.TempDir())
	rt, err := StartRuntime(ctx,
		WithWorkingDir(t.TempDir()),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig("http://127.0.0.1:1", "test-provider"), nil
		}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)

	state, _, _, _, _, _, _, err := rt.NewSession("fix-auth")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if !state.TitleManuallySet() {
		t.Fatal("TitleManuallySet = false, want true")
	}
	if got := state.Title(); got != "fix-auth" {
		t.Fatalf("Title = %q, want %q", got, "fix-auth")
	}

	// Empty name preserves auto-title behavior.
	state2, _, _, _, _, _, _, err := rt.NewSession("")
	if err != nil {
		t.Fatalf("NewSession empty: %v", err)
	}
	if state2.TitleManuallySet() {
		t.Fatal("empty-name session must not be manually titled")
	}
}

func TestRuntimeNewSessionReloadsLayerSnapshot(t *testing.T) {
	// Regression for the stale-snapshot re-bake: in-session config saves
	// refresh the TUI's layer snapshot but not rt.Layers. NewSession must
	// seed the new session from a fresh load — otherwise its first
	// project-scope save diffs against the startup user layer and bakes
	// user-global values into the committable .marshal/config.toml.
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MARSHAL_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MARSHAL_DATA_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	rt, err := StartRuntime(ctx,
		WithWorkingDir(t.TempDir()),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return knowledgeEnabledConfig("http://127.0.0.1:1", "test-provider"), nil
		}),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(ctx)

	// The user config changes on disk after startup; the startup snapshot
	// in rt.Layers cannot see the write.
	if err := os.MkdirAll(config.UserDir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserConfigPath(home), []byte("[commands]\ntest = \"go test -run changed\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := rt.Layers.User.Commands.Test; got == "go test -run changed" {
		t.Fatal("test premise broken: startup snapshot already saw the post-startup write")
	}

	state, _, _, _, _, _, _, err := rt.NewSession("layers-refresh")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if got := state.Layers().User.Commands.Test; got != "go test -run changed" {
		t.Errorf("new session Layers().User.Commands.Test = %q, want the on-disk value", got)
	}
	// The runtime snapshot converges too, so later sessions stay fresh.
	if got := rt.Layers.User.Commands.Test; got != "go test -run changed" {
		t.Errorf("rt.Layers.User.Commands.Test = %q, want the refreshed snapshot", got)
	}
}

func TestRuntimeCloseJoinsErrorsAndContinues(t *testing.T) {
	prevTimeout := jobShutdownTimeout
	jobShutdownTimeout = 50 * time.Millisecond
	defer func() { jobShutdownTimeout = prevTimeout }()

	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})

	workCtx, workCancel := context.WithCancel(ctx)

	// Quiesce-stage error: a background job that times out on shutdown
	// so Quiesce returns DeadlineExceeded.
	jobStarted := make(chan struct{})
	fr := &fakeRunner{
		run: func(runCtx context.Context, req native.CommandRequest) (native.CommandResult, error) {
			close(jobStarted)
			<-runCtx.Done()
			time.Sleep(200 * time.Millisecond)
			return native.CommandResult{}, runCtx.Err()
		},
	}
	jobManager := native.NewJobManager(ctx, fr, t.TempDir(), 5, time.Hour, 1024*1024)
	if _, err := jobManager.Launch(ctx, native.LaunchSpec{Command: "blocking", Detached: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-jobStarted

	// Close-stage errors: inject failures into MCP and DB
	// (broker Close() has no return value so it cannot produce an error).
	// Verify each is surfaced and later stages (resourceClosers, state shutdown)
	// still ran.
	mcpErr := errors.New("mcp failure")
	dbErr := errors.New("db close failure")

	mcp := &recordingMCP{name: "mcp", err: mcpErr}
	db := &recordingDB{closeErr: dbErr}

	// Mark that later stages (resourceClosers, state shutdown) ran despite errors.
	var orderMu sync.Mutex
	var order []string
	record := func(s string) {
		orderMu.Lock()
		order = append(order, s)
		orderMu.Unlock()
	}

	rt := &Runtime{
		Config:     config.Default(),
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
		JobManager: jobManager,
		MCPManager: mcp,
		DB:         db,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		resourceClosers: []func(){
			func() { record("closeFn-2") },
			func() { record("closeFn-3") },
		},
	}

	closeErr := rt.Close(ctx)
	if closeErr == nil {
		t.Fatal("expected Close to return errors, got nil")
	}

	// (a) errors.Is for each injected failure.
	if !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Errorf("Close error should contain DeadlineExceeded, got %v", closeErr)
	}
	if !errors.Is(closeErr, mcpErr) {
		t.Errorf("Close error should contain %q, got %v", mcpErr, closeErr)
	}
	// steering broker has no Close() return value, so no error.
	if !errors.Is(closeErr, dbErr) {
		t.Errorf("Close error should contain %q, got %v", dbErr, closeErr)
	}

	// (b) Later stages (resourceClosers, state shutdown) still ran.
	if len(order) != 2 {
		t.Errorf("expected 2 resourceCloser calls, got %d: %v", len(order), order)
	} else {
		if order[0] != "closeFn-3" || order[1] != "closeFn-2" {
			t.Errorf("resourceCloser order = %v, want [closeFn-3 closeFn-2]", order)
		}
	}

	select {
	case <-state.Done():
	default:
		t.Error("session state was not shut down after Close")
	}
}

// The context pack starts empty of repo orientation: no repo card,
// directory map or recent-session summaries, even when the index and
// session history exist.
func TestRuntimeSeedsNoRepoOrientation(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	start := func() *Runtime {
		rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
		if err != nil {
			t.Fatalf("StartRuntime: %v", err)
		}
		return rt
	}
	// First start creates the project row under whatever root the runtime
	// resolves (temp dirs are symlinked on macOS, so never guess the path).
	// Record an ended session with a summary against that row, then restart.
	first := start()
	pid := first.ProjectID
	if err := first.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	database, err := db.Open(db.Path(tmp))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if err := database.CreateSession("sess_old", pid, "old", now); err != nil {
		t.Fatal(err)
	}
	if err := database.EndSession("sess_old", now, "did some work"); err != nil {
		t.Fatal(err)
	}
	database.Close()

	rt := start()
	defer rt.Close(context.Background())
	for _, s := range rt.State.ContextPack().Sections {
		switch s.Kind {
		case contextpack.SectionRepoCard, contextpack.SectionRepoMap, contextpack.SectionSessionSummaries:
			t.Fatalf("context pack carries %s section %q", s.Kind, s.Title)
		}
	}
}

// ── Task 5: credential composition over the real wiring ───────────────

// TestOAuthWorkerSharesEngineAndRefreshesAgainstOwnedServer is the
// composition regression for step 7. It builds the provider and the refresh
// worker the way startup does — both through the runtime's selected resolver —
// and then proves, against a test-owned token endpoint, that:
//
//  1. the worker's engine is the SAME object the provider holds, so a
//     proactive refresh and an inline one serialize on one mutex; and
//  2. a synthetic token read and a real refresh both flow through the
//     injected in-memory store, never the OS keychain.
//
// The worker is driven with its injectable clock/sleep seams, so nothing waits
// on a production retry interval.
func TestOAuthWorkerSharesEngineAndRefreshesAgainstOwnedServer(t *testing.T) {
	ctx := context.Background()

	// Test-owned token endpoint. It hands back a rotated token set and counts
	// how many refreshes it served.
	var (
		mu        sync.Mutex
		refreshes int
	)
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		refreshes++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "rotated-access",
			"refresh_token": "rotated-refresh",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	defer tokenServer.Close()

	const providerName = "compose-codex"

	resolver, store := memoryAuthResolver(t)
	seedCodexToken(t, store, providerName, "initial-access")

	// The runtime builds the provider through this resolver.
	cfg := codexOAuthProviderConfig(providerName, "https://chatgpt.com/backend-api")
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
	defer rt.Close(ctx)

	if rt.Runner == nil {
		t.Fatal("the OAuth provider was not built")
	}
	if got := rt.Runner.Provider.Name(); got != providerName {
		t.Fatalf("runner provider = %q, want %q", got, providerName)
	}

	workers := buildOAuthWorkers(cfg, rt.Logger, resolver)
	var worker *oauthworker.Worker
	for _, w := range workers {
		if ow, ok := w.(*oauthworker.Worker); ok && ow.Provider == providerName {
			worker = ow
			break
		}
	}
	if worker == nil {
		t.Fatalf("no refresh worker for %q (workers=%d)", providerName, len(workers))
	}

	// (1) Provider/worker engine identity. The provider reached through the
	// resolver is the same engine the worker was given — one mutex, one store.
	engine, err := resolver.CodexOAuthEngine(providerName)
	if err != nil {
		t.Fatalf("resolver CodexOAuthEngine: %v", err)
	}
	if worker.Engine != engine {
		t.Fatal("the refresh worker and the provider do not share one engine")
	}

	// (2a) Synthetic token read through the shared engine.
	token, err := worker.Engine.TokenSource(ctx)
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if token != "initial-access" {
		t.Fatalf("token = %q, want the token seeded in the injected store", token)
	}

	// Point the shared engine at the owned token endpoint and drive a real
	// refresh through the worker. The worker's own engine object is mutated,
	// so this is the identity under test, not a copy.
	worker.Engine.Flow.Issuer = "https://auth.example.invalid"
	worker.Engine.Flow.AuthorizeURL = "https://auth.example.invalid/authorize"
	worker.Engine.Flow.TokenURL = tokenServer.URL
	worker.Engine.Flow.ClientID = "test-client"

	// The stored token records the issuing endpoint; a refresh is refused if
	// the advertised one differs, so align it with the owned server.
	raw, err := store.Get("marshal:provider:" + providerName)
	if err != nil {
		t.Fatalf("store read: %v", err)
	}
	var stored oauth.StoredTokens
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode stored tokens: %v", err)
	}
	stored.TokenEndpoint = tokenServer.URL
	// Expire the stored token so the schedule is the 1s floor, and the
	// refresh happens on the worker's first due cycle.
	stored.ExpiresAt = time.Now().Add(-time.Hour)
	stored.IssuedAt = time.Now().Add(-2 * time.Hour)
	updated, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored tokens: %v", err)
	}
	if err := store.Set("marshal:provider:"+providerName, updated); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	// Barrier-based shutdown: the sleeper stops the loop after the refresh has
	// been observed, so the test never waits for an interval.
	refreshed := make(chan struct{})
	var stopOnce sync.Once
	worker.Sleep = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		served := refreshes
		mu.Unlock()
		if served > 0 {
			stopOnce.Do(func() { close(refreshed) })
			return false // stop the loop
		}
		// The loop is asking to wait for its next due cycle; let it proceed
		// without actually sleeping.
		return true
	}

	runDone := make(chan error, 1)
	go func() { runDone <- worker.Run(ctx) }()

	select {
	case <-refreshed:
	case err := <-runDone:
		t.Fatalf("worker exited before refreshing (err=%v)", err)
	case <-time.After(5 * time.Second):
		t.Fatal("worker never refreshed against the owned token endpoint")
	}
	if err := <-runDone; err != nil {
		t.Fatalf("worker Run: %v", err)
	}

	// The rotated token must have been persisted to the injected store, and
	// an inline read must now see it: the worker and the provider share both
	// the engine and its storage.
	rotated, err := worker.Engine.TokenSource(ctx)
	if err != nil {
		t.Fatalf("TokenSource after refresh: %v", err)
	}
	if rotated != "rotated-access" {
		t.Fatalf("token after refresh = %q, want %q", rotated, "rotated-access")
	}

	// The refresh went to the test-owned server exactly once and nothing else
	// was contacted.
	mu.Lock()
	served := refreshes
	mu.Unlock()
	if served != 1 {
		t.Fatalf("owned token endpoint served %d refreshes, want exactly 1", served)
	}
}

// TestFixtureStartupDoesNotTouchAmbientCredentialConfig turns the diagnostic
// ambient-config probe into durable regression coverage. The package runs with
// HOME pointed at a temporary directory (internal/testenv.Run), so a synthetic
// "developer" config planted at the process-default user path is exactly what
// an unisolated startup would have loaded.
//
// Control first: the ambient fixture declares an OAuth Codex provider and an
// OAuth MCP server, so an unisolated startup would have REQUESTED credentials
// for both. The resolver used as the control records those requests instead of
// opening anything real, proving the fixture is credential-shaped rather than
// inert. Then the isolated case: startup with an injected home, an injected
// config loader and an injected resolver never even loads the ambient layers,
// let alone asks for a credential.
//
// No real developer data is touched: the ambient file is written only after a
// guard proves the process home is a test-owned temp dir, and both cases use
// in-memory stores and non-routable endpoints.
func TestFixtureStartupDoesNotTouchAmbientCredentialConfig(t *testing.T) {
	const ambientUserConfig = `[project]
name = "ambient-project"

[privacy]
remote_providers_allowed = true

[profile]
default = "ambient_profile"

[providers.ambient-codex]
type = "openai_codex"
base_url = "https://example.invalid/backend-api"
auth = "oauth"

[models.presets.ambient]
provider = "ambient-codex"
model = "gpt-5.6-luna"

[agent_profiles.ambient_profile]
implementer = "ambient"
planner = "ambient"
repo_scout = "ambient"
tester = "ambient"
reviewer = "ambient"

[mcp.servers.ambient-mcp]
url = "https://127.0.0.1:9/mcp"
type = "http"
auth = "oauth"
trust = "unrestricted"
`
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
	// home. If package isolation is ever dropped, this must fail rather than
	// overwrite a developer's real config.
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
		if priorErr == nil {
			_ = os.WriteFile(ambientPath, priorAmbient, 0o644)
			return
		}
		_ = os.Remove(ambientPath)
	})

	// Control: the ambient configuration, read from the process-default home,
	// genuinely asks for credentials. This is the "unisolated synthetic config
	// would request credentials" half of the regression — recorded by the fake
	// resolver rather than opened against a real backend. The fixture is
	// fully hermetic: every endpoint is closed (port 9) or non-routable, and
	// the resolver never opens a real store.
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

	// Startup over that ambient configuration requests the ambient provider's
	// OAuth engine. It does not reach a real keychain — the resolver hands back
	// a memory-backed engine and store — but the *requests* are the dependency
	// this regression exists to make visible. The MCP endpoint is a closed
	// loopback port, so the connect attempt fails fast and the manager reports
	// a per-server failure rather than aborting the runtime.
	controlStore := credentials.NewMemStore()
	controlEngine, err := provider.NewCodexOAuthEngineWithStore("ambient-codex", controlStore)
	if err != nil {
		t.Fatalf("NewCodexOAuthEngineWithStore: %v", err)
	}
	controlAuth := &countingAuthResolver{store: controlStore, engine: controlEngine}
	controlCtx, controlCancel := context.WithCancel(context.Background())
	controlRuntime, controlErr := StartRuntime(controlCtx,
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return control, nil }),
		WithAuthResolver(controlAuth),
	)
	controlCancel()
	if controlErr == nil {
		_ = controlRuntime.Close(context.Background())
	}
	if engines := controlAuth.enginesRequested(); len(engines) == 0 {
		t.Fatalf("control startup never requested the ambient provider's OAuth engine; the fixture is not credential-shaped (err=%v)", controlErr)
	} else if engines[0] != "ambient-codex" {
		t.Fatalf("control resolver engine requests = %v, want the ambient provider", engines)
	}
	// The ambient MCP server is credential-shaped too: startup opens a store
	// for it (through the injected opener) before it can fail to connect.
	if opens := controlAuth.openedStores(); len(opens) == 0 {
		t.Fatal("control startup never requested a credential store for the ambient OAuth MCP server")
	}

	// Isolated case: a test-owned home, an injected loader and an injected
	// resolver. The ambient provider must never appear and no credential entry
	// point may be reached.
	fixtureHome := t.TempDir()
	fixtureWork := t.TempDir()
	fixtureAuth := &countingAuthResolver{}

	fixtureCfg := config.Default()
	fixtureCfg.Project.Name = "fixture-project"

	rt, err := StartRuntime(context.Background(),
		WithWorkingDir(fixtureWork),
		WithHomeDir(fixtureHome),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return fixtureCfg, nil }),
		WithAuthResolver(fixtureAuth),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())

	if rt.Config.Project.Name != "fixture-project" {
		t.Fatalf("Project.Name = %q, want the fixture value", rt.Config.Project.Name)
	}
	if _, ok := rt.Config.Providers["ambient-codex"]; ok {
		t.Fatalf("isolated startup inherited the ambient provider: %+v", rt.Config.Providers)
	}
	if _, ok := rt.Config.MCP.Servers["ambient-mcp"]; ok {
		t.Fatalf("isolated startup inherited the ambient MCP server: %+v", rt.Config.MCP.Servers)
	}
	if opens := fixtureAuth.openedStores(); len(opens) != 0 {
		t.Fatalf("isolated startup requested credential stores: %v", opens)
	}
	if engines := fixtureAuth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("isolated startup requested OAuth engines: %v", engines)
	}
	// The fixture's own (provider-less) startup built no runner, so the
	// engine-request assertion above cannot pass vacuously for a built
	// provider: assert the same thing where a provider IS built.
	providerCfg := nativeToolAgentConfig("fixture-provider")
	providerCfg.Project.Name = "fixture-provider-project"
	rt2, err := StartRuntime(context.Background(),
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return providerCfg, nil }),
		WithAuthResolver(fixtureAuth),
	)
	if err != nil {
		t.Fatalf("StartRuntime with a provider: %v", err)
	}
	defer rt2.Close(context.Background())
	if rt2.Runner == nil {
		t.Fatal("the fixture provider was not built; the isolation assertion would be vacuous")
	}
	if _, ok := rt2.Config.Providers["ambient-codex"]; ok {
		t.Fatalf("isolated startup with a provider inherited the ambient provider: %+v", rt2.Config.Providers)
	}
	if engines := fixtureAuth.enginesRequested(); len(engines) != 0 {
		t.Fatalf("isolated startup requested OAuth engines: %v", engines)
	}
	if opens := fixtureAuth.openedStores(); len(opens) != 0 {
		t.Fatalf("isolated startup requested credential stores: %v", opens)
	}
}

// ---- system-access approval: the runtime resolver ----

// accessPendingCall builds the canonical pending call an access request needs:
// a buffered, empty response channel and a stable provider call ID.
func accessPendingCall(id string) *session.PendingToolCall {
	return &session.PendingToolCall{
		ID:           id,
		Name:         "mode.request",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
}

// newAccessTestRuntime builds a Runtime with a real runner/policy pair and a
// session state, without starting the full runtime: no database, no brokers,
// no background managers. That is enough for the access resolver, which is
// memory-only by contract, and for reloadAgentRuntime, which tolerates nil
// database and broker handles.
func newAccessTestRuntime(t *testing.T, cfg config.Config) (*Runtime, *session.State) {
	t.Helper()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	runner, reg, swarmRunner, _, _, jobMgr, _, _, _, _, _, _, err := buildAgentRunner(
		context.Background(), cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	workCtx, workCancel := context.WithCancel(context.Background())
	rt := &Runtime{
		Config:       cfg,
		State:        state,
		Runner:       runner,
		ToolRegistry: reg,
		SwarmRunner:  swarmRunner,
		JobManager:   jobMgr,
		workCtx:      workCtx,
		workCancel:   workCancel,
	}
	t.Cleanup(func() {
		workCancel()
		if jobMgr != nil {
			sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = jobMgr.Shutdown(sc)
		}
	})
	return rt, state
}

// registerAccess publishes a request on state and fails the test if the
// session refused it.
func registerAccess(t *testing.T, state *session.State, id string, intent session.AccessIntent) session.AccessRequest {
	t.Helper()
	req, _ := registerAccessCh(t, state, id, intent)
	return req
}

// registerAccessCh is registerAccess for tests that must observe the canonical
// decision. The channel is captured at registration because settlement clears
// the pending slot, so it cannot be read afterwards.
func registerAccessCh(t *testing.T, state *session.State, id string, intent session.AccessIntent) (session.AccessRequest, chan session.UserApprovalDecision) {
	t.Helper()
	call := accessPendingCall(id)
	req, waiting, err := state.RegisterAccess(context.Background(), call, intent)
	if err != nil {
		t.Fatalf("RegisterAccess: %v", err)
	}
	if !waiting {
		t.Fatalf("RegisterAccess published no request for intent %+v", intent)
	}
	return req, call.ResponseChan
}

// approve builds the choice a transport sends when the user accepts req.
func approve(req session.AccessRequest, mode string) session.AccessChoice {
	return session.AccessChoice{
		RequestID: req.RequestID,
		SessionID: req.SessionID,
		Current:   req.Current,
		Approved:  true,
		Mode:      mode,
	}
}

// A system-only grant preserves whichever mode the session is in, including
// when the request also carried an editing hint the session had already
// satisfied.
func TestRuntimeAccessSystemOnlyPreservesEachMode(t *testing.T) {
	for _, mode := range []string{"plan", "default", "edit", "copilot", "auto"} {
		t.Run(mode, func(t *testing.T) {
			cfg := nativeToolAgentConfig("access-system-only")
			cfg.Agent.ApprovalMode = mode
			rt, state := newAccessTestRuntime(t, cfg)

			req := registerAccess(t, state, "call-1", session.AccessIntent{System: true, Reason: "read outside the workspace"})
			if req.MissingEditing || !req.MissingSystem {
				t.Fatalf("system-only request flags = editing %t / system %t, want false/true", req.MissingEditing, req.MissingSystem)
			}
			result, err := rt.ResolveAccess(approve(req, ""))
			if err != nil {
				t.Fatalf("ResolveAccess: %v", err)
			}
			if result.Status != session.AccessGranted {
				t.Fatalf("status = %q, want %q", result.Status, session.AccessGranted)
			}
			if want := (session.AccessState{Mode: mode, System: true}); result.Effective != want {
				t.Fatalf("effective = %+v, want %+v", result.Effective, want)
			}
			if got := state.AccessState(); got != (session.AccessState{Mode: mode, System: true}) {
				t.Fatalf("session pair = %+v, want the mode preserved with system access on", got)
			}
			if got := string(rt.Runner.Policy.ApprovalMode()); got != mode {
				t.Fatalf("policy mode = %q, want %q (a system-only grant must not switch modes)", got, mode)
			}
		})
	}

	// A legacy editing hint alongside system access is still system-only once
	// the session already has editing authority.
	for _, mode := range []string{"edit", "copilot", "auto"} {
		t.Run(mode+"/legacy-hint", func(t *testing.T) {
			cfg := nativeToolAgentConfig("access-system-only")
			cfg.Agent.ApprovalMode = mode
			rt, state := newAccessTestRuntime(t, cfg)

			req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
			if req.MissingEditing {
				t.Fatalf("editing reported missing in %s mode; the hint must not create a selector", mode)
			}
			if !req.MissingSystem {
				t.Fatal("system access should still be missing")
			}
			result, err := rt.ResolveAccess(approve(req, ""))
			if err != nil {
				t.Fatalf("ResolveAccess: %v", err)
			}
			if want := (session.AccessState{Mode: mode, System: true}); result.Effective != want {
				t.Fatalf("effective = %+v, want %+v", result.Effective, want)
			}
		})
	}
}

// A manual mode change that satisfies a waiting request settles it as
// already-available: the waiter unblocks, nothing is applied again, and the
// request stops being presented.
func TestRuntimeAccessSatisfiedWhilePendingSettlesAvailable(t *testing.T) {
	cfg := nativeToolAgentConfig("access-satisfied")
	rt, state := newAccessTestRuntime(t, cfg)

	req, decisions := registerAccessCh(t, state, "call-1", session.AccessIntent{Editing: true, Reason: "edit a file"})
	if !req.MissingEditing {
		t.Fatal("editing should be missing from the configured default mode")
	}

	// The user switches mode by hand while the request is still waiting.
	if err := rt.SetSessionMode("copilot"); err != nil {
		t.Fatalf("SetSessionMode: %v", err)
	}
	before := rt.Runner.Policy.ApprovalMode()

	got, pending := rt.AccessRequest()
	if pending {
		t.Fatalf("AccessRequest still presents a satisfied request: %+v", got)
	}

	// The waiter was settled as already-available, not granted again.
	decision := <-decisions
	if decision.Access == nil {
		t.Fatal("the waiter received no typed result")
	}
	if decision.Access.Status != session.AccessAvailable {
		t.Fatalf("status = %q, want %q", decision.Access.Status, session.AccessAvailable)
	}
	if decision.Approved {
		t.Fatal("an already-available result must not report approval")
	}
	if want := (session.AccessState{Mode: "copilot"}); decision.Access.Effective != want {
		t.Fatalf("effective = %+v, want %+v", decision.Access.Effective, want)
	}
	if after := rt.Runner.Policy.ApprovalMode(); after != before {
		t.Fatalf("policy mode moved from %q to %q while settling an already-available request", before, after)
	}
}

// A combined grant commits the policy engine, the force class and the session
// pair before the waiting tool can observe the typed result.
func TestRuntimeAccessCombinedCommitsSelectedBundle(t *testing.T) {
	for _, current := range []string{"default", "plan"} {
		for _, selected := range []string{"edit", "copilot", "auto"} {
			t.Run(current+"->"+selected, func(t *testing.T) {
				cfg := nativeToolAgentConfig("access-combined")
				cfg.Agent.ApprovalMode = current
				rt, state := newAccessTestRuntime(t, cfg)

				req, decisions := registerAccessCh(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
				if !req.MissingEditing || !req.MissingSystem {
					t.Fatalf("combined request flags = editing %t / system %t, want true/true", req.MissingEditing, req.MissingSystem)
				}
				result, err := rt.ResolveAccess(approve(req, selected))
				if err != nil {
					t.Fatalf("ResolveAccess: %v", err)
				}
				if result.Status != session.AccessGranted {
					t.Fatalf("status = %q, want %q", result.Status, session.AccessGranted)
				}
				want := session.AccessState{Mode: selected, System: true}
				if result.Effective != want {
					t.Fatalf("effective = %+v, want %+v", result.Effective, want)
				}

				// The typed result is already queued; everything it claims must
				// already be true at the moment it becomes observable.
				decision := <-decisions
				if !decision.Approved || decision.Access == nil {
					t.Fatalf("decision = %+v, want an approved typed grant", decision)
				}
				if decision.Access.Status != session.AccessGranted {
					t.Fatalf("delivered status = %q, want %q", decision.Access.Status, session.AccessGranted)
				}
				if got := state.AccessState(); got != want {
					t.Fatalf("session pair at acknowledgment = %+v, want %+v", got, want)
				}
				if got := string(rt.Runner.Policy.ApprovalMode()); got != selected {
					t.Fatalf("policy mode at acknowledgment = %q, want %q", got, selected)
				}
				if got := rt.Runner.ForceClass; got != "edit" {
					t.Fatalf("force class at acknowledgment = %q, want %q", got, "edit")
				}
			})
		}
	}
}

// A combined request cannot be applied without a policy engine, and the
// refusal leaves both capabilities untouched; a system-only request needs no
// editing setter and still succeeds.
func TestRuntimeAccessUnavailablePolicyFailsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		detach func(rt *Runtime)
	}{
		{name: "no runner", detach: func(rt *Runtime) { rt.Runner = nil }},
		{name: "no policy engine", detach: func(rt *Runtime) { rt.Runner.Policy = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := nativeToolAgentConfig("access-no-policy")
			rt, state := newAccessTestRuntime(t, cfg)
			tc.detach(rt)

			req, decisions := registerAccessCh(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
			result, err := rt.ResolveAccess(approve(req, "edit"))
			if err == nil {
				t.Fatal("a combined grant succeeded without a policy engine")
			}
			if result.Status != session.AccessFailed {
				t.Fatalf("status = %q, want %q", result.Status, session.AccessFailed)
			}
			if want := (session.AccessState{Mode: "default"}); result.Effective != want {
				t.Fatalf("effective = %+v, want the unchanged %+v", result.Effective, want)
			}
			if got := state.AccessState(); got != (session.AccessState{Mode: "default"}) {
				t.Fatalf("session pair = %+v, want both capabilities unchanged", got)
			}
			decision := <-decisions
			if decision.Approved {
				t.Fatal("a failed grant was acknowledged as approved")
			}
			if decision.Access == nil || decision.Access.Status != session.AccessFailed {
				t.Fatalf("delivered result = %+v, want a typed failure", decision.Access)
			}

			// System-only needs no editing setter, so it still works.
			sysReq := registerAccess(t, state, "call-2", session.AccessIntent{System: true, Reason: "read outside"})
			sysResult, err := rt.ResolveAccess(approve(sysReq, ""))
			if err != nil {
				t.Fatalf("system-only ResolveAccess: %v", err)
			}
			if sysResult.Status != session.AccessGranted {
				t.Fatalf("system-only status = %q, want %q", sysResult.Status, session.AccessGranted)
			}
			if got := state.AccessState(); got != (session.AccessState{Mode: "default", System: true}) {
				t.Fatalf("session pair = %+v, want system access on with the mode preserved", got)
			}
		})
	}
}

// A grant is a session-scoped permission change: it must not save a config
// file, reload the runtime, or replace the managers a running job depends on.
func TestRuntimeAccessDoesNotSaveReloadOrReplaceJobs(t *testing.T) {
	cfg := nativeToolAgentConfig("access-no-reload")
	rt, state := newAccessTestRuntime(t, cfg)

	// A save-visible fixture: bytes of a config file the runtime could
	// plausibly rewrite. A grant must leave them byte-identical.
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[agent]\napproval_mode = \"default\"\n"), 0644); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}

	var reloads atomic.Int64
	rt.ConfigReloader = func(config.Config) error {
		reloads.Add(1)
		return nil
	}
	runner, jobMgr, watchMgr := rt.Runner, rt.JobManager, rt.WatchManager

	req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
	if _, err := rt.ResolveAccess(approve(req, "copilot")); err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}

	if n := reloads.Load(); n != 0 {
		t.Fatalf("grant triggered %d config reloads, want 0", n)
	}
	if rt.Runner != runner {
		t.Fatal("grant replaced the runner")
	}
	if rt.JobManager != jobMgr {
		t.Fatal("grant replaced the job manager")
	}
	if rt.WatchManager != watchMgr {
		t.Fatal("grant replaced the watch manager")
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-read fixture config: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("grant rewrote the config file:\nbefore %q\nafter  %q", before, after)
	}
}

// A grant must not disturb an already-running background command: same
// manager, same job identity, same captured execution settings.
func TestRuntimeAccessKeepsRunningCommandIdentity(t *testing.T) {
	cfg := nativeToolAgentConfig("access-running-job")
	rt, state := newAccessTestRuntime(t, cfg)

	ctx := context.Background()
	started := make(chan native.CommandRequest, 1)
	release := make(chan struct{})
	fr := &fakeRunner{run: func(runCtx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		started <- req
		select {
		case <-release:
			return native.CommandResult{Stdout: "done"}, nil
		case <-runCtx.Done():
			return native.CommandResult{}, runCtx.Err()
		}
	}}
	jobMgr := native.NewJobManager(ctx, fr, state.WorkingDir, 2, time.Minute, 4096)
	rt.JobManager = jobMgr
	t.Cleanup(func() {
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jobMgr.Shutdown(sc)
	})

	// Managed commands go through Launch: there is no execution deadline to
	// pass, because no wall-clock budget ends a managed process.
	info, err := jobMgr.Launch(ctx, native.LaunchSpec{Command: "fixture download", Detached: true})
	if err != nil {
		t.Fatalf("JobManager.Launch: %v", err)
	}
	id := info.ID
	var captured native.CommandRequest
	select {
	case captured = <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the fixture command never started")
	}

	req := registerAccess(t, state, "call-1", session.AccessIntent{System: true, Reason: "read outside the workspace"})
	if _, err := rt.ResolveAccess(approve(req, "")); err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}

	if rt.JobManager != jobMgr {
		t.Fatal("grant replaced the job manager")
	}
	found := false
	for _, info := range jobMgr.List() {
		if info.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("job %s is no longer listed after the grant", id)
	}
	if _, _, err := jobMgr.Output(id, 10); err != nil {
		t.Fatalf("job %s is no longer readable after the grant: %v", id, err)
	}
	// The captured execution settings are the ones the job launched with.
	if captured.Command != "fixture download" {
		t.Fatalf("captured command = %q, want %q", captured.Command, "fixture download")
	}
	if captured.Dir != state.WorkingDir {
		t.Fatalf("captured dir = %q, want %q", captured.Dir, state.WorkingDir)
	}
	if captured.MaxOutputBytes != 4096 {
		t.Fatalf("captured max output bytes = %d, want 4096", captured.MaxOutputBytes)
	}
	if captured.Timeout != 0 {
		t.Fatalf("captured timeout = %v, want the launched value 0", captured.Timeout)
	}

	close(release)
	if _, err := jobMgr.Wait(ctx, id); err != nil {
		t.Fatalf("job %s did not complete normally: %v", id, err)
	}
}

// A session-scoped mode override survives a reload that changes an unrelated
// setting, and the runtime's configured baseline stays the configured value.
func TestRuntimeAccessOverrideSurvivesReload(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("access-override")
	rt, state := newAccessTestRuntime(t, cfg)

	req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
	if _, err := rt.ResolveAccess(approve(req, "copilot")); err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}

	// An unrelated setting changes; the configured mode does not.
	reloaded := nativeToolAgentConfig("access-override")
	reloaded.Agent.MaxToolIterations = cfg.Agent.MaxToolIterations + 7
	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	if got, want := state.AccessState(), (session.AccessState{Mode: "copilot", System: true}); got != want {
		t.Fatalf("session pair after reload = %+v, want %+v", got, want)
	}
	if got := string(rt.Runner.Policy.ApprovalMode()); got != "copilot" {
		t.Fatalf("policy mode after reload = %q, want %q", got, "copilot")
	}
	if got := rt.Config.Agent.ApprovalMode; got != "default" {
		t.Fatalf("Runtime.Config mode = %q, want the configured %q", got, "default")
	}
}

// An explicit change to the configured mode replaces the session override,
// while the system grant survives.
func TestRuntimeExplicitConfiguredModeReplacesOverride(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("access-explicit")
	rt, state := newAccessTestRuntime(t, cfg)

	req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
	if _, err := rt.ResolveAccess(approve(req, "copilot")); err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}

	reloaded := nativeToolAgentConfig("access-explicit")
	reloaded.Agent.ApprovalMode = "edit"
	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	if got, want := state.AccessState(), (session.AccessState{Mode: "edit", System: true}); got != want {
		t.Fatalf("session pair after an explicit configured change = %+v, want %+v", got, want)
	}
	if got := string(rt.Runner.Policy.ApprovalMode()); got != "edit" {
		t.Fatalf("policy mode = %q, want %q", got, "edit")
	}
	if got := rt.Config.Agent.ApprovalMode; got != "edit" {
		t.Fatalf("Runtime.Config mode = %q, want %q", got, "edit")
	}
}

// A reload that cannot build leaves the access pair, the runner and the
// override exactly as they were.
func TestRuntimeFailedReloadKeepsAccessState(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("access-failed-reload")
	rt, state := newAccessTestRuntime(t, cfg)

	req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
	if _, err := rt.ResolveAccess(approve(req, "copilot")); err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}
	runner := rt.Runner

	// A config that cannot build: the preset names a provider that is not
	// configured, so resolution fails before any credential is touched.
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
	if rt.Runner != runner {
		t.Fatal("a failed reload replaced the runner")
	}
	if got, want := state.AccessState(), (session.AccessState{Mode: "copilot", System: true}); got != want {
		t.Fatalf("session pair after a failed reload = %+v, want %+v", got, want)
	}
	if got := string(rt.Runner.Policy.ApprovalMode()); got != "copilot" {
		t.Fatalf("policy mode after a failed reload = %q, want %q", got, "copilot")
	}
	if got := rt.Config.Agent.ApprovalMode; got != "default" {
		t.Fatalf("Runtime.Config mode after a failed reload = %q, want the configured %q", got, "default")
	}
}

// A new conversation and a resumed one both start from the configured mode
// with system access off, and a retired request cannot reach either.
func TestRuntimeNewAndResumedSessionResetAccess(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("access-reset")
	cfg.Skills.Autoload = nil
	work := t.TempDir()
	home := t.TempDir()

	rt, err := StartRuntime(ctx,
		WithWorkingDir(work),
		WithHomeDir(home),
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

	req := registerAccess(t, rt.State, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})
	if _, err := rt.ResolveAccess(approve(req, "copilot")); err != nil {
		t.Fatalf("ResolveAccess: %v", err)
	}
	if got, want := rt.State.AccessState(), (session.AccessState{Mode: "copilot", System: true}); got != want {
		t.Fatalf("granted pair = %+v, want %+v", got, want)
	}

	newState, _, _, _, _, _, _, err := rt.NewSession("")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	want := session.AccessState{Mode: cfg.Agent.ApprovalMode, System: false}
	if got := newState.AccessState(); got != want {
		t.Fatalf("new session pair = %+v, want %+v", got, want)
	}
	if _, err := rt.ResolveAccess(approve(req, "copilot")); err == nil {
		t.Fatal("a retired request ID resolved against the replacement session")
	}
	if got := newState.AccessState(); got != want {
		t.Fatalf("a retired request changed the new session: %+v", got)
	}

	resumed, err := StartRuntime(ctx,
		WithWorkingDir(work),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) { return cfg, nil }),
		WithExistingSession(rt.SessionID),
	)
	if err != nil {
		t.Fatalf("StartRuntime (resumed): %v", err)
	}
	defer resumed.Close(ctx)
	if got := resumed.State.AccessState(); got != want {
		t.Fatalf("resumed session pair = %+v, want %+v", got, want)
	}
}

// Resolution takes the reload gate, so it cannot apply to a runner or state
// that a reload or session swap is replacing, and a grant racing a reload
// still leaves the runner and the session agreeing.
func TestRuntimeAccessSerializesWithReloadAndQuiesce(t *testing.T) {
	cfg := nativeToolAgentConfig("access-serialize")
	rt, state := newAccessTestRuntime(t, cfg)

	req := registerAccess(t, state, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})

	// Hold the reload gate: a resolution must not proceed past it.
	rt.reloadMu.Lock()
	done := make(chan session.AccessResult, 1)
	go func() {
		result, _ := rt.ResolveAccess(approve(req, "copilot"))
		done <- result
	}()
	select {
	case result := <-done:
		rt.reloadMu.Unlock()
		t.Fatalf("ResolveAccess completed while the reload gate was held: %+v", result)
	case <-time.After(150 * time.Millisecond):
		// Expected: parked on the gate.
	}
	rt.reloadMu.Unlock()
	select {
	case result := <-done:
		if result.Status != session.AccessGranted {
			t.Fatalf("status = %q, want %q", result.Status, session.AccessGranted)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ResolveAccess did not complete after the gate was released")
	}
	if got, want := state.AccessState(), (session.AccessState{Mode: "copilot", System: true}); got != want {
		t.Fatalf("session pair = %+v, want %+v", got, want)
	}

	// A grant and a reload racing each other must leave the runner and the
	// session agreeing, whichever order they land in.
	raceCfg := nativeToolAgentConfig("access-serialize")
	raceRT, raceState := newAccessTestRuntime(t, raceCfg)
	raceReq := registerAccess(t, raceState, "call-1", session.AccessIntent{Editing: true, System: true, Reason: "edit and read outside"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = raceRT.ResolveAccess(approve(raceReq, "copilot"))
	}()
	go func() {
		defer wg.Done()
		_ = reloadAgentRuntime(context.Background(), raceCfg, raceRT)
	}()
	wg.Wait()

	effective := raceState.AccessState()
	if got := string(raceRT.Runner.Policy.ApprovalMode()); got != effective.Mode {
		t.Fatalf("runner policy mode %q disagrees with the session mode %q after a concurrent reload", got, effective.Mode)
	}
	if got := raceRT.Runner.ForceClass; got != forceClassForMode(effective.Mode) {
		t.Fatalf("runner force class %q disagrees with the session mode %q after a concurrent reload", got, effective.Mode)
	}
}

// ── Task 8: authoritative conversation-end phases ─────────────────────────

// endTestRuntime builds a minimal runtime around a state and (optionally) a
// command service, with a logger that discards output.
func endTestRuntime(t *testing.T, state *session.State, service *native.JobManager) *Runtime {
	t.Helper()
	workCtx, workCancel := context.WithCancel(context.Background())
	t.Cleanup(workCancel)
	rt := &Runtime{
		Config:     config.Default(),
		State:      state,
		workCtx:    workCtx,
		workCancel: workCancel,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if service != nil {
		rt.CommandService = &conversationCommandService{manager: service, generation: 1}
		rt.JobManager = service
		state.SetTurnStopService(service)
		service.SetWorkAdmission(state)
	}
	return rt
}

// blockingEndRunner blocks until its context ends, publishing a PID so a stopped
// execution is observable.
type blockingEndRunner struct {
	started chan struct{}
	once    sync.Once
}

func (r *blockingEndRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if req.OnStart != nil {
		req.OnStart(4242)
	}
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return native.CommandResult{}, ctx.Err()
}

// endWorkIDs renders a snapshot's ordinary IDs: exactly what a warning panel
// displays and what an acceptance submits back.
func endWorkIDs(work ActiveWork) []string {
	ids := make([]string, 0, len(work.Items))
	for _, item := range work.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// immediateEndRunner completes every command at once with the given stdout, so a
// test can settle a completion through the durable exit sink without a real process.
type immediateEndRunner struct{ out string }

func newImmediateRunner(out string) *immediateEndRunner { return &immediateEndRunner{out: out} }

func (r *immediateEndRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if req.OnStart != nil {
		req.OnStart(5252)
	}
	if req.Stdout != nil && r.out != "" {
		_, _ = req.Stdout.Write([]byte(r.out))
	}
	return native.CommandResult{Stdout: r.out, ExitCode: 0}, nil
}

// TestActiveWorkReportsParentChildAndCommandMembership is Task 8's membership
// check: the snapshot a warning renders holds the parent operation, the
// delegated child and the managed command, each with its own distinct opaque ID,
// and a finished item leaves it.
func TestActiveWorkReportsParentChildAndCommandMembership(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	runner := &blockingEndRunner{started: make(chan struct{})}
	service := native.NewCommandService(ctx, native.LaunchPolicy{Runner: runner, MaxOutputBytes: 1 << 16})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	rt := endTestRuntime(t, state, service)

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	if err := state.EnterSubagent(); err != nil {
		t.Fatalf("EnterSubagent: %v", err)
	}
	if _, err := service.Launch(ctx, native.LaunchSpec{Command: "sleep 30"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-runner.started

	work := rt.ActiveWork()
	if len(work.Items) != 3 {
		t.Fatalf("ActiveWork items = %d (%+v), want parent, child and command", len(work.Items), work.Items)
	}
	kinds := map[string]int{}
	ids := map[string]bool{}
	for _, item := range work.Items {
		kinds[item.Kind]++
		if item.ID == "" {
			t.Fatalf("item %+v has no stable ID", item)
		}
		if ids[item.ID] {
			t.Fatalf("duplicate work ID %q in %+v", item.ID, work.Items)
		}
		ids[item.ID] = true
	}
	for _, want := range []string{string(session.WorkKindParent), string(session.WorkKindChild), string(session.WorkKindCommand)} {
		if kinds[want] != 1 {
			t.Fatalf("kind %q appears %d times in %+v", want, kinds[want], work.Items)
		}
	}
	if len(work.Targets) == 0 {
		t.Fatal("ActiveWork.Targets is empty with active work")
	}

	// A finished child leaves the active membership.
	state.ExitSubagent()
	work = rt.ActiveWork()
	for _, item := range work.Items {
		if item.Kind == string(session.WorkKindChild) {
			t.Fatalf("finished child still active: %+v", item)
		}
	}
	state.EndWork()
}

// TestAcceptEndRejectsExpansionWithoutStoppingAnything pins shared contract 7's
// refresh path: when a currently active ID was not displayed, acceptance changes
// nothing and stops nothing, and ordinary admission stays open.
func TestAcceptEndRejectsExpansionWithoutStoppingAnything(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	runner := &blockingEndRunner{started: make(chan struct{})}
	service := native.NewCommandService(ctx, native.LaunchPolicy{Runner: runner, MaxOutputBytes: 1 << 16})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	rt := endTestRuntime(t, state, service)

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	displayed := rt.ActiveWork()
	if len(displayed.Items) != 1 {
		t.Fatalf("displayed items = %d, want 1", len(displayed.Items))
	}
	// New work appears between the snapshot and the confirmation: a command is
	// admitted after the user saw the warning.
	if _, err := service.Launch(ctx, native.LaunchSpec{Command: "new work"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-runner.started

	decision, err := rt.AcceptEnd(EndAcceptRequest{
		Intent:           EndIntentExit,
		DisplayedIDs:     endWorkIDs(displayed),
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if decision.Accepted {
		t.Fatal("acceptance with an undisplayed active ID must not be accepted")
	}
	if len(decision.Expanded) != 1 {
		t.Fatalf("Expanded = %v, want exactly the one new item", decision.Expanded)
	}
	// The expanded ID is the new work's own stable ID — the same one a refreshed
	// snapshot reports — so a client can mark what changed.
	known := map[string]bool{}
	for _, item := range rt.ActiveWork().Items {
		known[item.ID] = true
	}
	if !known[decision.Expanded[0]] {
		t.Fatalf("Expanded %q is not an active snapshot ID", decision.Expanded[0])
	}
	// Nothing was stopped and admission is still open.
	if state.WorkPhase() != session.ExitPhaseOpen {
		t.Fatalf("phase after a refused acceptance = %q, want open", state.WorkPhase())
	}
	if _, err := service.Launch(ctx, native.LaunchSpec{Command: "still admit"}); err != nil {
		t.Fatalf("admission must stay open after a refused acceptance: %v", err)
	}
	state.EndWork()
}

// TestAcceptEndIgnoresFinishedDisplayedIDs pins the other half of contract 7:
// finished IDs need no reconfirmation, because the membership checked is the one
// that exists NOW.
func TestAcceptEndIgnoresFinishedDisplayedIDs(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	ids := endWorkIDs(rt.ActiveWork())
	// The displayed operation finishes while the warning is open.
	state.EndWork()

	decision, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: ids, RequireDisplayed: true})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !decision.Accepted {
		t.Fatalf("finished displayed IDs must need no reconfirmation: %+v", decision)
	}
	if !decision.Work.Empty() {
		t.Fatalf("snapshot after the work finished = %+v, want empty", decision.Work)
	}
}

// TestAcceptEndFreezesAdmissionAtomicallyAndRefusesNewWork pins step 3's freeze:
// a full displayed set freezes ordinary admission, and work that tries to start
// afterwards is refused rather than hiding behind the freeze.
func TestAcceptEndFreezesAdmissionAtomicallyAndRefusesNewWork(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	service := native.NewCommandService(ctx, native.LaunchPolicy{Runner: &blockingEndRunner{started: make(chan struct{})}, MaxOutputBytes: 1 << 16})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	rt := endTestRuntime(t, state, service)

	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	ids := endWorkIDs(rt.ActiveWork())
	decision, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: ids, RequireDisplayed: true})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !decision.Accepted {
		t.Fatalf("acceptance refused: %+v", decision)
	}
	if !rt.EndAccepted() || rt.EndPhase() != EndPhaseStopping {
		t.Fatalf("phase after acceptance = %q (accepted=%v), want stopping", rt.EndPhase(), rt.EndAccepted())
	}

	// A new command cannot be admitted: the freeze is at the service.
	if _, err := service.Launch(ctx, native.LaunchSpec{Command: "late"}); !errors.Is(err, session.ErrConversationEnding) {
		t.Fatalf("launch after acceptance = %v, want ErrConversationEnding", err)
	}
	// Nor can a new parent operation or child.
	if err := state.BeginWork(); !errors.Is(err, session.ErrConversationEnding) {
		t.Fatalf("BeginWork after acceptance = %v, want ErrConversationEnding", err)
	}
	if err := state.EnterSubagent(); !errors.Is(err, session.ErrConversationEnding) {
		t.Fatalf("EnterSubagent after acceptance = %v, want ErrConversationEnding", err)
	}

	// A second acceptance is a no-op that stops nothing again.
	again, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: ids, RequireDisplayed: true})
	if err != nil {
		t.Fatalf("second AcceptEnd: %v", err)
	}
	if !again.Accepted {
		t.Fatal("second acceptance of the same membership should stay accepted")
	}
	state.EndWork()
}

// TestAcceptEndToleratesUnknownIDsWhenNotRequired pins the noninteractive form: a
// caller that does not require displayed membership freezes exactly what it finds,
// so headless shutdown never blocks on a warning nobody can answer.
func TestAcceptEndToleratesUnknownIDsWhenNotRequired(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)
	if err := state.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	decision, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: []string{"not-a-real-id"}})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !decision.Accepted {
		t.Fatalf("noninteractive acceptance must freeze what it found: %+v", decision)
	}
	state.EndWork()
}

// TestWaitForEndStopsAndJoinsWorkBeforeReturning pins step 3: acceptance stops
// only still-active targets and joins the parent operation and the command before
// reporting settlement.
func TestWaitForEndStopsAndJoinsWorkBeforeReturning(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	runner := &blockingEndRunner{started: make(chan struct{})}
	service := native.NewCommandService(ctx, native.LaunchPolicy{Generation: 1, Runner: runner, MaxOutputBytes: 1 << 16})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	rt := endTestRuntime(t, state, service)

	// One parent operation whose own work context is what the end must cancel.
	parentCtx, cancelParent := context.WithCancel(ctx)
	defer cancelParent()
	if err := state.BeginWorkCancelable(cancelParent); err != nil {
		t.Fatalf("BeginWorkCancelable: %v", err)
	}
	joined := make(chan struct{})
	go func() {
		<-parentCtx.Done()
		state.EndWork()
		close(joined)
	}()

	// A live command owned by the conversation's service.
	if _, err := service.Launch(ctx, native.LaunchSpec{Command: "sleep 30"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-runner.started

	snap := rt.ActiveWork()
	var sawParent bool
	for _, item := range snap.Items {
		if item.Parent {
			sawParent = true
		}
	}
	if !sawParent {
		t.Fatalf("no parent item in %+v", snap.Items)
	}
	decision, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: endWorkIDs(snap), RequireDisplayed: true})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !decision.Accepted {
		t.Fatalf("acceptance refused: %+v", decision)
	}

	waitCtx, cancelWait := context.WithTimeout(ctx, 10*time.Second)
	defer cancelWait()
	if err := rt.WaitForEnd(waitCtx); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}

	select {
	case <-joined:
	default:
		t.Fatal("WaitForEnd returned before the parent operation was joined")
	}
	// The command was stopped and settled: nothing is active any more.
	if active := service.ActiveCount(); active != 0 {
		t.Fatalf("active executions after settlement = %d, want 0", active)
	}
	if phase := rt.EndPhase(); phase != EndPhasePostmortem {
		t.Fatalf("phase after a successful exit settlement = %q, want postmortem", phase)
	}
	if err := rt.LastEndError(); err != nil {
		t.Fatalf("LastEndError = %v, want nil after a successful join", err)
	}
}

// TestWaitForEndReportsOutstandingWorkAndPermitsRetry pins step 6's failed-join
// contract: a join that misses its deadline reports the outstanding work and leaves
// success unlatched, so the caller can try again.
func TestWaitForEndReportsOutstandingWorkAndPermitsRetry(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)

	if err := rt.State.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	// Hold the work open with no cancel hook, so the end pass has to WAIT for it.
	release := make(chan struct{})
	go func() {
		<-release
		state.EndWork()
	}()

	ids := endWorkIDs(rt.ActiveWork())
	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: ids, RequireDisplayed: true}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}

	shortCtx, cancelShort := context.WithTimeout(ctx, 100*time.Millisecond)
	joinErr := rt.WaitForEnd(shortCtx)
	cancelShort()
	if joinErr == nil {
		t.Fatal("WaitForEnd with an outstanding operation must report a failed join")
	}
	if err := rt.LastEndError(); err == nil {
		t.Fatal("LastEndError is nil after a failed join")
	}
	// Success is not latched: the phase has not advanced past stopping, and a
	// retry can still succeed.
	if phase := rt.EndPhase(); phase != EndPhaseStopping {
		t.Fatalf("phase after a failed join = %q, want stopping", phase)
	}
	close(release)

	retryCtx, cancelRetry := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRetry()
	if err := rt.WaitForEnd(retryCtx); err != nil {
		t.Fatalf("retry WaitForEnd: %v", err)
	}
	if err := rt.LastEndError(); err != nil {
		t.Fatalf("LastEndError after a successful retry = %v, want nil", err)
	}
	if phase := rt.EndPhase(); phase != EndPhasePostmortem {
		t.Fatalf("phase after a successful retry = %q, want postmortem", phase)
	}
}

// TestWaitForEndResolvesPendingApprovalSoTheJoinCanFinish pins step 3's pending-
// decision resolution: a turn parked on an approval must be released, or the join
// could never complete an operation the user already agreed to stop.
func TestWaitForEndResolvesPendingApprovalSoTheJoinCanFinish(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)

	parentCtx, cancelParent := context.WithCancel(ctx)
	defer cancelParent()
	_ = parentCtx
	if err := state.BeginWorkCancelable(cancelParent); err != nil {
		t.Fatalf("BeginWorkCancelable: %v", err)
	}
	// The operation is parked on a pending approval when the end is accepted.
	state.SetPendingApproval(&session.PendingToolCall{
		Name:         "file.write",
		Command:      "write a file",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	state.EndWork()
	cancelParent()

	ids := endWorkIDs(rt.ActiveWork())
	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: ids, RequireDisplayed: true}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	if err := rt.WaitForEnd(waitCtx); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}
	if got := state.PendingApproval(); got != nil {
		t.Fatalf("pending approval survived settlement: %+v", got)
	}
}

// TestRunExitAnalysisIsAdmittedOnlyDuringPostmortem pins shared contract 8 and
// step 5: scoped exit analysis is refused before the postmortem phase, and when it
// runs its commands inherit the epoch and are joined before it returns.
func TestRunExitAnalysisIsAdmittedOnlyDuringPostmortem(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	service := native.NewCommandService(ctx, native.LaunchPolicy{Runner: &blockingEndRunner{started: make(chan struct{})}, MaxOutputBytes: 1 << 16})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	rt := endTestRuntime(t, state, service)

	// Before any end is accepted there is no postmortem phase at all.
	rt.SetExitAnalysisOperation(func(context.Context, *session.ExitAnalysisScope, string) error {
		t.Fatal("analysis operation must not run outside the postmortem phase")
		return nil
	})
	if err := rt.RunExitAnalysis(ctx, "summarise the session"); err == nil {
		t.Fatal("RunExitAnalysis before any end was accepted must fail")
	}

	// Accept an exit with no active work so the settlement is immediate, then run
	// the analysis while it launches a scoped command.
	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: endWorkIDs(rt.ActiveWork()), RequireDisplayed: true}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, 5*time.Second)
	if err := rt.WaitForEnd(settleCtx); err != nil {
		cancelSettle()
		t.Fatalf("WaitForEnd: %v", err)
	}
	cancelSettle()
	if phase := rt.EndPhase(); phase != EndPhasePostmortem {
		t.Fatalf("phase = %q, want postmortem", phase)
	}

	var scopedLaunches int
	rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
		if scope == nil || scope.ID() == "" {
			return errors.New("analysis ran without a scope")
		}
		// Ordinary admission stays frozen while analysis runs: a context that
		// does NOT carry the scope is refused.
		if _, err := service.Launch(ctx, native.LaunchSpec{Command: "ordinary"}); !errors.Is(err, session.ErrConversationEnding) {
			return fmt.Errorf("ordinary launch during postmortem = %v, want ErrConversationEnding", err)
		}
		// Scoped work inherits the epoch and is admitted.
		if _, err := service.Launch(analysisCtx, native.LaunchSpec{Command: "scoped analysis command"}); err != nil {
			return fmt.Errorf("scoped launch: %w", err)
		}
		scopedLaunches++
		if got := service.ActiveCount(); got != 1 {
			return fmt.Errorf("active executions during analysis = %d, want 1", got)
		}
		return nil
	})
	if err := rt.RunExitAnalysis(ctx, "summarise the session"); err != nil {
		t.Fatalf("RunExitAnalysis: %v", err)
	}
	if scopedLaunches != 1 {
		t.Fatalf("scoped launches = %d, want 1", scopedLaunches)
	}
	// The epoch was stopped and joined before RunExitAnalysis returned.
	if active := service.ActiveCount(); active != 0 {
		t.Fatalf("active executions after analysis cleanup = %d, want 0", active)
	}
	// The epoch is closed, so a stale authorization cannot admit work again.
	if _, err := state.OpenExitAnalysis(); err != nil {
		t.Fatalf("reopening a fresh epoch after cleanup = %v, want nil", err)
	}
}

// TestRunExitAnalysisIsRefusedWhileAnotherEpochIsOpen pins the single-epoch rule:
// a second analysis cannot start while one is running.
func TestRunExitAnalysisIsRefusedWhileAnotherEpochIsOpen(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)

	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: endWorkIDs(rt.ActiveWork()), RequireDisplayed: true}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, 5*time.Second)
	if err := rt.WaitForEnd(settleCtx); err != nil {
		cancelSettle()
		t.Fatalf("WaitForEnd: %v", err)
	}
	cancelSettle()

	// Hold an epoch open by hand: the second call must be refused, not queued.
	scope, err := state.OpenExitAnalysis()
	if err != nil {
		t.Fatalf("OpenExitAnalysis: %v", err)
	}
	defer state.CloseExitAnalysis(scope)
	if err := rt.RunExitAnalysis(ctx, "second analysis"); !errors.Is(err, session.ErrExitAnalysisAlreadyOpen) {
		t.Fatalf("RunExitAnalysis with an open epoch = %v, want ErrExitAnalysisAlreadyOpen", err)
	}
}

// TestRunExitAnalysisCleansUpOnFailure pins the failure path: an analysis that
// fails still stops and joins its epoch, so nothing outlives the phase.
func TestRunExitAnalysisCleansUpOnFailure(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	service := native.NewCommandService(ctx, native.LaunchPolicy{Runner: &blockingEndRunner{started: make(chan struct{})}, MaxOutputBytes: 1 << 16})
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	rt := endTestRuntime(t, state, service)

	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: endWorkIDs(rt.ActiveWork()), RequireDisplayed: true}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, 5*time.Second)
	if err := rt.WaitForEnd(settleCtx); err != nil {
		cancelSettle()
		t.Fatalf("WaitForEnd: %v", err)
	}
	cancelSettle()

	analysisErr := errors.New("analysis failed")
	var launched bool
	rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
		if _, err := service.Launch(analysisCtx, native.LaunchSpec{Command: "scoped", Detached: true}); err != nil {
			return fmt.Errorf("scoped launch: %w", err)
		}
		launched = true
		return analysisErr
	})
	if err := rt.RunExitAnalysis(ctx, "goal"); !errors.Is(err, analysisErr) {
		t.Fatalf("RunExitAnalysis error = %v, want the analysis failure", err)
	}
	if !launched {
		t.Fatal("the analysis operation never ran")
	}
	if active := service.ActiveCount(); active != 0 {
		t.Fatalf("active executions after a failed analysis = %d, want 0", active)
	}
}

// TestAcceptEndReplacementOmitsPostmortem pins contract 8's replacement rule: an
// accepted replacement advances straight past the postmortem phase, so no exit
// analysis can start on the conversation being replaced.
func TestAcceptEndReplacementOmitsPostmortem(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)

	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentReplace, DisplayedIDs: endWorkIDs(rt.ActiveWork()), RequireDisplayed: true}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	joinCtx, cancelJoin := context.WithTimeout(ctx, 5*time.Second)
	defer cancelJoin()
	if err := rt.WaitForEnd(joinCtx); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}
	if phase := rt.EndPhase(); phase == EndPhasePostmortem {
		t.Fatal("a replacement must not enter the postmortem phase")
	}
	rt.SetExitAnalysisOperation(func(context.Context, *session.ExitAnalysisScope, string) error {
		t.Fatal("a replacement must not run exit analysis")
		return nil
	})
	if err := rt.RunExitAnalysis(ctx, "goal"); err == nil {
		t.Fatal("RunExitAnalysis after a replacement acceptance must fail")
	}
}

// TestQuiesceReusesStopAndJoinIdempotently pins step 6 for the noninteractive
// path: Quiesce and Close run the same stop/join pass, so a second call neither
// stops anything again nor reports the first call's outcome as a new settlement.
func TestQuiesceReusesStopAndJoinIdempotently(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	runner := &blockingEndRunner{started: make(chan struct{})}
	service := native.NewCommandService(ctx, native.LaunchPolicy{Generation: 1, Runner: runner, MaxOutputBytes: 1 << 16})
	rt := endTestRuntime(t, state, service)

	if _, err := service.Launch(ctx, native.LaunchSpec{Command: "sleep 30"}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-runner.started

	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	if active := service.ActiveCount(); active != 0 {
		t.Fatalf("active executions after Quiesce = %d, want 0", active)
	}
	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("second Quiesce: %v", err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close after Quiesce: %v", err)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestQuiesceDisablesWatchAutoResume pins step 4's no-prompt rule: an ending
// conversation must not start a fresh turn from a fired watch, and the hook is
// cleared without asking the user anything.
func TestQuiesceDisablesWatchAutoResume(t *testing.T) {
	ctx := context.Background()
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	rt := endTestRuntime(t, state, nil)
	rt.WatchResume = &WatchResumeHook{}

	var resumed bool
	rt.WatchResume.Set(func(watch.Report) { resumed = true })
	if err := rt.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	rt.WatchResume.Invoke(watch.Report{})
	if resumed {
		t.Fatal("watch auto-resume ran after Quiesce: an ending conversation must not start work")
	}
}

// TestEndSettlementStoresDurableOutputBeforeClose pins step 6's ordering rule for
// the whole runtime: a detached completion reached by the end settlement is written
// durably while the database is still open, so extraction sees final evidence and the
// close path never loses it.
func TestEndSettlementStoresDurableOutputBeforeClose(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "end.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "end-settlement")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	const sessionID = "sess_end"
	if err := database.CreateSession(sessionID, projectID, "", time.Now()); err != nil {
		database.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(config.Default(), dir, time.Now(), session.Persistence{
		DB:        database,
		SessionID: sessionID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { database.Close() })

	service := native.NewCommandService(ctx, native.LaunchPolicy{Runner: newImmediateRunner("final-tail-marker\n"), MaxOutputBytes: 1 << 16})
	service.SetOnExit(func(info native.JobInfo, ran time.Duration, output string) error {
		return state.AddJobExit(session.JobExitFromExecution(info.Execution(), info.Command, output, ran, info.ExitCode))
	})
	rt := endTestRuntime(t, state, service)

	// One detached command, completed and reported through the exit sink.
	jobID := launchDetachedJob(t, ctx, service, "echo final-tail-marker")
	waitForJobStatus(t, service, jobID, native.StatusCompleted, 5*time.Second)

	// The end settlement runs the durable finalization while the DB is open.
	if _, err := rt.AcceptEnd(EndAcceptRequest{Intent: EndIntentExit, DisplayedIDs: endWorkIDs(rt.ActiveWork())}); err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	settleCtx, cancelSettle := context.WithTimeout(ctx, 10*time.Second)
	defer cancelSettle()
	if err := rt.WaitForEnd(settleCtx); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}

	// The completion is durable: a fresh load of the session's stored exits sees
	// the final output.
	records, err := database.LoadJobExits(sessionID)
	if err != nil {
		t.Fatalf("LoadJobExits: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("stored job exits = %d (%+v), want the one completion", len(records), records)
	}
	if !strings.Contains(records[0].Output, "final-tail-marker") {
		t.Fatalf("stored output = %q, want the command's final tail", records[0].Output)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ── Task 12: lifecycle composition regressions ───────────────────────────

// lifecycleRuntimeFixture builds a runtime over a real database and a real
// command service for the composition tests: a session with durable job exits, a
// managed execution service whose completions are stored through the session,
// and a runtime whose end phases drive all of it.
//
// It composes the production pieces (session → service → runtime) directly
// rather than through StartRuntime, because these tests are about the lifecycle
// those pieces agree on, not about startup configuration.
type lifecycleRuntimeFixture struct {
	rt       *Runtime
	state    *session.State
	service  *native.JobManager
	database *db.DB
	dir      string
	// sessionID is the conversation the fixture's state and service own.
	sessionID string
	// releases are the runners' release channels, closed exactly once at cleanup
	// so no test leaks a blocked goroutine — including a test that released one
	// itself.
	releases []chan struct{}
	// closed tracks the release channels already closed, so cleanup never closes
	// one twice.
	closedMu sync.Mutex
	closed   map[chan struct{}]bool
}

// release closes a release channel exactly once.
func (f *lifecycleRuntimeFixture) release(ch chan struct{}) {
	f.closedMu.Lock()
	defer f.closedMu.Unlock()
	if f.closed == nil {
		f.closed = make(map[chan struct{}]bool)
	}
	if f.closed[ch] {
		return
	}
	f.closed[ch] = true
	close(ch)
}

func newLifecycleRuntimeFixture(t *testing.T, runner native.CommandRunner, maxOutputBytes int) *lifecycleRuntimeFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "lifecycle.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "lifecycle-composition")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	const sessionID = "sess_lifecycle"
	if err := database.CreateSession(sessionID, projectID, "", time.Now()); err != nil {
		database.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(config.Default(), dir, time.Now(), session.Persistence{
		DB:        database,
		SessionID: sessionID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	f := &lifecycleRuntimeFixture{state: state, database: database, dir: dir, sessionID: sessionID}
	t.Cleanup(func() {
		for _, ch := range f.releases {
			f.release(ch)
		}
		if f.service != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = f.service.Shutdown(shutdownCtx)
			cancel()
		}
		_ = f.rt.Close(context.Background())
		_ = database.Close()
	})

	service := native.NewCommandService(ctx, native.LaunchPolicy{
		Generation:     1,
		Runner:         runner,
		Dir:            dir,
		MaxOutputBytes: maxOutputBytes,
		Retention:      time.Hour,
	})
	service.SetOnExit(func(info native.JobInfo, ran time.Duration, output string) error {
		return state.AddJobExit(session.JobExitFromExecution(info.Execution(), info.Command, output, ran, info.ExitCode))
	})
	service.SetOnChange(state.SetRunningJobsCount)
	f.service = service

	f.rt = &Runtime{
		Config:         config.Default(),
		State:          state,
		CommandService: &conversationCommandService{manager: service, generation: 1},
		JobManager:     service,
		DB:             database,
		ProjectID:      projectID,
		SessionID:      sessionID,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		WorkingDir:     dir,
		HomeDir:        dir,
		DataDir:        dir,
	}
	workCtx, workCancel := context.WithCancel(ctx)
	f.rt.workCtx = workCtx
	f.rt.workCancel = workCancel
	state.SetTurnStopService(service)
	service.SetWorkAdmission(state)
	return f
}

// blockingLifecycleRunner blocks every execution until its own context is
// cancelled, publishing a PID so a stopped execution is observable. It is the
// stand-in for a long command that a stop has to terminate.
type blockingLifecycleRunner struct {
	started chan struct{}
	once    sync.Once
	pid     int
	// runs counts executions that reached the runner, so a test can assert one
	// launch across a handoff and a reload.
	runs atomic.Int32
}

func (r *blockingLifecycleRunner) runCount() int { return int(r.runs.Load()) }

func (r *blockingLifecycleRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	r.runs.Add(1)
	if req.OnStart != nil {
		pid := r.pid
		if pid == 0 {
			pid = 6100
		}
		req.OnStart(pid)
	}
	r.once.Do(func() {
		if r.started != nil {
			close(r.started)
		}
	})
	<-ctx.Done()
	return native.CommandResult{}, ctx.Err()
}

// lifecycleJobExits returns the session's durable detached completions.
func lifecycleJobExits(state *session.State) []session.JobExit {
	var out []session.JobExit
	for _, item := range state.Transcript() {
		if item.JobExit != nil {
			out = append(out, *item.JobExit)
		}
	}
	return out
}

// TestEndSettlementComposesPostmortemEvidenceAndDatabaseClose is Task 12's
// accepted-warning chain at the runtime boundary: work is warned about and
// accepted, the settlement stores the stopped command's terminal output durably,
// the postmortem phase runs with the database still open so extraction sees that
// evidence, and only then does Close actually close it.
//
// It exercises the composition the interactive path relies on (accept → join →
// postmortem → close) with a real database and a real command service, so a
// regression in any single step makes one of the assertions below fail.
func TestEndSettlementComposesPostmortemEvidenceAndDatabaseClose(t *testing.T) {
	ctx := context.Background()
	runner := &blockingLifecycleRunner{started: make(chan struct{}), pid: 6200}
	f := newLifecycleRuntimeFixture(t, runner, 1<<16)
	rt, state, service := f.rt, f.state, f.service

	// One parent operation, so the warning has something to show besides the
	// command, and one command the accepted end has to stop. The parent
	// registers through BeginWorkCancelable with its own stop hook — the runtime's
	// registration seam — so the accepted end stops exactly this operation instead
	// of the runtime's infrastructure root, and the join can complete.
	parentCtx, cancelParent := context.WithCancel(ctx)
	defer cancelParent()
	if err := state.BeginWorkCancelable(cancelParent); err != nil {
		t.Fatalf("BeginWorkCancelable: %v", err)
	}
	joined := make(chan struct{})
	go func() {
		<-parentCtx.Done()
		state.EndWork()
		close(joined)
	}()

	jobID := launchDetachedJob(t, ctx, service, "sleep 30")
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the command never started")
	}

	// The warning's membership covers both, then the user accepts it.
	snap := rt.ActiveWork()
	if len(snap.Items) != 2 {
		t.Fatalf("ActiveWork items = %+v, want the parent operation and the command", snap.Items)
	}
	decision, err := rt.AcceptEnd(EndAcceptRequest{
		Intent:           EndIntentExit,
		DisplayedIDs:     endWorkIDs(snap),
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !decision.Accepted {
		t.Fatalf("acceptance refused: %+v", decision)
	}
	if phase := rt.EndPhase(); phase != EndPhaseStopping {
		t.Fatalf("phase after acceptance = %q, want stopping", phase)
	}

	settleCtx, cancelSettle := context.WithTimeout(ctx, 15*time.Second)
	defer cancelSettle()
	if err := rt.WaitForEnd(settleCtx); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}
	if phase := rt.EndPhase(); phase != EndPhasePostmortem {
		t.Fatalf("phase after settlement = %q, want postmortem", phase)
	}
	if err := rt.LastEndError(); err != nil {
		t.Fatalf("LastEndError = %v, want nil after a successful settlement", err)
	}
	if active := service.ActiveCount(); active != 0 {
		t.Fatalf("active executions after settlement = %d, want 0", active)
	}
	if got := runner.runCount(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1: the accepted end stops the command, it does not relaunch it", got)
	}

	// The stopped command's terminal output is durable, and it is stored as a
	// stopped outcome rather than as a natural completion.
	exits := lifecycleJobExits(state)
	if len(exits) != 1 {
		t.Fatalf("job exits = %d (%+v), want the one stopped completion", len(exits), exits)
	}
	if exits[0].ID != jobID {
		t.Fatalf("stored exit id = %q, want the stopped command %q", exits[0].ID, jobID)
	}
	if exits[0].Outcome != string(native.OutcomeStopped) || exits[0].StopCause != "conversation ended" {
		t.Fatalf("stored exit = %q/%q, want stopped/conversation ended", exits[0].Outcome, exits[0].StopCause)
	}
	if stored, err := f.database.LoadJobExits(f.sessionID); err != nil {
		t.Fatalf("LoadJobExits: %v", err)
	} else if len(stored) != 1 {
		t.Fatalf("stored rows = %d, want the settled completion", len(stored))
	}

	// The postmortem phase runs the scoped analysis with the database still
	// open: the extraction it performs observes the stored completion above,
	// which is exactly the evidence a report needs.
	var sawEvidence bool
	rt.SetExitAnalysisOperation(func(analysisCtx context.Context, scope *session.ExitAnalysisScope, goal string) error {
		if scope == nil {
			return errors.New("analysis ran without a scope")
		}
		records, err := f.database.LoadJobExits(f.sessionID)
		if err != nil {
			return fmt.Errorf("read stored completions during the postmortem: %w", err)
		}
		if len(records) == 1 && records[0].Outcome == string(native.OutcomeStopped) {
			sawEvidence = true
		}
		return nil
	})
	if err := rt.RunExitAnalysis(ctx, "summarise the session"); err != nil {
		t.Fatalf("RunExitAnalysis: %v", err)
	}
	if !sawEvidence {
		t.Fatal("the postmortem pass could not read the settlement's durable evidence")
	}

	// The replacement/closure step closes the database; afterwards its own
	// connection is unusable, which is how "the DB close followed the durable
	// write" is observable from outside.
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := f.database.LoadJobExits(f.sessionID); err == nil {
		t.Fatal("the database was still usable after Close: the write must precede the close")
	}
	if phase := rt.EndPhase(); phase != EndPhasePostmortem && phase != EndPhaseClosing {
		t.Fatalf("phase after Close = %q, want the postmortem/closing phase the runtime reached", phase)
	}
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("the parent operation was never joined")
	}
}

// TestReplacementLeavesNoWorkBehind is Task 12's replacement rule: an accepted
// replacement stops the old conversation's work, advances past the postmortem
// phase, and the new session owns its own supervisor — no command, callback or
// execution from the replaced conversation survives into it.
func TestReplacementLeavesNoWorkBehind(t *testing.T) {
	ctx := context.Background()
	runner := &blockingLifecycleRunner{started: make(chan struct{}), pid: 6300}
	f := newLifecycleRuntimeFixture(t, runner, 1<<16)
	rt, state, service := f.rt, f.state, f.service

	// The replaced conversation's own operation, registered with its stop hook,
	// so the accepted replacement is what ends it.
	parentCtx, cancelParent := context.WithCancel(ctx)
	defer cancelParent()
	if err := state.BeginWorkCancelable(cancelParent); err != nil {
		t.Fatalf("BeginWorkCancelable: %v", err)
	}
	stopped := make(chan struct{})
	go func() {
		<-parentCtx.Done()
		state.EndWork()
		close(stopped)
	}()

	jobID := launchDetachedJob(t, ctx, service, "old-conversation-command")
	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the command never started")
	}

	decision, err := rt.AcceptEnd(EndAcceptRequest{
		Intent:           EndIntentReplace,
		DisplayedIDs:     endWorkIDs(rt.ActiveWork()),
		RequireDisplayed: true,
	})
	if err != nil {
		t.Fatalf("AcceptEnd: %v", err)
	}
	if !decision.Accepted {
		t.Fatalf("replacement refused: %+v", decision)
	}

	joinCtx, cancelJoin := context.WithTimeout(ctx, 15*time.Second)
	defer cancelJoin()
	if err := rt.WaitForEnd(joinCtx); err != nil {
		t.Fatalf("WaitForEnd: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the accepted replacement never stopped the old conversation's operation")
	}
	if phase := rt.EndPhase(); phase == EndPhasePostmortem {
		t.Fatal("a replacement must not enter the postmortem phase")
	}
	// The old command is gone with its conversation.
	if active := service.ActiveCount(); active != 0 {
		t.Fatalf("active executions after the replacement = %d, want 0", active)
	}
	if got := runner.runCount(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1: the replacement stops the old command, it does not relaunch it", got)
	}
	settled, _, err := service.Output(jobID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !settled.Status.Terminal() {
		t.Fatalf("old command status = %q, want a settled state", settled.Status)
	}
	if settled.Outcome != native.OutcomeStopped {
		t.Fatalf("old command outcome = %q, want stopped", settled.Outcome)
	}
	// The replacement leaves no ordinary work registered behind it.
	if outstanding := state.OutstandingWork(); len(outstanding) != 0 {
		t.Fatalf("outstanding work after the replacement = %+v, want none", outstanding)
	}
	// A replacement adds no exit postmortem, so scoped analysis cannot start.
	if err := rt.RunExitAnalysis(ctx, "must not run"); err == nil {
		t.Fatal("a replacement must not run exit analysis")
	}
}

// TestTargetedChildStopKeepsThatChildsDetachedJob is Task 12's targeted-stop
// rule composed end to end: stopping one delegated child stops the commands that
// child still has attached, while a job the same child already handed off
// remains conversation-owned and running.
func TestTargetedChildStopKeepsThatChildsDetachedJob(t *testing.T) {
	ctx := context.Background()
	releaseDetached := make(chan struct{})
	attachedStarted := make(chan struct{})
	runner := &childStopRunner{releaseDetached: releaseDetached, attachedStarted: attachedStarted}
	f := newLifecycleRuntimeFixture(t, runner, 1<<16)
	f.releases = append(f.releases, releaseDetached)
	rt, state, service := f.rt, f.state, f.service

	// The child: its creation item, its card, and its own stop group — the same
	// three registrations the agent.run handler makes.
	childCtx, cancelChild := context.WithCancel(ctx)
	work, err := state.EnterSubagentWork()
	if err != nil {
		t.Fatalf("EnterSubagentWork: %v", err)
	}
	view := state.RegisterSubagentWithMetaAndWork(work, "delegate", nil, session.SubagentMeta{})
	childStop := session.NewTurnStopToken(service, fmt.Sprintf("subagent-%d", view.ID))
	state.RegisterSubagentStopGroup(view.ID, childStop)
	state.SetSubagentCancel(view.ID, cancelChild)

	// The child's detached job: admitted under the child's turn, then handed off.
	childTurn := childStop.TurnID()
	detached, err := service.Launch(ctx, native.LaunchSpec{
		Command:  "child-download",
		Detached: true,
		Origin:   native.CommandOrigin{ConversationID: state.SessionID(), Actor: "delegate", TurnID: childTurn},
	})
	if err != nil {
		t.Fatalf("Launch detached: %v", err)
	}
	// The child's attached command: what the targeted stop must terminate.
	attached, err := service.Launch(ctx, native.LaunchSpec{
		Command: "child-attached",
		Origin:  native.CommandOrigin{ConversationID: state.SessionID(), Actor: "delegate", TurnID: childTurn},
	})
	if err != nil {
		t.Fatalf("Launch attached: %v", err)
	}
	select {
	case <-attachedStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the child's commands never started")
	}

	// The user stops that child specifically.
	if !state.CancelSubagent(view.ID) {
		t.Fatal("CancelSubagent did not stop the running child")
	}
	// The child's own context is cancelled by that stop: the targeted stop ends
	// the child itself, not only its command.
	select {
	case <-childCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the targeted child stop never cancelled the child's context")
	}

	// The child's attached command is stopped...
	stopped, _, err := service.Output(attached.ID, 0)
	if err != nil {
		t.Fatalf("Output attached: %v", err)
	}
	if stopped.Status == native.StatusRunning {
		t.Fatalf("the child's attached command is still %q, want it stopped", stopped.Status)
	}
	if stopped.StopCause == "" {
		t.Fatal("the stopped command recorded no cause")
	}
	// ...while the child's own detached job keeps running: it is
	// conversation-owned, and a targeted child stop reaches the child's attached
	// work only.
	kept, _, err := service.Output(detached.ID, 0)
	if err != nil {
		t.Fatalf("Output detached: %v", err)
	}
	if kept.Status != native.StatusRunning {
		t.Fatalf("the child's detached job status = %q, want running", kept.Status)
	}
	if kept.StopCause != "" {
		t.Fatalf("the child's detached job StopCause = %q, want none", kept.StopCause)
	}

	// The conversation's own work is unaffected: the child's detached job is
	// still part of the membership an end would stop.
	snap := rt.ActiveWork()
	var sawDetached bool
	for _, item := range snap.Items {
		if item.Kind == string(session.WorkKindCommand) && item.Label == "child-download" {
			sawDetached = true
		}
	}
	if !sawDetached {
		t.Fatalf("membership = %+v, want the child's detached job still tracked", snap.Items)
	}

	// Finishing the child leaves its detached job running exactly one more step:
	// an accepted conversation end is what eventually stops it.
	state.FinishSubagent(view.ID, "done", nil)
	state.ClearSubagentStopGroup(view.ID)
	state.ExitSubagent()
	alive, _, err := service.Output(detached.ID, 0)
	if err != nil {
		t.Fatalf("Output detached after the child finished: %v", err)
	}
	if alive.Status != native.StatusRunning {
		t.Fatalf("detached job after the child finished = %q, want running", alive.Status)
	}
	f.release(releaseDetached)
	settled, err := service.Wait(ctx, detached.ID)
	if err != nil {
		t.Fatalf("Wait detached: %v", err)
	}
	if settled.Status != native.StatusCompleted {
		t.Fatalf("detached job settled as %q, want completed", settled.Status)
	}
}

// childStopRunner models one child's two commands: the detached one blocks until
// released (so it can outlive the child's stop), the attached one blocks until
// its context is cancelled (so a stop terminates it) and publishes its start.
type childStopRunner struct {
	releaseDetached chan struct{}
	attachedStarted chan struct{}
	once            sync.Once
}

func (r *childStopRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if req.OnStart != nil {
		req.OnStart(6400)
	}
	if req.Command == "child-download" {
		select {
		case <-r.releaseDetached:
			return native.CommandResult{Stdout: "download complete\n", ExitCode: 0}, nil
		case <-ctx.Done():
			return native.CommandResult{}, ctx.Err()
		}
	}
	r.once.Do(func() { close(r.attachedStarted) })
	<-ctx.Done()
	return native.CommandResult{}, ctx.Err()
}

// ── Send now: runtime ownership, reload and replacement ──────────────────

// sendNowHeldRunner is a CommandRunner whose execution reports a PID, prints a
// marker and then blocks until it is released (a natural completion) or its
// context is cancelled (a stop). It stands in for a long ordinary command
// without spawning a real process, so a Send now interruption test is exact and
// hermetic. It satisfies native.CommandRunner.
type sendNowHeldRunner struct {
	release chan struct{}
}

func (r *sendNowHeldRunner) Run(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
	if req.OnStart != nil {
		req.OnStart(4242)
	}
	if req.Stdout != nil {
		_, _ = req.Stdout.Write([]byte("send-now-marker\n"))
	}
	select {
	case <-r.release:
		return native.CommandResult{Stdout: "released\n", ExitCode: 0}, nil
	case <-ctx.Done():
		return native.CommandResult{}, ctx.Err()
	}
}

// commitHeldRunner installs a controlled blocking runner as the conversation
// manager's launch defaults, so every command this test admits runs the fake
// process rather than a real one. It mirrors the fixture the reload tests use.
func commitHeldRunner(t *testing.T, jm *native.JobManager, held *sendNowHeldRunner) {
	t.Helper()
	jm.CommitLaunchPolicy(native.LaunchPolicy{
		Generation:     jm.Generation(),
		Runner:         held,
		Dir:            t.TempDir(),
		MaxOutputBytes: 1 << 20,
		MaxJobs:        25,
		Retention:      time.Hour,
	})
}

// waitForJobOutput polls until the execution has printed marker, failing the
// test on timeout. A marker means the process really started, so the assertion
// that follows is about a running command rather than a launch race.
func waitForJobOutput(t *testing.T, jm *native.JobManager, id, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, out, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%s): %v", id, err)
		}
		if strings.Contains(out, marker) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never printed %q (output %q)", id, marker, out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// newSendNowTestRuntime starts a full runtime whose conversation has the
// production Send now wiring: the owning build installs the interruption
// handler on the state and binds it to the conversation-owned command manager.
// The provider is a scripted local fixture, the auth resolver is memory-backed,
// and every directory is a temp dir, so nothing reaches the developer's config,
// keychain or model.
func newSendNowTestRuntime(t *testing.T, cfg config.Config) *Runtime {
	t.Helper()
	cfg.Skills.Autoload = nil
	auth, _ := memoryAuthResolver(t)
	rt, err := StartRuntime(context.Background(),
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
	if rt.Runner == nil {
		t.Fatal("the fixture provider was not built")
	}
	if rt.JobManager == nil {
		t.Fatal("the conversation command manager was not built")
	}
	return rt
}

// TestRuntimeSendNowUsesCapturedConversationManager pins the ownership of the
// interruption handler: the state's handler is bound to the conversation's own
// manager, so the operation a Send now request captures reaches exactly that
// manager's attached execution and nothing else. The same turn keeps admitting
// the NEXT operation, because only the interrupted operation's admission
// closes.
func TestRuntimeSendNowUsesCapturedConversationManager(t *testing.T) {
	ctx := context.Background()
	rt := newSendNowTestRuntime(t, nativeToolAgentConfig("send-now-captured-manager"))
	state := rt.State
	mgr := rt.JobManager

	// The owning build installed the conversation's interruption handler; no
	// turn is active yet, so the snapshot reports supported but inactive.
	if snap := state.SteeringSnapshot(); !snap.Supported || snap.Active {
		t.Fatalf("startup snapshot = %+v, want supported and inactive", snap)
	}

	held := &sendNowHeldRunner{release: make(chan struct{})}
	commitHeldRunner(t, mgr, held)

	turnID, err := state.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	opCtx, ref, err := state.BeginSteeringOperation(ctx, turnID, session.OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation: %v", err)
	}
	info, err := mgr.Launch(opCtx, native.LaunchSpec{
		Command:   "held-attached",
		Operation: ref,
		Origin:    native.CommandOrigin{TurnID: turnID, ToolCallID: "call-1"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitForJobOutput(t, mgr, info.ID, "send-now-marker")

	receipt, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: state.SessionID(),
		TurnID:    turnID,
		RequestID: "req-1",
		Text:      "stop this",
	})
	if err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	if receipt.Status != session.SteeringAccepted || receipt.OperationID != ref.ID || receipt.TurnID != turnID {
		t.Fatalf("receipt = %+v, want accepted for operation %s on turn %s", receipt, ref.ID, turnID)
	}

	// The exact manager receives the operation: its attached execution settles
	// as an explicit stop, never as a natural completion.
	settled := waitForJobStatus(t, mgr, info.ID, native.StatusKilled, 10*time.Second)
	if settled.Outcome != native.OutcomeStopped || settled.StopCause == "" {
		t.Fatalf("settled = %q/%q, want an explicit stop", settled.Outcome, settled.StopCause)
	}

	// The interrupted operation is joined, then the SAME turn admits its next
	// operation and command.
	state.FinishSteeringOperation(ref, nil)
	batch, err := state.StageSteering(turnID)
	if err != nil {
		t.Fatalf("StageSteering: %v", err)
	}
	ids := make([]string, 0, len(batch.Messages))
	for _, m := range batch.Messages {
		ids = append(ids, m.ID)
	}
	state.MarkSteeringSubmitted(turnID, ids)

	_, nextRef, err := state.BeginSteeringOperation(ctx, turnID, session.OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation (next): %v", err)
	}
	if nextRef.ID == ref.ID {
		t.Fatal("the replacement operation reused the interrupted operation's ID")
	}
	next, err := mgr.Launch(ctx, native.LaunchSpec{
		Command:   "same-turn-next",
		Operation: nextRef,
		Origin:    native.CommandOrigin{TurnID: turnID},
	})
	if err != nil {
		t.Fatalf("Launch (next operation in the same turn): %v", err)
	}
	close(held.release)
	if got := waitForJobStatus(t, mgr, next.ID, native.StatusCompleted, 10*time.Second); got.Outcome != native.OutcomeCompleted {
		t.Fatalf("next command outcome = %q, want completed", got.Outcome)
	}

	state.EndSteeringTurn(turnID, "test complete")
}

// TestRuntimeSendNowDoesNotSaveReloadOrReplaceManagers pins the boundary of the
// runtime delegation: accepting a request and reading a snapshot are
// memory-only. Neither invokes the config reloader, saves config, replaces the
// service pointers, moves the launch generation nor touches the session.
func TestRuntimeSendNowDoesNotSaveReloadOrReplaceManagers(t *testing.T) {
	ctx := context.Background()
	rt := newSendNowTestRuntime(t, nativeToolAgentConfig("send-now-no-rebuild"))
	state := rt.State

	var reloads atomic.Int64
	rt.ConfigReloader = func(config.Config) error {
		reloads.Add(1)
		return nil
	}

	svcBefore := rt.CommandService
	mgrBefore := rt.JobManager
	runnerBefore := rt.Runner
	stateBefore := rt.State
	sessionBefore := rt.SessionID
	maxJobsBefore := rt.Config.Tools.Shell.MaxBackgroundJobs
	genBefore := mgrBefore.Generation()

	turnID, err := state.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	if _, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: state.SessionID(),
		TurnID:    turnID,
		RequestID: "req-1",
		Text:      "boundary",
	}); err != nil {
		t.Fatalf("SubmitSendNow: %v", err)
	}
	if snap := rt.SteeringSnapshot(); !snap.Active || !snap.Supported || snap.TurnID != turnID {
		t.Fatalf("snapshot = %+v, want the live supported turn", snap)
	}

	if n := reloads.Load(); n != 0 {
		t.Fatalf("send now invoked the config reloader %d times", n)
	}
	if rt.CommandService != svcBefore {
		t.Fatal("send now replaced the command service")
	}
	if rt.JobManager != mgrBefore {
		t.Fatal("send now replaced the command manager")
	}
	if rt.Runner != runnerBefore {
		t.Fatal("send now replaced the runner")
	}
	if rt.State != stateBefore {
		t.Fatal("send now replaced the session state")
	}
	if rt.SessionID != sessionBefore {
		t.Fatal("send now changed the session id")
	}
	if rt.Config.Tools.Shell.MaxBackgroundJobs != maxJobsBefore {
		t.Fatal("send now mutated the runtime config")
	}
	if got := mgrBefore.Generation(); got != genBefore {
		t.Fatalf("send now moved the launch generation: %d -> %d", genBefore, got)
	}

	state.EndSteeringTurn(turnID, "test complete")
}

// TestRuntimeSendNowReloadRetainsOperationAndReceipts pins that a config reload
// inside one conversation borrows what the conversation already owns: the
// state, the eligible turn, the open operation token, the interruption handler
// and the command manager all survive, so the post-reload Send now still
// reaches the pre-reload manager's attached command and the request ledger
// stays consistent.
func TestRuntimeSendNowReloadRetainsOperationAndReceipts(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("send-now-reload")
	rt := newSendNowTestRuntime(t, cfg)
	state := rt.State

	held := &sendNowHeldRunner{release: make(chan struct{})}
	commitHeldRunner(t, rt.JobManager, held)

	turnID, err := state.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	_, ref, err := state.BeginSteeringOperation(ctx, turnID, session.OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation: %v", err)
	}
	info, err := rt.JobManager.Launch(ctx, native.LaunchSpec{
		Command:   "held-across-reload",
		Operation: ref,
		Origin:    native.CommandOrigin{TurnID: turnID},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitForJobOutput(t, rt.JobManager, info.ID, "send-now-marker")

	mgr := rt.JobManager
	genBefore := mgr.Generation()

	reloaded := cfg
	reloaded.Tools.Shell.MaxBackgroundJobs = cfg.Tools.Shell.MaxBackgroundJobs + 3
	if err := reloadAgentRuntime(ctx, reloaded, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	if rt.JobManager != mgr {
		t.Fatal("the reload replaced the conversation manager")
	}
	if got := mgr.Generation(); got <= genBefore {
		t.Fatalf("generation after reload = %d, want a newer one than %d", got, genBefore)
	}
	snap := state.SteeringSnapshot()
	if !snap.Active || !snap.Supported || snap.TurnID != turnID || snap.OperationID != ref.ID {
		t.Fatalf("snapshot after reload = %+v, want turn %s, operation %s and a handler preserved", snap, turnID, ref.ID)
	}

	// The post-reload request still captures the preserved operation and the
	// preserved handler still reaches the pre-reload manager's command.
	receipt, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: state.SessionID(),
		TurnID:    turnID,
		RequestID: "req-keep",
		Text:      "across the reload",
	})
	if err != nil {
		t.Fatalf("SubmitSendNow after reload: %v", err)
	}
	if receipt.OperationID != ref.ID || receipt.TurnID != turnID {
		t.Fatalf("receipt after reload = %+v, want the preserved operation %s", receipt, ref.ID)
	}
	settled := waitForJobStatus(t, mgr, info.ID, native.StatusKilled, 10*time.Second)
	if settled.Outcome != native.OutcomeStopped {
		t.Fatalf("command across the reload settled as %q, want an explicit stop", settled.Outcome)
	}

	// The request ledger survived: retrying the same request ID does not add a
	// second queue entry, and it names the same turn and operation.
	again, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: state.SessionID(),
		TurnID:    turnID,
		RequestID: "req-keep",
		Text:      "across the reload",
	})
	if err != nil {
		t.Fatalf("retry after reload: %v", err)
	}
	if again.RequestID != "req-keep" || again.TurnID != turnID {
		t.Fatalf("retry receipt = %+v, want the retained request", again)
	}
	if q := state.SteeringQueue(); len(q) != 1 || q[0] != "across the reload" {
		t.Fatalf("queue after the retry = %v, want exactly the one accepted entry", q)
	}

	state.EndSteeringTurn(turnID, "test complete")
}

// TestRuntimeSendNowReplacementRejectsOldBinding pins the replacement boundary:
// NewSession retires the old conversation (its handler's manager is closed and
// the conversation is frozen), so an old request can never stop or seed the
// replacement, while the replacement's own state carries its own handler bound
// to its own manager.
func TestRuntimeSendNowReplacementRejectsOldBinding(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("send-now-replacement")
	cfg.Skills.Autoload = nil
	rt := newSendNowTestRuntime(t, cfg)

	oldState := rt.State
	oldMgr := rt.JobManager

	oldTurn, err := oldState.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	if _, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: oldState.SessionID(),
		TurnID:    oldTurn,
		RequestID: "old-1",
		Text:      "old work",
	}); err != nil {
		t.Fatalf("pre-replacement SubmitSendNow: %v", err)
	}

	newState, _, _, _, _, _, _, err := rt.NewSession("replacement")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if newState == oldState {
		t.Fatal("NewSession did not replace the session")
	}
	if rt.JobManager == oldMgr {
		t.Fatal("the replacement did not get its own command manager")
	}
	if !newState.SteeringSnapshot().Supported {
		t.Fatal("the replacement state carries no interruption handler")
	}

	// The retired conversation's binding cannot reach new work: its manager is
	// closed, and the conversation is frozen against new registrations.
	if _, err := oldMgr.Launch(ctx, native.LaunchSpec{Command: "echo after-replacement"}); !errors.Is(err, native.ErrJobManagerClosed) {
		t.Fatalf("the old conversation's manager still admits work: %v", err)
	}
	if err := oldState.BeginWork(); !errors.Is(err, session.ErrSessionQuiescing) {
		t.Fatalf("old BeginWork = %v, want ErrSessionQuiescing", err)
	}

	// An old request routed through the runtime names the NEW conversation's
	// state, whose turn identity differs, so it is refused and mutates nothing.
	if _, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: oldState.SessionID(),
		TurnID:    oldTurn,
		RequestID: "old-2",
		Text:      "late",
	}); err == nil {
		t.Fatal("an old request was accepted against the replacement conversation")
	}
	if q := newState.SteeringQueue(); len(q) != 0 {
		t.Fatalf("an old request mutated the replacement's queue: %v", q)
	}
	if snap := newState.SteeringSnapshot(); snap.Active {
		t.Fatalf("an old request opened a turn on the replacement: %+v", snap)
	}

	// The replacement's own handler serves a fresh turn normally.
	newTurn, err := newState.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("replacement BeginSteeringTurn: %v", err)
	}
	receipt, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: newState.SessionID(),
		TurnID:    newTurn,
		RequestID: "new-1",
		Text:      "new work",
	})
	if err != nil {
		t.Fatalf("replacement SubmitSendNow: %v", err)
	}
	if receipt.Status != session.SteeringAccepted || receipt.TurnID != newTurn {
		t.Fatalf("replacement receipt = %+v, want accepted on the new turn", receipt)
	}
	if q := newState.SteeringQueue(); len(q) != 1 || q[0] != "new work" {
		t.Fatalf("replacement queue = %v, want the new work only", q)
	}
	// The old conversation's accepted-but-undelivered steering is preserved on
	// its own state and reported undelivered: teardown never erases work the
	// user explicitly asked to deliver (spec §lifecycle, plan Task 3 step 7).
	// What matters for the replacement boundary is that it never reappears on
	// the replacement — asserted above.
	if q := oldState.SteeringQueue(); len(q) != 1 || q[0] != "old work" {
		t.Fatalf("the retired conversation's accepted steering = %v, want it preserved", q)
	}
	if got := oldState.SteeringSnapshot().PendingCount; got != 1 {
		t.Fatalf("retired conversation pending count = %d, want 1", got)
	}

	newState.EndSteeringTurn(newTurn, "test complete")
}

// TestRuntimeSendNowUnavailableFailsClosed pins the refusal side of the seam:
// a missing state, a missing handler, an idle or mismatched turn all refuse
// before anything is queued or stopped. A refusal leaves the queue exactly as
// it was and never invokes an installed handler.
func TestRuntimeSendNowUnavailableFailsClosed(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("send-now-fail-closed")

	// No session at all: the zero snapshot and a refusal, never a panic.
	empty := &Runtime{}
	if snap := empty.SteeringSnapshot(); snap.Active || snap.Supported || snap.Version != 0 {
		t.Fatalf("nil-state snapshot = %+v, want the zero value", snap)
	}
	if _, err := empty.SubmitSendNow(session.SendNowRequest{RequestID: "r", TurnID: "t"}); err == nil {
		t.Fatal("SubmitSendNow with no session returned no error")
	}

	// A conversation with an installed handler that must never be invoked
	// while the request is ineligible.
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var interrupts atomic.Int64
	state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
		interrupts.Add(1)
		return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
	})
	rt := &Runtime{State: state}

	state.PushSteering("already queued")

	// No active turn: refused before the handler is reached.
	if _, err := rt.SubmitSendNow(session.SendNowRequest{
		SessionID: state.SessionID(),
		TurnID:    "turn-1",
		RequestID: "r1",
		Text:      "never",
	}); err == nil {
		t.Fatal("a request with no active turn was accepted")
	}
	if q := state.SteeringQueue(); len(q) != 1 || q[0] != "already queued" {
		t.Fatalf("a refused request mutated the queue: %v", q)
	}

	// Active turn, but the request names a different session or turn: refused.
	turnID, err := state.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("BeginSteeringTurn: %v", err)
	}
	for _, req := range []session.SendNowRequest{
		{SessionID: "sess-other", TurnID: turnID, RequestID: "r2", Text: "never"},
		{SessionID: state.SessionID(), TurnID: "turn-other", RequestID: "r3", Text: "never"},
		{SessionID: state.SessionID(), TurnID: turnID, RequestID: "", Text: "never"},
	} {
		if _, err := rt.SubmitSendNow(req); err == nil {
			t.Fatalf("SubmitSendNow(%+v) was accepted", req)
		}
	}
	if q := state.SteeringQueue(); len(q) != 1 || q[0] != "already queued" {
		t.Fatalf("a mismatched request mutated the queue: %v", q)
	}
	if n := interrupts.Load(); n != 0 {
		t.Fatalf("an ineligible request invoked the handler %d times", n)
	}
	state.EndSteeringTurn(turnID, "test complete")

	// Active turn and no handler installed: unsupported, refused, and the
	// snapshot reports it as unsupported.
	bare := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	bareTurn, err := bare.BeginSteeringTurn(ctx)
	if err != nil {
		t.Fatalf("bare BeginSteeringTurn: %v", err)
	}
	bareRT := &Runtime{State: bare}
	if _, err := bareRT.SubmitSendNow(session.SendNowRequest{
		SessionID: bare.SessionID(),
		TurnID:    bareTurn,
		RequestID: "r4",
		Text:      "never",
	}); err == nil {
		t.Fatal("SubmitSendNow accepted a request with no interruption handler")
	}
	if snap := bare.SteeringSnapshot(); snap.Supported {
		t.Fatalf("snapshot = %+v, want unsupported with no handler", snap)
	}
	if len(bare.SteeringQueue()) != 0 {
		t.Fatalf("a refused unsupported request queued %v", bare.SteeringQueue())
	}
}

// ── Task 13: conversation work projection and action facade ─────────────────

// newWorkFixture builds an isolated, persisted conversation whose work index
// and job-exit evidence can be stored and read back.
func newWorkFixture(t *testing.T) (*session.State, *db.DB, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "work.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "work-projection")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	const sessionID = "sess_work"
	if err := database.CreateSession(sessionID, projectID, "", time.Now()); err != nil {
		database.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	state := session.New(config.Default(), dir, time.Now(), session.Persistence{
		DB:        database,
		SessionID: sessionID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	t.Cleanup(func() { _ = database.Close() })
	return state, database, sessionID
}

// workClock is a controllable clock so a test can drive the projection's
// throttle and receipt windows without sleeping.
type workClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *workClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *workClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// workTestJobManager builds a command service whose detached completions store
// their durable exit into state, mirroring the production wiring.
func workTestJobManager(t *testing.T, state *session.State, runner native.CommandRunner, dir string) *native.JobManager {
	t.Helper()
	jm := native.NewCommandService(context.Background(), native.LaunchPolicy{
		Runner:         runner,
		Dir:            dir,
		MaxJobs:        200,
		Retention:      time.Hour,
		MaxOutputBytes: 2 << 20,
	})
	if state != nil {
		jm.SetOnExit(func(info native.JobInfo, ran time.Duration, output string) error {
			return state.AddJobExit(session.JobExitFromExecution(info.Execution(), info.Command, output, ran, info.ExitCode))
		})
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jm.Shutdown(ctx)
	})
	return jm
}

func waitWorkUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func workItemBySource(snap viewmodel.WorkSnapshot, kind, sourceID string) (viewmodel.WorkItem, bool) {
	for _, it := range snap.Items {
		if it.Kind == kind && it.SourceID == sourceID {
			return it, true
		}
	}
	return viewmodel.WorkItem{}, false
}

func workItemByKind(snap viewmodel.WorkSnapshot, kind string) (viewmodel.WorkItem, bool) {
	for _, it := range snap.Items {
		if it.Kind == kind {
			return it, true
		}
	}
	return viewmodel.WorkItem{}, false
}

// TestWorkCoordinatorSeedsAndRepairsDroppedEvents pins that the coordinator
// seeds from an initial source snapshot even when no event was observed, and
// that a reconcile on demand repairs a source change no wake announced.
func TestWorkCoordinatorSeedsAndRepairsDroppedEvents(t *testing.T) {
	state, _, sessionID := newWorkFixture(t)
	dir := t.TempDir()
	clock := &workClock{t: time.Unix(1000, 0)}

	started := make(chan struct{})
	var once sync.Once
	runner := &fakeRunner{run: func(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return native.CommandResult{}, ctx.Err()
	}}
	jm := workTestJobManager(t, state, runner, dir)
	if _, err := jm.Launch(context.Background(), native.LaunchSpec{Command: "first", Detached: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	// The coordinator subscribes no broker for jobs here, so only the initial
	// snapshot can seed the first item.
	wc := newWorkCoordinator(context.Background(), state, workSources{Jobs: jm}, clock.now)
	t.Cleanup(wc.close)

	snap := wc.snapshotNow()
	if snap.ConversationID != sessionID {
		t.Fatalf("ConversationID = %q, want %q", snap.ConversationID, sessionID)
	}
	if len(snap.Items) != 1 {
		t.Fatalf("seeded items = %d, want 1: %+v", len(snap.Items), snap.Items)
	}
	if snap.Items[0].Kind != workKindCommand || snap.Items[0].State != workStateRunning {
		t.Fatalf("seeded item = %+v, want a running command", snap.Items[0])
	}
	if !snap.Items[0].Capabilities.Stop {
		t.Fatal("a live detached command should be stoppable")
	}

	// A second launch receives no wake; an on-demand reconcile still repairs the
	// dropped event.
	if _, err := jm.Launch(context.Background(), native.LaunchSpec{Command: "second", Detached: true}); err != nil {
		t.Fatalf("Launch second: %v", err)
	}
	wc.reconcileWork(clock.now())
	if got := len(wc.snapshotNow().Items); got != 2 {
		t.Fatalf("items after repair = %d, want 2", got)
	}
}

// TestWorkCoordinatorAllActiveAndBoundedTerminalDetails pins that every active
// item stays in the snapshot while terminal details are capped, and that the
// omitted results remain pageable.
func TestWorkCoordinatorAllActiveAndBoundedTerminalDetails(t *testing.T) {
	state, _, _ := newWorkFixture(t)
	dir := t.TempDir()
	clock := &workClock{t: time.Unix(1500, 0)}

	runner := &fakeRunner{run: func(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		if req.Command == "blocking" {
			<-ctx.Done()
			return native.CommandResult{}, ctx.Err()
		}
		return native.CommandResult{Stdout: "ok", ExitCode: 0}, nil
	}}
	jm := workTestJobManager(t, state, runner, dir)
	for i := 0; i < 40; i++ {
		if _, err := jm.Launch(context.Background(), native.LaunchSpec{Command: fmt.Sprintf("job-%d", i), Detached: true}); err != nil {
			t.Fatalf("Launch %d: %v", i, err)
		}
	}
	waitWorkUntil(t, func() bool { return jm.RunningCount() == 0 }, "the instant jobs to settle")
	if _, err := jm.Launch(context.Background(), native.LaunchSpec{Command: "blocking", Detached: true}); err != nil {
		t.Fatalf("Launch blocking: %v", err)
	}
	waitWorkUntil(t, func() bool { return jm.RunningCount() == 1 }, "the blocking job to run")

	wc := newWorkCoordinator(context.Background(), state, workSources{Jobs: jm}, clock.now)
	t.Cleanup(wc.close)
	wc.reconcileWork(clock.now())
	snap := wc.snapshotNow()

	active, terminal := 0, 0
	for _, it := range snap.Items {
		if viewmodel.WorkActive(it) {
			active++
		} else if workItemTerminal(it.State) {
			terminal++
		}
	}
	if active != 1 {
		t.Fatalf("active items = %d, want 1 (every active item stays in the snapshot)", active)
	}
	if terminal > workTerminalDetails {
		t.Fatalf("terminal details = %d, want at most %d", terminal, workTerminalDetails)
	}
	page, err := wc.page(viewmodel.WorkQuery{View: "history", Limit: 100})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(page.Items) != 41 {
		t.Fatalf("history page = %d items, want 41 (omitted results stay pageable)", len(page.Items))
	}
}

// TestWorkCoordinatorProgressThrottleAndUrgentSettlement pins that an idle
// poll advances no revision, a cosmetic change publishes at most 5 Hz, and a
// lifecycle change publishes immediately.
func TestWorkCoordinatorProgressThrottleAndUrgentSettlement(t *testing.T) {
	state, _, _ := newWorkFixture(t)
	clock := &workClock{t: time.Unix(2000, 0)}

	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	v := state.RegisterSubagent("child", child)
	child.SetActiveToolCall(session.ActiveToolCall{Name: "file.read"})

	wc := newWorkCoordinator(context.Background(), state, workSources{}, clock.now)
	t.Cleanup(wc.close)
	rev1 := wc.snapshotNow().Revision

	// An idle identical poll advances no revision.
	wc.reconcileWork(clock.now())
	if got := wc.snapshotNow().Revision; got != rev1 {
		t.Fatalf("idle poll revision = %d, want %d", got, rev1)
	}

	// A cosmetic change (the child's activity) inside the throttle window is
	// held back rather than published.
	child.SetActiveToolCall(session.ActiveToolCall{Name: "shell.run"})
	clock.advance(10 * time.Millisecond)
	wc.reconcileWork(clock.now())
	if got := wc.snapshotNow().Revision; got != rev1 {
		t.Fatalf("throttled cosmetic revision = %d, want %d", got, rev1)
	}

	// Once the window passes, the cosmetic change publishes.
	clock.advance(300 * time.Millisecond)
	wc.reconcileWork(clock.now())
	rev2 := wc.snapshotNow().Revision
	if rev2 <= rev1 {
		t.Fatalf("cosmetic revision = %d, want > %d", rev2, rev1)
	}

	// A lifecycle change publishes immediately, even inside the window.
	state.FinishSubagent(v.ID, "done", nil)
	wc.reconcileWork(clock.now())
	rev3 := wc.snapshotNow().Revision
	if rev3 <= rev2 {
		t.Fatalf("urgent settlement revision = %d, want > %d", rev3, rev2)
	}
	it, ok := workItemBySource(wc.snapshotNow(), workKindAgent, strconv.FormatInt(v.ID, 10))
	if !ok || it.State != workStateCompleted {
		t.Fatalf("settled item = %+v, want completed", it)
	}
}

// TestWorkCoordinatorLegacyHistoryCannotStopNewJob pins that legacy command
// history gets no stop capability and cannot reach a live execution that reuses
// its display ID.
func TestWorkCoordinatorLegacyHistoryCannotStopNewJob(t *testing.T) {
	state, _, sessionID := newWorkFixture(t)
	dir := t.TempDir()
	clock := &workClock{t: time.Unix(3000, 0)}

	runner := &fakeRunner{run: func(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		<-ctx.Done()
		return native.CommandResult{}, ctx.Err()
	}}
	jm := workTestJobManager(t, state, runner, dir)
	info, err := jm.Launch(context.Background(), native.LaunchSpec{Command: "live job", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if info.ID != "job-1" {
		t.Fatalf("display id = %q, want job-1", info.ID)
	}
	// A legacy exit shares the display id but carries no execution identity.
	if err := state.AddJobExit(session.JobExit{ID: "job-1", Command: "old job", State: "failed", ExitCode: 1, At: time.Unix(2900, 0)}); err != nil {
		t.Fatalf("AddJobExit: %v", err)
	}

	wc := newWorkCoordinator(context.Background(), state, workSources{Jobs: jm}, clock.now)
	t.Cleanup(wc.close)
	wc.reconcileWork(clock.now())

	snap := wc.snapshotNow()
	var live, legacy *viewmodel.WorkItem
	for i := range snap.Items {
		it := snap.Items[i]
		if it.Kind != workKindCommand {
			continue
		}
		if strings.HasPrefix(it.SourceID, "legacy:") {
			c := it
			legacy = &c
		} else if it.SourceID == info.ExecutionID {
			c := it
			live = &c
		}
	}
	if live == nil {
		t.Fatalf("live job missing from %+v", snap.Items)
	}
	if legacy == nil {
		t.Fatalf("legacy history missing from %+v", snap.Items)
	}
	if !live.Capabilities.Stop {
		t.Fatal("the live job should be stoppable")
	}
	if legacy.Capabilities.Stop {
		t.Fatal("a legacy command must never receive stop capability")
	}
	if legacy.RuntimeEpoch != "legacy" {
		t.Fatalf("legacy epoch = %q, want legacy", legacy.RuntimeEpoch)
	}

	// A stop aimed at the legacy history key must not reach the live job.
	res, err := wc.stop(context.Background(), viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: legacy.TaskKey}, "stop")
	if err != nil {
		t.Fatalf("stop legacy: %v", err)
	}
	if res.Accepted {
		t.Fatalf("legacy stop was accepted: %+v", res)
	}
	now, _, err := jm.Output("job-1", 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if now.Status.Terminal() {
		t.Fatal("the live job was stopped by a legacy-history request")
	}
}

// TestWorkFacadeExactChildAndWatchStop pins that the facade routes an exact
// child stop to that child's cancel and an exact watch stop to that
// registration, and never falls back to a parent cancel.
func TestWorkFacadeExactChildAndWatchStop(t *testing.T) {
	state, _, sessionID := newWorkFixture(t)
	clock := &workClock{t: time.Unix(4000, 0)}

	v := state.RegisterSubagent("child", nil)
	canceled := false
	state.SetSubagentCancel(v.ID, func() { canceled = true })

	wm := watch.NewManager(context.Background(), watch.Deps{
		ConversationID: sessionID,
		RunSample:      func(ctx context.Context, command, dir string) (string, int, error) { return "", 1, nil },
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = wm.Close(ctx)
	})
	watchID, _, err := wm.Start(watch.Spec{
		Name: "watcher", Kind: watch.KindCommand, Command: "true",
		Condition: "exit_code 0", Mode: watch.ModeRepeat, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Start watch: %v", err)
	}

	wc := newWorkCoordinator(context.Background(), state, workSources{Watches: wm}, clock.now)
	t.Cleanup(wc.close)
	rt := &Runtime{State: state, work: wc}

	snap := wc.snapshotNow()
	agent, ok := workItemBySource(snap, workKindAgent, strconv.FormatInt(v.ID, 10))
	if !ok {
		t.Fatalf("agent item missing from %+v", snap.Items)
	}
	watchItem, ok := workItemBySource(snap, workKindWatch, watchID)
	if !ok {
		t.Fatalf("watch item missing from %+v", snap.Items)
	}

	// The exact child stop reaches only that child's cancel.
	res, err := rt.StopWork(context.Background(), viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: agent.TaskKey}, "stop child")
	if err != nil {
		t.Fatalf("StopWork child: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("child stop not accepted: %+v", res)
	}
	if !canceled {
		t.Fatal("the exact child cancel was not invoked")
	}
	// The watch is untouched by the child stop.
	if info, err := wm.Status(watchID); err != nil || !info.Active {
		t.Fatalf("the watch should still be active after a child stop: %+v err=%v", info, err)
	}

	// The exact watch stop reaches only that registration.
	res, err = rt.StopWork(context.Background(), viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: watchItem.TaskKey}, "stop watch")
	if err != nil {
		t.Fatalf("StopWork watch: %v", err)
	}
	if !res.Accepted {
		t.Fatalf("watch stop not accepted: %+v", res)
	}
	if _, err := wm.Status(watchID); err == nil {
		t.Fatal("the watch should have been removed after its stop")
	}
}

// TestWorkFacadeAckFailureAndRevisionCAS pins the acknowledgment revision
// compare-and-swap: a stale revision and a running item are refused, the exact
// revision acknowledges, a repeat is idempotent, and the attention count reacts.
func TestWorkFacadeAckFailureAndRevisionCAS(t *testing.T) {
	state, _, sessionID := newWorkFixture(t)
	clock := &workClock{t: time.Unix(5000, 0)}

	v := state.RegisterSubagent("failing child", nil)
	state.FinishSubagent(v.ID, "boom", errors.New("boom"))
	running := state.RegisterSubagent("running child", nil)

	wc := newWorkCoordinator(context.Background(), state, workSources{}, clock.now)
	t.Cleanup(wc.close)
	rt := &Runtime{State: state, work: wc}

	snap := wc.snapshotNow()
	failed, ok := workItemBySource(snap, workKindAgent, strconv.FormatInt(v.ID, 10))
	if !ok {
		t.Fatalf("failed agent missing from %+v", snap.Items)
	}
	if !viewmodel.WorkNeedsAttention(failed) {
		t.Fatalf("failed agent should need attention: %+v", failed)
	}
	if !failed.Capabilities.Acknowledge {
		t.Fatal("a failed terminal result should be acknowledgeable")
	}
	before := snap.AttentionCount
	if before == 0 {
		t.Fatal("attention count should include the failed result")
	}

	// A revision that names no current terminal result is refused.
	if err := rt.AcknowledgeWork(context.Background(), viewmodel.WorkAck{
		WorkTarget:     viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: failed.TaskKey},
		ResultRevision: "stale-revision",
	}); err == nil {
		t.Fatal("acknowledging a stale revision succeeded")
	}

	// A running item is not acknowledgeable.
	run, ok := workItemBySource(snap, workKindAgent, strconv.FormatInt(running.ID, 10))
	if !ok {
		t.Fatalf("running agent missing from %+v", snap.Items)
	}
	if err := rt.AcknowledgeWork(context.Background(), viewmodel.WorkAck{
		WorkTarget:     viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: run.TaskKey},
		ResultRevision: viewmodel.WorkResultRevision(run),
	}); !errors.Is(err, viewmodel.ErrWorkUnsupported) {
		t.Fatalf("acknowledging a running item err = %v, want ErrWorkUnsupported", err)
	}

	// The exact revision acknowledges, and the repeat is idempotent.
	rev := viewmodel.WorkResultRevision(failed)
	if err := rt.AcknowledgeWork(context.Background(), viewmodel.WorkAck{
		WorkTarget:     viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: failed.TaskKey},
		ResultRevision: rev,
	}); err != nil {
		t.Fatalf("AcknowledgeWork: %v", err)
	}
	if err := rt.AcknowledgeWork(context.Background(), viewmodel.WorkAck{
		WorkTarget:     viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: failed.TaskKey},
		ResultRevision: rev,
	}); err != nil {
		t.Fatalf("repeat AcknowledgeWork: %v", err)
	}
	wc.reconcileWork(clock.now())
	if after := wc.snapshotNow().AttentionCount; after >= before {
		t.Fatalf("attention count = %d, want < %d after acknowledgment", after, before)
	}
}

// TestWorkInspectUsesExactReportAndBoundedOutput pins that inspection resolves
// an agent's exact persisted report and returns a bounded, safe command output.
func TestWorkInspectUsesExactReportAndBoundedOutput(t *testing.T) {
	state, _, sessionID := newWorkFixture(t)
	dir := t.TempDir()
	clock := &workClock{t: time.Unix(6000, 0)}

	v := state.RegisterSubagent("reporter", nil)
	report := fmt.Sprintf("[subagent %d finished] exact report body", v.ID)
	state.AddMessage(session.RoleUser, report, session.ContentTypeSubagentReport)
	if err := state.BindSubagentReport(v.ID, report); err != nil {
		t.Fatalf("BindSubagentReport: %v", err)
	}
	state.FinishSubagent(v.ID, report, nil)

	big := strings.Repeat("abcdefgh", 6000) // 48 KiB
	runner := &fakeRunner{run: func(ctx context.Context, req native.CommandRequest) (native.CommandResult, error) {
		return native.CommandResult{Stdout: big, ExitCode: 0}, nil
	}}
	jm := workTestJobManager(t, state, runner, dir)
	if _, err := jm.Launch(context.Background(), native.LaunchSpec{Command: "loud", Detached: true}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	waitWorkUntil(t, func() bool { return jm.RunningCount() == 0 }, "the loud job to settle")

	wc := newWorkCoordinator(context.Background(), state, workSources{Jobs: jm}, clock.now)
	t.Cleanup(wc.close)
	rt := &Runtime{State: state, work: wc}
	wc.reconcileWork(clock.now())
	snap := wc.snapshotNow()

	agent, ok := workItemBySource(snap, workKindAgent, strconv.FormatInt(v.ID, 10))
	if !ok {
		t.Fatalf("agent item missing from %+v", snap.Items)
	}
	detail, err := rt.InspectWork(context.Background(), viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: agent.TaskKey}, false)
	if err != nil {
		t.Fatalf("InspectWork agent: %v", err)
	}
	if detail.Report != report {
		t.Fatalf("report = %q, want %q", detail.Report, report)
	}
	if detail.Unavailable != "" {
		t.Fatalf("a persisted report must be available, got %q", detail.Unavailable)
	}

	cmd, ok := workItemByKind(snap, workKindCommand)
	if !ok {
		t.Fatalf("command item missing from %+v", snap.Items)
	}
	detail, err = rt.InspectWork(context.Background(), viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: cmd.TaskKey}, true)
	if err != nil {
		t.Fatalf("InspectWork command raw: %v", err)
	}
	if detail.Output == "" {
		t.Fatal("raw command output is empty")
	}
	if len(detail.Output) > workInspectBytes {
		t.Fatalf("raw output = %d bytes, want <= %d", len(detail.Output), workInspectBytes)
	}
	if !detail.Raw {
		t.Fatal("detail.Raw should echo the raw request")
	}

	// An unknown key is a stale target, never a fabricated detail.
	if _, err := rt.InspectWork(context.Background(), viewmodel.WorkTarget{ConversationID: sessionID, TaskKey: "work:nope"}, false); !errors.Is(err, viewmodel.ErrWorkStaleTarget) {
		t.Fatalf("inspect unknown err = %v, want ErrWorkStaleTarget", err)
	}
}

// TestWorkRuntimeReloadRetainsExecutionIdentity pins that a config reload keeps
// the conversation's projection and its execution identity: the coordinator
// and its epoch survive, a live child keeps its task key and stop capability,
// and the replacement watch manager is rebound only after the old one is joined
// and reconciled, so a registration on the replacement carries its own epoch.
func TestWorkRuntimeReloadRetainsExecutionIdentity(t *testing.T) {
	ctx := context.Background()
	rt := newSendNowTestRuntime(t, nativeToolAgentConfig("work-reload-identity"))

	before := rt.WorkSnapshot()
	if before.RuntimeEpoch == "" {
		t.Fatalf("startup snapshot has no projection epoch: %+v", before)
	}
	if before.ConversationID != rt.State.SessionID() {
		t.Fatalf("snapshot conversation = %q, want %q", before.ConversationID, rt.State.SessionID())
	}

	// A live child is execution identity the projection owns.
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	v := rt.State.RegisterSubagent("worker", child)
	source := strconv.FormatInt(v.ID, 10)
	waitWorkUntil(t, func() bool {
		_, ok := workItemBySource(rt.WorkSnapshot(), workKindAgent, source)
		return ok
	}, "the live child to appear in the projection")
	item, ok := workItemBySource(rt.WorkSnapshot(), workKindAgent, source)
	if !ok {
		t.Fatal("child missing before reload")
	}
	keyBefore := item.TaskKey
	oldWatchMgr := rt.WatchManager

	if err := reloadAgentRuntime(ctx, rt.State.Config, rt); err != nil {
		t.Fatalf("reloadAgentRuntime: %v", err)
	}

	after := rt.WorkSnapshot()
	if after.ConversationID != before.ConversationID {
		t.Fatalf("reload changed the conversation: %q -> %q", before.ConversationID, after.ConversationID)
	}
	if after.RuntimeEpoch != before.RuntimeEpoch {
		t.Fatalf("reload minted a new projection epoch %q -> %q; execution identity was not retained", before.RuntimeEpoch, after.RuntimeEpoch)
	}
	item, ok = workItemBySource(after, workKindAgent, source)
	if !ok {
		t.Fatalf("the live child left the projection across a reload: %+v", after.Items)
	}
	if item.TaskKey != keyBefore {
		t.Fatalf("child task key changed across reload: %q -> %q", keyBefore, item.TaskKey)
	}
	if !item.Capabilities.Stop {
		t.Fatalf("the live child lost its stop capability across reload: %+v", item)
	}

	// The watch manager is replaced, and the projection is rebound to the new
	// pointer: a registration on the replacement is owned under its own epoch.
	if rt.WatchManager == oldWatchMgr {
		t.Fatal("reload did not replace the watch manager")
	}
	watchID, _, err := rt.WatchManager.Start(watch.Spec{
		Name: "post-reload", Kind: watch.KindCommand, Command: "true",
		Condition: "exit_code 0", Mode: watch.ModeRepeat, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("start watch on the replacement: %v", err)
	}
	waitWorkUntil(t, func() bool {
		it, ok := workItemBySource(rt.WorkSnapshot(), workKindWatch, watchID)
		return ok && it.RuntimeEpoch != before.RuntimeEpoch
	}, "the replacement's watch to appear under its own epoch")
	if _, err := rt.WatchManager.Stop(watchID); err != nil {
		t.Fatalf("stop post-reload watch: %v", err)
	}
	// Release the live child so the runtime drains cleanly at cleanup.
	rt.State.FinishSubagent(v.ID, "done", nil)
}

// TestWorkRuntimeReplacementCannotReceiveOldEpoch pins that replacing the
// conversation closes the old projection and starts a fresh one: the
// replacement never reuses the old epoch, carries none of its items, and starts
// with no attention or receipt counters. The old coordinator is closed, not
// repointed at the replacement.
func TestWorkRuntimeReplacementCannotReceiveOldEpoch(t *testing.T) {
	rt := newSendNowTestRuntime(t, nativeToolAgentConfig("work-replacement-epoch"))

	// The old conversation carries attention and a completed receipt.
	failed := rt.State.RegisterSubagent("failed", nil)
	rt.State.FinishSubagent(failed.ID, "boom", errors.New("boom"))
	done := rt.State.RegisterSubagent("finished", nil)
	rt.State.FinishSubagent(done.ID, "ok", nil)
	waitWorkUntil(t, func() bool {
		s := rt.WorkSnapshot()
		return s.AttentionCount > 0 && (s.SuccessReceiptCount > 0 || s.StoppedReceiptCount > 0)
	}, "the old conversation's attention and receipt counters")

	old := rt.WorkSnapshot()
	oldEpoch := old.RuntimeEpoch
	oldConv := old.ConversationID
	oldWork := rt.currentWork()
	if oldWork == nil {
		t.Fatal("startup did not install a work coordinator")
	}

	newState, _, _, _, _, _, _, err := rt.NewSession("")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if rt.currentWork() == oldWork {
		t.Fatal("the replacement reused the replaced conversation's coordinator")
	}
	snap := rt.WorkSnapshot()
	if snap.ConversationID != newState.SessionID() || snap.ConversationID == oldConv {
		t.Fatalf("replacement conversation = %q, want %q (not %q)", snap.ConversationID, newState.SessionID(), oldConv)
	}
	if snap.RuntimeEpoch == oldEpoch {
		t.Fatalf("the replacement reused the old projection epoch %q", oldEpoch)
	}
	if snap.AttentionCount != 0 || snap.SuccessReceiptCount != 0 || snap.StoppedReceiptCount != 0 {
		t.Fatalf("replacement counters = attention %d, success %d, stopped %d; want all zero",
			snap.AttentionCount, snap.SuccessReceiptCount, snap.StoppedReceiptCount)
	}
	for _, it := range snap.Items {
		if it.RuntimeEpoch == oldEpoch {
			t.Fatalf("the replacement's snapshot carries an item from the old epoch: %+v", it)
		}
	}
	// The replaced projection still holds the old conversation's own facts.
	if got := oldWork.snapshotNow(); got.ConversationID != oldConv || got.AttentionCount == 0 {
		t.Fatalf("the replaced coordinator was mutated: %+v", got)
	}
}

// TestWorkTUIFacadeReplacementUsesNewConversation pins that the single
// viewmodel.WorkService the TUI is handed (the *Runtime itself, via
// tui.WithWork) follows an explicit session replacement. The same pointer the
// TUI holds delegates to the replacement conversation's coordinator, so every
// facade read the renderer makes switches to the new conversation's identity
// and epoch, and a read bound to the retired conversation is refused as stale
// rather than silently served from the old projection.
func TestWorkTUIFacadeReplacementUsesNewConversation(t *testing.T) {
	rt := newSendNowTestRuntime(t, nativeToolAgentConfig("work-tui-facade-replacement"))

	// The TUI is handed the runtime pointer itself as its Work service.
	var svc viewmodel.WorkService = rt

	// Give the old conversation a terminal work item, so a projection that
	// leaked across the replacement would be observable through the facade.
	failed := rt.State.RegisterSubagent("old-work", nil)
	rt.State.FinishSubagent(failed.ID, "boom", errors.New("boom"))
	waitWorkUntil(t, func() bool { return rt.WorkSnapshot().AttentionCount > 0 }, "the old conversation's work item")

	oldSnap := svc.WorkSnapshot()
	oldConv := oldSnap.ConversationID
	oldEpoch := oldSnap.RuntimeEpoch
	if oldConv == "" || oldEpoch == "" {
		t.Fatalf("the facade reported no conversation: %+v", oldSnap)
	}

	newState, _, _, _, _, _, _, err := rt.NewSession("")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	// The SAME facade now names the replacement conversation on a fresh epoch,
	// with no item carried over from the retired conversation.
	snap := svc.WorkSnapshot()
	if snap.ConversationID != newState.SessionID() || snap.ConversationID == oldConv {
		t.Fatalf("facade conversation after replacement = %q, want %q (not %q)", snap.ConversationID, newState.SessionID(), oldConv)
	}
	if snap.RuntimeEpoch == oldEpoch {
		t.Fatalf("the facade reused the replaced conversation's epoch %q", oldEpoch)
	}
	for _, it := range snap.Items {
		if it.ConversationID != newState.SessionID() {
			t.Fatalf("the facade exposes an old-conversation item after replacement: %+v", it)
		}
	}

	// List through the same facade: a page bound to the retired conversation is
	// refused as stale, and the replacement's page carries the new epoch.
	if _, err := svc.ListWork(context.Background(), viewmodel.WorkQuery{
		ConversationID: oldConv,
		View:           "attention",
	}); !errors.Is(err, viewmodel.ErrWorkStaleTarget) {
		t.Fatalf("ListWork against the replaced conversation = %v, want ErrWorkStaleTarget", err)
	}
	page, err := svc.ListWork(context.Background(), viewmodel.WorkQuery{
		ConversationID: newState.SessionID(),
		View:           "attention",
	})
	if err != nil {
		t.Fatalf("ListWork against the replacement: %v", err)
	}
	if page.RuntimeEpoch != snap.RuntimeEpoch {
		t.Fatalf("ListWork page epoch = %q, want the replacement epoch %q", page.RuntimeEpoch, snap.RuntimeEpoch)
	}
}

// TestWorkRuntimeCloseFlushesBeforeDBClose pins that a terminal watch save whose
// first durable write failed is retried by the shutdown path and is durable
// before the database closes.
func TestWorkRuntimeCloseFlushesBeforeDBClose(t *testing.T) {
	state, database, sessionID := newWorkFixture(t)

	var calls atomic.Int32
	transient := errors.New("transient terminal-sink failure")
	wm := watch.NewManager(context.Background(), watch.Deps{
		ConversationID: sessionID,
		RunSample:      func(context.Context, string, string) (string, int, error) { return "", 0, nil },
		OnTerminal: func(info watch.Info) error {
			if calls.Add(1) == 1 {
				return transient
			}
			return state.SaveWatchWorkResult(watchWorkResult(sessionID, info))
		},
	})
	watchID, _, err := wm.Start(watch.Spec{
		Name: "flush", Kind: watch.KindCommand, Command: "true",
		Condition: "exit_code 0", Mode: watch.ModeRepeat, Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Start watch: %v", err)
	}
	// A deliberate stop is a terminal transition; its first durable save fails.
	if _, err := wm.Stop(watchID); err != nil {
		t.Fatalf("Stop watch: %v", err)
	}
	if calls.Load() < 1 {
		t.Fatal("the terminal sink was never invoked")
	}
	if got := state.WorkView(); len(got.WatchResults) != 0 {
		t.Fatalf("the failed first save should leave nothing durable: %+v", got.WatchResults)
	}

	wc := newWorkCoordinator(context.Background(), state, workSources{Watches: wm}, time.Now)
	rt := &Runtime{
		Config:       config.Default(),
		State:        state,
		DB:           database,
		WatchManager: wm,
		work:         wc,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		workCtx:      context.Background(),
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The retry ran before the database closed: the result is durable, and the
	// database is now closed.
	if got := state.WorkView(); len(got.WatchResults) != 1 {
		t.Fatalf("work view = %+v, want the flushed watch result", got.WatchResults)
	}
	if err := database.SaveWorkView(sessionID, db.WorkViewRecord{Version: 1}); err == nil {
		t.Fatal("the database was still open after Close")
	}
}

// TestWorkRuntimeStartupRestoresHistoryWithoutLiveOwners pins that a stored work
// result is restored at startup even when no live owner exists: it appears as
// terminal, acknowledgeable history rather than an active registration.
func TestWorkRuntimeStartupRestoresHistoryWithoutLiveOwners(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	const sessionID = "sess_restore"

	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	database, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "startup-restore")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	started := time.Now().Add(-time.Hour)
	if err := database.CreateSession(sessionID, projectID, "", started); err != nil {
		database.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	sourceID := "watch-7"
	record := db.WorkResultRecord{
		TaskKey:        viewmodel.WorkKey(sessionID, "epoch-seed", workKindWatch, sourceID),
		SourceID:       sourceID,
		RuntimeEpoch:   "epoch-seed",
		Kind:           workKindWatch,
		Label:          "seeded watch",
		State:          workStateCompleted,
		Outcome:        "completed",
		StartedAt:      started,
		EndedAt:        started.Add(time.Minute),
		ResultRevision: "rev-seed",
	}
	if err := database.SaveWorkView(sessionID, db.WorkViewRecord{Version: 1, WatchResults: []db.WorkResultRecord{record}}); err != nil {
		database.Close()
		t.Fatalf("SaveWorkView: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	auth, _ := memoryAuthResolver(t)
	rt, err := StartRuntime(context.Background(),
		WithWorkingDir(dir),
		WithHomeDir(home),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return nativeToolAgentConfig("restore"), nil
		}),
		WithAuthResolver(auth),
		WithExistingSession(sessionID),
	)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	snap := rt.WorkSnapshot()
	if snap.ConversationID != sessionID {
		t.Fatalf("snapshot conversation = %q, want %q", snap.ConversationID, sessionID)
	}
	item, ok := workItemBySource(snap, workKindWatch, sourceID)
	if !ok {
		t.Fatalf("the stored result was not restored at startup: %+v", snap.Items)
	}
	if item.TaskKey != record.TaskKey || item.ResultRevision != record.ResultRevision {
		t.Fatalf("restored item lost its identity: %+v", item)
	}
	if !workItemTerminal(item.State) {
		t.Fatalf("restored item state = %q, want a terminal state", item.State)
	}
	if !item.Capabilities.Acknowledge {
		t.Fatalf("a restored terminal result should be acknowledgeable: %+v", item)
	}
	if viewmodel.WorkActive(item) {
		t.Fatalf("a restored result should not be active: %+v", item)
	}
}

// TestWorkRuntimeDirtyRetryDoesNotWriteProgressTicks pins that the normal
// runtime's retry path re-offers only a dirty durable write: a cosmetic progress
// change, reconciled and retried many times, never turns into a clean-state
// write.
func TestWorkRuntimeDirtyRetryDoesNotWriteProgressTicks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".marshal"), 0o755); err != nil {
		t.Fatalf("mkdir .marshal: %v", err)
	}
	database, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject(dir, "work-retry")
	if err != nil {
		database.Close()
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	const sessionID = "sess_retry"
	if err := database.CreateSession(sessionID, projectID, "", time.Now()); err != nil {
		database.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	state := session.New(config.Default(), dir, time.Now(), session.Persistence{
		DB:        database,
		SessionID: sessionID,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	// A second connection observes commits the session's own connection makes, so
	// a clean-state write is detectable even when it rewrites an identical row.
	probe, err := db.Open(db.Path(dir))
	if err != nil {
		t.Fatalf("open probe db: %v", err)
	}
	t.Cleanup(func() { _ = probe.Close() })
	dataVersion := func() int64 {
		t.Helper()
		var v int64
		if err := probe.SQLDB().QueryRow("PRAGMA data_version").Scan(&v); err != nil {
			t.Fatalf("read data_version: %v", err)
		}
		return v
	}

	clock := &workClock{t: time.Unix(8000, 0)}
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	state.RegisterSubagent("progressing", child)
	wc := newWorkCoordinator(context.Background(), state, workSources{}, clock.now)
	t.Cleanup(wc.close)

	baseline := dataVersion()
	for i := 0; i < 5; i++ {
		child.SetActiveToolCall(session.ActiveToolCall{Name: "file.read"})
		clock.advance(2 * time.Second)
		wc.reconcileWork(clock.now())
		wc.retryPersistence(clock.now())
	}
	if got := dataVersion(); got != baseline {
		t.Fatalf("a progress tick committed a clean-state write: data_version %d -> %d", baseline, got)
	}

	// A genuine durable save does commit, proving the probe observes writes.
	if err := state.SaveWatchWorkResult(db.WorkResultRecord{
		TaskKey:        viewmodel.WorkKey(sessionID, "epoch-1", workKindWatch, "w1"),
		SourceID:       "w1",
		RuntimeEpoch:   "epoch-1",
		Kind:           workKindWatch,
		Label:          "watch",
		State:          workStateCompleted,
		Outcome:        "completed",
		StartedAt:      clock.now(),
		EndedAt:        clock.now().Add(time.Second),
		ResultRevision: "rev-1",
	}); err != nil {
		t.Fatalf("SaveWatchWorkResult: %v", err)
	}
	if got := dataVersion(); got == baseline {
		t.Fatal("the probe did not observe a genuine durable save")
	}
}
