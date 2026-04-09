// internal/tui/filetree.go
// A compact file-tree component for the sidebar.
// Width is fixed at sidebarWidth; height is passed per View call.

package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Directories that are never shown in the tree.
var ftSkip = map[string]bool{
	".git": true, ".idea": true, ".DS_Store": true,
	"node_modules": true, "__pycache__": true,
	".pytest_cache": true, "vendor": true,
	".marshal": true, "dist": true, "build": true, ".next": true,
}

// ── Node ──────────────────────────────────────────────────────────────────────

type ftNode struct {
	name        string
	fullPath    string
	isDir       bool
	depth       int
	isLast      bool   // last sibling at its level
	parentPipes []bool // which ancestor levels still need a vertical pipe
}

// ── Model ─────────────────────────────────────────────────────────────────────

type FileTreeModel struct {
	root     string
	expanded map[string]bool
	nodes    []ftNode // currently visible (expanded) nodes
	cursor   int
}

func newFileTreeModel(root string) FileTreeModel {
	m := FileTreeModel{
		root:     root,
		expanded: make(map[string]bool),
	}
	m.rebuild()
	return m
}

// rebuild recomputes the flat visible node list by DFS through expanded dirs.
func (m *FileTreeModel) rebuild() {
	m.nodes = nil
	m.addLevel(m.root, 0, nil)
}

func (m *FileTreeModel) addLevel(dir string, depth int, parentPipes []bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Filter first
	var visible []os.DirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || ftSkip[name] {
			continue
		}
		visible = append(visible, e)
	}

	sort.Slice(visible, func(i, j int) bool {
		di, dj := visible[i].IsDir(), visible[j].IsDir()
		if di != dj {
			return di // dirs first
		}
		return visible[i].Name() < visible[j].Name()
	})

	for i, e := range visible {
		isLast := i == len(visible)-1
		name := e.Name()
		full := filepath.Join(dir, name)

		// Build the parentPipes slice for children:
		// pass down whether this level still has a pipe running through it.
		childPipes := make([]bool, len(parentPipes)+1)
		copy(childPipes, parentPipes)
		childPipes[len(parentPipes)] = !isLast

		m.nodes = append(m.nodes, ftNode{
			name:        name,
			fullPath:    full,
			isDir:       e.IsDir(),
			depth:       depth,
			isLast:      isLast,
			parentPipes: parentPipes,
		})
		if e.IsDir() && m.expanded[full] {
			m.addLevel(full, depth+1, childPipes)
		}
	}
}

// ── Bubble Tea ────────────────────────────────────────────────────────────────

func (m FileTreeModel) Update(msg tea.Msg) (FileTreeModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch keyMsg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.nodes)-1 {
			m.cursor++
		}
	case "enter":
		if m.cursor < len(m.nodes) {
			n := m.nodes[m.cursor]
			if n.isDir {
				m.expanded[n.fullPath] = !m.expanded[n.fullPath]
				m.rebuild()
				if m.cursor >= len(m.nodes) {
					m.cursor = max(0, len(m.nodes)-1)
				}
			} else {
				path := n.fullPath
				return m, func() tea.Msg { return OpenFileMsg{Path: path} }
			}
		}
	case " ":
		if m.cursor < len(m.nodes) && m.nodes[m.cursor].isDir {
			path := m.nodes[m.cursor].fullPath
			m.expanded[path] = !m.expanded[path]
			m.rebuild()
			if m.cursor >= len(m.nodes) {
				m.cursor = max(0, len(m.nodes)-1)
			}
		}
	}
	return m, nil
}

func (m FileTreeModel) View(h int) string {
	if h <= 0 {
		return ""
	}
	w := sidebarWidth

	// Scroll: keep cursor visible
	scrollTop := 0
	if len(m.nodes) > h && m.cursor >= h {
		scrollTop = m.cursor - h + 1
	}

	var lines []string
	end := scrollTop + h
	if end > len(m.nodes) {
		end = len(m.nodes)
	}
	for i := scrollTop; i < end; i++ {
		lines = append(lines, m.renderNode(m.nodes[i], i == m.cursor, w))
	}

	// Pad empty rows
	blank := lipgloss.NewStyle().Width(w).Background(colBg).Render("")
	for len(lines) < h {
		lines = append(lines, blank)
	}

	return strings.Join(lines, "\n")
}

func (m FileTreeModel) renderNode(n ftNode, active bool, w int) string {
	// Build the tree prefix: vertical pipes for ancestor levels, then connector
	var prefix strings.Builder

	// Pipe columns for each ancestor depth
	for _, hasPipe := range n.parentPipes {
		if hasPipe {
			prefix.WriteString("│ ")
		} else {
			prefix.WriteString("  ")
		}
	}

	// Connector for this node
	if n.isLast {
		prefix.WriteString("└─")
	} else {
		prefix.WriteString("├─")
	}

	// Directory open/close indicator or file spacer
	var icon string
	if n.isDir {
		if m.expanded[n.fullPath] {
			icon = "▾ "
		} else {
			icon = "▸ "
		}
	} else {
		icon = " "
	}

	full := prefix.String() + icon
	nameW := max(0, w-len([]rune(full)))
	text := full + truncate(n.name, nameW)

	// Style the pipe/connector portion in dim colour, name in normal colour
	pipeStyle := lipgloss.NewStyle().Foreground(colBr3).Background(colBg)
	pipeStr := prefix.String()

	switch {
	case active:
		activeStyle := lipgloss.NewStyle().
			Foreground(colTx).Background(colBg2).Bold(true).
			Width(w)
		return activeStyle.Render(text)
	case n.isDir:
		// Render pipes dim, dir name in blue
		dirStyle := lipgloss.NewStyle().Foreground(colBl).Background(colBg)
		nameStr := icon + truncate(n.name, nameW)
		rendered := pipeStyle.Render(pipeStr) + dirStyle.Render(nameStr)
		// Pad to full width
		renderedW := lipgloss.Width(rendered)
		if renderedW < w {
			rendered += lipgloss.NewStyle().Background(colBg).Render(strings.Repeat(" ", w-renderedW))
		}
		return rendered
	default:
		// Render pipes dim, file name muted
		fileStyle := lipgloss.NewStyle().Foreground(colTx3).Background(colBg)
		nameStr := icon + truncate(n.name, nameW)
		rendered := pipeStyle.Render(pipeStr) + fileStyle.Render(nameStr)
		renderedW := lipgloss.Width(rendered)
		if renderedW < w {
			rendered += lipgloss.NewStyle().Background(colBg).Render(strings.Repeat(" ", w-renderedW))
		}
		return rendered
	}
}
