package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

type fakeJobSource struct {
	mu          sync.Mutex
	outstanding []AwaitJobInfo
	wait        func(ctx context.Context, id string) (AwaitJobInfo, error)
	tail        string
	peek        func(id string, tailLines int) (AwaitJobInfo, string, error)
	killed      string
}

func (f *fakeJobSource) OutstandingJobs() []AwaitJobInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]AwaitJobInfo(nil), f.outstanding...)
}

func (f *fakeJobSource) AwaitJob(ctx context.Context, id string) (AwaitJobInfo, error) {
	return f.wait(ctx, id)
}

func (f *fakeJobSource) JobOutputTail(string, int) string { return f.tail }

func (f *fakeJobSource) PeekJob(id string, tailLines int) (AwaitJobInfo, string, error) {
	if f.peek == nil {
		return AwaitJobInfo{}, "", fmt.Errorf("unknown job %s", id)
	}
	return f.peek(id, tailLines)
}

func (f *fakeJobSource) KillJob(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = id
	return nil
}

func newAwaitTool(state *session.State, opts ...AwaitOption) registry.Tool {
	return NewSubagentAwaitTool(state, opts...)
}

func TestAgentAwaitValidatesModes(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	await := newAwaitTool(state)
	for _, args := range []string{
		`{"id": 1, "job_id": "job-1"}`,
		`{"any": true, "all": true}`,
		`{"any": true, "job_id": "job-1"}`,
		`{}`,
		`{"timeout_seconds": 5}`,
	} {
		if _, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(args)}); err == nil || !strings.Contains(err.Error(), "exactly one of") {
			t.Fatalf("args %s: err = %v, want exactly-one-of", args, err)
		}
	}
	if _, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"any": true, "timeout_seconds": -1}`)}); err == nil || !strings.Contains(err.Error(), ">= 0") {
		t.Fatalf("negative timeout: err = %v", err)
	}
}

func TestAgentAwaitJobIDWithoutSource(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	await := newAwaitTool(state)
	_, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"job_id": "job-1"}`)})
	if err == nil || !strings.Contains(err.Error(), "no background-job source") {
		t.Fatalf("err = %v, want no-source", err)
	}
}

// Review focus 5: peek must return a finished job's state and tail
// immediately, without waiting.
func TestAgentAwaitPeekFinishedJob(t *testing.T) {
	code := 1
	jobs := &fakeJobSource{peek: func(id string, _ int) (AwaitJobInfo, string, error) {
		if id != "job-1" {
			return AwaitJobInfo{}, "", fmt.Errorf("unknown job %s", id)
		}
		return AwaitJobInfo{ID: "job-1", Command: "go test", Status: "exited", ExitCode: &code}, "FAIL x", nil
	}}
	tool := newAwaitTool(newTestState(t), WithAwaitJobs(jobs))
	done := make(chan struct{})
	var res registry.ToolResult
	var err error
	go func() {
		res, err = tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-1","peek":true}`)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("peek blocked")
	}
	if err != nil || !strings.Contains(res.Content, "exited") || !strings.Contains(res.Content, "exit: 1") || !strings.Contains(res.Content, "FAIL x") {
		t.Fatalf("peek result = %+v, %v", res, err)
	}
}

func TestAgentAwaitPeekRequiresATarget(t *testing.T) {
	tool := newAwaitTool(newTestState(t))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"any":true,"peek":true}`)}); err == nil {
		t.Fatal("peek with any must be rejected")
	}
}

func TestAgentAwaitPeekJobWithoutSource(t *testing.T) {
	tool := newAwaitTool(newTestState(t))
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-1","peek":true}`)})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want jobs-not-available", err)
	}
}

func TestAgentKillKillsJob(t *testing.T) {
	jobs := &fakeJobSource{}
	tool := NewSubagentKillTool(newTestState(t), WithAwaitJobs(jobs))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-2"}`)}); err != nil || jobs.killed != "job-2" {
		t.Fatalf("kill job: err=%v killed=%q", err, jobs.killed)
	}
}

func TestAgentKillJobWithoutSource(t *testing.T) {
	tool := NewSubagentKillTool(newTestState(t))
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-2"}`)})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want jobs-not-available", err)
	}
}

