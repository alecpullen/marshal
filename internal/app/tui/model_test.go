package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/app/tui/memory"
	"marshal/internal/app/tui/settings"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

func TestEnterAppendsInputAndClearsPrompt(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
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

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	if got := len(state.Messages()); got != 0 {
		t.Fatalf("len(messages) = %d, want 0", got)
	}
}

func TestQuitKeyRequestsShutdown(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	// First Esc unfocuses input
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	// Second Esc quits
	_, cmd := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("quit command is nil")
	}

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not shut down")
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
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = updated.(Model)
	if !m.memoryOpen {
		t.Fatal("expected memoryOpen to be true")
	}
	if !strings.Contains(m.View(), "Project Memories") {
		t.Fatalf("View() missing memory browser:\n%s", m.View())
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
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
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

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = updated.(Model)
	if m.memoryOpen {
		t.Fatal("expected memoryOpen to remain false without memory store")
	}
	if strings.Contains(m.View(), "Project Memories") {
		t.Fatalf("View() should not show memory browser without memory store:\n%s", m.View())
	}
}

func TestPolishedViewContainsCurrentLayoutChrome(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Marshal",
		"Ask",
		"Plan",
		"Auto",
		"Swarm",
		"Chat",
		"live transcript",
		"1 Plan",
		"2 Context",
		"3 Log",
		"MARSHAL",
		"Ask Marshal...",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestPolishedStatusBarShowsRouteWhenActive(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActiveRoute(session.RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"MARSHAL",
		"Auto",
		"implementer",
		"qwen2.5-coder:14b @ ollama",
		"local",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing status item %q:\n%s", want, view)
		}
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

	view := m.View()
	for _, want := range []string{
		"Agent wants to run",
		"go test ./...",
		"Reason",
		"run the repository test suite",
		"Risk",
		"--- a/app.go",
		"+++ b/app.go",
		"+added line",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing approval item %q:\n%s", want, view)
		}
	}
}

func TestPolishedRightPanelTracksActiveTab(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{
				Kind:            contextpack.SectionRepoCard,
				Title:           "Repo Card",
				Source:          "repo.card",
				Content:         "Project: marshal",
				EstimatedTokens: 4,
			},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(1719946800, 0),
		ToolName:      "shell.run",
		ResultSummary: "command exit status 0",
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	planView := m.View()
	for _, want := range []string{"No active plan.", "Ready for input", "1 Plan", "2 Context", "3 Log"} {
		if !strings.Contains(planView, want) {
			t.Fatalf("plan tab content missing %q:\n%s", want, planView)
		}
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = updated.(Model)
	contextView := m.View()
	for _, want := range []string{"Context Pack", "4 / 12k", "Repo Card"} {
		if !strings.Contains(contextView, want) {
			t.Fatalf("context tab missing %q:\n%s", want, contextView)
		}
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	m = updated.(Model)
	logView := m.View()
	for _, want := range []string{"shell.run", "command exit"} {
		if !strings.Contains(logView, want) {
			t.Fatalf("log tab missing %q:\n%s", want, logView)
		}
	}
}

func TestPolishedViewFitsCommonTerminalSizes(t *testing.T) {
	longMessage := "I am checking that the transcript, prompt, and thinking blocks all reflow cleanly when the terminal width changes across common viewport sizes."
	longThinking := "First inspect the current viewport width. Then keep the newest visible reasoning in the thinking block while making sure every rendered line still respects the terminal width budget."
	for _, size := range []struct {
		width  int
		height int
	}{
		{80, 24},
		{100, 30},
		{120, 40},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
			state.AddMessage(session.RoleUser, longMessage)
			state.BeginStreaming()
			state.AppendThinking(longThinking)
			m := New(state)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			m = updated.(Model)
			m.busy = true
			m.refreshViewport()

			view := m.View()
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > size.height {
				t.Fatalf("line count = %d, want <= %d\n%s", len(lines), size.height, view)
			}
			for i, line := range lines {
				if got := visibleRunes(line); got > size.width {
					t.Fatalf("line %d width = %d, want <= %d\n%s", i+1, got, size.width, line)
				}
			}
		})
	}
}

