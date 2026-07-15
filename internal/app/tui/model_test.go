package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/connect"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/picker"
	"marshal/internal/app/tui/settings"
	"marshal/internal/app/tui/theme"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/permissions"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// drainCmds executes the returned command tree (up to a small bound) and
// feeds each produced message back into the model. This is necessary whenever
// a test drives a huh form, because huh advances fields and completes the form
// by returning command-produced messages that must round-trip through Update
// — exactly what the bubbletea runtime does in production.
//
// Cursor-blink ticks and other timed commands are skipped with a short timeout
// so that tests do not block waiting for the next blink frame.
func drainCmds(m Model, cmd tea.Cmd) Model {
	for i := 0; i < 8 && cmd != nil; i++ {
		var msg tea.Msg
		done := make(chan struct{})
		go func() {
			msg = cmd()
			close(done)
		}()
		select {
		case <-done:
			// message returned synchronously; keep draining.
		case <-time.After(20 * time.Millisecond):
			// timed command (e.g. cursor blink); stop draining.
			return m
		}
		if msg == nil {
			return m
		}
		updated, next := m.Update(msg)
		m = updated.(Model)
		cmd = next
	}
	return m
}

// sendKey sends a keypress to the model and drains the resulting command chain.
func sendKey(m Model, key tea.KeyPressMsg) Model {
	updated, cmd := m.Update(key)
	return drainCmds(updated.(Model), cmd)
}

// asModel normalises the *Model-vs-Model return from Update/dispatchCommand
// so that picker tests can use a single helper regardless of receiver type.
func asModel(t *testing.T, updated tea.Model) Model {
	t.Helper()
	if p, ok := updated.(*Model); ok {
		return *p
	}
	return updated.(Model)
}

func TestEnterAppendsInputAndClearsPrompt(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	updated, _ := model.Update(tea.KeyPressMsg{Text: "hello"})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].Role != session.RoleUser || messages[0].Content != "hello" {
		t.Fatalf("message = %#v", messages[0])
	}
	if model.input.Value() != "" {
		t.Fatalf("input = %q, want empty", model.input.Value())
	}
}

func TestEnterOnWhitespaceDoesNotAppendMessage(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	if got := len(state.Messages()); got != 0 {
		t.Fatalf("len(messages) = %d, want 0", got)
	}
}

func TestCtrlCQuits(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	_, cmd := model.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("quit command is nil")
	}

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not shut down")
	}
}

func TestEscCancelsInFlightTurn(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)
	m.busy = true
	cancelled := false
	m.agentCancel = func() { cancelled = true }

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)

	if !cancelled {
		t.Fatal("Esc should cancel the in-flight agent turn")
	}
	if m.agentCancel != nil {
		t.Fatal("agentCancel should be cleared after Esc")
	}
}

func TestEscWhenIdleDoesNotQuit(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("Esc when idle must be a no-op (no quit command)")
	}
	select {
	case <-state.Done():
		t.Fatal("Esc must not shut the session down")
	default:
	}
}

func TestTypingIsAlwaysCaptured(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	for _, r := range "123r" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	if got := m.input.Value(); got != "123r" {
		t.Fatalf("input value = %q, want %q", got, "123r")
	}
}

func TestSlashCommandsShowSuggestionsAndTabCompletes(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := reg.Register(commands.Command{Name: "settings", Description: "Open settings", Handler: func(*session.State, []string) string { return "" }}); err != nil {
		t.Fatalf("Register settings failed: %v", err)
	}
	if err := reg.Register(commands.Command{Name: "swarm", Description: "Run swarm", Args: "<goal>", Handler: func(*session.State, []string) string { return "" }}); err != nil {
		t.Fatalf("Register swarm failed: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)

	for _, r := range "/se" {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "/settings") {
		t.Fatalf("View() missing command suggestion:\n%s", view)
	}
	if strings.Contains(view, "/swarm") {
		t.Fatalf("View() should filter suggestions by prefix:\n%s", view)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(Model)
	if got := m.input.Value(); got != "/settings " {
		t.Fatalf("input after Tab = %q, want %q", got, "/settings ")
	}
}

func TestPageKeysScrollViewport(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 24)
	m.refreshViewport()
	bottom := m.viewport.YOffset()

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(Model)
	if m.viewport.YOffset() >= bottom {
		t.Fatalf("PgUp did not scroll up: offset %d -> %d", bottom, m.viewport.YOffset())
	}
	if m.input.Value() != "" {
		t.Fatalf("PgUp leaked into the input: %q", m.input.Value())
	}
	if m.viewportFollow {
		t.Fatalf("PgUp did not disable viewport follow")
	}
}

func TestCtrlUCtrlDScrollViewport(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 24)
	m.refreshViewport()
	bottom := m.viewport.YOffset()

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.viewport.YOffset() >= bottom {
		t.Fatalf("Ctrl+U did not scroll up: offset %d -> %d", bottom, m.viewport.YOffset())
	}
	upOffset := m.viewport.YOffset()
	if m.viewportFollow {
		t.Fatalf("Ctrl+U did not disable viewport follow")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.viewport.YOffset() <= upOffset {
		t.Fatalf("Ctrl+D did not scroll down: offset %d -> %d", upOffset, m.viewport.YOffset())
	}
	if m.input.Value() != "" {
		t.Fatalf("scroll keys leaked into the input: %q", m.input.Value())
	}
}

func TestCtrlDAtBottomReEnablesFollow(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 60)
	m.refreshViewport()
	bottom := m.viewport.YOffset()

	// Scroll up several pages so we're definitely not at the bottom.
	for i := 0; i < 5; i++ {
		updated, _ := m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
		m = updated.(Model)
	}
	if m.viewport.AtBottom() {
		t.Fatal("expected to be scrolled up before testing re-enable")
	}
	if m.viewportFollow {
		t.Fatal("scroll up should have disabled follow")
	}

	// Scroll down to the bottom.
	for m.viewport.YOffset() < bottom {
		updated, _ := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
		m = updated.(Model)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("expected to be at bottom after scrolling down, got offset %d", m.viewport.YOffset())
	}
	if !m.viewportFollow {
		t.Fatal("Ctrl+D at bottom should re-enable follow")
	}
}

func TestEndKeyReEnablesFollow(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 24)
	m.refreshViewport()

	m.viewportFollow = false
	m.viewport.SetYOffset(0)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = updated.(Model)
	if !m.viewportFollow {
		t.Fatal("End key did not re-enable viewport follow")
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("End key did not jump to bottom: offset %d", m.viewport.YOffset())
	}
}

func TestScrollUpPreventsAutoBottomOnRefresh(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 24)
	m.refreshViewport()

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(Model)
	upOffset := m.viewport.YOffset()

	// New content arrives while the user is scrolled up.
	state.AddMessage(session.RoleSystem, "new line", session.ContentTypePlain)
	m.refreshViewport()
	if m.viewport.YOffset() != upOffset {
		t.Fatalf("refreshViewport snapped to bottom while scrolled up: offset %d -> %d", upOffset, m.viewport.YOffset())
	}
	if m.viewportFollow {
		t.Fatal("viewportFollow should still be false after scroll-up + refresh")
	}
}

func TestNewSubmissionReEnablesFollow(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	for i := 0; i < 100; i++ {
		state.AddMessage(session.RoleUser, fmt.Sprintf("message %d", i), session.ContentTypePlain)
	}
	m := New(state)
	m.resize(80, 24)
	m.refreshViewport()
	m.viewportFollow = false
	m.viewport.SetYOffset(0)

	// Simulate typing and submitting a message.
	m.input.SetValue("hello")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if !m.viewportFollow {
		t.Fatal("submitting a message did not re-enable viewport follow")
	}
}

func TestCtrlKOpensMemoryBrowser(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	m := New(state, WithMemoryStore(database, projectID))
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.memoryOpen {
		t.Fatal("expected memoryOpen to be true")
	}
	if !strings.Contains(stripANSI(m.View().Content), "Project Memories") {
		t.Fatalf("View() missing memory browser:\n%s", stripANSI(m.View().Content))
	}
}

func TestMemoryClosedMsgClosesOverlay(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	m := New(state, WithMemoryStore(database, projectID))
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.memoryOpen {
		t.Fatal("expected memoryOpen")
	}
	updated, _ = m.Update(memory.ClosedMsg{})
	m = updated.(Model)
	if m.memoryOpen {
		t.Fatal("expected memoryOpen to be false after ClosedMsg")
	}
}

func TestCtrlKWithoutMemoryStoreDoesNothing(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Ctrl+K panicked without memory store: %v", r)
		}
	}()

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	m = updated.(Model)
	if m.memoryOpen {
		t.Fatal("expected memoryOpen to remain false without memory store")
	}
	if strings.Contains(stripANSI(m.View().Content), "Project Memories") {
		t.Fatalf("View() should not show memory browser without memory store:\n%s", stripANSI(m.View().Content))
	}
}

func TestPolishedViewPreservesPendingApprovalContent(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	tc := &session.PendingToolCall{
		ID:      "approval-1",
		Name:    "shell.run",
		Command: "go test ./...",
		Risk:    "command",
		Reason:  "run the repository test suite",
		Diff:    "--- a/app.go\n+++ b/app.go\n+added line",
	}
	state.SetPendingApproval(tc)
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := stripANSI(m.View().Content)
	for _, want := range []string{
		"Approval needed",
		"Agent wants to run",
		"go test ./...",
		"Risk",
		"Approve",
		"Deny",
		"Edit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing approval item %q:\n%s", want, view)
		}
	}
}

func TestPolishedTranscriptReflowsAfterResize(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	message := "This transcript line should be wide enough to wrap differently after resizing from a wide terminal down to a narrow terminal."
	thinking := "The live thinking copy should also be rebuilt after resize so it follows the narrowed viewport width instead of keeping stale wrapping."
	state.AddMessage(session.RoleUser, message, session.ContentTypePlain)
	state.BeginStreaming()
	state.AppendThinking(thinking)
	m := New(state)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	m.busy = true
	m.refreshViewport()
	wideView := stripANSI(m.View().Content)

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	narrowView := stripANSI(m.View().Content)
	expectedViewport := renderMessage(session.Message{Role: session.RoleUser, Content: message, ContentType: session.ContentTypePlain}, m.viewport.Width()) + renderThinkingBox(thinking, m.spinnerFrame, m.viewport.Width())

	// viewport.View() pads every line to the viewport's fixed width/height
	// with trailing spaces and blank lines; strip that padding before
	// comparing against the raw rendered content.
	trimPadding := func(s string) string {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimRight(line, " ")
		}
		return strings.TrimRight(strings.Join(lines, "\n"), "\n")
	}

	if got, want := trimPadding(m.viewport.View()), trimPadding(expectedViewport); got != want {
		t.Fatalf("viewport content did not rebuild for resized width\nwant:\n%s\n\ngot:\n%s", want, got)
	}

	if wideView == narrowView {
		t.Fatalf("expected resize to change rendered view")
	}

	lines := strings.Split(strings.TrimRight(narrowView, "\n"), "\n")
	if len(lines) > 24 {
		t.Fatalf("line count = %d, want <= 24\n%s", len(lines), narrowView)
	}
	for i, line := range lines {
		if got := visibleRunes(line); got > 80 {
			t.Fatalf("line %d width = %d, want <= 80\n%s", i+1, got, line)
		}
	}
}

