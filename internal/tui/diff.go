// internal/tui/diff.go
// Diff viewer overlay: shows the git patch for a completed task.
// Uses a viewport for scrolling; tab/shift+tab cycle between changed files.

package tui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Diff line types ───────────────────────────────────────────────────────────

type diffLineKind int

const (
	diffHeader   diffLineKind = iota // "diff --git a/… b/…"
	diffFileLine                     // "--- a/…" or "+++ b/…"
	diffHunk                         // "@@ … @@"
	diffAdd                          // "+…"
	diffDel                          // "-…"
	diffContext                      // " …" (unchanged)
	diffMsg                          // informational / error
)

type diffLine struct {
	kind    diffLineKind
	content string
}

// ── Messages ──────────────────────────────────────────────────────────────────

type diffReadyMsg struct {
	lines      []diffLine
	fileNames  []string
	fileStarts []int // line indices in `lines` where each file section begins
	err        error
}

// ── Model ─────────────────────────────────────────────────────────────────────

type DiffModel struct {
	taskID   string
	sha      string
	repoRoot string

	lines      []diffLine
	fileNames  []string
	fileStarts []int
	fileIdx    int // currently highlighted file tab

	viewport viewport.Model
	ready    bool
	err      error
}

func newDiffModel(taskID, sha, repoRoot string) DiffModel {
	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().Background(colBg)
	return DiffModel{
		taskID:   taskID,
		sha:      sha,
		repoRoot: repoRoot,
		viewport: vp,
	}
}

func (m DiffModel) Init() tea.Cmd {
	if m.sha == "" {
		return func() tea.Msg {
			return diffReadyMsg{
				lines: []diffLine{{kind: diffMsg, content: "no completed task with a commit to diff"}},
			}
		}
	}
	sha := m.sha
	root := m.repoRoot
	return func() tea.Msg {
		// git diff SHA^1 SHA gives the patch introduced by the commit
		out, err := exec.Command("git", "-C", root, "diff", sha+"^1", sha).Output()
		if err != nil {
			// Fallback: maybe it's the first commit
			out, err = exec.Command("git", "-C", root, "show", "--no-commit-id", "-p", sha).Output()
		}
		if err != nil {
			return diffReadyMsg{err: fmt.Errorf("git diff: %w", err)}
		}
		lines, names, starts := parseDiff(string(out))
		return diffReadyMsg{lines: lines, fileNames: names, fileStarts: starts}
	}
}

