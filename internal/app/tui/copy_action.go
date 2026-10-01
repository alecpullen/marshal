package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/picker"
	"marshal/internal/strutil"
)

// copyState carries the guards a copy result must satisfy before it is allowed
// to change anything on screen.
//
// It exists because a copy is asynchronous: the request is handed to a tea.Cmd
// and the result arrives on a later tick, by which time the user may have
// started a new conversation. Feedback that describes a copy performed in a
// session the user has left is worse than no feedback — it appears to describe
// what just happened in front of them.
type copyState struct {
	// session is the session token the outstanding request belongs to. It is
	// captured from the session identity at request time and compared on
	// arrival; a mismatch drops the result.
	//
	// It is derived rather than stored-and-bumped because there is no single
	// place a session is replaced: /new and /clear swap m.state, and a
	// resume replaces it through app.Run. Deriving the token from m.state
	// means every one of those paths invalidates outstanding results without
	// having to remember to.
	session string
	// requestSeq increments per request, so a superseded result from an
	// earlier request in the SAME session is dropped too. Without it, two
	// quick copies would race and the loser's feedback would overwrite the
	// winner's.
	requestSeq int64
	// staleDropped counts results discarded by the guards. It is what the
	// tests observe, and it is the number a future "why did that say
	// nothing?" question needs.
	staleDropped int
}

// copySessionToken is the identity outstanding copy results are scoped to.
//
// It is the session's own ID when one exists, which is the case that matters:
// a resumed or newly created conversation reports a different ID, so results
// from the previous one are stale. A model built without a persisted session
// (tests, and the in-memory-only path) still needs a token that changes when
// the transcript is replaced, so the transcript's first item identity stands
// in — it is empty for an empty conversation and different once a new session
// has added its own first item.
func (m Model) copySessionToken() string {
	if m.state == nil {
		return ""
	}
	if id := m.state.SessionID(); id != "" {
		return id
	}
	return m.transcriptOrigin()
}

// transcriptOrigin is a weak identity for a transcript that has no session ID:
// the first item's presentation identity, or "" when there is nothing yet.
func (m Model) transcriptOrigin() string {
	if m.state == nil {
		return ""
	}
	items := m.state.Transcript()
	if len(items) == 0 {
		return ""
	}
	return items[0].ViewID
}

// copyResultMsg carries one finished copy attempt back to the model.
type copyResultMsg struct {
	// seq is the request this result belongs to.
	seq int64
	// session is the token captured when the request was made.
	session string
	// result is the adapter's verdict.
	result CopyResult
	// label names what was copied, e.g. "Copy answer", so the feedback says
	// what happened rather than only how it went.
	label string
	// err is a failure that happened before the adapter ran (no block to
	// copy, no target of that kind). It is reported like an adapter failure
	// because from the user's side there is no difference.
	err error
}

// copySelection resolves a copy action to a target on the block the reader is
// on, and returns the command that performs it.
//
// Resolution failures do not return a command at all: they are reported as a
// transient toast immediately, because there is nothing asynchronous about
// "there is no code in this block".
func (m *Model) copySelection(source conversation.CopySource) tea.Cmd {
	block, ok := m.copyBlock()
	if !ok {
		m.showToast(copyResolveFailure(source, nil))
		return nil
	}

	switch source {
	case conversation.SourceCode:
		codes := codeTargets(block)
		switch len(codes) {
		case 0:
			m.showToast(copyResolveFailure(source, nil))
			return nil
		case 1:
			return m.beginCopy(codes[0])
		default:
			// Several code blocks: the user chooses. Copying the first would
			// be a silent guess about which snippet they wanted.
			m.openCopyPicker(codes)
			return nil
		}
	}

	target, ok := blockSourceTarget(block, source)
	if !ok {
		m.showToast(copyResolveFailure(source, nil))
		return nil
	}
	return m.beginCopy(target)
}

// blockSourceTarget resolves the target for a specific source, falling back to
// the block's dominant target for the answer action when the block has one
// but did not label it (a non-message block whose text came from elsewhere).
func blockSourceTarget(block conversation.Block, source conversation.CopySource) (conversation.CopyTarget, bool) {
	if target, ok := blockTarget(block, source); ok {
		return target, true
	}
	if source == conversation.SourceAnswer {
		// Any block with text is an answer in the sense the answer action
		// means: the readable prose the user is looking at. A thinking block
		// or a run event has no labelled answer target but is still text a
		// reader may want out.
		if len(block.CopyTargets) == 1 {
			return block.CopyTargets[0], true
		}
	}
	return conversation.CopyTarget{}, false
}