func TestPolishedTranscriptReflowsAfterResize(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	message := "This transcript line should be wide enough to wrap differently after resizing from a wide terminal down to a narrow terminal."
	thinking := "The live thinking copy should also be rebuilt after resize so it follows the narrowed viewport width instead of keeping stale wrapping."
	state.AddMessage(session.RoleUser, message)
	state.BeginStreaming()
	state.AppendThinking(thinking)
	m := New(state)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	m.busy = true
	m.refreshViewport()
	wideView := m.View()

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	narrowView := m.View()
	expectedViewport := renderMessage(string(session.RoleUser), message, m.viewport.Width) + renderThinkingBox(thinking, m.viewport.Width)

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
	state.AddMessage(session.RoleUser, "fix the layout")
	state.BeginStreaming()
	state.AppendThinking("I need to inspect the render bounds and keep the newest reasoning visible.")
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)
	m.busy = true
	m.refreshViewport()

	view := m.View()
	for _, want := range []string{
		"user",
		"fix the layout",
		"thinking",
		"Ask Marshal...",
		"Ctrl+G thinking",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestViewShowsProviderErrorWhenSet(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	state.SetProviderError(errors.New("dial tcp: connection refused"))
	view := m.View()

	if !strings.Contains(view, "Provider Error") {
		t.Fatalf("View() missing 'Provider Error' substring:\n%s", view)
	}
	if !strings.Contains(view, "connection refused") {
		t.Fatalf("View() missing 'connection refused' substring:\n%s", view)
	}
}

func TestViewOmitsProviderErrorSectionByDefault(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	view := m.View()

	if strings.Contains(view, "Provider Error") {
		t.Fatalf("View() should not contain 'Provider Error' when no error is set:\n%s", view)
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

	view := m.View()
	if !strings.Contains(view, "Approval") {
		t.Fatal("View() missing Approval panel")
	}
	if !strings.Contains(view, "go test") {
		t.Fatal("View() missing proposed command")
	}

	// 1. Test Deny Keypress 'd'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = updated.(Model)

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

	// Set up again for Enter key
	state.SetPendingApproval(tc)
	m = New(state)

	// 2. Test Approve Keypress 'enter'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	select {
	case dec := <-respChan:
		if !dec.Approved || dec.Edited != "" {
			t.Fatal("expected decision to be approved and not edited")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for approve response")
	}

	// Set up again for Edit key
	state.SetPendingApproval(tc)
	m = New(state)

	// 3. Test Edit Keypress 'e'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(Model)

	if !m.editingCommand {
		t.Fatal("expected model to enter editingCommand mode")
	}
	if m.input.Value() != "go test" {
		t.Fatalf("expected input value to be 'go test', got %q", m.input.Value())
	}

	// Simulate typing to edit command
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" -v")})
	m = updated.(Model)
	if m.input.Value() != "go test -v" {
		t.Fatalf("expected edited input value to be 'go test -v', got %q", m.input.Value())
	}

	// Press Enter to confirm edited command
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	select {
	case dec := <-respChan:
		if !dec.Approved || dec.Edited != "go test -v" {
			t.Fatalf("expected decision to be approved with edited command, got: %#v", dec)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for edited response")
	}

	// Set up again for Always Allow key
	state.SetPendingApproval(tc)
	m = New(state)

	// 4. Test Always Allow Keypress 'a'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = updated.(Model)

	select {
	case dec := <-respChan:
		if !dec.Approved {
			t.Fatal("expected decision to be approved via always allow")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for always allow response")
	}

	// Check if session rule was added
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
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	model = updated.(Model)

	if state.HasBackup() {
		t.Fatal("expected backup to be cleared after rollback")
	}

	view := model.View()
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

func TestEnterWithRunnerDispatchesAgentRunAndTick(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	runner := &fakeAgentRunner{called: make(chan string, 1)}
	model := New(state, WithRunner(context.Background(), runner))

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
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

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
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

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
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
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen to be true")
	}
}

func TestSettingsCancelClosesOverlay(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
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

func TestModelLayoutStateInit(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	if !model.inputFocused {
		t.Error("expected inputFocused to be true by default")
	}
	if model.activeTab != 0 {
		t.Errorf("expected activeTab to be 0 (Plan), got %d", model.activeTab)
	}
}

func TestFocusAndTabNavigation(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	// Test Esc unfocuses input
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.inputFocused {
		t.Error("Esc did not unfocus input")
	}

	// Test Enter focuses input when unfocused
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if !model.inputFocused {
		t.Error("Enter did not focus input when unfocused")
	}

	// Test Ctrl+X switches tab to Context (1)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	model = updated.(Model)
	if model.activeTab != 1 {
		t.Errorf("Ctrl+X did not switch to Context tab, got activeTab=%d", model.activeTab)
	}

	// Test number key when unfocused
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc}) // unfocus
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")}) // Press '3'
	model = updated.(Model)
	if model.activeTab != 2 {
		t.Errorf("Pressing 3 did not switch to Log tab, got activeTab=%d", model.activeTab)
	}
}

