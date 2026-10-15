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

// The watch-race tests that lived here were removed with the watch await
// class in the context-diet Tier 2a cut: agent.await no longer waits on
// watches, so there is no scan-to-wait race left to handle. The watch
// subsystem itself is unchanged and covered by internal/watch.
//
// TestAgentAwaitUnknownJobStillErrors: a job swept away between the
// outstanding scan and the wait is a genuine error, not a silent finisher.
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