// copyInspectedPath copies the path selected on the inspector's Changes tab.
//
// It is a separate entry point from the conversation's path copy because it
// has a different source: the conversation action reads the block under the
// reading anchor, and this one reads the inspector's own selection. One action
// with two sources would have to guess.
func (m *Model) copyInspectedPath() tea.Cmd {
	if m.inspector == nil {
		m.showToast(copyResolveFailure(conversation.SourcePath, nil))
		return nil
	}
	path, ok := m.inspector.model.CapturePath()
	if !ok {
		m.showToast("no changed file is selected — /inspect changes lists them")
		return nil
	}
	return m.beginCopy(conversation.CopyTarget{
		Source: conversation.SourcePath,
		Text:   path,
		Label:  "Copy path",
	})
}

// copyInspectedPatch copies the patch as FETCHED for the inspected file.
//
// The label states when the fetch was capped, because a copied patch that is a
// prefix must not be pasted as though it were whole — that failure surfaces in
// another program, long after the feedback that said "Copied".
func (m *Model) copyInspectedPatch() tea.Cmd {
	if m.inspector == nil {
		m.showToast(copyResolveFailure(conversation.SourcePatch, nil))
		return nil
	}
	text, label, _, ok := m.inspector.model.CapturedPatch()
	if !ok {
		m.showToast("no patch is loaded — press Enter on a changed file first")
		return nil
	}
	return m.beginCopy(conversation.CopyTarget{
		Source: conversation.SourcePatch,
		Text:   text,
		Label:  label,
	})
}

// copyInspectedContext copies the Context row the reader has open.
//
// The text is the REDACTED body, and the label names the row and states when it
// was capped. Redaction happens at the source (the inspector builds every
// request body through redact.Secrets), so the clipboard and the panel cannot
// disagree — a masked panel beside an unmasked clipboard is worse than neither.
func (m *Model) copyInspectedContext() tea.Cmd {
	if m.inspector == nil {
		m.showToast("the conversation inspector is not available in this build")
		return nil
	}
	text, label, _, ok := m.inspector.model.CaptureContextCopy()
	if !ok {
		m.showToast("no context entry is open — press Enter on a row in the inspector's Context tab")
		return nil
	}
	return m.beginCopy(conversation.CopyTarget{
		Source: conversation.SourceOutput,
		Text:   text,
		Label:  label,
	})
}

// copyBlock returns the block the copy action applies to.
//
// It is the block at the top of the viewport, which is where the reading
// anchor already points: the reader scrolled there, and until selection exists
// the top row is the only statement of intent the UI has. When the reader is
// following the bottom the anchor is empty by design, so the last block is
// used instead — "follow the conversation" means "the newest thing".
func (m *Model) copyBlock() (conversation.Block, bool) {
	doc := m.conversationDocument()
	if m.viewportFollow {
		blocks := doc.Blocks()
		if len(blocks) == 0 {
			return conversation.Block{}, false
		}
		return blocks[len(blocks)-1], true
	}
	if m.readingAnchor.Block != "" {
		if block, ok := copyBlockByID(doc, m.readingAnchor.Block); ok {
			return block, true
		}
	}
	// The anchored block is gone (or was never captured). Fall back to the
	// block under the viewport top, which is what the anchor was derived
	// from in the first place.
	if id, _ := m.blockAtViewportTop(); id != "" {
		if block, ok := copyBlockByID(doc, id); ok {
			return block, true
		}
	}
	blocks := doc.Blocks()
	if len(blocks) == 0 {
		return conversation.Block{}, false
	}
	return blocks[len(blocks)-1], true
}

func copyBlockByID(doc *conversation.Document, id conversation.BlockID) (conversation.Block, bool) {
	if block, ok := doc.Block(id); ok {
		return block, true
	}
	location, ok := doc.LocateMember(string(id))
	if !ok {
		return conversation.Block{}, false
	}
	return location.Block, true
}

// beginCopy stamps a request and returns the command that performs it offline
// of the render path.
//
// The token and sequence are captured HERE, before the command is built, so a
// result can only ever be matched against the state that requested it.
func (m *Model) beginCopy(target conversation.CopyTarget) tea.Cmd {
	m.copyState.requestSeq++
	m.copyState.session = m.copySessionToken()
	seq := m.copyState.requestSeq
	token := m.copyState.session
	adapter := m.copyAdapter()
	text := target.Text
	label := target.Label
	if label == "" {
		label = copyActionLabel(target.Source)
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), copyAdapterTimeout)
		defer cancel()
		return copyResultMsg{
			seq:     seq,
			session: token,
			result:  adapter.Copy(ctx, text),
			label:   label,
		}
	}
}

// copyAdapterTimeout bounds a local clipboard write from the TUI side. The
// adapter applies its own default; this is the outer bound so a wedged
// clipboard helper cannot hold the command open indefinitely.
const copyAdapterTimeout = 5 * time.Second

