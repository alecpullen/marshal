package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alec/marshal/internal/loop"
	"github.com/alec/marshal/internal/skills"
)

func noopGate(_ context.Context, _ string) (string, string, error) {
	return "proceed", "", nil
}

func noopEngine(_ context.Context, _ string, _ loop.Sink, _, _ string) error { return nil }

func newTestModel() model {
	m := newModel(context.Background(), noopGate, noopEngine, skills.New(), nil, "test-session", &progRef{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return next.(model)
}

func upd(m model, msg tea.Msg) model {
	next, _ := m.Update(msg)
	return next.(model)
}

func TestModel_InitialView(t *testing.T) {
	m := newTestModel()
	if v := m.View(); strings.Contains(v, "Loading") {
		t.Errorf("unexpected 'Loading' in view after resize: %q", v)
	}
}

func TestModel_SubmitGoesToGate(t *testing.T) {
	m := newTestModel()
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello world")})
	m = upd(m, tea.KeyMsg{Type: tea.KeyEnter})

	// First entry should be the user's message; model is busy waiting for gate.
	if len(m.entries) == 0 || m.entries[0].kind != "user" {
		t.Fatalf("expected user entry, got %+v", m.entries)
	}
	if !m.busy {
		t.Error("model should be busy while gate is pending")
	}
}

func TestModel_GateProceedKeepsBusy(t *testing.T) {
	m := newTestModel()
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("add a func")})
	m = upd(m, tea.KeyMsg{Type: tea.KeyEnter})
	// Gate returns "proceed" → engine cmd is queued, model stays busy.
	m = upd(m, MarshalGateMsg{Action: "proceed", Prompt: "add a func"})
	if !m.busy {
		t.Error("model should stay busy after proceed (engine running)")
	}
}

func TestModel_GateChatUnlocks(t *testing.T) {
	m := newTestModel()
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = upd(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = upd(m, MarshalGateMsg{Action: "chat", Text: "Hey there!", Prompt: "hello"})

	if m.busy {
		t.Error("model should not be busy after chat response")
	}
	var found bool
	for _, e := range m.entries {
		if e.kind == "marshal" && strings.Contains(e.content, "Hey there!") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected marshal chat entry; entries: %+v", m.entries)
	}
}

func TestModel_GateClarifyUnlocks(t *testing.T) {
	m := newTestModel()
	m = upd(m, MarshalGateMsg{Action: "clarify", Text: "Which file should I change?", Prompt: "fix it"})
	if m.busy {
		t.Error("model should not be busy after clarify")
	}
	var found bool
	for _, e := range m.entries {
		if e.kind == "marshal" && strings.Contains(e.content, "Which file") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected marshal clarify entry; entries: %+v", m.entries)
	}
}

func TestModel_TokenMsgStreams(t *testing.T) {
	m := newTestModel()
	m = upd(m, TokenMsg{Content: "package main"})
	m = upd(m, TokenMsg{Content: "\nfunc main() {}"})
	if !strings.Contains(m.streaming.String(), "package main") {
		t.Errorf("streaming buffer = %q", m.streaming.String())
	}
}

func TestModel_VerdictPassFlushesStream(t *testing.T) {
	m := newTestModel()
	m = upd(m, TokenMsg{Content: "some code"})
	m = upd(m, VerdictMsg{Verdict: "PASS", Summary: "looks good"})
	if m.streaming.Len() != 0 {
		t.Error("streaming buffer should be flushed on verdict")
	}
	var found bool
	for _, e := range m.entries {
		if e.kind == "pass" && strings.Contains(e.content, "looks good") {
			found = true
		}
	}
	if !found {
		t.Errorf("no pass entry; entries: %+v", m.entries)
	}
}

func TestModel_TaskDoneClearsBusy(t *testing.T) {
	m := newTestModel()
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("task")})
	m = upd(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = upd(m, TaskDoneMsg{})
	if m.busy {
		t.Error("should not be busy after TaskDoneMsg")
	}
}

