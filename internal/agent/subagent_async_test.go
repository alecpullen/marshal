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
// the await tool, all sharing one parent session. The third return value is
// the same await tool: agent.output was folded into agent.await's
// "peek": true in the context-diet Tier 2a cut, so peeking is expressed
// through agent.await now.
func newAsyncRunToolPair(state *session.State, exec func(ctx context.Context, child *Runner, prompt string) (string, string, error)) (run, await, peek registry.Tool) {
	run = NewSubagentTool(
		func(req SubagentRequest) (*Runner, *session.State, error) { return &Runner{}, state, nil },
		nil,
		registry.New(),
		state,
		WithSubagentExec(exec),
	)
	await = NewSubagentAwaitTool(state)
	peek = await
	return run, await, peek
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
	if !strings.Contains(res.Summary, "no outstanding background work") {
		t.Fatalf("await all summary = %q, want no-outstanding (pipeline card skipped)", res.Summary)
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
	if !strings.Contains(res.Summary, "no outstanding background work") {
		t.Fatalf("summary = %q, want no-outstanding notice", res.Summary)
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

func TestAgentAwaitPeekRunningAndFinished(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	release := make(chan struct{})
	run, _, peek := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-release
		return "the report", "", nil
	})
	run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"x","description":"y"}`)})
	viewID := state.Subagents()[0].ID

	res, err := peek.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d, "peek": true}`, viewID))})
	if err != nil {
		t.Fatalf("peek: %v", err)
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
	res, err = peek.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(fmt.Sprintf(`{"id": %d, "peek": true}`, viewID))})
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if !strings.Contains(res.Content, "status: finished") || !strings.Contains(res.Content, "the report") {
		t.Fatalf("finished peek = %q, want status and report", res.Content)
	}
}

func TestAgentAwaitPeekUnknownID(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	_, _, peek := newAsyncRunToolPair(state, nil)
	_, err := peek.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"id": 999, "peek": true}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent id") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

// The agent.output transcript tests that lived here were removed with the
// tool in the context-diet Tier 2a cut: agent.await's "peek": true returns
// status, report and activity tail, but deliberately not a message
// transcript (it duplicated the tail at real token cost).

// TestAgentSteerRoutesToChildSteering: the instruction lands on the LIVE
// child's own steering queue, enveloped so the child can tell it apart from
// ordinary user steering, and never on the parent's queue.
func TestAgentSteerRoutesToChildSteering(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	childState := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(1))
	view := state.RegisterSubagentWithMeta("migrate the schema", childState, session.SubagentMeta{Role: RoleSubtask})

	tool := NewSubagentSteerTool(state)
	args := fmt.Sprintf(`{"id":%d,"message":"use Postgres, not SQLite"}`, view.ID)
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "call_steer", Name: "agent.steer", Args: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, fmt.Sprintf("%d", view.ID)) {
		t.Errorf("summary should name the steered child; got: %s", res.Summary)
	}

	queued := childState.SteeringQueue()
	if len(queued) != 1 {
		t.Fatalf("child steering queue = %v, want exactly one message", queued)
	}
	if !strings.Contains(queued[0], "[steering from parent]") || !strings.Contains(queued[0], "use Postgres, not SQLite") {
		t.Errorf("queued steering should be the enveloped message; got: %s", queued[0])
	}
	if len(state.SteeringQueue()) != 0 {
		t.Error("the message must go to the child, not the parent")
	}
}

