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

// launchBackground admits command as a background execution through the launch
// API and returns its display id. It stands in for the removed Start wrapper,
// whose timeout argument was ignored: a managed command has no execution
// deadline, so there is no duration to pass.
func launchBackground(ctx context.Context, jm *JobManager, command string) (string, error) {
	info, err := jm.Launch(ctx, LaunchSpec{Command: command, Detached: true})
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

// newTestCommandService builds a test-OWNED conversation command service. Tests
// that expose shell.run or test.run must inject an owner explicitly: the toolset
// no longer creates one implicitly, so a dispatch through an ownerless registry
// fails rather than quietly supervising commands nothing can stop or settle.
// The optional state and broker receive the same count callback the
// conversation's owner installs in production, and cleanup shuts the service
// down so no test leaks a process.
func newTestCommandService(t *testing.T, runner CommandRunner, dir string, state *session.State, broker *pubsub.Broker[JobEvent]) *JobManager {
	t.Helper()
	jm := NewCommandService(context.Background(), LaunchPolicy{
		Runner:         runner,
		Dir:            dir,
		MaxOutputBytes: 1 << 20,
	})
	if state != nil {
		jm.SetOnChange(state.SetRunningJobsCount)
	}
	if broker != nil {
		jm.SetBroker(broker)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = jm.Shutdown(ctx)
	})
	return jm
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
	id, err := launchBackground(ctx, jm, "echo hello")
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

	// A former duration is gone from the API: no wait can end an execution.
	id, err := launchBackground(ctx, jm, "sleep 30")
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
	floodID, err := launchBackground(ctx, jm2, "chatty")
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
	id1, err := launchBackground(ctx, jm, "job1")
	if err != nil {
		t.Fatalf("start job1: %v", err)
	}

	waitForStatus(t, jm, id1, StatusRunning, false, time.Second)

	// maxJobs=1 but job 1 is completed (retained, not evicted).
	// Since only unsettled commands count toward the limit, job 2 must start.
	id2, err := launchBackground(ctx, jm, "job2")
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
	// only launches that have not happened yet. Committing it through the
	// service's own policy is exactly what a reload does.
	jm.CommitLaunchPolicy(LaunchPolicy{
		Generation: jm.Generation(),
		Runner:     jm.CurrentLaunchPolicy().Runner,
		MaxJobs:    1,
		Retention:  time.Hour,
	})
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

// resourceRunner is a fake CommandRunner that also declares the closeable
// resources one admission depends on, so a service can retain exactly what a
// replaced generation's command still needs.
type resourceRunner struct {
	fakeCommandRunner
	resources []io.Closer
}

func (r *resourceRunner) CommandResources() []io.Closer { return r.resources }

// countingCloser records how many times Close ran.
type countingCloser struct {
	mu     sync.Mutex
	closed int
}

func (c *countingCloser) Close() error {
	c.mu.Lock()
	c.closed++
	c.mu.Unlock()
	return nil
}

func (c *countingCloser) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// TestJobCommittedPolicyChangesFutureLaunchesOnly pins the reload contract at
// the service: committing a new policy re-times, redirects and re-limits
// NOTHING that is already running, and every later launch takes the new
// defaults — including the new generation, so a launch from one generation is
// distinguishable from another even when the display id repeats.
func TestJobCommittedPolicyChangesFutureLaunchesOnly(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	firstStarted := make(chan struct{})
	first := newFakeRunner(withRelease(release), withPID(111), withStarted(firstStarted))
	jm := NewCommandService(ctx, LaunchPolicy{
		Generation:     1,
		Runner:         first,
		Dir:            "/dir-one",
		MaxOutputBytes: 64,
		MaxJobs:        25,
		Retention:      time.Hour,
	})
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "long", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if info.Origin.Generation != 1 {
		t.Fatalf("admitted generation = %d, want 1", info.Origin.Generation)
	}
	// The process exists: OnStart published its PID before the runner blocked.
	<-firstStarted

	// A reload: a new runner, a new directory, a new output policy, a new
	// capacity, and a new generation.
	secondStarted := make(chan struct{})
	second := newFakeRunner(withResult(CommandResult{Stdout: "from-second-runner\n", ExitCode: 0}), withPID(222), withStarted(secondStarted))
	jm.CommitLaunchPolicy(LaunchPolicy{
		Generation:     2,
		Runner:         second,
		Dir:            "/dir-two",
		MaxOutputBytes: 4096,
		MaxJobs:        1,
		Retention:      time.Hour,
	})
	if got := jm.Generation(); got != 2 {
		t.Fatalf("generation after commit = %d, want 2", got)
	}

	// The admitted command keeps its captured snapshot in full.
	live, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if live.Dir != "/dir-one" {
		t.Fatalf("admitted dir = %q, want the captured /dir-one", live.Dir)
	}
	if live.ExecutionID != info.ExecutionID || live.Origin.Generation != 1 {
		t.Fatalf("admitted identity changed: %+v (was %+v)", live, info)
	}
	if live.Status != StatusRunning {
		t.Fatalf("admitted status = %q, want running: a policy commit must not stop it", live.Status)
	}
	if live.PID != 111 {
		t.Fatalf("admitted PID = %d, want the same 111", live.PID)
	}
	if got := first.countRuns(); got != 1 {
		t.Fatalf("old runner runs = %d, want 1 (the new runner must not be substituted)", got)
	}

	// It still runs to its own conclusion under its own runner.
	close(release)
	settled, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.Status != StatusCompleted || settled.Origin.Generation != 1 {
		t.Fatalf("settled = %q/gen %d, want completed at generation 1", settled.Status, settled.Origin.Generation)
	}
	if got := second.countRuns(); got != 0 {
		t.Fatalf("new runner runs = %d, want 0: an admitted command keeps its captured runner", got)
	}

	// A later launch resolves the committed generation wholesale.
	next, err := jm.Launch(ctx, LaunchSpec{Command: "short", Detached: true})
	if err != nil {
		t.Fatalf("launch after commit: %v", err)
	}
	if next.Origin.Generation != 2 {
		t.Fatalf("new launch generation = %d, want 2", next.Origin.Generation)
	}
	<-secondStarted
	// The second runner has no release channel, so it settles on its own.
	// Wait for the terminal snapshot rather than assuming a status change:
	// the launch may already have completed by the time we look.
	nextInfo, _, err := jm.Output(next.ID, 0)
	if err != nil {
		t.Fatalf("Output(new): %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for nextInfo.Status != StatusCompleted && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		nextInfo, _, err = jm.Output(next.ID, 0)
		if err != nil {
			t.Fatalf("Output(new): %v", err)
		}
	}
	if nextInfo.Status != StatusCompleted {
		t.Fatalf("new launch status = %q, want completed", nextInfo.Status)
	}
	if nextInfo.Dir != "/dir-two" {
		t.Fatalf("new launch dir = %q, want the committed /dir-two", nextInfo.Dir)
	}
	if nextInfo.PID != 222 {
		t.Fatalf("new launch PID = %d, want the committed runner's 222", nextInfo.PID)
	}
	if got := second.countRuns(); got != 1 {
		t.Fatalf("new runner runs = %d, want 1", got)
	}
}

// TestJobRetainsReplacedGenerationResourceUntilItsCommandSettles pins the
// resource rule: a reload keeps exactly what an admitted command still
// references and closes the rest, rather than retaining a whole generation
// wholesale or dropping a backend out from under a running process.
func TestJobRetainsReplacedGenerationResourceUntilItsCommandSettles(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	backend := &countingCloser{}
	first := &resourceRunner{
		fakeCommandRunner: *newFakeRunner(withRelease(release)),
		resources:         []io.Closer{backend},
	}
	jm := NewCommandService(ctx, LaunchPolicy{
		Generation: 1,
		Runner:     first,
		Dir:        "/dir",
		MaxJobs:    25,
		Retention:  time.Hour,
		Resources:  []io.Closer{backend},
	})
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "long", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// The reload swaps the backend for a new one.
	replacement := &countingCloser{}
	jm.CommitLaunchPolicy(LaunchPolicy{
		Generation: 2,
		Runner:     &resourceRunner{fakeCommandRunner: *newFakeRunner(), resources: []io.Closer{replacement}},
		Dir:        "/dir",
		MaxJobs:    25,
		Retention:  time.Hour,
		Resources:  []io.Closer{replacement},
	})

	if got := backend.count(); got != 0 {
		t.Fatalf("old backend closed %d times while its command is still running, want 0", got)
	}

	// Once the command it admitted has settled, the replaced backend is
	// released — and released exactly once.
	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for backend.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := backend.count(); got != 1 {
		t.Fatalf("old backend closed %d times after its command settled, want exactly 1", got)
	}
	if got := replacement.count(); got != 0 {
		t.Fatalf("the live generation's backend closed %d times, want 0", got)
	}
}

func TestJobKillWaitsForRunner(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	id, err := launchBackground(ctx, jm, "sleep 30")
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

	id, err := launchBackground(ctx, jm, "true")
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

	_, err := launchBackground(context.Background(), jm, "sleep 30")
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
	id, err := launchBackground(ctx, jm, "sleep 30")
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

	// After Shutdown returns, a launch should return ErrJobManagerClosed.
	_, err = launchBackground(context.Background(), jm, "echo hi")
	if !errors.Is(err, ErrJobManagerClosed) {
		t.Fatalf("launch after shutdown: err=%v, want ErrJobManagerClosed", err)
	}
}

func TestJobManagerShutdownHonorsCallerDeadline(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{}) // Never released.
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)

	_, err := launchBackground(ctx, jm, "sleep 30")
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

	id, err := launchBackground(context.Background(), jm, "echo hello")
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
	runner := &fakeRunner{}

	blocked := false
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		SessionState:  state,
		JobManager:    newTestCommandService(t, runner, root, state, nil),
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
	runner := newFakeRunner(withRelease(make(chan struct{})))
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, SessionState: state, JobManager: newTestCommandService(t, runner, root, state, nil)}); err != nil {
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
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state, JobManager: newTestCommandService(t, nil, root, state, nil)}); err != nil {
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
	// The same blocking runner is the toolset's CommandRunner: a managed
	// launch captures the runner it was built with, so a fixture that wired
	// only the manager would leave the tool on the real runner.
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release))
	jm := NewJobManager(context.Background(), runner, root, 1, cfg.Tools.Shell.BackgroundRetention, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	reg := registry.New()
	state := session.New(cfg, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, Config: cfg, SessionState: state, JobManager: jm}); err != nil {
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

	if _, err := launchBackground(context.Background(), jm, "sleep 1"); err != nil {
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
	// exitCalled is signalled by the exit sink. The assertion below is about the
	// callbacks having run, so it must wait for the callback rather than for
	// Wait's return: a Wait that starts after the execution settled observes the
	// terminal snapshot immediately (Wait's early-return path) and can therefore
	// return before the sink has been offered the completion.
	exitCalled := make(chan struct{}, 1)
	jm.SetOnChange(func(int) {
		// Re-enters the manager while a change is being reported.
		_ = jm.ActiveExecutions()
		_ = jm.List()
		_ = jm.ActiveCount()
	})
	jm.SetOnExit(func(info JobInfo, r time.Duration, output string) error {
		mu.Lock()
		exitInfo = append(exitInfo, info)
		ran = append(ran, r)
		outputs = append(outputs, output)
		mu.Unlock()
		select {
		case exitCalled <- struct{}{}:
		default:
		}
		// Re-enters the manager from the exit callback too.
		_ = jm.List()
		return nil
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
	// The exit sink runs after the execution settles, which is also when Wait can
	// already have returned, so the callback is awaited explicitly.
	select {
	case <-exitCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("the exit callback never ran for the settled execution")
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

	id, err := launchBackground(context.Background(), jm, "echo hi")
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

	id, err := launchBackground(context.Background(), jm, "sleep 30")
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
	_, _ = launchBackground(context.Background(), jm, "sleep 30")
	_, _ = launchBackground(context.Background(), jm, "sleep 30")
	_, err := launchBackground(context.Background(), jm, "sleep 30")
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

	_, err := launchBackground(context.Background(), jm, "echo hi")
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

	// After admission, change everything the manager could re-resolve for a
	// FUTURE launch: the committed runner, directory and output defaults.
	perLaunch2 := newFakeRunner(withResult(CommandResult{Stdout: "late", ExitCode: 0}))
	jm.CommitLaunchPolicy(LaunchPolicy{
		Generation:     jm.Generation() + 1,
		Runner:         perLaunch2,
		Dir:            "/after-admission",
		DirFunc:        func() string { return "/dir-fn" },
		MaxOutputBytes: 8,
	})

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
	id, err := launchBackground(context.Background(), jm, "echo out-line; echo err-line 1>&2")
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

	id, err := launchBackground(ctx, jm, "sleep 30")
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
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		mu.Lock()
		got = append(got, info)
		mu.Unlock()
		return nil
	})
	id, err := launchBackground(ctx, jm, "echo hi")
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
	_, err := launchBackground(context.Background(), jm, "echo hello")
	if err == nil {
		t.Fatal("expected error when managerCtx is cancelled, got nil")
	}
}

// ---------------------------------------------------------------------------
// Task 4: one completion per execution, and only a detached one is reported
// ---------------------------------------------------------------------------

// A foreground command's outcome is the waiting caller's tool result, which the
// agent audits. Reporting it to the exit sink as well would settle one
// execution twice, so the sink must only ever see detached completions.
func TestOnExitReportsDetachedCompletionsOnly(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release), withResult(CommandResult{Stdout: "out\n", ExitCode: 0}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	var mu sync.Mutex
	var reported []string
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		mu.Lock()
		reported = append(reported, info.ID)
		mu.Unlock()
		return nil
	})

	foreground, err := jm.Launch(ctx, LaunchSpec{Command: "foreground"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	close(release)
	if _, err := jm.Wait(ctx, foreground.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), reported...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("a foreground completion reached the exit sink: %v", got)
	}

	detached, err := jm.Launch(ctx, LaunchSpec{Command: "detached", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if _, err := jm.Wait(ctx, detached.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	// Reading the outcome again must not report it again: one execution has one
	// completion.
	if _, err := jm.Wait(ctx, detached.ID); err != nil {
		t.Fatalf("second Wait: %v", err)
	}

	mu.Lock()
	got = append([]string(nil), reported...)
	mu.Unlock()
	if len(got) != 1 || got[0] != detached.ID {
		t.Fatalf("reports = %v, want exactly one, for the detached job %q", got, detached.ID)
	}
}

// A sink that cannot store a completion leaves it pending: nothing may announce
// an outcome the session cannot restore, and a retry must be able to offer it
// again.
func TestSettlementIsRetainedWhenTheSinkFails(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner(withResult(CommandResult{Stdout: "late\n", ExitCode: 0}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	var mu sync.Mutex
	failing := true
	attempts := 0
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if failing {
			return errors.New("store unavailable")
		}
		return nil
	})

	info, err := jm.Launch(ctx, LaunchSpec{Command: "detached", Detached: true})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	pending := jm.PendingSettlements()
	if len(pending) != 1 || pending[0].Info.ID != info.ID || pending[0].Err == nil {
		t.Fatalf("pending = %+v, want the failed settlement retained", pending)
	}

	mu.Lock()
	failing = false
	mu.Unlock()
	if left := jm.RetrySettlements(); len(left) != 0 {
		t.Fatalf("RetrySettlements left %+v, want a clean retry", left)
	}
	if left := jm.PendingSettlements(); len(left) != 0 {
		t.Fatalf("pending after a successful retry = %+v", left)
	}
	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 2 {
		t.Fatalf("sink calls = %d, want the failure plus exactly one retry", got)
	}
}

// A settled snapshot carries the evidence a stored completion needs: the
// identity that pairs it across a resume, the observed exit, the retained tail
// and the truncation flag.
func TestSettledJobInfoCarriesExecutionEvidence(t *testing.T) {
	ctx := context.Background()
	// A natural non-zero exit is reported by the runner as both an exit code
	// and its own error, which is how classifySettlement tells it apart from a
	// clean completion.
	runner := newFakeRunner(
		withResult(CommandResult{
			Stdout: "tail\n", ExitCode: 2,
			Meta: registry.SandboxMeta{OutputTruncated: true},
		}),
		withRunErr(errors.New("exit status 2")),
	)
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	origin := CommandOrigin{
		ConversationID: "conv-1", Actor: "implementer",
		TurnID: "turn-1", StepID: "step-1", ToolCallID: "call-1", Generation: 7,
	}
	info, err := jm.Launch(ctx, LaunchSpec{Command: "go test", Detached: true, Wait: 10 * time.Second, Origin: origin})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	settled, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}

	if settled.ExecutionID != ExecutionIdentity(info.ID, origin) {
		t.Fatalf("ExecutionID = %q, want %q", settled.ExecutionID, ExecutionIdentity(info.ID, origin))
	}
	if settled.EndedAt.IsZero() || settled.EndedAt.Before(settled.StartedAt) {
		t.Fatalf("EndedAt = %v, want the settlement time", settled.EndedAt)
	}
	if settled.ExitCode == nil || *settled.ExitCode != 2 {
		t.Fatalf("ExitCode = %v, want the observed 2", settled.ExitCode)
	}
	if settled.OutputTail == "" {
		t.Fatal("a settled snapshot must carry its retained tail")
	}

	ex := settled.Execution()
	if ex.ExecutionID != settled.ExecutionID || ex.DisplayID != info.ID {
		t.Fatalf("execution identity in payload = %+v, want %q / %q", ex, settled.ExecutionID, info.ID)
	}
	if ex.ConversationID != "conv-1" || ex.Actor != "implementer" || ex.TurnID != "turn-1" ||
		ex.StepID != "step-1" || ex.ToolCallID != "call-1" || ex.Generation != 7 {
		t.Fatalf("origin lost in payload: %+v", ex)
	}
	if ex.Attachment != registry.ExecutionAttachmentBackground {
		t.Fatalf("attachment = %q, want background", ex.Attachment)
	}
	if ex.State != string(StatusFailed) || ex.Outcome != string(OutcomeNonZeroExit) {
		t.Fatalf("state/outcome = %q/%q, want failed/nonzero_exit", ex.State, ex.Outcome)
	}
	if ex.ExitCode == nil || *ex.ExitCode != 2 || !ex.Truncated || ex.Wait != "10s" {
		t.Fatalf("payload evidence = %+v", ex)
	}
	if ex.StartedAt.IsZero() || ex.EndedAt.IsZero() {
		t.Fatalf("payload times = %v/%v, want both recorded", ex.StartedAt, ex.EndedAt)
	}
}

// ---------------------------------------------------------------------------
// Task 6: regressions driven by a real JobManager, not a fake await source
// ---------------------------------------------------------------------------

// TestManagedForegroundLaunchSharesOneExecutionWithItsHandoff pins the whole
// managed foreground path against the real service: one launch, one identity,
// one PID, and no wall-clock timer anywhere in it.
//
// The launch is not a second admission when the wait ends, so the runner sees
// exactly one Run call and the execution keeps the identity the caller already
// holds.
func TestManagedForegroundLaunchSharesOneExecutionWithItsHandoff(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release), withPID(51015), withLiveOutput("early line\n"))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	origin := CommandOrigin{ConversationID: "conv-9", Actor: "parent", TurnID: "turn-9", StepID: "step-1", ToolCallID: "call-1"}
	info, err := jm.Launch(ctx, LaunchSpec{
		Command:        "long download",
		MaxOutputBytes: 1 << 20,
		Origin:         origin,
		Wait:           time.Second,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	res, err := jm.WaitForeground(ctx, info.ID, time.Second)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Terminal {
		t.Fatalf("result = %+v, want a continuation", res)
	}
	if res.Info.ExecutionID != info.ExecutionID || res.Info.PID != 51015 {
		t.Fatalf("continuation identity = %q/pid %d, want the same %q/51015", res.Info.ExecutionID, res.Info.PID, info.ExecutionID)
	}
	if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment = %q, want background after the wait ended", res.Info.Attachment)
	}
	if res.RunError != nil || res.Info.ExitCode != nil {
		t.Fatalf("continuation = %+v, want no exit and no error", res)
	}
	if !strings.Contains(res.Output, "early line") {
		t.Fatalf("continuation output = %q, want the recent tail", res.Output)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1", got)
	}

	// A continuation is outstanding background work, so another await can
	// still find it.
	outstanding := jm.Outstanding()
	if len(outstanding) != 1 || outstanding[0].ID != info.ID {
		t.Fatalf("Outstanding = %+v, want the handed-off execution", outstanding)
	}

	// Release it and confirm the natural outcome arrives on the same identity.
	close(release)
	settled, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if settled.Status != StatusCompleted || settled.Outcome != OutcomeCompleted {
		t.Fatalf("settled = %q/%q, want completed", settled.Status, settled.Outcome)
	}
	if settled.ExitCode == nil || *settled.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want the actual 0", settled.ExitCode)
	}
}

// TestManagedForegroundCompletionIsNotOutstandingWork pins the attachment
// split the await scan depends on: a foreground command that settles while its
// caller waits was never background work, so it must not appear as an awaitable
// target afterwards.
func TestManagedForegroundCompletionIsNotOutstandingWork(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner(withResult(CommandResult{Stdout: "ok\n", ExitCode: 0}))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "quick", Origin: CommandOrigin{TurnID: "turn-1"}})
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
	if got := jm.Outstanding(); len(got) != 0 {
		t.Fatalf("Outstanding = %+v, want none for a foreground completion", got)
	}
	if got := jm.RunningCount(); got != 0 {
		t.Fatalf("RunningCount = %d, want 0", got)
	}
}

