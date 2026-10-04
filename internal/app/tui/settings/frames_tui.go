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
			func() *field {
				f := enumField("tui.transcript.density", "Transcript detail", []string{"outline", "steps", "full"},
					func() string { return s.cfg.TUI.Transcript.Density },
					func(v string) { s.cfg.TUI.Transcript.Density = v })
				f.TomlPath = "tui.transcript.density"
				f.Desc = "starting detail level; Ctrl+G cycles it (outline · steps · full)"
				SetFieldWriteGlobal(f, true)
				return f
			}(),
			func() *field {
				f := &field{ID: "tui.transcript.fold_finished_tasks", Title: "Fold finished tasks", Kind: kindToggle,
					TomlPath: "tui.transcript.fold_finished_tasks",
					Desc:     "collapse a completed task to one row (z toggles in browse mode)",
					GetBool:  func() bool { return s.cfg.TUI.Transcript.FoldFinishedTasks },
					SetBool:  func(v bool) { s.cfg.TUI.Transcript.FoldFinishedTasks = v }}
				SetFieldWriteGlobal(f, true)
				return f
			}(),
		}
	})
}
