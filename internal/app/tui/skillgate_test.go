package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// keyPress is a helper that wraps a key string into a tea.KeyPressMsg.
func skillGateKeyPress(key string) tea.KeyPressMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	default:
		return tea.KeyPressMsg{Code: rune(key[0])}
	}
}

func TestSkillGateModelNavigation(t *testing.T) {
	sg := &session.PendingSkillGate{Skill: "review"}
	gm := newSkillGateModel(sg, 80)

	// down walks 0 → 1 → 2 → 3 and wraps 3 → 0.
	gm, _ = gm.Update(skillGateKeyPress("down"))
	if gm.selected != 1 {
		t.Fatalf("down from 0 should go to 1; got %d", gm.selected)
	}
	gm, _ = gm.Update(skillGateKeyPress("down"))
	if gm.selected != 2 {
		t.Fatalf("down from 1 should go to 2; got %d", gm.selected)
	}
	gm, _ = gm.Update(skillGateKeyPress("down"))
	if gm.selected != 3 {
		t.Fatalf("down from 2 should go to 3; got %d", gm.selected)
	}
	gm, _ = gm.Update(skillGateKeyPress("down"))
	if gm.selected != 0 {
		t.Fatalf("down from 3 should wrap to 0; got %d", gm.selected)
	}
	// up wraps 0 → 3, then walks back down.
	gm, _ = gm.Update(skillGateKeyPress("up"))
	if gm.selected != 3 {
		t.Fatalf("up from 0 should wrap to 3; got %d", gm.selected)
	}
	gm, _ = gm.Update(skillGateKeyPress("up"))
	if gm.selected != 2 {
		t.Fatalf("up from 3 should go to 2; got %d", gm.selected)
	}

	// "2" jumps to index 1.
	gm, _ = gm.Update(skillGateKeyPress("2"))
	if gm.selected != 1 {
		t.Fatalf("'2' should jump to index 1; got %d", gm.selected)
	}

	// Enter confirms with the selected option's choice.
	gm, _ = gm.Update(skillGateKeyPress("enter"))
	if !gm.IsDone() {
		t.Fatal("enter should set done")
	}
	if got, want := gm.Choice(), session.SkillGateAllowSkill; got != want {
		t.Fatalf("Choice() after '2'+enter = %v, want %v", got, want)
	}
}

func TestSkillGateModelEscSelectsDenyWithoutDone(t *testing.T) {
	sg := &session.PendingSkillGate{Skill: "review"}
	gm := newSkillGateModel(sg, 80)

	gm, _ = gm.Update(skillGateKeyPress("esc"))
	if gm.selected != 3 {
		t.Fatalf("esc should move the selection to Deny (3); got %d", gm.selected)
	}
	if gm.IsDone() {
		t.Fatal("esc must not set done: an accidental Esc must not record a sticky deny")
	}
	if got, want := gm.Choice(), session.SkillGateDeny; got != want {
		t.Fatalf("Choice() after esc = %v, want %v", got, want)
	}
}

func TestSkillGateModelViewContent(t *testing.T) {
	sg := &session.PendingSkillGate{
		Skill:       "review",
		Description: "Loads project review guidelines.",
		Reason:      "small context window",
	}
	gm := newSkillGateModel(sg, 80)
	view := gm.View()
	for _, want := range []string{
		"Load skill 'review'?",
		"Loads project review guidelines.",
		"small context window",
		"Allow once",
		"Allow always (this skill)",
		"Allow always (all skills)",
		"Deny",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("skill-gate view missing %q:\n%s", want, view)
		}
	}
}

func TestHandleSkillGateAllowOnceRespondsAndClears(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	ch := make(chan session.SkillGateChoice, 1)
	sg := &session.PendingSkillGate{Skill: "x", ResponseChan: ch}
	m.state.SetPendingSkillGate(sg)

	// First message lazily builds the dialog; second confirms the default
	// selection (Allow once).
	updated, _ := m.Update(skillGateKeyPress("enter"))
	m = updated.(Model)
	if m.skillGateModel == nil {
		t.Fatal("first message should lazily build the skill-gate model")
	}
	if m.state.PendingSkillGate() == nil {
		t.Fatal("lazy build must not clear the pending gate")
	}
	updated, _ = m.Update(skillGateKeyPress("enter"))
	m = updated.(Model)

	select {
	case got := <-ch:
		if got != session.SkillGateAllowOnce {
			t.Fatalf("responded choice = %v, want AllowOnce", got)
		}
	default:
		t.Fatal("pending skill gate was not responded")
	}
	if m.state.PendingSkillGate() != nil {
		t.Fatal("pending skill gate should be cleared after the decision")
	}
	if m.skillGateModel != nil {
		t.Fatal("skill-gate model should be dropped after the decision")
	}
	// AllowOnce records nothing.
	if got := m.state.SkillGateDecisions(); len(got) != 0 {
		t.Fatalf("SkillGateDecisions() after allow-once = %+v, want empty", got)
	}
}

