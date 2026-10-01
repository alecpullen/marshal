package tui

import (
	"reflect"
	"testing"
	"time"

	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

func TestConversationProjectionsKeepSourceIdentityAndCopyPayload(t *testing.T) {
	ref := activity.Ref{RunID: "run-1", ActorID: "main", ResponseID: "response-1", NarrationID: "narration-1"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "msg:user", Timestamp: time.Unix(1, 0), Message: &session.Message{Role: session.RoleUser, Content: "go", ContentType: session.ContentTypePlain}},
		{Kind: session.KindMessage, ViewID: "msg:narration", Timestamp: time.Unix(2, 0), Activity: ref, Message: &session.Message{ID: 2, Role: session.RoleAssistant, Content: "Checking files", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindMessage, ViewID: "msg:answer", Timestamp: time.Unix(4, 0), Activity: ref, Message: &session.Message{ID: 3, Role: session.RoleAssistant, Content: "Done", Final: true}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{{ID: "narration-1", RunID: "run-1", ActorID: "main", SourceMessageID: 2, Text: "Checking files", Sequence: 7}}}
	legacy := projectConversation(items, snapshot, conversationProjectionOptions{})
	notebook := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	for _, id := range []string{"msg:user", "msg:narration", "msg:answer"} {
		lb, lok := legacy.BlockForMember(id)
		nl, nok := notebook.LocateMember(id)
		if !lok || !nok {
			t.Fatalf("source %q absent: legacy=%v notebook=%v", id, lok, nok)
		}
		if lb.ID != nl.Block.ID || lb.CopyTargets == nil && nl.Block.CopyTargets != nil {
			t.Fatalf("source identity/copy shape changed for %q: legacy=%+v notebook=%+v", id, lb, nl.Block)
		}
		if len(lb.CopyTargets) != len(nl.Block.CopyTargets) {
			t.Fatalf("copy targets changed for %q: %+v vs %+v", id, lb.CopyTargets, nl.Block.CopyTargets)
		}
		for i := range lb.CopyTargets {
			if lb.CopyTargets[i] != nl.Block.CopyTargets[i] {
				t.Fatalf("copy target %q changed: %+v vs %+v", id, lb.CopyTargets[i], nl.Block.CopyTargets[i])
			}
		}
	}
	blocks := notebook.Blocks()
	if len(blocks) != 2 || blocks[1].ID != "msg:narration" {
		t.Fatalf("notebook top-level blocks = %+v", blocks)
	}
	loc, ok := notebook.LocateMember("msg:answer")
	if !ok || len(loc.Ancestors) != 1 || loc.Ancestors[0] != "msg:narration" {
		t.Fatalf("answer location = %+v, %v", loc, ok)
	}
}

func TestNotebookDoesNotInferHistoricalOwnership(t *testing.T) {
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "msg:old", Timestamp: time.Unix(1, 0), Message: &session.Message{Role: session.RoleAssistant, Content: "same headline"}},
		{Kind: session.KindMessage, ViewID: "msg:new", Timestamp: time.Unix(2, 0), Activity: activity.Ref{NarrationID: "n1"}, Message: &session.Message{Role: session.RoleAssistant, Content: "same headline", ContentType: session.ContentTypeNarration}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{{ID: "n1", Sequence: 1}}}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	if !doc.Has("msg:old") || !doc.Has("msg:new") {
		t.Fatalf("projection lost source records: %+v", doc.Blocks())
	}
	old, ok := doc.LocateMember("msg:old")
	if !ok || len(old.Ancestors) != 0 {
		t.Fatalf("historical record was claimed by narration: %+v", old)
	}
	var notes int
	for _, b := range doc.Blocks() {
		if b.Kind == conversation.BlockOwnershipNote {
			notes++
			if len(b.Members) != 0 || len(b.CopyTargets) != 0 {
				t.Fatalf("ownership note claims source/copy: %+v", b)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("ownership notes = %d, want one", notes)
	}
}

func TestNotebookUsesOwnershipForEqualTimestampSectionsAndKeepsTheirIDs(t *testing.T) {
	at := time.Unix(10, 0)
	first := activity.Ref{RunID: "run", ActorID: "main", ResponseID: "r1", NarrationID: "n1"}
	second := activity.Ref{RunID: "run", ActorID: "worker", ResponseID: "r2", NarrationID: "n2"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "user", Timestamp: at, Message: &session.Message{Role: session.RoleUser, Content: "do work"}},
		{Kind: session.KindMessage, ViewID: "narration-1", Timestamp: at, Activity: first, Message: &session.Message{ID: 11, Role: session.RoleAssistant, Content: "Checking", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindMessage, ViewID: "narration-2", Timestamp: at, Activity: second, Message: &session.Message{ID: 12, Role: session.RoleAssistant, Content: "Checking", ContentType: session.ContentTypeNarration}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{
		{ID: "n1", RunID: "run", ActorID: "main", SourceMessageID: 11, Sequence: 1},
		{ID: "n2", RunID: "run", ActorID: "worker", SourceMessageID: 12, Sequence: 2},
	}}
	latest := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	blocks := latest.Blocks()
	if len(blocks) != 3 || blocks[0].ID != "user" || blocks[1].ID != "narration-2" || blocks[2].ID != "narration-1" {
		t.Fatalf("latest section order = %+v", blocks)
	}
	reading := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: false})
	blocks = reading.Blocks()
	if len(blocks) != 3 || blocks[1].ID != "narration-1" || blocks[2].ID != "narration-2" {
		t.Fatalf("reading section order = %+v", blocks)
	}
}

func TestNotebookChildUpdateRevisesParentWithoutChangingSourceIDs(t *testing.T) {
	ref := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "narration", Activity: ref, Sequence: 1, Message: &session.Message{ID: 7, Role: session.RoleAssistant, Content: "Plan", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindMessage, ViewID: "answer", Activity: ref, Sequence: 2, Message: &session.Message{ID: 8, Role: session.RoleAssistant, Content: "first", Final: true}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{{ID: "n", RunID: "run", ActorID: "main", SourceMessageID: 7, Sequence: 1}}}
	before := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true})
	items[1].Message.Content = "changed"
	after := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true})
	b1, _ := before.Block(conversation.BlockID("narration"))
	b2, _ := after.Block(conversation.BlockID("narration"))
	if b1.ID != b2.ID || b1.Revision == b2.Revision {
		t.Fatalf("parent identity/revision = (%q,%d) then (%q,%d)", b1.ID, b1.Revision, b2.ID, b2.Revision)
	}
	child, ok := after.LocateMember("answer")
	if !ok || child.Block.ID != "answer" {
		t.Fatalf("updated child identity changed: %+v", child)
	}
}

