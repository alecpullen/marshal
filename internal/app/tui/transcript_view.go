package tui

import (
	"marshal/internal/app/config"
	"marshal/internal/app/tui/conversation"
)

type transcriptReadingState struct {
	follow   bool
	anchor   conversation.Anchor
	expanded map[itemKey]bool
}

func transcriptViewPtr(v config.TranscriptView) *config.TranscriptView { return &v }

func (m Model) configuredTranscriptView() config.TranscriptView {
	if m.state == nil {
		return config.TranscriptLegacy
	}
	return m.state.Config.TUI.TranscriptView.Effective()
}

func (m Model) effectiveTranscriptView() config.TranscriptView {
	if m.transcriptViewOverride != nil {
		return *m.transcriptViewOverride
	}
	return m.configuredTranscriptView()
}

func (m *Model) setTranscriptView(next config.TranscriptView) {
	if next != config.TranscriptLegacy && next != config.TranscriptNotebook {
		return
	}
	if m.selectionActive() {
		m.pendingTranscriptView = transcriptViewPtr(next)
		return
	}
	if m.notebookView == (next == config.TranscriptNotebook) {
		return
	}
	oldDoc := m.conversationDocument()
	var source string
	if m.readingAnchor.Block != "" {
		if b, ok := oldDoc.Block(m.readingAnchor.Block); ok && len(b.Members) > 0 {
			source = b.Members[0]
		}
	}
	oldView := config.TranscriptLegacy
	if m.notebookView {
		oldView = config.TranscriptNotebook
	}
	oldKey := m.transcriptReadingKey(oldView)
	if m.transcriptViewStates == nil {
		m.transcriptViewStates = map[string]transcriptReadingState{}
	}
	m.transcriptViewStates[oldKey] = transcriptReadingState{follow: m.viewportFollow, anchor: m.readingAnchor, expanded: cloneExpanded(m.itemExpanded)}
	m.notebookView = next == config.TranscriptNotebook
	newKey := m.transcriptReadingKey(next)
	if saved, ok := m.transcriptViewStates[newKey]; ok {
		m.viewportFollow, m.readingAnchor, m.itemExpanded = saved.follow, saved.anchor, cloneExpanded(saved.expanded)
	} else if source != "" {
		newDoc := m.conversationDocument()
		if loc, ok := newDoc.LocateMember(source); ok {
			idx, _ := newDoc.IndexOf(loc.Block.ID)
			m.readingAnchor = conversation.Anchor{Block: loc.Block.ID, Offset: m.readingAnchor.Offset, Index: idx}
		} else {
			// Keep the source anchor and let the existing resolver choose its nearest
			// surviving block; that path reports the approximate placement.
			m.readingAnchor.Block = conversation.BlockID(source)
		}
	} else {
		m.viewportFollow = true
		m.readingAnchor = conversation.Anchor{}
	}
	m.lastTranscriptHash = 0
	m.transcriptVersion++
	m.transcriptBase = ""
	m.blockRenderCache = map[blockKey]blockMemoEntry{}
	m.findIndex = nil
	m.refreshViewport()
}

func (m Model) transcriptReadingKey(view config.TranscriptView) string {
	state, _ := m.conversationSource()
	scope := "root"
	if state != nil {
		scope = state.ScopeID()
	}
	return string(view) + ":" + scope
}

func cloneExpanded(src map[itemKey]bool) map[itemKey]bool {
	out := make(map[itemKey]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
