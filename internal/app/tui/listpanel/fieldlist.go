// Package listpanel is the TUI's shared list engine: a row model, cursor and
// scroll handling, section headers, action rows, drill-in rows, nested
// frames, and filtering hooks. settings/ is its config-specific consumer.
package listpanel

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
)

func flTitleStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGDefault) }
func flValueStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().AccentSecondary)
}
func DescStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }
func flErrStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().StatusError) }
func flWarnStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Current().StatusWarning)
}
func flOnStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(theme.Current().StatusSuccess) }
func flOffStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.Current().FGMuted) }
func flHeaderStyle() lipgloss.Style {
	if theme.IsMonochrome() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Bold(true).Foreground(theme.Current().AccentSecondary)
}

// FieldList renders and edits a vertical list of typed rows. It is the one
// widget behind every settings pane and drill-down frame.
type FieldList struct {
	Fields func() []*Field
	rows   []*Field
	cursor int
	width  int
	height int

	// inline scalar edit
	editing bool
	input   textfield.Model
	ErrMsg  string

	// committed reports whether the most recent Update call actually
	// invoked a field setter successfully (toggle, enum cycle/pick, scalar
	// edit confirm, collection add, or paste) as opposed to pure cursor
	// navigation or filter typing. It is reset at the top of every Update
	// call. BrowserPanel uses it to decide whether a no-diff Update is a
	// cheap navigation no-op or an explicit commit gesture eligible to
	// retry a previously failed save.
	committed bool

	// enum picker (inline dropdown under the row)
	picking bool
	pickIdx int

	// collection add prompt (wired by pane frames in Task 4)
	adding    bool
	KeyPrompt string
	OnAdd     func(string) error
	keyInput  textfield.Model

	// OnAddMsg is an alternative to OnAdd: when set, pressing a emits a
	// tea.Cmd that returns the message from this function instead of
	// opening an inline key prompt. Used by the Providers collection to
	// emit OpenConnectMsg.
	OnAddMsg func() tea.Msg

	// drill request picked up by the owning pane after Update
	pushRequest *Frame

	// picker overlay request picked up by the owning pane after Update
	pushPicker *pickerRequest

	yankedData any

	// descSuppressed hides the inline desc line under the cursor row; the
	// two-column browser renders desc in its detail pane instead.
	descSuppressed bool

	// groupDividers draws KindHeader rows as chrome dividers (a rule across
	// the frame) instead of a styled, padded line. Only single-column layouts
	// turn it on: in two columns the rule would erase the detail pane's text
	// on its row.
	groupDividers bool

	// now is the list's clock. It bounds the delete arm window; tests inject
	// it to step past the window, production leaves it nil for time.Now.
	now func() time.Time

	// deleteArmed is true while a first d has armed a row for deletion;
	// deleteArmedID names that row by identity and deleteArmedAt when it
	// armed. The second d on that same row deletes; any other keystroke, a
	// cursor move off the row, or the arm window expiring disarms it (AUD-13).
	deleteArmed   bool
	deleteArmedID string
	deleteArmedAt time.Time
}

// deleteArmWindow bounds how long a first d keeps a row armed for the second
// press that deletes it. It mirrors the TUI's Ctrl+C arm window (model's
// ctrlCWindow): long enough that the second press is deliberate, short enough
// that a stray d a moment later is not read as one.
const deleteArmWindow = 4 * time.Second

// clock is the list's time source, defaulting to time.Now.
func (fl *FieldList) clock() time.Time {
	if fl.now != nil {
		return fl.now()
	}
	return time.Now()
}

// armDelete arms row for a confirming second d.
func (fl *FieldList) armDelete(row *Field) {
	fl.deleteArmed = true
	fl.deleteArmedID = row.ID
	fl.deleteArmedAt = fl.clock()
}

// disarmDelete clears a pending delete arm. Every path that moves the cursor,
// takes another key, or leaves the frame goes through it, so an arm can never
// outlive the gesture that set it.
func (fl *FieldList) disarmDelete() {
	fl.deleteArmed = false
	fl.deleteArmedID = ""
	fl.deleteArmedAt = time.Time{}
}