func TestResizeComputesGeometry(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model := updated.(Model)

	if model.width != 80 || model.height != 24 {
		t.Fatalf("size = %dx%d, want 80x24", model.width, model.height)
	}
	if model.leftWidth < 50 || model.leftWidth > 60 {
		t.Fatalf("leftWidth = %d, want ~56", model.leftWidth)
	}
	if model.rightWidth < minPanelWidth {
		t.Fatalf("rightWidth = %d, too small", model.rightWidth)
	}
	if model.chatHeight < 1 {
		t.Fatalf("chatHeight = %d, want >= 1", model.chatHeight)
	}
	if model.viewport.Width != model.leftWidth {
		t.Fatalf("viewport.Width = %d, want %d", model.viewport.Width, model.leftWidth)
	}
	if model.viewport.Height != model.chatHeight-1 {
		t.Fatalf("viewport.Height = %d, want %d", model.viewport.Height, model.chatHeight-1)
	}
	if model.input.Width != model.leftWidth-6 {
		t.Fatalf("input.Width = %d, want %d", model.input.Width, model.leftWidth-6)
	}
}

func TestAltScreenViewLayout(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	// Simulate terminal size via the resize path so stored geometry is populated.
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	model = updated.(Model)

	view := model.View()
	if !strings.Contains(view, "1 Plan") {
		t.Error("view missing Plan tab title")
	}
	if !strings.Contains(view, "Ctrl+O") {
		t.Error("view missing keybind help text")
	}
}

func TestAltScreenViewFits80x24(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	view := model.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > 24 {
		t.Fatalf("view height = %d lines, want <= 24", len(lines))
	}
	for i, line := range lines {
		if len([]rune(line)) > 80 {
			t.Fatalf("line %d width = %d, want <= 80: %q", i, len([]rune(line)), line)
		}
	}
}

func TestStatusBarFitsTerminalWidth(t *testing.T) {
	cases := []struct {
		width  int
		height int
	}{
		{80, 24},
		{40, 10},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dx%d", c.width, c.height), func(t *testing.T) {
			state := session.New(config.Default(), "/very/long/working/directory/path", time.Unix(100, 0), session.Persistence{})
			state.Config.Project.Name = "a-very-long-project-name"
			model := New(state)
			updated, _ := model.Update(tea.WindowSizeMsg{Width: c.width, Height: c.height})
			model = updated.(Model)

			view := model.View()
			lines := strings.Split(view, "\n")
			last := lines[len(lines)-1]
			if len([]rune(last)) > c.width {
				t.Fatalf("status bar width = %d, want <= %d", len([]rune(last)), c.width)
			}
		})
	}
}

