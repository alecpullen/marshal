package tui

import (
	"fmt"
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
// form, then returns the model. It uses an arrow key, which the masked field
// ignores, so priming does not type anything into the password.
func primeSudoAuth(t *testing.T, m Model) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(Model)
	if m.sudoAuthModel == nil {
		t.Fatal("first message should lazily build the sudo-auth model")
	}
	if stateOf(m).PendingSudoAuth() == nil {
		t.Fatal("lazy build must not clear the pending prompt")
	}
	return m
}

// TestSudoAuthFirstKeystrokeIsNotDropped pins the fix for the dropped first
// keystroke: the prompt appears asynchronously, so the user is often already
// typing when it opens. The build path must apply the message rather than
// discard it, or the submitted password is silently corrupted.
func TestSudoAuthFirstKeystrokeIsNotDropped(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sa := &session.PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	stateOf(m).SetPendingSudoAuth(sa)

	// No priming: the very first keystroke must land in the field.
	for _, r := range "pw" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	updated, _ := m.Update(sudoAuthKeyPress("enter"))
	m = updated.(Model)

	select {
	case d := <-sa.ResponseChan:
		if !d.OK || string(d.Password) != "pw" {
			// The message never formats the password: a failure report is not a
			// place for a secret.
			t.Fatal("the decision is not OK with the full typed password (first keystroke dropped?)")
		}
	default:
		t.Fatal("Enter did not respond on the pending prompt")
	}
}

func TestSudoAuthModalMasksAndShowsCommand(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sa := &session.PendingSudoAuth{
		Command:      "systemctl restart nginx",
		Reason:       "restart the web server",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	stateOf(m).SetPendingSudoAuth(sa)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "systemctl restart nginx") {
		t.Fatalf("view does not show the verbatim command:\n%s", view)
	}

	// Type a password and assert it never appears in the rendered view. The
	// assertion is on the value the field actually holds, so it cannot pass
	// vacuously if a keystroke were dropped.
	for _, r := range "hunter2" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if m.sudoAuthModel == nil {
		t.Fatal("sudo-auth model missing after typing")
	}
	typed := m.sudoAuthModel.input.Value()
	if typed != "hunter2" {
		// The message reports what went wrong without formatting the secret.
		t.Fatal("the masked field did not hold the full typed password")
	}
	if strings.Contains(stripANSI(m.View().Content), typed) {
		t.Fatalf("typed password appears in the view:\n%s", stripANSI(m.View().Content))
	}
}

func TestSudoAuthEscDenies(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	sa := &session.PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	}
	stateOf(m).SetPendingSudoAuth(sa)
	m = primeSudoAuth(t, m)

	updated, _ := m.Update(sudoAuthKeyPress("esc"))
	m = updated.(Model)

	select {
	case d := <-sa.ResponseChan:
		if d.OK {
			t.Fatal("Esc must deny")
		}
		if len(d.Password) != 0 {
			t.Fatal("the denied decision carries a password")
		}
	default:
		t.Fatal("Esc did not respond on the pending prompt")
	}
	if stateOf(m).PendingSudoAuth() != nil {
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
	stateOf(m).SetPendingSudoAuth(sa)
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
			t.Fatal("the decision is not OK with the typed bytes")
		}
	default:
		t.Fatal("Enter did not respond on the pending prompt")
	}
}

// sudoLongPendingAuth builds a pending privileged prompt whose command and
// reason are each far taller than one review page, with unique markers at both
// ends of each so paging can be checked end to end.
func sudoLongPendingAuth(t *testing.T, ch chan session.SudoAuthDecision) *session.PendingSudoAuth {
	t.Helper()
	var command strings.Builder
	for i := 1; i <= 6; i++ {
		fmt.Fprintf(&command, "CMD%d echo %s\n", i, strings.Repeat("x", 90))
	}
	command.WriteString("CMDEND echo done")
	var reason strings.Builder
	reason.WriteString("RSNSTART ")
	for i := 0; i < 40; i++ {
		reason.WriteString("the guardrail fired on this privileged command ")
	}
	reason.WriteString("RSNEND")
	return &session.PendingSudoAuth{
		Command:      command.String(),
		Reason:       reason.String(),
		ResponseChan: ch,
	}
}