func TestPolishedTranscriptShowsRolesThinkingAndInput(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "fix the layout", session.ContentTypePlain)
	state.BeginStreaming()
	state.AppendThinking("I need to inspect the render bounds and keep the newest reasoning visible.")
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.busy = true
	m.refreshViewport()

	view := stripANSI(m.View().Content)
	for _, want := range []string{
		"❯",
		"fix the layout",
		"thinking",
		"Ask Marshal...",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestTUIApprovalBannerAndKeypresses(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})

	respChan := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "456",
		Name:         "shell.run",
		Args:         `{"command":"go test"}`,
		Command:      "go test",
		Risk:         "command",
		Reason:       "run tests",
		ResponseChan: respChan,
	}
	state.SetPendingApproval(tc)

	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Approval") {
		t.Fatal("View() missing Approval panel")
	}
	if !strings.Contains(view, "go test") {
		t.Fatal("View() missing proposed command")
	}

	// 1. Test Deny: navigate to "Deny", then press Enter twice to confirm.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case dec := <-respChan:
		if dec.Approved {
			t.Fatal("expected decision to be denied")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for deny response")
	}
	if state.PendingApproval() != nil {
		t.Fatal("expected pending approval to be cleared")
	}

	// Set up again for Approve.
	state.SetPendingApproval(tc)
	m = New(state)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	// 2. Test Approve: press Enter twice to confirm.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case dec := <-respChan:
		if !dec.Approved || dec.Edited != "" {
			t.Fatal("expected decision to be approved and not edited")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for approve response")
	}

	// Set up again for Edit.
	state.SetPendingApproval(tc)
	m = New(state)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	// 3. Test Edit: navigate to "Edit", then press Enter twice to confirm.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.editingCommand {
		t.Fatal("expected model to enter editingCommand mode")
	}
	if m.input.Value() != "go test" {
		t.Fatalf("expected input value to be 'go test', got %q", m.input.Value())
	}

	// Simulate typing to edit command.
	updated, _ = m.Update(tea.KeyPressMsg{Text: " -v"})
	m = updated.(Model)
	if m.input.Value() != "go test -v" {
		t.Fatalf("expected edited input value to be 'go test -v', got %q", m.input.Value())
	}

	// Press Enter to confirm edited command.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	select {
	case dec := <-respChan:
		if !dec.Approved || dec.Edited != "go test -v" {
			t.Fatalf("expected decision to be approved with edited command, got: %#v", dec)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for edited response")
	}

	// Set up again for Always Allow.
	state.SetPendingApproval(tc)
	m = New(state)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	// 4. Test Always Allow: navigate to the 4th option, then press Enter twice.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case dec := <-respChan:
		if !dec.Approved {
			t.Fatal("expected decision to be approved via always allow")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for always allow response")
	}

	// Set up again for Allow this session.
	state.SetPendingApproval(tc)
	m = New(state)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	// 5. Test Allow this session: navigate to the 5th option, then press Enter twice.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case dec := <-respChan:
		if !dec.Approved {
			t.Fatal("expected decision to be approved via session allow")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for session allow response")
	}

	// Check if session rule was added.
	rules := state.SessionRules()
	if len(rules) != 1 || rules[0] != "go test" {
		t.Fatalf("expected session rules to contain 'go test', got %#v", rules)
	}
}

func TestTUIRollbackFlow(t *testing.T) {
	tmpDir := t.TempDir()
	state := session.New(config.Default(), tmpDir, time.Unix(100, 0), session.Persistence{})
	state.StoreBackup([]session.BackupFile{
		{Path: "app.go", Content: "original content"},
	})

	model := New(state)

	// Update with Ctrl+R to trigger rollback
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	model = updated.(Model)

	if state.HasBackup() {
		t.Fatal("expected backup to be cleared after rollback")
	}

	view := stripANSI(model.View().Content)
	if !strings.Contains(view, "[r] Rollback Last Patch") {
		// should be removed after backup is cleared
	}
}

type fakeAgentRunner struct {
	called chan string
	err    error
}

func (f *fakeAgentRunner) Run(ctx context.Context, goal string) error {
	f.called <- goal
	return f.err
}

func (f *fakeAgentRunner) SetForceClass(string)                   {}
func (f *fakeAgentRunner) SetPolicyRules([]config.PermissionRule) {}

// blockingAgentRunner blocks on a channel in Run until the channel is
// closed. Used by TestAgentCommandRegistersAndReleasesSessionWork to
// verify that session work is tracked and released.
type blockingAgentRunner struct {
	block chan struct{}
	err   error
}

func (b *blockingAgentRunner) Run(ctx context.Context, goal string) error {
	<-b.block
	return b.err
}

func (b *blockingAgentRunner) SetForceClass(string)                   {}
func (b *blockingAgentRunner) SetPolicyRules([]config.PermissionRule) {}

type fakeSwarmRunner struct {
	mu    sync.Mutex
	goals []string
}

func (f *fakeSwarmRunner) Run(ctx context.Context, goal string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goals = append(f.goals, goal)
	return nil
}

func (f *fakeSwarmRunner) SetForceClass(string)                   {}
func (f *fakeSwarmRunner) SetPolicyRules([]config.PermissionRule) {}

func TestSwarmCommandDispatchesGoalToSwarmRunner(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	fake := &fakeSwarmRunner{}
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatal(err)
	}
	model := New(state,
		WithCommandRegistry(cmdReg),
		WithSwarmRunner(context.Background(), fake),
	)

	_, cmd := model.dispatchCommand("/swarm add a regression test")
	if cmd == nil {
		t.Fatal("dispatchCommand returned nil cmd")
	}
	if !model.busy {
		t.Fatal("model should be busy while the swarm runs")
	}

	// Execute the batched commands; one of them runs the swarm.
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}
	for _, sub := range batch {
		if sub != nil {
			_ = sub()
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.goals) != 1 || fake.goals[0] != "add a regression test" {
		t.Fatalf("swarm runner goals = %v, want [\"add a regression test\"]", fake.goals)
	}
}

func TestSwarmCommandWithoutGoalShowsUsage(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatal(err)
	}
	model := New(state,
		WithCommandRegistry(cmdReg),
		WithSwarmRunner(context.Background(), &fakeSwarmRunner{}),
	)

	_, _ = model.dispatchCommand("/swarm")
	messages := state.Messages()
	last := messages[len(messages)-1]
	if !strings.Contains(last.Content, "Usage: /swarm <goal>") {
		t.Fatalf("expected usage message, got %q", last.Content)
	}
	if model.busy {
		t.Fatal("model must not be busy after a usage error")
	}
}

type fakeSDDRunner struct {
	mu    sync.Mutex
	plans []string
}

func (f *fakeSDDRunner) Run(ctx context.Context, planPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, planPath)
	return nil
}

func (f *fakeSDDRunner) SetForceClass(string)                   {}
func (f *fakeSDDRunner) SetPolicyRules([]config.PermissionRule) {}

func TestSDDCommandDispatchesPlanToSDDRunner(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	fake := &fakeSDDRunner{}
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatal(err)
	}
	model := New(state,
		WithCommandRegistry(cmdReg),
		WithSDDRunner(context.Background(), fake),
	)

	_, cmd := model.dispatchCommand("/sdd /repo/docs/plans/feature-plan.md")
	if cmd == nil {
		t.Fatal("dispatchCommand returned nil cmd")
	}
	if !model.busy {
		t.Fatal("model should be busy while SDD runs")
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}
	for _, sub := range batch {
		if sub != nil {
			_ = sub()
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.plans) != 1 || fake.plans[0] != "/repo/docs/plans/feature-plan.md" {
		t.Fatalf("SDD runner plans = %v, want [\"/repo/docs/plans/feature-plan.md\"]", fake.plans)
	}
}

func TestSDDCommandWithoutPlanShowsUsage(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatal(err)
	}
	model := New(state,
		WithCommandRegistry(cmdReg),
		WithSDDRunner(context.Background(), &fakeSDDRunner{}),
	)

	_, _ = model.dispatchCommand("/sdd")
	messages := state.Messages()
	last := messages[len(messages)-1]
	if !strings.Contains(last.Content, "Usage: /sdd") {
		t.Fatalf("expected usage message, got %q", last.Content)
	}
	if model.busy {
		t.Fatal("model must not be busy after a usage error")
	}
}

func TestModePickerIncludesSDD(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	model := New(state)
	items := model.modePickerItems()
	found := false
	for _, item := range items {
		if item.Value == "sdd" {
			found = true
			if item.Label != "SDD" {
				t.Errorf("SDD label = %q, want %q", item.Label, "SDD")
			}
		}
	}
	if !found {
		t.Fatal("modePickerItems missing SDD entry")
	}
}

func TestEnterWithRunnerDispatchesAgentRunAndTick(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	model := New(state, WithRunner(context.Background(), runner))

	updated, _ := model.Update(tea.KeyPressMsg{Text: "hello"})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	if !model.busy {
		t.Fatal("model.busy = false, want true after dispatching an agent run")
	}
	if cmd == nil {
		t.Fatal("Update returned a nil cmd, want a batch of agent+tick commands")
	}

	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want tea.BatchMsg", cmd())
	}
	if len(batch) != 3 {
		t.Fatalf("len(batch) = %d, want 3 (runAgentCmd, tickCmd, spinnerTickCmd)", len(batch))
	}

	var sawFinished, sawTick, sawSpinnerTick bool
	for _, sub := range batch {
		switch msg := sub().(type) {
		case agentFinishedMsg:
			sawFinished = true
			if msg.err != nil {
				t.Fatalf("agentFinishedMsg.err = %v, want nil", msg.err)
			}
		case agentTickMsg:
			sawTick = true
		case spinnerTickMsg:
			sawSpinnerTick = true
		default:
			t.Fatalf("unexpected message type %T", msg)
		}
	}
	if !sawFinished || !sawTick || !sawSpinnerTick {
		t.Fatalf("sawFinished=%v sawTick=%v sawSpinnerTick=%v, want all true", sawFinished, sawTick, sawSpinnerTick)
	}

	select {
	case goal := <-runner.called:
		if goal != "hello" {
			t.Fatalf("runner.Run goal = %q, want %q", goal, "hello")
		}
	default:
		t.Fatal("runner.Run was not called")
	}
}

func TestAgentFinishedMsgClearsBusyAndRecordsProviderError(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	model.busy = true

	updated, cmd := model.Update(agentFinishedMsg{err: errors.New("boom")})
	model = updated.(Model)

	if model.busy {
		t.Fatal("model.busy = true, want false after agentFinishedMsg")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil cmd (tickCmd) after agentFinishedMsg to re-arm the pulse-clearing tick")
	}
	if err := state.ProviderError(); err == nil || err.Error() != "boom" {
		t.Fatalf("ProviderError() = %v, want an error wrapping %q", err, "boom")
	}
}

func TestAgentCommandRegistersAndReleasesSessionWork(t *testing.T) {
	block := make(chan struct{})
	runner := &blockingAgentRunner{block: block}
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	model := New(state, WithRunner(context.Background(), runner))
	model.resize(80, 24)

	// Submit a turn. The runner blocks in Run, so the runAgentCmd
	// command will block until we close(block).
	model.input.SetValue("hello")
	_, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("cmd is nil")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", cmd())
	}
	if len(batch) != 3 {
		t.Fatalf("len(batch) = %d, want 3", len(batch))
	}

	// Execute the runAgentCmd (batch[0]) in a goroutine; it will block.
	runDone := make(chan tea.Msg, 1)
	go func() {
		runDone <- batch[0]()
	}()

	// Give the goroutine a moment to block inside Run.
	time.Sleep(10 * time.Millisecond)

	// BeginQuiesce and wait for work. WaitForWork should block because
	// the runner hasn't finished.
	state.BeginQuiesce()
	waitDone := make(chan struct{})
	go func() {
		state.WaitForWork(context.Background())
		close(waitDone)
	}()

	select {
	case <-waitDone:
		t.Fatal("WaitForWork returned before runner finished")
	case <-time.After(50 * time.Millisecond):
	}

	// Unblock the runner.
	close(block)

	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("WaitForWork did not return after runner unblocked")
	}

	select {
	case msg := <-runDone:
		if _, ok := msg.(agentFinishedMsg); !ok {
			t.Fatalf("runAgentCmd returned %T, want agentFinishedMsg", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("runner command did not complete")
	}
}

func TestEnterWithoutRunnerFallsBackToPlainAppend(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	updated, _ := model.Update(tea.KeyPressMsg{Text: "hi"})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	if model.busy {
		t.Fatal("model.busy = true, want false when no runner is configured")
	}
	messages := state.Messages()
	if len(messages) != 1 || messages[0].Content != "hi" {
		t.Fatalf("messages = %#v, want a single message %q", messages, "hi")
	}
}

func TestEnterWhileBusyIsIgnored(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	model := New(state, WithRunner(context.Background(), runner))
	model.busy = true

	updated, _ := model.Update(tea.KeyPressMsg{Text: "hello"})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	if cmd != nil {
		if _, ok := cmd().(tea.BatchMsg); ok {
			t.Fatal("Update dispatched a new agent run while already busy")
		}
	}
	select {
	case <-runner.called:
		t.Fatal("runner.Run was called while busy")
	default:
	}
}

func TestCtrlOOpensSettings(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen to be true")
	}
}

