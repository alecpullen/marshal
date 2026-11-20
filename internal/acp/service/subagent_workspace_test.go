package service

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/subagentwork"
)

// workspaceRecord is one retained owned-workspace record a conversation holds,
// in the states a card must be able to distinguish.
func workspaceRecord(taskKey, candidate string) subagentwork.Record {
	return subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: "s1",
		TaskKey:        taskKey,
		InvocationID:   "inv-" + taskKey,
		Binding: subagentwork.Binding{
			Mode:    subagentwork.ModeIsolated,
			Root:    "/worktrees/" + taskKey,
			Branch:  "marshal/" + taskKey,
			BaseSHA: "1111111111111111111111111111111111111111",
		},
		Target:    subagentwork.Target{Root: "/repo", Branch: "main", BaseSHA: "1111111111111111111111111111111111111111"},
		Revision:  2,
		Outcome:   subagentwork.OutcomeDone,
		Candidate: candidate,
	}
}

// TestWorkspaceProjectionUnknownAndCleanupPending pins the subagent card's
// workspace evidence across the client seam: a conversation with no record
// sends none at all, and every recorded lifecycle state — including the unknown
// and the not-yet-integrated ones — survives as itself, with nothing inferred
// as success.
func TestWorkspaceProjectionUnknownAndCleanupPending(t *testing.T) {
	state := newState(t, "s1")
	_, sess := New(Deps{State: state})

	// A legacy card: the conversation recorded no owned workspace for it, so the
	// wire omits the payload entirely rather than sending an empty record.
	legacyChild := newState(t, "child-legacy")
	legacy := state.RegisterSubagentWithMeta("legacy", legacyChild, session.SubagentMeta{WorkKey: "task-legacy"})

	// A known, integrated-but-cleanup-pending child.
	state.RegisterSubagentWithMeta("known", newState(t, "child-known"), session.SubagentMeta{WorkKey: "task-1"})
	integrated := workspaceRecord("task-1", "2222222222222222222222222222222222222222")
	integrated.Integration = &subagentwork.Integration{
		OperationID: "op-1", Kind: subagentwork.KindIntegrate,
		State:  subagentwork.IntegrationIntegrated,
		Reason: "merged into main",
	}
	integrated.CleanupState = subagentwork.CleanupIntegratedPending
	state.ApplySubagentWorkspace(integrated)

	// A candidate nobody has integrated yet: the record holds the commit and
	// names no integration at all.
	state.RegisterSubagentWithMeta("unintegrated", newState(t, "child-2"), session.SubagentMeta{WorkKey: "task-2"})
	state.ApplySubagentWorkspace(workspaceRecord("task-2", "3333333333333333333333333333333333333333"))

	// An interrupted child: unknown execution, no candidate, no integration.
	state.RegisterSubagentWithMeta("interrupted", newState(t, "child-3"), session.SubagentMeta{WorkKey: "task-3"})
	interrupted := subagentwork.Record{
		Version: subagentwork.RecordVersion, OwnerSessionID: "s1", TaskKey: "task-3",
		Binding:  subagentwork.Binding{Mode: subagentwork.ModeShared, Root: "/repo"},
		Revision: 1, Outcome: subagentwork.OutcomeInterrupted,
	}
	state.ApplySubagentWorkspace(interrupted)

	views := sess.Status().Subagents
	byID := make(map[int64]acpwire.Subagent, len(views))
	for _, v := range views {
		byID[v.ID] = v
	}

	if got := byID[legacy.ID].Workspace; got != nil {
		t.Fatalf("a child with no owned workspace was sent %+v, want no payload", got)
	}

	known := byID[views[1].ID].Workspace
	if known == nil {
		t.Fatalf("the integrated child sent no workspace evidence")
	}
	if known.TaskKey != "task-1" || known.CandidateSHA != "2222222222222222222222222222222222222222" {
		t.Fatalf("identity = %+v", known)
	}
	if known.Mode != string(subagentwork.ModeIsolated) || known.Root != "/worktrees/task-1" ||
		known.Branch != "marshal/task-1" || known.TargetBranch != "main" || known.TargetRoot != "/repo" {
		t.Fatalf("binding/target = %+v", known)
	}
	// The cleanup pending state is the record's, and the wire says so rather
	// than reporting the work integrated-and-finished.
	if known.Integration != subagentwork.IntegrationIntegrated ||
		known.Cleanup != subagentwork.CleanupIntegratedPending {
		t.Fatalf("states = %+v, want integrated with cleanup pending", known)
	}

	unintegratedCard := byID[views[2].ID].Workspace
	if unintegratedCard == nil {
		t.Fatalf("the unintegrated child sent no workspace evidence")
	}
	if unintegratedCard.CandidateSHA != "3333333333333333333333333333333333333333" {
		t.Fatalf("candidate = %q, want the child's commit", unintegratedCard.CandidateSHA)
	}
	// A candidate is not an integration: nothing was integrated, so nothing is
	// reported as integrated.
	if unintegratedCard.Integration != "" || unintegratedCard.Cleanup != "" {
		t.Fatalf("an unintegrated candidate reported %+v, want no integration and no cleanup",
			unintegratedCard)
	}

	unknownCard := byID[views[3].ID].Workspace
	if unknownCard == nil {
		t.Fatalf("the interrupted child sent no workspace evidence")
	}
	if unknownCard.Execution != subagentwork.OutcomeInterrupted {
		t.Fatalf("execution = %q, want the recorded interruption", unknownCard.Execution)
	}
	if unknownCard.CandidateSHA != "" {
		t.Fatalf("candidate = %q, want empty: the child produced no commit", unknownCard.CandidateSHA)
	}

	// The wire form itself never states a success it was not told about: an
	// absent integration is omitted, not encoded as a zero-valued state.
	raw, err := json.Marshal(unintegratedCard)
	if err != nil {
		t.Fatalf("marshal workspace: %v", err)
	}
	if strings.Contains(string(raw), `"integration"`) || strings.Contains(string(raw), `"cleanup"`) {
		t.Fatalf("an unintegrated workspace encoded %s, which reads as a recorded state", raw)
	}
}