// TestSudoReviewPagesFullCommandAndReason pins that the whole privileged
// command and its context stay reachable at the minimum supported size: both
// ends are one page away, and the masked field never scrolls away with the
// body.
func TestSudoReviewPagesFullCommandAndReason(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	for i := 0; i < 80; i++ {
		stateOf(m).AddMessage(session.RoleSystem, fmt.Sprintf("history line %d", i), session.ContentTypePlain)
	}
	stateOf(m).SetPendingSudoAuth(sudoLongPendingAuth(t, make(chan session.SudoAuthDecision, 1)))
	m.refreshViewport()
	m = primeSudoAuth(t, m)
	if !m.sudoActionable() {
		t.Fatal("80x24: the privileged prompt should be actionable")
	}

	page := func() string {
		m.syncPromptReviewHeights()
		return stripANSI(m.viewString())
	}
	// The masked field and its key hint are pinned: they are on screen on
	// every page, whatever the body is showing. The hint sits directly below
	// the field, so it is the deterministic witness for both rows.
	pinned := func(name, frame string) {
		t.Helper()
		if !strings.Contains(frame, "[↵] authenticate  [Esc] deny") {
			t.Errorf("%s: the pinned field hint is missing:\n%s", name, frame)
		}
	}

	first := page()
	pinned("first page", first)
	for _, want := range []string{"Run as root?", "CMD1"} {
		if !strings.Contains(first, want) {
			t.Fatalf("the review does not open on the start of the command (%q missing):\n%s", want, first)
		}
	}
	if strings.Contains(first, "RSNEND") || strings.Contains(first, "CMDEND") {
		t.Fatalf("fixture: the whole command and reason already fit one page:\n%s", first)
	}

	transcript := m.viewport.YOffset()
	seen := first
	for i := 0; i < 40; i++ {
		m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
		frame := page()
		pinned(fmt.Sprintf("page %d", i+1), frame)
		seen += "\n" + frame
	}
	if m.viewport.YOffset() != transcript {
		t.Fatalf("PgDn scrolled the transcript: %d → %d", transcript, m.viewport.YOffset())
	}
	for _, marker := range []string{"CMD1", "CMD2", "CMD3", "CMD4", "CMD5", "CMD6", "CMDEND", "RSNSTART", "RSNEND"} {
		if !strings.Contains(seen, marker) {
			t.Errorf("marker %q never became visible while paging the review", marker)
		}
	}

	for i := 0; i < 40; i++ {
		m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	back := page()
	if !strings.Contains(back, "CMD1") {
		t.Fatalf("paging back does not return to the start of the command:\n%s", back)
	}
	if strings.Contains(back, "RSNEND") {
		t.Fatalf("paging back left the end of the reason on screen:\n%s", back)
	}
	pinned("after paging back", back)
}

// TestSudoReviewPreservesMaskedPasswordAcrossResizeAndScroll pins that the
// review window is purely presentational: reflowing it and paging it never
// touches the typed bytes or the decision, and no rendered frame ever carries
// the password. The assertions compare values instead of formatting them, so a
// failure cannot print the secret either.
func TestSudoReviewPreservesMaskedPasswordAcrossResizeAndScroll(t *testing.T) {
	const secret = "sudo-pw-8f21-must-stay-masked"
	ch := make(chan session.SudoAuthDecision, 1)
	m := newViewTestModel(t, 80, 24)
	stateOf(m).SetPendingSudoAuth(sudoLongPendingAuth(t, ch))
	m = primeSudoAuth(t, m)

	for _, r := range secret {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if m.sudoAuthModel == nil {
		t.Fatal("sudo-auth model missing after typing")
	}
	if m.sudoAuthModel.input.Value() != secret {
		t.Fatal("the masked field did not hold the full typed password")
	}

	frames := make([]string, 0, 6)
	render := func() {
		m.syncPromptReviewHeights()
		frames = append(frames, m.viewString())
	}
	render()
	// A resize reflows the review…
	m, _ = press(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	render()
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	render()
	// …and paging moves the body. Neither may touch the field or the decision.
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	render()
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	render()
	if m.sudoAuthModel.review.offset == 0 {
		t.Fatal("setup: the review should be paged away from the top")
	}
	if m.sudoAuthModel.IsDone() {
		t.Fatal("resizing or paging settled the prompt")
	}
	if m.sudoAuthModel.input.Value() != secret {
		t.Fatal("resizing or paging changed the typed password")
	}
	for i, frame := range frames {
		if strings.Contains(frame, secret) || strings.Contains(stripANSI(frame), secret) {
			t.Fatalf("frame %d exposes the typed password", i)
		}
	}

	m, _ = press(m, sudoAuthKeyPress("enter"))
	select {
	case d := <-ch:
		if !d.OK {
			t.Fatal("Enter denied the prompt instead of authenticating")
		}
		if string(d.Password) != secret {
			t.Fatal("the submitted bytes are not the typed password")
		}
	default:
		t.Fatal("Enter did not respond on the pending prompt")
	}
}

// TestSudoReviewHiddenPromptCannotAuthenticate pins that an invisible prompt
// accepts no password byte and no authentication: below the supported minimum,
// behind a full-frame panel and with no room for its body only Escape reaches
// the form, so denial still works. It also pins that a replacement request
// starts its review at the top again.
func TestSudoReviewHiddenPromptCannotAuthenticate(t *testing.T) {
	ch := make(chan session.SudoAuthDecision, 1)
	m := newViewTestModel(t, 100, 30)
	stateOf(m).SetPendingSudoAuth(&session.PendingSudoAuth{Command: "id", ResponseChan: ch})
	m = primeSudoAuth(t, m)
	for _, r := range "pw" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if m.sudoAuthModel == nil || m.sudoAuthModel.input.Value() != "pw" {
		t.Fatal("setup: the masked field did not hold the typed password")
	}

	// Below the supported minimum the frame is hidden, and neither a keypress,
	// a paste nor Enter may reach the field or settle a decision.
	m, _ = press(m, tea.WindowSizeMsg{Width: 60, Height: 15})
	if m.sudoActionable() {
		t.Fatal("below the minimum terminal the privileged prompt must not be actionable")
	}
	held := m.sudoAuthModel.input.Value()
	m, _ = press(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, _ = press(m, tea.PasteMsg{Content: "pasted"})
	m, _ = press(m, sudoAuthKeyPress("enter"))
	select {
	case <-ch:
		t.Fatal("a hidden privileged prompt settled a decision")
	default:
	}
	if m.sudoAuthModel == nil {
		t.Fatal("a hidden privileged prompt must stay pending")
	}
	if m.sudoAuthModel.input.Value() != held {
		t.Fatal("a hidden privileged prompt read a keystroke or a paste")
	}

	// Escape still denies: the user can always dismiss a prompt they cannot
	// see.
	m, _ = press(m, sudoAuthKeyPress("esc"))
	select {
	case d := <-ch:
		if d.OK || len(d.Password) != 0 {
			t.Fatal("Escape from a hidden prompt must deny without a password")
		}
	default:
		t.Fatal("Escape from a hidden prompt did not settle the denial")
	}
	if stateOf(m).PendingSudoAuth() != nil || m.sudoAuthModel != nil {
		t.Fatal("the denied prompt was not cleared")
	}

	// A replacement request pages normally…
	ch2 := make(chan session.SudoAuthDecision, 1)
	sa2 := sudoLongPendingAuth(t, ch2)
	stateOf(m).SetPendingSudoAuth(sa2)
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = primeSudoAuth(t, m)
	if pending := m.sess.Pending().Sudo; pending == nil || m.sudoAuthModel.sa.Key != pending.Key {
		t.Fatal("the replacement request did not rebuild the prompt")
	}
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.sudoAuthModel.review.offset == 0 {
		t.Fatal("PgDn did not page the replacement request's review")
	}

	// …but a full-frame panel hides it: Enter must settle nothing.
	m.openSettingsBrowser("")
	if !m.dock.FullFrameOpen() {
		t.Fatal("fixture: /settings should own the whole frame")
	}
	if m.sudoActionable() {
		t.Fatal("a privileged prompt behind a full-frame panel must not be actionable")
	}
	m, _ = press(m, sudoAuthKeyPress("enter"))
	select {
	case <-ch2:
		t.Fatal("a privileged prompt behind a full-frame panel settled a decision")
	default:
	}
	if m.sudoAuthModel == nil {
		t.Fatal("a prompt behind a full-frame panel must stay pending on the same request")
	}
	if pending := m.sess.Pending().Sudo; pending == nil || m.sudoAuthModel.sa.Key != pending.Key {
		t.Fatal("a prompt behind a full-frame panel must stay pending on the same request")
	}

	// Back on screen, a replacement request starts its review at the top.
	m.dock.CloseNow()
	m, _ = press(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.sudoActionable() {
		t.Fatal("the prompt should be actionable again once the panel is closed")
	}
	ch3 := make(chan session.SudoAuthDecision, 1)
	sa3 := &session.PendingSudoAuth{Command: "id", ResponseChan: ch3}
	stateOf(m).SetPendingSudoAuth(sa3)
	m, _ = press(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if pending := m.sess.Pending().Sudo; pending == nil || m.sudoAuthModel.sa.Key != pending.Key {
		t.Fatal("the replacement request did not rebuild the prompt")
	}
	if m.sudoAuthModel.review.offset != 0 {
		t.Fatalf("the replacement request resumed the old review offset: %d", m.sudoAuthModel.review.offset)
	}
	m, _ = press(m, sudoAuthKeyPress("enter"))
	select {
	case d := <-ch3:
		if !d.OK || len(d.Password) != 0 {
			t.Fatal("the on-screen replacement prompt did not authenticate")
		}
	default:
		t.Fatal("the on-screen replacement prompt did not respond")
	}
}

func TestSudoAuthBlocksModeCyclingKeys(t *testing.T) {
	m := newViewTestModel(t, 100, 30)
	stateOf(m).SetPendingSudoAuth(&session.PendingSudoAuth{
		Command:      "id",
		ResponseChan: make(chan session.SudoAuthDecision, 1),
	})
	m = primeSudoAuth(t, m)

	before := stateOf(m).InteractionState().Mode
	updated, _ := m.Update(sudoAuthKeyPress("tab"))
	m = updated.(Model)
	if stateOf(m).InteractionState().Mode != before {
		t.Fatalf("mode changed from %v to %v while a sudo prompt was pending", before, stateOf(m).InteractionState().Mode)
	}
}