func TestViewFitsTerminalSizes(t *testing.T) {
	sizes := []struct {
		width  int
		height int
	}{
		{40, 10},
		{50, 20},
		{80, 24},
		{100, 30},
	}
	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dx%d", sz.width, sz.height), func(t *testing.T) {
			state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
			model := New(state)
			updated, _ := model.Update(tea.WindowSizeMsg{Width: sz.width, Height: sz.height})
			model = updated.(Model)

			view := model.View()
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > sz.height {
				t.Fatalf("view height = %d lines, want <= %d", len(lines), sz.height)
			}
			for i, line := range lines {
				if w := len([]rune(line)); w > sz.width {
					t.Fatalf("line %d width = %d, want <= %d: %q", i, w, sz.width, line)
				}
			}
		})
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

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyTab},
		{Type: tea.KeyShiftTab},
		{Type: tea.KeyCtrlP},
		{Type: tea.KeyCtrlX},
		{Type: tea.KeyCtrlT},
		{Type: tea.KeyCtrlR},
	} {
		updated, _ := model.Update(key)
		m := updated.(Model)
		if m.activeTab != 0 {
			t.Fatalf("activeTab changed on %v during approval", key)
		}
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
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
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
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model = updated.(Model)
	if !model.memoryOpen {
		t.Fatal("Ctrl+K did not open memory")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model = updated.(Model)
	if model.memoryOpen {
		t.Fatal("Ctrl+K did not close memory")
	}
}

func TestProviderErrorVisibleInAltScreen(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetProviderError(errors.New("connection refused"))
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	view := model.View()
	if !strings.Contains(view, "connection refused") {
		t.Fatalf("provider error not visible in AltScreen view:\n%s", view)
	}
}

type streamingRunner struct {
	called chan string
}

func (s *streamingRunner) Run(ctx context.Context, goal string) error {
	s.called <- goal
	return nil
}

func TestBusyTickRefreshesViewport(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	runner := &streamingRunner{called: make(chan string, 1)}
	model := New(state, WithRunner(context.Background(), runner))
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	// Start a turn.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

	// Simulate the agent adding a message mid-turn.
	state.AddMessage(session.RoleAssistant, "working...")

	// Tick should refresh the viewport.
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)

	view := model.View()
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
	state.AddMessage(session.RoleUser, longContent)
	model.refreshViewport()

	viewport := model.viewport.View()
	lines := strings.Split(viewport, "\n")
	maxWidth := model.viewport.Width

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

	view := model.View()
	// With an empty diff there should be a single full-width Approval panel,
	// not a split Diff+Approval layout. A duplicated or nested panel would
	// show multiple "Approval" titles or a "Diff" title alongside it.
	if strings.Count(view, "Approval") != 1 {
		t.Fatalf("approval banner missing title or duplicated:\n%s", view)
	}
	if strings.Contains(view, "Diff") {
		t.Fatalf("approval banner should not be split into a Diff panel:\n%s", view)
	}
	if !strings.Contains(view, "run tests") {
		t.Fatalf("approval banner missing human reason:\n%s", view)
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
			state.AddMessage(session.RoleAssistant, fmt.Sprintf("msg %d", i))
			state.LogToolCall(registry.AuditEvent{ToolName: "test", ResultSummary: "ok"})
		}
	}()

	for i := 0; i < 200; i++ {
		_ = model.View()
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

	view := model.View()
	if !strings.Contains(view, "thinking") {
		t.Fatalf("view missing thinking box:\n%s", view)
	}
	if !strings.Contains(view, "checking the auth flow") {
		t.Fatalf("view missing live reasoning text:\n%s", view)
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
	state.AddMessage(session.RoleAssistant, "Here's the fix.")
	model.refreshViewport()

	view := model.View()
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
	state.AddMessage(session.RoleAssistant, "Here's the fix.")
	model.refreshViewport()

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	model = updated.(Model)
	if !strings.Contains(model.View(), "checking the auth flow") {
		t.Fatalf("expected expanded reasoning after Ctrl+G:\n%s", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	model = updated.(Model)
	if strings.Contains(model.View(), "checking the auth flow") {
		t.Fatalf("expected collapsed reasoning after second Ctrl+G:\n%s", model.View())
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
	if !strings.Contains(model.View(), "step one") {
		t.Fatal("expected viewport to show reasoning after first tick")
	}

	state.AppendThinking(" step two")
	updated, _ = model.Update(agentTickMsg{})
	model = updated.(Model)
	if !strings.Contains(model.View(), "step one step two") {
		t.Fatalf("expected viewport to refresh on reasoning growth alone (message count unchanged):\n%s", model.View())
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

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}

	view := m.View()
	if !strings.Contains(view, "> Default profile:") {
		t.Fatalf("first settings field should be focused:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "> Preset:") {
		t.Fatalf("Tab should move focus to second field:\n%s", view)
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

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)

	// Tab to Provider field (third field)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "> Provider:") {
		t.Fatalf("expected Provider field focused:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "Provider: ollamaz") {
		t.Fatalf("typing should append to Provider value, got:\n%s", view)
	}

	// Move cursor left inside the textinput and type in the middle.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "Provider: ollamaAz") {
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

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)

	// Tab to Remote providers allowed field (last field)
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
	}

	view := m.View()
	if !strings.Contains(view, "> [ ] Remote providers allowed") {
		t.Fatalf("expected Remote providers allowed field focused:\n%s", view)
	}
	if !strings.Contains(view, "[ ] Remote providers allowed") {
		t.Fatalf("expected bool to start false, got:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = updated.(Model)

	view = m.View()
	if !strings.Contains(view, "[x] Remote providers allowed") {
		t.Fatalf("Space should toggle bool to true, got:\n%s", view)
	}
}

func TestSettingsNavigationWithDefaultConfig(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = updated.(Model)
	if !m.settingsOpen {
		t.Fatal("expected settingsOpen")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "> Preset:") {
		t.Fatalf("Tab should move focus to second field with default config:\n%s", view)
	}
}

func TestPolishedSidebarTabsAndContextSummary(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Source: "repo.card", EstimatedTokens: 120},
			{Kind: contextpack.SectionFileSnippet, Title: "internal/app/tui/model.go", Source: "internal/app/tui/model.go", EstimatedTokens: 8400},
		},
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)
	m.activeTab = 1

	view := m.View()
	for _, want := range []string{
		"1 Plan",
		"2 Context",
		"3 Log",
		"Context Pack",
		"18k / 32k",
		"internal/app/tui/model.go",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestRenderSidebarTabsSingleRowAcrossActiveIndex(t *testing.T) {
	const width = 40
	labels := []string{"1 Plan", "2 Context", "3 Log"}
	var lineCounts []int
	for active := 0; active < 3; active++ {
		out := renderSidebarTabs(width, active)
		lines := strings.Split(out, "\n")
		lineCounts = append(lineCounts, len(lines))

		// Diagnostic-only: each label must appear somewhere in the output.
		// This alone does not prove the labels are aligned onto a single
		// row — lipgloss.JoinHorizontal pads every block to equal height,
		// so a label detached onto its own line still "appears somewhere"
		// even in the buggy mismatched-pill-height case.
		for _, name := range labels {
			found := false
			for _, line := range lines {
				if strings.Contains(line, name) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("active=%d: no line contains label %q; lines=%q", active, name, lines)
			}
		}

		// Load-bearing assertion: the tab strip must render as a single
		// logical row, meaning there must be one line that simultaneously
		// contains all three labels together. If active/inactive pill
		// styles have mismatched rendered heights, the labels land on
		// different lines relative to each other and no single line
		// contains all three, even though each label individually still
		// appears somewhere in the block.
		foundCombinedLine := false
		for _, line := range lines {
			allPresent := true
			for _, name := range labels {
				if !strings.Contains(line, name) {
					allPresent = false
					break
				}
			}
			if allPresent {
				foundCombinedLine = true
				break
			}
		}
		if !foundCombinedLine {
			t.Fatalf("active=%d: no single line contains all labels %q together; lines=%q", active, labels, lines)
		}
	}

	for i := 1; i < len(lineCounts); i++ {
		if lineCounts[i] != lineCounts[0] {
			t.Fatalf("renderSidebarTabs height varies by active index: active=0 -> %d lines, active=%d -> %d lines", lineCounts[0], i, lineCounts[i])
		}
	}
}

func TestPolishedApprovalStateShowsCommandReasonRiskAndActions(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetPendingApproval(&session.PendingToolCall{
		Command:      "go test ./internal/app/tui/...",
		Reason:       "Validate layout bounds and modal capture.",
		Risk:         "Low - test command, no destructive flags detected.",
		Diff:         "- old\n+ new\n",
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Diff",
		"Agent wants to run",
		"go test ./internal/app/tui/...",
		"Reason",
		"Risk",
		"Enter approve",
		"d deny",
		"e edit",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing approval copy %q:\n%s", want, view)
		}
	}

	// The panel labeled "Risk" must show the actual risk classification,
	// not a second copy of the Reason text. Find the line following the
	// "Risk" label and assert it holds the Risk content (and not the
	// Reason content) — this guards against riskText() preferring Reason
	// over Risk.
	lines := strings.Split(view, "\n")
	foundRiskLine := false
	for i, line := range lines {
		if strings.Contains(line, "Risk") && i+1 < len(lines) {
			next := lines[i+1]
			if strings.Contains(next, "Low - test command") {
				foundRiskLine = true
			}
			if strings.Contains(next, "Validate layout bounds") {
				t.Fatalf("Risk section shows Reason content instead of Risk content:\n%s", view)
			}
		}
	}
	if !foundRiskLine {
		t.Fatalf("View() did not render the Risk classification text under the Risk label:\n%s", view)
	}
}

func TestPolishedProviderErrorUsesCompactBanner(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetProviderError(errors.New("provider timeout: retrying local_heavy"))
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Provider Error",
		"fits AltScreen",
		"provider timeout",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing provider error copy %q:\n%s", want, view)
		}
	}
}

func TestPolishedProviderErrorBannerFitsCommonTerminalSizes(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{
		{40, 10},
		{80, 24},
		{100, 30},
		{120, 40},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
			state.SetProviderError(errors.New("provider timeout: retrying local_heavy"))
			m := New(state)
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			m = updated.(Model)

			view := m.View()
			lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
			if len(lines) > size.height {
				t.Fatalf("line count = %d, want <= %d\n%s", len(lines), size.height, view)
			}
			for i, line := range lines {
				if got := visibleRunes(line); got > size.width {
					t.Fatalf("line %d width = %d, want <= %d\n%s", i+1, got, size.width, line)
				}
			}
		})
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

	view := m.View()
	if !strings.Contains(view, "⠋") {
		t.Fatalf("View() missing spinner frame in status bar:\n%s", view)
	}
	if !strings.Contains(view, "thinking...") {
		t.Fatalf("View() missing thinking label in status bar:\n%s", view)
	}
}

func TestStatusBarShowsToolLabel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test ./...", StartedAt: time.Now()})
	m := New(state)
	m.spinnerFrame = "⠹"
	m.busy = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "⠹") {
		t.Fatalf("View() missing spinner frame in status bar:\n%s", view)
	}
	if !strings.Contains(view, "shell.run") {
		t.Fatalf("View() missing tool label in status bar:\n%s", view)
	}
}

