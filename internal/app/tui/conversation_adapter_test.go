package tui

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// A document built from a real transcript must carry the same identities the
// transcript produced, so a click region, an expand override and an anchor
// all name the same thing.
func TestConversationDocumentUsesTranscriptViewIDs(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(500, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "one", StartedAt: at})
	m.state.LogThinking(session.ThinkingEntry{Text: "two", StartedAt: at})

	doc := m.conversationDocument()
	items := m.state.Transcript()
	if len(items) != 2 {
		t.Fatalf("precondition: got %d items, want 2", len(items))
	}

	for _, item := range items {
		block, ok := doc.BlockForMember(item.ViewID)
		if !ok {
			t.Fatalf("no block for transcript item %q", item.ViewID)
		}
		if string(block.ID) != item.ViewID {
			t.Fatalf("block ID %q does not match ViewID %q", block.ID, item.ViewID)
		}
	}
}

// The two same-timestamp thinking entries that broke (Timestamp, Kind)
// identity must be two distinct blocks in the document too. This is the
// end-to-end version of the collision: the transcript and the document must
// agree that these are different things.
func TestConversationDocumentSeparatesIdenticalTimestamps(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(501, 0)
	m.state.LogThinking(session.ThinkingEntry{Text: "first", StartedAt: at})
	m.state.LogThinking(session.ThinkingEntry{Text: "second", StartedAt: at})

	doc := m.conversationDocument()
	items := m.state.Transcript()

	if items[0].ViewID == items[1].ViewID {
		t.Fatalf("transcript gave both items the same identity %q", items[0].ViewID)
	}

	first, ok := doc.BlockForMember(items[0].ViewID)
	if !ok {
		t.Fatalf("no block for %q", items[0].ViewID)
	}
	second, ok := doc.BlockForMember(items[1].ViewID)
	if !ok {
		t.Fatalf("no block for %q", items[1].ViewID)
	}
	if first.ID == second.ID {
		t.Fatalf("both thinking entries resolved to one block %q", first.ID)
	}
}

// A collapsed run of same-tool audit events becomes one group block, and the
// group's identity is stable as the run grows — otherwise a reader's anchor
// would move each time another read was logged.
func TestConversationDocumentGroupsStableAcrossGrowth(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(502, 0)
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second)})

	before := m.conversationDocument()
	var beforeGroup conversation.Block
	found := false
	for _, b := range before.Blocks() {
		if b.Kind == conversation.BlockToolGroup {
			beforeGroup, found = b, true
		}
	}
	if !found {
		t.Fatalf("two same-tool reads did not collapse into a group: %+v", before.Blocks())
	}

	// A third read joins the run.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(2 * time.Second)})
	after := m.conversationDocument()

	got, ok := after.Block(beforeGroup.ID)
	if !ok {
		t.Fatalf("the group lost its identity %q when it grew", beforeGroup.ID)
	}
	if len(got.Members) != 3 {
		t.Fatalf("group members = %v, want 3 after the new read", got.Members)
	}
}

// Selecting a member of a collapsed group must resolve to that member's own
// block, not to the group: this is the "copy one line, get four tool outputs"
// bug, asserted end to end through the real grouping logic.
func TestConversationDocumentGroupMemberSelectionIsNarrow(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(503, 0)
	// A run of mergeable reads.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second)})
	// A failure never joins a run (see mergeableAuditEvent), so it is a block
	// of its own that happens to sit between them in time.
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(2 * time.Second), Error: "boom"})

	doc := m.conversationDocument()
	items := m.state.Transcript()

	var failed string
	for _, item := range items {
		if item.Audit != nil && item.Audit.Error != "" {
			failed = item.ViewID
		}
	}
	if failed == "" {
		t.Fatal("precondition: no failed audit item found")
	}

	block, ok := doc.BlockForMember(failed)
	if !ok {
		t.Fatalf("no block for the failed call %q", failed)
	}
	if block.Kind == conversation.BlockToolGroup {
		t.Fatalf("the failed call resolved to the enclosing group %q instead of its own block", block.ID)
	}
	if len(block.Members) != 1 {
		t.Fatalf("failed call block covers %v, want exactly one member", block.Members)
	}
}

