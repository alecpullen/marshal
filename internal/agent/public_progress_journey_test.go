package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/rollover"
	"marshal/internal/tools/native"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// This deterministic run covers the public story and the actual work in the
// same provider responses. It deliberately uses a failed check followed by a
// successful retry so the first result remains available as immutable evidence.
func TestPublicProgressNativeJourneyKeepsReceiptsAndFailureEvidence(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	checks := 0
	work := 0
	for _, tool := range []registry.Tool{
		{Name: "journey.read", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			work++
			return registry.ToolResult{Summary: "Read target", Content: "old value"}, nil
		}},
		{Name: "journey.patch", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			work++
			return registry.ToolResult{Summary: "Patch applied", Content: "changed value", FilesChanged: []string{"target.txt"}}, nil
		}},
		{Name: "journey.check", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			work++
			checks++
			code := 0
			if checks == 1 {
				code = 1
				return registry.ToolResult{Summary: "Check failed", Content: "expected new value; got old value", CommandExitCode: &code}, nil
			}
			return registry.ToolResult{Summary: "Check passed", Content: "expected new value", CommandExitCode: &code}, nil
		}},
	} {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	progress := func(mode, headline, kind, body, ref string) json.RawMessage {
		refs := ""
		if ref != "" {
			refs = fmt.Sprintf(`,"evidence_refs":[%q]`, ref)
		}
		return json.RawMessage(fmt.Sprintf(`{"mode":%q,"headline":%q,"sections":[{"kind":%q,"text":%q%s}]}`, mode, headline, kind, body, refs))
	}
	call := func(id, name string, args string) schema.ToolCall {
		return schema.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
	}
	var deliveredFailure []session.EvidenceRecord
	var failureWarnings []string
	var deliveredEvidence []session.EvidenceRecord
	var evidenceWarnings []string
	p := &agenttest.ScriptedProvider{
		Responses: []string{"", "", "", "", "The fix is complete."},
		ToolCalls: [][]schema.ToolCall{
			{call("p1", "progress_update", string(progress("begin", "Inspecting target", "work", "Reading the current value.", ""))), call("r1", "journey_read", `{}`)},
			{call("p2", "progress_update", string(progress("revise", "Applying change", "evidence", "The read returned the old value.", "e1-1"))), call("w1", "journey_patch", `{}`)},
			{call("p3", "progress_update", string(progress("revise", "Checking the change", "checking", "Confirming the new value.", "e1-2"))), call("c1", "journey_check", `{}`)},
			{call("p4", "progress_update", string(progress("revise", "Explaining the failed check", "evidence", "The first check reported the old value; retrying after the patch.", "e1-3"))), call("c2", "journey_check", `{}`)},
			nil,
		},
		FinishReasons: []string{"tool_calls", "tool_calls", "tool_calls", "tool_calls", "stop"},
		OnChat: func(idx int, _ schema.ChatRequest) {
			if idx == 4 {
				narration := state.ActivitySnapshot().Narrations[0]
				owner := activity.Ref{RunID: narration.RunID, ActorID: narration.ActorID}
				deliveredFailure, failureWarnings = state.ResolveEvidenceRefs(owner, []string{"e1-3"})
				aliases := []string{"e1-1", "e1-2", "e1-3", "e1-4"}
				deliveredEvidence, evidenceWarnings = state.ResolveEvidenceRefs(owner, aliases)
			}
		},
	}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "Update the target and verify it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.Calls != 5 || work != 4 || state.ToolBudget().Used != 4 {
		t.Fatalf("provider/work/budget = %d/%d/%d, want 5/4/4", p.Calls, work, state.ToolBudget().Used)
	}
	snapshot := state.ActivitySnapshot()
	if len(snapshot.ProgressRevisions) != 4 {
		t.Fatalf("revisions=%+v, want four immutable snapshots", snapshot.ProgressRevisions)
	}
	for i, revision := range snapshot.ProgressRevisions {
		if revision.Revision != uint64(i+1) || revision.NarrationID != snapshot.ProgressRevisions[0].NarrationID {
			t.Fatalf("revision identity/order changed: %+v", snapshot.ProgressRevisions)
		}
	}
	if got := snapshot.ProgressRevisions[2].Sections[0].Text; got != "Confirming the new value." {
		t.Fatalf("failed-check-era revision was mutated: %q", got)
	}
	if got := snapshot.ProgressRevisions[3].Sections[0].Text; got != "The first check reported the old value; retrying after the patch." {
		t.Fatalf("retry explanation missing: %q", got)
	}
	if len(failureWarnings) != 0 || len(deliveredFailure) != 1 || deliveredFailure[0].Summary != "Check failed" || deliveredFailure[0].Content != "expected new value; got old value" || deliveredFailure[0].ExitCode == nil || *deliveredFailure[0].ExitCode != 1 {
		t.Fatalf("failure receipt changed or disappeared: evidence=%+v warnings=%v", deliveredFailure, failureWarnings)
	}
	assertJourneyEvidence(t, state, deliveredEvidence, evidenceWarnings)
	if len(state.AuditLog()) != 4 {
		t.Fatalf("audit records=%d, want exactly four work receipts", len(state.AuditLog()))
	}
	finals := 0
	for _, message := range state.Messages() {
		if message.Final {
			finals++
			if message.Content != "The fix is complete." || message.ToolCallCount != 4 {
				t.Fatalf("final message = %+v", message)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("final answer count=%d, want 1", finals)
	}
}

func TestPublicProgressJSONJourneyKeepsWorkAndMetadataInSameRounds(t *testing.T) {
	state := newTestState(t)
	reg := registry.New()
	if err := reg.Register(native.PublicProgressTool(state)); err != nil {
		t.Fatal(err)
	}
	checks, work := 0, 0
	tools := []registry.Tool{
		{Name: "journey.read", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			work++
			return registry.ToolResult{Summary: "Read target", Content: "old value"}, nil
		}},
		{Name: "journey.patch", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			work++
			return registry.ToolResult{Summary: "Patch applied", Content: "changed value"}, nil
		}},
		{Name: "journey.check", Risk: registry.RiskReadOnly, Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			work++
			checks++
			code := 0
			if checks == 1 {
				code = 1
				return registry.ToolResult{Summary: "Check failed", Content: "expected new value; got old value", CommandExitCode: &code}, nil
			}
			return registry.ToolResult{Summary: "Check passed", Content: "expected new value", CommandExitCode: &code}, nil
		}},
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	responses := []string{
		`{"progress":{"mode":"begin","headline":"Inspecting target","sections":[{"kind":"work","text":"Reading the current value."}]},"action":{"type":"tool_call","tool":"journey.read","args":{}}}`,
		`{"progress":{"mode":"revise","headline":"Applying change","sections":[{"kind":"evidence","text":"The read returned the old value.","evidence_refs":["e1-1"]}]},"action":{"type":"tool_call","tool":"journey.patch","args":{}}}`,
		`{"progress":{"mode":"revise","headline":"Checking the change","sections":[{"kind":"checking","text":"Confirming the new value.","evidence_refs":["e1-2"]}]},"action":{"type":"tool_call","tool":"journey.check","args":{}}}`,
		`{"progress":{"mode":"revise","headline":"Explaining the failed check","sections":[{"kind":"evidence","text":"The first check reported the old value; retrying after the patch.","evidence_refs":["e1-3"]}]},"action":{"type":"tool_call","tool":"journey.check","args":{}}}`,
		`{"progress":{"mode":"revise","headline":"Verified","sections":[{"kind":"evidence","text":"The retry returned the expected value.","evidence_refs":["e1-4"]}]},"action":{"type":"final","content":"The fix is complete."}}`,
	}
	var failure []session.EvidenceRecord
	var warnings []string
	var allEvidence []session.EvidenceRecord
	var allEvidenceWarnings []string
	p := &agenttest.ScriptedProvider{Responses: responses, OnChat: func(idx int, _ schema.ChatRequest) {
		if idx == 4 {
			n := state.ActivitySnapshot().Narrations[0]
			owner := activity.Ref{RunID: n.RunID, ActorID: n.ActorID}
			failure, warnings = state.ResolveEvidenceRefs(owner, []string{"e1-3"})
			allEvidence, allEvidenceWarnings = state.ResolveEvidenceRefs(owner, []string{"e1-1", "e1-2", "e1-3", "e1-4"})
		}
	}}
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test")
	r.SetForceClass(string(ClassQuestion))
	if err := r.Run(context.Background(), "Update the target and verify it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.Calls != 5 || work != 4 || state.ToolBudget().Used != 4 {
		t.Fatalf("provider/work/budget = %d/%d/%d, want 5/4/4", p.Calls, work, state.ToolBudget().Used)
	}
	revisions := state.ActivitySnapshot().ProgressRevisions
	if len(revisions) != 5 || revisions[0].NarrationID != revisions[4].NarrationID || revisions[3].Sections[0].EvidenceRefs[0] != "e1-3" {
		t.Fatalf("JSON revisions lost identity or the failed-check citation: %+v", revisions)
	}
	if len(warnings) != 0 || len(failure) != 1 || failure[0].Summary != "Check failed" || failure[0].Content != "expected new value; got old value" || failure[0].ExitCode == nil || *failure[0].ExitCode != 1 {
		t.Fatalf("JSON failure receipt changed: evidence=%+v warnings=%v", failure, warnings)
	}
	assertJourneyEvidence(t, state, allEvidence, allEvidenceWarnings)
	finals := 0
	for _, message := range state.Messages() {
		if message.Final {
			finals++
			if message.Content != "The fix is complete." || message.ToolCallCount != 4 {
				t.Fatalf("JSON final = %+v", message)
			}
		}
	}
	if finals != 1 || len(state.AuditLog()) != 4 {
		t.Fatalf("JSON final/audit count = %d/%d, want 1/4", finals, len(state.AuditLog()))
	}
}

