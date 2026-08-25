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

// newAsyncRunToolPair builds an agent.run tool with the given exec stub plus
// the await and output tools, all sharing one parent session.
func newAsyncRunToolPair(state *session.State, exec func(ctx context.Context, child *Runner, prompt string) (string, string, error)) (run, await, output registry.Tool) {
	run = NewSubagentTool(
		func(req SubagentRequest) (*Runner, *session.State, error) { return &Runner{}, state, nil },
		nil,
		registry.New(),
		state,
		WithSubagentExec(exec),
	)
	await = NewSubagentAwaitTool(state)
	output = NewSubagentOutputTool(state)
	return run, await, output
}

// newChildState builds a real child session state so a subagent registered
// with it has Child != nil and is treated as a genuine background child.
func newChildState(t *testing.T) *session.State {
	t.Helper()
	return session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
}

func TestAgentAwaitReturnsFinishedResult(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		return "the report", "", nil
	})
	_, view := runAsyncSubagent(t, run, state, `{"prompt":"x","description":"y"}`)

	res, err := await.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, view.ID)),
	})
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !strings.Contains(res.Summary, "completed") {
		t.Fatalf("await summary = %q, want completed", res.Summary)
	}
	if !strings.Contains(res.Content, "the report") {
		t.Fatalf("await content = %q, want the report", res.Content)
	}
}

func TestAgentAwaitFailedChildSurfacesError(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		return "", "", errors.New("child exploded")
	})
	_, view := runAsyncSubagent(t, run, state, `{"prompt":"x","description":"y"}`)

	res, err := await.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, view.ID)),
	})
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !strings.Contains(res.Summary, "failed") {
		t.Fatalf("await summary = %q, want failed", res.Summary)
	}
	if !strings.Contains(res.Content, "child exploded") {
		t.Fatalf("await content = %q, want the failure text", res.Content)
	}
}