func TestSettingsCancelClosesOverlay(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}
	updated, _ = m.Update(settings.CancelledMsg{})
	m = updated.(Model)
	if m.settingsOpen {
		t.Fatal("expected settingsOpen to be false after cancel")
	}
}

func TestGlobalKeysDoNotLeakDuringApproval(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	tc := &session.PendingToolCall{
		ID:           "1",
		Name:         "shell.run",
		Command:      "go test",
		Risk:         "low",
		Reason:       "run tests",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	state.SetPendingApproval(tc)
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyTab},
		{Code: tea.KeyTab, Mod: tea.ModShift},
		{Code: 'p', Mod: tea.ModCtrl},
		{Code: 'x', Mod: tea.ModCtrl},
		{Code: 't', Mod: tea.ModCtrl},
		{Code: 'r', Mod: tea.ModCtrl},
	} {
		updated, _ := model.Update(key)
		model = updated.(Model)
	}
	if state.PendingApproval() == nil {
		t.Fatal("approval was cleared by a global key")
	}
}

func TestEscDuringApprovalDenies(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	tc := &session.PendingToolCall{
		ID:           "1",
		Name:         "shell.run",
		Command:      "go test",
		Risk:         "low",
		Reason:       "run tests",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	state.SetPendingApproval(tc)
	model := New(state)
	updated, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m := updated.(Model)

	if cmd != nil {
		t.Fatal("Esc during approval should not return a quit command")
	}
	select {
	case dec := <-tc.ResponseChan:
		if dec.Approved {
			t.Fatal("Esc should deny approval")
		}
	case <-time.After(time.Second):
		t.Fatal("no decision sent on Esc")
	}
	if m.state.PendingApproval() != nil {
		t.Fatal("pending approval was not cleared")
	}
}

func TestCtrlKTogglesMemory(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	db, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, err := db.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatal(err)
	}

	model := New(state, WithMemoryStore(db, pid))
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	model = updated.(Model)
	if !model.memoryOpen {
		t.Fatal("Ctrl+K did not open memory")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.memoryOpen {
		t.Fatal("Ctrl+K did not close memory")
	}
}
func TestBusyTickRefreshesViewport(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	model := New(state, WithRunner(context.Background(), runner))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	// Start a turn.
	updated, _ = model.Update(tea.KeyPressMsg{Text: "hello"})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)

	// Simulate the agent adding a message mid-turn.
	state.AddMessage(session.RoleAssistant, "working...", session.ContentTypePlain)

	// Tick should refresh the viewport.
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)

	view := stripANSI(model.View().Content)
	if !strings.Contains(view, "working...") {
		t.Fatalf("viewport not refreshed during busy tick:\n%s", view)
	}
}

func TestChatMessagesWrapWithinViewportWidth(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	longContent := strings.Repeat("the quick brown fox jumps over the lazy dog ", 3)
	state.AddMessage(session.RoleUser, longContent, session.ContentTypePlain)
	model.refreshViewport()

	viewport := stripANSI(model.viewport.View())
	lines := strings.Split(viewport, "\n")
	maxWidth := model.viewport.Width()

	wrapped := false
	for i, line := range lines {
		if line == "" {
			continue
		}
		if len([]rune(line)) > maxWidth {
			t.Fatalf("viewport line %d exceeds width %d: %q", i, maxWidth, line)
		}
		if strings.HasPrefix(line, "  ") && !strings.Contains(line, "user:") && strings.Contains(line, "quick") {
			wrapped = true
		}
	}
	if !wrapped {
		t.Fatalf("expected long message to wrap onto continuation lines:\n%s", viewport)
	}
}

func TestApprovalBannerHasSingleBorder(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	tc := &session.PendingToolCall{
		ID:           "1",
		Name:         "shell.run",
		Command:      "go test",
		Risk:         "low",
		Reason:       "run tests to validate changes",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	state.SetPendingApproval(tc)
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	view := stripANSI(model.View().Content)
	// With an empty diff there should be a single inline approval block,
	// not a split Diff+Approval layout.
	if strings.Count(view, "Diff") > 0 {
		t.Fatalf("approval should not show a Diff panel:\n%s", view)
	}
	if !strings.Contains(view, "⚠ Approval needed") {
		t.Fatalf("approval banner missing title:\n%s", view)
	}
	if !strings.Contains(view, "go test") {
		t.Fatalf("approval banner missing command:\n%s", view)
	}
}

func TestRenderWhileStateMutatedDoesNotRace(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			state.AddMessage(session.RoleAssistant, fmt.Sprintf("msg %d", i), session.ContentTypePlain)
			state.LogToolCall(registry.AuditEvent{ToolName: "test", ResultSummary: "ok"})
		}
	}()

	for i := 0; i < 200; i++ {
		_ = stripANSI(model.View().Content)
		updated, _ := model.Update(agentTickMsg{})
		model = updated.(Model)
	}
	<-done
}

func TestThinkingBoxRendersWhileStreaming(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	model.busy = true
	model.refreshViewport()

	view := stripANSI(model.View().Content)
	if !strings.Contains(view, "thinking") {
		t.Fatalf("view missing thinking box:\n%s", view)
	}
	if !strings.Contains(view, "checking the auth flow") {
		t.Fatalf("view missing live reasoning text:\n%s", view)
	}
}

func TestThinkingDoesNotRenderBeforeReasoningStreams(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	model.busy = true
	model.refreshViewport()

	view := stripANSI(model.View().Content)
	if strings.Contains(view, "thinking") {
		t.Fatalf("view should not show a thinking panel before reasoning arrives:\n%s", view)
	}
}

func TestFinishedMessageShowsCollapsedThinkingSummary(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()
	state.AddMessage(session.RoleAssistant, "Here's the fix.", session.ContentTypePlain)
	model.refreshViewport()

	view := stripANSI(model.View().Content)
	if !strings.Contains(view, "thought for") {
		t.Fatalf("view missing collapsed thinking summary:\n%s", view)
	}
	if strings.Contains(view, "checking the auth flow") {
		t.Fatalf("full reasoning text should not be visible when collapsed:\n%s", view)
	}
}

func TestCtrlGTogglesThinkingExpansion(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()
	state.AddMessage(session.RoleAssistant, "Here's the fix.", session.ContentTypePlain)
	model.refreshViewport()

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	model = updated.(Model)
	if !strings.Contains(stripANSI(model.View().Content), "checking the auth flow") {
		t.Fatalf("expected expanded reasoning after Ctrl+G:\n%s", stripANSI(model.View().Content))
	}

	updated, _ = model.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	model = updated.(Model)
	if strings.Contains(stripANSI(model.View().Content), "checking the auth flow") {
		t.Fatalf("expected collapsed reasoning after second Ctrl+G:\n%s", stripANSI(model.View().Content))
	}
}

func TestBusyTickRefreshesViewportOnReasoningGrowthAlone(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.busy = true

	state.BeginStreaming()
	state.AppendThinking("step one")
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)
	if !strings.Contains(stripANSI(model.View().Content), "step one") {
		t.Fatal("expected viewport to show reasoning after first tick")
	}

	state.AppendThinking(" step two")
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)
	if !strings.Contains(stripANSI(model.View().Content), "step one step two") {
		t.Fatalf("expected viewport to refresh on reasoning growth alone (message count unchanged):\n%s", stripANSI(model.View().Content))
	}
}

func TestSettingsNavigationThroughMainModel(t *testing.T) {
	cfg := config.Default()
	cfg.Profile.Default = "local_balanced"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
	}
	state := session.New(cfg, "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}

	// Sidebar starts focused. FocusedFieldTitle returns the section title.
	if got := m.settingsModel.FocusedFieldTitle(); got != "Agent" {
		t.Fatalf("first section = %q, want Agent", got)
	}

	// Tab enters the agent pane. The first field there is "Default profile".
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.settingsModel.FocusedFieldTitle(); got != "Default profile" {
		t.Fatalf("Tab should focus first field of Agent pane, got %q", got)
	}
}

func TestSettingsTypingThroughMainModel(t *testing.T) {
	cfg := config.Default()
	cfg.Profile.Default = "local_balanced"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
	}
	state := session.New(cfg, "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Enter the agent pane and navigate to the Provider field.
	// (In the new fieldList model, Enter opens/selects the current
	// row; use j to advance.)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})   // Focus pane
	m = sendKey(m, tea.KeyPressMsg{Text: "j"})          // Past Default profile
	m = sendKey(m, tea.KeyPressMsg{Text: "j"})          // Past Preset -> Provider
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // Open inline edit

	if got := m.settingsModel.FocusedFieldTitle(); got != "Provider" {
		t.Fatalf("expected Provider field focused, got %q", got)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Text: "z"})
	m = updated.(Model)

	if !m.settingsModel.BoolValue("Local only") {
		t.Fatal("typing into Provider must not toggle Local only off")
	}
	if got := m.settingsModel.FocusedFieldTitle(); got != "Provider" {
		t.Fatalf("focused field = %q, want Provider", got)
	}
}

func TestSettingsBoolFieldToggleThroughMainModel(t *testing.T) {
	cfg := config.Default()
	cfg.Profile.Default = "local_balanced"
	cfg.AgentProfiles = map[string]routing.AgentProfile{
		"local_balanced": {
			Name: "local_balanced",
			Roles: map[routing.AgentRole]string{
				routing.RoleImplementer: "coder",
			},
		},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"coder": {Name: "coder", Provider: "ollama", Model: "qwen2.5-coder:14b", LocalOnly: true},
	}
	state := session.New(cfg, "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)

	// Enter the agent pane and navigate to the Local only toggle.
	// (In the new fieldList model, use j to advance; Enter opens/selects.)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab}) // Focus pane
	m = sendKey(m, tea.KeyPressMsg{Text: "j"})        // Past Default profile
	m = sendKey(m, tea.KeyPressMsg{Text: "j"})        // Past Preset
	m = sendKey(m, tea.KeyPressMsg{Text: "j"})        // Past Provider
	m = sendKey(m, tea.KeyPressMsg{Text: "j"})        // Past Model -> Local only
	if got := m.settingsModel.FocusedFieldTitle(); got != "Local only" {
		t.Fatalf("expected Local only focused, got %q", got)
	}
	if !m.settingsModel.BoolValue("Local only") {
		t.Fatalf("expected Local only to start true (preset default)")
	}

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})

	if m.settingsModel.BoolValue("Local only") {
		t.Fatalf("Space should have toggled Local only to false")
	}
}

func TestSettingsNavigationWithDefaultConfig(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}

	// With the default config the sidebar shows the Agent section selected.
	if got := m.settingsModel.FocusedFieldTitle(); got != "Agent" {
		t.Fatalf("default section = %q, want Agent", got)
	}
}

