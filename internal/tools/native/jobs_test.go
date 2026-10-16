package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

// ---------------------------------------------------------------------------
// Fake CommandRunner for unit tests
// ---------------------------------------------------------------------------

// fakeCommandRunner is a controllable fake CommandRunner that records the
// CommandRequest, calls OnStart with a well-known PID, writes live output
// to the observers, then waits on a release channel or ctx.Done before
// returning.
type fakeCommandRunner struct {
	mu sync.Mutex

	requests     []CommandRequest
	release      chan struct{} // nil = don't block
	liveOutput   string
	extraBytes   int
	result       CommandResult
	runErr       error
	startedCount int32
	// started, when non-nil, receives one value per Run call.
	started chan struct{}
	// onStart, when non-nil, runs where OnStart is invoked.
	onStart func(pid int)
	// pid lets a test give the fake a recognizable process identity.
	pid int
}

func (f *fakeCommandRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	atomic.AddInt32(&f.startedCount, 1)

	f.mu.Lock()
	f.requests = append(f.requests, req)
	started := f.started
	onStart := f.onStart
	pid := f.pid
	f.mu.Unlock()

	// Call OnStart only when the fake models a process actually starting.
	// A runner that reports a startup failure must not publish a PID.
	if req.OnStart != nil && !(f.runErr != nil && f.modelsStartupFailure()) {
		if pid == 0 {
			pid = 4242
		}
		req.OnStart(pid)
		if onStart != nil {
			onStart(pid)
		}
	}

	if f.liveOutput != "" && req.Stdout != nil {
		_, _ = io.WriteString(req.Stdout, f.liveOutput)
	}

	if f.extraBytes > 0 && req.Stdout != nil {
		_, _ = io.WriteString(req.Stdout, strings.Repeat("x", f.extraBytes))
	}

	// Write the result's captured stdout/stderr to the observers, matching
	// the real execRunner behaviour.
	if f.result.Stdout != "" && req.Stdout != nil {
		_, _ = io.WriteString(req.Stdout, f.result.Stdout)
	}
	if f.result.Stderr != "" && req.Stderr != nil {
		_, _ = io.WriteString(req.Stderr, f.result.Stderr)
	}

	if started != nil {
		close(started)
	}

	if f.release != nil {
		select {
		case <-f.release:
			// Released normally.
		case <-ctx.Done():
			// Context was cancelled (stop or shutdown). Simulate the real
			// world where process cleanup takes a small amount of time
			// so Kill/Shutdown cannot return before the runner finishes.
			time.Sleep(200 * time.Millisecond)
		}
	}

	return f.result, f.runErr
}

// modelsStartupFailure reports whether this fake's recorded error stands for a
// runner that never started a process: it carries no result at all.
func (f *fakeCommandRunner) modelsStartupFailure() bool {
	return f.result == (CommandResult{})
}

// countRuns returns how many times Run was called.
func (f *fakeCommandRunner) countRuns() int {
	return int(atomic.LoadInt32(&f.startedCount))
}

// lastRequest returns the most recent CommandRequest.
func (f *fakeCommandRunner) lastRequest(t *testing.T) CommandRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("runner was never called")
	}
	return f.requests[len(f.requests)-1]
}

type fakeRunnerOption func(*fakeCommandRunner)

func withRelease(ch chan struct{}) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.release = ch }
}

func withLiveOutput(s string) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.liveOutput = s }
}

func withExtraBytes(n int) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.extraBytes = n }
}

func withResult(r CommandResult) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.result = r }
}

func withRunErr(e error) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.runErr = e }
}

func withStarted(ch chan struct{}) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.started = ch }
}

func withPID(pid int) fakeRunnerOption {
	return func(f *fakeCommandRunner) { f.pid = pid }
}