func (m DiffModel) Update(msg tea.Msg) (DiffModel, tea.Cmd) {
	switch msg := msg.(type) {

	case diffReadyMsg:
		m.err = msg.err
		m.lines = msg.lines
		m.fileNames = msg.fileNames
		m.fileStarts = msg.fileStarts
		m.ready = true
		m.viewport.SetContent(m.renderLines())
		m.viewport.GotoTop()
		return m, nil

	case tea.WindowSizeMsg:
		const hintH = 2                    // tab strip + hint bar
		m.viewport.Width = msg.Width - 4   // modal padding
		m.viewport.Height = msg.Height - 4 // modal border+padding
		if m.viewport.Height > hintH {
			m.viewport.Height -= hintH
		}
		if m.ready {
			m.viewport.SetContent(m.renderLines())
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "tab":
			if len(m.fileNames) > 0 {
				m.fileIdx = (m.fileIdx + 1) % len(m.fileNames)
				m.jumpToFile(m.fileIdx)
			}
		case "shift+tab":
			if len(m.fileNames) > 0 {
				m.fileIdx = (m.fileIdx - 1 + len(m.fileNames)) % len(m.fileNames)
				m.jumpToFile(m.fileIdx)
			}
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *DiffModel) jumpToFile(idx int) {
	if idx < len(m.fileStarts) {
		// Count rendered lines before this file's start
		linesBefore := 0
		for i := 0; i < m.fileStarts[idx] && i < len(m.lines); i++ {
			linesBefore++
		}
		m.viewport.SetYOffset(linesBefore)
	}
}

func (m DiffModel) View(w, h int) string {
	innerW := w - 8 // border (2) + padding (2*2) on each side
	if innerW < 10 {
		innerW = 10
	}
	innerH := h - 6
	if innerH < 2 {
		innerH = 2
	}

	// ── File tab strip ────────────────────────────────────────────────────────
	tabStrip := m.renderTabStrip(innerW)

	// ── Hint bar ──────────────────────────────────────────────────────────────
	hint := stylePromptHint.Width(innerW).Render(
		"tab next file · shift+tab prev · ↑↓ scroll · q/esc close",
	)

	// ── Viewport ──────────────────────────────────────────────────────────────
	vpH := innerH - 2 // tab + hint
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.Width = innerW
	m.viewport.Height = vpH
	if !m.ready {
		m.viewport.SetContent(
			lipgloss.NewStyle().Foreground(colTx3).Italic(true).Render("loading diff…"),
		)
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		tabStrip,
		m.viewport.View(),
		hint,
	)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBr3).
		Background(colBg).
		Padding(1, 2).
		Width(w - 4).
		Height(h - 2).
		Render(content)
}

func (m DiffModel) renderTabStrip(w int) string {
	if len(m.fileNames) == 0 {
		title := "diff"
		if m.taskID != "" {
			title += " · " + m.taskID
		}
		return lipgloss.NewStyle().Foreground(colTx).Bold(true).Width(w).Render(title)
	}

	var tabs []string
	for i, name := range m.fileNames {
		// Show only the base filename to save space
		parts := strings.Split(name, "/")
		short := parts[len(parts)-1]
		if i == m.fileIdx {
			tabs = append(tabs, lipgloss.NewStyle().
				Foreground(colBg).Background(colBl).
				Padding(0, 1).Bold(true).Render(short))
		} else {
			tabs = append(tabs, lipgloss.NewStyle().
				Foreground(colTx3).Background(colBg2).
				Padding(0, 1).Render(short))
		}
	}
	strip := strings.Join(tabs, " ")
	stripW := lipgloss.Width(strip)
	if stripW < w {
		strip += lipgloss.NewStyle().Width(w - stripW).Background(colBg).Render("")
	}
	return strip
}

// renderLines converts parsed diff lines to styled strings for the viewport.
func (m DiffModel) renderLines() string {
	var sb strings.Builder
	for _, l := range m.lines {
		sb.WriteString(m.renderDiffLine(l))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (m DiffModel) renderDiffLine(l diffLine) string {
	switch l.kind {
	case diffAdd:
		return lipgloss.NewStyle().Foreground(colGr).Render(l.content)
	case diffDel:
		return lipgloss.NewStyle().Foreground(colRd).Render(l.content)
	case diffHunk:
		return lipgloss.NewStyle().Foreground(colPu).Render(l.content)
	case diffHeader, diffFileLine:
		return lipgloss.NewStyle().Foreground(colBl).Bold(true).Render(l.content)
	case diffMsg:
		return lipgloss.NewStyle().Foreground(colTx3).Italic(true).Render(l.content)
	default:
		return lipgloss.NewStyle().Foreground(colTx3).Render(l.content)
	}
}

// ── Parser ────────────────────────────────────────────────────────────────────

func parseDiff(raw string) (lines []diffLine, fileNames []string, fileStarts []int) {
	for _, raw := range strings.Split(raw, "\n") {
		l := classifyLine(raw)
		if l.kind == diffHeader {
			// Extract filename from "diff --git a/foo b/foo"
			parts := strings.Fields(raw)
			if len(parts) >= 3 {
				name := strings.TrimPrefix(parts[len(parts)-1], "b/")
				fileNames = append(fileNames, name)
				fileStarts = append(fileStarts, len(lines))
			}
		}
		lines = append(lines, l)
	}
	return
}

func classifyLine(s string) diffLine {
	switch {
	case strings.HasPrefix(s, "diff --git"):
		return diffLine{diffHeader, s}
	case strings.HasPrefix(s, "--- ") || strings.HasPrefix(s, "+++ "):
		return diffLine{diffFileLine, s}
	case strings.HasPrefix(s, "@@"):
		return diffLine{diffHunk, s}
	case strings.HasPrefix(s, "+"):
		return diffLine{diffAdd, s}
	case strings.HasPrefix(s, "-"):
		return diffLine{diffDel, s}
	default:
		return diffLine{diffContext, s}
	}
}