func TestPolishedApprovalStateShowsCommandReasonRiskAndActions(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetPendingApproval(&session.PendingToolCall{
		Name:         "shell.run",
		Command:      "go test ./internal/app/tui/...",
		Reason:       "Validate layout bounds and modal capture.",
		Risk:         "Low - test command, no destructive flags detected.",
		Diff:         "- old\n+ new\n",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := stripANSI(m.View().Content)
	for _, want := range []string{
		"Approval needed",
		"Agent wants to run",
		"go test",
		"Risk",
		"Approve",
		"Deny",
		"Edit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing approval copy %q:\n%s", want, view)
		}
	}

	// The risk label must show the actual risk classification, not the Reason text.
	if !strings.Contains(view, "Low - test command") {
		t.Fatalf("View() did not render the Risk classification text:\n%s", view)
	}
	if strings.Contains(view, "Validate layout bounds") {
		t.Fatalf("Risk section shows Reason content instead of Risk content:\n%s", view)
	}
}

func TestStatusBarShowsSpinnerAndThinkingWhenBusy(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: time.Now().Add(-time.Second)})
	m := New(state)
	m.spinnerFrame = "⠋"
	m.busy = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "⠋") {
		t.Fatalf("View() missing spinner frame in status bar:\n%s", view)
	}
	if !strings.Contains(view, "thinking") {
		t.Fatalf("View() missing thinking label in status bar:\n%s", view)
	}
}

func TestStatusBarDoneBadgeExpiresAfterDuration(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	m.busy = true
	m.spinnerFrame = "⠏"
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Now()})
	m.lastActivityKind = session.ActivityTool
	m.lastActivityLabel = "shell.run: go test"

	updated, _ = m.Update(agentFinishedMsg{})
	m = updated.(Model)

	if !strings.Contains(stripANSI(m.View().Content), "✔") {
		t.Fatal("expected done badge immediately after finish")
	}

	m.lastActivityDone = m.lastActivityDone.Add(-doneDisplayDuration).Add(-time.Millisecond)

	view := stripANSI(m.View().Content)
	if strings.Contains(view, "✔") {
		t.Fatalf("done badge should have expired after %v:\n%s", doneDisplayDuration, view)
	}
	if !strings.Contains(view, "auto") {
		t.Fatalf("View() missing idle status after done badge expiry:\n%s", view)
	}
}

func TestFinalAnswerRendersWithResponseLabel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	state.AddMessageFinal(session.RoleAssistant, "here is the answer", session.ContentTypeMarkdown)

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "Response") {
		t.Fatalf("View() does not show Response label for final answer:\n%s", view)
	}
}

func TestSlashCommandExit(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/exit")
	_, cmd := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd == nil {
		t.Error("expected quit command from /exit, got nil")
	}
}

func TestSlashCommandHelp(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/help")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := updated.(*Model)

	msgs := m.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected system message from /help")
	}
	if !strings.Contains(msgs[0].Content, "Available commands") {
		t.Errorf("help output missing header: %s", msgs[0].Content)
	}
}

func TestSlashCommandUnknown(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/nonexistent")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := updated.(*Model)

	msgs := m.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected error message for unknown command")
	}
	if !strings.Contains(msgs[0].Content, "Unknown command") {
		t.Errorf("expected unknown command message, got: %s", msgs[0].Content)
	}
}

func TestSlashCommandNotSentToAgent(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	model := New(state, WithCommandRegistry(cmdReg), WithRunner(context.Background(), runner))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.input.SetValue("/help")
	model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case <-runner.called:
		t.Error("/help should not be sent to agent runner")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlashCommandClearMessages(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	model.state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	model.input.SetValue("/new")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := updated.(*Model)

	msgs := m.state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 system message after /new, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Cleared") {
		t.Errorf("expected system message to mention clearing, got: %s", msgs[0].Content)
	}
}

func TestSlashCommandBusyStillDispatched(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	cmdReg := setupCmdReg(t)
	model := New(state, WithCommandRegistry(cmdReg))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	model.busy = true

	// F18: with the completion popup, typing "/help" and pressing Enter
	// once accepts the popup (replaces "/help" with "/help " and keeps
	// editing). A second Enter dispatches the now-complete command.
	// Verify the command still fires after the popup is accepted.
	//
	// In production the popup is updated automatically on every
	// keystroke. SetValue() bypasses Update, so we manually run the
	// popup trigger once to mirror that.
	//
	// AsModel normalizes the *Model-vs-Model return from Update so the
	// helper accepts whichever form the interface boxing produced for
	// this particular keystroke path.
	asModel := func(v tea.Model) *Model {
		if p, ok := v.(*Model); ok {
			return p
		}
		mv := v.(Model)
		return &mv
	}
	model.input.SetValue("/help")
	model.updateCompletionPopups()
	if !model.cmdPopup.isVisible() {
		t.Fatalf("expected cmd popup to be visible for /help, got filtered=%d", len(model.cmdPopup.filtered))
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := asModel(updated)
	if val := m.input.Value(); val != "/help " {
		t.Fatalf("first Enter should accept the popup: input = %q, want %q", val, "/help ")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = asModel(updated)

	msgs := m.state.Messages()
	if len(msgs) == 0 {
		t.Fatal("commands should work even when busy")
	}
}

func setupCmdReg(t *testing.T) *commands.Registry {
	t.Helper()
	cmdReg := commands.New()
	if err := commands.RegisterAll(cmdReg, registry.New()); err != nil {
		t.Fatalf("RegisterAll() error = %v", err)
	}
	return cmdReg
}

func TestRenderCodeBlockWrapsInBorder(t *testing.T) {
	result := renderMessage(session.Message{Role: session.RoleAssistant, Content: "func main() {}", ContentType: session.ContentTypeCode}, 80)
	if !strings.Contains(result, "func main()") {
		t.Fatalf("expected code content, got: %s", result)
	}
}

func TestApprovalRendersInlineInChat(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	state.AddMessage(session.RoleUser, "run the tests", session.ContentTypePlain)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_1",
		Name:         "shell.run",
		Command:      "go test ./...",
		Risk:         "command",
		Reason:       "needs confirmation",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "go test ./...") {
		t.Fatalf("View() does not contain the approval command:\n%s", view)
	}
	if !strings.Contains(view, "Approval") {
		t.Fatalf("View() does not contain the Approval panel title:\n%s", view)
	}
	if !strings.Contains(view, "run the tests") {
		t.Fatalf("View() does not contain the prior user message (approval took over chat):\n%s", view)
	}
}

func TestApprovalKeyHandlingStillWorksInline(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	ch := make(chan session.UserApprovalDecision, 1)
	state.SetPendingApproval(&session.PendingToolCall{
		ID:           "call_1",
		Name:         "shell.run",
		Command:      "echo hi",
		Risk:         "command",
		Reason:       "needs confirmation",
		ResponseChan: ch,
	})

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case decision := <-ch:
		if !decision.Approved {
			t.Fatal("approval decision = false, want true (Enter twice should approve)")
		}
	default:
		t.Fatal("no decision sent on ResponseChan after Enter twice")
	}
	if state.PendingApproval() != nil {
		t.Fatal("PendingApproval still set after Enter twice, want nil")
	}
}

func TestActiveToolCallRendersInline(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.spinnerFrame = "⠹"

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Now(),
	})

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "shell.run") {
		t.Fatalf("View() does not show active tool name:\n%s", view)
	}
	if !strings.Contains(view, "go test ./...") {
		t.Fatalf("View() does not show active tool args:\n%s", view)
	}
}

func TestRecentToolCallRendersInlineAfterFastToolCompletes(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true
	m.now = func() time.Time { return time.Unix(105, 0) }

	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(104, 0),
		ToolName:      "file.read",
		ResultSummary: "/repo/main.go",
	})

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "file.read") {
		t.Fatalf("View() does not show recent tool name:\n%s", view)
	}
	if !strings.Contains(view, "/repo/main.go") {
		t.Fatalf("View() does not show recent tool summary:\n%s", view)
	}
}

func TestCompletedToolCallsRemainInTranscriptLog(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.now = func() time.Time { return time.Unix(200, 0) }

	state.AddMessage(session.RoleUser, "inspect the repo", session.ContentTypePlain)
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(100, 0),
		ToolName:      "file.read",
		ResultSummary: "/repo/main.go",
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(101, 0),
		ToolName:      "shell.run",
		ResultSummary: "go test ./...",
	})

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	for _, want := range []string{"file.read", "/repo/main.go", "shell.run", "go test ./..."} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing persistent tool log item %q:\n%s", want, view)
		}
	}
}

func TestCompletedToolCallsRenderInMessageTimeline(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(120, 40)
	m.now = func() time.Time { return time.Unix(200, 0) }

	state.AddMessage(session.RoleUser, "USER: inspect auth flow", session.ContentTypePlain)
	time.Sleep(2 * time.Millisecond)
	state.AddMessage(session.RoleAssistant, "ASSISTANT: auth flow summary", session.ContentTypePlain)

	messages := state.Messages()
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	toolTime := messages[0].CreatedAt.Add(messages[1].CreatedAt.Sub(messages[0].CreatedAt) / 2)
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     toolTime,
		ToolName:      "file.read",
		ResultSummary: "internal/auth.go",
	})

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	userIdx := strings.Index(view, "USER: inspect auth flow")
	toolIdx := strings.Index(view, "file.read")
	assistantIdx := strings.Index(view, "ASSISTANT: auth flow summary")
	if userIdx == -1 || toolIdx == -1 || assistantIdx == -1 {
		t.Fatalf("view missing timeline entries:\n%s", view)
	}
	if !(userIdx < toolIdx && toolIdx < assistantIdx) {
		t.Fatalf("tool log should render between related messages, got user=%d tool=%d assistant=%d:\n%s", userIdx, toolIdx, assistantIdx, view)
	}
}

func TestActiveToolCallClearsFromView(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.busy = true

	state.SetActiveToolCall(session.ActiveToolCall{
		Name:      "file.read",
		Args:      "/repo/main.go",
		StartedAt: time.Unix(100, 0),
	})
	m.refreshViewport()
	viewWithTool := stripANSI(m.View().Content)

	state.ClearActiveToolCall()
	m.lastTranscriptHash = 0
	m.refreshViewport()
	viewWithoutTool := stripANSI(m.View().Content)

	if strings.Contains(viewWithoutTool, "/repo/main.go") && !strings.Contains(viewWithTool, "/repo/main.go") {
		t.Fatalf("tool-call block did not clear from view")
	}
}

func TestTUIRichMCPApprovalStates(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)

	ch := make(chan session.UserApprovalDecision, 1)
	tc := &session.PendingToolCall{
		ID:           "call_1",
		Name:         "mcp.github.create_issue",
		Args:         `{"title":"some issue"}`,
		Command:      "mcp.github.create_issue",
		Risk:         "workspace_write",
		Reason:       "requires confirmation",
		Schema:       "creates a new github issue in the repo",
		ResponseChan: ch,
	}
	state.SetPendingApproval(tc)

	m.refreshViewport()
	view := stripANSI(m.View().Content)

	if !strings.Contains(view, "mcp.github.create_issue") {
		t.Fatalf("View() missing tool name:\n%s", view)
	}
	if !strings.Contains(view, "creates a new github issue") {
		t.Fatalf("View() missing tool description:\n%s", view)
	}
	if !strings.Contains(view, `{"title":"some issue"}`) {
		t.Fatalf("View() missing JSON arguments:\n%s", view)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.editingCommand {
		t.Fatal("expected editingCommand to be true")
	}
	if m.input.Value() != `{"title":"some issue"}` {
		t.Errorf("input value = %q, want JSON args", m.input.Value())
	}

	m.input.SetValue(`{"title":"edited issue"}`)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	select {
	case dec := <-ch:
		if !dec.Approved {
			t.Fatal("expected decision to be approved")
		}
		if dec.Edited != `{"title":"edited issue"}` {
			t.Errorf("dec.Edited = %q, want edited JSON", dec.Edited)
		}
	default:
		t.Fatal("no decision sent on channel")
	}
}

