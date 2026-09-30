// internal/app/tui/conversation_adapter_hidden_test.go — what the document says
// about text the transcript does not draw
package tui

import (
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// The adapter must tell the document which messages never reach the screen.
// A skill body is model context: the transcript deliberately renders nothing
// for it, so a search that offered to jump to it would move the reader to a
// line that is not there — and would surface instructions the transcript hides
// on purpose.
//
// The check is stated as "the document and the renderer agree", because that is
// the invariant. Asserting only the flag would pass on a renderer that had
// started drawing skill bodies, which is the half of the pair that matters.
func TestConversationAdapterMarksUndrawnMessagesHidden(t *testing.T) {
	cases := []struct {
		name        string
		contentType session.ContentType
		wantDrawn   bool
	}{
		{"skill body", session.ContentTypeSkillBody, false},
		{"subagent report", session.ContentTypeSubagentReport, false},
		{"watch report", session.ContentTypeWatchReport, false},
		// A skill TAG renders a compact one-line trace, so it is on screen and
		// must be searchable. Marking it hidden would silently drop a visible
		// line out of every search.
		{"skill tag", session.ContentTypeSkill, true},
		{"compaction marker", session.ContentTypeCompaction, true},
		{"steering marker", session.ContentTypeSteering, true},
		{"plain message", session.ContentTypePlain, true},
		{"markdown answer", session.ContentTypeMarkdown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			const text = "distinctive-marker"
			m.state.AddMessage(session.RoleSystem, text, tc.contentType)
			items := m.state.Transcript()
			if len(items) == 0 {
				t.Fatal("precondition: the message is not in the transcript at all")
			}
			last := items[len(items)-1]

			block, ok := m.conversationDocument().BlockForMember(last.ViewID)
			if !ok {
				t.Fatal("no block for the message")
			}

			// What the RENDERER does is the ground truth for whether a reader
			// can see the line.
			drawn := renderTranscriptItem(last, false, m.spinnerFrame,
				regionView{}, nil, m.viewport.Width()) != ""
			if drawn != tc.wantDrawn {
				t.Fatalf("fixture is stale: the renderer draws this as %v, the test expects %v",
					drawn, tc.wantDrawn)
			}
			if block.Hidden == tc.wantDrawn {
				t.Fatalf("block.Hidden = %v for a message the renderer draws as %v", block.Hidden, drawn)
			}
		})
	}
}

// auditEventWithNotice returns a tool event whose captured output was capped,
// which is the only way a document reports that a search could not cover all of
// it.
func auditEventWithNotice() registry.AuditEvent {
	return registry.AuditEvent{
		ToolName:      "file.read",
		Timestamp:     time.Unix(702, 0),
		ResultContent: "captured output",
		Notice:        &registry.ToolNotice{Kind: registry.NoticeOversizeFallback},
	}
}

// conversationBlockForLastItem resolves the document block for whatever is
// already at the end of the transcript.
//
// It is the read-only counterpart of conversationAdapterBlockForLastMessage:
// that one APPENDS a message first, which is right for the copy-target tests
// (they are about what the adapter does with content it is handed) and wrong
// for a test that needs the transcript to contain exactly one of something.
func conversationBlockForLastItem(t *testing.T, m Model) conversation.Block {
	t.Helper()
	items := m.state.Transcript()
	if len(items) == 0 {
		t.Fatal("precondition: transcript is empty")
	}
	last := items[len(items)-1]
	block, ok := m.conversationDocument().BlockForMember(last.ViewID)
	if !ok {
		t.Fatalf("no block for the last transcript item %q", last.ViewID)
	}
	return block
}

// A hidden block must not be findable. This is the end-to-end statement of the
// same invariant: a reader searching for a phrase in a skill body must not be
// offered a jump to it.
func TestConversationAdapterHiddenMessagesAreNotFindable(t *testing.T) {
	m := newTestModel(t)
	m.state.AddMessage(session.RoleSystem, "the skill says: searchable-secret", session.ContentTypeSkillBody)
	m.state.AddMessage(session.RoleUser, "ordinary visible prompt", session.ContentTypePlain)

	doc := m.conversationDocument()
	if got := conversation.FindInDocument(doc, conversation.NewFindQuery("searchable-secret"), nil); len(got) != 0 {
		t.Fatalf("a hidden message was findable: %+v", got)
	}
	if got := conversation.FindInDocument(doc, conversation.NewFindQuery("ordinary"), nil); len(got) != 1 {
		t.Fatalf("the visible message was not findable alongside it: %+v", got)
	}
}