// deleteArmedOn reports whether row is the armed row and the arm has not
// expired. An expired arm is cleared on the way out.
func (fl *FieldList) deleteArmedOn(row *Field) bool {
	if !fl.deleteArmed || row == nil || row.ID != fl.deleteArmedID {
		return false
	}
	if fl.clock().Sub(fl.deleteArmedAt) > deleteArmWindow {
		fl.disarmDelete()
		return false
	}
	return true
}

// DeleteArmed reports whether the cursor row is armed for a confirming second
// d. The owning surface renders it so the first press is never silent.
func (fl *FieldList) DeleteArmed() bool {
	fl.Refresh()
	if len(fl.rows) == 0 || !fl.deleteArmed {
		return false
	}
	return fl.deleteArmedOn(fl.rows[fl.cursor])
}

// NewFieldList creates a FieldList with the given field provider.
func NewFieldList(fields func() []*Field) *FieldList {
	ti := textfield.New()
	ti.SetVirtualCursor(true)
	ki := textfield.New()
	ki.SetVirtualCursor(true)
	fl := &FieldList{Fields: func() []*Field { return fields() }, input: ti, keyInput: ki}
	fl.Refresh()
	return fl
}

func (fl *FieldList) Refresh() {
	// Remember the row under the cursor by identity, not by index: a rebuild
	// that inserts or removes a row above it would otherwise slide a different
	// entry under the cursor, and a repeated d would delete that one.
	prevID := ""
	if fl.cursor >= 0 && fl.cursor < len(fl.rows) {
		prevID = fl.rows[fl.cursor].ID
	}
	fl.rows = fl.Fields()
	if prevID != "" {
		// Identity only decides the cursor when it names exactly one row. A
		// list may legitimately repeat an ID (action rows, index-derived ids),
		// and jumping to the first of those would move the cursor somewhere
		// the user did not ask for; the index clamp below is the fallback.
		match := -1
		for i, row := range fl.rows {
			if row.ID == prevID && row.Kind != KindHeader {
				if match >= 0 {
					match = -1
					break
				}
				match = i
			}
		}
		if match >= 0 {
			fl.cursor = match
		}
	}
	if fl.cursor >= len(fl.rows) {
		fl.cursor = len(fl.rows) - 1
	}
	if fl.cursor < 0 {
		fl.cursor = 0
	}
	// Clamp cursor away from header rows.
	for fl.cursor < len(fl.rows) && fl.rows[fl.cursor].Kind == KindHeader {
		fl.cursor++
	}
	if fl.cursor >= len(fl.rows) {
		fl.cursor = len(fl.rows) - 1
	}
	if fl.cursor < 0 {
		fl.cursor = 0
	}
}

func (fl *FieldList) Rows() []*Field { fl.Refresh(); return fl.rows }
func (fl *FieldList) Cursor() int    { return fl.cursor }

func (fl *FieldList) SetCursor(i int) {
	fl.disarmDelete()
	fl.Refresh()
	if i >= 0 && i < len(fl.rows) {
		fl.cursor = i
	}
	// Skip past header rows.
	for fl.cursor < len(fl.rows) && fl.rows[fl.cursor].Kind == KindHeader {
		fl.cursor++
	}
	if fl.cursor >= len(fl.rows) {
		fl.cursor = len(fl.rows) - 1
	}
	if fl.cursor < 0 {
		fl.cursor = 0
	}
}

func (fl *FieldList) CursorRow() *Field {
	fl.Refresh()
	if len(fl.rows) == 0 {
		return nil
	}
	return fl.rows[fl.cursor]
}

func (fl *FieldList) SetSize(w, h int) { fl.width, fl.height = w, h }

func (fl *FieldList) setDescSuppressed(suppress bool) { fl.descSuppressed = suppress }

func (fl *FieldList) setGroupDividers(on bool) { fl.groupDividers = on }

func (fl *FieldList) disarmRow(i int) {
	fl.Refresh()
	if i >= 0 && i < len(fl.rows) && fl.rows[i].Disarm != nil {
		fl.rows[i].Disarm()
	}
}

func (fl *FieldList) DisarmCurrent() {
	fl.Refresh()
	fl.disarmRow(fl.cursor)
}

// InputValue returns the scalar edit input's current text.
func (fl *FieldList) InputValue() string { return fl.input.Value() }

func (fl *FieldList) Editing() bool { return fl.editing || fl.picking || fl.adding }