func TestAgentAwaitAll(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	release := make(chan struct{})
	var mu sync.Mutex
	var finished []string
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-release
		mu.Lock()
		finished = append(finished, prompt)
		mu.Unlock()
		return "report: " + prompt, "", nil
	})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"a","description":"first"}`)})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"b","description":"second"}`)})

	awaitDone := make(chan registry.ToolResult, 1)
	go func() {
		res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
		if err != nil {
			t.Errorf("await all: %v", err)
		}
		awaitDone <- res
	}()
	select {
	case <-awaitDone:
		t.Fatal("await all returned while children were still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case res := <-awaitDone:
		if !strings.Contains(res.Content, "report: a") || !strings.Contains(res.Content, "report: b") {
			t.Fatalf("await all content = %q, want both reports", res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await all did not return after both children finished")
	}
}

// TestSubagentPanicRecovered guards I1: a panic in the child goroutine
// (cfg.exec, a tool handler, the SQLite write path) must not crash the
// process. It is recovered, surfaced as a failed subagent, and reported.
func TestSubagentPanicRecovered(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		panic("boom in child")
	})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"x","description":"y"}`)})
	viewID := state.Subagents()[0].ID

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	view, err := state.WaitSubagent(ctx, viewID)
	if err != nil {
		t.Fatalf("WaitSubagent: %v", err)
	}
	if view.Status != session.SubagentFailed {
		t.Fatalf("status = %v, want SubagentFailed after panic", view.Status)
	}
	if !strings.Contains(view.Error, "panicked") {
		t.Fatalf("view.Error = %q, want a panicked marker", view.Error)
	}
	// The failure report must reach the model via the report queue.
	reports := state.SubagentReports()
	if len(reports) != 1 || !strings.Contains(reports[0], "panicked") {
		t.Fatalf("report queue = %v, want one panicked report", reports)
	}
	// The panic report must also be persisted as a RoleUser message so it
	// survives restart (buildHistoryMessages replays RoleUser). A panic that
	// only reaches the in-memory queue is lost across rollover/restart.
	var foundDurable bool
	for _, msg := range state.Messages() {
		if msg.Role == session.RoleUser && strings.Contains(msg.Content, "panicked") {
			foundDurable = true
			break
		}
	}
	if !foundDurable {
		t.Fatalf("no RoleUser message containing panic report found in transcript")
	}
	// The concurrency slot must be released even after a panic.
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency after panic = %d, want 0", got)
	}
	// await on the failed child surfaces the panic text.
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, viewID))})
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !strings.Contains(res.Content, "panicked") {
		t.Fatalf("await content = %q, want panic text", res.Content)
	}
}

// TestAgentAwaitAllSkipsPipelineCards guards I3: agent.await all must only
// wait on real background children (Child != nil), not pipeline/SDD cards
// that share the parent's state.
func TestAgentAwaitAllSkipsPipelineCards(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	// A pipeline/SDD card: Child == nil, Status running.
	state.RegisterSubagentWithMeta("review · main.go", nil, session.SubagentMeta{})
	_, await, _ := newAsyncRunToolPair(state, nil)
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
	if err != nil {
		t.Fatalf("await all: %v", err)
	}
	if !strings.Contains(res.Summary, "no running subagents") {
		t.Fatalf("await all summary = %q, want no-running (pipeline card skipped)", res.Summary)
	}
}

// TestAgentAwaitAllIncludesLateChildren guards I3: agent.await all must
// include children registered after the initial snapshot, not just the ones
// running when the call started.
func TestAgentAwaitAllIncludesLateChildren(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	release := make(chan struct{})
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-release
		return "report: " + prompt, "", nil
	})
	// First child starts and blocks.
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"a","description":"first"}`)})

	awaitDone := make(chan registry.ToolResult, 1)
	go func() {
		res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
		if err != nil {
			t.Errorf("await all: %v", err)
		}
		awaitDone <- res
	}()
	// Give await all a moment to snapshot, then register a second child.
	time.Sleep(20 * time.Millisecond)
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"b","description":"second"}`)})

	close(release)
	select {
	case res := <-awaitDone:
		if !strings.Contains(res.Content, "report: a") || !strings.Contains(res.Content, "report: b") {
			t.Fatalf("await all content = %q, want both late and early reports", res.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await all did not include the late child")
	}
}

func TestAgentAwaitNoRunning(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	_, await, _ := newAsyncRunToolPair(state, nil)
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !strings.Contains(res.Summary, "no running subagents") {
		t.Fatalf("summary = %q, want no-running notice", res.Summary)
	}
}

func TestAgentAwaitUnknownID(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	_, await, _ := newAsyncRunToolPair(state, nil)
	_, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"id": 999}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent id") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

// TestAgentAwaitCancelledByTurnContext is the Esc-during-await escape
// hatch: the blocking handler unblocks when the turn's context ends.
func TestAgentAwaitCancelledByTurnContext(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"x","description":"y"}`)})
	viewID := state.Subagents()[0].ID

	ctx, cancel := context.WithCancel(context.Background())
	awaitErr := make(chan error, 1)
	go func() {
		_, err := await.Handler(ctx, registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, viewID))})
		awaitErr <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-awaitErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("await err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("await did not unblock on turn cancel")
	}
	// Clean up the blocked child so the test does not leak a goroutine.
	state.Shutdown()
}