// TestManagedForegroundWaitExpiryKeepsTheProcessAlive pins that an expired wait
// never terminates the process: the runner is still blocked afterwards, exactly
// one process exists, and no stop was recorded.
func TestManagedForegroundWaitExpiryKeepsTheProcessAlive(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "hang", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	for i := 0; i < 3; i++ {
		res, err := jm.WaitForeground(ctx, info.ID, 20*time.Millisecond)
		if err != nil {
			t.Fatalf("WaitForeground %d: %v", i, err)
		}
		if res.Terminal {
			t.Fatalf("wait %d returned %+v, want a continuation", i, res)
		}
		if res.WaitEnd != WaitEndExpired {
			t.Fatalf("wait %d WaitEnd = %q, want expired", i, res.WaitEnd)
		}
	}

	live, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if live.Status != StatusRunning {
		t.Fatalf("status = %q, want running after repeated expired waits", live.Status)
	}
	if live.StopCause != "" {
		t.Fatalf("StopCause = %q, want none: a wait expiry must not stop anything", live.StopCause)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want 1 (no relaunch)", got)
	}

	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestManagedCanceledWaitLeavesOtherWorkRunning pins any/all cleanup semantics
// at the service boundary: ending one caller's wait cancels that wait only, so
// every other execution keeps running and nobody is stopped.
func TestManagedCanceledWaitLeavesOtherWorkRunning(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	worker := newFakeRunner(withRelease(release))
	jm := NewJobManager(ctx, worker, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	winner, err := jm.Launch(ctx, LaunchSpec{Command: "winner", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch winner: %v", err)
	}
	loser, err := jm.Launch(ctx, LaunchSpec{Command: "loser", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch loser: %v", err)
	}
	// Neither launch is complete until both goroutines have reached the runner:
	// the assertion below counts Run calls, and admission does not wait for the
	// goroutine it starts.
	waitForRunCount(t, worker, 2, time.Second)

	// Cancel the wait on the loser, exactly as an any-winner cleanup does.
	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	res, err := jm.WaitForeground(waitCtx, loser.ID, time.Hour)
	if err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	if res.Terminal || res.WaitEnd != WaitEndCanceled {
		t.Fatalf("result = %+v, want a canceled continuation", res)
	}

	for _, id := range []string{winner.ID, loser.ID} {
		info, _, err := jm.Output(id, 0)
		if err != nil {
			t.Fatalf("Output(%s): %v", id, err)
		}
		if info.Status != StatusRunning {
			t.Fatalf("job %s status = %q, want running after a sibling's canceled wait", id, info.Status)
		}
		if info.StopCause != "" {
			t.Fatalf("job %s StopCause = %q, want none", id, info.StopCause)
		}
	}
	if got := worker.countRuns(); got != 2 {
		t.Fatalf("runner runs = %d, want 2 (one per launch, no relaunch)", got)
	}

	close(release)
	for _, id := range []string{winner.ID, loser.ID} {
		if _, err := jm.Wait(ctx, id); err != nil {
			t.Fatalf("Wait(%s): %v", id, err)
		}
	}
}

// waitForRunCount blocks until the runner has been entered count times, so a
// test can assert on launches without racing the goroutines admission starts.
func waitForRunCount(t *testing.T, runner *fakeCommandRunner, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for runner.countRuns() < count {
		if time.Now().After(deadline) {
			t.Fatalf("runner entered %d times, want %d", runner.countRuns(), count)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestManagedPeekLeavesAnExecutionUntouched pins that a peek (the manager's
// Output) is read-only: it reports the live state and tail without detaching,
// stopping or otherwise changing the execution.
func TestManagedPeekLeavesAnExecutionUntouched(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	started := make(chan struct{})
	runner := newFakeRunner(withStarted(started), withRelease(release), withLiveOutput("peekable\n"))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	info, err := jm.Launch(ctx, LaunchSpec{Command: "download", Origin: CommandOrigin{TurnID: "turn-1"}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	// Peek while still attached: the state and the tail are visible and the
	// attachment is unchanged.
	peeked, tail, err := jm.Output(info.ID, 20)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if peeked.Status != StatusRunning || peeked.Attachment != AttachmentForeground {
		t.Fatalf("peek = %q/%q, want running/foreground", peeked.Status, peeked.Attachment)
	}
	if !strings.Contains(tail, "peekable") {
		t.Fatalf("tail = %q, want the live output", tail)
	}

	// After handing off, a peek reports the same execution as background work.
	if _, err := jm.WaitForeground(ctx, info.ID, 0); err != nil {
		t.Fatalf("WaitForeground: %v", err)
	}
	peeked, _, err = jm.Output(info.ID, 20)
	if err != nil {
		t.Fatalf("Output after handoff: %v", err)
	}
	if peeked.Attachment != AttachmentBackground || peeked.Status != StatusRunning {
		t.Fatalf("peek after handoff = %q/%q, want running/background", peeked.Status, peeked.Attachment)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want 1 (a peek must not relaunch)", got)
	}

	close(release)
	if _, err := jm.Wait(ctx, info.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestManagedHandoffSurvivesRepeatedExpiriesAndAReloadThenSettlesOnce is Task
// 12's composition regression at the service boundary: it chains the whole
// lifecycle a background command lives through — handoff, repeated await
// expiries, a committed relaunch policy (the reload's commit) and natural
// completion — and asserts the user-visible invariants hold across all of it:
// one launch, one process identity, the settings captured at admission, and
// exactly one completion record.
func TestManagedHandoffSurvivesRepeatedExpiriesAndAReloadThenSettlesOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{})
	// The output is deliberately longer than the cap the reload commits below:
	// a running command must keep what it captured at admission.
	runner := newFakeRunner(withStarted(started), withRelease(release), withPID(7311), withLiveOutput("admitted-output-line\n"))
	jm := NewCommandService(ctx, LaunchPolicy{
		Generation:     1,
		Runner:         runner,
		Dir:            dir,
		MaxOutputBytes: 64,
		Retention:      time.Hour,
	})
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	settlements := make(chan JobInfo, 4)
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		settlements <- info
		return nil
	})

	info, err := jm.Launch(ctx, LaunchSpec{
		Command: "long download",
		Origin:  CommandOrigin{ConversationID: "conv-1", Actor: "parent", TurnID: "turn-1"},
		Wait:    10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	<-started

	// 1. The foreground wait expires and hands the still-running command off.
	res, err := jm.WaitForeground(ctx, info.ID, time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForeground (handoff): %v", err)
	}
	if res.Terminal || res.WaitEnd != WaitEndExpired {
		t.Fatalf("handoff = %+v, want a continuation from an expired wait", res)
	}
	if res.Info.Attachment != AttachmentBackground {
		t.Fatalf("attachment after the handoff = %q, want background", res.Info.Attachment)
	}

	// 2. Repeated await expiries: each ends only the wait. None of them stops,
	// relaunches or re-times the command.
	for i := 0; i < 3; i++ {
		await, err := jm.WaitForeground(ctx, info.ID, 5*time.Millisecond)
		if err != nil {
			t.Fatalf("await %d: %v", i, err)
		}
		if await.Terminal || await.WaitEnd != WaitEndExpired {
			t.Fatalf("await %d = %+v, want a continuation", i, await)
		}
		if await.Info.ExecutionID != info.ExecutionID || await.Info.PID != 7311 {
			t.Fatalf("await %d identity = %q/pid %d, want the admitted %q/7311",
				i, await.Info.ExecutionID, await.Info.PID, info.ExecutionID)
		}
		if await.RunError != nil || await.Info.ExitCode != nil {
			t.Fatalf("await %d = %+v, want no exit and no error", i, await)
		}
	}

	// 3. A reload commits a new launch generation with a smaller output cap.
	closed := jm.CommitLaunchPolicy(LaunchPolicy{
		Generation:     2,
		Runner:         runner,
		Dir:            dir,
		MaxOutputBytes: 8,
		Retention:      time.Hour,
	})
	if len(closed) != 0 {
		t.Fatalf("the reload released %d resources with none referenced", len(closed))
	}
	if got := jm.Generation(); got != 2 {
		t.Fatalf("generation after the reload = %d, want 2", got)
	}

	// The running command is untouched by the reload: same identity, same PID,
	// still running, with no stop recorded and still outstanding background work.
	live, _, err := jm.Output(info.ID, 0)
	if err != nil {
		t.Fatalf("Output after the reload: %v", err)
	}
	if live.Status != StatusRunning || live.PID != 7311 || live.ExecutionID != info.ExecutionID {
		t.Fatalf("live after the reload = %q/pid %d/%q, want running/7311/%q", live.Status, live.PID, live.ExecutionID, info.ExecutionID)
	}
	if live.StopCause != "" {
		t.Fatalf("StopCause after the reload = %q, want none", live.StopCause)
	}
	if outstanding := jm.Outstanding(); len(outstanding) != 1 || outstanding[0].ID != info.ID {
		t.Fatalf("Outstanding after the reload = %+v, want the handed-off command", outstanding)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1 across handoff, expiries and reload", got)
	}

	// 4. Natural completion, once.
	close(release)
	final, err := jm.Wait(ctx, info.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.Status != StatusCompleted || final.Outcome != OutcomeCompleted {
		t.Fatalf("settled = %q/%q, want completed/completed", final.Status, final.Outcome)
	}
	if final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want the natural 0", final.ExitCode)
	}
	if final.ExecutionID != info.ExecutionID || final.PID != 7311 {
		t.Fatalf("identity changed across the lifecycle: %q/pid %d", final.ExecutionID, final.PID)
	}
	// The command kept the output policy it captured at admission, not the one
	// the reload committed: the retained tail is longer than the new 8-byte cap.
	if !strings.Contains(final.OutputTail, "admitted-output-line") {
		t.Fatalf("retained tail = %q, want the output captured under the admission policy", final.OutputTail)
	}
	if got := jm.CurrentLaunchPolicy().MaxOutputBytes; got != 8 {
		t.Fatalf("committed output cap = %d, want the reloaded 8 for future launches", got)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs after completion = %d, want exactly 1", got)
	}

	// Exactly one completion reached the durable sink, for this execution.
	select {
	case got := <-settlements:
		if got.ExecutionID != info.ExecutionID {
			t.Fatalf("settlement identity = %q, want the admitted %q", got.ExecutionID, info.ExecutionID)
		}
		if got.Outcome != OutcomeCompleted || got.Attachment != AttachmentBackground {
			t.Fatalf("settlement = %q/%q, want completed/background", got.Outcome, got.Attachment)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handed-off completion never reached the exit sink")
	}
	select {
	case extra := <-settlements:
		t.Fatalf("a second completion was reported for the same execution: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
	if pending := jm.PendingSettlements(); len(pending) != 0 {
		t.Fatalf("PendingSettlements = %+v, want none after a stored completion", pending)
	}
}

// TestManagedStopWaitsForTheActualOutcome pins the explicit-stop rule at the
// service boundary: stopping a still-attached command reports the process's own
// outcome rather than a continuation, and a detached sibling is untouched.
func TestManagedStopWaitsForTheActualOutcome(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release), withResult(CommandResult{ExitCode: -1}))
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

	// The caller waits; the turn stop is accepted while it is still attached.
	waited := make(chan ForegroundResult, 1)
	go func() {
		res, err := jm.WaitForeground(ctx, attached.ID, time.Hour)
		if err != nil {
			t.Errorf("WaitForeground: %v", err)
			waited <- ForegroundResult{}
			return
		}
		waited <- res
	}()
	time.Sleep(30 * time.Millisecond)

	ids, err := jm.StopAttached("turn-1", "user stopped the turn")
	if err != nil {
		t.Fatalf("StopAttached: %v", err)
	}
	if len(ids) != 1 || ids[0] != attached.ID {
		t.Fatalf("stopped = %v, want only the attached %s", ids, attached.ID)
	}

	// The detached job survived its turn's stop: it is still running while the
	// attached command's termination is still settling.
	alive, _, err := jm.Output(detached.ID, 0)
	if err != nil {
		t.Fatalf("Output detached: %v", err)
	}
	if alive.Status != StatusRunning {
		t.Fatalf("detached status = %q, want running: a detached job survives its turn's stop", alive.Status)
	}
	if alive.StopCause != "" {
		t.Fatalf("detached StopCause = %q, want none", alive.StopCause)
	}

	select {
	case res := <-waited:
		if !res.Terminal {
			t.Fatalf("result = %+v, want the terminal stopped outcome", res)
		}
		if res.Info.Outcome != OutcomeStopped {
			t.Fatalf("outcome = %q, want stopped", res.Info.Outcome)
		}
		if res.Info.StopCause != "user stopped the turn" {
			t.Fatalf("StopCause = %q, want the turn's cause", res.Info.StopCause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped command's outcome never reached the waiting caller")
	}

	close(release)
	if _, err := jm.Wait(ctx, detached.ID); err != nil {
		t.Fatalf("Wait detached: %v", err)
	}
}

// partialBatchRunner lets one admitted command finish naturally and blocks the
// rest, so a turn's parallel batch has a settled member, an attached member and
// a member that hands off — all three at once, which is the batch shape a stop
// has to arbitrate.
type partialBatchRunner struct {
	// settled is the command that completes immediately instead of blocking.
	settled  string
	finished chan string
	release  chan struct{}
	started  chan struct{}
	once     sync.Once
	// runs counts how many executions actually reached the runner, so a refused
	// batch member that never started is visible as one run too few.
	runs atomic.Int32
}

func (r *partialBatchRunner) countRuns() int { return int(r.runs.Load()) }

func (r *partialBatchRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	r.runs.Add(1)
	if req.OnStart != nil {
		req.OnStart(8080)
	}
	r.once.Do(func() { close(r.started) })
	if req.Command == r.settled {
		r.finished <- req.Command
		return CommandResult{Stdout: "already done\n", ExitCode: 0}, nil
	}
	select {
	case <-r.release:
		return CommandResult{Stdout: "batch sibling finished\n", ExitCode: 0}, nil
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	}
}

// TestTurnStopOfAParallelBatchStopsOnlyItsStillAttachedMembers pins Task 12's
// partial-batch rule: one turn's stop acts on exactly the members still attached
// to it. A member that already settled keeps its natural outcome, a member that
// already handed off keeps running, and only the still-attached member is
// stopped — each with its own single settlement.
func TestTurnStopOfAParallelBatchStopsOnlyItsStillAttachedMembers(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	finished := make(chan string, 1)
	started := make(chan struct{})
	runner := &partialBatchRunner{settled: "batch-settled", finished: finished, release: release, started: started}
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 1<<20)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	settlements := make(chan JobInfo, 4)
	jm.SetOnExit(func(info JobInfo, ran time.Duration, output string) error {
		settlements <- info
		return nil
	})

	origin := CommandOrigin{ConversationID: "conv-batch", Actor: "parent", TurnID: "turn-batch"}
	// Member 1: completes while the batch is still running.
	settled, err := jm.Launch(ctx, LaunchSpec{Command: "batch-settled", Detached: true, Origin: origin})
	if err != nil {
		t.Fatalf("Launch settled: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the first batch member never ran")
	}
	if got, err := jm.Wait(ctx, settled.ID); err != nil || got.Status != StatusCompleted {
		t.Fatalf("first member = %+v (%v), want completed before the stop", got, err)
	}

	// Member 2: handed off to the background by an expired wait. Member 3 is
	// launched foreground and detached the same way, so both are background work
	// while the third admission below remains attached.
	handed, err := jm.Launch(ctx, LaunchSpec{Command: "batch-handed-off", Origin: origin})
	if err != nil {
		t.Fatalf("Launch handed off: %v", err)
	}
	alsoHanded, err := jm.Launch(ctx, LaunchSpec{Command: "batch-also-handed-off", Origin: origin})
	if err != nil {
		t.Fatalf("Launch also handed off: %v", err)
	}
	<-started
	if res, err := jm.WaitForeground(ctx, handed.ID, time.Millisecond); err != nil || res.Terminal {
		t.Fatalf("handoff = %+v (%v), want a continuation", res, err)
	}
	if res, err := jm.WaitForeground(ctx, alsoHanded.ID, time.Millisecond); err != nil || res.Terminal {
		t.Fatalf("second handoff = %+v (%v), want a continuation", res, err)
	}
	// Member 3: still attached when the stop arrives, with a caller waiting on
	// it exactly as the tool call that admitted it would be.
	stillAttached, err := jm.Launch(ctx, LaunchSpec{Command: "batch-still-attached", Origin: origin})
	if err != nil {
		t.Fatalf("Launch still-attached: %v", err)
	}
	type batchResult struct {
		res ForegroundResult
		err error
	}
	attachedOutcome := make(chan batchResult, 1)
	go func() {
		res, err := jm.WaitForeground(ctx, stillAttached.ID, time.Hour)
		attachedOutcome <- batchResult{res: res, err: err}
	}()

	ids, err := jm.StopAttached("turn-batch", "user stopped the turn")
	if err != nil {
		t.Fatalf("StopAttached: %v", err)
	}
	if len(ids) != 1 || ids[0] != stillAttached.ID {
		t.Fatalf("stopped = %v, want only the still-attached %s", ids, stillAttached.ID)
	}

	// The settled member kept its natural outcome: the stop never relabelled it.
	kept, _, err := jm.Output(settled.ID, 0)
	if err != nil {
		t.Fatalf("Output settled: %v", err)
	}
	if kept.Status != StatusCompleted || kept.Outcome != OutcomeCompleted {
		t.Fatalf("settled member = %q/%q, want completed/completed", kept.Status, kept.Outcome)
	}
	if kept.StopCause != "" {
		t.Fatalf("settled member StopCause = %q, want none", kept.StopCause)
	}
	// The handed-off members keep running: a detached job survives its turn's
	// stop, whoever else the stop reached.
	for _, member := range []JobInfo{handed, alsoHanded} {
		live, _, err := jm.Output(member.ID, 0)
		if err != nil {
			t.Fatalf("Output(%s): %v", member.ID, err)
		}
		if live.Status != StatusRunning || live.StopCause != "" {
			t.Fatalf("handed-off member %s = %q/%q, want running with no stop cause", member.ID, live.Status, live.StopCause)
		}
	}
	// The stopped member's outcome reaches the caller that was waiting on it —
	// it was never background work, so it produces one tool result, not a second
	// completion card.
	select {
	case got := <-attachedOutcome:
		if got.err != nil {
			t.Fatalf("WaitForeground on the stopped member: %v", got.err)
		}
		if !got.res.Terminal {
			t.Fatalf("stopped member result = %+v, want the terminal outcome", got.res)
		}
		if got.res.Info.Outcome != OutcomeStopped || got.res.Info.StopCause != "user stopped the turn" {
			t.Fatalf("stopped member = %q/%q, want stopped with the turn's cause", got.res.Info.Outcome, got.res.Info.StopCause)
		}
		if got.res.Info.ExecutionID != stillAttached.ExecutionID {
			t.Fatalf("stopped member identity = %q, want the admitted %q", got.res.Info.ExecutionID, stillAttached.ExecutionID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped member's outcome never reached its waiting caller")
	}
	// The stopped turn admits nothing more, so an unlaunched batch member stays
	// unstarted: exactly the batch's four members were admitted (the refused one
	// never reached the runner), and none of them is missing from the record.
	if _, err := jm.Launch(ctx, LaunchSpec{Command: "batch-never-started", Origin: origin}); !errors.Is(err, ErrTurnStopped) {
		t.Fatalf("late batch admission = %v, want ErrTurnStopped", err)
	}
	if got := len(jm.List()); got != 4 {
		t.Fatalf("admitted executions = %d, want the batch's 4 (the refused member must not start)", got)
	}
	if got := runner.countRuns(); got != 4 {
		t.Fatalf("runner runs = %d, want 4: a refused batch member must not start a process", got)
	}

	// Exactly one detached completion reaches the durable sink — the member that
	// finished in the background — and the stopped member is not announced a
	// second time, because its outcome is its caller's tool result.
	seen := map[string]int{}
	for {
		select {
		case info := <-settlements:
			seen[info.ID]++
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
	if len(seen) != 1 || seen[settled.ID] != 1 {
		t.Fatalf("detached completions = %v, want exactly one for the settled member %s", seen, settled.ID)
	}

	close(release)
	for _, member := range []JobInfo{handed, alsoHanded} {
		if _, err := jm.Wait(ctx, member.ID); err != nil {
			t.Fatalf("Wait(%s): %v", member.ID, err)
		}
	}
}
