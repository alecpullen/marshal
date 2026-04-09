// internal/tui/sidebar.go
// The sidebar has two panes:
//   top  — FileTreeModel: navigable project file explorer
//   bottom — task queue: running/queued/completed task list

package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Sub-focus ─────────────────────────────────────────────────────────────────

type subFocusKind int

const (
	subFocusNone  subFocusKind = iota
	subFocusFiles              // file tree has focus
	subFocusTasks              // task queue has focus
)

// ── Task state ────────────────────────────────────────────────────────────────

type taskState int

const (
	taskQueued    taskState = iota
	taskRunning             // currently executing the loop
	taskCancelling          // cancellation requested, cleaning up
	taskPass                // loop completed, PASS
	taskFail                // loop completed, FAIL
)

type sidebarTask struct {
	id          string
	description string
	state       taskState
	rounds      int
	completedAt time.Time
}

// ── Model ─────────────────────────────────────────────────────────────────────

type SidebarModel struct {
	fileTree   FileTreeModel
	tasks      []sidebarTask
	taskCursor int

	subFocus subFocusKind
	blinkOn  bool
	JumpTo   string
}

func newSidebarModel(root string) SidebarModel {
	return SidebarModel{
		fileTree: newFileTreeModel(root),
	}
}

// SetSubFocus tells the sidebar which section is active.
func (m *SidebarModel) SetSubFocus(f subFocusKind) {
	m.subFocus = f
}

// ── Public API called by app.go ───────────────────────────────────────────────

func (m *SidebarModel) Enqueue(description string) tea.Cmd {
	id := fmt.Sprintf("task-%d", len(m.tasks)+1)
	m.tasks = append(m.tasks, sidebarTask{
		id:          id,
		description: description,
		state:       taskQueued,
	})
	return nil
}

func (m *SidebarModel) SetRunning(id string) {
	for i := range m.tasks {
		if m.tasks[i].id == id {
			m.tasks[i].state = taskRunning
			return
		}
	}
}

func (m *SidebarModel) SetCancelling(id string) {
	for i := range m.tasks {
		if m.tasks[i].id == id {
			m.tasks[i].state = taskCancelling
			return
		}
	}
}

func (m *SidebarModel) SetComplete(id string, passed bool, rounds int) {
	for i := range m.tasks {
		if m.tasks[i].id == id {
			if passed {
				m.tasks[i].state = taskPass
			} else {
				m.tasks[i].state = taskFail
			}
			m.tasks[i].rounds = rounds
			m.tasks[i].completedAt = time.Now()
			return
		}
	}
}

// ── Bubble Tea ────────────────────────────────────────────────────────────────

type blinkMsg struct{}

func (m SidebarModel) Init() tea.Cmd {
	return tea.Tick(600*time.Millisecond, func(t time.Time) tea.Msg { return blinkMsg{} })
}

func (m SidebarModel) Update(msg tea.Msg) (SidebarModel, tea.Cmd) {
	switch msg := msg.(type) {
	case blinkMsg:
		m.blinkOn = !m.blinkOn
		return m, tea.Tick(600*time.Millisecond, func(t time.Time) tea.Msg { return blinkMsg{} })

	case tea.KeyMsg:
		switch m.subFocus {
		case subFocusFiles:
			switch msg.String() {
			case "esc":
				// Tab handles focus cycling; Esc is a no-op here
			case "ctrl+e":
				// Open selected file in external editor (skip inline editor)
				if m.fileTree.cursor < len(m.fileTree.nodes) {
					n := m.fileTree.nodes[m.fileTree.cursor]
					if !n.isDir {
						path := n.fullPath
						return m, func() tea.Msg { return OpenFileExternalMsg{Path: path} }
					}
				}
			default:
				var cmd tea.Cmd
				m.fileTree, cmd = m.fileTree.Update(msg)
				return m, cmd
			}
		case subFocusTasks:
			switch msg.String() {
			case "up", "k":
				if m.taskCursor > 0 {
					m.taskCursor--
				}
			case "down", "j":
				if m.taskCursor < len(m.tasks)-1 {
					m.taskCursor++
				}
			case "enter":
				if m.taskCursor < len(m.tasks) {
					id := m.tasks[m.taskCursor].id
					return m, func() tea.Msg { return JumpToTaskMsg{ID: id} }
				}
			}
		}
	}
	return m, nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m SidebarModel) View(h int) string {
	// Split height: each section gets half, minus 2 rows per section header.
	// Layout: [files title] [files rule] [tree content]
	//         [tasks title] [tasks rule] [task content]
	const headerRows = 2 // title + rule per section
	content := h - headerRows*2
	if content < 0 {
		content = 0
	}
	treeH := content / 2
	taskH := content - treeH

	lines := []string{
		m.renderSectionHeader("files", m.subFocus == subFocusFiles),
		m.fileTree.View(treeH),
		m.renderSectionHeader("tasks", m.subFocus == subFocusTasks),
		m.renderTasks(taskH),
	}

	return styleSidebar.Height(h).Render(strings.Join(lines, "\n"))
}

