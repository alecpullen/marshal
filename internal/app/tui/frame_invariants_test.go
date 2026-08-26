package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/theme"
	"marshal/internal/tools/native"
)

// TestFrameInvariants sweeps stacked-panel states and asserts two things of
// every rendered row: no doubled rail glyph, and no row wider than the frame.
//
// The doubled rail is a reported defect that has resisted reproduction — see
// the 2026-08-26 agents-chrome spec. When the provoking state is found, add it
// to variants below rather than starting a new investigation. The width
// invariant is independent: an over-wide row wraps in a real terminal, which
// breaks the frame-height budget clipLeftColumn enforces.
func TestFrameInvariants(t *testing.T) {
	doubled := glyph.Rail + glyph.Rail

	variants := []struct {
		name  string
		setup func(t *testing.T, m *Model)
	}{
		{"bare", func(t *testing.T, m *Model) {}},
		{"todos+lane", func(t *testing.T, m *Model) {
			registerRunningSubagent(t, m, "reviewer")
			mustSetTodos(t, m, native.TodoItem{Content: "a task", Status: native.TodoInProgress})
		}},
		{"todos+lane+drilled", func(t *testing.T, m *Model) {
			registerRunningSubagent(t, m, "reviewer")
			mustSetTodos(t, m, native.TodoItem{Content: "a task", Status: native.TodoInProgress})
			if subs := m.state.Subagents(); len(subs) > 0 {
				m.viewStack = append(m.viewStack, subs[0])
			}
		}},
		{"many-todos+agents", func(t *testing.T, m *Model) {
			for _, n := range []string{"a", "b", "c", "d"} {
				registerRunningSubagent(t, m, n)
			}
			items := make([]native.TodoItem, 0, 10)
			for i := 0; i < 9; i++ {
				items = append(items, native.TodoItem{Content: "task", Status: native.TodoCompleted})
			}
			items = append(items, native.TodoItem{Content: "current", Status: native.TodoInProgress})
			mustSetTodos(t, m, items...)
		}},
	}

	prev := theme.Current()
	t.Cleanup(func() { theme.Reload(prev) })

	for _, v := range variants {
		for _, w := range []int{80, 100, 140, 200} {
			for _, h := range []int{24, 30, 40} {
				for _, depth := range []theme.Depth{theme.DepthFlat, theme.DepthRaised, theme.DepthFull} {
					for mode := todoPanelExpanded; mode < todoPanelModeCount; mode++ {
						th := prev
						th.Depth = depth
						theme.Reload(th)

						m := newTestModel(t)
						m.resize(w, h)
						v.setup(t, &m)
						m.todoPanelMode = mode

						for i, line := range strings.Split(m.viewString(), "\n") {
							if strings.Contains(line, doubled) {
								t.Errorf("[%s w=%d h=%d depth=%v mode=%d] doubled rail on row %d: %q",
									v.name, w, h, depth, mode, i, line)
							}
							if lw := ansi.StringWidth(line); lw > w {
								t.Errorf("[%s w=%d h=%d depth=%v mode=%d] row %d is %d wide, frame is %d: %q",
									v.name, w, h, depth, mode, i, lw, w, line)
							}
						}
					}
				}
			}
		}
	}
}

func mustSetTodos(t *testing.T, m *Model, items ...native.TodoItem) {
	t.Helper()
	if err := m.state.SetTodos(items); err != nil {
		t.Fatalf("SetTodos: %v", err)
	}
}