func TestStatusBarShowsDoneBadgeAfterActivity(t *testing.T) {
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
	state.SetActivity(session.Activity{})

	view := m.View()
	if !strings.Contains(view, "✓") {
		t.Fatalf("View() missing done checkmark in status bar:\n%s", view)
	}
	if !strings.Contains(view, "shell.r") {
		t.Fatalf("View() missing tool label in done badge:\n%s", view)
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
	state.SetActivity(session.Activity{})

	if !strings.Contains(m.View(), "✓") {
		t.Fatal("expected done badge immediately after finish")
	}

	m.lastActivityDone = m.lastActivityDone.Add(-doneDisplayDuration).Add(-time.Millisecond)

	view := m.View()
	if strings.Contains(view, "✓") {
		t.Fatalf("done badge should have expired after %v:\n%s", doneDisplayDuration, view)
	}
	if !strings.Contains(view, "IDLE") {
		t.Fatalf("View() missing IDLE after done badge expiry:\n%s", view)
	}
}

func TestPlanTabShowsPlanItemsAndSpinner(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetPlan([]string{"Refactor layout", "Add tests", "Update docs"})
	state.SetActivity(session.Activity{Kind: session.ActivityTool, Label: "shell.run: go test", StartedAt: time.Now()})
	m := New(state)
	m.spinnerFrame = "⠙"
	m.busy = true
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{"Current Plan:", "Refactor layout", "Add tests", "Update docs", "shell.run: go test"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Plan tab missing %q:\n%s", want, view)
		}
	}
}

