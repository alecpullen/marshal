package native

import (
	"context"
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
}

func (f *fakeCommandRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	atomic.AddInt32(&f.startedCount, 1)

	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	if req.OnStart != nil {
		req.OnStart(4242)
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

	if f.release != nil {
		select {
		case <-f.release:
			// Released normally.
		case <-ctx.Done():
			// Context was cancelled (Kill or timeout). Simulate the real
			// world where process cleanup takes a small amount of time
			// so Kill/Shutdown cannot return before the runner finishes.
			time.Sleep(200 * time.Millisecond)
		}
	}

	return f.result, f.runErr
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

func TestJobOutputIsLiveAndBounded(t *testing.T) {
	ctx := context.Background()
	runner := newFakeRunner(
		withLiveOutput("hello world\n"),
		withExtraBytes(200000), // Write well past a small limit.
	)
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	id, err := jm.Start(ctx, "echo hello", time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Output before the goroutine finishes should show the live output
	// (the runner has no release channel, so it returns immediately, but
	// the goroutine might not have written output yet).
	time.Sleep(50 * time.Millisecond)

	info, output, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output before release: %v", err)
	}
	if !strings.Contains(output, "hello world") {
		t.Fatalf("output before completion = %q, want to contain 'hello world'", output)
	}
	_ = info

	// Wait for completion and check truncation.
	waitForStatus(t, jm, id, StatusRunning, false, time.Second)

	info, output, err = jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output after completion: %v", err)
	}
	if !info.OutputTruncated {
		t.Fatalf("OutputTruncated should be true, info=%+v", info)
	}
	if !strings.Contains(output, "[output truncated]") {
		t.Fatalf("output = %q, want truncation marker", output)
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
	// Since only running jobs count toward the limit, job 2 must start.
	id2, err := jm.Start(ctx, "job2", time.Second)
	if err != nil {
		t.Fatalf("start job2 with maxJobs=1 and a completed retained job1: %v", err)
	}

	_ = id2
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

func TestJobTimeoutIsDistinctFromKill(t *testing.T) {
	ctx := context.Background()
	ch := make(chan struct{})
	runner := newFakeRunner(withRelease(ch))
	jm := NewJobManager(ctx, runner, t.TempDir(), 25, time.Hour, 100000)
	t.Cleanup(func() { _ = jm.Shutdown(context.Background()) })

	// Very short timeout — the job context will be cancelled before we
	// release the runner.
	id, err := jm.Start(ctx, "sleep 30", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the job to time out.
	waitForStatus(t, jm, id, StatusRunning, true, time.Second)

	info, _, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if info.Status != StatusTimedOut {
		t.Fatalf("status = %q, want timed_out", info.Status)
	}

	// Release the runner so the goroutine can finish.
	close(ch)
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
		Guardrail: func(command string) error {
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

	reg := registry.New()
	state := session.New(cfg, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, Config: cfg, SessionState: state}); err != nil {
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
	if _, err := invokeTool(t, reg, "job.kill", fmt.Sprintf(`{"job_id":"%s"}`, jobID)); err != nil {
		t.Fatalf("job.kill: %v", err)
	}
}

func TestJobOutputAndListTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	state := session.New(config.Config{}, root, time.Now(), session.Persistence{})
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	res, err := invokeTool(t, reg, "shell.run", `{"command":"sleep 30","background":true,"timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("start background job: %v", err)
	}
	jobID := strings.TrimPrefix(strings.TrimSpace(res.Content), "job_id: ")

	listRes, err := invokeTool(t, reg, "job.list", `{}`)
	if err != nil {
		t.Fatalf("job.list: %v", err)
	}
	if !strings.Contains(listRes.Content, jobID) {
		t.Fatalf("job.list content = %q, want %q", listRes.Content, jobID)
	}

	outRes, err := invokeTool(t, reg, "job.output", fmt.Sprintf(`{"job_id":"%s"}`, jobID))
	if err != nil {
		t.Fatalf("job.output: %v", err)
	}
	if !strings.Contains(outRes.Content, "status: running") {
		t.Fatalf("job.output content = %q, want status running", outRes.Content)
	}

	if _, err := invokeTool(t, reg, "job.kill", fmt.Sprintf(`{"job_id":"%s"}`, jobID)); err != nil {
		t.Fatalf("job.kill: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	outRes, err = invokeTool(t, reg, "job.output", fmt.Sprintf(`{"job_id":"%s"}`, jobID))
	if err != nil {
		t.Fatalf("job.output after kill: %v", err)
	}
	if !strings.Contains(outRes.Content, "status: killed") {
		t.Fatalf("job.output content after kill = %q, want status killed", outRes.Content)
	}
}

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
// Helpers
// ---------------------------------------------------------------------------

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
