package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// racedWatchFake is a fakeWatchSource whose List reports a pending once-watch
// but whose AwaitWatch always returns watch.Manager.WaitFire's not-found
// error — the shape produced when the watch fires or stops between the
// outstanding scan and the WaitFire registration.
type racedWatchFake struct {
	watchID string
}

func (f racedWatchFake) OutstandingWatches() []AwaitWatchInfo {
	return []AwaitWatchInfo{{
		ID:        f.watchID,
		Name:      "tests",
		Kind:      "command",
		State:     "watching",
		Condition: "exit_code 0",
		Mode:      "once",
	}}
}

func (f racedWatchFake) AwaitWatch(_ context.Context, id string) (AwaitWatchInfo, error) {
	// Same wording as watch.Manager.WaitFire's not-found path; agent.await
	// matches it only via the watchGoneSentinel substring.
	return AwaitWatchInfo{}, fmt.Errorf("watch %q not found (already fired or stopped)", id)
}

// TestAgentAwaitAnyRacedWatchGoneBecomesFinisher: a once-watch that fires
// during the scan-to-wait window makes WaitFire report it gone; the any
// branch must treat that arrival as the awaited finisher (synthesized from
// the scan snapshot), not hard-error the call.
func TestAgentAwaitAnyRacedWatchGoneBecomesFinisher(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	await := newAwaitTool(state, WithAwaitWatches(racedWatchFake{watchID: "w-race"}))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"any": true}`)})
	if err != nil {
		t.Fatalf("raced watch-gone must not error the any await: %v", err)
	}
	if !strings.Contains(res.Summary, "watch tests raced to a finish") || !strings.Contains(res.Summary, "terminal state unavailable") {
		t.Fatalf("summary = %q, want raced-finisher notice", res.Summary)
	}
	if !strings.Contains(res.Content, "watch_id: w-race") || !strings.Contains(res.Content, "state: raced (fired or stopped") {
		t.Fatalf("content = %q, want synthesized raced entry", res.Content)
	}
	if strings.Contains(res.Summary, "not found") || strings.Contains(res.Content, "not found") {
		t.Fatalf("result leaks the raw watch-gone error: %q / %q", res.Summary, res.Content)
	}
}

// TestAgentAwaitAllRacedWatchGoneCollectsLine: in an all-wait the same raced
// watch-gone error appends a synthesized terminal line and the remaining
// targets are still collected, instead of failing the call.
func TestAgentAwaitAllRacedWatchGoneCollectsRemaining(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	exit := 0
	jobs := &fakeJobSource{outstanding: []AwaitJobInfo{{ID: "job-1", Command: "sleep 1", Status: "running"}}}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		select {
		case <-time.After(20 * time.Millisecond):
			return AwaitJobInfo{ID: id, Command: "sleep 1", Status: "completed", ExitCode: &exit}, nil
		case <-ctx.Done():
			return AwaitJobInfo{}, ctx.Err()
		}
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs), WithAwaitWatches(racedWatchFake{watchID: "w-race"}))
	res, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"all": true}`)})
	if err != nil {
		t.Fatalf("raced watch-gone must not error the all await: %v", err)
	}
	if !strings.Contains(res.Summary, "job job-1 completed") {
		t.Fatalf("summary = %q, want the sibling job result collected", res.Summary)
	}
	if !strings.Contains(res.Summary, "watch tests raced to a finish") {
		t.Fatalf("summary = %q, want the synthesized raced watch line among results", res.Summary)
	}
	if !strings.Contains(res.Content, "state: raced (fired or stopped") {
		t.Fatalf("content = %q, want the raced honesty note", res.Content)
	}
}

// TestAgentAwaitNonSentinelWatchErrorStillErrors: only the watch-gone
// sentinel is forgiven; any other watch-class failure still errors the call.
func TestAgentAwaitNonSentinelWatchErrorStillErrors(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	broken := &fakeWatchSource{
		outstanding: []AwaitWatchInfo{{ID: "w1", Name: "w", Kind: "command", State: "watching", Mode: "once"}},
	}
	broken.wait = func(ctx context.Context, id string) (AwaitWatchInfo, error) {
		return AwaitWatchInfo{}, fmt.Errorf("watch %q unavailable: connection lost", id)
	}
	for _, args := range []string{`{"any": true}`, `{"all": true}`} {
		await := newAwaitTool(state, WithAwaitWatches(broken))
		_, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(args)})
		if err == nil || !strings.Contains(err.Error(), "connection lost") {
			t.Fatalf("args %s: err = %v, want non-sentinel watch error surfaced", args, err)
		}
	}
}

// TestAgentAwaitUnknownJobStillErrors: the job-class analog of watch-gone
// (unknown job swept by the scan) is correct to surface as an error.
func TestAgentAwaitUnknownJobStillErrors(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{})
	jobs := &fakeJobSource{outstanding: []AwaitJobInfo{{ID: "job-1", Command: "sleep 1", Status: "running"}}}
	jobs.wait = func(ctx context.Context, id string) (AwaitJobInfo, error) {
		return AwaitJobInfo{}, fmt.Errorf("job %q not found (already completed or removed)", id)
	}
	await := newAwaitTool(state, WithAwaitJobs(jobs))
	_, err := await.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"any": true}`)})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want unknown-job error surfaced", err)
	}
}