// TestAgentSteerRejectsUnknownFinishedAndEmpty keeps every failure mode
// actionable rather than silently dropping the instruction.
func TestAgentSteerRejectsUnknownFinishedAndEmpty(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	tool := NewSubagentSteerTool(state)

	_, err := tool.Handler(context.Background(), registry.ToolCall{ID: "c", Name: "agent.steer", Args: json.RawMessage(`{"id":99,"message":"x"}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent") {
		t.Fatalf("unknown child error = %v, want a named 'unknown subagent'", err)
	}

	childState := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}, session.WithDepth(1))
	view := state.RegisterSubagentWithMeta("finished child", childState, session.SubagentMeta{Role: RoleSubtask})

	_, err = tool.Handler(context.Background(), registry.ToolCall{ID: "c", Name: "agent.steer", Args: json.RawMessage(fmt.Sprintf(`{"id":%d,"message":"  "}`, view.ID))})
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty message error = %v, want 'must not be empty'", err)
	}

	state.FinishSubagent(view.ID, "done", nil)
	_, err = tool.Handler(context.Background(), registry.ToolCall{ID: "c", Name: "agent.steer", Args: json.RawMessage(fmt.Sprintf(`{"id":%d,"message":"x"}`, view.ID))})
	if err == nil || !strings.Contains(err.Error(), "no longer running") {
		t.Fatalf("finished child error = %v, want 'no longer running'", err)
	}
	if len(childState.SteeringQueue()) != 0 {
		t.Error("a finished child must not receive steering")
	}
}

func TestAgentSteerIsReadOnly(t *testing.T) {
	tool := NewSubagentSteerTool(session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{}))
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("agent.steer risk = %v, want RiskReadOnly", tool.Risk)
	}
}

func TestAgentKillRunningSubagent(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	run, _, _ := newAsyncRunToolPair(state, func(ctx context.Context, child *Runner, prompt string) (string, string, error) {
		<-ctx.Done()
		return "", "", ctx.Err()
	})
	kill := NewSubagentKillTool(state)
	// Start the child via run.Handler directly (returns immediately while
	// the child blocks on <-ctx.Done()); do not use runAsyncSubagent, which
	// waits for the child to finish on its own.
	if _, err := run.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"prompt":"x","description":"y"}`)}); err != nil {
		t.Fatalf("run: %v", err)
	}
	viewID := state.Subagents()[0].ID

	res, err := kill.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, viewID)),
	})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !strings.Contains(res.Summary, "killed subagent") {
		t.Fatalf("kill summary = %q, want killed subagent", res.Summary)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	view, err := state.WaitSubagent(ctx, viewID)
	if err != nil {
		t.Fatalf("WaitSubagent: %v", err)
	}
	if view.Status != session.SubagentFailed {
		t.Fatalf("status = %v, want SubagentFailed after kill", view.Status)
	}
	if !strings.Contains(view.Error, "context canceled") {
		t.Fatalf("view.Error = %q, want context canceled", view.Error)
	}
	reports := state.SubagentReports()
	if len(reports) != 1 || !strings.Contains(reports[0], "failed") {
		t.Fatalf("report queue = %v, want one failure report", reports)
	}
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency after kill = %d, want 0", got)
	}
}

func TestAgentKillAlreadyFinished(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	childState := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	view := state.RegisterSubagentWithMeta("done child", childState, session.SubagentMeta{})
	state.FinishSubagent(view.ID, "already done", nil)

	kill := NewSubagentKillTool(state)
	res, err := kill.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(fmt.Sprintf(`{"id": %d}`, view.ID)),
	})
	if err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !strings.Contains(res.Summary, "already finished") {
		t.Fatalf("kill summary = %q, want already finished", res.Summary)
	}
}

func TestAgentKillUnknownID(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	kill := NewSubagentKillTool(state)
	_, err := kill.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"id": 999}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown subagent id") {
		t.Fatalf("err = %v, want unknown-id error", err)
	}
}

func TestAgentKillRequiresID(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	kill := NewSubagentKillTool(state)
	_, err := kill.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), `"id"`) {
		t.Fatalf("err = %v, want an id-required error", err)
	}
}

// agent.kill covers two target classes, so the schema advertises both and the
// handler enforces exactly-one (pinned by TestAgentKillRequiresID below).
func TestAgentKillSchemaAdvertisesBothTargets(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	kill := NewSubagentKillTool(state)
	for _, want := range []string{`"id"`, `"job_id"`} {
		if !strings.Contains(string(kill.Schema), want) {
			t.Fatalf("schema must advertise %s: %s", want, kill.Schema)
		}
	}
	if !strings.Contains(string(kill.Schema), `"additionalProperties":false`) {
		t.Fatal("schema must reject additional properties")
	}
	if strings.Contains(string(kill.Schema), `"required"`) {
		t.Fatal("neither target is individually required; the handler enforces exactly one")
	}
	if !strings.Contains(kill.Description, "agent.await") {
		t.Fatal("description must document the asynchronous terminal-state follow-up")
	}
}

func TestAgentKillIsReadOnlyRisk(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	kill := NewSubagentKillTool(state)
	if kill.Risk != registry.RiskReadOnly {
		t.Fatalf("agent.kill risk = %v, want RiskReadOnly", kill.Risk)
	}
}

func TestSubtaskScopeViewExcludesAsyncTools(t *testing.T) {
	src := registry.New()
	for _, name := range []string{"agent.run", "agent.await", "agent.kill", "agent.steer", "file.read"} {
		tool := registry.Tool{Name: name, Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskReadOnly}
		tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		}
		if err := src.Register(tool); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	view := SubtaskScopeView(src)
	for _, name := range []string{"agent.run", "agent.await", "agent.kill", "agent.steer"} {
		if _, ok := view.Lookup(name); ok {
			t.Fatalf("%s must be excluded from the subtask scope view", name)
		}
	}
	if _, ok := view.Lookup("file.read"); !ok {
		t.Fatal("file.read must remain visible to subtasks")
	}
}

