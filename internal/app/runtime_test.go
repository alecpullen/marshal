package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/native"
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
type recordingSnapshot struct {
	record func(string)
	err    error
}

func (s *recordingSnapshot) Prune(_ context.Context, _ int) error {
	if s.record != nil {
		s.record("snapshot")
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
	//   dbPrune → snapshot → dbClose → resourceClosers (reversed) → state shutdown.
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
		"dbPrune",        // 6a. DB prune (inside snapshot stage)
		"snapshot",       // 6b. snapshot filesystem prune
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
