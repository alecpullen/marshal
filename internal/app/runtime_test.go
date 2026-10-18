package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/tools/native"
	"marshal/internal/trust"
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