func TestAgentAwaitAnyReturnsFirstAcrossClasses(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	jobs := &fakeJobSource{outstanding: []AwaitJobInfo{{ID: "job-1", Command: "sleep 5", Status: "running"}}}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		select {
		case <-time.After(30 * time.Millisecond):
			exit := 0
			return AwaitJobInfo{ID: id, Command: "sleep 5", Status: "completed", ExitCode: &exit}, nil
		case <-ctx.Done():
			return AwaitJobInfo{}, ctx.Err()
		}
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"any": true}`)})
	if err != nil {
		t.Fatalf("await any: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-1 completed") {
		t.Fatalf("summary = %q, want job result", res.Summary)
	}
}

func TestAgentAwaitTimeoutReturnsNormalResult(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	jobs := &fakeJobSource{outstanding: []AwaitJobInfo{{ID: "job-1", Command: "hang", Status: "running"}}}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		<-ctx.Done()
		return AwaitJobInfo{}, ctx.Err()
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true, "timeout_seconds": 1}`)})
	if err != nil {
		t.Fatalf("await all with timeout: %v", err)
	}
	if !strings.Contains(res.Summary, "timed out after 1s") || !strings.Contains(res.Summary, "still running") {
		t.Fatalf("summary = %q, want timeout notice", res.Summary)
	}
}

func TestAgentAwaitJobResultFormat(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	exit := 3
	jobs := &fakeJobSource{tail: "line1\nline2"}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		return AwaitJobInfo{ID: "job-7", Command: "go test ./...", Status: "failed", ExitCode: &exit}, nil
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"job_id": "job-7"}`)})
	if err != nil {
		t.Fatalf("await job: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-7 failed (exit 3): go test ./...") {
		t.Fatalf("summary = %q", res.Summary)
	}
	if !strings.Contains(res.Content, "line2") {
		t.Fatalf("content missing output tail: %q", res.Content)
	}
}

// TestAgentAwaitJobResultCarriesStatusExitAndStopCause pins the evidence a job
// result must carry: the status says what the execution is doing, the exit code
// is the one that was observed (or absent), and a stop cause explains a stop the
// exit code cannot.
func TestAgentAwaitJobResultCarriesStatusExitAndStopCause(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})

	// A stopped job: no observed exit, but a recorded cause.
	stopped := &fakeJobSource{}
	stopped.wait = func(context.Context, string) (AwaitJobInfo, error) {
		return AwaitJobInfo{ID: "job-2", Command: "sleep 30", Status: "killed", StopCause: "user stopped the turn"}, nil
	}
	await := newAwaitTool(state, WithAwaitJobs(stopped))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"job_id": "job-2"}`)})
	if err != nil {
		t.Fatalf("await job: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-2 killed (exit n/a): sleep 30") {
		t.Fatalf("summary = %q, want the status with no observed exit", res.Summary)
	}
	if !strings.Contains(res.Content, "stopped: user stopped the turn") {
		t.Fatalf("content = %q, want the stop cause", res.Content)
	}

	// A still-running job: the status is the continuation, and no exit is
	// invented for it.
	running := &fakeJobSource{}
	running.wait = func(context.Context, string) (AwaitJobInfo, error) {
		return AwaitJobInfo{ID: "job-3", Command: "download", Status: "running"}, nil
	}
	await = newAwaitTool(state, WithAwaitJobs(running))
	res, err = await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"job_id": "job-3"}`)})
	if err != nil {
		t.Fatalf("await running job: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-3 running (exit n/a): download") {
		t.Fatalf("summary = %q, want the running status", res.Summary)
	}
}

// TestAgentAwaitPeekReportsStopCause pins that peek explains a stopped job the
// same way a wait does.
func TestAgentAwaitPeekReportsStopCause(t *testing.T) {
	jobs := &fakeJobSource{peek: func(id string, _ int) (AwaitJobInfo, string, error) {
		return AwaitJobInfo{ID: id, Command: "sleep 30", Status: "killed", StopCause: "stopped by the user"}, "tail line", nil
	}}
	tool := newAwaitTool(newTestState(t), WithAwaitJobs(jobs))
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-4","peek":true}`)})
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	for _, want := range []string{"status: killed", "stopped: stopped by the user", "tail line"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("peek content missing %q: %q", want, res.Content)
		}
	}
}