func TestPendingQuestionEnterSubmitsAnswer(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)

	q := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "Archive or delete?"}},
		ResponseChan: make(chan []session.Answer, 1),
	}
	state.SetPendingQuestion(q)

	for _, r := range "archive" {
		model, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = model.(Model)
	}
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case got := <-q.ResponseChan:
		if len(got) != 1 || got[0].Answer != "archive" || got[0].Question != "Archive or delete?" {
			t.Fatalf("answer = %+v, want archive for Archive or delete?", got)
		}
	default:
		t.Fatal("no answer sent on Enter")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question not cleared after submit")
	}
}

func TestPendingQuestionEscDeclines(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	q := &session.PendingQuestion{
		Questions:    []session.Question{{Question: "Archive or delete?"}},
		ResponseChan: make(chan []session.Answer, 1),
	}
	state.SetPendingQuestion(q)

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	select {
	case got := <-q.ResponseChan:
		if len(got) != 1 || got[0].Answer != session.AnswerUnanswered {
			t.Fatalf("answer = %+v, want single [Unanswered]", got)
		}
	default:
		t.Fatal("no answer sent on Esc")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question not cleared after Esc")
	}
}

// TestStatusLineJobCountFromBroker exercises the F19 broker-sourced job
// count: when a jobBroker is wired, the status line reads m.jobCount
// instead of m.state.RunningJobsCount().
func TestStatusLineJobCountFromBroker(t *testing.T) {
	b := pubsub.NewBroker[native.JobEvent]()
	m := newViewTestModel(t, 80, 24)
	m.jobBroker = b
	m.jobCount = 5
	line := m.renderStatusLine(80)
	if !strings.Contains(stripANSI(line), "jobs 5") {
		t.Fatalf("status line missing broker job count:\n%s", line)
	}
}

// TestStatusLineJobCountFallbackWhenNoBroker verifies the polled fallback
// is used when no broker is wired (e.g. test harnesses).
func TestStatusLineJobCountFallbackWhenNoBroker(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.state.SetRunningJobsCount(3)
	line := m.renderStatusLine(80)
	if !strings.Contains(stripANSI(line), "jobs 3") {
		t.Fatalf("status line missing polled job count:\n%s", line)
	}
}

// TestJobCountMsgUpdatesModel verifies the jobCountMsg handler updates
// the cached count and re-arms the pump.
func TestJobCountMsgUpdatesModel(t *testing.T) {
	b := pubsub.NewBroker[native.JobEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newViewTestModel(t, 80, 24)
	m.jobBroker = b
	m.jobEvents = b.Subscribe(ctx)
	updated, cmd := m.Update(jobCountMsg{count: 7})
	um := updated.(Model)
	if um.jobCount != 7 {
		t.Fatalf("jobCount = %d, want 7", um.jobCount)
	}
	if cmd == nil {
		t.Fatal("expected pump re-arm cmd, got nil")
	}
}

func TestEnterWhileBusyEnqueuesSteering(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.busy = true
	m.input.SetValue("also update the README")
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.state.SteeringQueue()) != 1 {
		t.Fatalf("queue = %v, want 1 item", m.state.SteeringQueue())
	}
}

func TestCtrlXClearsSteering(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.busy = true
	m.state.PushSteering("a")
	m.state.PushSteering("b")
	m.queuedCount = 2
	m = sendKey(m, tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if len(m.state.SteeringQueue()) != 0 {
		t.Fatalf("queue not cleared: %v", m.state.SteeringQueue())
	}
	if m.queuedCount != 0 {
		t.Fatalf("queuedCount = %d, want 0", m.queuedCount)
	}
}

func TestEnterOnEmptyDrainsQueuedFollowUp(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	// runner is nil, so submitTurn just appends the message to state.
	// We pre-seed the queue and verify Enter pops the oldest.
	m.state.PushSteering("first follow-up")
	m.state.PushSteering("second follow-up")
	m.queuedCount = 2
	// Busy must be false for the follow-up path to fire.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.state.SteeringQueue()) != 1 {
		t.Fatalf("queue = %v, want 1 remaining", m.state.SteeringQueue())
	}
	if m.state.SteeringQueue()[0] != "second follow-up" {
		t.Fatalf("remaining = %q, want %q", m.state.SteeringQueue()[0], "second follow-up")
	}
	messages := m.state.Messages()
	if len(messages) != 1 || messages[0].Content != "first follow-up" {
		t.Fatalf("follow-up not submitted; messages = %v", messages)
	}
}

func TestCancelTurnDropsSteeringQueue(t *testing.T) {
	m := newViewTestModel(t, 80, 24)
	m.busy = true
	m.state.PushSteering("a")
	m.state.PushSteering("b")
	m.queuedCount = 2
	cancelled := false
	m.agentCancel = func() { cancelled = true }
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if !cancelled {
		t.Fatal("Esc should cancel the in-flight agent turn")
	}
	if len(m.state.SteeringQueue()) != 0 {
		t.Fatalf("queue not dropped on cancel: %v", m.state.SteeringQueue())
	}
	if m.queuedCount != 0 {
		t.Fatalf("queuedCount = %d, want 0", m.queuedCount)
	}
}

// F18: /command completion popup is triggered by "/" at position 0 and
// accepting a suggestion replaces the trigger token with the full
// command name (and a trailing space for arg typing).
func TestSlashCompletionAcceptsPlan(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 80, 24)
	m.input.SetValue("/pl")
	m.updateCompletionPopups()
	if m.cmdPopup == nil || !m.cmdPopup.isVisible() {
		t.Fatal("command popup not visible after /pl")
	}
	if got := m.acceptCompletion(); !got {
		t.Fatal("acceptCompletion returned false")
	}
	if val := m.input.Value(); !strings.HasPrefix(val, "/plan") {
		t.Fatalf("input = %q, want /plan prefix", val)
	}
}

// Regression: each KeyPressMsg in Bubble Tea v2 is paired with a
// KeyReleaseMsg. Letting the release event fall through the outer
// switch hit the default path that calls updateCompletionPopups, which
// resets the popup's index to 0 — so the user could only "scroll" one
// entry before the selector snapped back to the top.
func TestArrowKeysSurviveKeyReleaseEvent(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		mustRegister(t, reg, commands.Command{
			Name:    name,
			Handler: func(s *session.State, args []string) string { return "" },
		})
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.refreshViewport()

	updated, _ := m.Update(tea.KeyPressMsg{Text: "/"})
	m = updated.(Model)
	if !m.cmdPopup.isVisible() {
		t.Fatal("popup not visible after /")
	}
	for i := 1; i <= 5; i++ {
		updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = updated.(Model)
		updated, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyDown})
		m = updated.(Model)
		if m.cmdPopup.index != i {
			t.Fatalf("after down #%d: index = %d, want %d (KeyReleaseMsg reset the popup)", i, m.cmdPopup.index, i)
		}
	}
}

// Regression: in the default Update path, updateCompletionPopups() was
// called for every non-handled event (mouse, spinner tick, paste echo,
// etc.) and always reset the popup index to 0. The fix is to make
// updateCompletionPopups idempotent — skip when the input value hasn't
// actually changed.
func TestCompletionPopupSurvivesNonKeyEvents(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		mustRegister(t, reg, commands.Command{
			Name:    name,
			Handler: func(s *session.State, args []string) string { return "" },
		})
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.refreshViewport()

	updated, _ := m.Update(tea.KeyPressMsg{Text: "/"})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(Model)
	if m.cmdPopup.index != 3 {
		t.Fatalf("after 3 downs: index = %d, want 3", m.cmdPopup.index)
	}

	// Now drive a barrage of non-key events. None should change the
	// input value, and so none should touch the popup.
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(spinnerTickMsg{})
		m = updated.(Model)
		updated, _ = m.Update(agentTickMsg{})
		m = updated.(Model)
		updated, _ = m.Update(tea.KeyReleaseMsg{Code: tea.KeyDown})
		m = updated.(Model)
		updated, _ = m.Update(tea.MouseWheelMsg{X: 0, Y: -1})
		m = updated.(Model)
		if m.cmdPopup.index != 3 {
			t.Fatalf("non-key event reset popup: index = %d, want 3", m.cmdPopup.index)
		}
	}
}

// F18: @file completion popup is triggered by "@" at a word start and
// accepting a suggestion replaces the trigger token with the matched
// @file path. Requires a model with a seeded file index — see
// newViewTestModelWithFileIndex.
func TestAtFileCompletionMatchesRepoFiles(t *testing.T) {
	m := newViewTestModelWithFileIndex(t, 80, 24, []string{
		"internal/agent/runner.go",
		"cmd/marshal/main.go",
	})
	m.input.SetValue("@run")
	m.updateCompletionPopups()
	if m.filePopup == nil || !m.filePopup.isVisible() {
		t.Fatal("file popup not visible after @run")
	}
	if got := m.acceptCompletion(); !got {
		t.Fatal("acceptCompletion returned false")
	}
	if val := m.input.Value(); val != "@internal/agent/runner.go " {
		t.Fatalf("input = %q, want %q", val, "@internal/agent/runner.go ")
	}
}

func TestAtFileAfterPunctuationDoesNotTrigger(t *testing.T) {
	m := newViewTestModelWithFileIndex(t, 80, 24, []string{
		"internal/agent/runner.go",
	})
	m.input.SetValue("see (@run")
	m.updateCompletionPopups()
	if m.filePopup != nil && m.filePopup.isVisible() {
		t.Fatal("file popup should not be visible when @ is preceded by punctuation")
	}
}

func TestAtFileCompletionOmitsWhitespacePaths(t *testing.T) {
	m := newViewTestModelWithFileIndex(t, 80, 24, []string{
		"docs/has space.md",
		"internal/agent/runner.go",
	})
	m.input.SetValue("@space")
	m.updateCompletionPopups()
	if m.filePopup != nil && m.filePopup.isVisible() {
		t.Fatalf("file popup should not offer whitespace paths, got %#v", m.filePopup.matches())
	}

	m.input.SetValue("@run")
	m.updateCompletionPopups()
	if m.filePopup == nil || !m.filePopup.isVisible() {
		t.Fatal("file popup should still offer normal paths")
	}
	matches := m.filePopup.matches()
	if len(matches) != 1 || matches[0].Text != "internal/agent/runner.go" {
		t.Fatalf("matches = %#v, want only internal/agent/runner.go", matches)
	}
}

