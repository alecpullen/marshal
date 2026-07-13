package settings

import (
	"marshal/internal/app/tui/theme"
)

func interfaceFrame(s *state) *frame {
	return newFrame("Interface", func() []*field {
		return []*field{
			enumField("tui.theme", "Theme", theme.Names(),
				func() string { return s.cfg.TUI.Theme },
				func(v string) { s.cfg.TUI.Theme = v }),
		}
	})
}
