// Package docpanel renders a commands.Doc as an interactive dock panel
// using the listpanel engine: section headers, title/detail rows, Enter
// actions, and one level of drill-in.
package docpanel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/listpanel"
	"marshal/internal/app/tui/theme"
	"marshal/internal/commands"
)

// ClosedMsg asks the host to close the panel.
type ClosedMsg struct{}

// ActionMsg carries the Result a row action produced.
type ActionMsg struct{ Result commands.Result }

// Panel is a dock.Panel over a commands.Doc.
type Panel struct {
	state *session.State
	doc   commands.Doc
	stack *listpanel.PaneStack
}

var _ dock.Panel = (*Panel)(nil)

// New builds a panel for doc. Rows are converted eagerly; drill frames are
// built lazily by the engine on Enter.
func New(doc commands.Doc, state *session.State) *Panel {
	root := listpanel.NewFrame(doc.Title, func() []*listpanel.Field {
		return rowsToFields(doc.Rows, state)
	})
	return &Panel{state: state, doc: doc, stack: listpanel.NewPaneStack(root)}
}

// rowsToFields converts Doc rows to engine fields. Shared by the root frame
// and drill-in children.
func rowsToFields(rows []commands.Row, state *session.State) []*listpanel.Field {
	out := make([]*listpanel.Field, 0, len(rows))
	for _, row := range rows {
		row := row
		id := row.Text
		switch {
		case row.Header != "":
			out = append(out, &listpanel.Field{ID: "h" + row.Header, Title: row.Header, Kind: listpanel.KindHeader})
		case len(row.Children) > 0:
			children := row.Children
			out = append(out, &listpanel.Field{
				ID: id, Title: row.Text, Kind: listpanel.KindDrill, Desc: row.Desc,
				Summary: func() string { return row.Detail },
				Build: func() *listpanel.Frame {
					return listpanel.NewFrame(row.Text, func() []*listpanel.Field {
						return rowsToFields(children, state)
					})
				},
			})
		case row.Action != nil:
			label := row.ActionLabel
			if label == "" {
				label = "↵ run"
			}
			out = append(out, &listpanel.Field{
				ID: id, Title: row.Text, Kind: listpanel.KindAction, Desc: row.Desc,
				ActLabel: func() string { return label },
				Act: func() tea.Cmd {
					return func() tea.Msg { return ActionMsg{Result: row.Action(state)} }
				},
			})
		default:
			out = append(out, &listpanel.Field{
				ID: id, Title: row.Text, Kind: listpanel.KindScalar, Desc: row.Desc,
				GetStr: func() string { return row.Detail },
			})
		}
	}
	return out
}

// Update routes keys to the engine; Esc pops a drill level or closes.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		if p.stack.Pop() {
			return nil
		}
		return func() tea.Msg { return ClosedMsg{} }
	}
	return p.stack.Update(msg)
}

// Sizing follows the doc's FullFrame flag.
func (p *Panel) Sizing() dock.Sizing {
	if p.doc.FullFrame {
		return dock.FullFrame
	}
	return dock.Docked
}

// View renders the current frame inside the dock's dimensions.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 2 {
		return ""
	}
	pw := layout.PanelWidth(width)
	inner := pw - 3

	// Reserve the rendered footer height so the keybinding cheatsheet is not
	// clipped by chrome.PanelWithHints. The chrome adds one header row, so the
	// list plus footer must fit in maxHeight-1 rows.
	footerHeight := 0
	footer := ""
	if len(p.stack.Stack) == 1 && p.doc.Footer != "" {
		footer = lipgloss.NewStyle().Foreground(theme.Current().FGMuted).Render(p.doc.Footer)
		footerHeight = lipgloss.Height(footer)
	}
	listHeight := maxHeight - 1 - footerHeight
	if listHeight < 1 {
		listHeight = 1
	}
	p.stack.SetSize(inner, listHeight)

	title := p.doc.Title
	if len(p.stack.Stack) > 1 {
		title = p.stack.Breadcrumb(p.doc.Title)
	}
	body := p.stack.Top().List.View()
	if footer != "" {
		body += "\n" + footer
	}
	hints := "↑↓ move · ↵ select · esc back"
	if len(p.stack.Stack) == 1 {
		hints = "↑↓ move · ↵ select · esc close"
	}
	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints(title, hints, body, pw, ph, true, theme.Current())
}