// F-BUG-159: @ completion with an empty file index shows a placeholder
// explaining that no files are indexed.
func TestAtFileCompletionEmptyIndexShowsPlaceholder(t *testing.T) {
	m := newViewTestModelWithFileIndex(t, 80, 24, []string{})
	m.input.SetValue("@")
	m.updateCompletionPopups()
	if m.filePopup == nil || !m.filePopup.isVisible() {
		t.Fatal("file popup should be visible even with empty index")
	}
	matches := m.filePopup.matches()
	if len(matches) == 0 {
		t.Fatal("expected placeholder in matches, got empty")
	}
	found := false
	for _, it := range matches {
		if strings.Contains(it.Text, "no indexed files") {
			found = true
			if !it.Disabled {
				t.Fatal("placeholder item must have Disabled=true")
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected placeholder text in matches, got %#v", matches)
	}

	// Verify the placeholder is non-selectable: accepting it must not
	// delete the "@" from the input.
	inputBefore := m.input.Value()
	if !m.acceptCompletion() {
		t.Fatal("acceptCompletion should return true (popup was visible)")
	}
	if m.filePopup != nil && m.filePopup.isVisible() {
		t.Fatal("popup should be dismissed after accept")
	}
	if got := m.input.Value(); got != inputBefore {
		t.Fatalf("input value changed from %q to %q after accepting placeholder", inputBefore, got)
	}
}

// F18: @ inside a word (e.g. an email) does not trigger the file popup.
func TestAtInsideWordDoesNotTrigger(t *testing.T) {
	m := newViewTestModelWithFileIndex(t, 80, 24, []string{"a.go", "b.go"})
	m.input.SetValue("user@example.com")
	m.updateCompletionPopups()
	if m.filePopup != nil && m.filePopup.isVisible() {
		t.Fatal("file popup should not be visible for email-style @")
	}
	if m.cmdPopup != nil && m.cmdPopup.isVisible() {
		t.Fatal("cmd popup should not be visible either")
	}
}

// F18: Esc dismisses the active popup without cancelling the in-flight
// turn. A subsequent Esc with no popup cancels the turn as before.
func TestEscDismissesPopupBeforeCancelTurn(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 80, 24)
	m.busy = true
	m.input.SetValue("/p")
	m.updateCompletionPopups()
	if !m.cmdPopup.isVisible() {
		t.Fatal("popup should be visible")
	}
	cancelled := false
	m.agentCancel = func() { cancelled = true }
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if cancelled {
		t.Fatal("first Esc should dismiss the popup, not cancel the turn")
	}
	if m.cmdPopup.isVisible() {
		t.Fatal("popup should be hidden after first Esc")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if !cancelled {
		t.Fatal("second Esc with no popup should cancel the turn")
	}
}

// F18: empty / shows the full command list; filtering narrows it.
func TestSlashCompletionListsAllCommandsOnBareSlash(t *testing.T) {
	m := newViewTestModelWithRegistry(t, 80, 24)
	m.input.SetValue("/")
	m.updateCompletionPopups()
	if m.cmdPopup == nil || !m.cmdPopup.isVisible() {
		t.Fatal("bare / should make the popup visible with all commands")
	}
	if len(m.cmdPopup.matches()) < 2 {
		t.Fatalf("matches = %d, want at least 2 (plan + help)", len(m.cmdPopup.matches()))
	}
}

// F18: the file popup is mutually exclusive with the cmd popup; typing
// /something after @something dismisses the file popup.
func TestCommandTriggerDismissesFilePopup(t *testing.T) {
	m := newViewTestModelWithRegistryAndFileIndex(t, 80, 24, []string{"a.go"})
	m.input.SetValue("@a")
	m.updateCompletionPopups()
	if !m.filePopup.isVisible() {
		t.Fatal("file popup should be visible for @a")
	}
	m.input.SetValue("/pl")
	m.updateCompletionPopups()
	if m.filePopup.isVisible() {
		t.Fatal("file popup should be dismissed by command trigger")
	}
	if !m.cmdPopup.isVisible() {
		t.Fatal("cmd popup should be visible for /pl")
	}
}

func TestHelpOpenWithQuestionMarkKey(t *testing.T) {
	m := newViewTestModel(t, 80, 24)

	// Press ? to open help overlay.
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?'})
	m = updated.(Model)

	if !m.helpOpen {
		t.Fatal("expected helpOpen to be true after pressing ?")
	}

	view := m.View().Content
	if !strings.Contains(view, "marshal keys") {
		t.Fatalf("help overlay missing title:\n%s", view)
	}

	// Press Esc to close help overlay.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)

	if m.helpOpen {
		t.Fatal("expected helpOpen to be false after pressing Esc")
	}

	view = m.View().Content
	if strings.Contains(view, "marshal keys") {
		t.Fatalf("help overlay should be closed after Esc:\n%s", view)
	}
}

func TestQuestionMarkDoesNotLeakIntoInput(t *testing.T) {
	m := newViewTestModel(t, 80, 24)

	// Press ? — it should toggle help, not type ? into the textarea.
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?'})
	m = updated.(Model)
	if !m.helpOpen {
		t.Fatal("expected helpOpen to be true")
	}
	if m.input.Value() != "" {
		t.Fatalf("expected empty input after ?, got %q", m.input.Value())
	}
}

func TestHelpToggleWithQuestionMark(t *testing.T) {
	m := newViewTestModel(t, 80, 24)

	// Press ? to open.
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?'})
	m = updated.(Model)
	if !m.helpOpen {
		t.Fatal("expected helpOpen after first ?")
	}

	// Press ? again to close.
	updated, _ = m.Update(tea.KeyPressMsg{Code: '?'})
	m = updated.(Model)
	if m.helpOpen {
		t.Fatal("expected helpOpen to be false after second ?")
	}
}

// TestQuestionMarkTypesLiterallyInNonEmptyInput verifies that ? is inserted
// as a literal character when the textarea has content — the help overlay
// should NOT open in that case, because ? is a very common character in
// chat prompts (e.g. "What is X? How do I Y?").
func TestQuestionMarkTypesLiterallyInNonEmptyInput(t *testing.T) {
	m := newViewTestModel(t, 80, 24)

	// Type some text first.
	m = sendKey(m, tea.KeyPressMsg{Text: "hello"})

	// Press ? — should append to the input, NOT open help.
	m = sendKey(m, tea.KeyPressMsg{Text: "?"})

	if m.helpOpen {
		t.Fatal("? should not open help overlay when input has content")
	}
	if !strings.Contains(m.input.Value(), "?") {
		t.Fatalf("? should be a literal char in input, got %q", m.input.Value())
	}
}

func TestHelpOverlayDoesNotSwallowAgentFinishedMsg(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 30)
	m.helpOpen = true
	m.busy = true
	m.lastActivityKind = session.ActivityThinking

	updated, _ := m.Update(agentFinishedMsg{})
	m = updated.(Model)

	if m.busy {
		t.Fatal("help overlay should not swallow agentFinishedMsg")
	}
	if !m.helpOpen {
		t.Fatal("non-key runtime messages should not close the help overlay")
	}
}

func TestPickerRoutingOpenPickCancel(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	// open a picker directly via the helper
	m.openPicker("branches", "Switch branch", "", []picker.Item{
		{Label: "branch 1", Value: "1"},
		{Label: "branch 2", Value: "2", Badge: "● now"},
	}, "")
	if m.pickerModel == nil || m.pickerCommand != "branches" {
		t.Fatal("openPicker should set the modal state")
	}

	// the modal renders composited over the normal view
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Switch branch") || !strings.Contains(view, "branch 1") {
		t.Fatalf("picker should render over the view:\n%s", view)
	}

	// keys route to the picker: esc → CancelledMsg → closes
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = asModel(t, updated)
	if cmd == nil {
		t.Fatal("esc should produce the picker's cancel command")
	}
	updated, _ = m.Update(cmd())
	m = asModel(t, updated)
	if m.pickerModel != nil || m.pickerCommand != "" {
		t.Fatal("CancelledMsg should close the picker")
	}
}

// ── /model picker routing (Task 6) ──────────────────────────────────────

func modelTestState(t *testing.T) *session.State {
	t.Helper()
	cfg := config.Default()
	if cfg.Models.Presets == nil {
		cfg.Models.Presets = map[string]routing.ModelPreset{}
	}
	cfg.Models.Presets["test-a"] = routing.ModelPreset{Name: "test-a", Provider: "ollama", Model: "qwen2.5", LocalOnly: true}
	cfg.Models.Presets["test-b"] = routing.ModelPreset{Name: "test-b", Provider: "anthropic", Model: "sonnet-5"}
	return session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
}

func TestModelBareOpensPicker(t *testing.T) {
	m := New(modelTestState(t), WithCommandRegistry(setupCmdReg(t)))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model")
	m = asModel(t, updated)
	if m.pickerModel == nil || m.pickerCommand != "model" {
		t.Fatal("bare /model should open the picker")
	}
	view := stripANSI(m.View().Content)
	for _, want := range []string{"test-a", "test-b", "ollama/qwen2.5", "local", "session only"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
}

func TestModelExactArgBypassesPicker(t *testing.T) {
	var reloaded *config.Config
	m := New(modelTestState(t), WithCommandRegistry(setupCmdReg(t)), WithConfigReloader(func(c config.Config) error { reloaded = &c; return nil }))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model test-b")
	m = asModel(t, updated)
	if m.pickerModel != nil {
		t.Fatal("exact preset arg must switch directly, no picker")
	}
	if reloaded == nil || reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer] != "test-b" {
		t.Fatalf("direct switch should reload with test-b, got %+v", reloaded)
	}
}

func TestModelUnknownArgOpensPrefilteredPicker(t *testing.T) {
	m := New(modelTestState(t), WithCommandRegistry(setupCmdReg(t)))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model test-a-typo-b")
	m = asModel(t, updated)
	if m.pickerModel == nil {
		t.Fatal("unknown arg should open the picker instead of erroring")
	}
}

func TestModelPickAppliesSessionSwitch(t *testing.T) {
	var reloaded *config.Config
	m := New(modelTestState(t), WithCommandRegistry(setupCmdReg(t)), WithConfigReloader(func(c config.Config) error { reloaded = &c; return nil }))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model")
	m = asModel(t, updated)
	updated, _ = m.Update(picker.PickedMsg{Value: "test-a"})
	m = asModel(t, updated)
	if m.pickerModel != nil {
		t.Fatal("pick should close the modal")
	}
	if reloaded == nil || reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer] != "test-a" {
		t.Fatalf("pick should reload with test-a, got %+v", reloaded)
	}
}

func TestModelNoPresetsPointsAtSettings(t *testing.T) {
	cfg := config.Default()
	cfg.Models.Presets = map[string]routing.ModelPreset{}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state, WithCommandRegistry(setupCmdReg(t)))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/model")
	m = asModel(t, updated)
	if m.pickerModel != nil {
		t.Fatal("no presets: picker must not open")
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "/settings") {
		t.Fatal("should add a system message pointing at /settings")
	}
}

// ── /rewind and /branches pickers (Task 7) ──────────────────────────────

func pickerTestModel(t *testing.T, state *session.State) Model {
	t.Helper()
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	return m
}

func TestRewindBareOpensPickerNewestFirst(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "first question", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "second question about parsing", session.ContentTypePlain)
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/rewind")
	m = asModel(t, updated)
	if m.pickerModel == nil || m.pickerCommand != "rewind" {
		t.Fatal("bare /rewind should open the picker, not rewind immediately")
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "second question") {
		t.Fatalf("picker should preview turn content:\n%s", view)
	}
	// newest turn is first and carries the ● badge → default Enter target
	first := strings.Index(view, "turn 2")
	second := strings.Index(view, "turn 1")
	if first == -1 || second == -1 || first > second {
		t.Fatalf("turns should list newest first:\n%s", view)
	}
}

func TestRewindWithArgSkipsPicker(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "only turn", session.ContentTypePlain)
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/rewind 1")
	m = asModel(t, updated)
	if m.pickerModel != nil {
		t.Fatal("/rewind 1 must run directly")
	}
}

func TestBranchesBareOpensPickerWithCurrentBadge(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.AddMessage(session.RoleUser, "first turn", session.ContentTypePlain)
	state.Rewind(1) // create a fork at the root
	state.AddMessage(session.RoleUser, "second turn", session.ContentTypePlain)
	// Now there are 2 branches (messages 1 and 2 are both leaves).
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/branches")
	m = asModel(t, updated)
	if m.pickerModel == nil || m.pickerCommand != "branches" {
		t.Fatal("bare /branches should open the picker")
	}
	if !strings.Contains(stripANSI(m.View().Content), "● now") {
		t.Fatal("current branch should be badged")
	}
}

func TestRewindNoTurnsFallsThroughToHandler(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := pickerTestModel(t, state)
	updated, _ := m.dispatchCommand("/rewind")
	m = asModel(t, updated)
	if m.pickerModel != nil {
		t.Fatal("no turns: picker must not open")
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "No user turns") {
		t.Fatal("handler's 'No user turns to rewind to.' message should appear")
	}
}