// While drilled into a subagent, the document describes the CHILD transcript,
// so anchors and selections name what is on screen rather than the parent's
// hidden items.
func TestConversationDocumentFollowsDrill(t *testing.T) {
	m := newTestModel(t)
	child := newChildState(t)
	view := m.state.RegisterSubagent("explore", child)
	child.AddMessage(session.RoleUser, "child question", session.ContentTypePlain)

	m.drillIntoSubagent(view)
	doc := m.conversationDocument()

	childItems := child.Transcript()
	if len(childItems) == 0 {
		t.Fatal("precondition: child has no transcript items")
	}
	if _, ok := doc.BlockForMember(childItems[0].ViewID); !ok {
		t.Fatalf("drilled document is missing the child's item %q", childItems[0].ViewID)
	}

	// The parent's own items must not be present while drilled in.
	if _, ok := doc.BlockForMember("msg:1"); ok {
		t.Fatal("drilled document still describes the parent transcript")
	}
}

// Every block the adapter emits must carry an identity, so nothing can be
// rendered that a reader cannot anchor to or select.
func TestConversationDocumentBlocksAllIdentified(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(504, 0)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.LogThinking(session.ThinkingEntry{Text: "hmm", StartedAt: at})
	m.state.LogToolCall(registry.AuditEvent{ToolName: "file.read", Timestamp: at.Add(time.Second)})
	m.state.AddRunEvent(session.RunEvent{Kind: session.RunEventCommit, TaskN: 1, Title: "abc", At: at.Add(2 * time.Second)})
	m.state.AddJobExit(session.JobExit{ID: "job-1", Command: "go test", At: at.Add(3 * time.Second)})
	_ = m.state.RegisterSubagent("explore", newChildState(t))

	for _, b := range m.conversationDocument().Blocks() {
		if b.ID == "" {
			t.Fatalf("adapter emitted an unidentified block: %+v", b)
		}
		if len(b.Members) == 0 {
			t.Fatalf("adapter emitted a memberless block: %+v", b)
		}
	}
}

// The document must be rebuildable at any time without the identities moving:
// a caller rebuilds it on every render.
func TestConversationDocumentStableAcrossRebuilds(t *testing.T) {
	m := newTestModel(t)
	at := time.Unix(505, 0)
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.state.LogThinking(session.ThinkingEntry{Text: "hmm", StartedAt: at})

	first := conversationIDs(m.conversationDocument())
	for i := 0; i < 3; i++ {
		got := conversationIDs(m.conversationDocument())
		if len(got) != len(first) {
			t.Fatalf("rebuild %d produced %d blocks, want %d", i+1, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("rebuild %d changed block %d: %q -> %q", i+1, j, first[j], got[j])
			}
		}
	}
}

func conversationIDs(doc *conversation.Document) []conversation.BlockID {
	out := make([]conversation.BlockID, 0, doc.Len())
	for _, b := range doc.Blocks() {
		out = append(out, b.ID)
	}
	return out
}

// conversationAdapterBlockForLastMessage appends one message to the
// transcript and returns the document block the adapter built for it.
//
// It is the shared scaffolding for the copy-target tests below: they all
// assert on what the adapter did with one message's content, so they all need
// the same add-then-resolve step, and resolving through the document (rather
// than calling withMessageContent directly) is what keeps the tests honest
// about the path the TUI actually takes.
func conversationAdapterBlockForLastMessage(t *testing.T, m Model, role session.Role, content string) conversation.Block {
	t.Helper()
	m.state.AddMessage(role, content, session.ContentTypeMarkdown)
	items := m.state.Transcript()
	if len(items) == 0 {
		t.Fatal("precondition: transcript is empty after AddMessage")
	}
	last := items[len(items)-1]
	block, ok := m.conversationDocument().BlockForMember(last.ViewID)
	if !ok {
		t.Fatalf("no block for the message %q", last.ViewID)
	}
	return block
}

// A user message is what the user typed: offering to copy it back is noise,
// and a fenced block inside it is still the user's own text. This re-asserts
// the unchanged behaviour now that assistant messages carry code targets.
func TestConversationAdapterUserMessageHasNoCopyTargets(t *testing.T) {
	m := newTestModel(t)
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleUser,
		"here is my code:\n\n```go\nfunc main() {}\n```\n")

	if len(block.CopyTargets) != 0 {
		t.Fatalf("user message carries %d copy targets, want 0: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	if block.Source != "" {
		t.Fatalf("user message has source %q, want none", block.Source)
	}
}