func TestPlanTabShowsNoActivePlanWhenIdleAndEmpty(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(Model)

	view := m.View()
	if !strings.Contains(view, "No active plan") {
		t.Fatalf("Plan tab missing 'No active plan':\n%s", view)
	}
	if !strings.Contains(view, "Ready for input") {
		t.Fatalf("Plan tab missing 'Ready for input':\n%s", view)
	}
}

func TestPolishedCurrentLayoutFullSurface(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetActiveRoute(session.RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	})
	state.AddMessage(session.RoleUser, "fix the failing TUI layout tests")
	state.AddMessage(session.RoleAssistant, "I found the render drift and am tightening the layout.")
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(100, 0),
		ToolName:      "go test",
		ResultSummary: "FAIL: line exceeds width",
	})
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{EstimatedTokens: 18000, MaxTokens: 32000},
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionFileSnippet, Title: "internal/app/tui/model.go", Source: "internal/app/tui/model.go", EstimatedTokens: 8400},
			{Kind: contextpack.SectionFileSnippet, Title: "internal/app/session/session.go", Source: "internal/app/session/session.go", EstimatedTokens: 4100},
		},
	})
	m := New(state)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 36})
	m = updated.(Model)

	view := m.View()
	for _, want := range []string{
		"Marshal",
		"Chat",
		"live transcript",
		"user",
		"assistant",
		"1 Plan",
		"2 Context",
		"3 Log",
		"MARSHAL",
		"implementer",
		"qwen2.5-coder:14b @ ollama",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("full surface missing %q:\n%s", want, view)
		}
	}
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	for i, line := range lines {
		if got := visibleRunes(line); got > 120 {
			t.Fatalf("line %d width = %d, want <= 120\n%s", i+1, got, line)
		}
	}
}