// newFakeRunner returns a fake runner. By default the runner does NOT block
// on Run; call withRelease(ch) to make it block.
func newFakeRunner(opts ...fakeRunnerOption) *fakeCommandRunner {
	f := &fakeCommandRunner{
		result: CommandResult{
			ExitCode: 0,
			Meta: registry.SandboxMeta{
				Backend: "fake",
				Enabled: true,
			},
		},
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// ---------------------------------------------------------------------------
// Unit tests — fake-runner based
// ---------------------------------------------------------------------------

func TestJobManagerUsesConfiguredRunnerAndReportsSandboxMeta(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	// Start a job. The goroutine will call runner.Run which calls OnStart
	// (setting PID) and then blocks on the release channel.
	id, err := jm.Start(ctx, "echo hello", time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Unblock the runner so the job completes.
	close(ch)

	// Wait for the job to finish.
	waitForStatus(t, jm, id, StatusRunning, false, time.Second)

	info, _, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if info.PID != 4242 {
		t.Fatalf("PID = %d, want 4242", info.PID)
	}
	if !info.Sandbox.Enabled || info.Sandbox.Backend != "fake" {
		t.Fatalf("Sandbox meta = %+v, want enabled=true backend=fake", info.Sandbox)
	}
}

// TestLaunchPublishesPIDImmediately pins contract 2: the PID is published from
// OnStart, so it is visible while the command is still running rather than only
// in the settled record.
func TestLaunchPublishesPIDImmediately(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release), withPID(31337))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "slow", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	live, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if live.PID != 31337 {
		t.Fatalf("live PID = %d, want 31337", live.PID)
	}
	if live.Status != StatusRunning {
		t.Fatalf("live status = %q, want running", live.Status)
	}
	close(release)
	if _, err := jm.Wait(context.Background(), info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestFormerDurationCannotKillExecution replaces the old
// TestJobTimeoutIsDistinctFromKill: a duration passed to the deprecated Start
// wrapper must not impose an execution deadline, and the historical timed_out
// status must not be produced by elapsed wall-clock time.
func TestFormerDurationCannotKillExecution(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	// A duration far shorter than the command's runtime. It is ignored.
	id, err := jm.Start(ctx, "sleep 30", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(150 * time.Millisecond) // 15x the former duration

	info, _, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("status = %q after 15x the former duration, want running", info.Status)
	}
	if info.Status == StatusTimedOut {
		t.Fatal("a former duration must not produce the historical timed_out state")
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want 1 (one launch, no restart)", got)
	}

	close(release)
	if _, err := jm.Wait(ctx, id); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestLaunchIgnoresCallerCancellationForLifetime pins contract 1: the process
// context derives from the conversation service, so cancelling the launching
// call context cannot end the command.
func TestLaunchIgnoresCallerCancellationForLifetime(t *testing.T) {
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(context.Background(), runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	callCtx, callCancel := context.WithCancel(context.Background())
	info, err := jm.Launch(callCtx, LaunchSpec{Command: "outlives the call"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	callCancel()

	// A non-positive budget means "do not wait" and must still leave the
	// command running.
	res, err := jm.WaitForeground(context.Background(), info.ID, 0)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Terminal {
		t.Fatalf("result = %+v, want a continuation", res)
	}
	if res.Info.Status != StatusRunning {
		t.Fatalf("status = %q, want running", res.Info.Status)
	}

	close(release)
	if _, err := jm.Wait(context.Background(), info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestJobOutputIsLiveAndBounded(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release), withLiveOutput("hello world\n"))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "echo hello", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	// Output streamed before the command finishes is already visible.
	_, output, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output before release: %v", err)
	}
	if !strings.Contains(output, "hello world") {
		t.Fatalf("output before completion = %q, want to contain 'hello world'", output)
	}

	close(release)
	waitForStatus(t, jm, info.ID, StatusRunning, false, time.Second)

	// A chatty command past the capture cap: the retained text stays bounded
	// and the truncation is disclosed.
	flooded := newFakeRunner(withLiveOutput("hello world\n"), withExtraBytes(200000))
	jm2 := NewJobManager(ctx, flooded, t.TempDir(), 25, time.Hour, 100)
	t.Cleanup(func() { _ = jm2.Shutdown(context.Background()) })
	floodID, err := jm2.Start(ctx, "chatty", time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForStatus(t, jm2, floodID, StatusRunning, false, 2*time.Second)

	info2, output2, err := jm2.Output(floodID, 0)
	if err != nil {
		t.Fatalf("Output after completion: %v", err)
	}
	if !info2.OutputTruncated {
		t.Fatalf("OutputTruncated should be true, info=%+v", info2)
	}
	if !strings.Contains(output2, "[output truncated]") {
		t.Fatalf("output = %q, want truncation marker", output2)
	}
	if len(output2) > 100+len(truncationMarker)+len("stdout:\n") {
		t.Fatalf("retained output = %d bytes, want it bounded by the cap", len(output2))
	}
}

// TestManagedOutputKeepsRecentTailPastTheCap pins the tail mode the manager
// selects through RetainOutputTail: the retained text stays bounded but holds
// the most recent bytes, not the first ones, and the observer keeps receiving
// bytes after the cap.
func TestManagedOutputKeepsRecentTailPastTheCap(t *testing.T) {
	ctx := context.Background()
	var live strings.Builder
	runner := newFakeRunner(
		withLiveOutput(strings.Repeat("early\n", 200)),
		withResult(CommandResult{Stdout: "LATE-MARKER\n", ExitCode: 0}),
	)
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 64)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "chatty", Stdout: &live, Detached: false})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := jm.WaitForeground(ctx, info.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if !res.Terminal {
		t.Fatalf("result = %+v, want a terminal result", res)
	}
	req := runner.lastRequest(t)
	if !req.RetainOutputTail {
		t.Fatal("managed commands must retain the output tail, not the prefix")
	}
	if !strings.Contains(res.Output, "LATE-MARKER") {
		t.Fatalf("output = %q, want the late marker in the retained tail", res.Output)
	}
	if len(res.Output) > 64+len(truncationMarker)+len("stdout:\n") {
		t.Fatalf("retained output = %d bytes, want it bounded by the cap", len(res.Output))
	}
	// The observer was attached for the whole run, so it saw the late bytes
	// even though the tail cap had long since been reached.
	if !strings.Contains(live.String(), "LATE-MARKER") {
		t.Fatal("the attached observer stopped receiving output at the capture cap")
	}
}

func TestCompletedJobsDoNotConsumeConcurrency(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner() // No release channel → returns immediately.
	jm := NewJobManager(ctx, runner, t.TempDir(), 1, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	// Start job 1 — it completes immediately.
	id1, err := jm.Start(ctx, "job1", time.Second)
	if err != nil {
		t.Fatalf("start job1: %v", err)
	}

	waitForStatus(t, jm, id1, StatusRunning, false, time.Second)

	// maxJobs=1 but job 1 is completed (retained, not evicted).
	// Since only unsettled commands count toward the limit, job 2 must start.
	id2, err := jm.Start(ctx, "job2", time.Second)
	if err != nil {
		t.Fatalf("start job2 with maxJobs=1 and a completed retained job1: %v", err)
	}

	_ = id2
}

// TestCapacityCountsAttachedAndDetachedCommands pins the reserved-slot rule: an
// attached foreground command consumes capacity, promotion needs no new slot,
// and a stopping process retains its slot until it settles.
func TestCapacityCountsAttachedAndDetachedCommands(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 1, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	attached, err := jm.Launch(ctx, LaunchSpec{Command: "first", Origin: CommandOrigin{TurnID: "t1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if got := jm.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount = %d, want 1", got)
	}
	if got := jm.RunningCount(); got != 0 {
		t.Fatalf("RunningCount = %d, want 0 (the command is attached)", got)
	}

	// An attached command already consumes the only slot.
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "second"}); err == nil {
		t.Fatal("expected the attached command to consume the capacity slot")
	}

	// Promotion from a zero budget consumes no new slot.
	res, err := jm.WaitForeground(ctx, attached.ID, 0)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment = %q, want background", res.Info.Attachment)
	}
	if got := jm.RunningCount(); got != 1 {
		t.Fatalf("RunningCount after promotion = %d, want 1", got)
	}
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "second"}); err == nil {
		t.Fatal("a promoted background job must still occupy its slot")
	}

	// A stopping process keeps its slot until it settles, then releases it.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := jm.Kill(attached.ID); err != nil {
			t.Errorf("Kill: %v", err)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if got := jm.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount while stopping = %d, want 1", got)
	}
	close(release)
	<-stopped
	if got := jm.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount after settlement = %d, want 0", got)
	}
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "second"}); err != nil {
		t.Fatalf("slot was not released after settlement: %v", err)
	}
}

