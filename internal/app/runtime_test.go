package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"marshal/internal/credentials"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/provider/oauthworker"
	"marshal/internal/oauth"
	"marshal/internal/tools/native"
	"marshal/internal/trust"
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
	_, err := jobManager.Start(ctx, "blocking-job", time.Hour)
	if err != nil {
		t.Fatalf("Start: %v", err)
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
	_, err := jobManager.Start(ctx, "blocking", time.Hour)
	if err != nil {
		t.Fatalf("Start: %v", err)
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

	err = rt.Close(ctx)
	if err == nil {
		t.Fatal("expected Close to return errors, got nil")
	}

	// (a) errors.Is for each injected failure.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close error should contain DeadlineExceeded, got %v", err)
	}
	if !errors.Is(err, mcpErr) {
		t.Errorf("Close error should contain %q, got %v", mcpErr, err)
	}
	// steering broker has no Close() return value, so no error.
	if !errors.Is(err, dbErr) {
		t.Errorf("Close error should contain %q, got %v", dbErr, err)
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
