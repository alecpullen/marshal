// Package doctorpanel renders /doctor diagnostics as an interactive dock
// panel. Fixable env-key diagnostics become action rows that emit FixMsg.
package doctorpanel

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/dock"
	"marshal/internal/app/tui/layout"
	"marshal/internal/app/tui/listpanel"
	"marshal/internal/app/tui/theme"
)

// ClosedMsg asks the host to close the panel.
type ClosedMsg struct{}

// FixMsg is emitted when the user selects a fixable missing-env diagnostic.
type FixMsg struct{ Provider string }

// Panel renders diagnostics and supports fixing missing env vars.
type Panel struct {
	state *session.State
	diags []config.Diagnostic
	stack *listpanel.PaneStack
}

var _ dock.Panel = (*Panel)(nil)

// New builds a panel from diagnostics and intelligence-subsystem checks.
func New(state *session.State, diags []config.Diagnostic, checks []Check) *Panel {
	root := listpanel.NewFrame("Doctor", func() []*listpanel.Field {
		return diagsToFields(diags, checks)
	})
	return &Panel{state: state, diags: diags, stack: listpanel.NewPaneStack(root)}
}

func diagsToFields(diags []config.Diagnostic, checks []Check) []*listpanel.Field {
	out := make([]*listpanel.Field, 0, len(diags)+len(checks)+3)
	if len(diags) == 0 {
		// Scoped to config problems: the Intelligence section below can still
		// legitimately report subsystem warnings (e.g. the index watcher is
		// off), so an unconditional "No problems found." would read as a
		// contradiction next to it.
		out = append(out, &listpanel.Field{
			ID: "clean", Title: "No config problems found.", Kind: listpanel.KindScalar,
			GetStr: func() string { return "" },
		})
	} else {
		var errs, warns []config.Diagnostic
		for _, d := range diags {
			switch d.Severity {
			case config.SeverityError:
				errs = append(errs, d)
			case config.SeverityWarning:
				warns = append(warns, d)
			}
		}
		if len(errs) > 0 {
			out = append(out, &listpanel.Field{ID: "herrors", Title: "Errors", Kind: listpanel.KindHeader})
			out = append(out, diagRows(errs, true)...)
		}
		if len(warns) > 0 {
			out = append(out, &listpanel.Field{ID: "hwarnings", Title: "Warnings", Kind: listpanel.KindHeader})
			out = append(out, diagRows(warns, true)...)
		}
	}
	if len(checks) > 0 {
		out = append(out, &listpanel.Field{ID: "hintelligence", Title: "Intelligence", Kind: listpanel.KindHeader})
		for _, c := range checks {
			c := c
			out = append(out, &listpanel.Field{
				ID: "intel." + strings.ToLower(c.Name), Title: c.Name, Kind: listpanel.KindScalar,
				Desc:   c.Detail,
				GetStr: func() string { return c.Status },
			})
		}
	}
	return out
}

func diagRows(diags []config.Diagnostic, includeActions bool) []*listpanel.Field {
	out := make([]*listpanel.Field, 0, len(diags))
	for _, d := range diags {
		d := d
		desc := d.Message
		if d.Source != "" && d.Source != "default" {
			desc += " — in " + d.Source
		}
		provider := fixableProvider(d)
		if includeActions && provider != "" {
			out = append(out, &listpanel.Field{
				ID: d.Path, Title: d.Path, Kind: listpanel.KindAction,
				Desc:     desc,
				ActLabel: func() string { return "enter key" },
				Act: func() tea.Cmd {
					p := provider
					return func() tea.Msg { return FixMsg{Provider: p} }
				},
			})
			continue
		}
		out = append(out, &listpanel.Field{
			ID: d.Path, Title: d.Path, Kind: listpanel.KindScalar,
			Desc: desc,
			GetStr: func() string {
				if d.Severity == config.SeverityError {
					return "error"
				}
				return "warning"
			},
		})
	}
	return out
}

// fixableProvider returns the provider name if the diagnostic is a fixable
// missing-env diagnostic, otherwise empty string.
func fixableProvider(d config.Diagnostic) string {
	if !strings.HasPrefix(d.Path, "providers.") || !strings.HasSuffix(d.Path, ".api_key_env") {
		return ""
	}
	parts := strings.Split(d.Path, ".")
	if len(parts) != 3 {
		return ""
	}
	return parts[1]
}

// Update routes keys to the engine; Esc closes.
func (p *Panel) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok && k.String() == "esc" {
		if p.stack.Pop() {
			return nil
		}
		return func() tea.Msg { return ClosedMsg{} }
	}
	return p.stack.Update(msg)
}

// Sizing returns docked sizing.
func (p *Panel) Sizing() dock.Sizing { return dock.Docked }

// View renders the panel.
func (p *Panel) View(width, maxHeight int) string {
	if maxHeight < 2 {
		return ""
	}
	pw := layout.PanelWidth(width)
	inner := pw - 3
	p.stack.SetSize(inner, maxHeight-1)
	body := p.stack.Top().List.View()
	hints := "↑↓ move · ↵ enter key · esc close"
	ph := min(lipgloss.Height(body)+1, maxHeight)
	return chrome.PanelWithHints("Doctor", hints, body, pw, ph, true, theme.Current())
}
