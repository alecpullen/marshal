package tui

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"strconv"
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
	var snapshot session.ActivitySnapshot
	options := conversationProjectionOptions{Notebook: m.notebookView, FollowingLatest: m.viewportFollow}
	if state, _ := m.conversationSource(); state != nil {
		snapshot = state.ActivitySnapshot()
		options.ScopeID = state.ScopeID()
	}
	return projectConversation(items, snapshot, options)
}

// conversationItems returns the transcript the user is actually looking at,
// plus whether that is a drilled-in child.
//
// It mirrors refreshViewport's selection, including the agent.run filter: the
// completed agent.run audit event duplicates the subagent card, and the card
// replaces it in the parent view. A document that included the filtered event
// would offer a copy target for an item the user cannot see.
func (m Model) conversationItems() []session.TranscriptItem {
	_, items := m.conversationSource()
	return items
}

// conversationSource is shared with refreshViewport so semantic actions and
// rendered rows use the same drill scope and duplicate agent.run filter.
func (m Model) conversationSource() (*session.State, []session.TranscriptItem) {
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
		return nil, nil
	}

	items := transcriptState.Transcript()
	if drilling {
		return transcriptState, items
	}

	filtered := make([]session.TranscriptItem, 0, len(items))
	for _, item := range items {
		if item.Kind == session.KindAudit && item.Audit != nil && item.Audit.ToolName == "agent.run" {
			continue
		}
		filtered = append(filtered, item)
	}
	return transcriptState, filtered
}

// conversationBlock converts one render entry into a semantic block.
//
// It reports false for an entry that has no identity to carry — a malformed
// item with no ViewID — rather than emitting a block a reader could not
// anchor to or select.
func conversationBlock(entry transcriptEntry) (conversation.Block, bool) {
	if entry.Group != nil {
		return blockRevision(toolGroupBlock(entry.Group, entry.GroupIDs)), true
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
		block.Hidden = messageIsHidden(item.Message)
	case session.KindAudit:
		block = withAuditContent(block, item.Audit)
		block.Truncated = auditTruncated(item.Audit)
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
	return blockRevision(block), true
}

// blockRevision stamps a block with a revision derived from its content.
//
// It exists because Block.Revision was DECLARED and never populated: every
// construction site left it at zero, so two consumers that key on it were both
// silently comparing 0 == 0.
//
//   - SearchIndex serves a cached projection when the revision matches, so a
//     streaming answer whose text grew was searched through its OLD text.
//   - Selection.MatchesRevision is how a caller detects that the text moved
//     underneath a selection, so a check that always returned true was no
//     check at all.
//
// A content hash is the right source because the revision's whole contract is
// "semantic change to this block's content": any cheap summary that changed
// exactly when the text changed and never otherwise would do, and a hash is
// the one that cannot drift from the text it describes.
//
// It is computed AFTER the text is attached, and it deliberately excludes
// Revision itself so stamping is not recursive.
func blockRevision(b conversation.Block) conversation.Block {
	b.Revision = blockRevisionFor(b)
	return b
}

// blockRevisionFor computes the revision a block's current content implies.
//
// The field set is exactly the set a render or a search projection depends on,
// each length-prefixed so two different field layouts cannot hash alike: a
// block whose Source became "Answer" and whose Text became "foo" must not
// share a revision with one whose Text became "Answerfoo".
func blockRevisionFor(b conversation.Block) int {
	h := fnv.New64a()
	// Kind is an int enum, so it is written as a number: converting it to a
	// string would yield a single rune (kind 7 becomes "\a"), and two different
	// kinds could then hash to text that collides with real content.
	writeRevisionField(h, strconv.Itoa(int(b.Kind)))
	writeRevisionField(h, b.Text)
	writeRevisionField(h, string(b.Source))
	writeRevisionField(h, b.ReferenceTarget)
	writeRevisionField(h, b.SectionLabel)
	for _, t := range b.CopyTargets {
		writeRevisionField(h, string(t.Source))
		writeRevisionField(h, t.Text)
		writeRevisionField(h, t.Label)
	}
	var flag byte
	if b.Hidden {
		flag |= 1
	}
	if b.Truncated {
		flag |= 2
	}
	_, _ = h.Write([]byte{flag})
	// Masked to the positive int range: Revision is an int, and on a 32-bit
	// build a raw uint64 would wrap to a negative number. A revision only ever
	// needs to DIFFER when the content differs, so the truncation is harmless.
	return int(h.Sum64() & 0x7fffffff)
}

// writeRevisionField writes one length-prefixed field into a revision hash.
//
// The length prefix is what stops two different field layouts hashing alike: a
// block whose Source became "Answer" and whose Text became "foo" must not share
// a revision with one whose Text became "Answerfoo".
func writeRevisionField(h hash.Hash64, s string) {
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(s))
}

