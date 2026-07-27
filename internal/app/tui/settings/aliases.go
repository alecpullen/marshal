package settings

import (
	"charm.land/lipgloss/v2"

	"marshal/internal/app/tui/listpanel"
	"marshal/internal/app/tui/picker"
)

// This file re-exports the list engine that moved to listpanel, so the
// settings package's remaining code, its test suite, and external consumers
// (agents) keep their existing identifiers. Additive only — no behavior.

type (
	field          = listpanel.Field
	fieldList      = listpanel.FieldList
	frame          = listpanel.Frame
	paneStack      = listpanel.PaneStack
	Field          = listpanel.Field
	FieldList      = listpanel.FieldList
	Frame          = listpanel.Frame
	PaneStack      = listpanel.PaneStack
	fieldKind      = listpanel.FieldKind
	entriesOpts    = listpanel.EntriesOpts
	yankedMapEntry = listpanel.YankedMapEntry
	PickerRequest  = listpanel.PickerRequest
)

const (
	kindToggle = listpanel.KindToggle
	kindScalar = listpanel.KindScalar
	kindEnum   = listpanel.KindEnum
	kindDrill  = listpanel.KindDrill
	kindAction = listpanel.KindAction
	kindPicker = listpanel.KindPicker
	kindHeader = listpanel.KindHeader

	KindToggle = listpanel.KindToggle
	KindScalar = listpanel.KindScalar
	KindEnum   = listpanel.KindEnum
	KindDrill  = listpanel.KindDrill
	KindAction = listpanel.KindAction
	KindPicker = listpanel.KindPicker
	KindHeader = listpanel.KindHeader
)

var (
	newFieldList       = listpanel.NewFieldList
	newFrame           = listpanel.NewFrame
	newCollectionFrame = listpanel.NewCollectionFrame
	newPaneStack       = listpanel.NewPaneStack
	scalarField        = listpanel.ScalarField
	intField           = listpanel.IntField
	enumField          = listpanel.EnumField
	secretRow          = listpanel.SecretRow
	intSetter          = listpanel.IntSetter
	durationSetter     = listpanel.DurationSetter
	mapStringDrill     = listpanel.MapStringDrill
	mapIntDrill        = listpanel.MapIntDrill
	entriesDrill       = listpanel.EntriesDrill
	entriesDrillExt    = listpanel.EntriesDrillExt
	listDrill          = listpanel.ListDrill
	listDrillExt       = listpanel.ListDrillExt
	maskKey            = listpanel.MaskKey
	flDescStyle        = listpanel.DescStyle

	NewField                   = listpanel.NewField
	FieldID                    = listpanel.FieldID
	FieldTitle                 = listpanel.FieldTitle
	FieldDesc                  = listpanel.FieldDesc
	SetFieldID                 = listpanel.SetFieldID
	SetFieldTitle              = listpanel.SetFieldTitle
	SetFieldDesc               = listpanel.SetFieldDesc
	SetFieldKind               = listpanel.SetFieldKind
	SetFieldGetStr             = listpanel.SetFieldGetStr
	SetFieldSetStr             = listpanel.SetFieldSetStr
	SetFieldGetBool            = listpanel.SetFieldGetBool
	SetFieldSetBool            = listpanel.SetFieldSetBool
	SetFieldOptions            = listpanel.SetFieldOptions
	SetFieldSummary            = listpanel.SetFieldSummary
	SetFieldBuild              = listpanel.SetFieldBuild
	SetFieldDel                = listpanel.SetFieldDel
	SetFieldAct                = listpanel.SetFieldAct
	SetFieldActLabel           = listpanel.SetFieldActLabel
	SetFieldPickOptions        = listpanel.SetFieldPickOptions
	SetFieldPickOnPick         = listpanel.SetFieldPickOnPick
	SetFieldPickAllowCustom    = listpanel.SetFieldPickAllowCustom
	SetFieldKeywords           = listpanel.SetFieldKeywords
	SetFieldWarn               = listpanel.SetFieldWarn
	SetFieldMasked             = listpanel.SetFieldMasked
	SetFieldYank               = listpanel.SetFieldYank
	SetFieldPaste              = listpanel.SetFieldPaste
	SetFieldMoveUp             = listpanel.SetFieldMoveUp
	SetFieldMoveDown           = listpanel.SetFieldMoveDown
	SetFieldDisarm             = listpanel.SetFieldDisarm
	FieldGetStr                = listpanel.FieldGetStr
	FieldGetBool               = listpanel.FieldGetBool
	FrameTitle                 = listpanel.FrameTitle
	FrameList                  = listpanel.FrameList
	SetFrameTitle              = listpanel.SetFrameTitle
	SetFrameList               = listpanel.SetFrameList
	FieldListRefresh           = listpanel.FieldListRefresh
	FieldListSetSize           = listpanel.FieldListSetSize
	FieldListRows              = listpanel.FieldListRows
	FieldListUpdate            = listpanel.FieldListUpdate
	FieldListCommitted         = listpanel.FieldListCommitted
	FieldListTakePushPicker    = listpanel.FieldListTakePushPicker
	FieldListView              = listpanel.FieldListView
	FieldListSetDescSuppressed = listpanel.FieldListSetDescSuppressed
	FieldListCursorDesc        = listpanel.FieldListCursorDesc
	NewFieldList               = listpanel.NewFieldList
	NewFrame                   = listpanel.NewFrame
)

// FieldKind returns the kind of a field. It shadows the type alias so that
// callers like agents/panel.go can write settings.FieldKind(f).
func FieldKind(f *Field) listpanel.FieldKind { return f.Kind }

// Generic functions cannot be var-assigned without instantiation; wrap them.
func mapDrill[T any](id, title string, values *map[string]T, parse func(string) (T, error), format func(T) string) *field {
	return listpanel.MapDrill(id, title, values, parse, format)
}

func sliceOpts[T any](items *[]T) entriesOpts { return listpanel.SliceOpts(items) }

func sortedKeys[T any](m map[string]T) []string { return listpanel.SortedKeys(m) }

// Keep referenced imports alive if the alias set shrinks during cleanup.
var (
	_ = lipgloss.NewStyle
	_ = picker.Item{}
)
