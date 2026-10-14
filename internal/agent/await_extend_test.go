package agent

import (
	"context"
	"encoding/json"
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