func assertJourneyEvidence(t *testing.T, state *session.State, records []session.EvidenceRecord, warnings []string) {
	t.Helper()
	exitPtr := func(code int) *int { return &code }
	want := []struct {
		alias, tool, summary, content, err string
		exit                               *int
		approval                           registry.ApprovalState
	}{
		{"e1-1", "journey.read", "Read target", "old value", "", nil, registry.ApprovalNotRequired},
		{"e1-2", "journey.patch", "Patch applied", "changed value", "", nil, registry.ApprovalNotRequired},
		{"e1-3", "journey.check", "Check failed", "expected new value; got old value", "", exitPtr(1), registry.ApprovalNotRequired},
		{"e1-4", "journey.check", "Check passed", "expected new value", "", exitPtr(0), registry.ApprovalNotRequired},
	}
	if len(warnings) != 0 || len(records) != len(want) {
		t.Fatalf("resolved journey evidence=%+v warnings=%v, want four records and no warnings", records, warnings)
	}
	items := state.Transcript()
	for i, expected := range want {
		got := records[i]
		if got.Alias != expected.alias || got.ToolName != expected.tool || got.Summary != expected.summary || got.Content != expected.content {
			t.Fatalf("receipt %d = %+v, want %s %s %q", i, got, expected.alias, expected.tool, expected.content)
		}
		if got.Error != expected.err || got.Denied != (expected.approval == registry.ApprovalDenied) || !equalOptionalInt(got.ExitCode, expected.exit) {
			t.Fatalf("receipt %s outcome error=%q denied=%v exit=%v, want error=%q denied=%v exit=%v", got.Alias, got.Error, got.Denied, got.ExitCode, expected.err, expected.approval == registry.ApprovalDenied, expected.exit)
		}
		var sourceAudit *registry.AuditEvent
		for _, item := range items {
			if item.ViewID == got.SourceViewID && item.Audit != nil {
				sourceAudit = item.Audit
				break
			}
		}
		if sourceAudit == nil {
			t.Fatalf("receipt %s source %q does not identify its exact audited result", got.Alias, got.SourceViewID)
		}
		auditExitMatches := equalOptionalInt(got.ExitCode, sourceAudit.CommandExitCode)
		if sourceAudit.ToolName != got.ToolName || sourceAudit.ResultSummary != got.Summary || sourceAudit.ResultContent != got.Content || sourceAudit.Error != got.Error || (sourceAudit.Approval == registry.ApprovalDenied) != got.Denied || !auditExitMatches || sourceAudit.Approval != expected.approval {
			t.Fatalf("receipt %s does not match source audit %q: receipt=%+v audit=%+v expected approval=%s", got.Alias, got.SourceViewID, got, sourceAudit, expected.approval)
		}
	}
}

func equalOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func TestPublicProgressAndEvidenceSurviveInRunContextRollover(t *testing.T) {
	state := newTestState(t)
	state.AddMessage(session.RoleUser, "continue after compaction", session.ContentTypePlain)
	boundary := state.Messages()[0].ID
	state.BeginActivityRun(boundary)
	headline := "Rollover journey"
	sections := []activity.ProgressSection{{Kind: activity.SectionEvidence, Text: "Read the target."}}
	first, err := state.ApplyPublicProgress(state.BeginActivityResponse(), activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: &headline, Sections: &sections})
	if err != nil {
		t.Fatal(err)
	}
	call := state.BeginActivityCall(first.Owner, "read-call")
	state.LogToolCall(registry.AuditEvent{Activity: call, ToolName: "journey.read", ResultSummary: "Read target", ResultContent: "rollover-source-content"})
	record, ok := state.IssueEvidenceReceipt(call)
	if !ok || record.Alias != "e1-1" {
		t.Fatalf("initial evidence receipt = %+v, %v", record, ok)
	}
	secondResponse := state.BeginActivityResponse()
	secondSections := []activity.ProgressSection{{Kind: activity.SectionEvidence, Text: "The active receipt remains available.", EvidenceRefs: []string{record.Alias}}}
	if _, err := state.ApplyPublicProgress(secondResponse, activity.ProgressUpdate{Mode: activity.ProgressRevise, Sections: &secondSections}); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(true)
	controller.Policy = rollover.Policy{Mode: rollover.PolicyContextPercent, ContextPercent: 0}
	runner := &Runner{Registry: registry.New(), Policy: policy.NewEngine(&config.Config{}, nil), State: state, Model: "test-model", MaxTurnContextTokens: 1000, Rollover: &Rollover{Controller: controller, State: state}}
	wire := []schema.ChatMessage{{Role: schema.RoleUser, Content: "continue after compaction"}, {Role: schema.RoleAssistant, Content: "ROLL_OVER_PUBLIC_PROGRESS_SOURCE and rollover-source-content"}}
	fresh, err := rolloverAndContinue(context.Background(), runner, wire, "continue after compaction", runner.MaxTurnContextTokens)
	if err != nil {
		t.Fatalf("rolloverAndContinue: %v", err)
	}
	var rebuilt strings.Builder
	for _, message := range fresh {
		rebuilt.WriteString(message.Content)
		rebuilt.WriteByte('\n')
	}
	if strings.Contains(rebuilt.String(), "ROLL_OVER_PUBLIC_PROGRESS_SOURCE") || strings.Contains(rebuilt.String(), "rollover-source-content") {
		t.Fatalf("compacted wire content was replayed into fresh history: %q", rebuilt.String())
	}
	snapshot := state.ActivitySnapshot()
	if len(snapshot.ProgressRevisions) != 2 || snapshot.ProgressRevisions[1].Sections[0].EvidenceRefs[0] != "e1-1" {
		t.Fatalf("structured progress did not survive rollover exactly once: %+v", snapshot.ProgressRevisions)
	}
	owner := activity.Ref{RunID: snapshot.Narrations[0].RunID, ActorID: snapshot.Narrations[0].ActorID}
	resolved, warnings := state.ResolveEvidenceRefs(owner, []string{"e1-1"})
	if len(warnings) != 0 || len(resolved) != 1 || resolved[0].Content != "rollover-source-content" {
		t.Fatalf("active-run evidence after rollover = %+v warnings=%v", resolved, warnings)
	}
}

