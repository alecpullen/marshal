package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/conversation"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
)

// notebookRenderPart is one independently addressable piece of a notebook
// section. Keeping the mapping beside its text lets the viewport place it in
// the same row geometry used by selection, find and click handling.
type notebookRenderPart struct {
	ID           conversation.BlockID
	Text         string
	Rendered     conversation.RenderedBlock
	BodyOffset   int
	PrefixCells  int
	OrderControl bool
	CopySource   conversation.CopySource
	Reference    string
	Disabled     string
}

func (m *Model) revealNotebookReference(member string) {
	state, _ := m.conversationSource()
	if state == nil || member == "" {
		return
	}
	doc := m.conversationDocument()
	location, ok := doc.LocateMember(member)
	if !ok {
		m.showToast("evidence source is no longer available")
		return
	}
	if m.notebookWorkReversed == nil {
		m.notebookWorkReversed = map[itemKey]bool{}
	}
	for _, ancestor := range location.Ancestors {
		key := notebookItemKey(state.ScopeID(), ancestor)
		if len(location.Ancestors) > 0 && ancestor == location.Ancestors[0] {
			m.notebookWorkReversed[key] = false
		}
		if m.itemExpanded == nil {
			m.itemExpanded = map[itemKey]bool{}
		}
		m.itemExpanded[key] = true
	}
	m.viewportFollow = false
	m.lastTranscriptHash = 0
	m.refreshViewport()
	if row, found := m.blockStartRow(conversation.BlockID(member)); found {
		m.viewport.SetYOffset(row)
		m.captureReadingAnchor()
	} else {
		m.showToast("evidence source is retained but not visible in this view")
	}
}

func (m *Model) copyNotebookBlockSource(id conversation.BlockID, source conversation.CopySource) tea.Cmd {
	var block conversation.Block
	var found bool
	var walk func([]conversation.Block) bool
	walk = func(blocks []conversation.Block) bool {
		for _, candidate := range blocks {
			if candidate.ID == id {
				block, found = candidate, true
				return true
			}
			if walk(candidate.Children) || walk(candidate.EventOrderAlternatives) {
				return true
			}
		}
		return false
	}
	walk(m.conversationDocument().Blocks())
	if !found {
		m.showToast(copyResolveFailure(source, nil))
		return nil
	}
	target, ok := blockSourceTarget(block, source)
	if !ok {
		m.showToast(copyResolveFailure(source, nil))
		return nil
	}
	return m.beginCopy(target)
}

func (m Model) notebookOrderTarget() (itemKey, bool) {
	if !m.notebookView {
		return itemKey{}, false
	}
	doc := m.conversationDocument()
	anchor := m.readingAnchor.Block
	if m.viewportFollow {
		for _, block := range doc.Blocks() {
			if block.Kind == conversation.BlockNarration && len(block.EventOrderAlternatives) > 0 {
				anchor = block.ID
				break
			}
		}
	} else if anchor == "" {
		anchor, _ = m.blockAtViewportTop()
	}
	if anchor == "" {
		return itemKey{}, false
	}
	var root *conversation.Block
	var walk func([]conversation.Block, *conversation.Block) bool
	walk = func(blocks []conversation.Block, narration *conversation.Block) bool {
		for i := range blocks {
			b := &blocks[i]
			current := narration
			if b.Kind == conversation.BlockNarration && len(b.EventOrderAlternatives) > 0 {
				current = b
			}
			if b.ID == anchor {
				root = current
				return true
			}
			if walk(b.Children, current) || walk(b.EventOrderAlternatives, current) {
				return true
			}
		}
		return false
	}
	walk(doc.Blocks(), nil)
	if root == nil {
		return itemKey{}, false
	}
	state, _ := m.conversationSource()
	if state == nil {
		return itemKey{}, false
	}
	return notebookItemKey(state.ScopeID(), root.ID), true
}

