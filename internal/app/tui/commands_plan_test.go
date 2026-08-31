package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
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
	m := New(state, opts...)
	updated, _ := m.Update(teaWindowSize())
	return asModel(t, updated)
}

// /plan pairs Plan mode with the writing-plans skill so inline plans follow
// the house format and chain into marshal-executing-plans.
func TestPlanCommandLoadsWritingPlansSkill(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	idx := skills.NewIndex()
	idx.Set("marshal-writing-plans", skills.Skill{
		Name:        "marshal-writing-plans",
		Description: "author a plan",
		Body:        "Write the plan.",
	})
	m := newDispatchModel(t, state, WithSkillIndex(idx))

	updated, _ := m.dispatchCommand("/plan")
	m = asModel(t, updated)

	if !m.state.HasActiveSkill("marshal-writing-plans") {
		t.Fatal("/plan should activate marshal-writing-plans")
	}
	// The load is quiet: no ContentTypeSkill tag, only the body.
	for _, msg := range m.state.Messages() {
		if msg.ContentType == session.ContentTypeSkill {
			t.Fatalf("/plan should not post a skill tag, got %q", msg.Content)
		}
	}
}

// A missing or renamed skill must never block the mode switch.
func TestPlanCommandToleratesMissingWritingPlansSkill(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := newDispatchModel(t, state, WithSkillIndex(skills.NewIndex()))

	updated, _ := m.dispatchCommand("/plan")
	m = asModel(t, updated)

	if m.approvalMode != policy.ModePlan {
		t.Fatalf("mode = %v, want plan", m.approvalMode)
	}
	if m.state.HasActiveSkill("marshal-writing-plans") {
		t.Fatal("nothing should be active when the skill is absent")
	}
}

// Tab/Shift+Tab cycling into Plan mode pairs the skill exactly like /plan
// does — the load lives in setMode, so every entry point behaves the same.
func TestCycleModeIntoPlanLoadsWritingPlansSkill(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	idx := skills.NewIndex()
	idx.Set("marshal-writing-plans", skills.Skill{
		Name:        "marshal-writing-plans",
		Description: "author a plan",
		Body:        "Write the plan.",
	})
	m := newDispatchModel(t, state, WithSkillIndex(idx))
	if m.approvalMode != policy.ModeDefault {
		t.Fatalf("precondition: mode = %v, want default", m.approvalMode)
	}

	m.cycleMode(true) // default -> edit
	if m.state.HasActiveSkill("marshal-writing-plans") {
		t.Fatal("non-plan cycle must not load the writing-plans skill")
	}

	m.cycleMode(true) // edit -> copilot
	m.cycleMode(true) // copilot -> auto
	m.cycleMode(true) // auto -> plan

	if m.approvalMode != policy.ModePlan {
		t.Fatalf("mode = %v, want plan after cycling", m.approvalMode)
	}
	if !m.state.HasActiveSkill("marshal-writing-plans") {
		t.Fatal("cycling into plan mode should activate marshal-writing-plans")
	}
}

// A nil skill index (model built without one) must not panic.
func TestPlanCommandNilSkillIndex(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := newDispatchModel(t, state)

	updated, _ := m.dispatchCommand("/plan")
	m = asModel(t, updated)

	if m.approvalMode != policy.ModePlan {
		t.Fatalf("mode = %v, want plan", m.approvalMode)
	}
}