func TestNotebookKeepsMisattributedAndNonConversationRecordsStandalone(t *testing.T) {
	ref := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "narration", Timestamp: time.Unix(1, 0), Activity: ref, Message: &session.Message{ID: 4, Role: session.RoleAssistant, Content: "Checking", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindAudit, ViewID: "audit-wrong-actor", Timestamp: time.Unix(2, 0), Activity: activity.Ref{RunID: "run", ActorID: "worker", NarrationID: "n"}, Audit: &registry.AuditEvent{ToolName: "file.read", ResultContent: "wrong actor"}},
		{Kind: session.KindAudit, ViewID: "audit-wrong-run", Timestamp: time.Unix(3, 0), Activity: activity.Ref{RunID: "other-run", ActorID: "main", NarrationID: "n"}, Audit: &registry.AuditEvent{ToolName: "file.read", ResultContent: "wrong run"}},
		{Kind: session.KindMessage, ViewID: "system", Timestamp: time.Unix(4, 0), Activity: ref, Message: &session.Message{Role: session.RoleSystem, Content: "system notice"}},
		{Kind: session.KindJobExit, ViewID: "job", Timestamp: time.Unix(5, 0), Activity: ref, JobExit: &session.JobExit{Output: "job complete"}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{{ID: "n", RunID: "run", ActorID: "main", SourceMessageID: 4}}}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true})
	for _, id := range []string{"audit-wrong-actor", "audit-wrong-run", "system", "job"} {
		loc, ok := doc.LocateMember(id)
		if !ok || len(loc.Ancestors) != 0 {
			t.Fatalf("record %q was absorbed into narration: %+v, %v", id, loc, ok)
		}
	}
	section, ok := doc.LocateMember("narration")
	if !ok || len(section.Ancestors) != 1 {
		t.Fatalf("narration source location = %+v, %v", section, ok)
	}
}

