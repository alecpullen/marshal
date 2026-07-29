package settings

import (
	"marshal/internal/app/tui/theme"
)

func interfaceFrame(s *state) *frame {
	return newFrame("Interface", func() []*field {
		return []*field{
			func() *field {
				f := enumField("tui.theme", "Theme", theme.Names(),
					func() string { return s.cfg.TUI.Theme },
					func(v string) { s.cfg.TUI.Theme = v })
				f.TomlPath = "tui.theme"
				f.Desc = "color theme for the terminal UI"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				f := enumField("tui.mode", "Mode", []string{"dark", "light"},
					func() string { return s.cfg.TUI.Mode },
					func(v string) { s.cfg.TUI.Mode = v })
				f.TomlPath = "tui.mode"
				f.Desc = "color scheme variant"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				f := &field{ID: "tui.mouse_capture", Title: "Mouse capture", Kind: kindToggle,
					TomlPath: "tui.mouse_capture",
					Desc:     "wheel scrolls the transcript; hold Option/Alt to select text",
					GetBool:  func() bool { return s.cfg.TUI.MouseCapture },
					SetBool:  func(v bool) { s.cfg.TUI.MouseCapture = v }}
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				f := &field{ID: "tui.side_panel.enabled", Title: "Side panel", Kind: kindToggle,
					TomlPath: "tui.side_panel.enabled",
					Desc:     "show the widescreen side rail on wide terminals",
					GetBool:  func() bool { return s.cfg.TUI.SidePanel.Enabled },
					SetBool:  func(v bool) { s.cfg.TUI.SidePanel.Enabled = v }}
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				f := intField("tui.side_panel.min_width", "Side panel min width",
					func() int { return s.cfg.TUI.SidePanel.MinWidth },
					80, func(v int) { s.cfg.TUI.SidePanel.MinWidth = v })
				f.TomlPath = "tui.side_panel.min_width"
				f.Desc = "frame width at which the side rail appears"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				f := intField("tui.side_panel.width_pct", "Side panel width %",
					func() int { return s.cfg.TUI.SidePanel.WidthPct },
					10, func(v int) { s.cfg.TUI.SidePanel.WidthPct = v })
				f.TomlPath = "tui.side_panel.width_pct"
				f.Desc = "percentage of frame width the rail occupies"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
		}
	})
}
