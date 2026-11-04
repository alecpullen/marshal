package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui/docpanel"
)

// A doc the TUI builds resolves its IDs against the map it was given: a known
// ID runs its entry, and an ID the map no longer holds says so instead of
// panicking on a missing key.
func TestLocalDocActionsRunTheirEntry(t *testing.T) {
	act := localDocActions(map[string]func() acpwire.CommandResult{
		"1": func() acpwire.CommandResult { return acpwire.CommandResult{Text: "done"} },
	})

	msg, ok := act("1")().(docActionMsg)
	if !ok {
		t.Fatalf("known ID produced %T, want docActionMsg", act("1")())
	}
	if msg.err != nil {
		t.Fatalf("known ID error = %v, want nil", msg.err)
	}
	if msg.result.Text != "done" {
		t.Errorf("result text = %q, want %q", msg.result.Text, "done")
	}
	if msg.act == nil {
		t.Error("the message must carry the act, so a doc it opens keeps working")
	}

	missing, ok := act("nope")().(docActionMsg)
	if !ok {
		t.Fatalf("unknown ID produced %T, want docActionMsg", act("nope")())
	}
	if !errors.Is(missing.err, errUnknownDocAction) {
		t.Errorf("unknown ID error = %v, want errUnknownDocAction", missing.err)
	}
}

// A doc that an action returns opens a panel whose own rows still act through
// the same function, so a second round of actions works.
func TestADocActionsDocKeepsItsActions(t *testing.T) {
	var seen []string
	var act func(actionID string) tea.Cmd
	act = func(actionID string) tea.Cmd {
		return func() tea.Msg {
			seen = append(seen, actionID)
			return docActionMsg{result: acpwire.CommandResult{Text: "ran " + actionID}, act: act}
		}
	}

	m := newViewTestModel(t, 80, 24)
	updated, _ := m.Update(docActionMsg{
		result: acpwire.CommandResult{Doc: &acpwire.WireDoc{
			Title: "Things",
			Rows:  []acpwire.WireRow{{Text: "row", ActionLabel: "↵ run", ActionID: "a1"}},
		}},
		act: act,
	})
	m = asModel(t, updated)

	if !m.dock.IsOpen() {
		t.Fatal("a doc result should open a doc panel")
	}
	panel, ok := m.dock.Panel().(*docpanel.Panel)
	if !ok {
		t.Fatalf("dock holds %T, want *docpanel.Panel", m.dock.Panel())
	}
	panel.View(100, 20)
	cmd := panel.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the action row returned no command")
	}
	if _, ok := cmd().(docActionMsg); !ok {
		t.Fatalf("action produced %T, want a docActionMsg through the carried act", cmd())
	}
	if len(seen) != 1 || seen[0] != "a1" {
		t.Fatalf("act saw %v, want [a1]", seen)
	}
}
