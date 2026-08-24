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

func TestApprovalDialogRendersDiffAsTintedBlock(t *testing.T) {
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
	// No old-form box-drawing border glyphs in the output (the diffview
	// may use │ in side-by-side mode, which is fine — we only check for
	// the old form's ─ border).
	if strings.Contains(view, "───") {
		t.Fatalf("approval dialog should not contain old form border glyphs:\n%s", view)
	}
}

func TestApprovalDialogNoDiffWhenEmpty(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	view := am.View()
	// No diff → just the warning line and selector.
	if strings.Contains(view, "───") {
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

func TestApprovalDialogLabelsContainFlatSelector(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	view := am.View()
	for _, want := range []string{"⚠", "allow", "deny", "edit"} {
		if !strings.Contains(view, want) {
			t.Errorf("approval view missing %q:\n%s", want, view)
		}
	}
	for _, gone := range []string{"Approve", "Deny", "j/k"} {
		if strings.Contains(view, gone) {
			t.Errorf("approval view still contains old form label %q", gone)
		}
	}
}

func TestApprovalNavigatesHorizontally(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)

	// The choices render on one horizontal row, so ←/→ (and h/l) move the
	// selection; ↑/↓/j/k must do nothing.
	am, _ = am.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if am.selected != 1 {
		t.Fatalf("right should move selection to 1; got %d", am.selected)
	}
	am, _ = am.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if am.selected != 2 {
		t.Fatalf("second right should move selection to 2; got %d", am.selected)
	}
	am, _ = am.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if am.selected != 1 {
		t.Fatalf("left should move selection back to 1; got %d", am.selected)
	}
	am, _ = am.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if am.selected != 2 {
		t.Fatalf("l should move selection to 2; got %d", am.selected)
	}
	am, _ = am.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if am.selected != 1 {
		t.Fatalf("h should move selection back to 1; got %d", am.selected)
	}
	for _, vertical := range []tea.KeyPressMsg{
		{Code: tea.KeyDown},
		{Code: tea.KeyUp},
		{Code: 'j', Text: "j"},
		{Code: 'k', Text: "k"},
	} {
		am, _ = am.Update(vertical)
		if am.selected != 1 {
			t.Fatalf("vertical key %q must not move a horizontal selector; got %d", vertical.String(), am.selected)
		}
	}
}

func TestApprovalViewHasChromeRail(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "file.write_patch",
		Args: `{"path":"a.go"}`,
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	lines := strings.Split(strings.TrimRight(stripANSI(am.View()), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("approval view too short to rail:\n%s", am.View())
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, "▍") {
			t.Errorf("line %d missing the ▍ chrome rail: %q\nfull view:\n%s", i, line, stripANSI(am.View()))
		}
	}
}

// The mnemonic row advertises these keys; they must actually work.
func TestApprovalMnemonicKeysSelectChoices(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want approvalChoice
	}{
		{"a", choiceApprove},
		{"d", choiceDeny},
		{"e", choiceEdit},
		{"A", choiceAlways},
		{"s", choiceSessionAllow},
	} {
		am := newApprovalModel(&session.PendingToolCall{Name: "shell.run"}, session.SandboxInfo{}, false, false, 80)
		am, _ = am.Update(keyPress(tc.key))
		if am.choice != tc.want {
			t.Errorf("key %q selected %v, want %v", tc.key, am.choice, tc.want)
		}
	}
}

// config.write is deliberately non-bypassable: persisting an always-allow
// rule for it would be a safety hole, and a session rule would be a silent
// no-op since session rules only match shell.run/test.run.
func TestForcedPromptsHideAlwaysAndSession(t *testing.T) {
	for _, tc := range []*session.PendingToolCall{
		{Name: "config.write"},
		{Name: "agent.run", Risk: "model-cost-consent"},
		{Name: "file.write", Reason: "mode-elevation: edit requires write access"},
	} {
		am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 80)
		for _, c := range am.candidates {
			if c == choiceAlways || c == choiceSessionAllow {
				t.Errorf("%s must not offer %v", tc.Name, c)
			}
		}
	}
}

// Approve, deny and edit are always available.
func TestForcedPromptsKeepCoreChoices(t *testing.T) {
	am := newApprovalModel(&session.PendingToolCall{Name: "config.write"}, session.SandboxInfo{}, false, false, 80)
	have := map[approvalChoice]bool{}
	for _, c := range am.candidates {
		have[c] = true
	}
	for _, want := range []approvalChoice{choiceApprove, choiceDeny, choiceEdit} {
		if !have[want] {
			t.Errorf("forced prompt lost core choice %v", want)
		}
	}
}

// A mnemonic for a filtered-out choice must do nothing, not select an
// absent option.
func TestFilteredMnemonicIsInert(t *testing.T) {
	am := newApprovalModel(&session.PendingToolCall{Name: "config.write"}, session.SandboxInfo{}, false, false, 80)
	before := am.choice
	am, _ = am.Update(keyPress("A"))
	if am.choice != before {
		t.Fatalf("A selected a filtered-out choice: %v", am.choice)
	}
}
