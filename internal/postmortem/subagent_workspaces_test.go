package postmortem

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/subagentwork"
	"marshal/internal/tools/registry"
)

// workspaceRecord is one retained owned-workspace record the session holds, in
// the shape the dispatcher applies it: the durable task identity a report reader
// pairs with the child's command evidence.
func workspaceRecord(taskKey, candidate string) subagentwork.Record {
	return subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: testSessionID,
		TaskKey:        taskKey,
		InvocationID:   "inv-" + taskKey,
		Binding: subagentwork.Binding{
			Mode:    subagentwork.ModeIsolated,
			Root:    "/worktrees/" + taskKey,
			Branch:  "marshal/" + taskKey,
			BaseSHA: "1111111111111111111111111111111111111111",
		},
		Target: subagentwork.Target{
			Root:    "/repo",
			Branch:  "main",
			BaseSHA: "1111111111111111111111111111111111111111",
		},
		Revision:     3,
		Outcome:      subagentwork.OutcomeDone,
		Candidate:    candidate,
		ChangedPaths: []string{"internal/viewmodel/detail.go"},
		// The full report body and the environment live where they belong —
		// the durable report message and the record's own setup plan — and must
		// not appear in an aggregated report.
		ReportMessageID: 42,
		SetupWarnings:   []string{"seed skipped node_modules"},
		Integration: &subagentwork.Integration{
			OperationID:    "op-1",
			Kind:           subagentwork.KindIntegrate,
			State:          subagentwork.IntegrationIntegrated,
			ExpectedSource: candidate,
			BeforeTarget:   "1111111111111111111111111111111111111111",
			ResultCommit:   "3333333333333333333333333333333333333333",
			Reason:         "merged into main",
		},
		CleanupState: subagentwork.CleanupIntegratedPending,
	}
}

// TestPostmortemWorkspaceEvidence pins the report's workspace section: the same
// durable task identity and candidate a command's cwd evidence sits beside, with
// no environment, secret or duplicate copy of the child's report.
func TestPostmortemWorkspaceEvidence(t *testing.T) {
	state := testState(t, testConfig(), nil)
	code := 0
	// One managed command the child ran: its cwd evidence is the other half of
	// the same story.
	state.LogToolCall(registry.AuditEvent{
		Timestamp: time.Unix(2000, 0).UTC(),
		ToolName:  "shell.run",
		Notice: registry.NewCommandExecutionNotice(registry.CommandExecution{
			ExecutionID: "exec-1", DisplayID: "job-1", ConversationID: testSessionID,
			Actor: "task-1", Cwd: "/worktrees/task-1", Generation: 1,
			State: "completed", Outcome: "completed",
			Attachment: registry.ExecutionAttachmentForeground, ExitCode: &code,
		}),
	})
	state.ApplySubagentWorkspace(workspaceRecord("task-1", "2222222222222222222222222222222222222222"))

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(report.ExecutionEvidence) != 1 {
		t.Fatalf("execution_evidence = %+v, want the command's own evidence", report.ExecutionEvidence)
	}
	if len(report.SubagentWorkspaces) != 1 {
		t.Fatalf("subagent_workspaces = %+v, want one entry", report.SubagentWorkspaces)
	}
	ws := report.SubagentWorkspaces[0]
	if ws.OwnerSessionID != testSessionID || ws.TaskKey != "task-1" {
		t.Fatalf("identity = %+v, want this session's task-1", ws)
	}
	if ws.CandidateSHA != "2222222222222222222222222222222222222222" {
		t.Fatalf("candidate = %q, want the record's exact candidate", ws.CandidateSHA)
	}
	if ws.Mode != string(subagentwork.ModeIsolated) || ws.Branch != "marshal/task-1" ||
		ws.BaseSHA != "1111111111111111111111111111111111111111" {
		t.Fatalf("binding = %+v", ws)
	}
	if ws.TargetRoot != "/repo" || ws.TargetBranch != "main" {
		t.Fatalf("target = %+v", ws)
	}
	// The states are the record's, never a verdict: an integrated worktree whose
	// cleanup is still pending says exactly that.
	if ws.Execution != subagentwork.OutcomeDone || ws.Integration != subagentwork.IntegrationIntegrated ||
		ws.Cleanup != subagentwork.CleanupIntegratedPending {
		t.Fatalf("states = %+v", ws)
	}
	// The command's cwd evidence and the workspace's root name the same tree, so
	// a reader pairs them without prose.
	if report.ExecutionEvidence[0].Cwd != ws.Root {
		t.Fatalf("command cwd %q does not name the workspace root %q",
			report.ExecutionEvidence[0].Cwd, ws.Root)
	}

	// No environment, secret or duplicated report body reaches the report.
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	encoded := string(raw)
	for _, leak := range []string{"node_modules", "setup_plan", "ChangedPaths", "detail.go"} {
		if strings.Contains(encoded, leak) {
			t.Fatalf("the report carries %q, which belongs to the record rather than the aggregated report", leak)
		}
	}

	// An interrupted record keeps its available evidence and gains no invented
	// success: an empty integration and an empty candidate stay empty.
	state.ApplySubagentWorkspace(subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: testSessionID,
		TaskKey:        "task-2",
		Binding:        subagentwork.Binding{Mode: subagentwork.ModeShared, Root: "/repo"},
		Revision:       1,
		Outcome:        subagentwork.OutcomeInterrupted,
	})
	report, err = Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var interrupted *SubagentWorkspaceEvidence
	for i := range report.SubagentWorkspaces {
		if report.SubagentWorkspaces[i].TaskKey == "task-2" {
			interrupted = &report.SubagentWorkspaces[i]
		}
	}
	if interrupted == nil {
		t.Fatalf("subagent_workspaces = %+v, want the interrupted child", report.SubagentWorkspaces)
	}
	if interrupted.Execution != subagentwork.OutcomeInterrupted {
		t.Fatalf("execution = %q, want the recorded interruption", interrupted.Execution)
	}
	if interrupted.CandidateSHA != "" || interrupted.Integration != "" || interrupted.Cleanup != "" {
		t.Fatalf("an interrupted record reported an outcome it never reached: %+v", *interrupted)
	}
}

// TestPostmortemWorkspaceEvidenceIsSessionScoped pins the owner scoping the
// report inherits from the conversation: another session's records are never
// folded into this one's report.
func TestPostmortemWorkspaceEvidenceIsSessionScoped(t *testing.T) {
	state := testState(t, testConfig(), nil)
	foreign := workspaceRecord("task-foreign", "4444444444444444444444444444444444444444")
	foreign.OwnerSessionID = "someone-else"
	state.ApplySubagentWorkspace(foreign)

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(report.SubagentWorkspaces) != 0 {
		t.Fatalf("subagent_workspaces = %+v, want none: the record belongs to another session",
			report.SubagentWorkspaces)
	}
}
