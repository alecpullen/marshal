package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
	"marshal/internal/app/tui/picker"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
)

// TestEffortCommandSetsLevel pins that /effort <level> hands the level to the
// injected runtime setter unchanged.
func TestEffortCommandSetsLevel(t *testing.T) {
	m := newTestModel(t)
	var got string
	m.effortSetter = func(level string) (string, error) {
		got = level
		return session.EffortAppliesNextTurn, nil
	}

	updated, _ := m.dispatchCommand("/effort low")
	m = asModel(t, updated)

	if got != "low" {
		t.Fatalf("setter got %q, want %q", got, "low")
	}
	if note := lastMessageContent(m.state.Messages()); !strings.Contains(note, "Effort set to low (from the next turn).") {
		t.Fatalf("transcript note = %q, want the confirmation", note)
	}
}

// TestEffortDefaultClears pins that /effort default (and the "default" label)
// clears the override by passing the empty level to the setter.
func TestEffortDefaultClears(t *testing.T) {
	m := newTestModel(t)
	got := "unset"
	m.effortSetter = func(level string) (string, error) {
		got = level
		return session.EffortAppliesNextTurn, nil
	}

	updated, _ := m.dispatchCommand("/effort default")
	m = asModel(t, updated)

	if got != "" {
		t.Fatalf("setter got %q, want the empty level", got)
	}
	if note := lastMessageContent(m.state.Messages()); !strings.Contains(note, "Effort set to default (from the next turn).") {
		t.Fatalf("transcript note = %q, want the clearing confirmation", note)
	}
}

// TestEffortCommandShowsSetterError pins that a rejected level surfaces the
// runtime's error text instead of failing silently.
func TestEffortCommandShowsSetterError(t *testing.T) {
	m := newTestModel(t)
	m.effortSetter = func(string) (string, error) {
		return "", errors.New(`effort "max" is not supported`)
	}

	updated, _ := m.dispatchCommand("/effort max")
	m = asModel(t, updated)

	if note := lastMessageContent(m.state.Messages()); !strings.Contains(note, `effort "max" is not supported`) {
		t.Fatalf("transcript note = %q, want the setter error", note)
	}
}

// TestAltECyclesEffort pins that alt+e walks default → first level → second,
// applying each step through the setter.
func TestAltECyclesEffort(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.Models.Presets["test"] = routing.ModelPreset{
		Provider:        "p",
		Model:           "m",
		ThinkingOptions: &schema.ThinkingOptions{Levels: []string{"low", "high"}},
	}
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "test", Model: "m", Provider: "p", ReasoningCapable: true})
	var got []string
	m.effortSetter = func(level string) (string, error) {
		got = append(got, level)
		m.state.SetEffortOverride(session.EffortOverride{Level: level})
		return session.EffortAppliesNextStep, nil
	}

	for i := 0; i < 2; i++ {
		mm, _, handled := m.handleKeypress(tea.KeyPressMsg{Code: 'e', Mod: tea.ModAlt})
		if !handled {
			t.Fatal("alt+e must be handled")
		}
		m = asModel(t, mm)
	}

	if want := []string{"low", "high"}; !slices.Equal(got, want) {
		t.Fatalf("cycle applied %q, want %q", got, want)
	}
}

// TestStatusShowsTurnRouteWhileBusy pins that the footer shows the running
// turn's route and points at the pending next-turn route.
func TestStatusShowsTurnRouteWhileBusy(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "b", Model: "model-b", Provider: "prov-b"})
	m.state.SetTurnRoute(session.RouteInfo{Active: true, Preset: "a", Model: "model-a", Provider: "prov-a"})

	line := m.renderStatusLine(160)
	for _, want := range []string{"model-a", "→ model-b (next turn)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line missing %q:\n%s", want, line)
		}
	}
}

// TestStatusShowsPendingEffort pins that a running turn whose override
// retargets the effort shows the turn's level, the pending level and when it
// takes effect.
func TestStatusShowsPendingEffort(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "a", Model: "model-a", Provider: "prov-a", ReasoningCapable: true, Thinking: "high"})
	m.state.SetTurnRoute(session.RouteInfo{Active: true, Preset: "a", Model: "model-a", Provider: "prov-a", ReasoningCapable: true, Thinking: "high"})
	m.state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "prov-a", Model: "model-a"})

	line := m.renderStatusLine(160)
	if want := "think high → low (next step)"; !strings.Contains(line, want) {
		t.Fatalf("status line missing %q:\n%s", want, line)
	}
}

// TestStatusIgnoresStaleOverride pins that an override recorded for a route
// the running turn is not on is never advertised as pending: the runner drops
// it at the next turn's start (agent/effort.go sessionEffortTarget), so this
// turn will not apply it.
func TestStatusIgnoresStaleOverride(t *testing.T) {
	m := newStatusTestModel(t)
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "b", Model: "model-b", Provider: "prov-b", ReasoningCapable: true, Thinking: "high"})
	m.state.SetTurnRoute(session.RouteInfo{Active: true, Preset: "a", Model: "model-a", Provider: "prov-a", ReasoningCapable: true, Thinking: "high"})
	m.state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "prov-b", Model: "model-b"})

	line := m.renderStatusLine(160)
	if strings.Contains(line, "→ low") {
		t.Fatalf("status line advertises a pending effort the turn will not apply:\n%s", line)
	}
	if !strings.Contains(line, "think high") {
		t.Fatalf("status line should still show the turn's effort:\n%s", line)
	}
}

// TestEffortPickerMarksCurrentAndClears pins the picker's rows. The clearing
// row must carry the literal "default": the picker routing switch drops an
// empty Value before the effort case is reached, so an empty Value would make
// clearing from the picker a silent no-op.
func TestEffortPickerMarksCurrentAndClears(t *testing.T) {
	m := newTestModel(t)
	m.state.Config.Models.Presets["test"] = routing.ModelPreset{
		Provider:        "p",
		Model:           "m",
		ThinkingOptions: &schema.ThinkingOptions{Levels: []string{"low", "high"}},
	}
	m.state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "test", Model: "m", Provider: "p", ReasoningCapable: true})
	m.state.SetEffortOverride(session.EffortOverride{Level: "low", Provider: "p", Model: "m"})

	m.openEffortPicker()

	p, ok := m.dock.Panel().(*picker.Model)
	if !ok {
		t.Fatal("openEffortPicker should open a picker")
	}
	if m.pickerCommand != "effort" {
		t.Fatalf("pickerCommand = %q, want %q", m.pickerCommand, "effort")
	}
	want := []picker.Item{
		{Label: "default", Value: "default"},
		{Label: "low", Value: "low", Badge: "●"},
		{Label: "high", Value: "high"},
	}
	items := p.Items()
	if len(items) != len(want) {
		t.Fatalf("picker items = %+v, want %+v", items, want)
	}
	for i := range want {
		if items[i].Label != want[i].Label || items[i].Value != want[i].Value || items[i].Badge != want[i].Badge {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}
}
