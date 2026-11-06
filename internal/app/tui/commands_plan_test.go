package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/skills"
)

func teaWindowSize() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: 120, Height: 32}
}

func newDispatchModel(t *testing.T, state *session.State, opts ...Option) Model {
	t.Helper()
	return newDispatchModelWith(t, inproc.Deps{State: state}, opts...)
}

// newDispatchModelWith is newDispatchModel over an adapter made from d.
func newDispatchModelWith(t *testing.T, d inproc.Deps, opts ...Option) Model {
	t.Helper()
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, nil); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	if d.Commands == nil {
		d.Commands = cmdReg
	}
	m := testModelWith(d, opts...)
	updated, _ := m.Update(teaWindowSize())
	return asModel(t, updated)
}

// Selecting a canonical mode must not load the retired planning skill. Planning
// is an explicit skill workflow now, never a side effect of the mode.
func TestModeSelectionDoesNotLoadPlanningSkill(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	idx := skills.NewIndex()
	idx.Set("marshal-writing-plans", skills.Skill{
		Name:        "marshal-writing-plans",
		Description: "author a plan",
		Body:        "Write the plan.",
	})
	m := newDispatchModelWith(t, inproc.Deps{
		State:          state,
		SetInteraction: state.ApplyInteraction,
		SkillIndex:     func() *skills.Index { return idx },
	})

	updated, _ := m.dispatchCommand("/act")
	m = asModel(t, updated)
	if m.state.HasActiveSkill("marshal-writing-plans") {
		t.Fatal("/act must not load the writing-plans skill")
	}

	m.cycleMode(true)
	if m.state.HasActiveSkill("marshal-writing-plans") {
		t.Fatal("Tab cycling must not load the writing-plans skill")
	}
}
