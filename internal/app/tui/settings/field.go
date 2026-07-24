package settings

import (
	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/tui/picker"
)

// fieldKind discriminates the row types a fieldList can render and edit.
type fieldKind int

const (
	kindToggle fieldKind = iota // bool: Space flips
	kindScalar                  // string/number/duration: Enter opens inline edit
	kindEnum                    // one of options(): ←/→ cycle, Enter opens picker
	kindDrill                   // Enter pushes build() as a new frame
	kindAction                  // Enter runs act() and returns a Cmd
	kindPicker                  // Enter opens a picker overlay
	kindHeader                  // section header (unfiltered browser only, not selectable)
)

// Exported field kind constants.
const (
	KindToggle = kindToggle
	KindScalar = kindScalar
	KindEnum   = kindEnum
	KindDrill  = kindDrill
	KindAction = kindAction
	KindPicker = kindPicker
	KindHeader = kindHeader
)

// field is one row in a fieldList. Exactly one kind-group of closures is
// set. Setters write straight into the working config; every mutation is
// persisted immediately on commit (see BrowserPanel.flushChanges).
type field struct {
	id       string // stable id for the search registry ("shell.allow_network")
	tomlPath string // real TOML config path ("tools.shell.allow_network") for /set
	title    string
	desc     string   // one-liner rendered under the row while the cursor is on it
	keywords []string // extra search terms beyond the title

	kind fieldKind

	// kindToggle
	getBool func() bool
	setBool func(bool)

	// kindScalar and kindEnum current-value access. setStr validates and
	// applies; a non-nil error renders under the row and blocks apply.
	// setStr == nil marks the row read-only (Enter does nothing).
	getStr func() string
	setStr func(string) error
	masked bool // render via maskKey; edits replace, empty input keeps
	warn   bool // render with warning style for risky settings

	// kindEnum
	options func() []string

	// kindDrill
	summary func() string // right-cell summary, e.g. "3 items"
	build   func() *frame

	// optional per-row delete: entry rows, list items, map keys, and
	// masked-secret clear all hang their removal behavior here (key: d).
	del func()

	// kindAction
	act      func() tea.Cmd
	actLabel func() string

	// kindPicker
	pickOptions     func() []picker.Item
	pickOnPick      func(string) error
	pickPending     func() bool
	pickAllowCustom bool

	// collection ops (Phase 2)
	yank     func() any
	paste    func(any) error
	moveUp   func()
	moveDown func()
	// disarm is called by the fieldList when the cursor leaves this row
	// (used by the reset-to-defaults confirm idiom).
	disarm func()
}

// frame is one level of a pane's drill-down stack: a titled fieldList plus
// optional add behavior for collection frames. (Stack management lives in
// pane.go — Task 4.)
type frame struct {
	title string // breadcrumb segment, e.g. "github" in "MCP › github"
	list  *fieldList
}

// Exported type aliases for use by the agents package.
type Field = field
type Frame = frame

// NewField creates a Field with the given properties. This is the exported
// constructor for building fields from outside the settings package.
func NewField(id, title string, kind fieldKind) *Field {
	return &Field{id: id, title: title, kind: kind}
}

// NewFrame creates a Frame with the given title and field list.
func NewFrame(title string, fields func() []*Field) *Frame {
	return &Frame{title: title, list: newFieldList(func() []*field {
		result := fields()
		out := make([]*field, len(result))
		for i, f := range result {
			out[i] = f
		}
		return out
	})}
}

// FieldID returns the id of a field.
func FieldID(f *Field) string { return f.id }

// FieldTitle returns the title of a field.
func FieldTitle(f *Field) string { return f.title }

// FieldDesc returns the description of a field.
func FieldDesc(f *Field) string { return f.desc }

// FieldKind returns the kind of a field.
func FieldKind(f *Field) fieldKind { return f.kind }

// SetFieldID sets the id of a field.
func SetFieldID(f *Field, id string) { f.id = id }

// SetFieldTitle sets the title of a field.
func SetFieldTitle(f *Field, title string) { f.title = title }

// SetFieldDesc sets the description of a field.
func SetFieldDesc(f *Field, desc string) { f.desc = desc }

// SetFieldKind sets the kind of a field.
func SetFieldKind(f *Field, kind fieldKind) { f.kind = kind }

