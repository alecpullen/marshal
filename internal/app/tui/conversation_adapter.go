package tui

import (
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/tools/registry"
)

// conversationDocument builds the semantic view of the conversation currently
// on screen.
//
// It is deliberately a plain method with no side effects and no caching of
// its own: it is rebuilt from the live transcript, and every identity it
// produces comes from the transcript item's ViewID, so a click region, an
// expand override, a reading anchor and a copy target all name the same
// object.
//
// The block vocabulary is the same grouping the renderer already uses (see
// groupTranscript): a run of consecutive same-tool audit events is ONE group
// block whose members are the events, and anything that renders on its own is
// a block of one. Keeping the two in step is what makes a collapsed group's
// member list match what the user can actually click.
func (m Model) conversationDocument() *conversation.Document {
	items := m.conversationItems()
	entries := groupTranscript(items)
	blocks := make([]conversation.Block, 0, len(entries))
	for _, entry := range entries {
		if block, ok := conversationBlock(entry); ok {
			blocks = append(blocks, block)
		}
	}
	return conversation.NewDocument(blocks)
}

// conversationItems returns the transcript the user is actually looking at,
// plus whether that is a drilled-in child.
//
// It mirrors refreshViewport's selection, including the agent.run filter: the
// completed agent.run audit event duplicates the subagent card, and the card
// replaces it in the parent view. A document that included the filtered event
// would offer a copy target for an item the user cannot see.
func (m Model) conversationItems() []session.TranscriptItem {
	transcriptState := m.state
	drilled, drilling := m.drilledInto()
	if drilling {
		if drilled.Child == nil {
			drilling = false
		} else {
			transcriptState = drilled.Child
		}
	}
	if transcriptState == nil {
		return nil
	}

	items := transcriptState.Transcript()
	if drilling {
		return items
	}

	filtered := make([]session.TranscriptItem, 0, len(items))
	for _, item := range items {
		if item.Kind == session.KindAudit && item.Audit != nil && item.Audit.ToolName == "agent.run" {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// conversationBlock converts one render entry into a semantic block.
//
// It reports false for an entry that has no identity to carry — a malformed
// item with no ViewID — rather than emitting a block a reader could not
// anchor to or select.
func conversationBlock(entry transcriptEntry) (conversation.Block, bool) {
	if entry.Group != nil {
		return toolGroupBlock(entry.Group, entry.GroupIDs), true
	}
	if entry.Item == nil {
		return conversation.Block{}, false
	}
	item := entry.Item
	if item.ViewID == "" {
		return conversation.Block{}, false
	}

	block := conversation.Block{
		Kind:    blockKindFor(item.Kind),
		Members: []string{item.ViewID},
	}

	switch item.Kind {
	case session.KindMessage:
		block = withMessageContent(block, item.Message)
	case session.KindAudit:
		block = withAuditContent(block, item.Audit)
	case session.KindThinking:
		if item.Thinking != nil {
			block.Text = item.Thinking.Text
		}
	case session.KindSubagent:
		if item.Subagent != nil {
			block.Text = item.Subagent.Summary
		}
	case session.KindRunEvent:
		if item.RunEvent != nil {
			block.Text = item.RunEvent.Body
		}
	case session.KindJobExit:
		if item.JobExit != nil {
			block.Text = item.JobExit.Output
		}
	}
	return block, true
}

// toolGroupBlock describes a collapsed run of same-tool calls. Every member
// is listed, and each is also exposed as a child so a member can be addressed
// individually — the document's narrowest-block rule then makes selecting a
// member resolve to that member rather than to the whole run.
func toolGroupBlock(events []registry.AuditEvent, memberIDs []string) conversation.Block {
	// members falls back to nothing rather than to a synthesized identity:
	// an event without its transcript identity cannot be addressed, and the
	// document drops memberless blocks rather than inventing one.
	members := memberIDs
	if len(members) != len(events) {
		members = nil
	}

	children := make([]conversation.Block, 0, len(events))
	for i := range events {
		if i >= len(members) {
			break
		}
		child := conversation.Block{
			Kind:    conversation.BlockTool,
			Members: []string{members[i]},
		}
		child = withAuditContent(child, &events[i])
		children = append(children, child)
	}
	return conversation.Block{
		Kind:     conversation.BlockToolGroup,
		Members:  members,
		Children: children,
	}
}

// withMessageContent attaches a message's source text and its copy targets.
//
// A user message is not copyable: it is what the user typed, so an action
// offering to copy it back to them is noise. An assistant answer is, and it
// carries its ORIGINAL Markdown — the copy target is labelled "Copy answer"
// so the scope is stated rather than inferred from what the text looks like.
//
// An answer that contains fenced code offers each block as its own target as
// well. The answer target is the whole message, fences included, because that
// is what "Copy answer" promises; a reader who wants the code alone should
// not have to strip the prose and the fence lines out of the clipboard
// afterwards. The block text comes from ParseCodeFences, which slices the
// source bytes, so what lands on the clipboard is what the author wrote.
func withMessageContent(block conversation.Block, msg *session.Message) conversation.Block {
	if msg == nil {
		return block
	}
	if msg.Role != session.RoleAssistant {
		return block
	}
	block.Source = conversation.SourceAnswer
	block.Text = msg.Content
	if msg.Content == "" {
		return block
	}
	block.CopyTargets = append(block.CopyTargets, conversation.CopyTarget{
		Source: conversation.SourceAnswer,
		Text:   msg.Content,
		Label:  "Copy answer",
	})
	codeBlocks := conversation.ParseCodeFences(msg.Content)
	for i, cb := range codeBlocks {
		block.CopyTargets = append(block.CopyTargets, conversation.CopyTarget{
			Source: conversation.SourceCode,
			Text:   cb.Text,
			Label:  codeTargetLabel(i, len(codeBlocks), cb.Language),
		})
	}
	return block
}

// codeTargetLabel names one code block's copy action.
//
// A single block needs no number: there is nothing to distinguish it from, so
// the label is the plain scope statement. Several blocks do, because a menu
// of three identical "Copy code" entries is a menu the reader cannot use —
// hence the 1-based source-order number. The language hint is surfaced
// whenever the fence carried one, so a reader choosing between a go block and
// a bash block can tell which is which.
func codeTargetLabel(index, total int, language string) string {
	label := "Copy code"
	if total > 1 {
		label = fmt.Sprintf("Copy code %d", index+1)
	}
	if language == "" {
		return label
	}
	return fmt.Sprintf("%s (%s)", label, truncateHint(language))
}

// truncateHint bounds an author-supplied fence info string so a pathological
// fence cannot produce an unbounded menu label. The hint is otherwise
// surfaced as written: it is the author's text, and trimming it would hide
// the metadata ("go title=main.go") that makes it legible.
//
// The bound counts runes, not bytes: cutting mid-rune would put invalid UTF-8
// in a label the TUI has to render.
func truncateHint(hint string) string {
	const maxHint = 32
	runes := []rune(hint)
	if len(runes) <= maxHint {
		return hint
	}
	return string(runes[:maxHint]) + "…"
}

// withAuditContent attaches a tool call's captured output and the paths it
// touched.
//
// The output is the tool's own captured text, not a rendering of it, so
// "Copy output" copies what the tool actually produced. Paths are separate
// targets because a path is not output: it is what the call acted on, and it
// is the thing a user most often wants to paste into another command.
func withAuditContent(block conversation.Block, ev *registry.AuditEvent) conversation.Block {
	if ev == nil {
		return block
	}
	block.Text = ev.ResultContent
	if ev.ResultContent != "" {
		block.CopyTargets = append(block.CopyTargets, conversation.CopyTarget{
			Source: conversation.SourceOutput,
			Text:   ev.ResultContent,
			Label:  "Copy output",
		})
	}
	if len(ev.FilesChanged) > 0 {
		block.CopyTargets = append(block.CopyTargets, conversation.CopyTarget{
			Source: conversation.SourcePath,
			Text:   strings.Join(ev.FilesChanged, "\n"),
			Label:  "Copy path",
		})
	}
	return block
}

// blockKindFor maps a transcript kind to a block kind.
func blockKindFor(kind session.TranscriptKind) conversation.BlockKind {
	switch kind {
	case session.KindMessage:
		return conversation.BlockMessage
	case session.KindAudit:
		return conversation.BlockTool
	case session.KindThinking:
		return conversation.BlockThinking
	case session.KindSubagent:
		return conversation.BlockSubagent
	case session.KindRunEvent:
		return conversation.BlockRunEvent
	case session.KindJobExit:
		return conversation.BlockJobExit
	default:
		return conversation.BlockMessage
	}
}