// TestJobLoweredCapacityNeverEvictsActiveCommands pins the retention rule
// against a capacity that is already below the active count. The capacity is
// changed the way a reload would change the service's launch defaults.
func TestJobLoweredCapacityNeverEvictsActiveCommands(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	var ids []string
	for i := 0; i < 3; i++ {
		info, err := jm.Launch(ctx, LaunchSpec{Command: fmt.Sprintf("cmd-%d", i), Detached: true})
		if err != nil {
			t.Fatalf("Launch %d: %v", i, err)
		}
		ids = append(ids, info.ID)
	}

	// A reload may lower the limit for future admissions; active commands must
	// survive it. The capacity is read at admission, so lowering it now affects
	// only launches that have not happened yet.
	jm.mu.Lock()
	jm.maxJobs = 1
	jm.mu.Unlock()
	if got := len(jm.ActiveExecutions()); got != 3 {
		t.Fatalf("ActiveExecutions = %d, want 3 after lowering capacity", got)
	}
	for i, id := range ids {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%s) after lowering capacity: %v", ids[i], err)
		}
		if info.Status != StatusRunning {
			t.Fatalf("job %s status = %q, want running", id, info.Status)
		}
	}

	// Future admission is blocked, active work is not evicted.
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "extra", Detached: true}); err == nil {
		t.Fatal("expected admission to be blocked by the lowered capacity")
	}
	close(release)
	for _, id := range ids {
		if _, err := jm.Wait(ctx, id); err != nil {
			t.Fatalf("Wait(%s): %v", id, err)
		}
	}
}

