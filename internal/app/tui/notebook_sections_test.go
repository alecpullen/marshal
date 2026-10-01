package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

func TestStructuredNarrationProjectsLatestSectionsAndUniqueEvidence(t *testing.T) {
	owner := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "user", Timestamp: time.Unix(1, 0), Message: &session.Message{ID: 1, Role: session.RoleUser, Content: "do"}},
		{Kind: session.KindMessage, ViewID: "rev1", Sequence: 2, Activity: owner, Message: &session.Message{ID: 2, Role: session.RoleAssistant, Content: "Earlier headline\n\nEarlier body", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindAudit, ViewID: "audit", Sequence: 3, Activity: activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n", CallID: "c"}, Audit: &registry.AuditEvent{ToolName: "file.read", ResultContent: "raw result"}},
		{Kind: session.KindMessage, ViewID: "rev2", Sequence: 4, Activity: owner, Message: &session.Message{ID: 3, Role: session.RoleAssistant, Content: "Current headline\n\nCurrent summary", ContentType: session.ContentTypeNarration}},
	}
	snapshot := session.ActivitySnapshot{
		Narrations: []activity.Narration{{ID: "n", RunID: "run", ActorID: "main", Source: activity.SourceStructuredProgress, SourceMessageID: 3, BoundaryMessageID: 1, Sequence: 2}},
		ProgressRevisions: []activity.ProgressRevision{
			{NarrationID: "n", Revision: 1, Headline: "Old headline", Sections: []activity.ProgressSection{{Kind: activity.SectionWork, Text: "old section"}}, SourceMessageID: 2, Sequence: 2},
			{NarrationID: "n", Revision: 2, Headline: "Current headline", Body: "Current summary", Sections: []activity.ProgressSection{{Kind: activity.SectionChange, Text: "Changed", EvidenceRefs: []string{"e1-1", "e1-1"}}, {Kind: activity.SectionChecking, Text: "Checked"}}, SourceMessageID: 3, Sequence: 4},
		},
		EvidenceRecords: []session.EvidenceRecord{{Alias: "e1-1", Owner: activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n", CallID: "c"}, SourceViewID: "audit", ToolName: "file.read", Content: "raw result"}},
	}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true})
	blocks := doc.Blocks()
	if len(blocks) != 2 || blocks[1].ID != conversation.BlockID("narration:n") || blocks[1].Text != "Current headline" {
		t.Fatalf("structured narration = %+v", blocks)
	}
	if len(blocks[1].CopyTargets) != 1 || blocks[1].CopyTargets[0].Text != "Current headline\n\nCurrent summary" {
		t.Fatalf("narration copy target = %+v", blocks[1].CopyTargets)
	}
	if len(blocks[1].EventOrderAlternatives) != 3 || blocks[1].EventOrderAlternatives[0].ID != "rev1" || blocks[1].EventOrderAlternatives[1].ID != "audit" || blocks[1].EventOrderAlternatives[2].ID != "rev2" {
		t.Fatalf("event-order source revisions = %+v", blocks[1].EventOrderAlternatives)
	}
	workEvent := blocks[1].EventOrderAlternatives[1]
	if workEvent.EventOrderSequence != 3 || len(workEvent.Members) != 1 || workEvent.Members[0] != "audit" || len(workEvent.CopyTargets) != 1 || workEvent.CopyTargets[0].Source != conversation.SourceOutput || workEvent.CopyTargets[0].Text != "raw result" {
		t.Fatalf("event-order work item lost source identity/copy payload: %+v", workEvent)
	}
	records := map[string]session.TranscriptItem{}
	for _, item := range items {
		records[item.ViewID] = item
	}
	parts := renderNotebookNarration(blocks[1], 100, true, true, false, records)
	var renderedOrder []conversation.BlockID
	var renderedWork, outputCopy bool
	for _, part := range parts {
		if part.OrderControl || part.CopySource != "" {
			if part.CopySource == conversation.SourceOutput && part.ID == "audit" {
				outputCopy = true
			}
			continue
		}
		renderedOrder = append(renderedOrder, part.ID)
		if part.ID == "audit" && strings.Contains(part.Text, "raw result") {
			renderedWork = true
		}
	}
	if len(renderedOrder) != 4 || renderedOrder[1] != "rev1" || renderedOrder[2] != "audit" || renderedOrder[3] != "rev2" || !renderedWork || !outputCopy {
		t.Fatalf("rendered event order/source copy = ids %v work %v output copy %v", renderedOrder, renderedWork, outputCopy)
	}
	for _, want := range []struct {
		id, text string
		revision uint64
	}{{"rev1", "Earlier headline\n\nEarlier body", 1}, {"rev2", "Current headline\n\nCurrent summary", 2}} {
		parent, parentFound := doc.BlockForMember(want.id)
		if !parentFound || parent.ID != conversation.BlockID("narration:n") {
			t.Fatalf("revision %q does not map to latest parent: %+v, %v", want.id, parent, parentFound)
		}
		loc, found := doc.LocateMember(want.id)
		if !found || loc.Block.ID != conversation.BlockID(want.id) || loc.Block.Text != want.text || loc.Block.SourceRevision != want.revision || len(loc.Block.CopyTargets) != 1 || loc.Block.CopyTargets[0].Text != want.text {
			t.Fatalf("revision %q exact location/copy = %+v, %v", want.id, loc, found)
		}
	}
	priorSources := conversation.NewDocument(blocks[1].EventOrderAlternatives)
	if matches := conversation.FindInDocument(priorSources, conversation.NewFindQuery("Earlier body"), nil); len(matches) == 0 || matches[0].Block != "rev1" {
		t.Fatalf("prior revision body is not searchable in event-order sources: %+v", matches)
	}
	loc, ok := doc.LocateMember("audit")
	if !ok || len(loc.Ancestors) < 2 {
		t.Fatalf("cited result location = %+v, %v", loc, ok)
	}
	var copies int
	var sections []string
	for _, child := range blocks[1].Children {
		if child.Kind != conversation.BlockSection {
			continue
		}
		sections = append(sections, child.SectionLabel)
		if child.SectionLabel == "change" && (len(child.CopyTargets) != 1 || child.CopyTargets[0].Text != "Changed") {
			t.Fatalf("section copy target = %+v", child.CopyTargets)
		}
		for _, cited := range child.Children {
			if cited.Kind == conversation.BlockTool && cited.Text == "raw result" {
				copies++
			}
		}
	}
	if copies != 1 || len(sections) != 3 || sections[0] != "change" || sections[1] != "checking" || sections[2] != "Summary" {
		t.Fatalf("section/evidence projection copies=%d sections=%v children=%+v", copies, sections, blocks[1].Children)
	}
}