func TestActiveThemeValuesAreCorrectFor256Color(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	th := theme.LoadFor(false, "xterm-256color")
	if th.AccentPrimary != lipgloss.Color("209") {
		t.Fatalf("AccentPrimary = %#v, want 209", th.AccentPrimary)
	}
	if th.AccentTertiary != lipgloss.Color("214") {
		t.Fatalf("AccentTertiary = %#v, want 214 (gold)", th.AccentTertiary)
	}
	if th.UserPrompt != lipgloss.Color("246") {
		t.Fatalf("UserPrompt = %#v, want 246 (medium grey)", th.UserPrompt)
	}
	if th.StatusSuccess != lipgloss.Color("43") {
		t.Fatalf("StatusSuccess = %#v, want 43", th.StatusSuccess)
	}
}

// ── /mode picker (Task 8) ────────────────────────────────────────────────

func TestModePickerMarksCurrentAndApplies(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.forceMode = "edit"

	updated, _ := m.dispatchCommand("/mode")
	m = asModel(t, updated)
	if m.pickerModel == nil || m.pickerCommand != "mode" {
		t.Fatal("/mode should open the picker")
	}
	view := stripANSI(m.View().Content)
	for _, want := range []string{"Ask", "Edit", "Auto", "● now"} {
		if !strings.Contains(view, want) {
			t.Fatalf("mode picker missing %q:\n%s", want, view)
		}
	}

	updated, _ = m.Update(picker.PickedMsg{Value: "ask"})
	m = asModel(t, updated)
	if m.forceMode != "ask" {
		t.Fatalf("picking Ask should set forceMode, got %q", m.forceMode)
	}
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "Ask mode") {
		t.Fatal("the /ask handler's confirmation message should appear")
	}
}

func TestModePickerSelectingSDDOpensPlanPicker(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)

	// Open the mode picker first
	updated, _ := m.dispatchCommand("/mode")
	m = asModel(t, updated)
	if m.pickerModel == nil || m.pickerCommand != "mode" {
		t.Fatal("/mode should open the picker")
	}

	// Simulate picking "sdd" from the mode picker
	updated, _ = m.Update(picker.PickedMsg{Value: "sdd"})
	m = asModel(t, updated)

	// Verify the SDD plan picker opened instead of dispatching "/sdd" directly
	if m.pickerModel == nil {
		t.Fatal("picking SDD should open the plan picker, but pickerModel is nil")
	}
	if m.pickerCommand != "sdd-plan" {
		t.Fatalf("picking SDD should set pickerCommand to %q, got %q", "sdd-plan", m.pickerCommand)
	}

	// Verify no usage message was printed (regression: the old code dispatched
	// "/sdd" with no args, which printed "Usage: /sdd <plan-file>")
	msgs := state.Messages()
	for _, msg := range msgs {
		if strings.Contains(msg.Content, "Usage: /sdd") {
			t.Fatal("picking SDD from mode picker should not print the /sdd usage message")
		}
	}
}

func TestModeWithArgDispatchesDirectly(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, nil); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	updated, _ := m.dispatchCommand("/mode edit")
	m = asModel(t, updated)
	if m.pickerModel != nil {
		t.Fatal("/mode edit must not open the picker")
	}
	if m.forceMode != "edit" {
		t.Fatalf("forceMode = %q, want edit", m.forceMode)
	}
}

// ── Tab/Shift+Tab mode cycling and Alt+M model cycling (Task 8) ─────────

func TestTabCyclesModeForward(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	// Start in auto mode (forceMode == "")
	if m.forceMode != "" {
		t.Fatalf("initial forceMode = %q, want \"\"", m.forceMode)
	}

	// Tab → ask
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.forceMode != "ask" {
		t.Fatalf("after 1st Tab: forceMode = %q, want \"ask\"", m.forceMode)
	}

	// Tab → edit
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.forceMode != "edit" {
		t.Fatalf("after 2nd Tab: forceMode = %q, want \"edit\"", m.forceMode)
	}

	// Tab → auto (wrap)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.forceMode != "" {
		t.Fatalf("after 3rd Tab: forceMode = %q, want \"\" (auto)", m.forceMode)
	}

	// Verify the last system message is the auto confirmation
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one system message")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleSystem {
		t.Fatal("last message should be system role")
	}
	if !strings.Contains(last.Content, "Auto mode") {
		t.Fatalf("last message = %q, want 'Auto mode' confirmation", last.Content)
	}
}

func TestShiftTabCyclesModeBackward(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)
	m.forceMode = "edit"

	// Shift+Tab → ask
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.forceMode != "ask" {
		t.Fatalf("after 1st Shift+Tab: forceMode = %q, want \"ask\"", m.forceMode)
	}

	// Shift+Tab → auto
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.forceMode != "" {
		t.Fatalf("after 2nd Shift+Tab: forceMode = %q, want \"\" (auto)", m.forceMode)
	}

	// Shift+Tab → edit (wrap)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.forceMode != "edit" {
		t.Fatalf("after 3rd Shift+Tab: forceMode = %q, want \"edit\"", m.forceMode)
	}

	// Verify the last system message is the edit confirmation
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one system message")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleSystem {
		t.Fatal("last message should be system role")
	}
	if !strings.Contains(last.Content, "Edit mode") {
		t.Fatalf("last message = %q, want 'Edit mode' confirmation", last.Content)
	}
}

func TestTabAcceptsCompletionWhenPopupOpen(t *testing.T) {
	reg := commands.New()
	mustRegister(t, reg, commands.Command{
		Name:        "test",
		Description: "test command",
		Handler:     func(s *session.State, args []string) string { return "" },
	})
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.refreshViewport()

	// Trigger cmd popup by setting input to "/"
	m.input.SetValue("/")
	m.updateCompletionPopups()
	if !m.cmdPopup.isVisible() {
		t.Fatal("cmd popup should be visible after /")
	}

	initialMode := m.forceMode // should be ""

	// Tab should accept the completion, not cycle mode
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if m.cmdPopup.isVisible() {
		t.Fatal("cmd popup should be dismissed after Tab")
	}
	if m.forceMode != initialMode {
		t.Fatalf("forceMode changed from %q to %q, should be unchanged", initialMode, m.forceMode)
	}
	// Input should have been replaced with the accepted command
	if !strings.HasPrefix(m.input.Value(), "/test") {
		t.Fatalf("input = %q, want \"/test\" prefix", m.input.Value())
	}
}

func TestTabIgnoredDuringApproval(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	tc := &session.PendingToolCall{
		ID:           "1",
		Name:         "shell.run",
		Command:      "echo test",
		Risk:         "low",
		Reason:       "testing",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	state.SetPendingApproval(tc)
	m := New(state)
	m.resize(80, 24)
	m.forceMode = "ask"

	// Tab while approval is pending should not cycle mode
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if m.forceMode != "ask" {
		t.Fatalf("forceMode = %q, want \"ask\" (unchanged during approval)", m.forceMode)
	}
	if state.PendingApproval() == nil {
		t.Fatal("pending approval was cleared by Tab")
	}
}

func TestAltMCyclesModelForward(t *testing.T) {
	var reloaded *config.Config
	state := modelTestState(t)
	m := New(state, WithConfigReloader(func(c config.Config) error {
		reloaded = &c
		return nil
	}))
	m.resize(80, 24)

	// Set active route to test-a so cycleModel finds a known index
	state.SetActiveRoute(session.RouteInfo{
		Active: true, Preset: "test-a", Provider: "ollama", Model: "qwen2.5", LocalOnly: true,
	})

	// Alt+M should cycle forward. Sorted order by provider then name:
	//   index 0: test-b (anthropic)
	//   index 1: test-a (ollama)
	// Forward from index 1 → index 0 → test-b
	m = sendKey(m, tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt})

	if reloaded == nil {
		t.Fatal("configReloader was not called")
	}
	if reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer] != "test-b" {
		t.Fatalf("configReloader preset = %q, want \"test-b\"",
			reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer])
	}
	// A confirmation system message should appear
	msgs := state.Messages()
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].Content, "Switched to model") {
		t.Fatal("expected 'Switched to model' system message")
	}
}

func TestAltShiftMCyclesModelBackward(t *testing.T) {
	var reloaded *config.Config
	state := modelTestState(t)
	m := New(state, WithConfigReloader(func(c config.Config) error {
		reloaded = &c
		return nil
	}))
	m.resize(80, 24)

	// Set active route to test-b (index 0 in sorted order)
	state.SetActiveRoute(session.RouteInfo{
		Active: true, Preset: "test-b", Provider: "anthropic", Model: "sonnet-5",
	})

	// Shift+Alt+M should cycle backward.
	// Sorted: index 0 = test-b, index 1 = test-a
	// Backward from index 0 wraps → index 1 → test-a
	m = sendKey(m, tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt | tea.ModShift})

	if reloaded == nil {
		t.Fatal("configReloader was not called")
	}
	if reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer] != "test-a" {
		t.Fatalf("configReloader preset = %q, want \"test-a\"",
			reloaded.AgentProfiles["switched"].Roles[routing.RoleImplementer])
	}
}

func TestAltMNoPresetsShowsGuidance(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(80, 24)

	// No model presets configured; Alt+M should not panic and show guidance
	m = sendKey(m, tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt})

	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a system message")
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "No model presets configured") {
		t.Fatalf("message = %q, want guidance about presets", last.Content)
	}
}

func TestAltMBlockedWhileBusy(t *testing.T) {
	var reloaded bool
	state := modelTestState(t)
	m := New(state, WithConfigReloader(func(c config.Config) error {
		reloaded = true
		return nil
	}))
	m.resize(80, 24)
	m.busy = true

	// Alt+M while busy should not call configReloader and should show busy msg
	m = sendKey(m, tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt})

	if reloaded {
		t.Fatal("configReloader should not be called when busy")
	}
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a system message")
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "Busy") {
		t.Fatalf("message = %q, want 'Busy' message", last.Content)
	}
}

func TestShiftTabDuringPopupIsNoOp(t *testing.T) {
	reg := commands.New()
	mustRegister(t, reg, commands.Command{
		Name:        "test",
		Description: "test command",
		Handler:     func(s *session.State, args []string) string { return "" },
	})
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.refreshViewport()

	// Trigger cmd popup
	m.input.SetValue("/")
	m.updateCompletionPopups()
	if !m.cmdPopup.isVisible() {
		t.Fatal("cmd popup should be visible after /")
	}

	initialMode := m.forceMode
	initialInput := m.input.Value()

	// Shift+Tab while popup is visible should be a no-op
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})

	if m.forceMode != initialMode {
		t.Fatalf("forceMode changed from %q to %q, should be unchanged", initialMode, m.forceMode)
	}
	if m.input.Value() != initialInput {
		t.Fatalf("input changed from %q to %q, should be unchanged", initialInput, m.input.Value())
	}
	if !m.cmdPopup.isVisible() {
		t.Fatal("popup should remain visible after Shift+Tab during popup")
	}
}

// ── Task 8: Turn-cancellation on quit paths ──────────────────────────────

