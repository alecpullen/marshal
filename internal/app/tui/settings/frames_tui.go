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
		}
	})
}
