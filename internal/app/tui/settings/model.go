package settings

import (
	"fmt"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
)

const sidebarWidth = 18

type Model struct {
	state          *state
	sections       []section
	panes          []sectionPane
	cursor         int
	paneFocused    bool
	helpOpen       bool
	workingDir     string
	projectCfgPath string
	footer         string
	width          int
	height         int
}

func New(cfg config.Config, workingDir, projectCfgPath string) Model {
	st := newState(cfg)
	secs := sectionList()
	panes := make([]sectionPane, len(secs))
	for i, sec := range secs {
		panes[i] = sec.build(st)
		if c := panes[i].Init(); c != nil {
			_ = c()
		}
	}
	return Model{
		state:          st,
		sections:       secs,
		panes:          panes,
		workingDir:     workingDir,
		projectCfgPath: projectCfgPath,
	}
}

func (m Model) Init() tea.Cmd { return nil }

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	pw := width - sidebarWidth - 6
	if pw < 30 {
		pw = 30
	}
	for _, p := range m.panes {
		p.SetWidth(pw)
	}
}

func (m Model) dirty() bool {
	return !reflect.DeepEqual(m.state.cfg, m.state.snapshot)
}

func (m *Model) activePane() sectionPane { return m.panes[m.cursor] }

func (m *Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			if m.activePane().HasInnerFocus() {
				m.activePane().CloseInner()
				return *m, nil
			}
			return *m, func() tea.Msg { return CancelledMsg{} }
		case "ctrl+s":
			return *m, m.saveCmd()
		case "?":
			if !m.activePane().HasInnerFocus() {
				m.helpOpen = !m.helpOpen
				return *m, nil
			}
		}

		if !m.paneFocused {
			switch k.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
				return *m, nil
			case "down", "j":
				if m.cursor < len(m.sections)-1 {
					m.cursor++
				}
				return *m, nil
			case "g":
				m.cursor = 0
				return *m, nil
			case "G":
				m.cursor = len(m.sections) - 1
				return *m, nil
			case "tab", "l", "right":
				m.paneFocused = true
				return *m, nil
			}
			return *m, nil
		}

		// Pane focused: sidebar-return keys are handled here only when the
		// pane has no inner edit open (so typing "h" into a text input works).
		if !m.activePane().HasInnerFocus() {
			switch k.String() {
			case "shift+tab", "h", "left":
				m.paneFocused = false
				return *m, nil
			}
		}
	}

	if m.paneFocused {
		updated, cmd := m.activePane().Update(msg)
		m.panes[m.cursor] = updated
		return *m, cmd
	}
	return *m, nil
}

func (m *Model) saveCmd() tea.Cmd {
	return func() tea.Msg {
		if err := config.SaveProjectConfig(m.projectCfgPath, m.state.cfg); err != nil {
			m.footer = fmt.Sprintf("Save failed: %v", err)
			return nil
		}
		loaded, err := config.Load(config.LoadOptions{WorkingDir: m.workingDir})
		if err != nil {
			m.footer = fmt.Sprintf("Reload failed: %v", err)
			return nil
		}
		return SavedMsg{Cfg: loaded}
	}
}

var (
	sidebarActiveStyle = lipgloss.NewStyle().Bold(true).Reverse(true)
	sidebarItemStyle   = lipgloss.NewStyle()
	paneTitleStyle     = lipgloss.NewStyle().Bold(true)
	warnStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (m Model) View() string {
	if m.helpOpen {
		return m.helpView()
	}
	var sb strings.Builder
	for i, sec := range m.sections {
		label := " " + sec.title
		if i == m.cursor {
			marker := " "
			if m.paneFocused {
				marker = "▸"
			}
			label = sidebarActiveStyle.Render(marker + sec.title)
		} else {
			label = sidebarItemStyle.Render(label)
		}
		sb.WriteString(lipgloss.NewStyle().Width(sidebarWidth).Render(label))
		sb.WriteString("\n")
	}
	sidebar := strings.TrimRight(sb.String(), "\n")

	paneWidth := m.width - sidebarWidth - 6
	if paneWidth < 30 {
		paneWidth = 30
	}
	header := paneTitleStyle.Render(m.sections[m.cursor].title)
	if warns := warningsFor(m.sections[m.cursor].id, m.state.cfg); len(warns) > 0 {
		header += "\n" + warnStyle.Render("⚠ "+strings.Join(warns, " · "))
	}
	pane := header + "\n\n" + m.activePane().View(paneWidth)

	body := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, "  ", pane)

	footer := "Ctrl+S save · Esc cancel · ? help"
	if m.dirty() {
		footer = "* modified · " + footer
	}
	if m.footer != "" {
		footer = m.footer
	}
	return body + "\n\n" + footer
}

func (m Model) helpView() string {
	return strings.Join([]string{
		"Settings keys",
		"",
		"  ↑/↓ or k/j    move (sidebar or list)",
		"  Tab / l / →   enter section",
		"  Shift+Tab / h back to sidebar",
		"  g / G         first / last section",
		"  a             add entry (lists)",
		"  e / Enter     edit entry (lists)",
		"  d             delete entry (lists)",
		"  Ctrl+S        save all changes",
		"  Esc           close sub-form, then cancel",
		"",
		"Press ? to close this help.",
	}, "\n")
}

func (m Model) FocusedFieldTitle() string {
	if m.paneFocused {
		if t := m.activePane().FocusedFieldTitle(); t != "" {
			return t
		}
	}
	return m.sections[m.cursor].title
}

func (m Model) Footer() string { return m.footer }

// BoolValue returns the current value of a named boolean settings field,
// read straight from the working copy. Convenience for tests and the parent
// status line.
func (m Model) BoolValue(title string) bool {
	switch title {
	case "Local only":
		if p, ok := m.state.cfg.Models.Presets[activePresetNameFor(m.state.cfg)]; ok {
			return p.LocalOnly
		}
		return false
	case "Remote providers allowed":
		return m.state.cfg.Privacy.RemoteProvidersAllowed
	case "Allow network":
		return m.state.cfg.Tools.Shell.AllowNetwork
	case "Allow sudo":
		return m.state.cfg.Tools.Shell.AllowSudo
	case "Allow destructive":
		return m.state.cfg.Tools.Shell.AllowDestructive
	case "Auto-approve shell":
		return m.state.cfg.Tools.Shell.AutoApprove
	}
	return false
}

// activePresetNameFor resolves the implementer preset of the default profile
// (same rule as config.activePresetName, duplicated here because that helper
// is package-private to config).
func activePresetNameFor(cfg config.Config) string {
	profile, ok := cfg.AgentProfiles[cfg.Profile.Default]
	if !ok {
		return ""
	}
	return profile.Roles[routing.RoleImplementer]
}