// An assistant answer with no fences offers exactly one thing to copy: the
// answer itself.
func TestConversationAdapterAssistantWithoutFencesHasOneTarget(t *testing.T) {
	m := newTestModel(t)
	const content = "All tests pass.\n"
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleAssistant, content)

	if len(block.CopyTargets) != 1 {
		t.Fatalf("got %d copy targets, want 1: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	got := block.CopyTargets[0]
	if got.Source != conversation.SourceAnswer || got.Text != content || got.Label != "Copy answer" {
		t.Fatalf("answer target = %+v, want the whole answer labelled %q", got, "Copy answer")
	}
}

// Two fenced blocks in one answer are two separate things to copy, so the
// block offers three targets in source order: the answer, then each block.
func TestConversationAdapterAssistantWithTwoFencesHasThreeTargets(t *testing.T) {
	m := newTestModel(t)
	const content = "First:\n\n```go\none\n```\n\nThen:\n\n```bash\ntwo\n```\n"
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleAssistant, content)

	if len(block.CopyTargets) != 3 {
		t.Fatalf("got %d copy targets, want 3: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	wantSources := []conversation.CopySource{
		conversation.SourceAnswer,
		conversation.SourceCode,
		conversation.SourceCode,
	}
	for i, want := range wantSources {
		if block.CopyTargets[i].Source != want {
			t.Fatalf("target %d source = %q, want %q", i, block.CopyTargets[i].Source, want)
		}
	}
	if block.CopyTargets[1].Text != "one\n" || block.CopyTargets[2].Text != "two\n" {
		t.Fatalf("code targets = %q, %q; want %q, %q",
			block.CopyTargets[1].Text, block.CopyTargets[2].Text, "one\n", "two\n")
	}
	if block.CopyTargets[1].Label == block.CopyTargets[2].Label {
		t.Fatalf("both code targets are labelled %q; a reader cannot tell them apart", block.CopyTargets[1].Label)
	}
}

// The code target's text is the block's source bytes and nothing else: not
// the whole answer, and not the fence lines. Trailing newline and interior
// indentation included, because that is what the author wrote.
func TestConversationAdapterCodeTargetTextIsExactBlockText(t *testing.T) {
	m := newTestModel(t)
	const content = "Here you go:\n\n```go\nfunc main() {\n    x := 1\n}\n```\n"
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleAssistant, content)

	blocks := conversation.ParseCodeFences(content)
	if len(blocks) != 1 {
		t.Fatalf("precondition: ParseCodeFences found %d blocks, want 1", len(blocks))
	}
	if len(block.CopyTargets) != 2 {
		t.Fatalf("got %d copy targets, want 2: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	code := block.CopyTargets[1]
	const want = "func main() {\n    x := 1\n}\n"
	if code.Text != want {
		t.Fatalf("code target text = %q, want %q", code.Text, want)
	}
	if code.Text != blocks[0].Text {
		t.Fatalf("code target text %q is not the parsed block's text %q", code.Text, blocks[0].Text)
	}
	if code.Text == content {
		t.Fatal("code target copied the whole answer instead of the block")
	}
	if strings.Contains(code.Text, "```") {
		t.Fatalf("code target text %q still carries the fence syntax", code.Text)
	}
}

// Markdown that merely looks code-ish is not a fenced block: an indented
// paragraph and an inline code span must not produce a code target.
func TestConversationAdapterProseOnlyHasNoCodeTargets(t *testing.T) {
	m := newTestModel(t)
	const content = "Run `go test ./...` to check.\n\n    this is an indented code-ish line\n"
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleAssistant, content)

	if len(block.CopyTargets) != 1 {
		t.Fatalf("got %d copy targets, want only the answer: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	if block.CopyTargets[0].Source != conversation.SourceAnswer {
		t.Fatalf("target source = %q, want %q", block.CopyTargets[0].Source, conversation.SourceAnswer)
	}
}

// The answer target is the ORIGINAL Markdown, fences and all: "Copy answer"
// promises the message, not a stripped rendering of it.
func TestConversationAdapterAnswerTargetKeepsFullMarkdown(t *testing.T) {
	m := newTestModel(t)
	const content = "Before\n\n```go\ncode\n```\n\nAfter\n"
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleAssistant, content)

	if len(block.CopyTargets) == 0 {
		t.Fatal("no copy targets")
	}
	answer := block.CopyTargets[0]
	if answer.Source != conversation.SourceAnswer {
		t.Fatalf("first target source = %q, want %q", answer.Source, conversation.SourceAnswer)
	}
	if answer.Text != content {
		t.Fatalf("answer target text = %q, want the full Markdown %q", answer.Text, content)
	}
	if !strings.Contains(answer.Text, "```go") {
		t.Fatalf("answer target lost its fences: %q", answer.Text)
	}
}

// An empty assistant message has nothing to copy, so it offers no targets —
// the existing guard, re-asserted now that code targets are appended too.
func TestConversationAdapterEmptyAssistantContentHasNoTargets(t *testing.T) {
	m := newTestModel(t)
	block := conversationAdapterBlockForLastMessage(t, m, session.RoleAssistant, "")

	if len(block.CopyTargets) != 0 {
		t.Fatalf("empty answer carries %d copy targets, want 0: %+v", len(block.CopyTargets), block.CopyTargets)
	}
	if block.Source != conversation.SourceAnswer {
		t.Fatalf("empty answer source = %q, want %q", block.Source, conversation.SourceAnswer)
	}
}

// One code block needs no number: there is nothing to distinguish it from, so
// the label is the plain scope statement plus the language when known.
func TestCodeTargetLabelSingleBlock(t *testing.T) {
	cases := []struct {
		language string
		want     string
	}{
		{"", "Copy code"},
		{"go", "Copy code (go)"},
	}
	for _, tc := range cases {
		if got := codeTargetLabel(0, 1, tc.language); got != tc.want {
			t.Fatalf("codeTargetLabel(0, 1, %q) = %q, want %q", tc.language, got, tc.want)
		}
	}
}

// Several code blocks must be distinguishable in a menu: numbered 1-based in
// source order, with the language hint so a reader can tell the go block from
// the bash one — and two blocks in the SAME language still differ by number.
func TestCodeTargetLabelMultipleBlocksAreDistinguishable(t *testing.T) {
	cases := []struct {
		index    int
		language string
		want     string
	}{
		{0, "go", "Copy code 1 (go)"},
		{1, "", "Copy code 2"},
		{2, "bash", "Copy code 3 (bash)"},
	}
	seen := make(map[string]bool, len(cases))
	for _, tc := range cases {
		got := codeTargetLabel(tc.index, len(cases), tc.language)
		if got != tc.want {
			t.Fatalf("codeTargetLabel(%d, %d, %q) = %q, want %q", tc.index, len(cases), tc.language, got, tc.want)
		}
		if seen[got] {
			t.Fatalf("label %q is not unique", got)
		}
		seen[got] = true
	}

	if a, b := codeTargetLabel(0, 2, "go"), codeTargetLabel(1, 2, "go"); a == b {
		t.Fatalf("two go blocks both labelled %q", a)
	}
}

// The language hint is author-controlled text from the fence's info string.
// It is surfaced as written (no trimming, so "go title=main.go" stays
// legible) but bounded, so a pathological fence cannot produce an unbounded
// menu label.
func TestCodeTargetLabelKeepsHintLegibleAndBounded(t *testing.T) {
	if got, want := codeTargetLabel(0, 1, "go title=main.go"), "Copy code (go title=main.go)"; got != want {
		t.Fatalf("codeTargetLabel() = %q, want %q", got, want)
	}

	long := strings.Repeat("x", 200)
	got := codeTargetLabel(0, 1, long)
	if strings.Contains(got, long) {
		t.Fatalf("label %q carries the whole 200-character hint", got)
	}
	if len(got) > 64 {
		t.Fatalf("label %q is %d bytes, want a bounded label", got, len(got))
	}
	if !strings.HasPrefix(got, "Copy code (") || !strings.HasSuffix(got, ")") {
		t.Fatalf("label %q lost its shape", got)
	}
}
