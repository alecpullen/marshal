package settings

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	flCursorStyle = lipgloss.NewStyle().Bold(true).Background(settingsTheme.BGSelection)
	flTitleStyle  = lipgloss.NewStyle().Foreground(settingsTheme.FGDefault)
	flValueStyle  = lipgloss.NewStyle().Foreground(settingsTheme.AccentSecondary)
	flDescStyle   = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
	flErrStyle    = lipgloss.NewStyle().Foreground(settingsTheme.StatusError)
	flOnStyle     = lipgloss.NewStyle().Foreground(settingsTheme.StatusSuccess)
	flOffStyle    = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
	flMoreStyle   = lipgloss.NewStyle().Foreground(settingsTheme.FGMuted)
)

// fieldList renders and edits a vertical list of typed rows. It is the one
// widget behind every settings pane and drill-down frame.
type fieldList struct {
	fields func() []*field
	rows   []*field
	cursor int
	width  int
	height int

	// inline scalar edit
	editing bool
	input   textinput.Model
	errMsg  string

	// enum picker (inline dropdown under the row)
	picking bool
	pickIdx int

	// collection add prompt (wired by pane frames in Task 4)
	adding    bool
	keyPrompt string
	onAdd     func(string) error
	keyInput  textinput.Model

	// drill request picked up by the owning pane after Update
	pushRequest *frame
}

func newFieldList(fields func() []*field) *fieldList {
	ti := textinput.New()
	ti.SetVirtualCursor(true)
	ki := textinput.New()
	ki.SetVirtualCursor(true)
	fl := &fieldList{fields: fields, input: ti, keyInput: ki}
	fl.Refresh()
	return fl
}

func (fl *fieldList) Refresh() {
	fl.rows = fl.fields()
	if fl.cursor >= len(fl.rows) {
		fl.cursor = len(fl.rows) - 1
	}
	if fl.cursor < 0 {
		fl.cursor = 0
	}
}

func (fl *fieldList) Rows() []*field { fl.Refresh(); return fl.rows }
func (fl *fieldList) Cursor() int    { return fl.cursor }

func (fl *fieldList) SetCursor(i int) {
	fl.Refresh()
	if i >= 0 && i < len(fl.rows) {
		fl.cursor = i
	}
}

func (fl *fieldList) CursorRow() *field {
	fl.Refresh()
	if len(fl.rows) == 0 {
		return nil
	}
	return fl.rows[fl.cursor]
}

func (fl *fieldList) SetSize(w, h int) { fl.width, fl.height = w, h }

func (fl *fieldList) Editing() bool { return fl.editing || fl.picking || fl.adding }

func (fl *fieldList) CancelEdit() {
	fl.editing = false
	fl.picking = false
	fl.adding = false
	fl.errMsg = ""
	fl.input.Blur()
	fl.keyInput.Blur()
}

// TakePushRequest returns and clears the frame a drill row asked to open.
func (fl *fieldList) TakePushRequest() *frame {
	f := fl.pushRequest
	fl.pushRequest = nil
	return f
}

func (fl *fieldList) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	fl.Refresh()
	if fl.adding {
		return fl.updateAdd(k)
	}
	if fl.editing {
		return fl.updateEdit(k)
	}
	if fl.picking {
		fl.updatePick(k)
		return nil
	}
	row := fl.CursorRow()
	switch k.String() {
	case "up", "k":
		if fl.cursor > 0 {
			fl.cursor--
		}
	case "down", "j":
		if fl.cursor < len(fl.rows)-1 {
			fl.cursor++
		}
	case "g":
		fl.cursor = 0
	case "G":
		fl.cursor = len(fl.rows) - 1
	case "space":
		if row != nil && row.kind == kindToggle {
			row.setBool(!row.getBool())
		}
	case "left", "right":
		if row != nil && row.kind == kindEnum {
			fl.cycleEnum(row, k.String() == "right")
		}
	case "enter", "e":
		fl.openRow(row)
	case "a":
		if fl.onAdd != nil {
			if fl.keyPrompt == "" {
				if err := fl.onAdd(""); err != nil {
					fl.errMsg = err.Error()
					return nil
				}
				fl.Refresh()
				fl.cursor = fl.findAddedRow("")
				return nil
			}
			fl.adding = true
			fl.errMsg = ""
			fl.keyInput.SetValue("")
			fl.keyInput.Focus()
		}
	case "d":
		if row != nil && row.del != nil {
			row.del()
			fl.Refresh()
		}
	}
	return nil
}