func TestStructuredNarrationIdentitySurvivesRevisionSourceChange(t *testing.T) {
	owner := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "user", Message: &session.Message{ID: 1, Role: session.RoleUser}},
		{Kind: session.KindMessage, ViewID: "first", Activity: owner, Message: &session.Message{ID: 2, Role: session.RoleAssistant, ContentType: session.ContentTypeNarration}},
		{Kind: session.KindMessage, ViewID: "second", Activity: owner, Message: &session.Message{ID: 3, Role: session.RoleAssistant, ContentType: session.ContentTypeNarration}},
	}
	base := session.ActivitySnapshot{Narrations: []activity.Narration{{ID: "n", RunID: "run", ActorID: "main", Source: activity.SourceStructuredProgress, SourceMessageID: 2, BoundaryMessageID: 1}}, ProgressRevisions: []activity.ProgressRevision{{NarrationID: "n", Revision: 1, Headline: "one", SourceMessageID: 2}}}
	first := projectConversation(items[:2], base, conversationProjectionOptions{Notebook: true})
	base.Narrations[0].SourceMessageID = 3
	base.ProgressRevisions = append(base.ProgressRevisions, activity.ProgressRevision{NarrationID: "n", Revision: 2, Headline: "two", SourceMessageID: 3})
	second := projectConversation(items, base, conversationProjectionOptions{Notebook: true})
	a, _ := first.Block(conversation.BlockID("narration:n"))
	b, _ := second.Block(conversation.BlockID("narration:n"))
	_, firstRetained := second.LocateMember("first")
	_, secondRetained := second.LocateMember("second")
	if a.ID != b.ID || a.Revision == b.Revision || !firstRetained || !secondRetained {
		t.Fatalf("identity/revision/source mapping: before=%+v after=%+v", a, b)
	}
}

func TestStructuredEvidenceKeepsCrossNarrationLinkAndUnavailablePlaceholder(t *testing.T) {
	owner := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n2"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "user", Message: &session.Message{ID: 1, Role: session.RoleUser}},
		{Kind: session.KindMessage, ViewID: "old-narration", Sequence: 1, Activity: activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n1"}, Message: &session.Message{ID: 2, Role: session.RoleAssistant, ContentType: session.ContentTypeNarration}},
		{Kind: session.KindAudit, ViewID: "old-audit", Sequence: 2, Activity: activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n1"}, Audit: &registry.AuditEvent{ToolName: "shell"}},
		{Kind: session.KindMessage, ViewID: "new-narration", Sequence: 3, Activity: owner, Message: &session.Message{ID: 3, Role: session.RoleAssistant, ContentType: session.ContentTypeNarration}},
	}
	snapshot := session.ActivitySnapshot{
		Narrations: []activity.Narration{
			{ID: "n1", RunID: "run", ActorID: "main", Source: activity.SourceModelProse, SourceMessageID: 2, BoundaryMessageID: 1, Sequence: 1},
			{ID: "n2", RunID: "run", ActorID: "main", Source: activity.SourceStructuredProgress, SourceMessageID: 3, BoundaryMessageID: 1, Sequence: 3},
		},
		ProgressRevisions: []activity.ProgressRevision{{NarrationID: "n2", Revision: 1, Headline: "current", Sections: []activity.ProgressSection{{Kind: activity.SectionEvidence, Text: "Prior check", EvidenceRefs: []string{"e1-1", "missing"}}}, SourceMessageID: 3}},
		EvidenceRecords:   []session.EvidenceRecord{{Alias: "e1-1", Owner: activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n1"}, SourceViewID: "old-audit", ToolName: "shell"}, {Alias: "missing-hidden", Owner: activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n1"}, SourceViewID: "hidden", ToolName: "shell"}},
	}
	// The second alias is intentionally absent from the revision, so it cannot
	// be surfaced by alias enumeration. The explicit unresolved ref exercises
	// the same unavailable path.
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true})
	parent, ok := doc.Block(conversation.BlockID("narration:n2"))
	if !ok || len(parent.Children) == 0 {
		t.Fatalf("structured parent missing: %+v", parent)
	}
	section := parent.Children[0]
	if len(section.Children) != 2 || section.Children[0].ReferenceTarget != "old-audit" || section.Children[1].Text != "Evidence unavailable" {
		t.Fatalf("cross-narration / unavailable refs = %+v", section.Children)
	}
	location, found := doc.LocateMember("old-audit")
	if !found || len(location.Ancestors) != 1 || location.Ancestors[0] != "old-narration" {
		t.Fatalf("original audit source was not retained at its original owner: %+v", location)
	}
}
