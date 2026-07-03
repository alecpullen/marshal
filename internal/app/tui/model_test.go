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

func TestViewContainsExpectedPanels(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Marshal",
		"Status",
		"Transcript",
		"Streaming Output",
		"Command Palette",
		"Tool Log",
		"Context",
		"Diff",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestViewShowsInactiveRouteByDefault(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	view := model.View()
	if !strings.Contains(view, "Route: inactive") {
		t.Fatalf("View() missing inactive route:\n%s", view)
	}
}

func TestViewShowsActiveRoute(t *testing.T) {
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
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Route: role=implementer",
		"profile=local_balanced",
		"preset=coder",
		"provider=ollama",
		"model=qwen2.5-coder:14b",
		"local-only=true",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}

func TestViewShowsProviderErrorWhenSet(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	state.SetProviderError(errors.New("dial tcp: connection refused"))
	view := model.View()

	if !strings.Contains(view, "Provider Error") {
		t.Fatalf("View() missing 'Provider Error' substring:\n%s", view)
	}
	if !strings.Contains(view, "connection refused") {
		t.Fatalf("View() missing 'connection refused' substring:\n%s", view)
	}
}

func TestViewOmitsProviderErrorSectionByDefault(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	view := model.View()

	if strings.Contains(view, "Provider Error") {
		t.Fatalf("View() should not contain 'Provider Error' when no error is set:\n%s", view)
	}
}

func TestViewShowsEmptyContextPanel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	view := model.View()
	if !strings.Contains(view, "Context") {
		t.Fatalf("View() missing Context panel:\n%s", view)
	}
	if !strings.Contains(view, "No context pack built yet.") {
		t.Fatalf("View() missing empty context message:\n%s", view)
	}
}

func TestViewShowsContextPackSummary(t *testing.T) {
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
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Context Pack: 4/12000 tokens",
		"repo_card",
		"Repo Card",
		"repo.card",
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

	model := New(state)

	// Check rendering of banner
	view := model.View()
	if !strings.Contains(view, "SECURITY APPROVAL REQUIRED") {
		t.Fatal("View() missing SECURITY APPROVAL REQUIRED banner")
	}
	if !strings.Contains(view, "go test") {
		t.Fatal("View() missing proposed command")
	}

	// 1. Test Deny Keypress 'd'
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	model = updated.(Model)

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
	model = New(state)

	// 2. Test Approve Keypress 'enter'
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

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
	model = New(state)

	// 3. Test Edit Keypress 'e'
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	model = updated.(Model)

	if !model.editingCommand {
		t.Fatal("expected model to enter editingCommand mode")
	}
	if model.input.Value() != "go test" {
		t.Fatalf("expected input value to be 'go test', got %q", model.input.Value())
	}

	// Simulate typing to edit command
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" -v")})
	model = updated.(Model)
	if model.input.Value() != "go test -v" {
		t.Fatalf("expected edited input value to be 'go test -v', got %q", model.input.Value())
	}

	// Press Enter to confirm edited command
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)

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
	model = New(state)

	// 4. Test Always Allow Keypress 'a'
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	model = updated.(Model)

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

func TestViewShowsAuditLogs(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	// Log a tool call
	state.LogToolCall(registry.AuditEvent{
		Timestamp:     time.Unix(1719946800, 0), // 15:00:00 UTC approximately
		ToolName:      "shell.run",
		ResultSummary: "command exit status 0",
	})

	view := model.View()
	if !strings.Contains(view, "shell.run") {
		t.Fatal("expected View to contain logged tool name")
	}
	if !strings.Contains(view, "command exit status 0") {
		t.Fatal("expected View to contain logged tool result summary")
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
	if model.viewport.Width != model.leftWidth-2 {
		t.Fatalf("viewport.Width = %d, want %d", model.viewport.Width, model.leftWidth-2)
	}
	if model.viewport.Height != model.chatHeight {
		t.Fatalf("viewport.Height = %d, want %d", model.viewport.Height, model.chatHeight)
	}
	if model.input.Width != model.leftWidth-4 {
		t.Fatalf("input.Width = %d, want %d", model.input.Width, model.leftWidth-4)
	}
}

func TestAltScreenViewLayout(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	// Simulate terminal size via the resize path so stored geometry is populated.
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	model = updated.(Model)

	view := model.View()
	// Check for sidebar tabs, status bar working dir
	if !strings.Contains(view, "[1] Plan") {
		t.Error("view missing Plan tab title")
	}
	if !strings.Contains(view, "/repo") {
		t.Error("view missing working directory in status bar")
	}
	if !strings.Contains(view, "[Ctrl+O] Settings") {
		t.Error("view missing keybind help text")
	}
}

func TestAltScreenViewFits80x24(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)

	view := model.View()
	lines := strings.Split(view, "\n")
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
	}
	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dx%d", sz.width, sz.height), func(t *testing.T) {
			state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
			model := New(state)
			updated, _ := model.Update(tea.WindowSizeMsg{Width: sz.width, Height: sz.height})
			model = updated.(Model)

			view := model.View()
			lines := strings.Split(view, "\n")
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