// Committed reports whether the most recent Update call invoked a field
// setter successfully. See the committed field doc for why this matters.
func (fl *FieldList) Committed() bool { return fl.committed }

func (fl *FieldList) CancelEdit() {
	fl.editing = false
	fl.picking = false
	fl.adding = false
	fl.ErrMsg = ""
	fl.input.Blur()
	fl.keyInput.Blur()
}

// TakePushRequest returns and clears the frame a drill row asked to open.
func (fl *FieldList) TakePushRequest() *Frame {
	f := fl.pushRequest
	fl.pushRequest = nil
	return f
}

func (fl *FieldList) Update(msg tea.Msg) tea.Cmd {
	fl.Refresh()
	fl.committed = false
	if fl.adding {
		return fl.updateAdd(msg)
	}
	if fl.editing {
		return fl.updateEdit(msg)
	}

	// Auto-open edit mode when a paste arrives on an editable scalar
	// field. Without this, pasting into a field that is not yet being
	// edited silently drops the paste, which is confusing in panels
	// like the skills install frame.
	if paste, ok := msg.(tea.PasteMsg); ok {
		row := fl.CursorRow()
		if row != nil && row.Kind == KindScalar && row.SetStr != nil {
			fl.openRow(row)
			if fl.editing {
				fl.input, _ = fl.input.Update(paste)
			}
		}
		return nil
	}

	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	// Any key other than a second d cancels a pending delete arm, mirroring
	// the Ctrl+R rollback arm: the armed state never outlives the keystroke
	// that set it (AUD-13).
	if k.String() != "d" {
		fl.disarmDelete()
	}
	if fl.picking {
		fl.updatePick(k)
		return nil
	}
	row := fl.CursorRow()
	switch k.String() {
	case "up", "k":
		if fl.cursor > 0 {
			fl.disarmRow(fl.cursor)
			orig := fl.cursor
			fl.cursor--
			for fl.cursor >= 0 && fl.rows[fl.cursor].Kind == KindHeader {
				fl.cursor--
			}
			if fl.cursor < 0 {
				fl.cursor = orig
			}
		}
	case "down", "j":
		if fl.cursor < len(fl.rows)-1 {
			fl.disarmRow(fl.cursor)
			orig := fl.cursor
			fl.cursor++
			for fl.cursor < len(fl.rows) && fl.rows[fl.cursor].Kind == KindHeader {
				fl.cursor++
			}
			if fl.cursor >= len(fl.rows) {
				fl.cursor = orig
			}
		}
	case "g":
		fl.disarmRow(fl.cursor)
		fl.cursor = 0
		for fl.cursor < len(fl.rows) && fl.rows[fl.cursor].Kind == KindHeader {
			fl.cursor++
		}
	case "G":
		fl.disarmRow(fl.cursor)
		fl.cursor = len(fl.rows) - 1
		for fl.cursor >= 0 && fl.rows[fl.cursor].Kind == KindHeader {
			fl.cursor--
		}
		if fl.cursor < 0 {
			fl.cursor = 0
		}
	case "space":
		if row != nil && row.Kind == KindToggle {
			row.SetBool(!row.GetBool())
			fl.committed = true
		}
	case "left", "right":
		if row != nil && row.Kind == KindEnum {
			fl.cycleEnum(row, k.String() == "right")
		}
	case "enter", "e":
		return fl.openRow(row)
	case "a":
		if fl.OnAddMsg != nil {
			return func() tea.Msg { return fl.OnAddMsg() }
		}
		if fl.OnAdd != nil {
			if fl.KeyPrompt == "" {
				if err := fl.OnAdd(""); err != nil {
					fl.ErrMsg = err.Error()
					return nil
				}
				fl.committed = true
				fl.Refresh()
				fl.cursor = fl.findAddedRow("")
				return nil
			}
			fl.adding = true
			fl.ErrMsg = ""
			fl.keyInput.SetValue("")
			fl.keyInput.Focus()
		}
	case "y":
		if row != nil && row.Yank != nil {
			fl.yankedData = row.Yank()
		}
		return nil
	case "p":
		if fl.yankedData != nil && row != nil && row.Paste != nil {
			if err := row.Paste(fl.yankedData); err != nil {
				fl.ErrMsg = err.Error()
			} else {
				fl.committed = true
				fl.yankedData = nil
				fl.Refresh()
			}
		}
		return nil
	case "shift+up":
		if row != nil && row.MoveUp != nil {
			row.MoveUp()
			fl.Refresh()
		}
		return nil
	case "shift+down":
		if row != nil && row.MoveDown != nil {
			row.MoveDown()
			fl.Refresh()
		}
		return nil
	case "d":
		if row == nil || row.Del == nil {
			return nil
		}
		// d is destructive and repeated presses land on a different row once
		// one is removed, so it is a two-press gesture: arm on the first, and
		// delete only on a second within the window (AUD-13).
		if !fl.deleteArmedOn(row) {
			fl.armDelete(row)
			return nil
		}
		fl.disarmDelete()
		row.Del()
		fl.committed = true
		fl.Refresh()
	}
	return nil
}

