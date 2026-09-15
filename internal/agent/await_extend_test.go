package agent

import (
	"context"
	"encoding/json"
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

type fakeWatchSource struct {
	outstanding []AwaitWatchInfo
	wait        func(ctx context.Context, id string) (AwaitWatchInfo, error)
}

func (f *fakeWatchSource) OutstandingWatches() []AwaitWatchInfo { return f.outstanding }

func (f *fakeWatchSource) AwaitWatch(ctx context.Context, id string) (AwaitWatchInfo, error) {
	return f.wait(ctx, id)
}

func newAwaitTool(state *session.State, opts ...AwaitOption) registry.Tool {
	return NewSubagentAwaitTool(state, opts...)
}

func TestAgentAwaitValidatesFiveModes(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	await := newAwaitTool(state)
	for _, args := range []string{
		`{"id": 1, "job_id": "job-1"}`,
		`{"any": true, "all": true}`,
		`{"job_id": "job-1", "watch_id": "w1"}`,
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

func TestAgentAwaitWatchIDWithoutSource(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	await := newAwaitTool(state)
	_, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"watch_id": "w1"}`)})
	if err == nil || !strings.Contains(err.Error(), "no watch source") {
		t.Fatalf("err = %v, want no-source", err)
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

func TestAgentAwaitWatchResultFormat(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	watches := &fakeWatchSource{}
	watches.wait = func(ctx context.Context, id string) (AwaitWatchInfo, error) {
		return AwaitWatchInfo{ID: id, Name: "tests", Kind: "command", State: "fired", Condition: "exit_code 0", Mode: "once", FireCount: 1, LastSample: "ok"}, nil
	}
	await := newAwaitTool(state, WithAwaitWatches(watches))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"watch_id": "w2"}`)})
	if err != nil {
		t.Fatalf("await watch: %v", err)
	}
	if !strings.Contains(res.Summary, "watch tests fired") || !strings.Contains(res.Content, "last_sample: ok") {
		t.Fatalf("result = %q / %q", res.Summary, res.Content)
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
