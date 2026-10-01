package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

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
	workChildren := flattenNotebookChildren(block.Children)
	workCount := 0
	for _, child := range workChildren {
		if child.ID != block.ID {
			workCount++
		}
	}
	if workCount > 1 {
		orderLabel := "Work order: recorded · click to reverse"
		if reversed {
			orderLabel = "Work order: reversed · click to restore"
		}
		parts = append(parts, notebookRenderPart{Text: continuation() + mutedStyle().Render(orderLabel) + "\n", OrderControl: true})
	}
	if !expanded || len(workChildren) == 0 {
		return parts
	}

	label := continuation() + mutedStyle().Render("Work") + "\n"
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
		}
		if hasItem && item.Audit != nil {
			header = DisplayToolName(item.Audit.ToolName)
			if item.Audit.Error != "" {
				header += dimSeparator + item.Audit.Error
			} else if item.Audit.ResultSummary != "" {
				header += dimSeparator + item.Audit.ResultSummary
			}
		} else if hasItem && item.Message != nil && item.Message.Final {
			header = "Final answer"
		}
		if header != "" {
			body.WriteString(nestedRail())
			body.WriteString(mutedStyle().Render(header))
			body.WriteByte('\n')
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
				if header != "" {
					bodyOffset = 1
				}
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
		})
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