func (fl *FieldList) openRow(row *Field) tea.Cmd {
	if row == nil {
		return nil
	}
	switch row.Kind {
	case KindToggle:
		row.SetBool(!row.GetBool())
		fl.committed = true
	case KindScalar:
		if row.SetStr == nil {
			return nil // read-only
		}
		fl.editing = true
		fl.ErrMsg = ""
		if row.Masked {
			fl.input.EchoMode = textinput.EchoPassword
			fl.input.SetValue("")
		} else {
			fl.input.EchoMode = textinput.EchoNormal
			fl.input.SetValue(row.GetStr())
			fl.input.CursorEnd()
		}
		fl.input.Focus()
	case KindEnum:
		fl.picking = true
		i := indexOf(row.Options(), row.GetStr())
		if i < 0 {
			i = 0
		}
		fl.pickIdx = i
	case KindDrill:
		fl.pushRequest = row.Build()
	case KindAction:
		return row.Act()
	case KindPicker:
		fl.pushPicker = &pickerRequest{
			fieldID:     row.ID,
			items:       row.PickOptions(),
			onPick:      row.PickOnPick,
			title:       row.Title,
			allowCustom: row.PickAllowCustom,
		}
	}
	return nil
}

func (fl *FieldList) cycleEnum(row *Field, forward bool) {
	opts := row.Options()
	if len(opts) == 0 {
		return
	}
	i := indexOf(opts, row.GetStr())
	if i < 0 {
		i = 0
	}
	if forward {
		i = (i + 1) % len(opts)
	} else {
		i = (i - 1 + len(opts)) % len(opts)
	}
	if err := row.SetStr(opts[i]); err != nil {
		fl.ErrMsg = err.Error()
		return
	}
	fl.committed = true
}

func (fl *FieldList) updateEdit(msg tea.Msg) tea.Cmd {
	row := fl.CursorRow()
	if row == nil {
		fl.CancelEdit()
		return nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter", "shift+enter":
			val := strings.TrimSpace(fl.input.Value())
			if row.Masked && val == "" {
				fl.CancelEdit() // empty keeps the stored secret
				return nil
			}
			if err := row.SetStr(val); err != nil {
				fl.ErrMsg = err.Error()
				return nil
			}
			fl.committed = true
			fl.CancelEdit()
			return nil
		case "esc":
			fl.CancelEdit()
			return nil
		}
	}
	var cmd tea.Cmd
	fl.input, cmd = fl.input.Update(msg)
	return cmd
}

func (fl *FieldList) updatePick(k tea.KeyPressMsg) {
	row := fl.CursorRow()
	if row == nil {
		fl.CancelEdit()
		return
	}
	opts := row.Options()
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
			if err := row.SetStr(opts[fl.pickIdx]); err != nil {
				fl.ErrMsg = err.Error()
				return
			}
			fl.committed = true
		}
		fl.picking = false
	case "esc":
		fl.picking = false
	}
}

func (fl *FieldList) updateAdd(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "enter":
			newKey := strings.TrimSpace(fl.keyInput.Value())
			if err := fl.OnAdd(newKey); err != nil {
				fl.ErrMsg = err.Error()
				return nil
			}
			fl.committed = true
			fl.CancelEdit()
			fl.Refresh()
			fl.cursor = fl.findAddedRow(newKey)
			return nil
		case "esc":
			fl.CancelEdit()
			return nil
		}
	}
	var cmd tea.Cmd
	fl.keyInput, cmd = fl.keyInput.Update(msg)
	return cmd
}

