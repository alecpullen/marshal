package session

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
)

// agentResultByKey finds one agent work-index entry by its stable key.
func agentResultByKey(view db.WorkViewRecord, key string) (db.WorkResultRecord, bool) {
	for _, r := range view.AgentResults {
		if r.TaskKey == key {
			return r, true
		}
	}
	return db.WorkResultRecord{}, false
}

// TestSubagentWorkIdentityAndSnapshotVersions pins the source-snapshot
// contract: a child gets exactly one stable work key and one runtime epoch per
// conversation; every registry mutation advances the source revision; and a
// snapshot is an immutable projection that never hands out a Cancel callback or
// a mutable Child pointer.
func TestSubagentWorkIdentityAndSnapshotVersions(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	child.AddMessage(RoleUser, "work", ContentTypePlain)

	v := s.RegisterSubagent("worker", child)
	if v.WorkKey == "" {
		t.Fatal("registered child has no stable work key")
	}
	if v.RuntimeEpoch == "" {
		t.Fatal("registered child has no runtime epoch")
	}

	first := s.SubagentSnapshot()
	if first.Epoch != v.RuntimeEpoch {
		t.Fatalf("snapshot epoch = %q, want the conversation epoch %q", first.Epoch, v.RuntimeEpoch)
	}
	if len(first.Views) != 1 || first.Views[0].WorkKey != v.WorkKey {
		t.Fatalf("snapshot views = %+v, want one view with work key %q", first.Views, v.WorkKey)
	}

	// One work key per child, stable across snapshots.
	second := s.SubagentSnapshot()
	if second.Views[0].WorkKey != v.WorkKey {
		t.Fatalf("work key changed between snapshots: %q then %q", v.WorkKey, second.Views[0].WorkKey)
	}
	// The projection never publishes the live handle or cancel callback.
	if second.Views[0].Child != nil || second.Views[0].Cancel != nil {
		t.Fatalf("snapshot leaked Child/Cancel: %+v", second.Views[0])
	}

	// A second child shares the single conversation epoch.
	v2 := s.RegisterSubagent("worker2", nil)
	if v2.RuntimeEpoch != v.RuntimeEpoch {
		t.Fatalf("two children carry different epochs: %q vs %q", v.RuntimeEpoch, v2.RuntimeEpoch)
	}

	before := second.Revision
	s.FinishSubagent(v.ID, "done", nil)
	after := s.SubagentSnapshot()
	if after.Revision <= before {
		t.Fatalf("revision did not advance across finish: %d then %d", before, after.Revision)
	}
	if after.Views[0].Outcome != "completed" {
		t.Fatalf("terminal outcome = %q, want completed", after.Views[0].Outcome)
	}
}

// TestSubagentWorkResumeNeverResurrectsChild pins the resume contract: a stored
// running entry becomes history only. It is never turned into a live SubagentView
// and grants no stop capability, and it keeps its ORIGINAL start time rather than
// being stamped with the current time.
func TestSubagentWorkResumeNeverResurrectsChild(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	v := s.RegisterSubagent("worker", nil)

	// The registration persisted a running work entry.
	entry, ok := agentResultByKey(s.WorkView(), v.WorkKey)
	if !ok {
		t.Fatalf("no work-index entry for the registered child %q", v.WorkKey)
	}
	if entry.State != "running" {
		t.Fatalf("stored state = %q, want running", entry.State)
	}

	// Restart the same conversation.
	resumed := persistedState(t, d, sid)
	if got := resumed.Subagents(); len(got) != 0 {
		t.Fatalf("resume resurrected %d live subagent views, want 0", len(got))
	}
	if _, ok := resumed.Subagent(v.ID); ok {
		t.Fatal("resume resurrected the child as a live view")
	}
	if resumed.HasRunningSubagent() {
		t.Fatal("resume reports a running subagent it cannot know about")
	}
	if resumed.CancelSubagent(v.ID) {
		t.Fatal("resume granted a stop capability for a child from a previous runtime")
	}

	// The stored entry survives as history only, with its original start time.
	hist, ok := agentResultByKey(resumed.WorkView(), v.WorkKey)
	if !ok {
		t.Fatalf("history lost the child's work entry %q", v.WorkKey)
	}
	if hist.State != "unknown" || hist.Outcome != "owner-gone" {
		t.Fatalf("restored running entry = state %q outcome %q, want unknown/owner-gone", hist.State, hist.Outcome)
	}
	if !hist.StartedAt.Equal(v.StartedAt) {
		t.Fatalf("restored start time = %v, want the original %v", hist.StartedAt, v.StartedAt)
	}
}

func TestActivityTailPrefersNarration(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AppendThinking("some verbose mid-thought reasoning\n")
	s.AddMessage(RoleAssistant, "Checking the guard before I touch anything.", ContentTypeNarration)

	got := s.SubagentActivityTail(5)
	if len(got) == 0 || !strings.Contains(got[0], "Checking the guard") {
		t.Fatalf("narration must win over streamed reasoning, got %v", got)
	}
}

func TestActivityTailUsesLatestNarration(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AddMessage(RoleAssistant, "first thing", ContentTypeNarration)
	s.AddMessage(RoleAssistant, "second thing", ContentTypeNarration)
	got := s.SubagentActivityTail(5)
	if len(got) == 0 || !strings.Contains(strings.Join(got, " "), "second thing") {
		t.Fatalf("want the most recent narration, got %v", got)
	}
}

// Ordinary assistant prose is not narration and must not be picked up.
func TestActivityTailIgnoresNonNarrationMessages(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AddMessage(RoleAssistant, "an ordinary answer", ContentTypeMarkdown)
	s.AppendThinking("reasoning line\n")
	got := s.SubagentActivityTail(5)
	if strings.Contains(strings.Join(got, " "), "an ordinary answer") {
		t.Fatalf("only ContentTypeNarration counts, got %v", got)
	}
}

// The existing fallbacks are unchanged when there is no narration.
func TestActivityTailFallsBackToReasoningThenAudit(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AppendThinking("reasoning line\n")
	if got := s.SubagentActivityTail(5); len(got) == 0 || !strings.Contains(got[0], "reasoning line") {
		t.Fatalf("want reasoning fallback, got %v", got)
	}
}

func TestActivityTailRespectsN(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.AddMessage(RoleAssistant, "one\ntwo\nthree\nfour\nfive\nsix", ContentTypeNarration)
	if got := s.SubagentActivityTail(2); len(got) > 2 {
		t.Fatalf("want at most 2 lines, got %d: %v", len(got), got)
	}
}
