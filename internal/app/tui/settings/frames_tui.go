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
				f := &field{ID: "tui.mouse_capture", Title: "Mouse capture", Kind: kindToggle,
					TomlPath: "tui.mouse_capture",
					Desc:     "wheel scrolls the transcript; hold Option/Alt to select text",
					GetBool:  func() bool { return s.cfg.TUI.MouseCapture },
					SetBool:  func(v bool) { s.cfg.TUI.MouseCapture = v }}
				SetFieldWriteGlobal(f, true)
				return f
			}(),
		}
	})
}
