package listpanel

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/tui/picker"
)

// FieldKind discriminates the row types a FieldList can render and edit.
type FieldKind int

const (
	KindToggle FieldKind = iota // bool: Space flips
	KindScalar                  // string/number/duration: Enter opens inline edit
	KindEnum                    // one of options(): ←/→ cycle, Enter opens picker
	KindDrill                   // Enter pushes build() as a new Frame
	KindAction                  // Enter runs act() and returns a Cmd
	KindPicker                  // Enter opens a picker overlay
	KindHeader                  // section header (unfiltered browser only, not selectable)
)

// Field is one row in a FieldList. Exactly one kind-group of closures is
// set. Setters write straight into the working config; every mutation is
// persisted immediately on commit (see BrowserPanel.flushChanges).
type Field struct {
	ID       string // stable id for the search registry ("shell.allow_network")
	TomlPath string // real TOML config path ("tools.shell.allow_network") for /set
	Title    string
	Desc     string   // one-liner rendered under the row while the cursor is on it
	Keywords []string // extra search terms beyond the title

	Kind FieldKind

	// KindToggle
	GetBool func() bool
	SetBool func(bool)

	// KindScalar and KindEnum current-value access. SetStr validates and
	// applies; a non-nil error renders under the row and blocks apply.
	// SetStr == nil marks the row read-only (Enter does nothing).
	GetStr func() string
	SetStr func(string) error
	Masked bool // render via MaskKey; edits replace, empty input keeps
	Warn   bool // render with warning style for risky settings

	// KindEnum
	Options func() []string

	// KindDrill
	Summary func() string // right-cell summary, e.g. "3 items"
	Build   func() *Frame

	// optional per-row delete: entry rows, list items, map keys, and
	// masked-secret clear all hang their removal behavior here (key: d).
	Del func()

	// KindAction
	Act      func() tea.Cmd
	ActLabel func() string

	// KindPicker
	PickOptions     func() []picker.Item
	PickOnPick      func(string) error
	PickPending     func() bool
	PickAllowCustom bool

	// writeGlobal marks a person-preference/credential row: commits write
	// the user-global config instead of the project config.
	writeGlobal bool

	// collection ops (Phase 2)
	Yank     func() any
	Paste    func(any) error
	MoveUp   func()
	MoveDown func()
	// Disarm is called by the FieldList when the cursor leaves this row
	// (used by the reset-to-defaults confirm idiom).
	Disarm func()
}

// Frame is one level of a pane's drill-down stack: a titled FieldList plus
// optional add behavior for collection frames. (Stack management lives in
// pane.go — Task 4.)
type Frame struct {
	Title string // breadcrumb segment, e.g. "github" in "MCP › github"
	List  *FieldList
}

// NewField creates a Field with the given properties. This is the exported
// constructor for building fields from outside the listpanel package.
func NewField(id, title string, kind FieldKind) *Field {
	return &Field{ID: id, Title: title, Kind: kind}
}

// NewFrame creates a Frame with the given title and field list.
func NewFrame(title string, fields func() []*Field) *Frame {
	return &Frame{Title: title, List: NewFieldList(func() []*Field {
		return fields()
	})}
}

// FieldID returns the ID of a field.
func FieldID(f *Field) string { return f.ID }

// FieldTitle returns the Title of a field.
func FieldTitle(f *Field) string { return f.Title }

// FieldDesc returns the Desc of a field.
func FieldDesc(f *Field) string { return f.Desc }

// SetFieldID sets the ID of a field.
func SetFieldID(f *Field, id string) { f.ID = id }

// SetFieldTitle sets the Title of a field.
func SetFieldTitle(f *Field, title string) { f.Title = title }

// SetFieldDesc sets the Desc of a field.
func SetFieldDesc(f *Field, desc string) { f.Desc = desc }

// SetFieldKind sets the Kind of a field.
func SetFieldKind(f *Field, kind FieldKind) { f.Kind = kind }

// SetFieldGetStr sets the GetStr closure of a field.
func SetFieldGetStr(f *Field, get func() string) { f.GetStr = get }

// SetFieldSetStr sets the SetStr closure of a field.
func SetFieldSetStr(f *Field, set func(string) error) { f.SetStr = set }

// SetFieldGetBool sets the GetBool closure of a field.
func SetFieldGetBool(f *Field, get func() bool) { f.GetBool = get }

// SetFieldSetBool sets the SetBool closure of a field.
func SetFieldSetBool(f *Field, set func(bool)) { f.SetBool = set }

// SetFieldOptions sets the Options closure of a field.
func SetFieldOptions(f *Field, opts func() []string) { f.Options = opts }

// SetFieldSummary sets the Summary closure of a field.
func SetFieldSummary(f *Field, summary func() string) { f.Summary = summary }

// SetFieldBuild sets the Build closure of a field.
func SetFieldBuild(f *Field, build func() *Frame) { f.Build = build }

// SetFieldDel sets the Del closure of a field.
func SetFieldDel(f *Field, del func()) { f.Del = del }

// SetFieldAct sets the Act closure of a field.
func SetFieldAct(f *Field, act func() tea.Cmd) { f.Act = act }

// SetFieldActLabel sets the ActLabel closure of a field.
func SetFieldActLabel(f *Field, label func() string) { f.ActLabel = label }

