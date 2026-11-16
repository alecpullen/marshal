package fleet

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/diffview"
	"marshal/web/bridge/contract"
)

// diffTab is the Diff tab's key, as plan 3 reserved it. A Sessions row has no
// Diff tab: its right panel is the session's own details.
const diffTab = 2

// diffMinus is the header's removed marker: a minus sign, not a hyphen, so it
// carries the same weight as the + beside it.
const diffMinus = "−"

// diffView is the Diff tab's files, cursor, the selected file's diff and its
// scroll, for one agent.
type diffView struct {
	// agent is the agent the view was read for, and count the changed-files
	// count it was read at. Both are set when a read is dispatched, so a
	// second state with the same count reads nothing; either one differing
	// from the shown agent's means the list is stale.
	agent string
	count int
	// files is the agent's changed files, in the bridge's order, and loaded
	// reports that the list's answer has landed.
	files  []contract.DiffFile
	loaded bool
	// cursor indexes files: the file under it is the one whose diff is shown.
	cursor int
	// path is the file the shown diff belongs to, and patch that diff as the
	// bridge sent it. It is kept raw, because it is rendered at the panel's
	// width, which changes with the terminal.
	path  string
	patch string
	// scroll is the first row of the rendered diff the panel shows.
	scroll int
	// err is a failed read's sentence, drawn in the panel: it is a read, not
	// an action, so it never takes the status line.
	err error
}

// diffReadMsg is one diff read's answer.
type diffReadMsg struct {
	agentID string
	path    string // "" for the file list
	result  contract.DiffResult
	err     error
}

// shownAgentID is the ID of the agent the right panel shows: "" for a Sessions
// row, an intake item, or no selection.
func (m Model) shownAgentID() string {
	row, ok := m.selectedRow()
	if !ok || row.session {
		return ""
	}
	return row.id
}

// syncDiff starts the reads the Diff tab needs: the changed-file list when it
// is not the shown agent's, or not its count, and the selected file's diff when
// it is not the one shown. It reads nothing while the tab is not shown, or for
// a row that has no Diff tab. It is called after every state and every key, so
// the tab follows the selection without any of them knowing about it.
func (m *Model) syncDiff() tea.Cmd {
	if m.tab != diffTab {
		return nil
	}
	id := m.shownAgentID()
	if id == "" {
		return nil
	}
	a, ok := m.agentByID(id)
	if !ok {
		return nil
	}
	if m.diff.agent != a.ID || m.diff.count != a.ChangedFiles {
		// The files the tab holds are not this agent's, or not its count:
		// drop them and read the list again.
		m.diff = diffView{agent: a.ID, count: a.ChangedFiles}
		return m.diffCmd(a.ID, "")
	}
	return m.diffFileCmd()
}

// diffFileCmd reads the diff of the file under the cursor, unless the view
// already holds it. It starts no read before the file list has landed.
func (m *Model) diffFileCmd() tea.Cmd {
	if m.diff.agent == "" || len(m.diff.files) == 0 {
		return nil
	}
	path := m.diff.files[clamp(m.diff.cursor, 0, len(m.diff.files)-1)].Path
	if path == m.diff.path {
		return nil
	}
	m.diff.path = path
	m.diff.patch = ""
	m.diff.scroll = 0
	return m.diffCmd(m.diff.agent, path)
}

// diffCmd reads one path's diff — the file list itself when path is "" — in a
// command, bounded by actionTimeout as every call to the fleet is.
func (m Model) diffCmd(agentID, path string) tea.Cmd {
	f := m.fleet
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		result, err := f.Diff(ctx, agentID, path)
		return diffReadMsg{agentID: agentID, path: path, result: result, err: err}
	}
}

