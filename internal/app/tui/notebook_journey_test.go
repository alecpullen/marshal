package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/agent"
	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// This fixture follows one ordinary tool-assisted turn through the real
// session APIs, then asks both transcript projections for every source and
// copy payload. The notebook may group the records differently, but it must
// preserve the same readable source identities and copy bytes.
func TestNotebookJourneyKeepsLegacySourcesAndCopyOutputs(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	state := session.New(config.Default(), t.TempDir(), now, session.Persistence{})
	state.AddMessage(session.RoleUser, "Inspect the parser", session.ContentTypePlain)
	request := state.Messages()[0]
	run := state.BeginActivityRun(request.ID)
	response := state.BeginActivityResponse()
	state.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "private reasoning"})
	response = state.BindActivityNarration(response, "I will inspect the parser and report what I find.")
	state.AddNarrationMessage(response, "I will inspect the parser and report what I find.")
	call := state.BeginActivityCall(response, "call-1")
	state.SetActiveToolCall(session.ActiveToolCall{Name: "file.read", Args: `{"path":"parser.go"}`, StartedAt: now})
	state.LogToolCall(registry.AuditEvent{
		Activity: call, Timestamp: now, ToolName: "file.read", Approval: registry.ApprovalNotRequired,
		ResultSummary: "Read parser.go", ResultContent: "func Parse() {}\n",
	})
	state.SetActiveToolCall(session.ActiveToolCall{})
	response2 := state.BeginActivityResponse()
	response2 = state.BindActivityNarration(response2, "The parser has one entry point.")
	state.AddNarrationMessage(response2, "The parser has one entry point.")
	state.AddMessageFinal(session.RoleAssistant, "Parse is the only entry point.", session.ContentTypeMarkdown)
	state.EndActivityRun(run)

	items := state.Transcript()
	// Freeze records to a shared clock tick. Their ownership/sequence, rather
	// than timestamp sorting, must keep concurrent records distinguishable.
	for i := range items {
		items[i].Timestamp = now
	}
	snapshot := state.ActivitySnapshot()
	legacy := projectConversation(items, snapshot, conversationProjectionOptions{})
	notebook := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
	if legacy.Len() == 0 || notebook.Len() == 0 {
		t.Fatal("journey produced an empty transcript projection")
	}

	want := map[session.TranscriptKind]bool{}
	for _, item := range items {
		if item.ViewID == "" {
			t.Fatalf("session fixture emitted a source without identity: %+v", item)
		}
		want[item.Kind] = true
		legacyLoc, legacyOK := legacy.LocateMember(item.ViewID)
		notebookLoc, notebookOK := notebook.LocateMember(item.ViewID)
		if !legacyOK || !notebookOK {
			t.Fatalf("source %q coverage legacy=%v notebook=%v", item.ViewID, legacyOK, notebookOK)
		}
		if !reflect.DeepEqual(legacyLoc.Block.CopyTargets, notebookLoc.Block.CopyTargets) {
			t.Fatalf("source %q copy output changed across views:\nlegacy=%+v\nnotebook=%+v", item.ViewID, legacyLoc.Block.CopyTargets, notebookLoc.Block.CopyTargets)
		}
	}
	for _, kind := range []session.TranscriptKind{session.KindMessage, session.KindAudit} {
		if !want[kind] {
			t.Fatalf("journey did not exercise transcript kind %d", kind)
		}
	}
	for _, block := range notebook.Blocks() {
		if block.Kind == conversation.BlockOwnershipNote {
			t.Fatalf("live owned journey acquired fallback note: %+v", block)
		}
	}
}

