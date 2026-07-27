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
				f.desc = "color theme for the terminal UI"
				return f
			}(),
			func() *field {
				f := enumField("tui.mode", "Mode", []string{"dark", "light"},
					func() string { return s.cfg.TUI.Mode },
					func(v string) { s.cfg.TUI.Mode = v })
				f.desc = "color scheme variant"
				return f
			}(),
			{id: "tui.side_panel.enabled", title: "Side panel", kind: kindToggle,
				tomlPath: "tui.side_panel.enabled",
				desc:     "show the widescreen side rail on wide terminals",
				getBool:  func() bool { return s.cfg.TUI.SidePanel.Enabled },
				setBool:  func(v bool) { s.cfg.TUI.SidePanel.Enabled = v }},
			func() *field {
				f := intField("tui.side_panel.min_width", "Side panel min width",
					func() int { return s.cfg.TUI.SidePanel.MinWidth },
					80, func(v int) { s.cfg.TUI.SidePanel.MinWidth = v })
				f.desc = "frame width at which the side rail appears"
				return f
			}(),
			func() *field {
				f := intField("tui.side_panel.width_pct", "Side panel width %",
					func() int { return s.cfg.TUI.SidePanel.WidthPct },
					10, func(v int) { s.cfg.TUI.SidePanel.WidthPct = v })
				f.desc = "percentage of frame width the rail occupies"
				return f
			}(),
		}
	})
}
