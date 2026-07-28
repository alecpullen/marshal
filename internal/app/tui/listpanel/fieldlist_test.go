package listpanel

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