func TestPublicProgressDoesNotChangeApprovalOrWorkOutcome(t *testing.T) {
	type outcome struct {
		requests, executions int
		approval             registry.ApprovalState
		toolName, args       string
		result               string
		pairedReplyID        string
	}
	run := func(t *testing.T, withProgress bool) outcome {
		t.Helper()
		state := newTestState(t)
		reg := registry.New()
		if withProgress {
			if err := reg.Register(native.PublicProgressTool(state)); err != nil {
				t.Fatal(err)
			}
		}
		var executions int
		if err := reg.Register(registry.Tool{Name: "web.fetch", Risk: registry.RiskNetwork, Schema: json.RawMessage(`{"type":"object","additionalProperties":false}`), Handler: func(_ context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			executions++
			return registry.ToolResult{Summary: "Fetched fixture", Content: "stable response"}, nil
		}}); err != nil {
			t.Fatal(err)
		}
		first := `{"action":{"type":"tool_call","tool":"web.fetch","args":{}}}`
		if withProgress {
			first = `{"progress":{"mode":"begin","headline":"Fetching fixture"},"action":{"type":"tool_call","tool":"web.fetch","args":{}}}`
		}
		provider := &agenttest.ScriptedProvider{Responses: []string{first, `{"action":{"type":"final","content":"done"}}`}}
		runner := NewRunner(provider, reg, policy.NewEngine(&config.Config{}, nil), state, "approval-equivalence")
		runner.SetForceClass(string(ClassQuestion))
		runErr := make(chan error, 1)
		go func() { runErr <- runner.Run(context.Background(), "fetch fixture") }()
		var pending *session.PendingToolCall
		deadline := time.After(5 * time.Second)
		for pending == nil {
			select {
			case <-deadline:
				t.Fatal("timed out waiting for the network approval")
			default:
				pending = state.PendingApproval()
				time.Sleep(time.Millisecond)
			}
		}
		if pending.Name != "web.fetch" || pending.ResponseChan == nil || cap(pending.ResponseChan) != 1 {
			t.Fatalf("approval request = %+v, want one buffered web.fetch decision channel", pending)
		}
		pending.ResponseChan <- session.UserApprovalDecision{Approved: true}
		select {
		case err := <-runErr:
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for approved tool result")
		}
		audits := state.AuditLog()
		if len(audits) != 1 || len(provider.Requests) != 2 || executions != 1 {
			t.Fatalf("calls/audits/requests=%d/%d/%d, want 1/2/1", executions, len(audits), len(provider.Requests))
		}
		var replyID string
		for _, message := range provider.Requests[1].Messages {
			if message.Role == schema.RoleTool {
				replyID = message.ToolCallID
			}
		}
		return outcome{requests: provider.Calls, executions: executions, approval: audits[0].Approval, toolName: audits[0].ToolName, args: string(audits[0].Args), result: audits[0].ResultContent, pairedReplyID: replyID}
	}
	without := run(t, false)
	with := run(t, true)
	if without != with {
		t.Fatalf("optional progress changed approval/work outcome:\nwithout=%+v\nwith=%+v", without, with)
	}
	if with.approval != registry.ApprovalApproved || with.pairedReplyID != "" || with.requests != 2 || with.executions != 1 {
		t.Fatalf("unexpected approved outcome = %+v", with)
	}
}