func TestStructuredProgressJourneyProjectsChronologicalLegacyAndNotebook(t *testing.T) {
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	state := session.New(config.Default(), t.TempDir(), now, session.Persistence{})
	state.AddMessage(session.RoleUser, "Read, change, and verify", session.ContentTypePlain)
	boundary := state.Messages()[0].ID
	state.BeginActivityRun(boundary)
	headline := "Inspecting the target"
	begin, err := state.ApplyPublicProgress(state.BeginActivityResponse(), activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: &headline})
	if err != nil {
		t.Fatal(err)
	}
	readCall := state.BeginActivityCall(begin.Owner, "read-call")
	state.LogToolCall(registry.AuditEvent{Activity: readCall, Timestamp: now, ToolName: "file.read", ResultSummary: "Read target", ResultContent: "old value"})
	readReceipt, ok := state.IssueEvidenceReceipt(readCall)
	if !ok {
		t.Fatal("read result did not issue an evidence receipt")
	}
	sections := []activity.ProgressSection{{Kind: activity.SectionEvidence, Text: "The read returned the old value.", EvidenceRefs: []string{readReceipt.Alias}}, {Kind: activity.SectionChecking, Text: "Checking the updated value."}}
	updatedHeadline := "Verified change"
	revised, err := state.ApplyPublicProgress(state.BeginActivityResponse(), activity.ProgressUpdate{Mode: activity.ProgressRevise, Headline: &updatedHeadline, Sections: &sections})
	if err != nil {
		t.Fatal(err)
	}
	state.AddMessageFinal(session.RoleAssistant, "The target now has the expected value.", session.ContentTypeMarkdown)
	state.EndActivityRun(revised.Owner)
	items := state.Transcript()
	snapshot := state.ActivitySnapshot()
	if got := snapshot.ProgressRevisions[1].Sections[0].EvidenceSources; len(got) != 1 || got[0].Alias != readReceipt.Alias || got[0].SourceViewID != readReceipt.SourceViewID {
		t.Fatalf("accepted evidence source identity = %+v, receipt=%+v", got, readReceipt)
	}
	legacy := projectConversation(items, snapshot, conversationProjectionOptions{})
	notebook := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
	legacyOrder := make(map[string]int)
	for i, block := range legacy.Blocks() {
		legacyOrder[string(block.ID)] = i
	}
	var journeySources []session.TranscriptItem
	for _, item := range items {
		if item.ViewID == "" {
			t.Fatalf("transcript source lacks identity: %+v", item)
		}
		left, leftOK := legacy.LocateMember(item.ViewID)
		right, rightOK := notebook.LocateMember(item.ViewID)
		if !leftOK || !rightOK {
			t.Fatalf("source %q coverage legacy=%v notebook=%v", item.ViewID, leftOK, rightOK)
		}
		if !reflect.DeepEqual(left.Block.CopyTargets, right.Block.CopyTargets) {
			t.Fatalf("source %q copy bytes changed across projections: legacy=%+v notebook=%+v", item.ViewID, left.Block.CopyTargets, right.Block.CopyTargets)
		}
		if item.Kind == session.KindMessage || item.Kind == session.KindAudit {
			journeySources = append(journeySources, item)
		}
	}
	for i := 1; i < len(journeySources); i++ {
		before := legacyOrder[journeySources[i-1].ViewID]
		after := legacyOrder[journeySources[i].ViewID]
		if before >= after {
			t.Fatalf("legacy chronology changed at %q then %q: block positions %d/%d", journeySources[i-1].ViewID, journeySources[i].ViewID, before, after)
		}
	}
	if len(snapshot.ProgressRevisions) != 2 || snapshot.ProgressRevisions[1].Sections[0].EvidenceRefs[0] != readReceipt.Alias {
		t.Fatalf("journey did not retain its cited revision: %+v", snapshot.ProgressRevisions)
	}
	latestID := ""
	for _, item := range items {
		if item.Message != nil && item.Message.ID == snapshot.ProgressRevisions[1].SourceMessageID {
			latestID = item.ViewID
		}
	}
	loc, ok := notebook.LocateMember(latestID)
	if !ok || len(loc.Ancestors) == 0 {
		t.Fatalf("latest revision is not inside the notebook narration: source=%q loc=%+v", latestID, loc)
	}
	parent, ok := notebook.Block(loc.Ancestors[0])
	if !ok || len(parent.EventOrderAlternatives) < 2 || parent.EventOrderAlternatives[0].SourceRevision != 1 || parent.EventOrderAlternatives[1].SourceRevision != 2 {
		t.Fatalf("notebook does not preserve both revisions in event order: %+v", parent)
	}
}

