// Package listpanel is the TUI's shared list engine: a row model, cursor and
// scroll handling, section headers, action rows, drill-in rows, nested
// frames, and filtering hooks. settings/ is its config-specific consumer.
package listpanel

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/glyph"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/textfield"
	"marshal/internal/app/tui/theme"
)

func isMono() bool {
	_, ok := theme.Current().FGDefault.(lipgloss.NoColor)
	return ok
}

func flCursorStyle() lipgloss.Style {
	if isMono() {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Bold(true).Background(theme.Current().BGSelection)
}
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
	if isMono() {
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
	fl.rows = fl.Fields()
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
		if row != nil && row.Del != nil {
			row.Del()
			fl.Refresh()
		}
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

// View renders the list clipped to height, keeping the cursor row visible
// and adding ↑/↓ more indicators when clipped.
func (fl *FieldList) View() string {
	fl.Refresh()
	var lines []string
	cursorLine := 0
	if len(fl.rows) == 0 && !fl.adding {
		empty := "  (empty"
		if fl.OnAdd != nil {
			empty += " — press a to add"
		}
		lines = append(lines, DescStyle().Render(empty+")"))
	}
	for i, row := range fl.rows {
		isCursor := i == fl.cursor
		if row.Kind == KindHeader {
			header := flHeaderStyle().Render(row.Title)
			gap := fl.width - lipgloss.Width(header)
			if gap < 0 {
				gap = 0
			}
			lines = append(lines, header+strings.Repeat(" ", gap))
			continue
		}
		marker := "  "
		if isCursor {
			marker = "▸ "
		}
		val := fl.valueCell(row, isCursor)
		title := row.Title
		if row.Warn {
			title = flWarnStyle().Render(glyph.Warning+" ") + title
		}
		gap := fl.width - lipgloss.Width(marker) - lipgloss.Width(title) - lipgloss.Width(val)
		if gap < 1 {
			gap = 1
		}
		line := marker + flTitleStyle().Render(title) + strings.Repeat(" ", gap) + val
		if isCursor {
			cursorLine = len(lines)
			line = flCursorStyle().Render(marker+title) + strings.Repeat(" ", gap) + val
		}
		lines = append(lines, line)
		if isCursor && fl.ErrMsg != "" {
			lines = append(lines, "    "+flErrStyle().Render(glyph.Warning+" "+fl.ErrMsg))
		}
		if isCursor && row.Desc != "" && !fl.Editing() && !fl.descSuppressed {
			lines = append(lines, "    "+DescStyle().Render(row.Desc))
		}
		if isCursor && fl.picking {
			for j, opt := range row.Options() {
				pm := "    "
				if j == fl.pickIdx {
					pm = "  ▸ "
				}
				lines = append(lines, pm+flValueStyle().Render(opt))
			}
		}
	}
	if fl.adding {
		lines = append(lines, "▸ "+fl.KeyPrompt+": "+fl.keyInput.View())
		if fl.ErrMsg != "" {
			lines = append(lines, "    "+flErrStyle().Render(glyph.Warning+" "+fl.ErrMsg))
		}
	}
	return clipLines(lines, cursorLine, fl.height)
}

// clipLines windows lines to at most height rows, keeping focusLine visible,
// with ↑/↓ more indicators when clipped.
func clipLines(lines []string, focusLine, height int) string {
	return chrome.ClipLines(lines, focusLine, height, theme.Current())
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
