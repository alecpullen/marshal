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
				f := enumField("tui.depth", "Depth", theme.DepthNames(),
					func() string { return s.cfg.TUI.Depth },
					func(v string) { s.cfg.TUI.Depth = v })
				f.TomlPath = "tui.depth"
				f.Desc = "background planes the TUI paints (flat · raised · full)"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				// The description states what the setting does and how to get
				// the other behaviour, both of which are verified: capture on
				// means the wheel scrolls, and Ctrl+S hands the mouse back for
				// native selection. It deliberately does NOT name a
				// per-terminal modifier for selecting while captured — that
				// shortcut is unverified per terminal and version, and a
				// blanket "hold Option/Alt" claim is wrong on terminals that
				// do not implement it.
				f := &field{ID: "tui.mouse_capture", Title: "Mouse capture", Kind: kindToggle,
					TomlPath: "tui.mouse_capture",
					Desc:     "keep the wheel scrolling the transcript; Ctrl+S releases the mouse for click-drag selection",
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