// handleDiffRead applies one diff read's answer. An answer for an agent or a
// path the tab no longer shows is dropped: the screen moved on while it was in
// flight.
func (m Model) handleDiffRead(msg diffReadMsg) (tea.Model, tea.Cmd) {
	if m.tab != diffTab || m.diff.agent == "" || m.diff.agent != msg.agentID || m.shownAgentID() != msg.agentID {
		return m, nil
	}
	if msg.path == "" {
		// The file list, whose answer starts the selected file's read.
		m.diff.loaded = msg.err == nil
		m.diff.err = msg.err
		m.diff.files = msg.result.Files
		m.diff.path = ""
		m.diff.patch = ""
		m.diff.scroll = 0
		if n := len(m.diff.files); n == 0 {
			m.diff.cursor = 0
		} else {
			m.diff.cursor = clamp(m.diff.cursor, 0, n-1)
		}
		return m, m.diffFileCmd()
	}
	if msg.path != m.diff.path {
		// The cursor moved to another file while this one was being read.
		return m, nil
	}
	m.diff.err = msg.err
	m.diff.patch = ""
	if msg.err == nil {
		m.diff.patch = msg.result.Diff
	}
	return m, nil
}

// scrollDiff moves the diff's scroll by delta rows. Over-scrolling past the
// end is cut back by the panel, which knows how many rows it has left.
func (m *Model) scrollDiff(delta int) {
	m.diff.scroll = max(m.diff.scroll+delta, 0)
}

// diffLines is the Diff tab: the changed files' totals, the files with the one
// under the cursor marked, and that file's diff from the scroll offset.
func (m Model) diffLines(inner, rows int) []string {
	d := m.diff
	if d.err != nil {
		return []string{m.plain(cut(d.err.Error(), inner))}
	}
	switch {
	case !d.loaded:
		return []string{m.plain(cut("reading the changed files…", inner))}
	case len(d.files) == 0:
		return []string{m.plain("no changed files")}
	}
	lines := []string{cut(m.diffTotals(d.files), inner)}
	for i, f := range d.files {
		lines = append(lines, m.diffFileLine(f, i == d.cursor, inner))
	}
	if strings.TrimSpace(d.patch) == "" {
		return lines[:min(len(lines), max(rows, 0))]
	}
	lines = append(lines, "")
	room := rows - len(lines)
	if room < 1 {
		// No room left under the file list: the diff waits for a taller panel.
		return lines[:min(len(lines), max(rows, 0))]
	}
	body := splitLines(strings.TrimRight(diffview.Render(d.patch, diffview.Options{
		Width: inner,
		Mode:  diffview.ModeUnified,
	}), "\n"))
	// The scroll counts the diff's own rows, so the file list stays put above
	// it. A scroll past the end shows the last row rather than a blank panel.
	if scroll := clamp(d.scroll, 0, max(len(body)-1, 0)); scroll > 0 {
		body = body[scroll:]
	}
	if len(body) > room {
		body = body[:room]
	}
	return append(lines, body...)
}

// diffTotals is the header: "<N> files +<added> −<removed>", with the two
// counts in the theme's added and removed colours.
func (m Model) diffTotals(files []contract.DiffFile) string {
	added, removed := 0, 0
	for _, f := range files {
		added += f.Added
		removed += f.Removed
	}
	if m.dimmed {
		return m.plain(diffTotalsText(len(files), added, removed))
	}
	plus := lipgloss.NewStyle().Foreground(m.th.StatusSuccess).Render("+" + strconv.Itoa(added))
	minus := lipgloss.NewStyle().Foreground(m.th.StatusError).Render(diffMinus + strconv.Itoa(removed))
	return strconv.Itoa(len(files)) + " files " + plus + " " + minus
}

// diffTotalsText is the header as plain text, for a screen that is not
// connected.
func diffTotalsText(n, added, removed int) string {
	return strconv.Itoa(n) + " files +" + strconv.Itoa(added) + " " + diffMinus + strconv.Itoa(removed)
}

// diffFileLine is one changed file: the cursor's mark, its path and its own
// counts. The counts keep their cells and the path takes what is left, so a
// narrow panel truncates the path rather than the count. The row under the
// cursor carries the selection band.
func (m Model) diffFileLine(f contract.DiffFile, selected bool, inner int) string {
	mark := "  "
	if selected {
		mark = glyph.Running + " "
	}
	counts := " +" + strconv.Itoa(f.Added) + " " + diffMinus + strconv.Itoa(f.Removed)
	text := cut(mark+f.Path+counts, inner)
	if room := inner - ansi.StringWidth(mark) - ansi.StringWidth(counts); room >= 8 {
		text = cut(mark+padTo(cut(f.Path, room), room)+counts, inner)
	}
	if selected {
		return chrome.SelectionStyle().Render(text)
	}
	return m.plain(text)
}
