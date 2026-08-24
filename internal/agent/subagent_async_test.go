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