func (fl *fieldList) openRow(row *field) {
	if row == nil {
		return
	}
	switch row.kind {
	case kindToggle:
		row.setBool(!row.getBool())
	case kindScalar:
		if row.setStr == nil {
			return // read-only
		}
		fl.editing = true
		fl.errMsg = ""
		if row.masked {
			fl.input.SetValue("")
		} else {
			fl.input.SetValue(row.getStr())
			fl.input.CursorEnd()
		}
		fl.input.Focus()
	case kindEnum:
		fl.picking = true
		i := indexOf(row.options(), row.getStr())
		if i < 0 {
			i = 0
		}
		fl.pickIdx = i
	case kindDrill:
		fl.pushRequest = row.build()
	}
}

func (fl *fieldList) cycleEnum(row *field, forward bool) {
	opts := row.options()
	if len(opts) == 0 {
		return
	}
	i := indexOf(opts, row.getStr())
	if i < 0 {
		i = 0
	}
	if forward {
		i = (i + 1) % len(opts)
	} else {
		i = (i - 1 + len(opts)) % len(opts)
	}
	if err := row.setStr(opts[i]); err != nil {
		fl.errMsg = err.Error()
	}
}

func (fl *fieldList) updateEdit(k tea.KeyPressMsg) tea.Cmd {
	row := fl.CursorRow()
	if row == nil {
		fl.CancelEdit()
		return nil
	}
	switch k.String() {
	case "enter":
		val := strings.TrimSpace(fl.input.Value())
		if row.masked && val == "" {
			fl.CancelEdit() // empty keeps the stored secret
			return nil
		}
		if err := row.setStr(val); err != nil {
			fl.errMsg = err.Error()
			return nil
		}
		fl.CancelEdit()
		return nil
	case "esc":
		fl.CancelEdit()
		return nil
	}
	var cmd tea.Cmd
	fl.input, cmd = fl.input.Update(k)
	return cmd
}

func (fl *fieldList) updatePick(k tea.KeyPressMsg) {
	row := fl.CursorRow()
	if row == nil {
		fl.CancelEdit()
		return
	}
	opts := row.options()
	switch k.String() {
	case "up", "k":
		if fl.pickIdx > 0 {
			fl.pickIdx--
		}
	case "down", "j":
		if fl.pickIdx < len(opts)-1 {
			fl.pickIdx++
		}
	case "enter":
		if fl.pickIdx >= 0 && fl.pickIdx < len(opts) {
			if err := row.setStr(opts[fl.pickIdx]); err != nil {
				fl.errMsg = err.Error()
				return
			}
		}
		fl.picking = false
	case "esc":
		fl.picking = false
	}
}

func (fl *fieldList) updateAdd(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "enter":
		newKey := strings.TrimSpace(fl.keyInput.Value())
		if err := fl.onAdd(newKey); err != nil {
			fl.errMsg = err.Error()
			return nil
		}
		fl.CancelEdit()
		fl.Refresh()
		fl.cursor = fl.findAddedRow(newKey)
		return nil
	case "esc":
		fl.CancelEdit()
		return nil
	}
	var cmd tea.Cmd
	fl.keyInput, cmd = fl.keyInput.Update(k)
	return cmd
}

