package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/interaction"
	"marshal/internal/skills"
)

func teaWindowSize() tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: 120, Height: 32}
}

func newDispatchModel(t *testing.T, state *session.State, opts ...Option) Model {
	t.Helper()
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, nil); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	opts = append([]Option{WithCommandRegistry(cmdReg)}, opts...)
	m := testModel(state, opts...)
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
	m := newDispatchModel(t, state,
		WithSkillIndex(idx),
		WithInteractionSetter(func(p interaction.Patch) (interaction.State, error) {
			return state.ApplyInteraction(p)
		}),
	)

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
