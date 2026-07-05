package session

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/tools/registry"
)

func TestStateAppendsMessagesInOrder(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.AddMessage(RoleSystem, "ready", ContentTypePlain)
	state.AddMessage(RoleUser, "hello", ContentTypePlain)

	messages := state.Messages()
	if len(messages) != 2 {
		t.Fatalf("len(Messages()) = %d, want 2", len(messages))
	}
	if messages[0].Role != RoleSystem || messages[0].Content != "ready" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != RoleUser || messages[1].Content != "hello" {
		t.Fatalf("second message = %#v", messages[1])
	}
}

func TestMessagesReturnsCopy(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	state.AddMessage(RoleUser, "hello", ContentTypePlain)

	messages := state.Messages()
	messages[0].Content = "mutated"

	got := state.Messages()[0].Content
	if got != "hello" {
		t.Fatalf("stored message = %q, want hello", got)
	}
}

func TestStateContextPackStoresCopies(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	pack := contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal"},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	}

	state.SetContextPack(pack)
	pack.Sections[0].Content = "mutated before read"

	got := state.ContextPack()
	if got.Sections[0].Content != "Project: marshal" {
		t.Fatalf("ContextPack() = %#v, want stored copy", got)
	}

	got.Sections[0].Content = "mutated after read"
	gotAgain := state.ContextPack()
	if gotAgain.Sections[0].Content != "Project: marshal" {
		t.Fatalf("ContextPack() returned mutable internal slice: %#v", gotAgain)
	}
}

func TestStateActiveRouteStoresCopies(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	route := RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Active:    true,
	}

	state.SetActiveRoute(route)
	route.Model = "mutated"

	got := state.ActiveRoute()
	if got.Model != "qwen2.5-coder:14b" || !got.Active {
		t.Fatalf("ActiveRoute() = %#v", got)
	}
}

func TestShutdownCancelsState(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	state.Shutdown()

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not cancelled")
	}
}

func TestSetProviderErrorStoresAndRetrieves(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	testErr := errors.New("provider connection failed")
	state.SetProviderError(testErr)

	got := state.ProviderError()
	if !errors.Is(got, testErr) {
		t.Fatalf("ProviderError() = %v, want %v", got, testErr)
	}
}

func TestSetProviderErrorNilClearsExistingError(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	testErr := errors.New("provider connection failed")
	state.SetProviderError(testErr)

	state.SetProviderError(nil)

	got := state.ProviderError()
	if got != nil {
		t.Fatalf("ProviderError() = %v, want nil", got)
	}
}

func TestStatePendingApprovalAndSessionRules(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	// Initially nil
	if got := state.PendingApproval(); got != nil {
		t.Fatalf("PendingApproval() = %v, want nil", got)
	}

	tc := &PendingToolCall{
		ID:           "123",
		Name:         "shell.run",
		Args:         `{"command": "go test"}`,
		Command:      "go test",
		Risk:         "command",
		Reason:       "test verification",
		ResponseChan: make(chan UserApprovalDecision, 1),
	}

	state.SetPendingApproval(tc)
	gotTc := state.PendingApproval()
	if gotTc == nil || gotTc.ID != "123" || gotTc.Command != "go test" {
		t.Fatalf("PendingApproval() = %#v, want %#v", gotTc, tc)
	}

	// Add session rule
	state.AddSessionRule("go test")
	rules := state.SessionRules()
	if len(rules) != 1 || rules[0] != "go test" {
		t.Fatalf("SessionRules() = %v, want ['go test']", rules)
	}
}

func TestStateAuditLog(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	if got := len(state.AuditLog()); got != 0 {
		t.Fatalf("AuditLog() length = %d, want 0", got)
	}

	event := registry.AuditEvent{
		Timestamp:     time.Now(),
		ToolName:      "shell.run",
		ResultSummary: "exit status 0",
	}

	state.LogToolCall(event)
	log := state.AuditLog()
	if len(log) != 1 || log[0].ToolName != "shell.run" || log[0].ResultSummary != "exit status 0" {
		t.Fatalf("AuditLog() = %#v, want event", log)
	}
}

func TestStateBackups(t *testing.T) {
	tmpDir := t.TempDir()
	state := New(config.Default(), tmpDir, time.Unix(100, 0), Persistence{})

	if state.HasBackup() {
		t.Fatal("initially HasBackup() should be false")
	}

	filePath := tmpDir + "/test.txt"
	if err := os.WriteFile(filePath, []byte("patched content"), 0755); err != nil {
		t.Fatalf("setup write failed: %v", err)
	}

	backups := []BackupFile{
		{Path: "test.txt", Content: "original content", Mode: 0755},
	}
	state.StoreBackup(backups)

	if !state.HasBackup() {
		t.Fatal("expected HasBackup() to be true")
	}

	got := state.Backup()
	if len(got) != 1 || got[0].Content != "original content" || got[0].Mode != 0755 {
		t.Fatalf("unexpected backup: %#v", got)
	}

	// Test RollbackBackup
	err := state.RollbackBackup()
	if err != nil {
		t.Fatalf("RollbackBackup failed: %v", err)
	}

	// Verify file reverted
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if string(data) != "original content" {
		t.Fatalf("expected reverted content, got %q", string(data))
	}

	// Verify state cleared
	if state.HasBackup() {
		t.Fatal("expected HasBackup() to be false after rollback")
	}

	// Verify system notice added
	msgs := state.Messages()
	if len(msgs) != 1 || msgs[0].Role != RoleSystem || !strings.Contains(msgs[0].Content, "rolled back") {
		t.Fatalf("unexpected messages: %#v", msgs)
	}
}