// A user's own prompt is drawn in the transcript, so it must be findable — a
// search that could not match the question, sitting directly above the answer
// being searched, would be broken in the most ordinary case there is.
//
// It must still offer NOTHING to copy: "copy the user's prompt back to them" is
// noise, and it is the copy path's job to keep that true. Attaching text must
// not quietly enroll the block in the copy actions.
//
// This was a real defect: before it, a user message carried no text at all, and
// only the fact that no test searched for a prompt hid it.
func TestConversationAdapterUserPromptIsFindableButNotCopyable(t *testing.T) {
	m := newTestModel(t)
	const prompt = "why does the parser drop the trailing newline"
	m.state.AddMessage(session.RoleUser, prompt, session.ContentTypePlain)

	// The block is resolved from the document the model builds, NOT through
	// conversationAdapterBlockForLastMessage: that helper appends its own
	// message, and appending a second copy of the prompt would make the search
	// below find two matches and the assertion meaningless.
	block := conversationBlockForLastItem(t, m)

	if block.Text != prompt {
		t.Fatalf("the prompt's text is %q, want %q", block.Text, prompt)
	}
	if len(block.CopyTargets) != 0 {
		t.Fatalf("the prompt offers %d copy targets, want 0: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	if block.Source != "" {
		t.Fatalf("the prompt has source %q; an empty source is what keeps the copy actions away", block.Source)
	}

	got := conversation.FindInDocument(m.conversationDocument(), conversation.NewFindQuery("trailing newline"), nil)
	if len(got) != 1 {
		t.Fatalf("the reader cannot find their own prompt: %+v", got)
	}
	if block.Text[got[0].Range.Start:got[0].Range.End] != "trailing newline" {
		t.Fatalf("the match names %q, want the prompt's own words",
			block.Text[got[0].Range.Start:got[0].Range.End])
	}
}

// A system notice is drawn too, so the same rule applies: text yes, copy no.
func TestConversationAdapterSystemNoticeIsFindableButNotCopyable(t *testing.T) {
	m := newTestModel(t)
	const notice = "System notice: the sandbox refused the command"
	m.state.AddMessage(session.RoleSystem, notice, session.ContentTypePlain)

	block := conversationBlockForLastItem(t, m)
	if block.Text != notice {
		t.Fatalf("the notice's text is %q, want %q", block.Text, notice)
	}
	if len(block.CopyTargets) != 0 || block.Source != "" {
		t.Fatalf("a system notice became copyable: source=%q targets=%+v", block.Source, block.CopyTargets)
	}
	if got := conversation.FindInDocument(m.conversationDocument(), conversation.NewFindQuery("sandbox refused"), nil); len(got) != 1 {
		t.Fatalf("the reader cannot find a system notice: %+v", got)
	}
}

// A capped tool result must be reported as truncated, because "not found" and
// "not in the part I could read" are different answers and only one of them is
// true. Every signal the tool can use is covered, since a reader whose output
// was cut off by the sandbox is in exactly the position a capped reader is in.
func TestConversationAdapterMarksTruncatedToolOutput(t *testing.T) {
	cases := []struct {
		name string
		ev   registry.AuditEvent
		want bool
	}{
		{"uncapped", registry.AuditEvent{ToolName: "file.read"}, false},
		{"oversize fallback", registry.AuditEvent{
			ToolName: "file.read",
			Notice:   &registry.ToolNotice{Kind: registry.NoticeOversizeFallback},
		}, true},
		{"capped results", registry.AuditEvent{
			ToolName: "grep.search",
			Notice:   &registry.ToolNotice{Kind: registry.NoticeCappedResults},
		}, true},
		{"slice truncation", registry.AuditEvent{
			ToolName: "grep.search",
			Notice:   &registry.ToolNotice{Kind: registry.NoticeSliceTruncated},
		}, true},
		{"sandbox cut the output", registry.AuditEvent{
			ToolName: "shell.run",
			Sandbox:  registry.SandboxMeta{Enabled: true, OutputTruncated: true},
		}, true},
		// A notice that means something else entirely must not be read as a cap.
		{"zero-match coaching", registry.AuditEvent{
			ToolName: "grep.search",
			Notice:   &registry.ToolNotice{Kind: registry.NoticeZeroMatchCoach},
		}, false},
	}
	at := time.Unix(700, 0)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			ev := tc.ev
			ev.Timestamp = at
			ev.ResultContent = "captured output"
			m.state.LogToolCall(ev)

			items := m.state.Transcript()
			if len(items) == 0 {
				t.Fatal("precondition: the audit event is not in the transcript")
			}
			last := items[len(items)-1]
			block, ok := m.conversationDocument().BlockForMember(last.ViewID)
			if !ok {
				t.Fatal("no block for the audit event")
			}
			if block.Truncated != tc.want {
				t.Fatalf("block.Truncated = %v, want %v", block.Truncated, tc.want)
			}
		})
	}
}

// A collapsed run's members carry the flag too, so a match inside a truncated
// read says so rather than implying the whole output was searched.
func TestConversationAdapterMarksTruncatedGroupMembers(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(701, 0)
	m.state.LogToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		Timestamp:     at,
		ResultContent: "whole file",
	})
	m.state.LogToolCall(registry.AuditEvent{
		ToolName:      "file.read",
		Timestamp:     at.Add(time.Second),
		ResultContent: "first part only",
		Notice:        &registry.ToolNotice{Kind: registry.NoticeOversizeFallback},
	})

	doc := m.conversationDocument()
	var group conversation.Block
	for _, b := range doc.Blocks() {
		if b.Kind == conversation.BlockToolGroup {
			group = b
		}
	}
	if group.ID == "" {
		t.Fatal("precondition: the two reads did not collapse into a group")
	}
	if len(group.Children) != 2 {
		t.Fatalf("precondition: the group has %d children, want 2", len(group.Children))
	}
	if group.Children[0].Truncated {
		t.Fatal("the uncapped member is marked truncated")
	}
	if !group.Children[1].Truncated {
		t.Fatal("the capped member is not marked truncated")
	}
	if !conversation.DocumentTruncated(doc) {
		t.Fatal("the document does not report that it searched a capped block")
	}
}