// notebookTranscriptEntries flattens the common semantic projection into
// viewport entries. A narration consumes its owned source records exactly
// once; unrelated items keep their chronological slots and source identities.
func notebookTranscriptEntries(items []session.TranscriptItem, snapshot session.ActivitySnapshot, options conversationProjectionOptions) []transcriptEntry {
	doc := notebookConversationDocument(items, snapshot, options)
	byID := make(map[string]*session.TranscriptItem, len(items))
	for i := range items {
		byID[items[i].ViewID] = &items[i]
	}
	used := make(map[string]bool, len(items))
	entries := make([]transcriptEntry, 0, len(items))
	for _, block := range doc.Blocks() {
		if block.Kind == conversation.BlockOwnershipNote {
			copy := block
			entries = append(entries, transcriptEntry{Notebook: &copy})
			continue
		}
		if block.Kind == conversation.BlockNarration && block.PresentationOnly {
			// A tool-only runtime fallback has no narration message to anchor
			// its display. Its explicitly owned first child supplies the
			// viewport position; the presentation parent itself claims no
			// transcript identity or copy payload.
			var anchor *session.TranscriptItem
			for _, child := range block.Children {
				if len(child.Members) == 0 {
					continue
				}
				anchor = byID[child.Members[0]]
				if anchor != nil {
					break
				}
			}
			if anchor != nil {
				copy := block
				entries = append(entries, transcriptEntry{Item: anchor, Notebook: &copy})
				for _, child := range block.Children {
					for _, member := range child.Members {
						used[member] = true
					}
				}
			}
			continue
		}
		if len(block.Members) == 0 {
			continue
		}
		id := block.Members[0]
		item := byID[id]
		if item == nil {
			continue
		}
		copy := block
		if block.Kind == conversation.BlockNarration {
			entries = append(entries, transcriptEntry{Item: item, Notebook: &copy})
			for _, member := range block.Members {
				used[member] = true
			}
			continue
		}
		if used[id] {
			continue
		}
		used[id] = true
		entries = append(entries, transcriptEntry{Item: item})
	}
	return entries
}

// renderNotebookNarration renders a public narration and its owned work at the
// supplied conversation width. The caller owns placement: each part carries
// its source identity and mapped rows, so the usual transcript geometry can be
// used without parsing screen text back into copyable content.
func renderNotebookNarration(block conversation.Block, width int, expanded, reversed, thinkingExpanded bool, records map[string]session.TranscriptItem) []notebookRenderPart {
	if block.Kind != conversation.BlockNarration || width <= 0 {
		return nil
	}

	logical := strings.TrimSpace(block.Text)
	if logical == "" {
		return nil
	}

	mode := BlockRenderSummary
	if expanded {
		mode = BlockRenderFull
	}
	const headlinePrefixCells = 5 // three-cell gutter, disclosure glyph, and gap
	headline, rendered := renderMappedBlock(logical, max(contentWidth(width)-2, 1), mode, 0)
	if len(rendered.Rows) == 0 {
		return nil
	}
	marker := glyph.DisclosureCollapsed
	if expanded {
		marker = glyph.DisclosureExpanded
	}
	style := lipgloss.NewStyle().Foreground(theme.Current().FGEmphasis)
	var head strings.Builder
	lines := strings.Split(strings.TrimSuffix(headline, "\n"), "\n")
	for i, line := range lines {
		if i == 0 {
			head.WriteString(gutterPrefix(glyph.Ambient, dimColor))
			head.WriteString(style.Render(marker + " "))
		} else {
			head.WriteString(strings.Repeat(" ", headlinePrefixCells))
		}
		head.WriteString(line)
		head.WriteByte('\n')
	}
	parts := []notebookRenderPart{{
		ID:          block.ID,
		Text:        head.String(),
		Rendered:    rendered,
		BodyOffset:  0,
		PrefixCells: headlinePrefixCells,
	}}
	structured := len(block.EventOrderAlternatives) > 0
	workChildren := flattenNotebookChildren(block.Children)
	if structured && reversed {
		workChildren = append([]conversation.Block(nil), block.EventOrderAlternatives...)
	}
	workCount := 0
	for _, child := range workChildren {
		if child.ID != block.ID {
			workCount++
		}
	}
	if structured || workCount > 1 {
		orderLabel := "Work order: recorded · click to reverse"
		if reversed {
			orderLabel = "Work order: reversed · click to restore"
		}
		if structured {
			orderLabel = "Show event order"
			if reversed {
				orderLabel = "Show sections"
			}
		}
		parts = append(parts, notebookRenderPart{Text: continuation() + mutedStyle().Render(orderLabel) + "\n", OrderControl: true})
	}
	if !expanded || len(workChildren) == 0 {
		return parts
	}

	workLabel := "Work"
	if structured {
		workLabel = "Sections"
		if reversed {
			workLabel = "Event order"
		}
	}
	label := continuation() + mutedStyle().Render(workLabel) + "\n"
	parts[0].Text += label
	for _, child := range workChildren {
		if child.ID == block.ID {
			continue // the parent headline already represents its source narration
		}
		item, hasItem := records[string(child.ID)]
		if child.ID == "" || (child.Text == "" && !hasItem) {
			continue
		}
		if hasItem && item.Kind == session.KindThinking {
			// Thinking remains governed by the existing disclosure preference;
			// its private body is never treated as public narration text.
			if item.Thinking != nil {
				parts[0].Text += renderThinkingSummary(item.Thinking.Text, item.Thinking.Duration, thinkingExpanded, width)
			}
			continue
		}
		const childPrefixCells = 5 // continuation indent, rail, and gap
		content := child.Text
		if child.Kind == conversation.BlockReference {
			content = ""
		}
		if hasItem && item.Message != nil {
			content = item.Message.Content
		}
		if hasItem && item.Audit != nil {
			content = item.Audit.ResultContent
		}
		var body strings.Builder
		header := ""
		if child.Kind == conversation.BlockSection {
			header = strings.ToUpper(child.SectionLabel)
		}
		if child.Kind == conversation.BlockReference {
			header = child.Text
			if child.ReferenceTarget == "" {
				header += " · source unavailable"
			}
		}
		if hasItem && item.Audit != nil {
			header = DisplayToolName(item.Audit.ToolName)
			if item.Audit.Approval == "denied" {
				header += dimSeparator + "Denied"
			}
			if item.Audit.Error != "" {
				header += dimSeparator + item.Audit.Error
			}
			if item.Audit.CommandExitCode != nil && *item.Audit.CommandExitCode != 0 {
				header += dimSeparator + "exit " + strconv.Itoa(*item.Audit.CommandExitCode)
			}
			if item.Audit.Error == "" && (item.Audit.CommandExitCode == nil || *item.Audit.CommandExitCode == 0) && item.Audit.ResultSummary != "" {
				header += dimSeparator + item.Audit.ResultSummary
			}
		} else if child.SourceRevision > 0 {
			header = "Revision " + strconv.FormatUint(child.SourceRevision, 10)
		} else if hasItem && item.Message != nil && item.Message.Final {
			header = "Final answer"
		}
		headerRows := 0
		if header != "" {
			wrapped := ansi.Wrap(header, nestedContentWidth(width), WrapBreakpoints)
			for _, line := range strings.Split(wrapped, "\n") {
				body.WriteString(nestedRail())
				body.WriteString(mutedStyle().Render(line))
				body.WriteByte('\n')
				headerRows++
			}
		}
		bodyOffset := 0
		var childRendered conversation.RenderedBlock
		if content != "" {
			text, mapped := renderMappedBlock(content, nestedContentWidth(width), BlockRenderFull, 0)
			if len(mapped.Rows) > 0 {
				childRendered = mapped
				for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
					body.WriteString(nestedRail())
					body.WriteString(line)
					body.WriteByte('\n')
				}
				bodyOffset = headerRows
			}
		}
		if childRendered.Logical == "" && header == "" {
			continue
		}
		parts = append(parts, notebookRenderPart{
			ID:          child.ID,
			Text:        body.String(),
			Rendered:    childRendered,
			BodyOffset:  bodyOffset,
			PrefixCells: childPrefixCells,
			Reference:   child.ReferenceTarget,
			Disabled: func() string {
				if child.Kind == conversation.BlockReference && child.ReferenceTarget == "" {
					return "source is unavailable"
				}
				return ""
			}(),
		})
		if child.Kind == conversation.BlockSection || (hasItem && item.Message != nil && item.Message.ContentType == session.ContentTypeNarration) {
			parts = append(parts, notebookRenderPart{ID: child.ID, Text: nestedRail() + mutedStyle().Render("Copy section") + "\n", CopySource: conversation.SourceAnswer})
		} else if hasItem && item.Audit != nil && item.Audit.ResultContent != "" {
			parts = append(parts, notebookRenderPart{ID: child.ID, Text: nestedRail() + mutedStyle().Render("Copy output") + "\n", CopySource: conversation.SourceOutput})
		}
	}
	return parts
}

