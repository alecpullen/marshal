package acp

import (
	"context"
	"encoding/json"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestStepDiffsOverACP(t *testing.T) {
	s := newSyncStack(t)
	s.st.AddMessage(session.RoleUser, "go", session.ContentTypePlain)
	step := s.st.BeginStep(session.Actor{})
	s.st.AddNarration(step, "Patching.")
	s.st.LogToolCall(registry.AuditEvent{ToolName: "file.write_patch", StepID: int64(step), ToolCallID: "c1",
		FilesChanged: []string{"a.go"}, ResultContent: "--- a\n+++ b"})
	got, err := s.StepDiffs(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	steps := got.(map[string]any)["steps"].([]StepDiffJSON)
	if len(steps) != 1 || steps[0].Diff != "--- a\n+++ b" || steps[0].Files[0] != "a.go" {
		t.Fatalf("steps = %+v", steps)
	}
}

func TestCommitDraftNeverCommits(t *testing.T) {
	git := &fakeExitGit{dirty: true}
	m := NewExitManager(ExitManagerConfig{
		Lookup:       func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Git:          git,
		DraftMessage: func(context.Context, *ExitRuntime) (string, error) { return "feat: drafted", nil },
	})
	got, err := m.CommitDraft(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["message"] != "feat: drafted" || git.committed != "" {
		t.Fatalf("got %v, committed %q", got, git.committed)
	}
}