// copyAdapter builds the adapter from the model's wired seams.
func (m Model) copyAdapter() CopyAdapter {
	return CopyAdapter{
		Writer: m.copyWriter,
		Remote: m.copyRemote,
	}
}

// handleCopyResult applies a finished copy.
//
// Stale results are dropped silently and counted: showing feedback for a copy
// the user can no longer see the source of is how "Copied" ends up describing
// something that is not on screen.
//
// A terminal request carries its own command, and returning it is load-bearing:
// that command is what emits the OSC 52 sequence. Dropping it would leave the
// UI reporting "Copy request sent to terminal" while nothing was ever sent.
func (m Model) handleCopyResult(msg copyResultMsg) (Model, tea.Cmd) {
	if msg.seq != m.copyState.requestSeq || msg.session != m.copySessionToken() {
		m.copyState.staleDropped++
		return m, nil
	}
	if msg.err != nil {
		return m, m.showToast(copyResolveFailure("", msg.err))
	}
	text := msg.result.Message()
	if msg.label != "" {
		text = msg.label + ": " + text
	}
	toast := m.showToast(text)
	if msg.result.Cmd != nil {
		// Batch so the toast's expiry timer and the OSC 52 emission both run.
		// Returning only the toast would report a request that was never sent.
		return m, tea.Batch(toast, msg.result.Cmd)
	}
	return m, toast
}

// copyResolveFailure explains a copy that could not be attempted. It names the
// export route because that is the one path that always works — it writes a
// file the user can open, which needs no clipboard at all.
func copyResolveFailure(source conversation.CopySource, err error) string {
	what := "nothing to copy here"
	switch source {
	case conversation.SourceCode:
		what = "this block has no code block to copy"
	case conversation.SourceOutput:
		what = "this block has no captured output to copy"
	case conversation.SourcePath:
		what = "this block refers to no file path"
	case conversation.SourcePatch:
		what = "there is no captured patch to copy"
	case conversation.SourceAnswer:
		what = "this block has no text to copy"
	}
	if err != nil {
		return err.Error() + " — /export writes the session to a file instead"
	}
	return what + " — /export writes the session to a file instead"
}

// copyActionLabel is the noun a copy action's feedback uses when the target
// carries no label of its own.
func copyActionLabel(source conversation.CopySource) string {
	switch source {
	case conversation.SourceCode:
		return "Copy code"
	case conversation.SourceOutput:
		return "Copy output"
	case conversation.SourcePath:
		return "Copy path"
	default:
		return "Copy answer"
	}
}

// openCopyPicker asks which code block to copy when an answer holds several.
//
// The picker is hosted in the dock like every other chooser, so the copy
// choice does not need a second modal mechanism. Values are the target's
// index into the block's code targets, which is stable for the lifetime of
// the picker because the block's targets are derived from a document that
// does not change while a modal is up.
func (m *Model) openCopyPicker(codes []conversation.CopyTarget) {
	items := make([]picker.Item, 0, len(codes))
	for i, c := range codes {
		items = append(items, picker.Item{
			Label:  c.Label,
			Detail: copyPreview(c.Text),
			Value:  strconv.Itoa(i),
		})
	}
	m.openPicker(copyPickerCommand, "Copy code", "Enter copies · Esc cancels", items, "")
}

// copyPickerCommand names the code-block chooser in the dock.
const copyPickerCommand = "copy-code"

// copyPickedCode copies the code block the chooser returned.
//
// The value is an index into the block's code targets, re-resolved against
// the current block. An index that no longer fits is a failure rather than a
// clamp: copying a neighbouring block because the list shrank would put bytes
// on the clipboard the user never chose.
func (m *Model) copyPickedCode(value string) tea.Cmd {
	idx, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		m.showToast(copyResolveFailure(conversation.SourceCode, nil))
		return nil
	}
	block, ok := m.copyBlock()
	if !ok {
		m.showToast(copyResolveFailure(conversation.SourceCode, nil))
		return nil
	}
	codes := codeTargets(block)
	if idx < 0 || idx >= len(codes) {
		m.showToast(copyResolveFailure(conversation.SourceCode, nil))
		return nil
	}
	return m.beginCopy(codes[idx])
}

// copyPreview renders a one-line preview of a code block for the chooser, so
// two blocks with the same language are still distinguishable by content.
func copyPreview(text string) string {
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		// A block that starts with a blank line is not empty; show its first
		// non-blank line rather than an empty detail.
		for _, l := range strings.Split(text, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				line = l
				break
			}
		}
	}
	return strutil.Truncate(line, 40, true)
}
