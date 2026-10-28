package listpanel

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/chrome"
	"marshal/internal/app/tui/picker"
)

// actionResultMsg mirrors settings.actionResultMsg for in-package tests.
type actionResultMsg struct {
	FieldID string
	Label   string
}

// kp is a single-rune key helper. Special keys (Space, etc.) are constructed
// inline to avoid conflict with the "charm.land/bubbles/v2/key" import.
func kp(s string) tea.KeyPressMsg {
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func testToggleField(title string, val *bool) *Field {
	return &Field{
		ID: "test." + title, Title: title, Kind: KindToggle,
		GetBool: func() bool { return *val },
		SetBool: func(b bool) { *val = b },
	}
}

func TestFieldListNavigationAndToggle(t *testing.T) {
	a, b := false, false
	fl := NewFieldList(func() []*Field {
		return []*Field{testToggleField("Alpha", &a), testToggleField("Beta", &b)}
	})
	fl.SetSize(60, 20)

	if fl.CursorRow().Title != "Alpha" {
		t.Fatalf("cursor should start on first row, got %q", fl.CursorRow().Title)
	}
	fl.Update(kp("j"))
	if fl.CursorRow().Title != "Beta" {
		t.Fatalf("j should move to Beta, got %q", fl.CursorRow().Title)
	}
	fl.Update(kp("k"))
	fl.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	if !a {
		t.Fatal("space should toggle Alpha to true")
	}
	view := fl.View()
	if !strings.Contains(view, "Alpha") || !strings.Contains(view, "on ●") {
		t.Fatalf("view should show toggled value, got:\n%s", view)
	}
	if !strings.Contains(view, "off ○") {
		t.Fatalf("view should show Beta off, got:\n%s", view)
	}
	if !strings.Contains(view, "▸") {
		t.Fatalf("view should mark the cursor row, got:\n%s", view)
	}
}

func TestFieldListGandG(t *testing.T) {
	a, b, c := false, false, false
	fl := NewFieldList(func() []*Field {
		return []*Field{testToggleField("A", &a), testToggleField("B", &b), testToggleField("C", &c)}
	})
	fl.SetSize(60, 20)
	fl.Update(kp("G"))
	if fl.CursorRow().Title != "C" {
		t.Fatalf("G should jump to last row, got %q", fl.CursorRow().Title)
	}
	fl.Update(kp("g"))
	if fl.CursorRow().Title != "A" {
		t.Fatalf("g should jump to first row, got %q", fl.CursorRow().Title)
	}
}

func TestFieldListDescriptionShownForCursorRow(t *testing.T) {
	a := false
	f := testToggleField("Alpha", &a)
	f.Desc = "controls the alpha behavior"
	b := false
	g := testToggleField("Beta", &b)
	g.Desc = "controls the beta behavior"
	fl := NewFieldList(func() []*Field { return []*Field{f, g} })
	fl.SetSize(60, 20)
	view := fl.View()
	if !strings.Contains(view, "controls the alpha behavior") {
		t.Fatalf("cursor row description should render, got:\n%s", view)
	}
	if strings.Contains(view, "controls the beta behavior") {
		t.Fatalf("non-cursor description should NOT render, got:\n%s", view)
	}
}

func TestScalarInlineEditAppliesAndValidates(t *testing.T) {
	n := 5
	fl := NewFieldList(func() []*Field {
		return []*Field{IntField("t.n", "Count", func() int { return n }, 1, func(v int) { n = v })}
	})
	fl.SetSize(60, 20)

	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open edit
	if !fl.Editing() {
		t.Fatal("enter should open inline edit")
	}
	fl.input.SetValue("") // clear the pre-filled "5" so typing "12" gives exactly "12"
	for _, r := range "12" {
		fl.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n != 12 {
		t.Fatalf("edit should apply 12, got %d", n)
	}
	if fl.Editing() {
		t.Fatal("apply should close the edit")
	}

	// invalid input blocks apply and shows the error
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	fl.input.SetValue("abc")
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n != 12 {
		t.Fatalf("invalid input must not apply, got %d", n)
	}
	if !strings.Contains(fl.View(), "must be a number") {
		t.Fatalf("error should render, got:\n%s", fl.View())
	}
	// esc cancels without applying
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if fl.Editing() {
		t.Fatal("esc should cancel the edit")
	}
}

func TestScalarInlineEditAcceptsPaste(t *testing.T) {
	var got string
	fl := NewFieldList(func() []*Field {
		return []*Field{ScalarField("t.Paste", "Token",
			func() string { return got },
			func(v string) error { got = v; return nil })}
	})
	fl.SetSize(60, 20)

	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open edit
	if !fl.Editing() {
		t.Fatal("enter should open inline edit")
	}
	fl.Update(tea.PasteMsg{Content: "pasted-secret"})
	if fl.input.Value() != "pasted-secret" {
		t.Fatalf("paste should insert into the input, got %q", fl.input.Value())
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got != "pasted-secret" {
		t.Fatalf("pasted value should apply, got %q", got)
	}
}

func TestScalarPasteAutoOpensEdit(t *testing.T) {
	var got string
	fl := NewFieldList(func() []*Field {
		return []*Field{ScalarField("t.Paste", "Token",
			func() string { return got },
			func(v string) error { got = v; return nil })}
	})
	fl.SetSize(60, 20)

	// Paste without first pressing Enter to open edit mode.
	fl.Update(tea.PasteMsg{Content: "github:owner/repo"})
	if !fl.Editing() {
		t.Fatal("paste on a scalar field should auto-open edit mode")
	}
	if fl.input.Value() != "github:owner/repo" {
		t.Fatalf("paste should populate the input, got %q", fl.input.Value())
	}

	// Confirm with Enter and verify the value was saved.
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got != "github:owner/repo" {
		t.Fatalf("pasted value should apply after confirm, got %q", got)
	}
}

func TestScalarReadOnlyRowIgnoresEnter(t *testing.T) {
	fl := NewFieldList(func() []*Field {
		return []*Field{{ID: "t.ro", Title: "Preset", Kind: KindScalar, GetStr: func() string { return "qwen" }}}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if fl.Editing() {
		t.Fatal("read-only row must not open an edit")
	}
}

func TestMaskedRowKeepsOnEmptyAndClearsOnD(t *testing.T) {
	secret := "sk-abcd1234"
	fl := NewFieldList(func() []*Field {
		return []*Field{SecretRow("t.key", "API key", func() string { return secret }, func(v string) { secret = v })}
	})
	fl.SetSize(60, 20)
	if !strings.Contains(fl.View(), "••••1234") {
		t.Fatalf("masked value should render last four, got:\n%s", fl.View())
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // apply empty = keep
	if secret != "sk-abcd1234" {
		t.Fatalf("empty apply must keep the secret, got %q", secret)
	}
	fl.Update(kp("d"))
	if secret != "" {
		t.Fatalf("d must clear the secret, got %q", secret)
	}
}

func TestFieldListScrollsToKeepCursorVisible(t *testing.T) {
	vals := make([]bool, 30)
	fl := NewFieldList(func() []*Field {
		out := make([]*Field, 30)
		for i := range out {
			out[i] = testToggleField(strings.Repeat("x", 3)+string(rune('a'+i%26)), &vals[i])
		}
		return out
	})
	fl.SetSize(60, 8)
	fl.Update(kp("G"))
	view := fl.View()
	if len(strings.Split(view, "\n")) > 8 {
		t.Fatalf("view must not exceed height 8, got %d lines", len(strings.Split(view, "\n")))
	}
	if !strings.Contains(view, "▸") {
		t.Fatalf("cursor row must remain visible after G, got:\n%s", view)
	}
	if !strings.Contains(view, "↑") {
		t.Fatalf("expected ↑ more indicator when scrolled down, got:\n%s", view)
	}
}

func TestEnumCycleWithArrows(t *testing.T) {
	v := "deny"
	fl := NewFieldList(func() []*Field {
		return []*Field{EnumField("t.e", "Guardrail", []string{"deny", "confirm", "allow"},
			func() string { return v }, func(s string) { v = s })}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if v != "confirm" {
		t.Fatalf("right should cycle deny→confirm, got %q", v)
	}
	fl.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if v != "deny" {
		t.Fatalf("left should cycle back to deny, got %q", v)
	}
}

func TestEnumPickerSelectsOption(t *testing.T) {
	v := "deny"
	fl := NewFieldList(func() []*Field {
		return []*Field{EnumField("t.e", "Guardrail", []string{"deny", "confirm", "allow"},
			func() string { return v }, func(s string) { v = s })}
	})
	fl.SetSize(60, 20)
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open picker
	if !fl.Editing() {
		t.Fatal("enter should open the picker")
	}
	view := fl.View()
	if !strings.Contains(view, "confirm") || !strings.Contains(view, "allow") {
		t.Fatalf("picker should list all options, got:\n%s", view)
	}
	fl.Update(kp("j"))
	fl.Update(kp("j"))
	fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v != "allow" {
		t.Fatalf("picker should apply allow, got %q", v)
	}
	if fl.Editing() {
		t.Fatal("picker should close after apply")
	}
}

func TestFieldListCursorAfterListDrillAdd(t *testing.T) {
	items := []string{"alpha", "beta"}
	root := NewFrame("Test", func() []*Field {
		return []*Field{ListDrill("t.List", "Items", &items)}
	})
	ps := NewPaneStack(root)
	ps.Top().List.SetSize(60, 20)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill

	// cursor starts on row 0 ("alpha")
	if got := ps.Top().List.CursorRow().Title; got != "alpha" {
		t.Fatalf("expected cursor on 'alpha', got %q", got)
	}

	// add "gamma" via prompt path
	ps.Update(kp("a"))
	for _, r := range "gamma" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// cursor should be on the new last row
	rows := ps.Top().List.Rows()
	want := len(rows) - 1
	if got := ps.Top().List.Cursor(); got != want {
		t.Fatalf("cursor after list add: want %d (last), got %d", want, got)
	}
	if got := ps.Top().List.CursorRow().Title; got != "gamma" {
		t.Fatalf("cursor row after list add: want 'gamma', got %q", got)
	}
}

func TestKindPickerRowPassesAllowCustomAndTitleToPickerRequest(t *testing.T) {
	t.Run("default allowCustom is false", func(t *testing.T) {
		f := &Field{
			ID:          "test.picker",
			Title:       "My Picker",
			Kind:        KindPicker,
			PickOptions: func() []picker.Item { return nil },
			PickOnPick:  func(string) error { return nil },
		}
		fl := NewFieldList(func() []*Field { return []*Field{f} })
		fl.SetSize(60, 20)
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open row
		req := fl.TakePushPicker()
		if req == nil {
			t.Fatal("KindPicker Enter should produce a pickerRequest")
		}
		if req.allowCustom {
			t.Error("default pickAllowCustom=false should produce allowCustom=false")
		}
		if req.title != "My Picker" {
			t.Errorf("pickerRequest title = %q, want %q", req.title, "My Picker")
		}
	})
	t.Run("allowCustom=true is passed through", func(t *testing.T) {
		f := &Field{
			ID:              "test.picker",
			Title:           "My Picker",
			Kind:            KindPicker,
			PickAllowCustom: true,
			PickOptions:     func() []picker.Item { return nil },
			PickOnPick:      func(string) error { return nil },
		}
		fl := NewFieldList(func() []*Field { return []*Field{f} })
		fl.SetSize(60, 20)
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open row
		req := fl.TakePushPicker()
		if req == nil {
			t.Fatal("KindPicker Enter should produce a pickerRequest")
		}
		if !req.allowCustom {
			t.Error("pickAllowCustom=true should produce allowCustom=true")
		}
	})
}

func TestFieldListCursorAfterMapIntDrillAdd(t *testing.T) {
	m := map[string]int{"reviewer": 4}
	root := NewFrame("Swarm", func() []*Field {
		return []*Field{MapIntDrill("swarm.tool_iters", "Tool iters", &m)}
	})
	ps := NewPaneStack(root)
	ps.Top().List.SetSize(60, 20)
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // drill

	// cursor starts on row 0 ("reviewer")
	if got := ps.Top().List.CursorRow().Title; got != "reviewer" {
		t.Fatalf("expected cursor on 'reviewer', got %q", got)
	}

	// add "zebra" (sorts after "reviewer", so cursor must jump from 0 to 1)
	ps.Update(kp("a"))
	for _, r := range "zebra" {
		ps.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	ps.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if _, ok := m["zebra"]; !ok {
		t.Fatalf("add should create the key, got %v", m)
	}

	// cursor should be on "zebra" (row 1, after "reviewer")
	if got := ps.Top().List.CursorRow().Title; got != "zebra" {
		t.Fatalf("cursor row after map add: want 'zebra', got %q", got)
	}
	if got := ps.Top().List.Cursor(); got != 1 {
		t.Fatalf("cursor index after map add: want 1, got %d", got)
	}
}

func TestKindActionEnterTriggersActAndReturnsCmd(t *testing.T) {
	fl := NewFieldList(func() []*Field {
		return []*Field{
			{
				ID:       "test.action",
				Title:    "Run",
				Kind:     KindAction,
				ActLabel: func() string { return "idle" },
				Act: func() tea.Cmd {
					return func() tea.Msg {
						return actionResultMsg{FieldID: "test.action", Label: "done"}
					}
				},
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	cmd := fl.Update(tea.KeyPressMsg{Text: "enter"})
	if cmd == nil {
		t.Fatal("KindAction Enter should return a non-nil Cmd")
	}
	msg := cmd()
	arm, ok := msg.(actionResultMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want actionResultMsg", msg)
	}
	if arm.FieldID != "test.action" || arm.Label != "done" {
		t.Fatalf("actionResultMsg = %+v, want {test.action done}", arm)
	}
}

func TestYankPasteDuplicates(t *testing.T) {
	got := ""
	fl := NewFieldList(func() []*Field {
		return []*Field{
			{ID: "x.a", Title: "A", Kind: KindScalar,
				GetStr: func() string { return "a" },
				SetStr: func(v string) error { got = v; return nil },
				Yank:   func() any { return "a-data" },
				Paste:  func(data any) error { got = data.(string) + "-pasted"; return nil },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	fl.Update(tea.KeyPressMsg{Text: "y"})
	if fl.yankedData != "a-data" {
		t.Fatalf("Yank: yankedData = %v, want a-data", fl.yankedData)
	}
	fl.Update(tea.KeyPressMsg{Text: "p"})
	if got != "a-data-pasted" {
		t.Fatalf("Paste: got = %q, want a-data-pasted", got)
	}
	if fl.yankedData != nil {
		t.Fatal("paste should clear the yank buffer")
	}
}

func TestMoveUpDownCallsClosures(t *testing.T) {
	calls := []string{}
	fl := NewFieldList(func() []*Field {
		return []*Field{
			{ID: "x.a", Title: "A", Kind: KindScalar,
				GetStr: func() string { return "a" }, SetStr: func(string) error { return nil },
				MoveUp:   func() { calls = append(calls, "up") },
				MoveDown: func() { calls = append(calls, "down") },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()

	fl.Update(tea.KeyPressMsg{Text: "shift+up"})
	fl.Update(tea.KeyPressMsg{Text: "shift+down"})
	want := []string{"up", "down"}
	if len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestDisarmCalledOnCursorMove(t *testing.T) {
	disarmed := false
	fl := NewFieldList(func() []*Field {
		return []*Field{
			{ID: "x.a", Title: "A", Kind: KindScalar,
				GetStr: func() string { return "a" }, SetStr: func(string) error { return nil },
				Disarm: func() { disarmed = true },
			},
			{ID: "x.b", Title: "B", Kind: KindScalar,
				GetStr: func() string { return "b" }, SetStr: func(string) error { return nil },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()
	fl.SetCursor(0)

	fl.Update(tea.KeyPressMsg{Text: "j"})
	if !disarmed {
		t.Fatal("leaving row A via j should call its disarm")
	}
}

func TestHeaderRowRendersAndCursorSkips(t *testing.T) {
	a, b := false, false
	fl := NewFieldList(func() []*Field {
		return []*Field{
			{ID: "header.x", Title: "Section X", Kind: KindHeader},
			testToggleField("Alpha", &a),
			{ID: "header.y", Title: "Section Y", Kind: KindHeader},
			testToggleField("Beta", &b),
		}
	})
	fl.SetSize(60, 20)

	// Cursor should start on the first non-header row (Alpha).
	if fl.CursorRow().Title != "Alpha" {
		t.Fatalf("cursor should start on Alpha, got %q", fl.CursorRow().Title)
	}

	// View should contain both section headers.
	view := fl.View()
	if !strings.Contains(view, "Section X") {
		t.Fatalf("view should contain Section X header, got:\n%s", view)
	}
	if !strings.Contains(view, "Section Y") {
		t.Fatalf("view should contain Section Y header, got:\n%s", view)
	}

	// Navigate down: should skip Section Y header and land on Beta.
	fl.Update(kp("j"))
	if fl.CursorRow().Title != "Beta" {
		t.Fatalf("j should skip Section Y header and land on Beta, got %q", fl.CursorRow().Title)
	}

	// Navigate up: should skip Section X header and land on Alpha.
	fl.Update(kp("k"))
	if fl.CursorRow().Title != "Alpha" {
		t.Fatalf("k should skip Section X header and land on Alpha, got %q", fl.CursorRow().Title)
	}

	// g should land on Alpha (skipping Section X header).
	fl.Update(kp("g"))
	if fl.CursorRow().Title != "Alpha" {
		t.Fatalf("g should land on Alpha, got %q", fl.CursorRow().Title)
	}

	// G should land on Beta (skipping Section Y header).
	fl.Update(kp("G"))
	if fl.CursorRow().Title != "Beta" {
		t.Fatalf("G should land on Beta, got %q", fl.CursorRow().Title)
	}
}

func TestGroupDividersDrawHeaderRowsAsDividers(t *testing.T) {
	a := false
	newList := func() *FieldList {
		return NewFieldList(func() []*Field {
			return []*Field{
				{ID: "header.x", Title: "Section X", Kind: KindHeader},
				testToggleField("Alpha", &a),
			}
		})
	}

	fl := newList()
	fl.SetSize(40, 10)
	FieldListSetGroupDividers(fl, true)
	if view := fl.View(); !strings.Contains(view, chrome.Divider("Section X")) {
		t.Fatalf("with dividers on, the header row should be chrome.Divider, got:\n%q", view)
	}

	plain := newList()
	plain.SetSize(40, 10)
	FieldListSetGroupDividers(plain, false)
	if view := plain.View(); strings.Contains(view, chrome.Divider("Section X")) {
		t.Fatalf("with dividers off, the header row should not be a divider, got:\n%q", view)
	}
}

func TestDisarmCurrent(t *testing.T) {
	disarmed := false
	fl := NewFieldList(func() []*Field {
		return []*Field{
			{ID: "x.a", Title: "A", Kind: KindScalar,
				GetStr: func() string { return "a" }, SetStr: func(string) error { return nil },
				Disarm: func() { disarmed = true },
			},
		}
	})
	fl.SetSize(40, 10)
	fl.Refresh()
	fl.SetCursor(0)

	fl.DisarmCurrent()
	if !disarmed {
		t.Fatal("DisarmCurrent should call the cursor row's disarm")
	}
}

func TestValueCellUsesDisplayOverride(t *testing.T) {
	stored := "deep"
	row := &Field{
		ID:      "profiles.coding.reviewer",
		Title:   "Reviewer",
		Kind:    KindPicker,
		GetStr:  func() string { return stored },
		Display: func() (string, string) { return "claude-opus-4-5", "← base" },
	}
	fl := NewFieldList(func() []*Field { return []*Field{row} })
	fl.SetSize(80, 10)
	out := fl.View()
	if !strings.Contains(out, "claude-opus-4-5") {
		t.Errorf("view must render the Display value, got:\n%s", out)
	}
	if !strings.Contains(out, "← base") {
		t.Errorf("view must render the Display badge, got:\n%s", out)
	}
	if row.GetStr() != "deep" {
		t.Error("Display must not disturb GetStr — /set and diffing depend on it")
	}
}

func TestValueCellWithoutDisplayIsUnchanged(t *testing.T) {
	row := &Field{
		ID:     "presets.main.model",
		Title:  "Model",
		Kind:   KindPicker,
		GetStr: func() string { return "gpt-4o" },
	}
	fl := NewFieldList(func() []*Field { return []*Field{row} })
	fl.SetSize(80, 10)
	if out := fl.View(); !strings.Contains(out, "gpt-4o") {
		t.Errorf("nil Display must preserve GetStr rendering, got:\n%s", out)
	}
}

// --- physical-line height budget -------------------------------------------
//
// chrome.Row stacks onto two lines when the right column would starve the
// label, so a view that budgets elements instead of physical lines renders
// taller than the height the dock handed the pane. These tests pin the
// physical budget and which line the window keeps.

// flWidth is narrow enough that every scalar row stacks: chrome.Row gives up
// on one line once the right column would leave the label under
// chrome.StackThreshold cells.
const flWidth = 20

// viewRows splits a rendered view into physical rows; "" renders no rows.
func viewRows(view string) []string {
	if view == "" {
		return nil
	}
	return strings.Split(view, "\n")
}

// stackedScalarFields builds n scalar rows titled and valued with double-width
// runes, so the window also has to budget cells rather than runes.
func stackedScalarFields(n int) []*Field {
	out := make([]*Field, 0, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("項目%d", i+1)
		value := fmt.Sprintf("値%d", i+1)
		out = append(out, ScalarField(fmt.Sprintf("t.f%d", i), title,
			func() string { return value }, func(string) error { return nil }))
	}
	return out
}

func TestFieldListPhysicalHeightAndFocus(t *testing.T) {
	const n = 5
	build := func() *FieldList {
		fields := append([]*Field{{ID: "header.top", Title: "設定", Kind: KindHeader}}, stackedScalarFields(n)...)
		fl := NewFieldList(func() []*Field { return fields })
		fl.SetSize(flWidth, 40)
		return fl
	}

	t.Run("stacked rows spend two physical rows each", func(t *testing.T) {
		fl := build()
		fl.rows[1].Desc = "説明"
		fl.Refresh()
		rows := viewRows(fl.View())
		// One header + n stacked rows (two lines each) + the cursor row's
		// description: element count would be n+2, physical height 2n+2.
		if want := 2*n + 2; len(rows) != want {
			t.Fatalf("rendered %d physical rows, want %d:\n%s", len(rows), want, fl.View())
		}
		if plain := ansi.Strip(fl.View()); strings.Contains(plain, "more") {
			t.Fatalf("nothing is clipped at height 40, got indicators:\n%s", plain)
		}
	})

	t.Run("height 0 renders nothing", func(t *testing.T) {
		fl := build()
		fl.SetSize(flWidth, 0)
		if got := fl.View(); got != "" {
			t.Fatalf("height 0 rendered %q, want the empty string", got)
		}
	})

	t.Run("height 1 shows the focus line whole and unindicated", func(t *testing.T) {
		fl := build()
		fl.Update(kp("G"))
		fl.SetSize(flWidth, 1)
		rows := viewRows(fl.View())
		if len(rows) != 1 {
			t.Fatalf("height 1 rendered %d rows, want 1:\n%s", len(rows), fl.View())
		}
		plain := ansi.Strip(rows[0])
		if !strings.Contains(plain, chrome.SelectionMarker) {
			t.Fatalf("height 1 dropped the selection, got %q", plain)
		}
		if strings.Contains(plain, "more") {
			t.Fatalf("height 1 spent its only row on an indicator: %q", plain)
		}
	})

	t.Run("every height keeps the selection visible", func(t *testing.T) {
		for _, height := range []int{2, 3, 4, 5, 6} {
			for _, pos := range []string{"first", "middle", "last"} {
				fl := build()
				switch pos {
				case "first":
					fl.Update(kp("g"))
				case "middle":
					for i := 0; i < n/2; i++ {
						fl.Update(kp("j"))
					}
				case "last":
					fl.Update(kp("G"))
				}
				fl.SetSize(flWidth, height)
				view := fl.View()
				rows := viewRows(view)
				if len(rows) > height {
					t.Fatalf("height %d %s: rendered %d rows:\n%s", height, pos, len(rows), view)
				}
				plain := ansi.Strip(view)
				if !strings.Contains(plain, chrome.SelectionMarker) {
					t.Fatalf("height %d %s: selection off-window:\n%s", height, pos, plain)
				}
				if pos == "first" && strings.Contains(plain, "↑ more") {
					t.Fatalf("height %d: ↑ more above the first row:\n%s", height, plain)
				}
				for i, row := range rows {
					if w := ansi.StringWidth(row); w > flWidth {
						t.Fatalf("height %d %s row %d is %d cells, want <= %d: %q",
							height, pos, i, w, flWidth, ansi.Strip(row))
					}
				}
			}
		}
	})

	t.Run("resize rebudgets the physical window", func(t *testing.T) {
		fl := build()
		fl.Update(kp("G"))
		for _, height := range []int{2, 5, 9} {
			fl.SetSize(flWidth, height)
			if got := len(viewRows(fl.View())); got > height {
				t.Fatalf("height %d rendered %d rows:\n%s", height, got, fl.View())
			}
		}
		// Widened past the stacking threshold the same fields render one row
		// each, so the same height holds more of them.
		fl.SetSize(120, 4)
		rows := viewRows(fl.View())
		if len(rows) > 4 {
			t.Fatalf("height 4 at width 120 rendered %d rows:\n%s", len(rows), fl.View())
		}
		if !strings.Contains(ansi.Strip(fl.View()), chrome.SelectionMarker) {
			t.Fatalf("selection off-window after resize:\n%s", fl.View())
		}
	})
}

func TestFieldListFocusedEditorAndPickerVisible(t *testing.T) {
	t.Run("the scalar editor stays visible in a stacked list", func(t *testing.T) {
		fl := NewFieldList(func() []*Field { return stackedScalarFields(4) })
		fl.SetSize(flWidth, 3)
		fl.Update(kp("G"))
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the inline edit
		if !fl.editing {
			t.Fatal("enter should open the inline editor")
		}
		fl.Update(kp("z")) // the editor now holds a value no other row renders
		view := fl.View()
		if got := len(viewRows(view)); got > 3 {
			t.Fatalf("rendered %d rows, want <= 3:\n%s", got, view)
		}
		plain := ansi.Strip(view)
		if !strings.Contains(plain, "値4z") {
			t.Fatalf("the editor scrolled out of the window:\n%s", plain)
		}
		if !strings.Contains(plain, chrome.SelectionMarker) {
			t.Fatalf("the edited row lost its marker:\n%s", plain)
		}
	})

	t.Run("a failed setter keeps the error under the visible editor", func(t *testing.T) {
		fl := NewFieldList(func() []*Field {
			return []*Field{ScalarField("t.bad", "項目",
				func() string { return "元" },
				func(string) error { return fmt.Errorf("拒否") })}
		})
		fl.SetSize(flWidth, 3)
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		fl.Update(kp("z"))
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // rejected, editor stays open
		view := fl.View()
		if got := len(viewRows(view)); got > 3 {
			t.Fatalf("rendered %d rows, want <= 3:\n%s", got, view)
		}
		plain := ansi.Strip(view)
		if !strings.Contains(plain, "拒否") {
			t.Fatalf("the error row scrolled out of the window:\n%s", plain)
		}
		if !strings.Contains(plain, "元z") {
			t.Fatalf("the still-open editor scrolled out of the window:\n%s", plain)
		}
	})

	t.Run("the add prompt is focused below the window", func(t *testing.T) {
		fl := NewFieldList(func() []*Field { return stackedScalarFields(3) })
		fl.OnAdd = func(string) error { return nil }
		fl.KeyPrompt = "name"
		fl.SetSize(flWidth, 2)
		fl.Update(kp("a"))
		if !fl.adding {
			t.Fatal("a should open the add prompt")
		}
		for _, r := range "zebra" {
			fl.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		view := fl.View()
		rows := viewRows(view)
		if len(rows) != 2 {
			t.Fatalf("rendered %d rows, want 2 (prompt + indicator):\n%s", len(rows), view)
		}
		plain := ansi.Strip(view)
		if !strings.Contains(plain, "name:") || !strings.Contains(plain, "zebra") {
			t.Fatalf("the add prompt scrolled out of the window:\n%s", plain)
		}
	})

	t.Run("the picker follows the selected option below the window", func(t *testing.T) {
		opts := []string{"deny", "confirm", "allow"}
		value := "deny"
		fields := append(stackedScalarFields(3),
			EnumField("t.e", "Guardrail", opts, func() string { return value }, func(v string) { value = v }))
		fl := NewFieldList(func() []*Field { return fields })
		fl.SetSize(flWidth, 3)
		fl.SetCursor(len(fields) - 1)
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // open the picker
		if !fl.picking {
			t.Fatal("enter should open the enum picker")
		}
		for i, want := range opts {
			view := fl.View()
			if got := len(viewRows(view)); got > 3 {
				t.Fatalf("pickIdx %d rendered %d rows, want <= 3:\n%s", i, got, view)
			}
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "▸ "+want) {
				t.Fatalf("pickIdx %d scrolled the selected option out of the window:\n%s", i, plain)
			}
			if i == len(opts)-1 && strings.Contains(plain, "Guardrail") {
				t.Fatalf("the window kept the row instead of following the picker:\n%s", plain)
			}
			if i < len(opts)-1 {
				fl.Update(kp("j"))
			}
		}
		fl.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if value != "allow" {
			t.Fatalf("picker applied %q, want allow", value)
		}
		if fl.picking {
			t.Fatal("the picker should close after apply")
		}
		// The committed value is back on the row the window focuses.
		fl.SetSize(flWidth, 4)
		if plain := ansi.Strip(fl.View()); !strings.Contains(plain, "allow") {
			t.Fatalf("the committed value is not visible:\n%s", plain)
		}
	})
}
