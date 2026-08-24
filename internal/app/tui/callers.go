package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// maxCallersShown caps how many references a row names before collapsing
// the rest into a count.
const maxCallersShown = 3

// ReferenceFinder resolves what references a symbol.
//
// ok is false when no language server is ready for the file's language,
// which is the common case: LSP is optional and off by default. The
// interface is declared here, narrow, rather than taking *lsp.Handle — the
// TUI needs exactly one method and should not take on the LSP package.
// lsp.QueryAdapter satisfies it as-is.
type ReferenceFinder interface {
	References(ctx context.Context, filePath string, line, col int) ([]string, bool)
}

// WithReferenceFinder wires blast-radius lookups. When unset, edit rows
// simply render without a callers line.
func WithReferenceFinder(rf ReferenceFinder) Option {
	return func(m *Model) { m.refFinder = rf }
}

// callersMsg carries a completed reference lookup back to the model. ok is
// false when no server was ready; the result is still cached, as a negative,
// so the query is never retried.
type callersMsg struct {
	key     itemKey
	callers []string
	ok      bool
}

// callerQueryCmds returns a batched command for every transcript audit item
// that carries a resolved symbol and has not been queried yet.
//
// This is deliberately not called from refreshViewport: that runs on every
// spinner tick and returns no commands. Callers come from the tick and
// turn-finished handlers instead, so a lookup costs at most one tick of
// latency and never sits on the render path.
func (m *Model) callerQueryCmds() tea.Cmd {
	if m.refFinder == nil {
		return nil
	}
	if m.callersAsked == nil {
		m.callersAsked = map[itemKey]bool{}
	}
	var cmds []tea.Cmd
	for _, item := range m.state.Transcript() {
		if item.Kind != session.KindAudit || item.Audit == nil {
			continue
		}
		if !subjectFirstTool(item.Audit.ToolName) || item.Audit.ToolName == "file.read" {
			continue
		}
		ref, ok := firstResolvedSymbol(*item.Audit)
		if !ok {
			continue
		}
		key := itemKeyFor(&item)
		if m.callersAsked[key] {
			continue
		}
		m.callersAsked[key] = true
		cmds = append(cmds, queryCallersCmd(m.lspCtx, m.refFinder, key, ref))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// firstResolvedSymbol returns the event's first symbol whose name position
// was resolved. An unresolved position is never queried: a lookup at a
// guessed column returns confidently wrong callers, which is worse than
// none.
func firstResolvedSymbol(event registry.AuditEvent) (registry.SymbolRef, bool) {
	for _, s := range event.Symbols {
		if s.Resolved {
			return s, true
		}
	}
	return registry.SymbolRef{}, false
}

func queryCallersCmd(ctx context.Context, rf ReferenceFinder, key itemKey, ref registry.SymbolRef) tea.Cmd {
	return func() tea.Msg {
		refs, ok := rf.References(ctx, ref.File, ref.Line, ref.Col)
		return callersMsg{key: key, callers: refs, ok: ok}
	}
}

// handleCallers stores a completed lookup. A negative result is stored as
// an empty slice so the key is present and the query is not retried.
func (m Model) handleCallers(msg callersMsg) (Model, tea.Cmd) {
	if m.callers == nil {
		m.callers = map[itemKey][]string{}
	}
	if msg.ok {
		m.callers[msg.key] = msg.callers
	} else {
		m.callers[msg.key] = nil
	}
	m.lastTranscriptHash = 0
	m.refreshViewport()
	return m, nil
}
