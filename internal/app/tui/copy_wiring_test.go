package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// copyTestWriter is a stub local clipboard backend that records what it was
// asked to write and can be told to fail or to block.
type copyTestWriter struct {
	written []string
	err     error
	block   bool
	noAvail bool
}

func (w *copyTestWriter) Write(ctx context.Context, text string) error {
	if w.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.written = append(w.written, text)
	return w.err
}

func (w *copyTestWriter) Available() bool { return !w.noAvail }

// copyTestModel builds a model whose copy path is wired to a stub writer, so
// the action can be exercised without touching a real clipboard.
func copyTestModel(t *testing.T, w *copyTestWriter) Model {
	t.Helper()
	m := newTestModel(t)
	m.copyWriter = w
	m.copyRemote = func() bool { return false }
	return m
}

// assistantWithCode adds a final assistant answer containing one fenced block
// and returns the model.
func assistantWithCode(t *testing.T, m Model, body string) Model {
	t.Helper()
	m.state.AddMessageFinal(session.RoleAssistant, body, session.ContentTypeMarkdown)
	m.refreshViewport()
	return m
}

// TestCopyTargetForBlockPrefersAnswerAndListsCode pins the resolution rule for
// a block's copy affordance: the default target is the block's DOMINANT text
// (the answer), and the alternative targets are still listed so a chooser can
// offer them. Ordering is load-bearing — the first target is what `y` copies.
func TestCopyTargetForBlockPrefersAnswerAndListsCode(t *testing.T) {
	m := copyTestModel(t, &copyTestWriter{})
	m = assistantWithCode(t, m, "Here you go:\n\n```go\nx := 1\n```\n")

	doc := m.conversationDocument()
	blocks := doc.Blocks()
	if len(blocks) == 0 {
		t.Fatal("document has no blocks")
	}
	block := blocks[len(blocks)-1]
	if len(block.CopyTargets) != 2 {
		t.Fatalf("block has %d copy targets, want 2 (answer + code): %+v", len(block.CopyTargets), block.CopyTargets)
	}

	def, ok := defaultCopyTarget(block)
	if !ok {
		t.Fatal("defaultCopyTarget reported no target for a block with two")
	}
	if def.Source != conversation.SourceAnswer {
		t.Fatalf("default target source = %q, want %q", def.Source, conversation.SourceAnswer)
	}
	if !strings.Contains(def.Text, "```go") {
		t.Fatalf("default target text = %q, want the original Markdown including the fence", def.Text)
	}
	if block.CopyTargets[1].Source != conversation.SourceCode {
		t.Fatalf("second target source = %q, want %q", block.CopyTargets[1].Source, conversation.SourceCode)
	}
	if block.CopyTargets[1].Text != "x := 1\n" {
		t.Fatalf("code target text = %q, want the exact fence body", block.CopyTargets[1].Text)
	}
}

// TestCopyCodeChoosesAmongMultipleBlocks pins that the user picks which block
// rather than the first one being copied silently. With several code targets
// present, no single target is the default.
func TestCopyCodeChoosesAmongMultipleBlocks(t *testing.T) {
	m := copyTestModel(t, &copyTestWriter{})
	m = assistantWithCode(t, m, "First:\n\n```go\none\n```\n\nThen:\n\n```bash\ntwo\n```\n")
	m.refreshViewport()

	doc := m.conversationDocument()
	block := doc.Blocks()[len(doc.Blocks())-1]
	codes := codeTargets(block)
	if len(codes) != 2 {
		t.Fatalf("code targets = %d, want 2", len(codes))
	}
	// The chooser's items must be distinguishable, or picking is a coin flip.
	if codes[0].Label == codes[1].Label {
		t.Fatalf("both code labels are %q; a chooser cannot tell them apart", codes[0].Label)
	}
	if codes[0].Text == codes[1].Text {
		t.Fatalf("both code texts are %q", codes[0].Text)
	}
	// The code chooser must not offer the answer: it is reachable through the
	// answer action, and mixing it in makes "which of these is code?" unclear.
	for _, c := range codes {
		if c.Source != conversation.SourceCode {
			t.Fatalf("codeTargets returned a %q target", c.Source)
		}
	}
}