func TestAgentOutputRunningAndFinished(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	release := make(chan struct{})
	run, _, output := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-release
		return "the report", "", nil
	})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"x","description":"y"}`)})
	viewID := state.Subagents()[0].ID

	res, err := output.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, viewID))})
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if !strings.Contains(res.Content, "status: running") {
		t.Fatalf("running peek = %q, want status: running", res.Content)
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := state.WaitSubagent(ctx, viewID); err != nil {
		t.Fatalf("WaitSubagent: %v", err)
	}
	res, err = output.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, viewID))})
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if !strings.Contains(res.Content, "status: finished") || !strings.Contains(res.Content, "the report") {
		t.Fatalf("finished peek = %q, want status and report", res.Content)
	}
}

func TestAgentOutputUnknownID(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	_, _, output := newAsyncRunToolPair(state, nil)
	_, err := output.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"id": 999}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent id") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

func TestSubtaskScopeViewExcludesAsyncTools(t *testing.T) {
	src := registry.New()
	for _, name := range []string{"agent.run", "agent.await", "agent.output", "file.read"} {
		tool := registry.Tool{Name: name, Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskReadOnly}
		tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		}
		if err := src.Register(tool); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	view := SubtaskScopeView(src)
	for _, name := range []string{"agent.run", "agent.await", "agent.output"} {
		if _, ok := view.Lookup(name); ok {
			t.Fatalf("%s must be excluded from the subtask scope view", name)
		}
	}
	if _, ok := view.Lookup("file.read"); !ok {
		t.Fatal("file.read must remain visible to subtasks")
	}
}

// TestAgentAwaitReturnsImmediatelyForApprovalPendingChild guards I-3:
// agent.await on a child pending user approval returns immediately with a
// liveness notice instead of blocking the parent turn indefinitely.
func TestAgentAwaitReturnsImmediatelyForApprovalPendingChild(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	release := make(chan struct{})
	approvalSet := make(chan struct{})
	run, await, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		// Simulate the child waiting for user approval.
		childState := state.Subagents()[0].Child
		childState.SetPendingApproval(&session.PendingToolCall{
			ID:   "test-approval",
			Name: "shell.run",
		})
		close(approvalSet)
		<-release
		return "done after approval", "", nil
	})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"x","description":"y"}`)})
	viewID := state.Subagents()[0].ID

	// Wait until the child has set its pending approval before calling
	// await, so the await handler sees the approval.
	<-approvalSet

	// await should return immediately (not block) with the approval notice.
	done := make(chan registry.ToolResult, 1)
	go func() {
		res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, viewID))})
		if err != nil {
			t.Errorf("await: %v", err)
		}
		done <- res
	}()
	select {
	case res := <-done:
		if !strings.Contains(res.Summary, "waiting for user approval") {
			t.Fatalf("await summary = %q, want approval-pending notice", res.Summary)
		}
		if !strings.Contains(res.Content, "shell.run") {
			t.Fatalf("await content = %q, want tool name", res.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("await blocked instead of returning immediately for approval-pending child")
	}
	close(release)
	// Let the child finish to avoid a goroutine leak.
	state.WaitSubagent(context.Background(), viewID)
}

// TestAgentAwaitAllSkipsAlreadyFinished guards M-1: await all must not
// re-collect children that already finished in a prior turn. Their reports
// were already delivered via the queue drain / persisted message, so
// collecting them again would double-deliver.
func TestAwaitAnyReturnsFirstFinisher(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	a := st.RegisterSubagentWithMeta("a", newChildState(t), session.SubagentMeta{})
	b := st.RegisterSubagentWithMeta("b", newChildState(t), session.SubagentMeta{})

	_, tool, _ := newAsyncRunToolPair(st, nil)
	go func() {
		time.Sleep(20 * time.Millisecond)
		st.FinishSubagent(b.ID, "b done", nil)
	}()

	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"any":true}`),
	})
	if err != nil {
		t.Fatalf("agent.await any: %v", err)
	}
	if !strings.Contains(res.Summary, "b done") && !strings.Contains(res.Content, "b done") {
		t.Errorf("result did not report the finisher: %+v", res)
	}
	if st.Subagents()[0].ID != a.ID {
		t.Fatalf("precondition: expected a to remain registered")
	}
}

func TestAwaitRejectsMultipleModes(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	_, tool, _ := newAsyncRunToolPair(st, nil)
	for _, args := range []string{
		`{"all":true,"any":true}`,
		`{"id":1,"any":true}`,
		`{"id":1,"all":true}`,
	} {
		if _, err := tool.Handler(context.Background(), registry.ToolCall{
			Args: json.RawMessage(args),
		}); err == nil {
			t.Errorf("args %s: want an error for multiple modes, got nil", args)
		}
	}
}

func TestAwaitSchemaAdvertisesAny(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	_, tool, _ := newAsyncRunToolPair(st, nil)
	if !strings.Contains(string(tool.Schema), `"any"`) {
		t.Error("schema does not advertise the any property; the model cannot use it")
	}
	if !strings.Contains(tool.Description, "any") {
		t.Error("description does not mention any; it is the model's only documentation")
	}
}

func TestAgentAwaitAllSkipsAlreadyFinished(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	// Register a child that is already finished.
	childState := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	view := state.RegisterSubagentWithMeta("done child", childState, session.SubagentMeta{})
	state.FinishSubagent(view.ID, "already done", nil)

	_, await, _ := newAsyncRunToolPair(state, nil)
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
	if err != nil {
		t.Fatalf("await all: %v", err)
	}
	if !strings.Contains(res.Summary, "no running subagents") {
		t.Fatalf("await all summary = %q, want no-running (finished child skipped)", res.Summary)
	}
}