// TestAgentAwaitPeekValidatesNegativeTimeout pins that the wait-budget
// validation applies to peek too: a negative timeout is a caller mistake
// whichever mode reads it.
func TestAgentAwaitPeekValidatesNegativeTimeout(t *testing.T) {
	jobs := &fakeJobSource{peek: func(id string, _ int) (AwaitJobInfo, string, error) {
		t.Fatal("peek must not reach the job source with an invalid timeout")
		return AwaitJobInfo{}, "", nil
	}}
	tool := newAwaitTool(newTestState(t), WithAwaitJobs(jobs))
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-4","peek":true,"timeout_seconds":-1}`)})
	if err == nil || !strings.Contains(err.Error(), ">= 0") {
		t.Fatalf("err = %v, want a negative-timeout rejection on peek", err)
	}
}

func TestAgentAwaitAllNoOutstanding(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	await := newAwaitTool(state)
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
	if err != nil {
		t.Fatalf("await all: %v", err)
	}
	if !strings.Contains(res.Summary, "no outstanding background work") {
		t.Fatalf("summary = %q", res.Summary)
	}
}

// TestAgentAwaitAnyLeavesUnfinishedLosersRunning pins the cleanup rule an any
// race must obey: the losers' waiters are cancelled, and nothing about the
// losers themselves changes. A loser that stopped or was killed by the race
// would be exactly the bug this feature exists to remove.
func TestAgentAwaitAnyLeavesUnfinishedLosersRunning(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	exit := 0
	jobs := &fakeJobSource{
		outstanding: []AwaitJobInfo{
			{ID: "job-1", Command: "winner", Status: "running"},
			{ID: "job-2", Command: "loser", Status: "running"},
		},
	}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		if id == "job-1" {
			return AwaitJobInfo{ID: id, Command: "winner", Status: "completed", ExitCode: &exit}, nil
		}
		// The loser blocks until its waiter is cancelled by the any cleanup.
		<-ctx.Done()
		return AwaitJobInfo{}, ctx.Err()
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"any": true}`)})
	if err != nil {
		t.Fatalf("await any: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-1 completed") {
		t.Fatalf("summary = %q, want the winner", res.Summary)
	}
	// Nothing was killed: the loser's lifetime was never the await's to end.
	if jobs.killed != "" {
		t.Fatalf("killed %q, want nothing: an any cleanup must cancel waiters only", jobs.killed)
	}
}

// TestAgentAwaitAllKeepsUnfinishedTargetsAfterTimeout pins that an expired all
// wait reports the remaining work as still running and stops nothing: the
// timeout bounds the wait, never the targets.
func TestAgentAwaitAllKeepsUnfinishedTargetsAfterTimeout(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	jobs := &fakeJobSource{
		outstanding: []AwaitJobInfo{{ID: "job-1", Command: "slow", Status: "running"}},
	}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		<-ctx.Done()
		return AwaitJobInfo{}, ctx.Err()
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true, "timeout_seconds": 1}`)})
	if err != nil {
		t.Fatalf("await all: %v", err)
	}
	if !strings.Contains(res.Summary, "still running") {
		t.Fatalf("summary = %q, want the remaining work named", res.Summary)
	}
	if jobs.killed != "" {
		t.Fatalf("killed %q, want nothing: a timeout must not stop a target", jobs.killed)
	}
}

// TestAgentAwaitStaysReadOnly pins that await keeps its read-only risk: a
// blocking handler that held the write gate would deadlock the very target it
// waits for.
func TestAgentAwaitStaysReadOnly(t *testing.T) {
	tool := newAwaitTool(newTestState(t))
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("agent.await risk = %v, want RiskReadOnly", tool.Risk)
	}
}

// steeringAwaitTargets builds a job source with two running jobs that block
// until their waiter context ends, and records every kill.
func steeringAwaitTargets() *fakeJobSource {
	jobs := &fakeJobSource{
		outstanding: []AwaitJobInfo{
			{ID: "job-1", Command: "slow one", Status: "running"},
			{ID: "job-2", Command: "slow two", Status: "running"},
		},
	}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		<-ctx.Done()
		return AwaitJobInfo{}, ctx.Err()
	}
	return jobs
}