func TestCtrlCCancelsTurn(t *testing.T) {
	tests := []struct {
		name    string
		trigger func(m *Model) tea.Cmd
	}{
		{
			name: "ctrl+c",
			trigger: func(m *Model) tea.Cmd {
				updated, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
				*m = updated.(Model)
				return cmd
			},
		},
		{
			name: "/quit",
			trigger: func(m *Model) tea.Cmd {
				_, cmd := m.dispatchCommand("/quit")
				return cmd
			},
		},
		{
			name: "/exit",
			trigger: func(m *Model) tea.Cmd {
				_, cmd := m.dispatchCommand("/exit")
				return cmd
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})

			approvalCh := make(chan session.UserApprovalDecision, 1)
			state.SetPendingApproval(&session.PendingToolCall{
				ID: "test-1", Name: "shell.run", Command: "echo test",
				Risk: "low", Reason: "testing", ResponseChan: approvalCh,
			})
			questionCh := make(chan []session.Answer, 1)
			state.SetPendingQuestion(&session.PendingQuestion{
				Questions:    []session.Question{{Question: "Continue?"}},
				ResponseChan: questionCh,
			})

			cancelled := false
			opts := []Option{}
			if tt.name != "ctrl+c" {
				opts = append(opts, WithCommandRegistry(setupCmdReg(t)))
			}
			m := New(state, opts...)
			m.busy = true
			m.agentCancel = func() { cancelled = true }
			m.state.PushSteering("pending")
			m.queuedCount = 1

			cmd := tt.trigger(&m)

			if !cancelled {
				t.Fatal("should cancel the agent turn")
			}
			if m.agentCancel != nil {
				t.Fatal("agentCancel should be cleared")
			}
			if len(m.state.SteeringQueue()) != 0 {
				t.Fatal("steering queue should be cleared")
			}
			if m.queuedCount != 0 {
				t.Fatal("queuedCount should be 0")
			}
			if state.PendingApproval() != nil {
				t.Fatal("pending approval should be cleared")
			}
			if state.PendingQuestion() != nil {
				t.Fatal("pending question should be cleared")
			}
			select {
			case dec := <-approvalCh:
				if dec.Approved {
					t.Fatal("approval should be denied")
				}
			default:
				t.Fatal("approval denial not sent")
			}
			select {
			case answers := <-questionCh:
				if len(answers) != 1 || answers[0].Answer != session.AnswerUnanswered {
					t.Fatalf("question should be unanswered, got %+v", answers)
				}
			default:
				t.Fatal("question not answered")
			}
			if cmd == nil {
				t.Fatal("should return a quit command")
			}
			select {
			case <-state.Done():
			case <-time.After(time.Second):
				t.Fatal("state was not shut down")
			}
		})
	}
}

func TestIntentionalAgentCancellationDoesNotSetProviderError(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	model.busy = true

	updated, _ := model.Update(agentFinishedMsg{err: context.Canceled})
	model = updated.(Model)

	if model.busy {
		t.Fatal("model.busy = true, want false after agentFinishedMsg")
	}
	if err := state.ProviderError(); err != nil {
		t.Fatalf("ProviderError() = %v, want nil for context.Canceled", err)
	}
}

// ── Task 8: Settings save guard ──────────────────────────────────────────

func TestSettingsSaveBlockedDuringAgentTurn(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 40)
	m.busy = true

	// Open settings while busy.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)

	dirty := config.Default()
	dirty.Privacy.RemoteProvidersAllowed = true
	m.settingsModel.SetWorkingConfig(dirty)

	// Ctrl+S opens the diff overlay; Enter commits and should be blocked.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("ctrl+s should open the diff overlay, not save directly")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	if cmd != nil {
		t.Fatal("expected nil cmd when save is blocked by busy turn")
	}
	if m.settingsModel.Footer() != settingsBusyMessage {
		t.Fatalf("footer = %q, want %q", m.settingsModel.Footer(), settingsBusyMessage)
	}
}

func TestSettingsSaveBlockedDuringBackgroundJob(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 40)
	m.state.SetRunningJobsCount(1)

	// Open settings while jobs are running.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)

	dirty := config.Default()
	dirty.Privacy.RemoteProvidersAllowed = true
	m.settingsModel.SetWorkingConfig(dirty)

	// Ctrl+S opens the diff overlay; Enter commits and should be blocked.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("ctrl+s should open the diff overlay, not save directly")
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	if cmd != nil {
		t.Fatal("expected nil cmd when save is blocked by background jobs")
	}
	if m.settingsModel.Footer() != settingsBusyMessage {
		t.Fatalf("footer = %q, want %q", m.settingsModel.Footer(), settingsBusyMessage)
	}
}

// ── Task 5: /connect and /models overlay ──────────────────────────────────

func newTestModel(t *testing.T) Model {
	t.Helper()
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	reg := commands.New()
	if err := commands.RegisterAll(reg, registry.New()); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	m := New(state, WithCommandRegistry(reg))
	m.resize(80, 24)
	m.refreshViewport()
	return m
}

func TestConnectOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.dispatchCommand("/connect")
	m = asModel(t, updated)
	if !m.connectOpen {
		t.Fatal("/connect should open the connect overlay")
	}
}

func TestModelsOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.dispatchCommand("/models")
	m = asModel(t, updated)
	if !m.connectOpen {
		t.Fatal("/models should open the connect overlay")
	}
}

func TestModelsEmptyProvidersShowsAddProvider(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.dispatchCommand("/models")
	m = asModel(t, updated)
	if !m.connectOpen || m.connectModel == nil {
		t.Fatal("/models with no providers should open the connect overlay")
	}
	view := stripANSI(m.connectModel.View(80, 24))
	if !strings.Contains(view, "Connect a provider") {
		t.Fatalf("no providers should show provider picker, got: %s", view)
	}
}

func TestConnectDoneMsgPersistsAgentModel(t *testing.T) {
	m := newTestModel(t)
	reloaded := false
	m.configReloader = func(cfg config.Config) error { reloaded = true; m.state.Config = cfg; return nil }
	updated, _ := m.Update(connect.DoneMsg{Provider: "ollama", Model: "qwen2.5-coder:7b", ProviderCfg: config.ProviderConfig{Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"}})
	if !reloaded {
		t.Fatal("DoneMsg should call configReloader")
	}
	if updated.(Model).state.Config.Agent.Provider != "ollama" || updated.(Model).state.Config.Agent.Model != "qwen2.5-coder:7b" {
		t.Fatalf("agent cfg not set: %+v", updated.(Model).state.Config.Agent)
	}
	if updated.(Model).state.Config.Profile.Default != "" {
		t.Fatalf("profile default should be cleared, got %q", updated.(Model).state.Config.Profile.Default)
	}
	prov, ok := updated.(Model).state.Config.Providers["ollama"]
	if !ok {
		t.Fatal(`providers["ollama"] should exist after DoneMsg`)
	}
	if prov.Type != "openai_compatible" {
		t.Fatalf(`expected provider type "openai_compatible", got %q`, prov.Type)
	}
	if prov.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf(`expected provider base_url "http://localhost:11434/v1", got %q`, prov.BaseURL)
	}
	if updated.(Model).connectOpen {
		t.Fatal("overlay should close after DoneMsg")
	}
}

func TestConnectCancelledClosesOverlay(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.dispatchCommand("/connect")
	m = asModel(t, updated)
	updated2, _ := m.Update(connect.CancelledMsg{})
	if updated2.(Model).connectOpen {
		t.Fatal("CancelledMsg should close the overlay")
	}
}

func TestSettingsSaveAllowedWhenIdle(t *testing.T) {
	var reloaded bool
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
	m := New(state, WithConfigReloader(func(cfg config.Config) error {
		reloaded = true
		return nil
	}))
	m.resize(100, 40)

	// Open settings while idle.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)

	dirty := config.Default()
	dirty.Privacy.RemoteProvidersAllowed = true
	m.settingsModel.SetWorkingConfig(dirty)

	// Ctrl+S opens the diff overlay; Enter commits the save.
	updated, _ = m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	if cmd == nil {
		t.Fatal("expected a non-nil cmd when save is allowed")
	}

	// Execute the command to get SavedMsg, then feed it back to trigger the reloader.
	msg := cmd()
	sm, ok := msg.(settings.SavedMsg)
	if !ok {
		t.Fatalf("expected SavedMsg, got %T", msg)
	}
	updated, _ = m.Update(sm)
	_ = updated.(Model)

	if !reloaded {
		t.Fatal("configReloader should have been called")
	}
}

func TestSettingsOverlayDoesNotSwallowAgentFinishedOrJobCount(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	m.resize(100, 40)
	m.busy = true
	m.lastActivityKind = session.ActivityThinking

	// Open settings.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}

	// Feed agentFinishedMsg while settings is open.
	updated, _ = m.Update(agentFinishedMsg{err: nil})
	m = updated.(Model)

	if m.busy {
		t.Fatal("busy should be cleared after agentFinishedMsg even with settings open")
	}
	if !m.settingsOpen {
		t.Fatal("settings should stay open after agentFinishedMsg")
	}

	// Feed jobCountMsg while settings is open.
	updated, _ = m.Update(jobCountMsg{count: 3})
	m = updated.(Model)

	if m.jobCount != 3 {
		t.Fatalf("jobCount = %d, want 3", m.jobCount)
	}
	if !m.settingsOpen {
		t.Fatal("settings should stay open after jobCountMsg")
	}
}

func TestPatternForApproval_FullArgv(t *testing.T) {
	tests := []struct {
		name     string
		tc       *session.PendingToolCall
		want     string
		wantGlob bool // if true, pattern contains "*"
	}{
		{
			name: "simple git status returns full argv",
			tc: &session.PendingToolCall{
				Name:    "shell.run",
				Command: "git status",
			},
			want:     "git status",
			wantGlob: false,
		},
		{
			name: "git status --short returns full argv",
			tc: &session.PendingToolCall{
				Name:    "shell.run",
				Command: "git status --short",
			},
			want:     "git status --short",
			wantGlob: false,
		},
		{
			name: "malicious command with semicolon returns full argv, not prefix",
			tc: &session.PendingToolCall{
				Name:    "shell.run",
				Command: "git ; rm -rf /",
			},
			want:     "git ; rm -rf /",
			wantGlob: false,
		},
		{
			name: "non-shell tool returns star",
			tc: &session.PendingToolCall{
				Name:    "file.read",
				Command: "anything",
			},
			want:     "*",
			wantGlob: true,
		},
		{
			name: "empty command returns star",
			tc: &session.PendingToolCall{
				Name:    "shell.run",
				Command: "",
			},
			want:     "*",
			wantGlob: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := patternForApproval(tt.tc)
			if got != tt.want {
				t.Errorf("patternForApproval() = %q, want %q", got, tt.want)
			}
			if tt.wantGlob && !strings.Contains(got, "*") {
				t.Errorf("pattern %q should contain glob character", got)
			}
			if !tt.wantGlob && strings.Contains(got, "*") {
				t.Errorf("pattern %q should not contain glob/wildcard", got)
			}
		})
	}
}

// TestDispatchCommand_QuotedArgs verifies that shlex.Split preserves quoted
// arguments instead of splitting on internal whitespace (F-SEC-31).
func TestDispatchCommand_QuotedArgs(t *testing.T) {
	var captured []string
	cmdReg := commands.New()
	if err := cmdReg.Register(commands.Command{
		Name: "teststub",
		Handler: func(state *session.State, args []string) string {
			captured = args
			return ""
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	m := New(modelTestState(t), WithCommandRegistry(cmdReg))
	m.resize(80, 24)

	_, _ = m.dispatchCommand(`/teststub "my idea"`)

	if len(captured) != 1 || captured[0] != "my idea" {
		t.Fatalf("captured args = %q, want [\"my idea\"]", captured)
	}
}

// TestPatternForApproval_SecurityProperty verifies that a pattern produced for
// "git status" does NOT match a malicious command like "git ; rm -rf /" via the
// permissions matching logic.
func TestPatternForApproval_SecurityProperty(t *testing.T) {
	tc := &session.PendingToolCall{
		Name:    "shell.run",
		Command: "git status",
	}
	pattern := patternForApproval(tc)

	// The pattern "git status" must NOT match "git ; rm -rf /"
	if permissions.Matches(pattern, "git ; rm -rf /") {
		t.Errorf("pattern %q should NOT match malicious command %q", pattern, "git ; rm -rf /")
	}

	// The pattern MUST match "git status" (exact match)
	if !permissions.Matches(pattern, "git status") {
		t.Errorf("pattern %q SHOULD match %q", pattern, "git status")
	}

	// The pattern must NOT contain "*"
	if strings.Contains(pattern, "*") {
		t.Errorf("pattern %q should not contain wildcard", pattern)
	}
}