// SetFieldPickOptions sets the PickOptions closure of a field.
func SetFieldPickOptions(f *Field, opts func() []picker.Item) { f.PickOptions = opts }

// SetFieldPickOnPick sets the PickOnPick closure of a field.
func SetFieldPickOnPick(f *Field, pick func(string) error) { f.PickOnPick = pick }

// SetFieldPickAllowCustom sets the PickAllowCustom flag.
func SetFieldPickAllowCustom(f *Field, allow bool) { f.PickAllowCustom = allow }

// SetFieldKeywords sets the Keywords of a field.
func SetFieldKeywords(f *Field, keywords []string) { f.Keywords = keywords }

// SetFieldWarn sets the Warn flag.
func SetFieldWarn(f *Field, warn bool) { f.Warn = warn }

// SetFieldMasked sets the Masked flag.
func SetFieldMasked(f *Field, masked bool) { f.Masked = masked }

// SetFieldYank sets the Yank closure.
func SetFieldYank(f *Field, yank func() any) { f.Yank = yank }

// SetFieldPaste sets the Paste closure.
func SetFieldPaste(f *Field, paste func(any) error) { f.Paste = paste }

// SetFieldMoveUp sets the MoveUp closure.
func SetFieldMoveUp(f *Field, up func()) { f.MoveUp = up }

// SetFieldMoveDown sets the MoveDown closure.
func SetFieldMoveDown(f *Field, down func()) { f.MoveDown = down }

// SetFieldDisarm sets the Disarm closure.
func SetFieldDisarm(f *Field, disarm func()) { f.Disarm = disarm }

// SetFieldWriteGlobal marks a field as global-target.
func SetFieldWriteGlobal(f *Field, global bool) { f.writeGlobal = global }

// FieldWriteGlobal reports whether a field writes to the user-global config.
func FieldWriteGlobal(f *Field) bool { return f.writeGlobal }

// FieldTomlPath returns the TOML path of a field.
func FieldTomlPath(f *Field) string { return f.TomlPath }

// FieldGetStr returns the GetStr closure result.
func FieldGetStr(f *Field) string {
	if f.GetStr == nil {
		return ""
	}
	return f.GetStr()
}

// FieldGetBool returns the GetBool closure result.
func FieldGetBool(f *Field) bool {
	if f.GetBool == nil {
		return false
	}
	return f.GetBool()
}

// FrameTitle returns the Title of a frame.
func FrameTitle(f *Frame) string { return f.Title }

// FrameList returns the List of a frame.
func FrameList(f *Frame) *FieldList { return f.List }

// SetFrameTitle sets the Title of a frame.
func SetFrameTitle(f *Frame, title string) { f.Title = title }

// SetFrameList sets the List of a frame.
func SetFrameList(f *Frame, list *FieldList) { f.List = list }

// PickerRequest carries the data needed to open a picker overlay for a
// picker-kind field. It mirrors the internal pickerRequest struct.
type PickerRequest struct {
	FieldID     string
	Items       []picker.Item
	OnPick      func(string) error
	Title       string
	Footer      string
	AllowCustom bool
}

// FieldListRefresh refreshes a field list.
func FieldListRefresh(fl *FieldList) { fl.Refresh() }

// FieldListSetSize sets the size of a field list.
func FieldListSetSize(fl *FieldList, w, h int) { fl.SetSize(w, h) }

// FieldListRows returns the rows of a field list.
func FieldListRows(fl *FieldList) []*Field { return fl.Rows() }

// FieldListUpdate updates a field list with a message.
func FieldListUpdate(fl *FieldList, msg tea.Msg) tea.Cmd { return fl.Update(msg) }

// FieldListCommitted reports whether the most recent Update call invoked a
// field setter successfully (toggle, enum cycle/pick, scalar edit confirm,
// collection add, or paste).
func FieldListCommitted(fl *FieldList) bool { return fl.Committed() }

// FieldListTakePushPicker returns and clears the picker request a picker row
// asked to open. Returns nil if no picker was requested.
func FieldListTakePushPicker(fl *FieldList) *PickerRequest {
	pr := fl.TakePushPicker()
	if pr == nil {
		return nil
	}
	return &PickerRequest{
		FieldID:     pr.fieldID,
		Items:       pr.items,
		OnPick:      pr.onPick,
		Title:       pr.title,
		Footer:      pr.footer,
		AllowCustom: pr.allowCustom,
	}
}

// FieldListTakePushRequest returns and clears the drill frame a drill row
// asked to open. Returns nil if no drill was requested.
func FieldListTakePushRequest(fl *FieldList) *Frame { return fl.TakePushRequest() }

// FieldListCursorRow returns the row under the cursor, or nil.
func FieldListCursorRow(fl *FieldList) *Field { return fl.CursorRow() }

// FieldListView returns the view of a field list.
func FieldListView(fl *FieldList) string { return fl.View() }

// FieldListSetDescSuppressed hides the inline desc line under the cursor row
// (used by two-column panels that render desc in a detail pane).
func FieldListSetDescSuppressed(fl *FieldList, suppress bool) { fl.setDescSuppressed(suppress) }

// FieldListCursorDesc returns the description of the cursor row, or "".
func FieldListCursorDesc(fl *FieldList) string {
	if row := fl.CursorRow(); row != nil {
		return row.Desc
	}
	return ""
}