// findAddedRow returns the index of the row whose id ends with "."+newKey,
// or whose title equals newKey (listDrill case). If newKey is empty or no
// match is found, it falls back to the last row index.
func (fl *fieldList) findAddedRow(newKey string) int {
	if newKey != "" {
		suffix := "." + newKey
		for i, row := range fl.rows {
			if strings.HasSuffix(row.id, suffix) {
				return i
			}
		}
		// No id match: try title match (listDrill uses numeric ids).
		for i, row := range fl.rows {
			if row.title == newKey {
				return i
			}
		}
	}
	if len(fl.rows) == 0 {
		return 0
	}
	return len(fl.rows) - 1
}

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// valueCell renders the right-hand value for a row.
func (fl *fieldList) valueCell(row *field, isCursor bool) string {
	switch row.kind {
	case kindToggle:
		if row.getBool() {
			return flOnStyle.Render("on ●")
		}
		return flOffStyle.Render("off ○")
	case kindScalar:
		if fl.editing && isCursor {
			return fl.input.View()
		}
		v := row.getStr()
		if row.masked {
			v = maskKey(v)
		}
		if v == "" {
			v = "—"
		}
		return flValueStyle.Render(v)
	case kindEnum:
		return flValueStyle.Render(row.getStr() + " ▾")
	case kindDrill:
		return flValueStyle.Render(row.summary() + " ›")
	}
	return ""
}

// View renders the list clipped to height, keeping the cursor row visible
// and adding ↑/↓ more indicators when clipped.
func (fl *fieldList) View() string {
	fl.Refresh()
	var lines []string
	cursorLine := 0
	if len(fl.rows) == 0 && !fl.adding {
		empty := "  (empty"
		if fl.onAdd != nil {
			empty += " — press a to add"
		}
		lines = append(lines, flDescStyle.Render(empty+")"))
	}
	for i, row := range fl.rows {
		isCursor := i == fl.cursor
		marker := "  "
		if isCursor {
			marker = "▸ "
		}
		val := fl.valueCell(row, isCursor)
		title := row.title
		gap := fl.width - lipgloss.Width(marker) - lipgloss.Width(title) - lipgloss.Width(val)
		if gap < 1 {
			gap = 1
		}
		line := marker + flTitleStyle.Render(title) + strings.Repeat(" ", gap) + val
		if isCursor {
			cursorLine = len(lines)
			line = flCursorStyle.Render(marker+title) + strings.Repeat(" ", gap) + val
		}
		lines = append(lines, line)
		if isCursor && fl.errMsg != "" {
			lines = append(lines, "    "+flErrStyle.Render("⚠ "+fl.errMsg))
		}
		if isCursor && row.desc != "" && !fl.Editing() {
			lines = append(lines, "    "+flDescStyle.Render(row.desc))
		}
		if isCursor && fl.picking {
			for j, opt := range row.options() {
				pm := "    "
				if j == fl.pickIdx {
					pm = "  ▸ "
				}
				lines = append(lines, pm+flValueStyle.Render(opt))
			}
		}
	}
	if fl.adding {
		lines = append(lines, "▸ "+fl.keyPrompt+": "+fl.keyInput.View())
		if fl.errMsg != "" {
			lines = append(lines, "    "+flErrStyle.Render("⚠ "+fl.errMsg))
		}
	}
	return clipLines(lines, cursorLine, fl.height)
}

// clipLines windows lines to at most height rows, keeping focusLine visible,
// with ↑/↓ more indicators occupying the first/last row when clipped.
func clipLines(lines []string, focusLine, height int) string {
	if height <= 0 || len(lines) <= height {
		return strings.Join(lines, "\n")
	}
	inner := height - 2 // reserve rows for the two indicators
	if inner < 1 {
		inner = 1
	}
	start := focusLine - inner/2
	if start < 0 {
		start = 0
	}
	if start+inner > len(lines) {
		start = len(lines) - inner
	}
	out := make([]string, 0, height)
	if start > 0 {
		out = append(out, flMoreStyle.Render("  ↑ more"))
	} else {
		out = append(out, "")
	}
	out = append(out, lines[start:start+inner]...)
	if start+inner < len(lines) {
		out = append(out, flMoreStyle.Render("  ↓ more"))
	}
	if len(out) > height {
		out = out[:height]
	}
	return strings.Join(out, "\n")
}