func TestModel_InfraErrorShown(t *testing.T) {
	m := newTestModel()
	m = upd(m, TaskDoneMsg{Err: errors.New("connection refused")})
	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "connection refused") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected system error entry; entries: %+v", m.entries)
	}
}

func TestModel_ErrTaskFailedNotShownAsError(t *testing.T) {
	m := newTestModel()
	m = upd(m, TaskDoneMsg{Err: loop.ErrTaskFailed})
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "error:") {
			t.Errorf("ErrTaskFailed should not appear as infra error; got: %q", e.content)
		}
	}
}

func TestModel_QuitOnCtrlC(t *testing.T) {
	m := newTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected a Quit command on Ctrl+C")
	}
}

func TestModel_RetryRoundAddsEntry(t *testing.T) {
	m := newTestModel()
	m = upd(m, RoundStartMsg{Round: 2, MaxRounds: 3})
	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "retrying") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected retrying entry; entries: %+v", m.entries)
	}
}

func TestModel_Round1NoRetryEntry(t *testing.T) {
	m := newTestModel()
	m = upd(m, RoundStartMsg{Round: 1, MaxRounds: 3})
	for _, e := range m.entries {
		if strings.Contains(e.content, "retrying") {
			t.Errorf("round 1 should not add retrying entry; got: %q", e.content)
		}
	}
}

// --- Slash command tests ---

func TestModel_SlashHelp(t *testing.T) {
	m := newTestModel()
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/help")})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "/ship") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected help text entry; entries: %+v", m.entries)
	}
	if m.busy {
		t.Error("model should not be busy after /help")
	}
}

func TestModel_SlashHistory_NoStore(t *testing.T) {
	m := newTestModel() // store is nil in test
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/history")})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "No history available") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'No history available' entry; entries: %+v", m.entries)
	}
	if m.busy {
		t.Error("model should not be busy after /history")
	}
}

func TestModel_SlashSkillsList_Empty(t *testing.T) {
	m := newTestModel() // empty registry
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/skills")})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "No skills loaded") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'No skills loaded' entry; entries: %+v", m.entries)
	}
}

func TestModel_SlashSkillsList_WithSkills(t *testing.T) {
	reg := skills.New()
	_ = reg.Register(&skills.Skill{Name: "test", Trigger: "/test", Description: "write tests"})
	m := newModel(context.Background(), noopGate, noopEngine, reg, nil, "test-session", &progRef{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(model)

	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/skills")})
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "/test") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected /test in skills list; entries: %+v", m.entries)
	}
}

func TestModel_SlashSkillActivation(t *testing.T) {
	var capturedExtra string
	engineFn := func(_ context.Context, _ string, _ loop.Sink, execExtra, _ string) error {
		capturedExtra = execExtra
		return nil
	}
	reg := skills.New()
	_ = reg.Register(&skills.Skill{
		Name:    "test",
		Trigger: "/test",
		Executor: skills.SkillLayer{SystemExtra: "write tests only"},
	})
	m := newModel(context.Background(), noopGate, engineFn, reg, nil, "test-session", &progRef{})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(model)

	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/test add unit tests")})
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)

	if !m.busy {
		t.Error("model should be busy after skill dispatch")
	}
	// Run the command to trigger the engine closure.
	if cmd != nil {
		cmd()
	}
	if capturedExtra != "write tests only" {
		t.Errorf("expected executor extra to be set, got %q", capturedExtra)
	}
}

func TestModel_SlashUnknown(t *testing.T) {
	m := newTestModel()
	m = upd(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/nosuchcmd")})
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(model)
	var found bool
	for _, e := range m.entries {
		if e.kind == "system" && strings.Contains(e.content, "unknown command") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown command entry; entries: %+v", m.entries)
	}
	if m.busy {
		t.Error("model should not be busy after unknown command")
	}
}