func TestNotebookDoesNotMoveLateAnswerAcrossUserBoundary(t *testing.T) {
	ref := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "user-1", Timestamp: time.Unix(1, 0), Message: &session.Message{ID: 1, Role: session.RoleUser, Content: "first turn"}},
		{Kind: session.KindMessage, ViewID: "narration", Timestamp: time.Unix(2, 0), Activity: ref, Sequence: 1, Message: &session.Message{ID: 4, Role: session.RoleAssistant, Content: "Working", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindMessage, ViewID: "user-2", Timestamp: time.Unix(3, 0), Message: &session.Message{Role: session.RoleUser, Content: "second turn"}},
		{Kind: session.KindMessage, ViewID: "late-answer", Timestamp: time.Unix(4, 0), Activity: ref, Sequence: 2, Message: &session.Message{ID: 5, Role: session.RoleAssistant, Content: "Result for turn one", Final: true}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{{ID: "n", RunID: "run", ActorID: "main", SourceMessageID: 4, BoundaryMessageID: 1, Sequence: 1}}}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	blocks := doc.Blocks()
	if len(blocks) != 4 || blocks[0].ID != "user-1" || blocks[1].ID != "narration" || blocks[2].ID != "user-2" || blocks[3].ID != "late-answer" {
		t.Fatalf("cross-turn projection order = %+v", blocks)
	}
	answer, ok := doc.LocateMember("late-answer")
	if !ok || len(answer.Ancestors) != 0 {
		t.Fatalf("late answer crossed the user boundary: %+v", answer)
	}
}

func TestNotebookReordersSectionsAtTheirSlotsAndKeepsFallbackChronology(t *testing.T) {
	first := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n1"}
	second := activity.Ref{RunID: "run", ActorID: "main", NarrationID: "n2"}
	items := []session.TranscriptItem{
		{Kind: session.KindMessage, ViewID: "user", Timestamp: time.Unix(1, 0), Message: &session.Message{ID: 1, Role: session.RoleUser, Content: "work"}},
		{Kind: session.KindJobExit, ViewID: "job-1", Timestamp: time.Unix(2, 0), JobExit: &session.JobExit{Output: "first fallback"}},
		{Kind: session.KindMessage, ViewID: "n1-source", Timestamp: time.Unix(3, 0), Activity: first, Message: &session.Message{ID: 11, Role: session.RoleAssistant, Content: "Earlier section", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindJobExit, ViewID: "job-2", Timestamp: time.Unix(4, 0), JobExit: &session.JobExit{Output: "middle fallback"}},
		{Kind: session.KindMessage, ViewID: "n2-source", Timestamp: time.Unix(5, 0), Activity: second, Message: &session.Message{ID: 12, Role: session.RoleAssistant, Content: "Latest section", ContentType: session.ContentTypeNarration}},
		{Kind: session.KindJobExit, ViewID: "job-3", Timestamp: time.Unix(6, 0), JobExit: &session.JobExit{Output: "last fallback"}},
	}
	snapshot := session.ActivitySnapshot{Narrations: []activity.Narration{
		{ID: "n1", RunID: "run", ActorID: "main", SourceMessageID: 11, BoundaryMessageID: 1, Sequence: 1},
		{ID: "n2", RunID: "run", ActorID: "main", SourceMessageID: 12, BoundaryMessageID: 1, Sequence: 2},
	}}
	doc := projectConversation(items, snapshot, conversationProjectionOptions{Notebook: true, FollowingLatest: true})
	got := make([]string, 0, doc.Len())
	for _, block := range doc.Blocks() {
		got = append(got, string(block.ID))
	}
	want := []string{"user", "job-1", "n2-source", "job-2", "n1-source", "job-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback placement = %v, want %v", got, want)
	}
}