// TestAwaitMixedSubagentAndJobTargets is the mixed-class case a managed shell
// command now reaches: a background job and a background child can be
// outstanding at the same time, and "any"/"all" must cover both classes without
// the loser of a race being cancelled.
func TestAwaitMixedSubagentAndJobTargets(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	slowChild := st.RegisterSubagentWithMeta("slow child", newChildState(t), session.SubagentMeta{})
	exit := 0
	jobs := &fakeJobSource{
		outstanding: []AwaitJobInfo{
			{ID: "job-1", Command: "download", Status: "running"},
			{ID: "job-2", Command: "suite", Status: "running"},
		},
	}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		if id == "job-2" {
			return AwaitJobInfo{ID: id, Command: "suite", Status: "completed", ExitCode: &exit}, nil
		}
		<-ctx.Done()
		return AwaitJobInfo{}, ctx.Err()
	}

	_, await, _ := newAsyncRunToolPair(st, nil)
	await = NewSubagentAwaitTool(st, WithAwaitJobs(jobs))

	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"any":true}`)})
	if err != nil {
		t.Fatalf("agent.await any: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-2 completed") {
		t.Fatalf("summary = %q, want the finishing job", res.Summary)
	}
	// The unfinished job and the unfinished child are both untouched.
	if jobs.killed != "" {
		t.Fatalf("killed %q, want nothing: the any cleanup cancels waiters only", jobs.killed)
	}
	if got := st.Subagents(); len(got) != 1 || got[0].ID != slowChild.ID || got[0].Status != session.SubagentRunning {
		t.Fatalf("subagents = %+v, want the slow child still running", got)
	}
}

// TestAwaitAllCoversJobsAndSubagentsAfterTimeout pins that an expired "all"
// reports every remaining class as still running: a job and a child outstanding
// at once are both named, and neither is stopped by the timeout.
func TestAwaitAllCoversJobsAndSubagentsAfterTimeout(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	st.RegisterSubagentWithMeta("slow child", newChildState(t), session.SubagentMeta{})
	jobs := &fakeJobSource{outstanding: []AwaitJobInfo{{ID: "job-1", Command: "download", Status: "running"}}}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		<-ctx.Done()
		return AwaitJobInfo{}, ctx.Err()
	}

	await := NewSubagentAwaitTool(st, WithAwaitJobs(jobs))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all":true,"timeout_seconds":1}`)})
	if err != nil {
		t.Fatalf("agent.await all: %v", err)
	}
	if !strings.Contains(res.Summary, "still running") {
		t.Fatalf("summary = %q, want the remaining work named", res.Summary)
	}
	if jobs.killed != "" {
		t.Fatalf("killed %q, want nothing", jobs.killed)
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
	if !strings.Contains(res.Summary, "no outstanding background work") {
		t.Fatalf("await all summary = %q, want no-outstanding (finished child skipped)", res.Summary)
	}
}

// An "all" wait must count down as children finish, so a user watching a
// long batch can see progress rather than a static line.
func TestAwaitAllUpdatesActiveToolCallArgs(t *testing.T) {
	st := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	a := st.RegisterSubagentWithMeta("a", newChildState(t), session.SubagentMeta{})
	b := st.RegisterSubagentWithMeta("b", newChildState(t), session.SubagentMeta{})
	st.SetActiveToolCall(session.ActiveToolCall{Name: "agent.await", Args: "all", StartedAt: time.Now()})

	var seen []string
	var mu sync.Mutex
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if atc, ok := st.ActiveToolCall(); ok {
				mu.Lock()
				if len(seen) == 0 || seen[len(seen)-1] != atc.Args {
					seen = append(seen, atc.Args)
				}
				mu.Unlock()
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	go func() {
		time.Sleep(30 * time.Millisecond)
		st.FinishSubagent(a.ID, "a done", nil)
		time.Sleep(30 * time.Millisecond)
		st.FinishSubagent(b.ID, "b done", nil)
	}()

	_, tool, _ := newAsyncRunToolPair(st, nil)
	if _, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: json.RawMessage(`{"all":true}`),
	}); err != nil {
		t.Fatalf("agent.await all: %v", err)
	}
	close(stop)

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(seen, " | ")
	if !strings.Contains(joined, "2 running") {
		t.Errorf("never showed 2 running; saw: %s", joined)
	}
	if !strings.Contains(joined, "1 running") {
		t.Errorf("never counted down to 1 running; saw: %s", joined)
	}
}