// blockTextRevision computes the revision a block's TEXT implies, for the
// transcript's mapped path.
//
// The transcript does not build a conversation.Block: it renders a session
// message straight to rows and keeps the mapping. But the mapping's Revision
// field is what Selection.MatchesRevision compares, so a transcript block left
// at zero made that check vacuous — 0 == 0 on every comparison — and an old
// logical offset was painted over text that had changed underneath it, which is
// exactly the case the check exists to refuse.
//
// So the revision is derived from the same KIND of source as the document
// path's — a content hash over the block's text — and hashed with the same
// helper and the same length-prefixed layout.
//
// The two are PER-PATH revisions, NOT equal ones, and the difference is
// deliberate rather than a defect to be closed:
//
//   - The document path hashes blockRevisionFor(Block), which covers the kind,
//     the source, and every copy target as well as the text. That is what it
//     must cover: SearchIndex serves a cached projection keyed on it, and a
//     block whose source became "Answer" while its text stayed put is a
//     different block to search.
//   - The transcript path hashes the block's logical text alone, because the
//     transcript has no Block — a message renders straight to rows. Widening it
//     to the document's field set would mean reconstructing a Block here purely
//     to hash it.
//   - The text each hashes is also not the same string. The transcript hashes
//     the PROJECTED Markdown (sp.Text) of already tab-expanded content, while
//     the document hashes the message's raw Content.
//
// Nothing compares the two: MatchesRevision is fed a RenderedBlock from the same
// path that froze the selection, and SearchIndex compares document revisions
// only. The contract each one has to keep is therefore local — "this block's
// content changed under the reader, so the offsets they stored no longer mean
// what they meant" — and that is what a text hash gives, exactly, on both paths.
//
// It deliberately does NOT include the identity: the revision's contract is
// "this block's content changed", and an identity is not content. Two blocks
// with identical text therefore share a revision, which is correct — a selection
// is scoped by identity separately, in Selection.Block.
func blockTextRevision(logical string) int {
	h := fnv.New64a()
	writeRevisionField(h, logical)
	return int(h.Sum64() & 0x7fffffff)
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
		child.Truncated = auditTruncated(&events[i])
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
		// A user or system message carries no COPY TARGET — offering to copy
		// the user's own prompt back to them is noise, and a system notice is
		// not a document they chose. Its TEXT is still attached, because text
		// is what the reader can SEE: a prompt is drawn in the transcript, and
		// a find that could not match the question the reader typed — right
		// above the answer they are searching for it in — would be broken in
		// the most ordinary possible use.
		//
		// Source stays empty for these, which is what keeps the copy actions
		// away from them: the copy path resolves through a block's targets and
		// its Source, and an empty Source with no targets offers nothing.
		block.Text = msg.Content
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

// messageIsHidden reports whether a message reaches the model but is not drawn.
//
// This mirrors the early returns in renderMessageWithSink, and the two must
// agree: a block marked hidden that IS drawn would silently drop out of search,
// and a block drawn but not marked would offer the reader a jump to text they
// cannot see. The list is deliberately short and named rather than derived from
// the content type's name, because the rendering decision is what matters and
// it lives in the renderer.
//
// The compact markers are NOT hidden: a skill load, a compaction point and a
// steering message each render a one-line trace, and that trace is on screen
// and searchable like anything else.
func messageIsHidden(msg *session.Message) bool {
	if msg == nil {
		return false
	}
	switch msg.ContentType {
	case session.ContentTypeSkillBody,
		session.ContentTypeSubagentReport,
		session.ContentTypeWatchReport:
		return true
	}
	return false
}

// auditTruncated reports whether a tool event's captured output was capped by
// its source, so a search over its text can say what it actually covered.
//
// Both signals are the tool's own structural report, not prose: a notice for a
// slice/result cap, and the sandbox's flag for a killed or truncated command.
// Looking for the word "truncated" in the text instead would misfire on a
// message that merely discusses truncation, which is exactly the kind of
// message this transcript is full of.
func auditTruncated(ev *registry.AuditEvent) bool {
	if ev == nil {
		return false
	}
	if ev.Sandbox.OutputTruncated {
		return true
	}
	if ev.Notice == nil {
		return false
	}
	switch ev.Notice.Kind {
	case registry.NoticeOversizeFallback, registry.NoticeSliceTruncated, registry.NoticeCappedResults:
		return true
	}
	return false
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
