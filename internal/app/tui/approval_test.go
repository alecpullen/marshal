package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestApprovalEscDisarmsPendingSubmit pins the gesture order the footer
// advertises: Escape always disarms an armed action, so the next Enter arms
// again instead of confirming the action the user was refusing.
func TestApprovalEscDisarmsPendingSubmit(t *testing.T) {
	am := newApprovalModel(&session.PendingToolCall{
		Name: "shell.run",
		Args: `{"command":"ls"}`,
	}, session.SandboxInfo{}, false, false, 160)

	am, _ = am.Update(keyPress("enter"))
	if !am.submitPending {
		t.Fatal("first Enter should arm the selected action")
	}
	am, _ = am.Update(keyPress("esc"))
	if am.IsDone() {
		t.Fatal("one Escape must not settle the prompt: it disarms and opens the deny window")
	}
	if am.submitPending {
		t.Fatal("Escape must disarm the pending submit")
	}
	am, _ = am.Update(keyPress("enter"))
	if am.IsDone() {
		t.Fatal("Enter after Escape must arm again, not confirm")
	}
	if !am.submitPending {
		t.Fatal("Enter after Escape should leave the action newly armed")
	}
	am, _ = am.Update(keyPress("enter"))
	if !am.IsDone() || am.Choice() != choiceApprove {
		t.Fatalf("only the further Enter confirms; got done=%v choice=%v", am.IsDone(), am.Choice())
	}
}

// TestApprovalEscDisarmsEveryChoice covers every action a prompt can offer,
// not just the default one: the disarm belongs to the prompt. Rollback is
// included because it is a selectable decision like any other when a backup
// exists.
func TestApprovalEscDisarmsEveryChoice(t *testing.T) {
	tc := &session.PendingToolCall{Name: "shell.run", Args: `{"command":"ls"}`}
	candidates := newApprovalModel(tc, session.SandboxInfo{}, false, true, 160).candidates
	have := map[approvalChoice]bool{}
	for _, c := range candidates {
		have[c] = true
	}
	for _, want := range []approvalChoice{
		choiceApprove, choiceDeny, choiceEdit, choiceAlways, choiceSessionAllow, choiceRollback,
	} {
		if !have[want] {
			t.Fatalf("setup: the unrestricted selector should offer %v; offered %v", want, candidates)
		}
	}

	for i, c := range candidates {
		am := newApprovalModel(tc, session.SandboxInfo{}, false, true, 160)
		am.selected, am.choice = i, c
		am, _ = am.Update(keyPress("enter"))
		if !am.submitPending {
			t.Fatalf("%v: first Enter should arm the action", c)
		}
		am, _ = am.Update(keyPress("esc"))
		if am.submitPending {
			t.Fatalf("%v: Escape must disarm the pending submit", c)
		}
		if am.IsDone() {
			t.Fatalf("%v: one Escape must not settle the prompt", c)
		}
		am, _ = am.Update(keyPress("enter"))
		if am.IsDone() || !am.submitPending {
			t.Fatalf("%v: Enter after Escape should re-arm, not confirm; done=%v armed=%v",
				c, am.IsDone(), am.submitPending)
		}
	}
}

// TestApprovalNavigationDisarmsPendingSubmit covers the other half of the
// contract: moving the selection also cancels an armed confirmation, so the
// armed action is always the one still on screen.
func TestApprovalNavigationDisarmsPendingSubmit(t *testing.T) {
	// A key that cannot move the selection (left at the first choice) changes
	// nothing, so it is armed from a position where every key here does move.
	for name, move := range map[string]tea.KeyPressMsg{
		"right arrow": {Code: tea.KeyRight},
		"left arrow":  {Code: tea.KeyLeft},
		"mnemonic":    {Code: 'd', Text: "d"},
	} {
		am := newApprovalModel(&session.PendingToolCall{Name: "shell.run", Args: `{"command":"ls"}`},
			session.SandboxInfo{}, false, false, 160)
		if name == "left arrow" {
			am, _ = am.Update(tea.KeyPressMsg{Code: tea.KeyRight})
		}
		am, _ = am.Update(keyPress("enter"))
		if !am.submitPending {
			t.Fatalf("%s: setup: Enter should arm the action", name)
		}
		am, _ = am.Update(move)
		if am.submitPending {
			t.Fatalf("%s: moving the selection must disarm the pending submit", name)
		}
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

func TestApprovalViewHasNoRail(t *testing.T) {
	tc := &session.PendingToolCall{
		Name: "file.write_patch",
		Args: `{"path":"a.go"}`,
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	am := newApprovalModel(tc, session.SandboxInfo{}, false, false, 160)
	view := stripANSI(am.View())
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("approval view too short:\n%s", view)
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "▍") {
			t.Errorf("line %d still wears the ▍ rail: %q\nfull view:\n%s", i, line, view)
		}
	}
	if !strings.Contains(view, "new") || !strings.Contains(view, "old") {
		t.Fatalf("the patch's lines are missing from the view:\n%s", view)
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

// shell.sudo is deliberately non-bypassable: an always-allow rule would let a later command run as root with no authentication.
func TestForcedPromptsHideAlwaysAndSession(t *testing.T) {
	for _, tc := range []*session.PendingToolCall{
		{Name: "shell.sudo"},
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
	am := newApprovalModel(&session.PendingToolCall{Name: "shell.sudo"}, session.SandboxInfo{}, false, false, 80)
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
	am := newApprovalModel(&session.PendingToolCall{Name: "shell.sudo"}, session.SandboxInfo{}, false, false, 80)
	before := am.choice
	am, _ = am.Update(keyPress("A"))
	if am.choice != before {
		t.Fatalf("A selected a filtered-out choice: %v", am.choice)
	}
}

// TestApprovalViewShowsMnemonicHints verifies that the interactive approval
// panel (approvalModel.View) renders the mnemonic key alongside each choice
// label, so the user knows the single-key shortcuts work.
func TestApprovalViewShowsMnemonicHints(t *testing.T) {
	am := newApprovalModel(&session.PendingToolCall{Name: "shell.run"}, session.SandboxInfo{}, false, false, 80)
	view := ansi.Strip(am.View())
	// The mnemonic keys for allow/deny/edit must appear in the view.
	for _, key := range []string{"a", "d", "e"} {
		if !strings.Contains(view, key+" ") {
			t.Errorf("mnemonic key %q not shown in approval view:\n%s", key, view)
		}
	}
}