func TestAcceptedEvidenceSurvivesAliasEvictionAndRunChange(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	state.AddMessage(session.RoleUser, "Inspect and report", session.ContentTypePlain)
	run := state.BeginActivityRun(state.Messages()[0].ID)
	headline := "Inspecting"
	begin, err := state.ApplyPublicProgress(state.BeginActivityResponse(), activity.ProgressUpdate{Mode: activity.ProgressBegin, Headline: &headline})
	if err != nil {
		t.Fatal(err)
	}
	call := state.BeginActivityCall(begin.Owner, "provider-original")
	state.LogToolCall(registry.AuditEvent{Activity: call, ToolName: "file.read", ResultSummary: "Read source", ResultContent: "source bytes"})
	first, ok := state.IssueEvidenceReceipt(call)
	if !ok {
		t.Fatal("initial evidence receipt was not issued")
	}
	sections := []activity.ProgressSection{{Kind: activity.SectionEvidence, Text: "The source returned these bytes.", EvidenceRefs: []string{first.Alias}}}
	if _, err := state.ApplyPublicProgress(state.BeginActivityResponse(), activity.ProgressUpdate{Mode: activity.ProgressRevise, Sections: &sections}); err != nil {
		t.Fatal(err)
	}
	acceptedSnapshot := state.ActivitySnapshot()
	var acceptedSection activity.ProgressSection
	for _, revision := range acceptedSnapshot.ProgressRevisions {
		if len(revision.Sections) > 0 && len(revision.Sections[0].EvidenceSources) > 0 {
			acceptedSection = revision.Sections[0]
		}
	}
	if len(acceptedSection.EvidenceSources) != 1 || acceptedSection.EvidenceSources[0].SourceViewID != first.SourceViewID {
		t.Fatalf("accepted section did not capture canonical source: %+v", acceptedSection)
	}
	for i := 0; i < 256; i++ {
		owner := state.BeginActivityCall(begin.Owner, fmt.Sprintf("provider-%d", i))
		state.LogToolCall(registry.AuditEvent{Activity: owner, ToolName: "file.read", ResultSummary: "later source"})
		if _, ok := state.IssueEvidenceReceipt(owner); !ok {
			t.Fatalf("later receipt %d was not issued", i)
		}
	}
	if records, warnings := state.ResolveEvidenceRefs(begin.Owner, []string{first.Alias}); len(records) != 0 || len(warnings) != 1 {
		t.Fatalf("old alias remained authorized after eviction: records=%+v warnings=%v", records, warnings)
	}
	state.EndActivityRun(run)
	state.AddMessage(session.RoleUser, "Continue in a new run", session.ContentTypePlain)
	state.BeginActivityRun(state.Messages()[len(state.Messages())-1].ID)

	items := state.Transcript()
	snapshot := state.ActivitySnapshot()
	if len(snapshot.EvidenceRecords) != 0 {
		t.Fatalf("new run inherited old alias authorization: %+v", snapshot.EvidenceRecords)
	}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
	location, found := doc.LocateMember(first.SourceViewID)
	if !found || location.Block.Kind != conversation.BlockTool || location.Block.Text != "source bytes" {
		t.Fatalf("accepted evidence no longer projects after alias eviction/new run: location=%+v found=%v", location, found)
	}

	withoutSource := make([]session.TranscriptItem, 0, len(items)-1)
	for _, item := range items {
		if item.ViewID != first.SourceViewID {
			withoutSource = append(withoutSource, item)
		}
	}
	missingDoc := projectConversation(withoutSource, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
	var unavailable bool
	var visit func([]conversation.Block)
	visit = func(blocks []conversation.Block) {
		for _, block := range blocks {
			if block.Text == "Evidence unavailable" {
				unavailable = true
			}
			visit(block.Children)
		}
	}
	visit(missingDoc.Blocks())
	if !unavailable {
		t.Fatal("projection did not mark a removed evidence source unavailable")
	}
}

func TestNotebookRunnerJourneysKeepNoProseJSONAndMalformedFallbacks(t *testing.T) {
	t.Run("native tool-only response", func(t *testing.T) {
		provider := &agenttest.ScriptedProvider{
			Responses:     []string{"", "done"},
			ToolCalls:     [][]schema.ToolCall{{{ID: "call-1", Name: "journey.noop", Args: json.RawMessage(`{}`)}}, nil},
			FinishReasons: []string{"tool_calls", "stop"},
		}
		state, executions := runNotebookJourney(t, provider, true, false)
		if executions != 1 || len(state.AuditLog()) != 1 || state.AuditLog()[0].Activity.NarrationID == "" {
			t.Fatalf("tool-only result = executions:%d audit:%+v", executions, state.AuditLog())
		}
		var fallback activity.Narration
		for _, narration := range state.ActivitySnapshot().Narrations {
			if narration.Source == activity.SourceRuntimeFallback {
				fallback = narration
				break
			}
		}
		if fallback.ID == "" {
			t.Fatal("tool-only runner response did not record its runtime fallback narration")
		}
		items := state.Transcript()
		var auditViewID string
		for _, item := range items {
			if item.Kind == session.KindAudit && item.Activity.NarrationID == fallback.ID {
				auditViewID = item.ViewID
				break
			}
		}
		if auditViewID == "" {
			t.Fatalf("fallback narration %q owns no audit source", fallback.ID)
		}
		doc := projectConversation(items, state.ActivitySnapshot(), conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
		loc, ok := doc.LocateMember(auditViewID)
		parentID := conversation.BlockID("notebook:fallback:" + fallback.ID)
		if !ok || loc.Block.ID != conversation.BlockID(auditViewID) || len(loc.Ancestors) != 1 || loc.Ancestors[0] != parentID {
			t.Fatalf("fallback audit location = %+v, want child beneath parent %q", loc, parentID)
		}
		parent, ok := doc.Block(parentID)
		if !ok || !parent.PresentationOnly || len(parent.Members) != 0 || len(parent.CopyTargets) != 0 {
			t.Fatalf("fallback parent = %+v, want source-free presentation section", parent)
		}
		entries := notebookTranscriptEntries(items, state.ActivitySnapshot(), conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
		foundParent := false
		for _, entry := range entries {
			if entry.Notebook != nil && entry.Notebook.ID == parentID {
				foundParent = entry.Item != nil && entry.Item.ViewID == auditViewID
				break
			}
		}
		if !foundParent {
			t.Fatal("fallback parent did not render at its first owned child")
		}
		records := map[string]session.TranscriptItem{}
		for _, item := range items {
			if item.ViewID == auditViewID {
				records[auditViewID] = item
			}
		}
		var rendered strings.Builder
		for _, part := range renderNotebookNarration(parent, 80, true, false, false, records) {
			rendered.WriteString(stripANSI(part.Text))
		}
		if !strings.Contains(rendered.String(), "Tool activity") || !strings.Contains(rendered.String(), "fixture output") {
			t.Fatalf("fallback section rendered without its factual label/output: %q", rendered.String())
		}
		assertStateProjectsSourcesAndCopies(t, state)
	})

	t.Run("JSON tool fallback", func(t *testing.T) {
		provider := &agenttest.ScriptedProvider{Responses: []string{
			`{"rationale":"inspect","action":{"type":"tool_call","tool":"journey.noop","args":{}}}`,
			`{"rationale":"done","action":{"type":"answer","content":"done"}}`,
		}}
		state, executions := runNotebookJourney(t, provider, false, false)
		if executions != 1 || len(state.AuditLog()) != 1 {
			t.Fatalf("JSON tool fallback = executions:%d audit:%+v", executions, state.AuditLog())
		}
		assertStateProjectsSourcesAndCopies(t, state)
	})

	t.Run("malformed response repaired", func(t *testing.T) {
		provider := &agenttest.ScriptedProvider{Responses: []string{
			`{"rationale":"broken","action":`,
			`{"rationale":"recovered","action":{"type":"answer","content":"recovered"}}`,
		}}
		state, _ := runNotebookJourney(t, provider, false, false)
		if len(provider.Requests) < 2 {
			t.Fatalf("malformed response did not trigger a repair request: %d requests", len(provider.Requests))
		}
		assertStateProjectsSourcesAndCopies(t, state)
	})

	t.Run("denied tool remains recorded and unexecuted", func(t *testing.T) {
		provider := &agenttest.ScriptedProvider{Responses: []string{
			`{"rationale":"need permission","action":{"type":"tool_call","tool":"shell.run","args":{"command":"echo denied"}}}`,
			`{"rationale":"denied","action":{"type":"answer","content":"I will leave the files unchanged."}}`,
		}}
		state, executions := runNotebookJourney(t, provider, false, true)
		if executions != 0 || len(state.AuditLog()) != 1 || state.AuditLog()[0].Approval != registry.ApprovalDenied {
			t.Fatalf("denied tool = executions:%d audit:%+v", executions, state.AuditLog())
		}
		assertStateProjectsSourcesAndCopies(t, state)
	})
}

func TestNotebookRewindAndRestoredHistoryKeepEverySurvivingSource(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(200, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "first request", session.ContentTypePlain)
	boundary := state.Messages()[0]
	run := state.BeginActivityRun(boundary.ID)
	response := state.BeginActivityResponse()
	response = state.BindActivityNarration(response, "branch narration")
	sourceID := state.AddNarrationMessage(response, "branch narration")
	state.AddMessageFinal(session.RoleAssistant, "branch answer", session.ContentTypeMarkdown)
	branchItems := state.Transcript()
	var narrationViewID string
	for _, item := range branchItems {
		if item.Message != nil && item.Message.ID == sourceID {
			narrationViewID = item.ViewID
		}
	}
	state.Rewind(sourceID)
	state.EndActivityRun(run)
	for _, item := range state.Transcript() {
		if item.ViewID == narrationViewID {
			t.Fatal("rewound narration remained in the active branch transcript")
		}
	}
	state.LogToolCall(registry.AuditEvent{Timestamp: time.Unix(201, 0), ToolName: "shell.run", Approval: registry.ApprovalDenied, ResultSummary: "denied"})
	state.AddMessage(session.RoleAssistant, "alternate answer", session.ContentTypeMarkdown)
	assertStateProjectsSourcesAndCopies(t, state)

	// Rebuild the same surviving path without its in-memory activity ledger,
	// which is what restored phase-1 history looks like. Every source and copy
	// target remains reachable and the notebook marks ownership as unavailable.
	items := state.Transcript()
	for i := range items {
		items[i].Activity = activity.Ref{}
		items[i].Sequence = 0
	}
	legacy := projectConversation(items, session.ActivitySnapshot{}, conversationProjectionOptions{})
	notebook := projectConversation(items, session.ActivitySnapshot{}, conversationProjectionOptions{Notebook: true, ScopeID: state.ScopeID()})
	assertDocumentSourceCoverage(t, items, legacy, notebook)
	var restoredAudit string
	for _, item := range items {
		if item.Kind == session.KindAudit {
			restoredAudit = item.ViewID
			if item.Activity != (activity.Ref{}) || item.Sequence != 0 {
				t.Fatalf("restored audit unexpectedly retained metadata: %+v", item)
			}
			break
		}
	}
	if restoredAudit == "" {
		t.Fatal("restored fallback fixture has no eligible audit record")
	}
	loc, ok := notebook.LocateMember(restoredAudit)
	if !ok || len(loc.Ancestors) != 0 || loc.Block.ID != conversation.BlockID(restoredAudit) {
		t.Fatalf("zero-metadata restored audit location = %+v, want standalone source", loc)
	}
	foundNote := false
	for _, block := range notebook.Blocks() {
		foundNote = foundNote || block.Kind == conversation.BlockOwnershipNote
	}
	if !foundNote {
		t.Fatal("restored history did not disclose unavailable ownership metadata")
	}
}

func TestRuntimeFallbackDoesNotCrossItsUserBoundary(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(250, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "first request", session.ContentTypePlain)
	firstBoundary := state.Messages()[0].ID
	state.BeginActivityRun(firstBoundary)
	fallback := state.BindActivityFallback(state.BeginActivityResponse())
	state.LogToolCall(registry.AuditEvent{Activity: fallback, ToolName: "file.read", ResultSummary: "first result", ResultContent: "first output"})

	state.AddMessage(session.RoleUser, "later request", session.ContentTypePlain)
	laterBoundary := state.Messages()[1].ID
	state.LogToolCall(registry.AuditEvent{Activity: fallback, ToolName: "file.read", ResultSummary: "late result", ResultContent: "late output"})

	snapshot := state.ActivitySnapshot()
	if len(snapshot.Narrations) != 1 || snapshot.Narrations[0].ID != fallback.NarrationID || snapshot.Narrations[0].BoundaryMessageID != firstBoundary {
		t.Fatalf("fallback snapshot changed after later request: %+v", snapshot.Narrations)
	}
	items := state.Transcript()
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
	var firstAudit, lateAudit string
	for _, item := range items {
		if item.Audit == nil {
			continue
		}
		switch item.Audit.ResultSummary {
		case "first result":
			firstAudit = item.ViewID
		case "late result":
			lateAudit = item.ViewID
		}
	}
	first, firstOK := doc.LocateMember(firstAudit)
	parentID := conversation.BlockID("notebook:fallback:" + fallback.NarrationID)
	if !firstOK || len(first.Ancestors) != 1 || first.Ancestors[0] != parentID {
		t.Fatalf("first audit = %+v, want under original fallback parent %q", first, parentID)
	}
	late, lateOK := doc.LocateMember(lateAudit)
	if !lateOK || len(late.Ancestors) != 0 || late.Block.ID != conversation.BlockID(lateAudit) {
		t.Fatalf("late audit crossed from boundary %d to fallback parent: %+v", laterBoundary, late)
	}

	state.Rewind(laterBoundary)
	if got := state.ActivitySnapshot().Narrations; len(got) != 1 || got[0].BoundaryMessageID != firstBoundary {
		t.Fatalf("rewinding later user boundary moved fallback ownership: %+v", got)
	}
	state.Rewind(firstBoundary)
	if got := state.ActivitySnapshot().Narrations; len(got) != 0 {
		t.Fatalf("fallback survived rewind of its original boundary: %+v", got)
	}
}

func TestNotebookProjectionHandlesBoundedManyNarrationsAndChildren(t *testing.T) {
	const count = 96
	at := time.Unix(300, 0)
	items := []session.TranscriptItem{{Kind: session.KindMessage, ViewID: "user", Timestamp: at, Message: &session.Message{ID: 1, Role: session.RoleUser, Content: "inspect"}}}
	snapshot := session.ActivitySnapshot{}
	for i := 0; i < count; i++ {
		n := i + 1
		owner := activity.Ref{RunID: "run", ActorID: "main", ResponseID: fmt.Sprintf("response-%03d", n), NarrationID: fmt.Sprintf("narration-%03d", n)}
		sourceID := int64(n + 1)
		snapshot.Narrations = append(snapshot.Narrations, activity.Narration{ID: owner.NarrationID, RunID: owner.RunID, ActorID: owner.ActorID, SourceMessageID: sourceID, BoundaryMessageID: 1, Sequence: uint64(n)})
		items = append(items,
			session.TranscriptItem{Kind: session.KindMessage, ViewID: fmt.Sprintf("narration-%03d", n), Timestamp: at, Activity: owner, Sequence: uint64(n * 3), Message: &session.Message{ID: sourceID, Role: session.RoleAssistant, Content: fmt.Sprintf("Step %d", n), ContentType: session.ContentTypeNarration}},
			session.TranscriptItem{Kind: session.KindAudit, ViewID: fmt.Sprintf("audit-%03d", n), Timestamp: at, Activity: owner, Sequence: uint64(n*3 + 1), Audit: &registry.AuditEvent{ToolName: "file.read", ResultContent: fmt.Sprintf("output %d", n)}},
			session.TranscriptItem{Kind: session.KindMessage, ViewID: fmt.Sprintf("answer-%03d", n), Timestamp: at, Activity: owner, Sequence: uint64(n*3 + 2), Message: &session.Message{ID: int64(count + n + 1), Role: session.RoleAssistant, Content: fmt.Sprintf("Result %d", n), Final: true}},
		)
	}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	if doc.Len() != count+1 {
		t.Fatalf("top-level blocks = %d, want request plus %d narration sections", doc.Len(), count)
	}
	for _, item := range items {
		if item.ViewID != "" {
			if _, ok := doc.LocateMember(item.ViewID); !ok {
				t.Fatalf("many-record projection lost source %q", item.ViewID)
			}
		}
	}
	before, ok := doc.LocateMember("audit-050")
	if !ok {
		t.Fatal("middle child source is not indexed")
	}
	items[1+49*3+1].Audit.ResultContent = "updated output"
	afterDoc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	after, ok := afterDoc.LocateMember("audit-050")
	if !ok || before.Block.ID != after.Block.ID || before.Block.Revision == after.Block.Revision {
		t.Fatalf("changed child did not revise its stable parent: before=%+v after=%+v", before, after)
	}
	unrelatedBefore, _ := doc.LocateMember("audit-051")
	unrelatedAfter, _ := afterDoc.LocateMember("audit-051")
	if unrelatedBefore.Block.ID != unrelatedAfter.Block.ID || unrelatedBefore.Block.Revision != unrelatedAfter.Block.Revision {
		t.Fatal("changing one child altered an unrelated narration block")
	}
}

func runNotebookJourney(t *testing.T, provider *agenttest.ScriptedProvider, nativeTools, denyCommand bool) (*session.State, int) {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var executions int
	tools := registry.New()
	tool := registry.Tool{Name: "journey.noop", Description: "no-op for transcript fixture", Risk: registry.RiskReadOnly}
	if denyCommand {
		tool = registry.Tool{Name: "shell.run", Description: "approval fixture", Risk: registry.RiskCommand}
	}
	tool.Handler = func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
		executions++
		return registry.ToolResult{Summary: "fixture complete", Content: "fixture output"}, nil
	}
	if err := tools.Register(tool); err != nil {
		t.Fatalf("register fixture tool: %v", err)
	}
	runner := agent.NewRunner(provider, tools, policy.NewEngine(&config.Config{}, nil), state, "journey-model")
	runner.NativeTools = nativeTools
	runner.SetForceClass(string(agent.ClassQuestion))
	if denyCommand {
		done := make(chan error, 1)
		go func() { done <- runner.Run(context.Background(), "run the command") }()
		deadline := time.After(2 * time.Second)
		for state.PendingApproval() == nil {
			select {
			case err := <-done:
				t.Fatalf("runner returned before requesting approval: %v", err)
			case <-deadline:
				t.Fatal("runner did not request command approval")
			default:
				time.Sleep(time.Millisecond)
			}
		}
		state.PendingApproval().Respond(session.UserApprovalDecision{Approved: false})
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runner after denial: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("runner did not finish after approval denial")
		}
	} else if err := runner.Run(context.Background(), "inspect the project"); err != nil {
		t.Fatalf("runner: %v", err)
	}
	return state, executions
}

func assertStateProjectsSourcesAndCopies(t *testing.T, state *session.State) {
	t.Helper()
	items := state.Transcript()
	snapshot := state.ActivitySnapshot()
	legacy := projectConversation(items, snapshot, conversationProjectionOptions{})
	notebook := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true, ScopeID: state.ScopeID()})
	assertDocumentSourceCoverage(t, items, legacy, notebook)
}

func assertDocumentSourceCoverage(t *testing.T, items []session.TranscriptItem, legacy, notebook *conversation.Document) {
	t.Helper()
	for _, item := range items {
		if item.ViewID == "" {
			continue
		}
		left, lok := legacy.LocateMember(item.ViewID)
		right, rok := notebook.LocateMember(item.ViewID)
		if !lok || !rok {
			t.Fatalf("source %q missing after projection: legacy=%v notebook=%v", item.ViewID, lok, rok)
		}
		if !reflect.DeepEqual(left.Block.CopyTargets, right.Block.CopyTargets) {
			t.Fatalf("source %q copy output changed:\nlegacy=%+v\nnotebook=%+v", item.ViewID, left.Block.CopyTargets, right.Block.CopyTargets)
		}
	}
}