func TestStatePersistsMessagesAndAudits(t *testing.T) {
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	projectID, err := dbConn.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	sessionID := "sess-1"
	if err := dbConn.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	s.AddMessage(RoleUser, "hello", ContentTypePlain)
	s.AddMessage(RoleAssistant, "hi", ContentTypePlain)

	event := registry.AuditEvent{
		Timestamp:     time.Now().UTC(),
		ToolName:      "file.read",
		ResultSummary: "read main.go",
	}
	s.LogToolCall(event)

	messages, err := dbConn.GetMessages(sessionID)
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Fatalf("first persisted message = %#v, want user/hello", messages[0])
	}

	calls, err := dbConn.GetToolCalls(sessionID)
	if err != nil {
		t.Fatalf("get tool calls: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 persisted tool call, got %d", len(calls))
	}
	if calls[0].ToolName != "file.read" || calls[0].ResultSummary != "read main.go" {
		t.Fatalf("persisted tool call = %#v, want file.read/read main.go", calls[0])
	}
}

func TestBeginStreamingThenAppendThinkingAccumulatesReasoning(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.BeginStreaming()
	state.AppendThinking("checking the ")
	state.AppendThinking("auth flow")

	got := state.InProgress()
	if !got.Active {
		t.Fatal("InProgress().Active = false, want true after BeginStreaming")
	}
	if got.Reasoning != "checking the auth flow" {
		t.Fatalf("InProgress().Reasoning = %q, want %q", got.Reasoning, "checking the auth flow")
	}
}

func TestEndStreamingMarksInactiveButPreservesReasoning(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()

	got := state.InProgress()
	if got.Active {
		t.Fatal("InProgress().Active = true, want false after EndStreaming")
	}
	if got.Reasoning != "checking the auth flow" {
		t.Fatalf("InProgress().Reasoning = %q, want preserved after EndStreaming", got.Reasoning)
	}
}

func TestAddMessageAttachesReasoningFromInProgressAndClearsIt(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.BeginStreaming()
	state.AppendThinking("checking the auth flow")
	state.EndStreaming()
	state.AddMessage(RoleAssistant, "Here's the fix.", ContentTypePlain)

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1", len(messages))
	}
	if messages[0].Reasoning != "checking the auth flow" {
		t.Fatalf("messages[0].Reasoning = %q, want %q", messages[0].Reasoning, "checking the auth flow")
	}
	if messages[0].ThinkDuration <= 0 {
		t.Fatalf("messages[0].ThinkDuration = %v, want > 0", messages[0].ThinkDuration)
	}

	if got := state.InProgress().Reasoning; got != "" {
		t.Fatalf("InProgress().Reasoning after AddMessage = %q, want empty (cleared)", got)
	}

	state.AddMessage(RoleUser, "thanks", ContentTypePlain)
	messages = state.Messages()
	if messages[1].Reasoning != "" || messages[1].ThinkDuration != 0 {
		t.Fatalf("messages[1] should have no reasoning when nothing was streamed: %#v", messages[1])
	}
}

func TestTurnToolCacheCachesAndClears(t *testing.T) {
	state := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	args := []byte(`{"path":"a.go"}`)
	want := registry.ToolResult{Summary: "read ok", Content: "package a"}

	state.SetTurnToolResult("file.read", args, want)
	got, ok := state.GetTurnToolResult("file.read", args)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Content != want.Content {
		t.Fatalf("cached content = %q, want %q", got.Content, want.Content)
	}

	state.ClearTurnToolCache()
	if _, ok := state.GetTurnToolResult("file.read", args); ok {
		t.Fatal("expected cache miss after clear")
	}
}

func TestStreamingLifecycleIsRaceFree(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			state.BeginStreaming()
			state.AppendThinking("step")
			state.EndStreaming()
			state.AddMessage(RoleAssistant, "answer", ContentTypePlain)
		}
	}()

	for i := 0; i < 100; i++ {
		_ = state.InProgress()
		_ = state.Messages()
	}
	<-done
}

func TestStateActivityRoundTrip(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	got := state.Activity()
	if got.Kind != ActivityIdle {
		t.Fatalf("initial Activity().Kind = %q, want idle", got.Kind)
	}

	act := Activity{Kind: ActivityTool, Label: "shell.run: go test", StartedAt: time.Unix(200, 0)}
	state.SetActivity(act)

	got = state.Activity()
	if got.Kind != ActivityTool || got.Label != "shell.run: go test" {
		t.Fatalf("Activity() = %#v", got)
	}
}

func TestStateActivityZeroValueIsIdle(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.SetActivity(Activity{})
	got := state.Activity()
	if got.Kind != ActivityIdle || got.Label != "" {
		t.Fatalf("Activity() = %#v, want zero/idle", got)
	}
}

func TestStatePlanRoundTrip(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	if len(state.Plan()) != 0 {
		t.Fatal("initial Plan() should be empty")
	}

	plan := []string{"Refactor layout", "Add tests", "Update docs"}
	state.SetPlan(plan)

	got := state.Plan()
	if len(got) != 3 || got[0] != "Refactor layout" || got[1] != "Add tests" {
		t.Fatalf("Plan() = %v", got)
	}

	plan[0] = "mutated"
	gotAgain := state.Plan()
	if gotAgain[0] != "Refactor layout" {
		t.Fatalf("Plan() returned mutable internal slice: %v", gotAgain)
	}
}

func TestStateActivityIsRaceFree(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			state.SetActivity(Activity{Kind: ActivityThinking, Label: "thinking..."})
			state.SetActivity(Activity{Kind: ActivityTool, Label: "shell.run: go test"})
			state.SetActivity(Activity{})
		}
	}()

	for i := 0; i < 100; i++ {
		_ = state.Activity()
		_ = state.Plan()
	}
	<-done
}