// TestCopyOutputAndPathAreSeparateActions pins that output and path are
// distinct offers, because they are distinct promises about bytes.
func TestCopyOutputAndPathAreSeparateActions(t *testing.T) {
	m := copyTestModel(t, &copyTestWriter{})
	m.state.AddMessage(session.RoleUser, "read the file", session.ContentTypePlain)
	ev := registry.AuditEvent{
		Timestamp:     time.Unix(200, 0),
		ToolName:      "file.read",
		ResultContent: "file contents here",
		FilesChanged:  []string{"/tmp/a.go", "/tmp/b.go"},
	}
	m.state.LogToolCall(ev)
	m.refreshViewport()

	doc := m.conversationDocument()
	// The transcript is ordered by timestamp, so the audit event is located
	// by kind rather than by position.
	var member string
	for _, item := range m.state.Transcript() {
		if item.Kind == session.KindAudit {
			member = item.ViewID
			break
		}
	}
	if member == "" {
		t.Fatal("no audit item in the transcript")
	}
	target, ok := doc.BlockForMember(member)
	if !ok {
		t.Fatal("no block for the audit item")
	}
	var sawOutput, sawPath bool
	for _, c := range target.CopyTargets {
		switch c.Source {
		case conversation.SourceOutput:
			sawOutput = true
			if c.Text != "file contents here" {
				t.Fatalf("output target text = %q", c.Text)
			}
		case conversation.SourcePath:
			sawPath = true
			if c.Text != "/tmp/a.go\n/tmp/b.go" {
				t.Fatalf("path target text = %q, want newline-joined paths", c.Text)
			}
		}
	}
	if !sawOutput || !sawPath {
		t.Fatalf("output=%v path=%v, want both present in %+v", sawOutput, sawPath, target.CopyTargets)
	}
}

// TestCopyActionUnavailableWithNothingToCopy pins the disabled reason: an
// action that silently does nothing is worse than one that explains itself.
func TestCopyActionUnavailableWithNothingToCopy(t *testing.T) {
	m := copyTestModel(t, &copyTestWriter{})
	m.refreshViewport()

	ctx := m.actionSnapshot()
	for _, id := range []ActionID{ActionCopyAnswer, ActionCopyCode, ActionCopyOutput, ActionCopyPath} {
		disabled, reason := availability(ctx, id)
		if !disabled {
			t.Errorf("%s is available with an empty conversation, want disabled", id)
			continue
		}
		if reason == "" {
			t.Errorf("%s is disabled with no reason given", id)
		}
	}
}

// TestCopyActionUnavailableForUserOnlyConversation pins that a user message is
// not copyable: copying the user's own words back to them is noise.
func TestCopyActionUnavailableForUserOnlyConversation(t *testing.T) {
	m := copyTestModel(t, &copyTestWriter{})
	m.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	m.refreshViewport()

	ctx := m.actionSnapshot()
	if disabled, _ := availability(ctx, ActionCopyAnswer); !disabled {
		t.Fatal("CopyAnswer is available with only a user message, want disabled")
	}
}

// TestCopyAnswerWritesExactMarkdownToLocalClipboard is the end-to-end path:
// invoke the action, and assert the exact bytes reached the writer.
func TestCopyAnswerWritesExactMarkdownToLocalClipboard(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	const body = "Fixed it.\n\nDetails 🚀 here.\n"
	m = assistantWithCode(t, m, body)

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)

	if cmd == nil {
		t.Fatal("runAction returned no command; the copy must run off the render path")
	}
	msg := cmd()
	applied, _ := got.Update(msg)
	got = applied.(Model)

	if len(w.written) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.written))
	}
	if w.written[0] != body {
		t.Fatalf("wrote %q, want %q", w.written[0], body)
	}
	if !strings.Contains(got.toastText(), "Copied") {
		t.Fatalf("toast = %q, want a confirmed local copy", got.toastText())
	}
}

// TestCopyDoesNotChangeMouseCaptureOrDraft is the task's core promise:
// copying never costs the user their selection or their draft.
func TestCopyDoesNotChangeMouseCaptureOrDraft(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")
	m.mouseOverride = MouseRelease
	m.input.SetValue("a draft in progress")
	before := m.mouseOverride

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if got.mouseOverride != before {
		t.Fatalf("mouseOverride = %v, want it unchanged at %v", got.mouseOverride, before)
	}
	if got.input.Value() != "a draft in progress" {
		t.Fatalf("draft = %q, want it preserved", got.input.Value())
	}
	if cmd != nil {
		applied, _ := got.Update(cmd())
		got = applied.(Model)
	}
	if got.mouseOverride != before {
		t.Fatalf("mouseOverride changed after the copy landed: %v, want %v", got.mouseOverride, before)
	}
	if got.input.Value() != "a draft in progress" {
		t.Fatalf("draft = %q after the copy landed, want it preserved", got.input.Value())
	}
}

