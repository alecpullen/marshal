package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// sudoAuthKeyPress wraps a key string into a tea.KeyPressMsg, mirroring
// skillGateKeyPress.
func sudoAuthKeyPress(key string) tea.KeyPressMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
}

// primeSudoAuth sends one message so handleSudoAuth lazily builds the masked
// form, then returns the model. The first message never responds — it only
// constructs the model (parity with the skill-gate dialog).
func primeSudoAuth(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(sudoAuthKeyPress("a"))
	m = updated.(Model)
	if m.sudoAuthModel == nil {
		t.Fatal("first message should lazily build the sudo-auth model")
	}
	if m.state.PendingSudoAuth() == nil {
		t.Fatal("lazy build must not clear the pending prompt")
	}
	return m
}

func TestSudoAuthModalMasksAndShowsCommand(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sa := &session.PendingSudoAuth{
		Command:      "systemctl restart nginx",
		Reason:       "restart the web server",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	m.state.SetPendingSudoAuth(sa)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "systemctl restart nginx") {
		t.Fatalf("view does not show the verbatim command:\n%s", view)
	}

	// Type a password and assert it never appears in the rendered view.
	for _, r := range "hunter2" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if strings.Contains(stripANSI(m.View().Content), "hunter2") {
		t.Fatalf("typed password appears in the view:\n%s", stripANSI(m.View().Content))
	}
}

func TestSudoAuthEscDenies(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sa := &session.PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	m.state.SetPendingSudoAuth(sa)
	m = primeSudoAuth(t, m)

	updated, _ := m.Update(sudoAuthKeyPress("esc"))
	m = updated.(Model)

	select {
	case d := <-sa.ResponseChan:
		if d.OK {
			t.Fatal("Esc must deny")
		}
		if len(d.Password) != 0 {
			t.Fatalf("denied decision carries a password: %q", d.Password)
		}
	default:
		t.Fatal("Esc did not respond on the pending prompt")
	}
	if m.state.PendingSudoAuth() != nil {
		t.Fatal("PendingSudoAuth should be cleared after the decision")
	}
	if m.sudoAuthModel != nil {
		t.Fatal("sudo-auth model should be dropped after the decision")
	}
}

func TestSudoAuthEnterSubmitsTypedBytes(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sa := &session.PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	m.state.SetPendingSudoAuth(sa)
	m = primeSudoAuth(t, m)

	for _, r := range "pw" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	updated, _ := m.Update(sudoAuthKeyPress("enter"))
	m = updated.(Model)

	select {
	case d := <-sa.ResponseChan:
		if !d.OK || string(d.Password) != "pw" {
			t.Fatalf("decision = %+v, want OK with the typed bytes", d)
		}
	default:
		t.Fatal("Enter did not respond on the pending prompt")
	}
}

func TestSudoAuthBlocksModeCyclingKeys(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	m.state.SetPendingSudoAuth(&session.PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	})
	m = primeSudoAuth(t, m)

	before := m.approvalMode
	updated, _ := m.Update(sudoAuthKeyPress("tab"))
	m = updated.(Model)
	if m.approvalMode != before {
		t.Fatalf("mode changed from %v to %v while a sudo prompt was pending", before, m.approvalMode)
	}
}