// findAddedRow returns the index of the row whose ID ends with "."+newKey,
// or whose Title equals newKey (listDrill case). If newKey is empty or no
// match is found, it falls back to the last row index.
func (fl *FieldList) findAddedRow(newKey string) int {
	if newKey != "" {
		suffix := "." + newKey
		for i, row := range fl.rows {
			if strings.HasSuffix(row.ID, suffix) {
				return i
			}
		}
		// No id match: try title match (listDrill uses numeric ids).
		for i, row := range fl.rows {
			if row.Title == newKey {
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
func (fl *FieldList) valueCell(row *Field, isCursor bool) string {
	if row.Display != nil && row.Kind != KindHeader && !(fl.editing && isCursor) {
		value, badge := row.Display()
		if value == "" {
			value = "—"
		}
		out := flValueStyle().Render(value)
		if badge != "" {
			out += " " + DescStyle().Render(badge)
		}
		return out
	}
	switch row.Kind {
	case KindToggle:
		if row.GetBool() {
			return flOnStyle().Render("on ●")
		}
		return flOffStyle().Render("off ○")
	case KindScalar:
		if fl.editing && isCursor {
			return fl.input.View()
		}
		v := row.GetStr()
		if row.Masked {
			v = MaskKey(v)
		}
		if v == "" {
			v = "—"
		}
		return flValueStyle().Render(v)
	case KindEnum:
		return flValueStyle().Render(row.GetStr() + " ▾")
	case KindDrill:
		return flValueStyle().Render(row.Summary() + " ›")
	case KindAction:
		label := "\u21b5 run"
		if row.ActLabel != nil {
			label = row.ActLabel()
		}
		if strings.HasPrefix(label, "\u2713") {
			return flOnStyle().Render(label)
		}
		if strings.HasPrefix(label, "\u2717") {
			return flErrStyle().Render(label)
		}
		return flValueStyle().Render(label)
	case KindPicker:
		v := row.GetStr()
		if v == "" {
			v = "\u2014"
		}
		suffix := " \u25be"
		if row.PickPending != nil && row.PickPending() {
			suffix = " \u2026"
		}
		return flValueStyle().Render(v + suffix)
	case KindHeader:
		return ""
	}
	return ""
}

// View renders the list to at most height physical rows, keeping the focused
// row — or its open editor, picker option, or add prompt — visible and adding
// ↑/↓ more indicators when clipped.
//
// Elements are budgeted as physical lines, not as elements: chrome.Row stacks
// onto two lines when the right column would starve the label, and a stacked
// row that counted as one line silently overflowed the height the dock gave
// the pane.
func (fl *FieldList) View() string {
	fl.Refresh()
	var lines []string
	// push splits one rendered element into its physical lines and returns
	// the index of the first, so callers can track where the focus landed.
	push := func(rendered string) int {
		start := len(lines)
		lines = append(lines, strings.Split(rendered, "\n")...)
		return start
	}
	cursorStart := 0
	editLine, pickLine, addLine := -1, -1, -1
	if len(fl.rows) == 0 && !fl.adding {
		empty := "  (empty"
		if fl.OnAdd != nil {
			empty += " — press a to add"
		}
		push(DescStyle().Render(empty + ")"))
	}
	for i, row := range fl.rows {
		isCursor := i == fl.cursor
		if row.Kind == KindHeader {
			if fl.groupDividers {
				push(chrome.Divider(row.Title))
				continue
			}
			header := flHeaderStyle().Render(row.Title)
			gap := fl.width - lipgloss.Width(header)
			if gap < 0 {
				gap = 0
			}
			push(header + strings.Repeat(" ", gap))
			continue
		}
		marker := chrome.BlankMarker
		if isCursor {
			marker = chrome.SelectionMarker
		}
		val := fl.valueCell(row, isCursor)
		title := row.Title
		if row.Warn {
			title = flWarnStyle().Render(glyph.Warning+" ") + title
		}
		var line string
		if isCursor {
			line = chrome.SelectionStyle().Width(fl.width).
				Render(chrome.Row(marker, row.Title, ansi.Strip(val), "", fl.width))
		} else {
			line = chrome.Row(marker, flTitleStyle().Render(title), val, "", fl.width)
		}
		start := push(line)
		if isCursor {
			cursorStart = start
			if fl.editing {
				// The edited value is the row's right column, which lands on
				// the last physical line of a stacked row and on the row
				// itself otherwise.
				editLine = len(lines) - 1
			}
		}
		if isCursor && fl.ErrMsg != "" {
			push("    " + flErrStyle().Render(glyph.Warning+" "+fl.ErrMsg))
		}
		// The first d must never look like a no-op: name the row a second
		// press would delete, and how to get out of it (AUD-13).
		if isCursor && fl.deleteArmedOn(row) {
			push("    " + flWarnStyle().Render(glyph.Warning+" press d again to delete "+row.Title+" · any other key cancels"))
		}
		if isCursor && row.Desc != "" && !fl.Editing() && !fl.descSuppressed {
			push("    " + DescStyle().Render(row.Desc))
		}
		if isCursor && fl.picking {
			for j, opt := range row.Options() {
				pm := "    "
				if j == fl.pickIdx {
					pm = "  ▸ "
				}
				optStart := push(pm + flValueStyle().Render(opt))
				if j == fl.pickIdx {
					pickLine = optStart
				}
			}
		}
	}
	if fl.adding {
		addLine = push("▸ " + fl.KeyPrompt + ": " + fl.keyInput.View())
		if fl.ErrMsg != "" {
			push("    " + flErrStyle().Render(glyph.Warning+" "+fl.ErrMsg))
		}
	}
	// Focus priority: an open add prompt, then an open editor, then the
	// selected picker option, else the cursor row's marker line.
	focus := cursorStart
	switch {
	case fl.adding && addLine >= 0:
		focus = addLine
	case fl.editing && editLine >= 0:
		focus = editLine
	case fl.picking && pickLine >= 0:
		focus = pickLine
	}
	return windowLines(lines, focus, fl.height)
}

// windowLines windows physical lines to at most height rows, keeping focusLine
// visible with ↑/↓ more indicators when content is hidden.
//
// Unlike chrome.ClipLines — which unrelated callers share and which treats
// "no lines" as "zero height" — this budgets rendered physical lines: a
// stacked row spends two rows, so a half-visible stacked field is trimmed to
// the focus line rather than overflowing the pane. height <= 0 renders
// nothing; height 1 renders the focus line alone, whole and unindicated.
func windowLines(lines []string, focusLine, height int) string {
	if height <= 0 || len(lines) == 0 {
		return ""
	}
	if focusLine < 0 {
		focusLine = 0
	}
	if focusLine >= len(lines) {
		focusLine = len(lines) - 1
	}
	if height == 1 {
		return lines[focusLine]
	}
	if len(lines) <= height {
		return strings.Join(lines, "\n")
	}

	// Take the largest window that fits once its indicators are counted, so
	// the rows an absent indicator frees go back to content. Each size is
	// tried centred on the focus first, then anchored to either end and to the
	// focus: a window that starts at the list's own edge spends one indicator
	// instead of two and is what a focus near an end actually wants.
	for size := height; size >= 1; size-- {
		centred := focusLine - (size-1)/2
		if centred < 0 {
			centred = 0
		}
		if centred+size > len(lines) {
			centred = len(lines) - size
		}
		for _, start := range [4]int{centred, 0, len(lines) - size, focusLine} {
			end := start + size
			if start < 0 || end > len(lines) || focusLine < start || focusLine >= end {
				continue
			}
			rows := size
			if start > 0 {
				rows++
			}
			if end < len(lines) {
				rows++
			}
			if rows > height {
				continue
			}
			out := make([]string, 0, height)
			if start > 0 {
				out = append(out, DescStyle().Render("  ↑ more"))
			}
			out = append(out, lines[start:end]...)
			if end < len(lines) {
				out = append(out, DescStyle().Render("  ↓ more"))
			}
			return strings.Join(out, "\n")
		}
	}
	// Only the focus line fits beside a single indicator: announce the hidden
	// content nearest it, below when there is any.
	if focusLine+1 < len(lines) {
		return lines[focusLine] + "\n" + DescStyle().Render("  ↓ more")
	}
	return DescStyle().Render("  ↑ more") + "\n" + lines[focusLine]
}

type pickerRequest struct {
	fieldID     string
	items       []picker.Item
	onPick      func(string) error
	title       string
	footer      string
	allowCustom bool
}

func (fl *FieldList) TakePushPicker() *pickerRequest {
	r := fl.pushPicker
	fl.pushPicker = nil
	return r
}
