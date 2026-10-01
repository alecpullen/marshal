package session

import (
	"testing"

	"marshal/internal/activity"
	"marshal/internal/tools/registry"
)

func TestEvidenceReceiptUsesExplicitAuditAndActorScope(t *testing.T) {
	s := newTestState()
	run := s.BeginActivityRun(addActivityBoundary(s))
	response := s.BeginActivityResponse()
	response = s.BindActivityFallback(response)
	owner := s.BeginActivityCall(response, "provider-1")
	exit := 7
	event := registry.AuditEvent{
		Activity: owner, ToolName: "shell.run", ResultSummary: "completed",
		ResultContent: "command output", Approval: registry.ApprovalApproved,
		CommandExitCode: &exit, FilesChanged: []string{"report.txt"},
		Notice: &registry.ToolNotice{Kind: registry.NoticeSliceTruncated, Text: "output shortened"},
	}
	s.LogToolCall(event)
	providerMismatch := owner
	providerMismatch.ProviderCallID = "different-provider-call"
	if _, ok := s.IssueEvidenceReceipt(providerMismatch); ok {
		t.Fatal("issued a receipt for an audit with a different provider call ID")
	}
	record, ok := s.IssueEvidenceReceipt(owner)
	if !ok {
		t.Fatal("audited call did not receive evidence alias")
	}
	if record.Alias != "e1-1" || record.Owner != owner || record.SourceViewID == "" || record.ToolName != "shell.run" || record.Error != "" || record.Denied {
		t.Fatalf("record = %+v", record)
	}
	if record.ExitCode == nil || *record.ExitCode != 7 || len(record.FilesChanged) != 1 || record.Retention == "" {
		t.Fatalf("record omitted observed facts: %+v", record)
	}
	deniedOwner := s.BeginActivityCall(response, "provider-denied")
	s.LogToolCall(registry.AuditEvent{Activity: deniedOwner, ToolName: "file.write_patch", Approval: registry.ApprovalDenied, Error: "denied by policy"})
	denied, ok := s.IssueEvidenceReceipt(deniedOwner)
	if !ok || !denied.Denied || denied.Error != "denied by policy" {
		t.Fatalf("denied receipt = %+v, ok=%v", denied, ok)
	}
	reportOwner := s.BeginActivityCall(response, "provider-report")
	s.LogToolCall(registry.AuditEvent{Activity: reportOwner, ToolName: "agent.output", ResultSummary: "report", ResultContent: "Report: tests passed"})
	report, ok := s.IssueEvidenceReceipt(reportOwner)
	if !ok || report.ToolName != "agent.output" || report.Content != "Report: tests passed" {
		t.Fatalf("agent output was promoted or rewritten: %+v, ok=%v", report, ok)
	}
	changedOwner := s.BindActivityNarration(s.BeginActivityResponse(), "another response")
	s.AddNarrationMessage(changedOwner, "another response")
	changedOwner.CallID = reportOwner.CallID
	if _, ok := s.IssueEvidenceReceipt(changedOwner); ok {
		t.Fatal("reused a receipt for the same call ID under a different response/narration owner")
	}
	record.FilesChanged[0] = "mutated"
	resolved, warnings := s.ResolveEvidenceRefs(response, []string{"e1-1", "e1-1"})
	if len(warnings) != 0 || len(resolved) != 1 || resolved[0].FilesChanged[0] != "report.txt" {
		t.Fatalf("resolved=%+v warnings=%v", resolved, warnings)
	}
	foreign := response
	foreign.ActorID = "sibling"
	if _, ok := s.IssueEvidenceReceipt(foreign); ok {
		t.Fatal("issued a receipt to a sibling actor")
	}
	if got, warnings := s.ResolveEvidenceRefs(foreign, []string{"e1-1"}); len(got) != 0 || len(warnings) != 1 {
		t.Fatalf("sibling resolved evidence: records=%+v warnings=%v", got, warnings)
	}
	if _, warnings := s.ResolveEvidenceRefs(response, []string{"e1-999"}); len(warnings) != 1 {
		t.Fatalf("future alias warnings = %v", warnings)
	}
	s.EndActivityRun(run)
	if got, warnings := s.ResolveEvidenceRefs(response, []string{"e1-1"}); len(got) != 0 || len(warnings) != 1 {
		t.Fatalf("ended run resolved evidence: records=%+v warnings=%v", got, warnings)
	}
}

func TestEvidenceReceiptRequiresExactCallAuditAndEvictsOldest(t *testing.T) {
	s := newTestState()
	run := s.BeginActivityRun(addActivityBoundary(s))
	response := s.BeginActivityResponse()
	response = s.BindActivityFallback(response)
	first := activity.Ref{}
	for i := 0; i < maxEvidenceAliases+1; i++ {
		owner := s.BeginActivityCall(response, "provider")
		if i == 0 {
			first = owner
		}
		s.LogToolCall(registry.AuditEvent{Activity: owner, ToolName: "file.read", ResultContent: "x"})
		if _, ok := s.IssueEvidenceReceipt(owner); !ok {
			t.Fatalf("call %d was not issued an alias", i)
		}
	}
	if _, ok := s.IssueEvidenceReceipt(first); ok {
		t.Fatal("reissued an evicted source call")
	}
	if records, warnings := s.ResolveEvidenceRefs(response, []string{"e1-1"}); len(records) != 0 || len(warnings) != 1 {
		t.Fatalf("oldest alias survived eviction: records=%+v warnings=%v", records, warnings)
	}
	if records, warnings := s.ResolveEvidenceRefs(response, []string{"e1-257"}); len(records) != 1 || len(warnings) != 0 {
		t.Fatalf("newest alias unavailable: records=%+v warnings=%v", records, warnings)
	}
	unknown := first
	unknown.CallID = "future-call"
	if _, ok := s.IssueEvidenceReceipt(unknown); ok {
		t.Fatal("issued alias without an exact audited call")
	}
	s.EndActivityRun(run)
}