// TestCopyTerminalRequestDoesNotClaimSuccess pins the truthfulness rule: OSC
// 52 has no acknowledgement, so the UI must say it asked the terminal, not
// that the copy succeeded.
func TestCopyTerminalRequestDoesNotClaimSuccess(t *testing.T) {
	w := &copyTestWriter{noAvail: true}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if cmd == nil {
		t.Fatal("no command returned for a terminal copy request")
	}
	applied, _ := got.Update(cmd())
	got = applied.(Model)

	if len(w.written) != 0 {
		t.Fatalf("writer was called %d times with no available backend", len(w.written))
	}
	toast := got.toastText()
	if toast == "" {
		t.Fatal("no feedback for a terminal copy request")
	}
	if !strings.Contains(strings.ToLower(toast), "request") {
		t.Fatalf("toast = %q, want it to say a request was sent", toast)
	}
	if strings.Contains(toast, "Copied") {
		t.Fatalf("toast = %q claims a confirmed copy, but OSC 52 has no acknowledgement", toast)
	}
}

// TestTerminalCopyRequestEmitsItsCommand pins the step that makes a terminal
// copy actually happen: the OSC 52 command carried by the result must be
// RETURNED from the update path. Reporting "request sent" while dropping the
// command would be the worst outcome of all — a confident lie.
func TestTerminalCopyRequestEmitsItsCommand(t *testing.T) {
	w := &copyTestWriter{noAvail: true}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if cmd == nil {
		t.Fatal("no command returned for a terminal request")
	}

	applied, emitted := got.Update(cmd())
	after := applied.(Model)
	if emitted == nil {
		t.Fatal("handling the result returned no command; the OSC 52 emission was dropped")
	}
	if after.toastText() == "" {
		t.Fatal("the terminal request produced no feedback")
	}

	// "A command exists" is NOT enough to prove anything: showToast alone
	// returns a timer command, so a dropped emission still yields one. The
	// emission and the timer must be BATCHED, and one batch member must
	// produce exactly what tea.SetClipboard produces for this text.
	//
	// Driving the command is bounded rather than direct: under the bug the
	// only command is tea.Tick, which blocks for the toast duration.
	done := make(chan tea.Msg, 1)
	go func() { done <- emitted() }()

	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(time.Second):
		t.Fatal("the returned command did not resolve promptly, so it carries no OSC 52 emission — only the toast timer")
	}

	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("handling the result produced %T, want a tea.BatchMsg carrying both the emission and the toast timer", msg)
	}
	want := tea.SetClipboard("answer text")()
	emits := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if reflect.DeepEqual(c(), want) {
			emits = true
		}
	}
	if !emits {
		t.Fatalf("no batch member emits the copied text; the OSC 52 request would never be sent")
	}
	if len(w.written) != 0 {
		t.Fatalf("a terminal request wrote to the local clipboard %d times", len(w.written))
	}
}

// TestCopyFailureIsActionableAndPreservesSelection pins the failure contract:
// report the exact failure, name the export route, and change nothing.
func TestCopyFailureIsActionableAndPreservesSelection(t *testing.T) {
	w := &copyTestWriter{err: errors.New("pbcopy exploded")}
	m := copyTestModel(t, w)
	m.copyRemote = func() bool { return false }
	// Force the local path to be the only option: a huge payload cannot fall
	// back to OSC 52, so the failure surfaces instead of silently degrading.
	big := strings.Repeat("x", MaxOSC52Bytes+1)
	m = assistantWithCode(t, m, big)
	m.mouseOverride = MouseRelease

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if cmd != nil {
		applied, _ := got.Update(cmd())
		got = applied.(Model)
	}
	toast := got.toastText()
	if toast == "" {
		t.Fatal("no feedback for a failed copy")
	}
	if !strings.Contains(toast, "/export") {
		t.Fatalf("toast = %q, want it to name the actionable /export route", toast)
	}
	if got.mouseOverride != MouseRelease {
		t.Fatalf("mouseOverride = %v, want the released selection preserved", got.mouseOverride)
	}
}