func flattenNotebookChildren(children []conversation.Block) []conversation.Block {
	var out []conversation.Block
	for _, child := range children {
		out = append(out, child)
		if len(child.Children) > 0 {
			out = append(out, flattenNotebookChildren(child.Children)...)
		}
	}
	return out
}

// reverseNotebookWorkChildren returns a private presentation copy with work
// children reversed. The narration source stays first and every source block,
// copy target, and runtime record remains untouched.
func reverseNotebookWorkChildren(block conversation.Block, reverse bool) conversation.Block {
	if !reverse || len(block.Children) < 2 {
		return block
	}
	children := append([]conversation.Block(nil), block.Children...)
	sectionEnd := 0
	for sectionEnd < len(children) && children[sectionEnd].Kind == conversation.BlockSection {
		sectionEnd++
	}
	if sectionEnd > 0 {
		for left, right := sectionEnd, len(children)-1; left < right; left, right = left+1, right-1 {
			children[left], children[right] = children[right], children[left]
		}
		block.Children = children
		return block
	}
	source := -1
	for i := range children {
		if children[i].ID == block.ID {
			source = i
			break
		}
	}
	start := 0
	if source >= 0 {
		children[0], children[source] = children[source], children[0]
		start = 1
	}
	for left, right := start, len(children)-1; left < right; left, right = left+1, right-1 {
		children[left], children[right] = children[right], children[left]
	}
	block.Children = children
	return block
}

func renderOwnershipNote(text string, width int) string {
	if strings.TrimSpace(text) == "" || width <= 0 {
		return ""
	}
	return gutterPrefix(glyph.Ambient, dimColor) + mutedStyle().Render(strings.TrimSpace(text)) + "\n"
}
