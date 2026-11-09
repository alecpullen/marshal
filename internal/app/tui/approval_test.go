package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
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
	a := acpwire.ApprovalRequest{
		Tool: "file.write_patch",
		Args: `{"path":"a.go"}`,
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	am := newApprovalModel(a, acpwire.Sandbox{}, false, 160)
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
	a := acpwire.ApprovalRequest{
		Tool: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(a, acpwire.Sandbox{}, false, 160)
	view := am.View()
	// No diff → just the warning line and selector.
	if strings.Contains(view, "───") {
		t.Fatalf("view should not contain diff artifacts when tc.Diff is empty:\n%s", view)
	}
}

func TestApprovalSubFormMarksDoneWithoutDispatching(t *testing.T) {
	ch := make(chan session.UserApprovalDecision, 1)
	am := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.run", Args: `{"command":"ls"}`}, acpwire.Sandbox{}, false, 160)

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
	am2 := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.run", Args: `{"command":"ls"}`}, acpwire.Sandbox{}, false, 160)
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
	am := newApprovalModel(acpwire.ApprovalRequest{
		Tool: "shell.run",
		Args: `{"command":"ls"}`,
	}, acpwire.Sandbox{}, false, 160)
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
	am := newApprovalModel(acpwire.ApprovalRequest{
		Tool: "shell.run",
		Args: `{"command":"ls"}`,
	}, acpwire.Sandbox{}, false, 160)
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
	am := newApprovalModel(acpwire.ApprovalRequest{
		Tool: "shell.run",
		Args: `{"command":"ls"}`,
	}, acpwire.Sandbox{}, false, 160)

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
	a := acpwire.ApprovalRequest{Tool: "shell.run", Args: `{"command":"ls"}`}
	candidates := newApprovalModel(a, acpwire.Sandbox{}, true, 160).candidates
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
		am := newApprovalModel(a, acpwire.Sandbox{}, true, 160)
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
		am := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.run", Args: `{"command":"ls"}`},
			acpwire.Sandbox{}, false, 160)
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
	a := acpwire.ApprovalRequest{
		Tool: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(a, acpwire.Sandbox{}, false, 160)
	view := ansi.Strip(am.View())
	for _, want := range []string{"⚠", "▸ a allow", "1/5"} {
		if !strings.Contains(view, want) {
			t.Errorf("approval view missing %q:\n%s", want, view)
		}
	}
	// The selector shows one action at a time, but every candidate stays
	// reachable: walking it with the arrow keys lands on each label.
	want := []string{"allow", "deny", "edit", "always", "session"}
	for i, label := range want {
		if am.selected != i {
			t.Fatalf("setup: selector at %d, want %d", am.selected, i)
		}
		if got := ansi.Strip(am.View()); !strings.Contains(got, label) {
			t.Errorf("selected choice %d does not show %q:\n%s", i, label, got)
		}
		am, _ = am.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	for _, gone := range []string{"Approve", "Deny", "j/k"} {
		if strings.Contains(view, gone) {
			t.Errorf("approval view still contains old form label %q", gone)
		}
	}
}

func TestApprovalNavigatesHorizontally(t *testing.T) {
	a := acpwire.ApprovalRequest{
		Tool: "shell.run",
		Args: `{"command":"ls"}`,
	}
	am := newApprovalModel(a, acpwire.Sandbox{}, false, 160)

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
	a := acpwire.ApprovalRequest{
		Tool: "file.write_patch",
		Args: `{"path":"a.go"}`,
		Diff: "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	am := newApprovalModel(a, acpwire.Sandbox{}, false, 160)
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
		am := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.run"}, acpwire.Sandbox{}, false, 80)
		am, _ = am.Update(keyPress(tc.key))
		if am.choice != tc.want {
			t.Errorf("key %q selected %v, want %v", tc.key, am.choice, tc.want)
		}
	}
}

// A forced prompt hides the standing choices: the wire says so, and the form
// offers only allow, deny and edit.
func TestForcedPromptsHideAlwaysAndSession(t *testing.T) {
	for _, a := range []acpwire.ApprovalRequest{
		{Tool: "shell.sudo", Forced: true},
		{Tool: "agent.run", Risk: "model-cost-consent", Forced: true},
		{Tool: "file.write", Reason: "mode-elevation: edit requires write access", Forced: true},
	} {
		am := newApprovalModel(a, acpwire.Sandbox{}, false, 80)
		for _, c := range am.candidates {
			if c == choiceAlways || c == choiceSessionAllow {
				t.Errorf("%s must not offer %v", a.Tool, c)
			}
		}
	}
}

// Approve, deny and edit are always available.
func TestForcedPromptsKeepCoreChoices(t *testing.T) {
	am := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.sudo", Forced: true}, acpwire.Sandbox{}, false, 80)
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
	am := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.sudo", Forced: true}, acpwire.Sandbox{}, false, 80)
	before := am.choice
	am, _ = am.Update(keyPress("A"))
	if am.choice != before {
		t.Fatalf("A selected a filtered-out choice: %v", am.choice)
	}
}

// TestApprovalReviewShowsOperationAndReason pins the content of the review
// body: who is asking and why, the operation itself (arguments and schema for
// a non-shell tool), the risk, and the reason as a separate field. Model-written
// controls in any of them are shown as visible escapes, never executed.
func TestApprovalReviewShowsOperationAndReason(t *testing.T) {
	tc := &session.PendingToolCall{
		ID:     "tc-review",
		Name:   "file.write_patch",
		Args:   "{\"path\":\"a.go\",\"note\":\"\x1b[31mred\x1b[0m\r\"}",
		Schema: "Write a file: path <string>",
		Risk:   "medium",
		Reason: "model-cost-consent: this spends $0.40 of the budget",
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	state.SetPendingApproval(tc)
	m := testModelWith(t, service.Deps{State: state, Auth: newMemAuth()}, WithHomeDir(t.TempDir()),
		WithOpenBrowser(func(string) error { return nil }))
	m, _ = press(m, tea.WindowSizeMsg{Width: 100, Height: 32})
	// The requester and the model's own "why" ride on the wire; supply them as
	// the adapter does, then assert on the composed frame.
	a := m.sess.Pending().Approvals[0]
	a.Actor = "reviewer"
	a.Why = "fix the failing test"
	m.approvalModel = newApprovalModel(a, acpwire.Sandbox{}, false, max(m.leftWidth-4, 30))
	m.syncPromptReviewHeights()

	raw := m.viewString()
	frame := ansi.Strip(raw)
	for _, want := range []string{
		"reviewer wants to ",
		"Agent wants to call tool: file.write_patch",
		"Description: Write a file: path <string>",
		`Arguments: {"path":"a.go"`,
		`why  "fix the failing test"`,
		"Risk: medium",
		"Reason: model-cost-consent: this spends $0.40 of the budget",
		"▸ a allow",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("approval review missing %q:\n%s", want, frame)
		}
	}
	// The model's own controls are escaped rather than executed.
	if !strings.Contains(frame, `\x1b[31mred`) {
		t.Errorf("the argument's ESC is not shown as a visible escape:\n%s", frame)
	}
	if !strings.Contains(frame, `\r`) {
		t.Errorf("the argument's CR is not shown as a visible escape:\n%s", frame)
	}
	if strings.Contains(raw, "\x1b[31mred") {
		t.Error("the arguments emitted a raw SGR sequence into the frame")
	}
}

// TestApprovalReviewPagesFullCommandAndDiff pages a long command and a long
// diff at the minimum supported size: both ends are reachable, and the
// selector never scrolls away with the body.
func TestApprovalReviewPagesFullCommandAndDiff(t *testing.T) {
	var cmd strings.Builder
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&cmd, "CM%d echo %s\n", i, strings.Repeat("a", 120))
	}
	cmd.WriteString("CMEND echo done")

	var diff strings.Builder
	diff.WriteString("--- a/a.go\n+++ b/a.go\n@@ -1,40 +1,40 @@\n")
	diff.WriteString(" DMSTART context before\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&diff, "-old line %d %s\n", i, strings.Repeat("o", 60))
		fmt.Fprintf(&diff, "+new line %d %s\n", i, strings.Repeat("n", 60))
	}
	diff.WriteString(" DMEND context after\n")

	tc := &session.PendingToolCall{
		ID:      "tc-pages",
		Name:    "shell.run",
		Args:    `{"command":"long"}`,
		Command: cmd.String(),
		Risk:    "high",
		Diff:    diff.String(),
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	state.SetPendingApproval(tc)
	m := testModelWith(t, service.Deps{State: state, Auth: newMemAuth()}, WithHomeDir(t.TempDir()),
		WithOpenBrowser(func(string) error { return nil }))
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.approvalModel == nil {
		t.Fatal("setup: the pending approval has no prompt")
	}
	m.syncPromptReviewHeights()

	page := func() string {
		m.syncPromptReviewHeights()
		return ansi.Strip(m.viewString())
	}
	first := page()
	if !strings.Contains(first, "CM1") {
		t.Fatalf("the review does not open on the start of the command:\n%s", first)
	}
	if !strings.Contains(first, "▸ a allow") {
		t.Fatalf("the selector is missing from the first page:\n%s", first)
	}
	seen := first
	for i := 0; i < 40; i++ {
		m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		seen += "\n" + page()
	}
	for _, marker := range []string{"CM1", "CM2", "CM3", "CM4", "CM5", "CM6", "CMEND", "DMSTART", "DMEND"} {
		if !strings.Contains(seen, marker) {
			t.Errorf("marker %q never became visible while paging the review", marker)
		}
	}
	for i := 0; i < 40; i++ {
		m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	back := page()
	if !strings.Contains(back, "CM1") {
		t.Fatalf("paging back does not return to the start of the command:\n%s", back)
	}
	// The selection and its counter are pinned: paging never hides them.
	for _, want := range []string{"▸ a allow", "1/5"} {
		if !strings.Contains(back, want) {
			t.Errorf("paging lost the pinned selector %q:\n%s", want, back)
		}
	}
}

// TestApprovalReviewResizeAndVisibility shrinks the terminal below the
// supported minimum and hides the prompt behind a full-frame panel: an
// off-screen prompt must never settle a decision, and coming back must restore
// the same selection.
func TestApprovalReviewResizeAndVisibility(t *testing.T) {
	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "tc-resize",
		Name:         "shell.run",
		Args:         `{"command":"rm -rf build"}`,
		Command:      "rm -rf build",
		Risk:         "high",
		ResponseChan: ch,
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	state.SetPendingApproval(tc)
	m := testModelWith(t, service.Deps{State: state, Auth: newMemAuth()}, WithHomeDir(t.TempDir()),
		WithOpenBrowser(func(string) error { return nil }))
	m, _ = press(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.workSnapshot = workSnapOf(workRunningCard("work:01", workT0.Add(-time.Second)))
	m.busy = true
	m.refreshViewport()

	// Move off the default so a lost or reset selection is visible.
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.approvalModel == nil {
		t.Fatal("setup: no approval prompt")
	}
	if m.approvalModel.selected != 1 {
		t.Fatalf("setup: right should select deny; got %d", m.approvalModel.selected)
	}

	// A prompt row must never be clipped out of the composed frame.
	assertPromptVisible := func(name string) {
		t.Helper()
		m.syncPromptReviewHeights()
		rows := strings.Split(ansi.Strip(m.viewString()), "\n")
		if len(rows) != m.height {
			t.Errorf("%s: frame is %d rows, want %d:\n%s", name, len(rows), m.height, strings.Join(rows, "\n"))
		}
		if rowOf(rows, "▸ d deny") < 0 {
			t.Errorf("%s: the selector is missing from the frame:\n%s", name, strings.Join(rows, "\n"))
		}
		if rowOf(rows, "│") < 0 {
			t.Errorf("%s: the console is missing from the frame:\n%s", name, strings.Join(rows, "\n"))
		}
	}
	assertPromptVisible("120x40")
	if !m.promptActionable() {
		t.Fatal("120x40: the prompt should be actionable")
	}

	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	assertPromptVisible("80x24")
	if !m.promptActionable() {
		t.Fatal("80x24: the prompt should be actionable")
	}
	if m.approvalModel.selected != 1 {
		t.Fatalf("resize changed the selection: %d", m.approvalModel.selected)
	}

	// Below the supported minimum the prompt cannot be seen: it must not
	// accept a decision.
	m, _ = press(m, tea.WindowSizeMsg{Width: 60, Height: 15})
	if m.promptActionable() {
		t.Fatal("below the minimum terminal the prompt must not be actionable")
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case d := <-ch:
		t.Fatalf("an off-screen prompt settled a decision: %+v", d)
	default:
	}
	if m.approvalModel == nil || m.approvalModel.IsDone() {
		t.Fatal("an off-screen prompt must stay pending")
	}

	// A full-frame panel owns the frame: the prompt is hidden, so it must not
	// settle either.
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.openSettingsBrowser("")
	if !m.dock.FullFrameOpen() {
		t.Fatal("fixture: /settings should own the whole frame")
	}
	if m.promptActionable() {
		t.Fatal("a prompt behind a full-frame panel must not be actionable")
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case d := <-ch:
		t.Fatalf("a prompt behind a full-frame panel settled a decision: %+v", d)
	default:
	}

	// Back to a supported frame with no panel: the same selection is still
	// there and its decision is the one the user chose.
	m.dock.CloseNow()
	m, _ = press(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	assertPromptVisible("restored 120x40")
	if !m.promptActionable() {
		t.Fatal("restored 120x40: the prompt should be actionable again")
	}
	if m.approvalModel.selected != 1 || m.approvalModel.choice != choiceDeny {
		t.Fatalf("the restored prompt lost its selection: selected=%d choice=%v",
			m.approvalModel.selected, m.approvalModel.choice)
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	d, ok := <-ch
	if !ok || d.Approved {
		t.Fatalf("the user's deny selection did not settle: %+v (ok=%v)", d, ok)
	}
}

// TestApprovalReviewKeepsTranscriptNavigation pins the split between the two
// scrollable regions: PgUp/PgDn page the review, while Ctrl+U/Ctrl+D and the
// mouse keep scrolling the transcript behind it. It also covers a forced
// prompt, walking every choice, the two-step confirmation, and a replacement
// request starting the review again at the top.
func TestApprovalReviewKeepsTranscriptNavigation(t *testing.T) {
	var cmd strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&cmd, "echo %d %s\n", i, strings.Repeat("x", 100))
	}
	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "tc-nav",
		Name:         "shell.run",
		Args:         `{"command":"long"}`,
		Command:      cmd.String(),
		Risk:         "high",
		ResponseChan: ch,
	}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	for i := 0; i < 80; i++ {
		state.AddMessage(session.RoleSystem, fmt.Sprintf("history line %d", i), session.ContentTypePlain)
	}
	state.SetPendingApproval(tc)
	m := testModelWith(t, service.Deps{State: state, Auth: newMemAuth()}, WithHomeDir(t.TempDir()),
		WithOpenBrowser(func(string) error { return nil }))
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.invalidateTranscript()
	m.refreshViewport()
	if m.approvalModel == nil {
		t.Fatal("setup: no approval prompt")
	}
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatalf("fixture: the transcript should overflow (%d lines in %d rows)",
			m.viewport.TotalLineCount(), m.viewport.Height())
	}

	// PgDn pages the review, not the transcript.
	transcript := m.viewport.YOffset()
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.approvalModel.review.offset == 0 {
		t.Fatal("PgDn did not page the approval review")
	}
	if m.viewport.YOffset() != transcript {
		t.Fatalf("PgDn scrolled the transcript: %d → %d", transcript, m.viewport.YOffset())
	}
	// PgUp pages it back.
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.approvalModel.review.offset != 0 {
		t.Fatalf("PgUp did not page the review back: offset %d", m.approvalModel.review.offset)
	}

	// Ctrl+U still scrolls the transcript.
	m, _ = press(m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if m.viewport.YOffset() == transcript {
		t.Fatal("Ctrl+U no longer scrolls the transcript")
	}
	// …and so does the mouse wheel.
	before := m.viewport.YOffset()
	m, _ = press(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.viewport.YOffset() == before {
		t.Fatalf("the mouse wheel no longer scrolls the transcript (offset %d)", before)
	}

	// Every choice is reachable through the pinned selector.
	for i, want := range []string{"allow", "deny", "edit", "always", "session"} {
		m.syncPromptReviewHeights()
		frame := ansi.Strip(m.viewString())
		if !strings.Contains(frame, want) {
			t.Errorf("choice %d (%q) is not shown in the frame:\n%s", i, want, frame)
		}
		if !strings.Contains(frame, fmt.Sprintf("%d/5", i+1)) {
			t.Errorf("the selector does not report position %d/5:\n%s", i+1, frame)
		}
		m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyRight})
	}
	// Back to deny, then confirm it with the two-step gesture.
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := m.approvalModel.choice; got != choiceDeny {
		t.Fatalf("walking the selector back did not reach deny: %v", got)
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.viewString()), "Press Enter again to confirm") {
		t.Fatal("the armed confirmation is not visible")
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if d := <-ch; d.Approved {
		t.Fatalf("the two-step confirmation settled the wrong decision: %+v", d)
	}

	// A replacement request starts the review at the top again.
	tc2ch := make(chan session.UserApprovalDecision, 1)
	tc2 := &session.PendingToolCall{
		ID: "tc-nav-2", Name: "shell.run", Args: `{"command":"next"}`,
		Command: cmd.String(), Risk: "high", ResponseChan: tc2ch,
	}
	state.SetPendingApproval(tc)
	// Any ordinary key rebuilds the prompt for the new pending request.
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	resentKey := m.sess.Pending().Approvals[0].Key
	if m.approvalModel == nil || m.approvalModel.a.Key != resentKey {
		t.Fatal("setup: the prompt was not rebuilt for the re-sent request")
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.approvalModel.review.offset == 0 {
		t.Fatal("setup: the review should be paged away from the top")
	}
	state.SetPendingApproval(tc2)
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	replacementKey := m.sess.Pending().Approvals[0].Key
	if m.approvalModel.a.Key != replacementKey {
		t.Fatalf("the replacement request did not rebuild the prompt: %q", m.approvalModel.a.Key)
	}
	if m.approvalModel.review.offset != 0 {
		t.Fatalf("the replacement request resumed the old review offset: %d", m.approvalModel.review.offset)
	}
	// The fresh review still pages normally.
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.approvalModel.review.offset == 0 {
		t.Fatal("PgDn on the replacement request did not move its review")
	}

	// A forced prompt offers only the choices it may offer: allow, deny, edit.
	sudo := &session.PendingToolCall{ID: "tc-sudo", Name: "shell.sudo", Args: `{"command":"id"}`}
	state2 := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: unnamedSessionID})
	state2.SetPendingApproval(sudo)
	f := testModelWith(t, service.Deps{State: state2, Auth: newMemAuth()}, WithHomeDir(t.TempDir()),
		WithOpenBrowser(func(string) error { return nil }))
	f, _ = press(f, tea.WindowSizeMsg{Width: 80, Height: 24})
	if f.approvalModel == nil {
		t.Fatal("setup: no forced prompt")
	}
	if len(f.approvalModel.candidates) != 3 {
		t.Fatalf("the forced prompt offers %v, want allow/deny/edit", f.approvalModel.candidates)
	}
	f, _ = press(f, tea.KeyPressMsg{Code: tea.KeyRight})
	f, _ = press(f, tea.KeyPressMsg{Code: tea.KeyRight})
	f, _ = press(f, tea.KeyPressMsg{Code: tea.KeyRight})
	if f.approvalModel.selected != 2 {
		t.Fatalf("the forced selector ran past its last choice: %d", f.approvalModel.selected)
	}
	if got := ansi.Strip(f.viewString()); !strings.Contains(got, "3/3") {
		t.Fatalf("the forced selector does not report 3/3:\n%s", got)
	}
}

func TestApprovalViewShowsMnemonicHints(t *testing.T) {
	am := newApprovalModel(acpwire.ApprovalRequest{Tool: "shell.run"}, acpwire.Sandbox{}, false, 80)
	for _, want := range []struct {
		key   string
		label string
	}{
		{"a", "allow"},
		{"s", "session"},
		{"e", "edit"},
		{"d", "deny"},
	} {
		am, _ = am.Update(keyPress(want.key))
		view := ansi.Strip(am.View())
		if !strings.Contains(view, want.key+" "+want.label) {
			t.Errorf("mnemonic key %q not shown beside %q in approval view:\n%s", want.key, want.label, view)
		}
	}
}
