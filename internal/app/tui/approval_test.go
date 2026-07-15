package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/app/session"
)

// keyPress is a helper that wraps a key string into a tea.KeyPressMsg.
func keyPress(key string) tea.KeyPressMsg {
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

func TestApprovalDialogRendersDiff(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "file.write_patch",
		Args: `{"path":"a.go"}`,
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	view := am.View()
	// The diff should appear above the form.
	if !strings.Contains(view, "old") || !strings.Contains(view, "new") {
		t.Fatalf("approval dialog missing diff content:\n%s", view)
	}
}

func TestApprovalDialogNoDiffWhenEmpty(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	view := am.View()
	// No diff → just the form.
	if strings.Contains(view, "───") && !strings.Contains(view, "Approval") {
		t.Fatalf("view should not contain diff artifacts when tc.Diff is empty:\n%s", view)
	}
}

func TestApprovalSubFormMarksDoneWithoutDispatching(t *testing.T) {
	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		Name:         "shell.run",
		Args:         `{"command":"ls"}`,
		ResponseChan: ch,
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)

	// Double Esc should mark done + deny without writing to the channel.
	am, _ = am.Update(keyPress("esc"))
	if am.IsDone() {
		t.Fatal("first esc should not set done")
	}
	am, _ = am.Update(keyPress("esc"))
	if !am.IsDone() {
		t.Fatal("second esc should set done")
	}
	if am.Choice() != choiceDeny {
		t.Fatalf("esc should set choiceDeny; got %v", am.Choice())
	}
	select {
	case got := <-ch:
		t.Fatalf("sub-form dispatched on esc: %v", got)
	default:
	}

	// Reset and test the two-enter confirm path.
	am2 := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	am2, _ = am2.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // arm
	if am2.IsDone() {
		t.Fatal("first enter should not set done")
	}
	am2, _ = am2.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // confirm
	if !am2.IsDone() {
		t.Fatal("second enter should set done")
	}
	if am2.Choice() != choiceApprove {
		t.Fatalf("expected choiceApprove; got %v", am2.Choice())
	}
	select {
	case got := <-ch:
		t.Fatalf("sub-form dispatched on confirm: %v", got)
	default:
	}
}

func TestEscRequiresDoublePressToDeny(t *testing.T) {
	am := newApprovalModel(&session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}, session.SandboxInfo{}, false, false, 160)
	am, _ = am.Update(keyPress("esc"))
	if am.IsDone() {
		t.Fatalf("first Esc should not complete the form")
	}
	am, _ = am.Update(keyPress("esc"))
	if !am.IsDone() || am.Choice() != choiceDeny {
		t.Fatalf("second Esc should deny; got done=%v choice=%v", am.IsDone(), am.Choice())
	}
}

func TestEscAfterOtherKeyResetsPendingDeny(t *testing.T) {
	am := newApprovalModel(&session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}, session.SandboxInfo{}, false, false, 160)
	am, _ = am.Update(keyPress("esc"))
	am, _ = am.Update(keyPress("down"))
	if !am.pendingDenyAt.IsZero() {
		t.Fatalf("non-Esc key should reset pending deny")
	}
}

func TestApprovalDialogLabelsContainSubmitIndicator(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "file.write_patch",
		Args: `{"path":"a.go"}`,
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	view := am.View()

	// The explicit submit row should be present.
	if !strings.Contains(view, "Submit selected action") {
		t.Errorf("approval view missing explicit submit row:\n%s", view)
	}

	// The edit option should show its descriptive label.
	if !strings.Contains(view, "Edit command/args") {
		t.Errorf("approval view missing descriptive 'Edit command/args' label:\n%s", view)
	}

	// The title should include a j/k navigation hint.
	if !strings.Contains(view, "j/k") {
		t.Errorf("approval view missing j/k navigation hint:\n%s", view)
	}
}