// TestCopyKeyLeavesTheDraftIntactWithConversationFocus is the task's headline
// promise: copying while a draft exists costs the user neither the draft nor
// the release they took to select text.
func TestCopyKeyLeavesTheDraftIntactWithConversationFocus(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")
	m.setFocus(FocusConversation)
	m.input.SetValue("draft being written")
	m.mouseOverride = MouseRelease

	mm, cmd, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'y'})
	got := mm.(Model)
	if !handled {
		t.Fatal("`y` with the conversation focused was not handled")
	}
	if got.input.Value() != "draft being written" {
		t.Fatalf("draft after the keypress = %q, want it untouched", got.input.Value())
	}
	if got.mouseOverride != MouseRelease {
		t.Fatalf("mouseOverride = %v, want the selection preserved", got.mouseOverride)
	}
	if cmd == nil {
		t.Fatal("`y` produced no command, so nothing would reach the clipboard")
	}
	applied, _ := got.Update(cmd())
	after := applied.(Model)

	// The copy must not have cost the user their draft or their release.
	if after.input.Value() != "draft being written" {
		t.Fatalf("draft after the copy landed = %q, want it untouched", after.input.Value())
	}
	if after.mouseOverride != MouseRelease {
		t.Fatalf("mouseOverride after the copy landed = %v, want the selection preserved", after.mouseOverride)
	}
	if len(w.written) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.written))
	}
	if w.written[0] != "answer text" {
		t.Fatalf("wrote %q, want the answer text", w.written[0])
	}
}

// TestCopyCodeBlockLeavesDraftAndCaptureIntact is Task 4's stated acceptance
// case in unit form: copying a CODE BLOCK while a draft is present and the
// mouse is released must leave both exactly as they were.
func TestCopyCodeBlockLeavesDraftAndCaptureIntact(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "Here:\n\n```go\nx := 1\n```\n")
	m.input.SetValue("half-written prompt")
	m.mouseOverride = MouseRelease
	before := m.mouseOverride

	mm, cmd := m.runAction(ActionCopyCode)
	got := mm.(Model)
	if cmd != nil {
		applied, _ := got.Update(cmd())
		got = applied.(Model)
	}

	if len(w.written) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.written))
	}
	if w.written[0] != "x := 1\n" {
		t.Fatalf("wrote %q, want exactly the fence body with its trailing newline", w.written[0])
	}
	if got.input.Value() != "half-written prompt" {
		t.Fatalf("draft = %q, want it preserved", got.input.Value())
	}
	if got.mouseOverride != before {
		t.Fatalf("mouseOverride = %v, want the released capture preserved", got.mouseOverride)
	}
}

// TestCopyCodeWithSeveralBlocksAsksWhichOne pins the other code-specific rule:
// with two code blocks the action opens a chooser and copies NOTHING until the
// user answers. Copying the first block would be a silent guess.
func TestCopyCodeWithSeveralBlocksAsksWhichOne(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "First:\n\n```go\none\n```\n\nThen:\n\n```bash\ntwo\n```\n")

	mm, cmd := m.runAction(ActionCopyCode)
	got := mm.(Model)
	if cmd != nil {
		applied, _ := got.Update(cmd())
		got = applied.(Model)
	}

	if len(w.written) != 0 {
		t.Fatalf("wrote %q without asking which block was meant", w.written)
	}
	if !got.dock.IsOpen() {
		t.Fatal("no chooser opened for an answer with two code blocks")
	}
	if got.pickerCommand != copyPickerCommand {
		t.Fatalf("pickerCommand = %q, want %q", got.pickerCommand, copyPickerCommand)
	}
}

// TestCopyKeyIsTextInTheComposer pins the other half of the routing rule: while
// the composer owns typing, `y` is a letter. Swallowing it would make the
// transcript's copy key a hole in the keyboard.
func TestCopyKeyIsTextInTheComposer(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")
	m.setFocus(FocusComposer)

	before := m.copyState.requestSeq
	mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'y'})
	got := mm.(Model)
	if handled {
		t.Fatal("`y` was consumed while the composer owns typing; it must reach the textarea")
	}
	if got.copyState.requestSeq != before {
		t.Fatal("`y` in the composer started a copy")
	}
	if len(w.written) != 0 {
		t.Fatalf("writer called %d times from the composer", len(w.written))
	}
}