func TestJobKillWaitsForRunner(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	id, err := jm.Start(ctx, "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Kill should block until the runner is released.
	killDone := make(chan struct{})
	go func() {
		_ = jm.Kill(id)
		close(killDone)
	}()

	time.Sleep(50 * time.Millisecond)

	select {
	case <-killDone:
		t.Fatal("Kill returned before runner was released")
	default:
	}

	// Release the runner — Kill should now return.
	close(ch)

	select {
	case <-killDone:
	case <-time.After(time.Second):
		t.Fatal("Kill did not return after runner release within 1s")
	}
}

// TestRequestStopUsesStoppingStateUntilSettled pins contract 6: an accepted stop
// puts the execution in the stopping state, which is not terminal, and the
// outcome is only recorded once the process is gone.
func TestRequestStopUsesStoppingStateUntilSettled(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release), withResult(CommandResult{ExitCode: -1}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "sleep 30", Detached: true, Origin: CommandOrigin{TurnID: "t1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if err := jm.RequestStop(info.ID, "user cancelled"); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}

	stopping, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if stopping.Status != StatusStopping {
		t.Fatalf("status = %q, want stopping while termination settles", stopping.Status)
	}
	if stopping.Status.Terminal() {
		t.Fatal("a stopping execution is not terminal")
	}
	if stopping.StopCause != "user cancelled" || stopping.StopRequestedAt.IsZero() {
		t.Fatalf("stop evidence = %q / %v, want the request recorded", stopping.StopCause, stopping.StopRequestedAt)
	}
	// A stopping job is still outstanding work.
	if got := len(jm.Outstanding()); got != 1 {
		t.Fatalf("Outstanding while stopping = %d, want 1", got)
	}

	close(release)
	settled, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.Status != StatusKilled || settled.Outcome != OutcomeStopped {
		t.Fatalf("settled = %q/%q, want killed/stopped", settled.Status, settled.Outcome)
	}
	if settled.ExitCode == nil || *settled.ExitCode != -1 {
		t.Fatalf("ExitCode = %v, want the observed -1", settled.ExitCode)
	}
}

// TestRepeatStopsAreHarmless pins that a second stop request neither fails nor
// rewrites the first request's cause.
func TestRepeatStopsAreHarmless(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "sleep 30", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started
	if err := jm.RequestStop(info.ID, "first cause"); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	if err := jm.RequestStop(info.ID, "second cause"); err != nil {
		t.Fatalf("second RequestStop: %v", err)
	}

	stopping, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if stopping.StopCause != "first cause" {
		t.Fatalf("StopCause = %q, want the first request's cause", stopping.StopCause)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want 1", got)
	}

	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestLateStopDoesNotRelabelNaturalCompletion pins the race rule: a stop request
// accepted after the process exited is recorded as evidence and leaves the
// natural outcome alone.
func TestLateStopDoesNotRelabelNaturalCompletion(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner(withResult(CommandResult{Stdout: "done\n", ExitCode: 0}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	id, err := jm.Start(ctx, "true", time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	settled, err := jm.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.Status != StatusCompleted || settled.Outcome != OutcomeCompleted {
		t.Fatalf("settled = %q/%q, want completed/completed", settled.Status, settled.Outcome)
	}

	// The stop arrives late: the completion stands, and the settled record is
	// not rewritten as a cancellation.
	if err := jm.RequestStop(id, "too late"); err != nil {
		t.Fatalf("RequestStop: %v", err)
	}
	after, _, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if after.Status != StatusCompleted || after.Outcome != OutcomeCompleted {
		t.Fatalf("after late stop = %q/%q, want completed/completed", after.Status, after.Outcome)
	}
	if after.StopCause != "" {
		t.Fatalf("StopCause = %q, want no cause recorded for a settled execution", after.StopCause)
	}
}

// TestNonZeroExitPreservesActualCodeAndOutcome pins contract 6's core case: a
// natural non-zero exit keeps its actual code and is distinguishable from a
// startup failure and from a stop.
func TestNonZeroExitPreservesActualCodeAndOutcome(t *testing.T) {
	ctx := context.Background()
	exitErr := errors.New("exit status 3")
	runner := newFakeRunner(
		withResult(CommandResult{Stdout: "FAIL\n", ExitCode: 3, Meta: registry.SandboxMeta{Enabled: true, Backend: "fake"}}),
		withRunErr(exitErr),
	)
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "false", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := jm.WaitForeground(ctx, info.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if !res.Terminal {
		t.Fatalf("result = %+v, want a terminal result", res)
	}
	if res.Info.Outcome != OutcomeNonZeroExit {
		t.Fatalf("outcome = %q, want nonzero_exit", res.Info.Outcome)
	}
	if res.Info.ExitCode == nil || *res.Info.ExitCode != 3 {
		t.Fatalf("ExitCode = %v, want the actual 3", res.Info.ExitCode)
	}
	if !errors.Is(res.RunError, exitErr) {
		t.Fatalf("RunError = %v, want the runner's error preserved", res.RunError)
	}
}

// TestStartupFailureIsDistinctFromExit pins that a runner which never started a
// process reports a startup failure with no exit code, rather than a non-zero
// exit.
func TestStartupFailureIsDistinctFromExit(t *testing.T) {
	ctx := context.Background()
	startErr := errors.New("exec: not found")
	runner := newFakeRunner(withResult(CommandResult{}), withRunErr(startErr))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "nonexistent-binary", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := jm.WaitForeground(ctx, info.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Info.Outcome != OutcomeStartupFailure {
		t.Fatalf("outcome = %q, want startup_failure", res.Info.Outcome)
	}
	if res.Info.ExitCode != nil {
		t.Fatalf("ExitCode = %v, want nil for a command that never started", *res.Info.ExitCode)
	}
	if !errors.Is(res.RunError, startErr) {
		t.Fatalf("RunError = %v, want the startup error preserved", res.RunError)
	}
}

// TestResourceLimitOnlyClaimedWithBackendEvidence pins the evidence rule: a
// backend-named limit is reported as a resource limit, while a bare non-zero
// exit is never guessed to be one.
func TestResourceLimitOnlyClaimedWithBackendEvidence(t *testing.T) {
	ctx := context.Background()
	limited := newFakeRunner(withResult(CommandResult{
		ExitCode: 137,
		Meta:     registry.SandboxMeta{Enabled: true, Backend: "container", ResourceLimits: true, KilledReason: "memory_limit"},
	}), withRunErr(errors.New("signal: killed")))
	jm := NewJobManager(ctx, limited, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "hog", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := jm.WaitForeground(ctx, info.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Info.Outcome != OutcomeResourceLimit {
		t.Fatalf("outcome = %q, want resource_limit on backend evidence", res.Info.Outcome)
	}
	if res.Info.ExitCode == nil || *res.Info.ExitCode != 137 {
		t.Fatalf("ExitCode = %v, want the observed 137", res.Info.ExitCode)
	}

	// The same exit code with no backend evidence stays a plain failure.
	bare := newFakeRunner(withResult(CommandResult{ExitCode: 137}), withRunErr(errors.New("signal: killed")))
	jm2 := NewJobManager(ctx, bare, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm2.Shutdown(context.Background()) })
	info2, err := jm2.Launch(ctx, LaunchSpec{Command: "hog", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res2, err := jm2.WaitForeground(ctx, info2.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res2.Info.Outcome != OutcomeNonZeroExit {
		t.Fatalf("outcome = %q, want nonzero_exit without backend evidence", res2.Info.Outcome)
	}
}

func TestJobManagerParentCancellationKillsJobs(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())

	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(parentCtx, runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	_, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Cancel the parent context — all jobs should be cancelled.
	parentCancel()

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		count := jm.RunningCount()
		if count == 0 {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("running count did not reach 0 within 1s, still %d", count)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestJobManagerShutdownJoinsAndRejectsStart(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)

	// Start a job that blocks.
	id, err := jm.Start(ctx, "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = id

	// Shutdown should block until we release the runner.
	shutdownDone := make(chan struct{})
	go func() {
		_ = jm.Shutdown(context.Background())
		close(shutdownDone)
	}()

	time.Sleep(50 * time.Millisecond)

	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before runner was released")
	default:
	}

	// Release the runner — Shutdown should now return.
	close(ch)

	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not return within 1s")
	}

	// After Shutdown returns, Start should return ErrJobManagerClosed.
	_, err = jm.Start(context.Background(), "echo hi", time.Second)
	if err != ErrJobManagerClosed {
		t.Fatalf("Start after shutdown: err=%v, want ErrJobManagerClosed", err)
	}
}

func TestJobManagerShutdownHonorsCallerDeadline(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{}) // Never released.
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)

	_, err := jm.Start(ctx, "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Shutdown with a very short deadline should time out.
	shortCtx, shortCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer shortCancel()

	err = jm.Shutdown(shortCtx)
	if err != context.DeadlineExceeded {
		t.Fatalf("Shutdown with short deadline: err=%v, want DeadlineExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// Integration tests — real execRunner backed
// ---------------------------------------------------------------------------

func TestJobManagerEvictsCompletedJobsByRetention(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), t.TempDir(), 25, 100*time.Millisecond, 100000)

	id, err := jm.Start(context.Background(), "echo hello", time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForStatus(t, jm, id, StatusRunning, false, time.Second)

	// Should still be accessible.
	if _, _, err := jm.Output(id, 0); err != nil {
		t.Fatalf("output right after completion: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	if jobs := jm.List(); len(jobs) != 0 {
		t.Fatalf("List returned %d jobs, want 0", len(jobs))
	}
	if _, _, err := jm.Output(id, 0); err == nil {
		t.Fatal("expected job to be evicted")
	}
}

// TestShellRunBackgroundInvokesGuardrail verifies that the handler-level
// guardrail is invoked for background shell.run jobs. Policy evaluation
// (deny/allow rules) happens upstream in the agent loop before the tool
// handler runs — see internal/agent/execute.go, where Policy.Evaluate is
// called for every tool call and DecisionDeny short-circuits before
// dispatch. The audit finding TOOLS-MOD-F9 ("background shell.run bypasses
// policy") is a false positive: evaluateShell reads only args["command"]
// and does not branch on background, so the same command is evaluated the
// same way whether foreground or background. This test pins the one piece
// of enforcement that lives at the handler layer: the guardrail pre-flight
// check, which runs for background jobs too.
func TestShellRunBackgroundInvokesGuardrail(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})

	blocked := false
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		SessionState:  state,
		Guardrail: func(command string, system bool) error {
			blocked = true
			return fmt.Errorf("guardrail blocked %q", command)
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	// A background job must still pass through the guardrail. If the
	// guardrail is not invoked for background jobs, this returns nil and
	// starts a job, which would be a regression.
	if _, err := invokeTool(t, reg, "shell.run", `{"command":"echo hi","background":true}`); err == nil {
		t.Fatal("background shell.run should have been blocked by the guardrail")
	}
	if !blocked {
		t.Fatal("guardrail was not invoked for a background shell.run job")
	}
	if state.RunningJobsCount() != 0 {
		t.Fatalf("running jobs = %d, want 0 (guardrail must block before start)", state.RunningJobsCount())
	}
}

func TestShellRunBackgroundStartsJob(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("shell.run background returned error: %v", err)
	}
	if !strings.Contains(res.Content, "job_id: job-") {
		t.Fatalf("content = %q, want job_id", res.Content)
	}

	if state.RunningJobsCount() != 1 {
		t.Fatalf("running jobs = %d, want 1", state.RunningJobsCount())
	}
}

func TestShellRunBackgroundUsesWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"touch bg-file.txt","background":true,"timeout_seconds":5}`); err != nil {
		t.Fatalf("shell.run background returned error: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if _, err := os.Stat(filepath.Join(root, "bg-file.txt")); err != nil {
		t.Fatalf("expected file in workspace: %v", err)
	}
}

func TestRegisterAllHonorsConfigBackgroundLimits(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Tools.Shell.MaxBackgroundJobs = 1
	cfg.Tools.Shell.BackgroundRetention = 30 * time.Minute

	// The job.* tools were retired in the context-diet Tier 2a cut, so the
	// test drives the manager the registry was built with directly (the
	// model reaches the same handle through agent.await/kill). The runner
	// must block, or every job finishes instantly and the cap never trips.
	release := make(chan struct{})
	jm := NewJobManager(context.Background(), newFakeRunner(withRelease(release)), root, 1, cfg.Tools.Shell.BackgroundRetention, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	reg := registry.New()
	state := session.New(cfg, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, Config: cfg, SessionState: state, JobManager: jm}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("first background job: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`); err == nil {
		t.Fatal("expected too many jobs error")
	}

	jobID := strings.TrimPrefix(strings.TrimSpace(res.Content), "job_id: ")
	if err := jm.Kill(jobID); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}

// TestJobOutputAndListTools was removed with the job.output/job.kill/job.list
// tools in the context-diet Tier 2a cut. The manager behaviour it checked
// (live bounded output, listing, kill-then-status) is covered at the manager
// level by TestJobOutputIsLiveAndBounded and TestJobKillWaitsForRunner above.

func TestJobEventCarriesSnapshot(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), "", 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	broker := pubsub.NewBroker[JobEvent]()
	t.Cleanup(broker.Close)
	subCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := broker.Subscribe(subCtx)
	jm.SetBroker(broker)

	if _, err := jm.Start(context.Background(), "sleep 1", 5*time.Second); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case ev := <-ch:
		if len(ev.Payload.Jobs) == 0 {
			t.Fatal("JobEvent.Jobs is empty; the lane cannot render without it")
		}
		if ev.Payload.Jobs[0].Command == "" {
			t.Fatal("JobEvent.Jobs carries no command")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no JobEvent published")
	}
}

// TestJobEventPublishesAttachmentChangeWithEqualCount pins contract 4's
// publication rule: a change that leaves the detached count equal is still
// published, because the execution snapshot moved.
func TestJobEventPublishesAttachmentChangeWithEqualCount(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), "", 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	broker := pubsub.NewBroker[JobEvent]()
	t.Cleanup(broker.Close)
	subCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := broker.Subscribe(subCtx)
	jm.SetBroker(broker)

	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm.runner = runner

	info, err := jm.Launch(context.Background(), LaunchSpec{Command: "handoff"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	drain(ch) // admission

	if res, err := jm.WaitForeground(context.Background(), info.ID, 0); err != nil {
		t.Fatalf("WaitForeground: %v", err)
	} else if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment = %q, want background", res.Info.Attachment)
	}

	select {
	case ev := <-ch:
		if ev.Payload.Count != 1 {
			t.Fatalf("count = %d, want 1 after promotion", ev.Payload.Count)
		}
		var seen bool
		for _, j := range ev.Payload.Jobs {
			if j.ID == info.ID && j.Attachment == AttachmentBackground {
				seen = true
			}
		}
		if !seen {
			t.Fatalf("event Jobs = %+v, want the promoted execution with background attachment", ev.Payload.Jobs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("promotion published no JobEvent; an attachment change must be visible")
	}
	close(release)
	if _, err := jm.Wait(context.Background(), info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestCallbacksRunOutsideLocksAndMayReenter pins step 5's requirement that
// callbacks are invoked with no manager lock held: the callback below re-enters
// the manager, which would deadlock if the snapshot were published under a lock.
func TestCallbacksRunOutsideLocksAndMayReenter(t *testing.T) {
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(context.Background(), runner, "", 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	var (
		mu       sync.Mutex
		exitInfo []JobInfo
		ran      []time.Duration
		outputs  []string
	)
	jm.SetOnChange(func(int) {
		// Re-enters the manager while a change is being reported.
		_ = jm.ActiveExecutions()
		_ = jm.List()
		_ = jm.ActiveCount()
	})
	jm.SetOnExit(func(info JobInfo, r time.Duration, output string) {
		mu.Lock()
		exitInfo = append(exitInfo, info)
		ran = append(ran, r)
		outputs = append(outputs, output)
		mu.Unlock()
		// Re-enters the manager from the exit callback too.
		_ = jm.List()
	})

	info, err := jm.Launch(context.Background(), LaunchSpec{Command: "callback", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := jm.Wait(context.Background(), info.ID); err != nil {
			t.Errorf("Wait: %v", err)
		}
	}()
	select {
	case <-done:
		t.Fatal("Wait returned before the runner was released")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a re-entrant callback deadlocked the manager")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(exitInfo) != 1 {
		t.Fatalf("OnExit calls = %d, want 1", len(exitInfo))
	}
	if exitInfo[0].ID != info.ID || exitInfo[0].Status.Terminal() != true {
		t.Fatalf("OnExit info = %+v, want the settled execution", exitInfo[0])
	}
}

func TestJobManagerPublishesViaBroker(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), "", 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	broker := pubsub.NewBroker[JobEvent]()
	t.Cleanup(broker.Close)
	subCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := broker.Subscribe(subCtx)
	jm.SetBroker(broker)

	id, err := jm.Start(context.Background(), "echo hi", time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "jobs" {
			t.Fatalf("type = %q, want jobs", ev.Type)
		}
		if ev.Payload.Count != 1 || ev.Payload.Delta != 1 {
			t.Fatalf("payload = %+v, want count=1 delta=+1", ev.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("no job-event published within 1s")
	}

	_ = jm.Kill(id)
}

func TestJobManagerStartAndKill(t *testing.T) {
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(context.Background(), runner, "", 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	id, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	info, _, err := jm.Output(id, 10)
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("status = %q, want running", info.Status)
	}

	// Kill the job — it should wait for the runner to be released.
	killDone := make(chan struct{})
	go func() {
		if err := jm.Kill(id); err != nil {
			t.Errorf("kill: %v", err)
		}
		close(killDone)
	}()

	// Wait until the stop request has actually been accepted, so the release
	// below cannot race it and turn this into a "late stop" case.
	waitForStopping(t, jm, id, time.Second)
	close(ch) // Release the runner.

	select {
	case <-killDone:
	case <-time.After(time.Second):
		t.Fatal("Kill did not return within 1s")
	}

	waitForStatus(t, jm, id, StatusRunning, true, time.Second)

	info, _, _ = jm.Output(id, 10)
	if info.Status != StatusKilled {
		t.Fatalf("status after kill = %q, want killed", info.Status)
	}
	if info.Outcome != OutcomeStopped {
		t.Fatalf("outcome after kill = %q, want stopped", info.Outcome)
	}
}

func TestJobManagerEnforcesMaxJobs(t *testing.T) {
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(context.Background(), runner, "", 2, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	_, _ = jm.Start(context.Background(), "sleep 30", 5*time.Second)
	_, _ = jm.Start(context.Background(), "sleep 30", 5*time.Second)
	_, err := jm.Start(context.Background(), "sleep 30", 5*time.Second)
	if err == nil {
		t.Fatal("expected too many jobs error")
	}
	// Release the runners so cleanup doesn't have to wait for the
	// 200ms sleep after context cancellation.
	close(ch)
}

func TestJobManagerRunsInWorkspaceDir(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner()
	jm := NewJobManager(context.Background(), runner, root, 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	_, err := jm.Start(context.Background(), "echo hi", 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	var found bool
	for _, req := range runner.requests {
		if req.Command == "echo hi" {
			if req.Dir != root {
				t.Fatalf("Start Dir = %q, want %q", req.Dir, root)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Start did not call runner.Run with expected command")
	}
}

// TestLaunchSnapshotsRunnerDirAndLimits pins contract 2: the runner, working
// directory, output policy, identity and generation are captured at admission,
// so a later change to the manager's defaults cannot redirect an admitted
// command.
func TestLaunchSnapshotsRunnerDirAndLimits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	perLaunch := newFakeRunner(
		withResult(CommandResult{Stdout: "captured\n", ExitCode: 0}),
		withLiveOutput("captured\n"),
	)
	jm := NewJobManager(ctx, newFakeRunner(), root, 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	origin := CommandOrigin{
		ConversationID: "conv-1",
		Actor:          "parent",
		TurnID:         "turn-7",
		StepID:         "step-2",
		ToolCallID:     "call-3",
		Generation:     4,
	}
	info, err := jm.Launch(ctx, LaunchSpec{
		Command: "captured",
		Dir:     "/captured-dir",
		Runner:  perLaunch,
		Origin:  origin,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// After admission, change everything the manager could re-resolve.
	jm.runner = newFakeRunner()
	jm.dir = "/after-admission"
	jm.SetDirFunc(func() string { return "/dir-fn" })
	jm.maxOutputBytes = 8

	res, err := jm.WaitForeground(ctx, info.ID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if !res.Terminal {
		t.Fatalf("result = %+v, want a terminal result", res)
	}
	req := perLaunch.lastRequest(t)
	if req.Dir != "/captured-dir" {
		t.Fatalf("Dir = %q, want the captured /captured-dir", req.Dir)
	}
	if req.Timeout != 0 {
		t.Fatalf("Timeout = %s, want no execution deadline", req.Timeout)
	}
	if !req.RetainOutputTail {
		t.Fatal("managed commands must request tail-mode capture")
	}
	if req.MaxOutputBytes != OutputLimit(1<<20) {
		t.Fatalf("MaxOutputBytes = %d, want the manager limit captured at admission", req.MaxOutputBytes)
	}
	if res.Info.Origin != origin {
		t.Fatalf("Origin = %+v, want the captured %+v", res.Info.Origin, origin)
	}
	if got := perLaunch.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want 1", got)
	}
}

func TestJobOutputSeparatesStdoutAndStderr(t *testing.T) {
	runner := newFakeRunner(withResult(CommandResult{Stdout: "out-line\n", Stderr: "err-line\n"}))
	jm := NewJobManager(context.Background(), runner, t.TempDir(), 1, time.Minute, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	id, err := jm.Start(context.Background(), "echo out-line; echo err-line 1>&2", time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForStatus(t, jm, id, StatusRunning, false, 2*time.Second)

	_, out, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !strings.Contains(out, "stdout:") || !strings.Contains(out, "stderr:") {
		t.Fatalf("output missing section headers: %q", out)
	}
	// stdout must come before stderr in the rendered output
	stdoutIdx := strings.Index(out, "out-line")
	stderrIdx := strings.Index(out, "err-line")
	if stdoutIdx == -1 || stderrIdx == -1 || stdoutIdx > stderrIdx {
		t.Fatalf("output not ordered stdout-then-stderr: %q", out)
	}
}

// ---------------------------------------------------------------------------
// Stop and attachment tests — the handoff/stop boundary
// ---------------------------------------------------------------------------

// TestStopAttachedTargetsOnlyAttachedCommandsOfThatTurn pins contract 3: an
// explicit turn stop hits the commands still attached to it, leaves detached
// jobs and other turns alone, and refuses further admission for that turn.
func TestStopAttachedTargetsOnlyAttachedCommandsOfThatTurn(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	attached, err := jm.Launch(ctx, LaunchSpec{Command: "attached", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch attached: %v", err)
	}
	detached, err := jm.Launch(ctx, LaunchSpec{Command: "detached", Detached: true, Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch detached: %v", err)
	}
	other, err := jm.Launch(ctx, LaunchSpec{Command: "other turn", Origin: CommandOrigin{TurnID: "turn-2"}})
	if err != nil {
		t.Fatalf("Launch other: %v", err)
	}

	ids, err := jm.StopAttached("turn-1", "user stopped the turn")
	if err != nil {
		t.Fatalf("StopAttached: %v", err)
	}
	if len(ids) != 1 || ids[0] != attached.ID {
		t.Fatalf("stopped = %v, want only the attached command %s", ids, attached.ID)
	}

	// The stopped command is already in the stopping state when the call
	// returns, so the decision is observable without racing the goroutine.
	stopping, _, err := jm.Output(attached.ID, 0)
	if err != nil {
		t.Fatalf("Output attached: %v", err)
	}
	if stopping.Status != StatusStopping {
		t.Fatalf("attached status = %q, want stopping", stopping.Status)
	}
	if stopping.StopCause != "user stopped the turn" {
		t.Fatalf("StopCause = %q, want the turn stop cause", stopping.StopCause)
	}

	// A detached job survives its turn's stop, and another turn is untouched.
	for _, id := range []string{detached.ID, other.ID} {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%s): %v", id, err)
		}
		if info.Status != StatusRunning {
			t.Fatalf("job %s status = %q, want running (the stop must not reach it)", id, info.Status)
		}
	}

	// A stopped turn cannot admit more commands.
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "late", Origin: CommandOrigin{TurnID: "turn-1"}}); !errors.Is(err, ErrTurnStopped) {
		t.Fatalf("Launch on a stopped turn = %v, want ErrTurnStopped", err)
	}
	// Another turn still can.
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "still fine", Origin: CommandOrigin{TurnID: "turn-2"}}); err != nil {
		t.Fatalf("Launch on a live turn: %v", err)
	}

	close(release)
	if _, err := jm.Wait(ctx, attached.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestDetachBeforeStopSurvivesTurnStop is the other ordering of the same race:
// the detachment is accepted first, so the turn stop no longer targets the
// command.
func TestDetachBeforeStopSurvivesTurnStop(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "handed off", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	res, err := jm.WaitForeground(ctx, info.ID, 0)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment = %q, want background", res.Info.Attachment)
	}

	ids, err := jm.StopAttached("turn-1", "user stopped the turn")
	if err != nil {
		t.Fatalf("StopAttached: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("stopped = %v, want nothing: the command detached first", ids)
	}

	alive, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if alive.Status != StatusRunning {
		t.Fatalf("status = %q, want running", alive.Status)
	}
	if alive.StopCause != "" {
		t.Fatalf("StopCause = %q, want no stop recorded", alive.StopCause)
	}

	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestStopAttachedRejectsBlankTurnID keeps the targeted stop from silently doing
// nothing.
func TestStopAttachedRejectsBlankTurnID(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	if _, err := jm.StopAttached("  ", "cause"); err == nil {
		t.Fatal("StopAttached with a blank turn id = nil, want an error")
	}
}

// TestStopAttachedOnClosedManager pins that a closed service refuses turn stops
// the same way it refuses admission.
func TestStopAttachedOnClosedManager(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), t.TempDir(), 5, time.Hour, 1<<20)
	if err := jm.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := jm.StopAttached("turn-1", "cause"); !errors.Is(err, ErrJobManagerClosed) {
		t.Fatalf("StopAttached after shutdown = %v, want ErrJobManagerClosed", err)
	}
}

// TestJobHandoffKeepsSameIdentityAndPID pins step 7's identity requirement: the
// execution that continues in the background is the same one, with the same ID
// and PID, and it ran exactly once — the handoff must not relaunch it.
func TestJobHandoffKeepsSameIdentityAndPID(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release), withPID(24680))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "long download", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	live, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if live.PID != 24680 || live.Attachment != AttachmentForeground {
		t.Fatalf("before handoff = pid %d / %q, want 24680 / foreground", live.PID, live.Attachment)
	}

	res, err := jm.WaitForeground(ctx, info.ID, 0)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Info.ID != info.ID {
		t.Fatalf("id after handoff = %q, want the same %q", res.Info.ID, info.ID)
	}
	if res.Info.PID != 24680 {
		t.Fatalf("pid after handoff = %d, want the same 24680", res.Info.PID)
	}
	if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment = %q, want background", res.Info.Attachment)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1 through the handoff", got)
	}

	close(release)
	settled, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.PID != 24680 || settled.ID != info.ID {
		t.Fatalf("settled = %d/%s, want the same identity 24680/%s", settled.PID, settled.ID, info.ID)
	}
}

// TestCompletionRacingTheWaitProducesOneOutcome pins step 7's completion race:
// however the settle and handoff decisions interleave, the caller either gets
// the terminal outcome or a handle to the same still-running command, never a
// detached snapshot of a command that has already finished.
func TestCompletionRacingTheWaitProducesOneOutcome(t *testing.T) {
	for _, delay := range []time.Duration{0, 5 * time.Millisecond, 20 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			ctx := context.Background()
			m := NewJobManager(ctx, &waitTestRunner{delay: delay}, t.TempDir(), 5, time.Hour, 1<<20)
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

			info, err := m.Launch(ctx, LaunchSpec{Command: "racy", Origin: CommandOrigin{TurnID: "turn-1"}})
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			res, err := m.WaitForeground(ctx, info.ID, 10*time.Millisecond)
			if err != nil {
				t.Fatalf("WaitForeground: %v", err)
			}
			if res.Info.ID != info.ID {
				t.Fatalf("identity = %q, want the same %q", res.Info.ID, info.ID)
			}
			if res.Terminal {
				// The command settled during the wait: the caller gets the
				// actual outcome.
				if res.Info.Status.Terminal() != true {
					t.Fatalf("terminal result with status %q", res.Info.Status)
				}
				if res.Info.ExitCode == nil {
					t.Fatal("a terminal natural result must carry an exit code")
				}
				if res.Info.Attachment != AttachmentForeground {
					t.Fatalf("attachment = %q, want foreground for a waited completion", res.Info.Attachment)
				}
				return
			}
			// The wait expired first: the command must still be alive, not a
			// stale snapshot of a finished one.
			live, _, err := m.Output(info.ID, 0)
			if err != nil {
				t.Fatalf("Output: %v", err)
			}
			if res.Info.Status.Terminal() {
				t.Fatalf("continuation carries a terminal status %q", res.Info.Status)
			}
			if !live.Status.Terminal() && live.Status != StatusRunning && live.Status != StatusStopping {
				t.Fatalf("live status = %q, want a non-terminal state", live.Status)
			}
			if _, err := m.Wait(ctx, info.ID); err != nil {
				t.Fatalf("Wait: %v", err)
			}
		})
	}
}

// TestTurnStopWinsAgainstPendingWait pins the stop-then-wait ordering: the
// caller that was waiting reports the command's actual stopped outcome rather
// than a continuation.
func TestTurnStopWinsAgainstPendingWait(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release), withResult(CommandResult{ExitCode: -1}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	oldSettle := foregroundStopSettleTimeout
	foregroundStopSettleTimeout = time.Second
	t.Cleanup(func() { foregroundStopSettleTimeout = oldSettle })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "sleep 30", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	result := make(chan ForegroundResult, 1)
	go func() {
		res, err := jm.WaitForeground(ctx, info.ID, 30*time.Second)
		if err != nil {
			t.Errorf("WaitForeground: %v", err)
			result <- ForegroundResult{}
			return
		}
		result <- res
	}()

	time.Sleep(50 * time.Millisecond)
	if _, err := jm.StopAttached("turn-1", "user stopped the turn"); err != nil {
		t.Fatalf("StopAttached: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // the wait must not return a continuation

	select {
	case res := <-result:
		t.Fatalf("wait returned %+v while the stop was still settling", res)
	default:
	}

	close(release)
	select {
	case res := <-result:
		if !res.Terminal {
			t.Fatalf("result = %+v, want the terminal stopped outcome", res)
		}
		if res.Info.Outcome != OutcomeStopped {
			t.Fatalf("outcome = %q, want stopped", res.Info.Outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForeground did not return after the stopped command settled")
	}
}

// TestWaitCancellationEndsWaitingWithoutStopping pins contract 3's
// non-destructive wait: a cancelled or expired wait detaches the command and
// requests no termination.
func TestWaitCancellationEndsWaitingWithoutStopping(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "download", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	res, err := jm.WaitForeground(waitCtx, info.ID, 30*time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Terminal {
		t.Fatalf("result = %+v, want a continuation", res)
	}
	if res.WaitEnd != WaitEndCanceled {
		t.Fatalf("WaitEnd = %q, want canceled", res.WaitEnd)
	}
	if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment = %q, want background after a cancelled wait", res.Info.Attachment)
	}

	// Still alive, no stop recorded.
	alive, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if alive.Status != StatusRunning {
		t.Fatalf("status = %q, want running", alive.Status)
	}
	if alive.StopCause != "" {
		t.Fatalf("StopCause = %q, want none", alive.StopCause)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want 1 (a wait must not relaunch)", got)
	}

	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestWaitForegroundUnknownID pins the retention-aware error for an unknown
// execution.
func TestWaitForegroundUnknownID(t *testing.T) {
	jm := NewJobManager(context.Background(), newFakeRunner(), t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	if _, err := jm.WaitForeground(context.Background(), "job-999", time.Second); err == nil {
		t.Fatal("WaitForeground with an unknown id = nil, want an error")
	}
}

// TestWaitForegroundReturnsSettledImmediately pins that a wait on a settled
// command reports its outcome without touching a timer.
func TestWaitForegroundReturnsSettledImmediately(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner(withResult(CommandResult{Stdout: "ok\n", ExitCode: 0}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "true", Origin: CommandOrigin{TurnID: "t"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	start := time.Now()
	res, err := jm.WaitForeground(ctx, info.ID, time.Hour)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitForeground took %s on a settled command", elapsed)
	}
	if !res.Terminal || res.Info.Outcome != OutcomeCompleted {
		t.Fatalf("result = %+v, want a completed terminal result", res)
	}
	if !strings.Contains(res.Output, "ok") {
		t.Fatalf("Output = %q, want the captured output", res.Output)
	}
}

// TestWaitForegroundBudgetIncludesStartupTime pins that the budget runs from
// admission, so a caller's wait is not extended by process startup.
func TestWaitForegroundBudgetIncludesStartupTime(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "slow start", Origin: CommandOrigin{TurnID: "t"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	// Consume most of the budget before asking, as an approval gap would.
	// The budget is 300ms; 240ms of it is already gone, so a wait measured
	// from admission has ~60ms left while one measured from the call would
	// run the full 300ms.
	const budget = 300 * time.Millisecond
	time.Sleep(240 * time.Millisecond)

	start := time.Now()
	res, err := jm.WaitForeground(ctx, info.ID, budget)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	elapsed := time.Since(start)
	if res.Terminal {
		t.Fatalf("result = %+v, want a continuation", res)
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("wait ran %s; the budget must be measured from admission, not from the call", elapsed)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("wait ran %s; it did not honour the remaining budget", elapsed)
	}

	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestLaunchRejectsCancelledAndClosedAdmission covers the admission rejections
// that must not leave a goroutine behind.
func TestLaunchRejectsCancelledAndClosedAdmission(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	jm := NewJobManager(parentCtx, runner, t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })
	parentCancel()

	if _, err := jm.Launch(context.Background(), LaunchSpec{Command: "echo hi"}); err == nil {
		t.Fatal("Launch with a cancelled manager context = nil, want an error")
	}

	callCtx, callCancel := context.WithCancel(context.Background())
	callCancel()
	jm2 := NewJobManager(ctx, runner, t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm2.Shutdown(context.Background()) })
	if _, err := jm2.Launch(callCtx, LaunchSpec{Command: "echo hi"}); err == nil {
		t.Fatal("Launch with a cancelled call context = nil, want an error")
	}

	if _, err := jm2.Launch(ctx, LaunchSpec{Command: "   "}); err == nil {
		t.Fatal("Launch with a blank command = nil, want an error")
	}

	if got := jm2.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount = %d after rejected admissions, want 0", got)
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d after rejected admissions, want 0", got)
	}
}

// TestWaitRemainsNonDestructive pins that the existing Wait keeps its contract:
// an ending context reports the context error and leaves the process running.
func TestWaitRemainsNonDestructive(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 5, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	id, err := jm.Start(ctx, "sleep 30", time.Hour)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := jm.Wait(waitCtx, id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want DeadlineExceeded", err)
	}

	info, _, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("status = %q, want running after a cancelled Wait", info.Status)
	}

	// Now settle it deliberately.
	close(release)
	if _, err := jm.Wait(ctx, id); err != nil {
		t.Fatalf("Wait after release: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// drain consumes every event currently queued on ch.
func drain(ch <-chan pubsub.Event[JobEvent]) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// waitForStopping polls jm.Output until the job reports the stopping state, so
// a test can act after a stop request has been accepted rather than racing it.
func waitForStopping(t *testing.T, jm *JobManager, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%q): %v", id, err)
		}
		if info.Status != StatusRunning {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %q to enter the stopping state", id)
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
}

// waitForStatus polls jm.Output until the job's status is no longer
// statusWant (i.e. it has transitioned). If expectTerminal is true,
// it waits until the status IS NOT statusWant (useful for waiting until
// a running job finishes). If expectTerminal is false, it waits until
// the status IS NOT statusWant (same thing). The semantics are:
// wait until status != statusWant.
func waitForStatus(t *testing.T, jm *JobManager, id string, statusWant JobStatus, _ bool, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%q): %v", id, err)
		}
		if info.Status != statusWant {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for status change from %q", statusWant)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestJobManagerOnExitFiresOncePerJob(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner(withResult(CommandResult{Stdout: "out\n", ExitCode: 0}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	var mu sync.Mutex
	var got []JobInfo
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) {
		mu.Lock()
		got = append(got, info)
		mu.Unlock()
	})
	id, err := jm.Start(ctx, "echo hi", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jm.Wait(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].ID != id || got[0].Status == StatusRunning {
		t.Fatalf("onExit calls = %+v", got)
	}
	if got[0].Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", got[0].Status)
	}
	if got[0].Outcome != OutcomeCompleted {
		t.Fatalf("outcome = %q, want completed", got[0].Outcome)
	}
}

func TestJobManagerStartRejectsCancelledManagerCtx(t *testing.T) {
	parentCtx, parentCancel := context.WithCancel(context.Background())
	runner := newFakeRunner()
	jm := NewJobManager(parentCtx, runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	// Cancel the parent context (which cancels managerCtx).
	parentCancel()

	// Use a fresh call context that is NOT cancelled.
	_, err := jm.Start(context.Background(), "echo hello", 0)
	if err == nil {
		t.Fatal("expected error when managerCtx is cancelled, got nil")
	}
}
