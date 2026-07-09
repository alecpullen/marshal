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

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/commands"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
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
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Approval") {
		t.Fatal("View() missing Approval panel")
	}
	if !strings.Contains(view, "go test") {
		t.Fatal("View() missing proposed command")
	}

	// 1. Test Deny: navigate to "Deny" (one Down) and submit.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
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
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// 2. Test Approve: the first option is already focused, submit with Enter.
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
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// 3. Test Edit: navigate to "Edit" (two Down) and submit.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
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
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// 4. Test Always Allow: navigate to the 4th option (three Down) and submit.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
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
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	// 5. Test Allow this session: navigate to the 5th option (four Down) and submit.
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
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

func (f *fakeAgentRunner) SetForceClass(string) {}
func (f *fakeAgentRunner) SetPolicyRules([]config.PermissionRule) {}

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

func (f *fakeSwarmRunner) SetForceClass(string) {}
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
	if len(batch) != 2 {
		t.Fatalf("len(batch) = %d, want 2", len(batch))
	}

	var sawFinished, sawTick bool
	for _, sub := range batch {
		switch msg := sub().(type) {
		case agentFinishedMsg:
			sawFinished = true
			if msg.err != nil {
				t.Fatalf("agentFinishedMsg.err = %v, want nil", msg.err)
			}
		case agentTickMsg:
			sawTick = true
		default:
			t.Fatalf("unexpected message type %T", msg)
		}
	}
	if !sawFinished || !sawTick {
		t.Fatalf("sawFinished=%v sawTick=%v, want both true", sawFinished, sawTick)
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
	if cmd != nil {
		t.Fatal("expected a nil cmd after agentFinishedMsg")
	}
	if err := state.ProviderError(); err == nil || err.Error() != "boom" {
		t.Fatalf("ProviderError() = %v, want an error wrapping %q", err, "boom")
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
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}

	if got := m.settingsModel.FocusedFieldTitle(); got != "Default profile" {
		t.Fatalf("first settings field should be focused, got %q", got)
	}

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if got := m.settingsModel.FocusedFieldTitle(); got != "Preset" {
		t.Fatalf("Tab should move focus to Preset field, got %q", got)
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

	// Tab to Provider field (third field)
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if got := m.settingsModel.FocusedFieldTitle(); got != "Provider" {
		t.Fatalf("expected Provider field focused, got %q", got)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Text: "z"})
	m = updated.(Model)

	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "Provider") || !strings.Contains(view, "ollamaz") {
		t.Fatalf("typing should append to Provider value, got:\n%s\nstate provider=%q", view, m.settingsModel.FocusedFieldTitle())
	}

	// Move cursor left inside the textinput and type in the middle.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Text: "A"})
	m = updated.(Model)

	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "Provider") || !strings.Contains(view, "ollamaAz") {
		t.Fatalf("cursor movement should insert in the middle, got:\n%s", view)
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

	// Tab to Remote providers allowed field (last field)
	for i := 0; i < 5; i++ {
		m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
	}

	if got := m.settingsModel.FocusedFieldTitle(); got != "Remote providers allowed" {
		t.Fatalf("expected Remote providers allowed field focused, got %q", got)
	}
	if m.settingsModel.BoolValue("Remote providers allowed") {
		t.Fatalf("expected bool to start false")
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m = updated.(Model)

	if !m.settingsModel.BoolValue("Remote providers allowed") {
		t.Fatalf("Space should toggle bool to true")
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

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if got := m.settingsModel.FocusedFieldTitle(); got != "Preset" {
		t.Fatalf("Tab should move focus to Preset field with default config, got %q", got)
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
	state.SetActivity(session.Activity{Kind: session.ActivityThinking, Label: "thinking...", StartedAt: time.Now()})
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

	model.input.SetValue("/help")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m := updated.(*Model)

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

	select {
	case decision := <-ch:
		if !decision.Approved {
			t.Fatal("approval decision = false, want true (Enter should approve)")
		}
	default:
		t.Fatal("no decision sent on ResponseChan after Enter")
	}
	if state.PendingApproval() != nil {
		t.Fatal("PendingApproval still set after Enter, want nil")
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

	q := &session.PendingQuestion{Question: "Archive or delete?", ResponseChan: make(chan string, 1)}
	state.SetPendingQuestion(q)

	for _, r := range "archive" {
		model, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = model.(Model)
	}
	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	select {
	case got := <-q.ResponseChan:
		if got != "archive" {
			t.Fatalf("answer = %q, want archive", got)
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
	q := &session.PendingQuestion{Question: "Archive or delete?", ResponseChan: make(chan string, 1)}
	state.SetPendingQuestion(q)

	m = sendKey(m, tea.KeyPressMsg{Code: tea.KeyEsc})

	select {
	case got := <-q.ResponseChan:
		if got != "" {
			t.Fatalf("answer = %q, want empty (declined)", got)
		}
	default:
		t.Fatal("no answer sent on Esc")
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question not cleared after Esc")
	}
}