// TestSteeringAwaitModesPreserveTargetsAndPartialResults pins the await
// contract under interruption: every mode's waiter ends with the distinct
// steering cause, surviving targets are reported by identity with a continues
// state, completed partial results are preserved, and NOTHING stops a target —
// no KillJob, no CancelSubagent, no turn stop.
func TestSteeringAwaitModesPreserveTargetsAndPartialResults(t *testing.T) {
	state := newTestState(t)
	jobs := steeringAwaitTargets()
	// A child that never finishes on its own: the interruption must end the
	// wait without ending the child.
	child := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	view := state.RegisterSubagent("background child", child)
	await := newAwaitTool(state, WithAwaitJobs(jobs))

	run := func(t *testing.T, args string) (registry.ToolResult, error) {
		t.Helper()
		state.SetOperationInterrupt(func(context.Context, session.OperationRef) (session.OperationStop, error) {
			return session.OperationStop{Wait: func(context.Context) ([]session.SteeringOutcome, error) { return nil, nil }}, nil
		})
		turnID, err := state.BeginSteeringTurn(context.Background())
		if err != nil {
			t.Fatalf("BeginSteeringTurn: %v", err)
		}
		ctx, ref, err := state.BeginSteeringOperation(context.Background(), turnID, session.OperationTools)
		if err != nil {
			t.Fatalf("BeginSteeringOperation: %v", err)
		}
		_ = ref

		type outcome struct {
			res registry.ToolResult
			err error
		}
		done := make(chan outcome, 1)
		go func() {
			res, err := await.Handler(ctx, registry.ToolCall{ID: "await-1", Args: json.RawMessage(args)})
			done <- outcome{res, err}
		}()
		time.Sleep(50 * time.Millisecond) // let the waiter park
		if _, err := state.SubmitSendNow(session.SendNowRequest{TurnID: turnID, RequestID: "r-" + args, Text: "interrupt the wait"}); err != nil {
			t.Fatalf("SubmitSendNow: %v", err)
		}
		select {
		case got := <-done:
			state.FinishSteeringOperation(ref, nil)
			// Close the eligible turn so the next subtest can open its own:
			// a session admits exactly one active steering turn.
			state.EndSteeringTurn(turnID, "test finished")
			return got.res, got.err
		case <-time.After(5 * time.Second):
			t.Fatal("the await handler never returned after the interruption")
			return registry.ToolResult{}, nil
		}
	}

	for _, tc := range []struct {
		name string
		args string
	}{
		{"single-child", `{"id":0}`},
		{"single-job", `{"job_id":"job-1"}`},
		{"any", `{"any":true}`},
		{"all", `{"all":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if tc.name == "single-child" {
				args = fmt.Sprintf(`{"id":%d}`, view.ID)
			}
			res, err := run(t, args)
			if !errors.Is(err, session.ErrSendNow) {
				t.Fatalf("await err = %v, want ErrSendNow", err)
			}
			notice := res.Notice
			if notice == nil || notice.Kind != steeringNoticeKind {
				t.Fatalf("notice = %+v, want a steering_interruption notice", notice)
			}
			outcomes, _ := notice.Data["outcomes"].([]session.SteeringOutcome)
			if len(outcomes) == 0 {
				t.Fatal("no surviving targets reported")
			}
			for _, o := range outcomes {
				if o.State != "continues" {
					t.Fatalf("outcome %+v: state = %q, want continues (the waiter was interrupted, not the target)", o, o.State)
				}
				if o.AgentID == "" && o.ExecutionID == "" {
					t.Fatalf("outcome %+v names no target identity", o)
				}
			}
			// Nothing stopped a target.
			jobs.mu.Lock()
			killed := jobs.killed
			jobs.mu.Unlock()
			if killed != "" {
				t.Fatalf("killed %q, want nothing: steering cancels the waiter, never the target", killed)
			}
			if v, ok := state.Subagent(view.ID); !ok || v.Status != session.SubagentRunning {
				t.Fatalf("child status = %+v, want it still running", v)
			}
		})
	}
}