// SetFieldGetStr sets the getStr closure of a field.
func SetFieldGetStr(f *Field, get func() string) { f.getStr = get }

// SetFieldSetStr sets the setStr closure of a field.
func SetFieldSetStr(f *Field, set func(string) error) { f.setStr = set }

// SetFieldGetBool sets the getBool closure of a field.
func SetFieldGetBool(f *Field, get func() bool) { f.getBool = get }

// SetFieldSetBool sets the setBool closure of a field.
func SetFieldSetBool(f *Field, set func(bool)) { f.setBool = set }

// SetFieldOptions sets the options closure of a field.
func SetFieldOptions(f *Field, opts func() []string) { f.options = opts }

// SetFieldSummary sets the summary closure of a field.
func SetFieldSummary(f *Field, summary func() string) { f.summary = summary }

// SetFieldBuild sets the build closure of a field.
func SetFieldBuild(f *Field, build func() *Frame) { f.build = build }

// SetFieldDel sets the del closure of a field.
func SetFieldDel(f *Field, del func()) { f.del = del }

// SetFieldAct sets the act closure of a field.
func SetFieldAct(f *Field, act func() tea.Cmd) { f.act = act }

// SetFieldActLabel sets the actLabel closure of a field.
func SetFieldActLabel(f *Field, label func() string) { f.actLabel = label }

// SetFieldPickOptions sets the pickOptions closure of a field.
func SetFieldPickOptions(f *Field, opts func() []picker.Item) { f.pickOptions = opts }

// SetFieldPickOnPick sets the pickOnPick closure of a field.
func SetFieldPickOnPick(f *Field, pick func(string) error) { f.pickOnPick = pick }

// SetFieldPickAllowCustom sets the pickAllowCustom flag.
func SetFieldPickAllowCustom(f *Field, allow bool) { f.pickAllowCustom = allow }

// SetFieldKeywords sets the keywords of a field.
func SetFieldKeywords(f *Field, keywords []string) { f.keywords = keywords }

// SetFieldWarn sets the warn flag.
func SetFieldWarn(f *Field, warn bool) { f.warn = warn }

// SetFieldMasked sets the masked flag.
func SetFieldMasked(f *Field, masked bool) { f.masked = masked }

// SetFieldYank sets the yank closure.
func SetFieldYank(f *Field, yank func() any) { f.yank = yank }

// SetFieldPaste sets the paste closure.
func SetFieldPaste(f *Field, paste func(any) error) { f.paste = paste }

// SetFieldMoveUp sets the moveUp closure.
func SetFieldMoveUp(f *Field, up func()) { f.moveUp = up }

// SetFieldMoveDown sets the moveDown closure.
func SetFieldMoveDown(f *Field, down func()) { f.moveDown = down }

// SetFieldDisarm sets the disarm closure.
func SetFieldDisarm(f *Field, disarm func()) { f.disarm = disarm }

// FieldGetStr returns the getStr closure result.
func FieldGetStr(f *Field) string {
	if f.getStr == nil {
		return ""
	}
	return f.getStr()
}

// FieldGetBool returns the getBool closure result.
func FieldGetBool(f *Field) bool {
	if f.getBool == nil {
		return false
	}
	return f.getBool()
}

// FrameTitle returns the title of a frame.
func FrameTitle(f *Frame) string { return f.title }

// FrameList returns the field list of a frame.
func FrameList(f *Frame) *FieldList { return f.list }

// SetFrameTitle sets the title of a frame.
func SetFrameTitle(f *Frame, title string) { f.title = title }

// SetFrameList sets the field list of a frame.
func SetFrameList(f *Frame, list *FieldList) { f.list = list }

// StateCfg returns the config from a state.
func StateCfg(s *State) config.Config { return s.cfg }

// FieldListRefresh refreshes a field list.
func FieldListRefresh(fl *FieldList) { fl.Refresh() }

// FieldListSetSize sets the size of a field list.
func FieldListSetSize(fl *FieldList, w, h int) { fl.SetSize(w, h) }

// FieldListRows returns the rows of a field list.
func FieldListRows(fl *FieldList) []*Field { return fl.Rows() }

// FieldListUpdate updates a field list with a message.
func FieldListUpdate(fl *FieldList, msg tea.Msg) tea.Cmd { return fl.Update(msg) }

// FieldListView returns the view of a field list.
func FieldListView(fl *FieldList) string { return fl.View() }