func (m SidebarModel) renderSectionHeader(label string, active bool) string {
	var titleStyle lipgloss.Style
	if active {
		titleStyle = lipgloss.NewStyle().
			Foreground(colBl).Background(colBg).
			Width(sidebarWidth - 2).PaddingLeft(1).Bold(true)
	} else {
		titleStyle = styleSidebarTitle
	}
	title := titleStyle.Render(label)
	rule  := styleSidebarRule.Render(strings.Repeat("─", sidebarWidth))
	return title + "\n" + rule
}

func (m SidebarModel) renderTasks(h int) string {
	var lines []string

	// Find divider index (first non-completed task)
	dividerIdx := -1
	for i, t := range m.tasks {
		if t.state == taskRunning || t.state == taskQueued {
			dividerIdx = i
			break
		}
	}

	for i, task := range m.tasks {
		if i == dividerIdx && dividerIdx > 0 {
			lines = append(lines,
				styleSidebarRule.Render(strings.Repeat("─", sidebarWidth)),
				"",
			)
		}
		active := m.subFocus == subFocusTasks && i == m.taskCursor
		dot    := m.dotForState(task.state)
		desc   := truncate(task.description, sidebarWidth-3)

		var nameStyle lipgloss.Style
		if active {
			nameStyle = lipgloss.NewStyle().
				Foreground(colTx).Background(colBg2).Bold(true).
				Width(sidebarWidth - 2)
		} else {
			nameStyle = styleSidebarItemName
		}
		lines = append(lines,
			nameStyle.Render(dot+" "+desc),
			styleSidebarMeta.Render(m.metaForTask(task)),
			"",
		)
	}

	// Pad to h lines
	blank := lipgloss.NewStyle().Width(sidebarWidth).Background(colBg).Render("")
	all   := strings.Join(lines, "\n")
	rows  := strings.Split(all, "\n")
	for len(rows) < h {
		rows = append(rows, blank)
	}

	return strings.Join(rows[:h], "\n")
}

func (m SidebarModel) dotForState(s taskState) string {
	switch s {
	case taskPass:
		return styleSidebarDotPass.Render("●")
	case taskFail:
		return styleSidebarDotFail.Render("●")
	case taskRunning:
		if m.blinkOn {
			return styleSidebarDotRunning.Render("◎")
		}
		return styleSidebarDotRunning.Render("○")
	case taskCancelling:
		// Blinking amber × to indicate cancellation in progress
		if m.blinkOn {
			return styleSidebarDotRunning.Render("✕")
		}
		return styleSidebarDotRunning.Render("×")
	default:
		return styleSidebarDotQueued.Render("○")
	}
}

func (m SidebarModel) metaForTask(t sidebarTask) string {
	switch t.state {
	case taskPass:
		return lipgloss.NewStyle().Foreground(colTx3).Render("pass  r" + fmt.Sprint(t.rounds))
	case taskFail:
		return lipgloss.NewStyle().Foreground(colRd).Render("fail  r" + fmt.Sprint(t.rounds))
	case taskRunning:
		return lipgloss.NewStyle().Foreground(colAm).Render("running r" + fmt.Sprint(t.rounds+1))
	case taskCancelling:
		return lipgloss.NewStyle().Foreground(colAm).Render("cancelling")
	default:
		return lipgloss.NewStyle().Foreground(colTx3).Render("queued")
	}
}