func TestHandleSkillGateAllowAllDisablesGate(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	ch := make(chan session.SkillGateChoice, 1)
	sg := &session.PendingSkillGate{Skill: "x", ResponseChan: ch}
	m.state.SetPendingSkillGate(sg)

	// Lazy build, jump to option 3 (Allow always (all skills)), confirm.
	updated, _ := m.Update(skillGateKeyPress("enter"))
	m = updated.(Model)
	updated, _ = m.Update(skillGateKeyPress("3"))
	m = updated.(Model)
	updated, _ = m.Update(skillGateKeyPress("enter"))
	m = updated.(Model)

	select {
	case got := <-ch:
		if got != session.SkillGateAllowAll {
			t.Fatalf("responded choice = %v, want AllowAll", got)
		}
	default:
		t.Fatal("pending skill gate was not responded")
	}
	if m.state.PendingSkillGate() != nil {
		t.Fatal("pending skill gate should be cleared after the decision")
	}
	decisions := m.state.SkillGateDecisions()
	if len(decisions) != 1 || decisions[0].Skill != "x" || !decisions[0].Allowed {
		t.Fatalf("SkillGateDecisions() = %+v, want one allowed entry for x", decisions)
	}
	if m.state.SkillGateEnabled() {
		t.Fatal("SkillGateEnabled() = true after allow-all, want false")
	}
}

func TestHandleSkillGateDenyDoesNotRecord(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	ch := make(chan session.SkillGateChoice, 1)
	sg := &session.PendingSkillGate{Skill: "x", ResponseChan: ch}
	m.state.SetPendingSkillGate(sg)

	// Lazy build, Esc moves the selection to Deny, Enter confirms.
	updated, _ := m.Update(skillGateKeyPress("enter"))
	m = updated.(Model)
	updated, _ = m.Update(skillGateKeyPress("esc"))
	m = updated.(Model)
	updated, _ = m.Update(skillGateKeyPress("enter"))
	m = updated.(Model)

	select {
	case got := <-ch:
		if got != session.SkillGateDeny {
			t.Fatalf("responded choice = %v, want Deny", got)
		}
	default:
		t.Fatal("pending skill gate was not responded")
	}
	if got := m.state.SkillGateDecisions(); len(got) != 0 {
		t.Fatalf("SkillGateDecisions() after deny = %+v, want empty (deny is the runner's counter)", got)
	}
}

func TestSkillGateSuppressesTextareaAndBudget(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sg := &session.PendingSkillGate{Skill: "x", ResponseChan: make(chan session.SkillGateChoice, 1)}
	m.state.SetPendingSkillGate(sg)
	m.skillGateModel = newSkillGateModel(sg, max(m.leftWidth-4, 30))

	// While the gate is pending the textarea is suppressed: the input area
	// is exactly the chrome rows (gate panel), nothing else.
	if got, want := m.inputAreaRows(), m.inputChromeRows(); got != want {
		t.Fatalf("inputAreaRows() = %d, want %d (textarea suppressed while gate pending)", got, want)
	}
	if got := m.inputChromeRows(); got == 0 {
		t.Fatal("inputChromeRows() = 0, want the gate panel rows")
	}
	// The rendered input area shows the gate dialog, not the textarea.
	view := m.renderInputArea()
	if !strings.Contains(view, "Load skill 'x'?") {
		t.Fatalf("renderInputArea missing the gate dialog:\n%s", view)
	}
	if strings.Contains(view, "Ask Marshal...") {
		t.Fatal("renderInputArea should not show the textarea placeholder while the gate is pending")
	}
}