// TestStaleCopyResultIsIgnored pins the session guard: a copy fired from a
// session that has since been replaced must not write feedback into the new
// one.
func TestStaleCopyResultIsIgnored(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if cmd == nil {
		t.Fatal("no command returned")
	}
	msg := cmd()

	// A new conversation starts before the result lands. This mirrors
	// newSessionEffect exactly: m.state is REPLACED with a fresh State, and
	// the request sequence is bumped. Appending a message to the same state
	// would not be a new session and must not invalidate the copy — the
	// transcript would still hold what was copied.
	before := got.copySessionToken()
	newState := session.New(got.state.Config, t.TempDir(), time.Unix(300, 0), session.Persistence{})
	got.state = newState
	got.copyState.requestSeq++
	if got.copySessionToken() == before {
		t.Fatal("the session token did not change when the session was replaced, so a stale result could not be detected")
	}
	got.clearToast()

	applied, _ := got.Update(msg)
	after := applied.(Model)
	if after.toastText() != "" {
		t.Fatalf("toast = %q, want a stale copy result to be dropped", after.toastText())
	}
	if after.copyState.staleDropped != 1 {
		t.Fatalf("staleDropped = %d, want 1", after.copyState.staleDropped)
	}
}

// TestSupersededCopyResultWithinOneSessionIsIgnored pins the sequence guard:
// two copies in the same session race, and the loser must not overwrite the
// winner's feedback.
func TestSupersededCopyResultWithinOneSessionIsIgnored(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "answer text")

	mm, first := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if first == nil {
		t.Fatal("no command for the first copy")
	}
	firstMsg := first()

	mm, second := got.runAction(ActionCopyAnswer)
	got = mm.(Model)
	if second == nil {
		t.Fatal("no command for the second copy")
	}
	got.clearToast()

	// The FIRST result lands last. It belongs to a superseded request, so it
	// must not report anything.
	applied, _ := got.Update(firstMsg)
	after := applied.(Model)
	if after.toastText() != "" {
		t.Fatalf("toast = %q, want the superseded first result to be dropped", after.toastText())
	}
	if after.copyState.staleDropped != 1 {
		t.Fatalf("staleDropped = %d, want 1", after.copyState.staleDropped)
	}
	// The current request's own result still lands.
	applied2, _ := after.Update(second())
	final := applied2.(Model)
	if final.toastText() == "" {
		t.Fatal("the current copy's result was dropped too")
	}
}

// TestCopyKeyCopiesTheBlockTheReaderIsOn pins the resolution rule for `y`: the
// block the reader is anchored to, not simply the newest one. Without this, a
// reader who scrolls back to an earlier answer and presses `y` silently gets
// whatever arrived most recently — the exact failure that makes a copy key
// untrustworthy.
func TestCopyKeyCopiesTheBlockTheReaderIsOn(t *testing.T) {
	w := &copyTestWriter{}
	m := copyTestModel(t, w)
	m = assistantWithCode(t, m, "an earlier answer")
	// A second answer arrives, so "the newest block" and "the anchored block"
	// are now different blocks.
	m = assistantWithCode(t, m, "a newer answer")

	// Anchor explicitly to the earlier answer: this is the reader having
	// scrolled up to it.
	doc := m.conversationDocument()
	blocks := doc.Blocks()
	if len(blocks) < 2 {
		t.Fatalf("document has %d blocks, want at least 2 answers", len(blocks))
	}
	earlier := blocks[0]
	for _, b := range blocks {
		for _, c := range b.CopyTargets {
			if strings.Contains(c.Text, "an earlier answer") {
				earlier = b
			}
		}
	}
	m.viewportFollow = false
	m.readingAnchor = conversation.Anchor{Block: earlier.ID}
	if got, ok := m.copyBlock(); !ok || got.ID != earlier.ID {
		t.Fatalf("copyBlock resolved to %q (ok=%v), want the anchored block %q", got.ID, ok, earlier.ID)
	}

	mm, cmd := m.runAction(ActionCopyAnswer)
	got := mm.(Model)
	if cmd == nil {
		t.Fatal("no command returned")
	}
	applied, _ := got.Update(cmd())
	after := applied.(Model)

	if len(w.written) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.written))
	}
	if !strings.Contains(w.written[0], "an earlier answer") {
		t.Fatalf("wrote %q, want the ANCHORED answer, not the newest one", w.written[0])
	}
	if strings.Contains(w.written[0], "a newer answer") {
		t.Fatal("copied the newest answer instead of the one the reader was on")
	}
	_ = after
}
