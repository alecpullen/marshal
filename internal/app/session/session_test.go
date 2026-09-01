package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/contextpack"
	"marshal/internal/db"
	"marshal/internal/llm/routing"
	"marshal/internal/pubsub"
	"marshal/internal/tools/registry"
)

func newTestState() *State {
	return New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
}

func TestEnterSubagentDefaultCapIsThree(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	for i := 0; i < 3; i++ {
		if err := s.EnterSubagent(); err != nil {
			t.Fatalf("EnterSubagent #%d = %v, want nil (default cap 3)", i+1, err)
		}
	}
	err := s.EnterSubagent()
	if !errors.Is(err, ErrSubagentConcurrencyLimit) {
		t.Fatalf("4th EnterSubagent = %v, want ErrSubagentConcurrencyLimit", err)
	}
	if !strings.Contains(err.Error(), "(max 3)") {
		t.Fatalf("error = %q, want the configured cap in the message", err)
	}
}

func TestWithSubagentMaxConcurrencyOverrides(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{}, WithSubagentMaxConcurrency(1))
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("first EnterSubagent = %v", err)
	}
	if err := s.EnterSubagent(); !errors.Is(err, ErrSubagentConcurrencyLimit) {
		t.Fatalf("second EnterSubagent = %v, want limit at configured cap 1", err)
	}
	if got := s.SubagentMaxConcurrency(); got != 1 {
		t.Fatalf("SubagentMaxConcurrency = %d, want 1", got)
	}
}

func TestWithSubagentMaxConcurrencyClamps(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{}, WithSubagentMaxConcurrency(99))
	if got := s.SubagentMaxConcurrency(); got != 8 {
		t.Fatalf("SubagentMaxConcurrency = %d, want clamp to 8", got)
	}
	s0 := New(config.Default(), t.TempDir(), time.Now(), Persistence{}, WithSubagentMaxConcurrency(0))
	if got := s0.SubagentMaxConcurrency(); got != 3 {
		t.Fatalf("SubagentMaxConcurrency(0) = %d, want default 3", got)
	}
}

func TestConcurrencyLimitErrorNamesTheRemedy(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{}, WithSubagentMaxConcurrency(2))
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("first EnterSubagent: %v", err)
	}
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("second EnterSubagent: %v", err)
	}
	err := s.EnterSubagent()
	if err == nil {
		t.Fatal("want an error at the cap")
	}
	if !errors.Is(err, ErrSubagentConcurrencyLimit) {
		t.Fatalf("error no longer wraps the sentinel: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"2", "agent.await", "any"} {
		if !strings.Contains(msg, want) {
			t.Errorf("cap error %q does not mention %q", msg, want)
		}
	}
}

func TestLoggerReturnsCachedDiscard(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(0, 0), Persistence{})
	l1 := s.Logger()
	l2 := s.Logger()
	if l1 != l2 {
		t.Fatal("Logger() should return the same cached instance when logger is nil")
	}
}

func TestSetTodosNoRace(t *testing.T) {
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
	sessionID := "set-todos-race-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "race", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	todos := []db.TodoItem{
		{Content: "a", Status: "pending"},
		{Content: "b", Status: "completed"},
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.SetTodos(todos) }()
		go func() { defer wg.Done(); _ = s.Todos() }()
	}
	wg.Wait()
}

func TestStateAppendsMessagesInOrder(t *testing.T) {
	state := newTestState()

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

func TestAddMessageFinalWithToolCountStoresCount(t *testing.T) {
	state := newTestState()
	state.AddMessageFinalWithToolCount(RoleAssistant, "answer", ContentTypeMarkdown, 5)

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1", len(messages))
	}
	if messages[0].ToolCallCount != 5 {
		t.Fatalf("ToolCallCount = %d, want 5", messages[0].ToolCallCount)
	}
}

func TestAddMessageFinalStoresZeroToolCallCount(t *testing.T) {
	state := newTestState()
	state.AddMessageFinal(RoleAssistant, "answer", ContentTypeMarkdown)

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1", len(messages))
	}
	if messages[0].ToolCallCount != 0 {
		t.Fatalf("ToolCallCount = %d, want 0", messages[0].ToolCallCount)
	}
}

func TestMessagesReturnsCopy(t *testing.T) {
	state := newTestState()
	state.AddMessage(RoleUser, "hello", ContentTypePlain)

	messages := state.Messages()
	messages[0].Content = "mutated"

	got := state.Messages()[0].Content
	if got != "hello" {
		t.Fatalf("stored message = %q, want hello", got)
	}
}

func TestStateContextPackStoresCopies(t *testing.T) {
	state := newTestState()
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
	state := newTestState()
	route := RouteInfo{
		Role:      routing.RoleImplementer,
		Profile:   "local_balanced",
		Preset:    "coder",
		Provider:  "ollama",
		Model:     "qwen2.5-coder:14b",
		LocalOnly: true,
		Thinking:  "high",
		Active:    true,
	}

	state.SetActiveRoute(route)
	route.Model = "mutated"
	route.Thinking = "mutated"

	got := state.ActiveRoute()
	if got.Model != "qwen2.5-coder:14b" || !got.Active {
		t.Fatalf("ActiveRoute() = %#v", got)
	}
	if got.Thinking != "high" {
		t.Fatalf("ActiveRoute() lost Thinking = %#v", got)
	}
}

func TestShutdownCancelsState(t *testing.T) {
	state := newTestState()
	state.Shutdown()

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not cancelled")
	}
}

func TestStatePendingApprovalAndSessionRules(t *testing.T) {
	state := newTestState()

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
	state := newTestState()

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
		{Path: "test.txt", Content: "original content", Mode: 0755, Exists: true},
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

func TestSessionNewLoadsExistingTree(t *testing.T) {
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

	sessionID := "cold-start-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "cold", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// First state: persist a few messages on the default branch.
	first := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	first.AddMessage(RoleUser, "hello", ContentTypePlain)
	first.AddMessage(RoleAssistant, "hi", ContentTypePlain)
	first.AddMessage(RoleUser, "bye", ContentTypePlain)

	persistedLeaf := first.LeafID()
	if persistedLeaf == 0 {
		t.Fatal("first state leaf is 0, want a real db id")
	}
	persistedMessages := first.Messages()
	if len(persistedMessages) != 3 {
		t.Fatalf("persisted messages = %d, want 3", len(persistedMessages))
	}

	// Second state: simulate cold start — brand new in-memory struct, same DB + session.
	second := New(cfg, "/repo", time.Unix(200, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	gotMessages := second.Messages()
	if len(gotMessages) != len(persistedMessages) {
		t.Fatalf("cold-start messages = %d, want %d", len(gotMessages), len(persistedMessages))
	}
	for i, m := range gotMessages {
		if m.Content != persistedMessages[i].Content || m.Role != persistedMessages[i].Role {
			t.Fatalf("cold-start msg[%d] = %+v, want %+v", i, m, persistedMessages[i])
		}
	}
	if got := second.LeafID(); got != persistedLeaf {
		t.Fatalf("cold-start LeafID = %d, want %d", got, persistedLeaf)
	}

	// Adding another message after cold start should continue the branch
	// (parent in the DB) and the next new message must get a fresh
	// in-memory id (not collide with a loaded one).
	second.AddMessage(RoleUser, "after restart", ContentTypePlain)
	after := second.Messages()
	if len(after) != 4 {
		t.Fatalf("after restart messages = %d, want 4", len(after))
	}
	if after[3].Content != "after restart" {
		t.Fatalf("after restart last content = %q", after[3].Content)
	}
	if after[3].ParentID != persistedLeaf {
		t.Fatalf("after restart parent = %d, want %d", after[3].ParentID, persistedLeaf)
	}
}

func TestSessionNewWithEmptyDBStaysEmpty(t *testing.T) {
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

	sessionID := "empty-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "empty", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	if got := state.Messages(); len(got) != 0 {
		t.Fatalf("messages = %d, want 0 (empty db)", len(got))
	}
	if got := state.LeafID(); got != 0 {
		t.Fatalf("LeafID = %d, want 0 for empty db", got)
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
	state := newTestState()

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
	state := newTestState()

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
	state := newTestState()

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

	state.ClearToolCache()
	if _, ok := state.GetTurnToolResult("file.read", args); ok {
		t.Fatal("expected cache miss after clear")
	}
}

func TestStreamingLifecycleIsRaceFree(t *testing.T) {
	state := newTestState()

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
	state := newTestState()

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
	state := newTestState()

	state.SetActivity(Activity{})
	got := state.Activity()
	if got.Kind != ActivityIdle || got.Label != "" {
		t.Fatalf("Activity() = %#v, want zero/idle", got)
	}
}

func TestStatePlanRoundTrip(t *testing.T) {
	state := newTestState()

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
	state := newTestState()

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

func TestStateActiveSkillsRoundTrip(t *testing.T) {
	state := newTestState()

	if len(state.ActiveSkills()) != 0 {
		t.Fatal("initial active skills should be empty")
	}

	state.ActivateSkill("debugging")

	if !state.HasActiveSkill("debugging") {
		t.Fatal("HasActiveSkill(debugging) = false, want true")
	}
	if state.HasActiveSkill("nonexistent") {
		t.Fatal("HasActiveSkill(nonexistent) = true, want false")
	}

	active := state.ActiveSkills()
	if len(active) != 1 || active[0] != "debugging" {
		t.Fatalf("ActiveSkills() = %v, want [debugging]", active)
	}
}

func TestStateActivateSkillDuplicateNoop(t *testing.T) {
	state := newTestState()

	state.ActivateSkill("debugging")
	state.ActivateSkill("debugging")

	active := state.ActiveSkills()
	if len(active) != 1 {
		t.Fatalf("duplicate activation should produce 1 entry, got %d", len(active))
	}
}

func TestStateActiveSkillsReturnsCopy(t *testing.T) {
	state := newTestState()

	state.ActivateSkill("debugging")
	active := state.ActiveSkills()
	active[0] = "mutated"

	got := state.ActiveSkills()
	if got[0] != "debugging" {
		t.Fatalf("ActiveSkills() returned mutable slice: %v", got)
	}
}

func TestActiveToolCallSetAndGet(t *testing.T) {
	state := newTestState()
	atc := ActiveToolCall{
		Name:      "shell.run",
		Args:      "go test ./...",
		StartedAt: time.Unix(200, 0),
	}
	state.SetActiveToolCall(atc)
	got, ok := state.ActiveToolCall()
	if !ok {
		t.Fatal("ActiveToolCall() returned ok=false, want true")
	}
	if got.Name != "shell.run" || got.Args != "go test ./..." {
		t.Fatalf("ActiveToolCall() = %+v, want {Name: shell.run, Args: go test ./...}", got)
	}
	if !got.StartedAt.Equal(time.Unix(200, 0)) {
		t.Fatalf("StartedAt = %v, want 200", got.StartedAt)
	}
}

func TestActiveToolCallClear(t *testing.T) {
	state := newTestState()
	state.SetActiveToolCall(ActiveToolCall{Name: "file.read", Args: "/path"})
	state.ClearActiveToolCall()
	_, ok := state.ActiveToolCall()
	if ok {
		t.Fatal("ActiveToolCall() returned ok=true after ClearActiveToolCall, want false")
	}
}

func TestCurrentToolLabelIdle(t *testing.T) {
	state := newTestState()
	if got := state.CurrentToolLabel(); got != "" {
		t.Fatalf("CurrentToolLabel() idle = %q, want empty", got)
	}
}

func TestCurrentToolLabelFallbackName(t *testing.T) {
	state := newTestState()
	state.SetActiveToolCall(ActiveToolCall{Name: "shell.run", Args: "go test ./..."})
	if got := state.CurrentToolLabel(); got != "shell.run" {
		t.Fatalf("CurrentToolLabel() = %q, want %q", got, "shell.run")
	}
}

func TestCurrentToolLabelFileWritePatch(t *testing.T) {
	state := newTestState()
	state.SetActiveToolCall(ActiveToolCall{
		Name: "file.write_patch",
		Path: "internal/app/config/config.go",
	})
	if got := state.CurrentToolLabel(); got != "editing internal/app/config/config.go" {
		t.Fatalf("CurrentToolLabel() = %q, want %q", got, "editing internal/app/config/config.go")
	}
}

func TestCurrentToolLabelFileWritePatchNoPath(t *testing.T) {
	state := newTestState()
	state.SetActiveToolCall(ActiveToolCall{Name: "file.write_patch"})
	if got := state.CurrentToolLabel(); got != "file.write_patch" {
		t.Fatalf("CurrentToolLabel() = %q, want %q", got, "file.write_patch")
	}
}

func TestMessageFinalField(t *testing.T) {
	state := newTestState()
	state.AddMessage(RoleAssistant, "here is the answer", ContentTypeMarkdown)
	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(msgs))
	}
	if msgs[0].Final {
		t.Fatal("Final = true, want false (AddMessage does not set Final)")
	}
}

func TestStateActiveSkillsRaceFree(t *testing.T) {
	state := newTestState()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			state.ActivateSkill("a")
			state.ActivateSkill("b")
		}
	}()

	for i := 0; i < 100; i++ {
		_ = state.ActiveSkills()
		_ = state.HasActiveSkill("a")
	}
	<-done
}

func TestLogThinking(t *testing.T) {
	s := newTestState()
	s.LogThinking(ThinkingEntry{
		Text:      "thinking about tool call",
		Duration:  2 * time.Second,
		StartedAt: time.Now(),
	})
	transcript := s.Transcript()
	if len(transcript) != 1 {
		t.Fatalf("expected 1 transcript item, got %d", len(transcript))
	}
	if transcript[0].Kind != KindThinking {
		t.Errorf("expected KindThinking, got %v", transcript[0].Kind)
	}
	if transcript[0].Thinking == nil {
		t.Fatal("expected non-nil Thinking")
	}
	if transcript[0].Thinking.Text != "thinking about tool call" {
		t.Errorf("expected thinking text, got %q", transcript[0].Thinking.Text)
	}
}

func TestTranscriptMergeOrder(t *testing.T) {
	s := newTestState()

	s.AddMessage(RoleUser, "hello", ContentTypePlain)
	thinkTime := time.Now()

	s.LogThinking(ThinkingEntry{
		Text:      "should I respond?",
		Duration:  1 * time.Second,
		StartedAt: thinkTime,
	})

	s.AddMessage(RoleAssistant, "world", ContentTypePlain)

	transcripts := s.Transcript()
	if len(transcripts) != 3 {
		t.Fatalf("expected 3 transcript items, got %d", len(transcripts))
	}
	if transcripts[0].Kind != KindMessage {
		t.Errorf("expected item 0 to be KindMessage, got %v", transcripts[0].Kind)
	}
	if transcripts[1].Kind != KindThinking {
		t.Errorf("expected item 1 to be KindThinking, got %v", transcripts[1].Kind)
	}
	if transcripts[2].Kind != KindMessage {
		t.Errorf("expected item 2 to be KindMessage, got %v", transcripts[2].Kind)
	}
}

func TestToolBudget(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	if b := s.ToolBudget(); b.Used != 0 || b.Max != 0 {
		t.Fatalf("zero value = %+v, want {0 0}", b)
	}
	s.SetToolBudget(ToolBudget{Used: 5, Max: 16})
	if b := s.ToolBudget(); b.Used != 5 || b.Max != 16 {
		t.Fatalf("ToolBudget() = %+v, want {5 16}", b)
	}
}

func TestAddMessageSalvaged(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.AddMessageSalvaged(RoleAssistant, "best effort answer", ContentTypeMarkdown, "exhausted")
	msgs := s.Messages()
	last := msgs[len(msgs)-1]
	if !last.Final || !last.Salvaged || last.SalvageReason != "exhausted" {
		t.Fatalf("last message = %+v, want Final+Salvaged+reason=exhausted", last)
	}
}

func TestStateScratchpadConfigDefaults(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	if s.scratchpadConfig.MaxEntries != 32 {
		t.Errorf("MaxEntries = %d, want 32", s.scratchpadConfig.MaxEntries)
	}
	if s.scratchpadConfig.MaxTotalTokens != 8000 {
		t.Errorf("MaxTotalTokens = %d, want 8000", s.scratchpadConfig.MaxTotalTokens)
	}
	if s.scratchpadConfig.MaxEntryTokens != 4000 {
		t.Errorf("MaxEntryTokens = %d, want 4000", s.scratchpadConfig.MaxEntryTokens)
	}
	if s.scratchpadConfig.ProjectionMaxTokens != 1000 {
		t.Errorf("ProjectionMaxTokens = %d, want 1000", s.scratchpadConfig.ProjectionMaxTokens)
	}
}

func TestStateScratchpadConfigPreservesExplicitValues(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          16,
		MaxTotalTokens:      4000,
		MaxEntryTokens:      2000,
		ProjectionMaxTokens: 500,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})
	if s.scratchpadConfig.MaxEntries != 16 {
		t.Errorf("MaxEntries = %d, want 16", s.scratchpadConfig.MaxEntries)
	}
	if s.scratchpadConfig.MaxTotalTokens != 4000 {
		t.Errorf("MaxTotalTokens = %d, want 4000", s.scratchpadConfig.MaxTotalTokens)
	}
	if s.scratchpadConfig.MaxEntryTokens != 2000 {
		t.Errorf("MaxEntryTokens = %d, want 2000", s.scratchpadConfig.MaxEntryTokens)
	}
	if s.scratchpadConfig.ProjectionMaxTokens != 500 {
		t.Errorf("ProjectionMaxTokens = %d, want 500", s.scratchpadConfig.ProjectionMaxTokens)
	}
}

func TestStateScratchpadConfigAppliesDefaultsToZeroValues(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})
	if s.scratchpadConfig.MaxEntries != 32 {
		t.Errorf("MaxEntries = %d, want 32", s.scratchpadConfig.MaxEntries)
	}
	if s.scratchpadConfig.MaxTotalTokens != 8000 {
		t.Errorf("MaxTotalTokens = %d, want 8000", s.scratchpadConfig.MaxTotalTokens)
	}
	if s.scratchpadConfig.MaxEntryTokens != 4000 {
		t.Errorf("MaxEntryTokens = %d, want 4000", s.scratchpadConfig.MaxEntryTokens)
	}
	if s.scratchpadConfig.ProjectionMaxTokens != 1000 {
		t.Errorf("ProjectionMaxTokens = %d, want 1000", s.scratchpadConfig.ProjectionMaxTokens)
	}
}

func TestStateScratchpadConfigAccessorReturnsAppliedDefaults(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	cfg := s.ScratchpadConfig()
	if cfg.MaxEntries != 32 {
		t.Errorf("MaxEntries = %d, want 32", cfg.MaxEntries)
	}
	if cfg.MaxTotalTokens != 8000 {
		t.Errorf("MaxTotalTokens = %d, want 8000", cfg.MaxTotalTokens)
	}
	if cfg.MaxEntryTokens != 4000 {
		t.Errorf("MaxEntryTokens = %d, want 4000", cfg.MaxEntryTokens)
	}
	if cfg.ProjectionMaxTokens != 1000 {
		t.Errorf("ProjectionMaxTokens = %d, want 1000", cfg.ProjectionMaxTokens)
	}
}

func TestStateScratchpadConfigAccessorReturnsConfiguredValues(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          16,
		MaxTotalTokens:      4000,
		MaxEntryTokens:      2000,
		ProjectionMaxTokens: 500,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})
	got := s.ScratchpadConfig()
	if got.MaxEntries != 16 {
		t.Errorf("MaxEntries = %d, want 16", got.MaxEntries)
	}
	if got.MaxTotalTokens != 4000 {
		t.Errorf("MaxTotalTokens = %d, want 4000", got.MaxTotalTokens)
	}
	if got.MaxEntryTokens != 2000 {
		t.Errorf("MaxEntryTokens = %d, want 2000", got.MaxEntryTokens)
	}
	if got.ProjectionMaxTokens != 500 {
		t.Errorf("ProjectionMaxTokens = %d, want 500", got.ProjectionMaxTokens)
	}
}

func TestRewindStartsNewBranch(t *testing.T) {
	state := newTestState()
	state.AddMessage(RoleUser, "turn1", ContentTypePlain)
	state.AddMessage(RoleAssistant, "a1", ContentTypeMarkdown)
	state.AddMessage(RoleUser, "turn2", ContentTypePlain)
	state.AddMessage(RoleAssistant, "a2", ContentTypeMarkdown)

	msgs := state.Messages()
	turn2ID := msgs[2].ID
	newLeaf := state.Rewind(turn2ID)
	if newLeaf != msgs[1].ID {
		t.Fatalf("rewind leaf = %d, want a1 id %d", newLeaf, msgs[1].ID)
	}
	state.AddMessage(RoleUser, "turn3", ContentTypePlain)
	state.AddMessage(RoleAssistant, "a3", ContentTypeMarkdown)

	active := state.Messages()
	if len(active) != 4 || active[3].Content != "a3" {
		t.Fatalf("active branch = %+v, want turn1->a1->turn3->a3", active)
	}
	branches := state.Branches()
	if len(branches) != 2 {
		t.Fatalf("branches = %v, want 2", branches)
	}
}

func TestSwitchBranchRestoresOriginalPath(t *testing.T) {
	state := newTestState()
	state.AddMessage(RoleUser, "turn1", ContentTypePlain)
	state.AddMessage(RoleAssistant, "a1", ContentTypeMarkdown)
	state.AddMessage(RoleUser, "turn2", ContentTypePlain)
	state.AddMessage(RoleAssistant, "a2", ContentTypeMarkdown)
	origLeaf := state.LeafID()
	state.Rewind(state.Messages()[2].ID)
	state.AddMessage(RoleUser, "turn3", ContentTypePlain)

	state.SwitchBranch(origLeaf)
	active := state.Messages()
	if len(active) != 4 || active[3].Content != "a2" {
		t.Fatalf("switch back = %+v, want original 4-msg branch", active)
	}
}

func TestPendingQuestionRoundTrip(t *testing.T) {
	s := newTestState()
	if s.PendingQuestion() != nil {
		t.Fatal("expected no pending question initially")
	}
	qs := []Question{{Question: "archive or delete?"}}
	q := &PendingQuestion{Questions: qs, ResponseChan: make(chan []Answer, 1)}
	s.SetPendingQuestion(q)
	got := s.PendingQuestion()
	if got == nil || len(got.Questions) != 1 || got.Questions[0].Question != "archive or delete?" {
		t.Fatalf("PendingQuestion = %+v", got)
	}
	s.SetPendingQuestion(nil)
	if s.PendingQuestion() != nil {
		t.Fatal("expected pending question cleared")
	}
}

func TestStateTodosRoundTrip(t *testing.T) {
	state := newTestState()

	if len(state.Todos()) != 0 {
		t.Fatal("initial Todos() should be empty")
	}

	todos := []db.TodoItem{
		{Content: "read spec", Status: "completed"},
		{Content: "write plan", Status: "in_progress"},
		{Content: "implement", Status: "pending"},
	}
	if err := state.SetTodos(todos); err != nil {
		t.Fatalf("SetTodos error: %v", err)
	}

	got := state.Todos()
	if len(got) != 3 || got[0].Content != "read spec" || got[1].Status != "in_progress" {
		t.Fatalf("Todos() = %+v", got)
	}

	todos[0].Content = "mutated"
	gotAgain := state.Todos()
	if gotAgain[0].Content != "read spec" {
		t.Fatalf("Todos() returned mutable internal slice: %+v", gotAgain)
	}
}

// ── Work gate and shutdown resolution (Task 5) ─────────────────────────

func TestWorkGateWaitsForActiveWork(t *testing.T) {
	s := newTestState()

	// Begin some work, then start a goroutine that waits for it.
	err := s.BeginWork()
	if err != nil {
		t.Fatalf("BeginWork() = %v, want nil before quiesce", err)
	}

	waitReturned := make(chan struct{})
	go func() {
		s.BeginQuiesce()
		_ = s.WaitForWork(context.Background())
		close(waitReturned)
	}()

	// Prove it blocks: the waiter should not return within a short time.
	select {
	case <-waitReturned:
		t.Fatal("WaitForWork returned before EndWork was called")
	case <-time.After(50 * time.Millisecond):
	}

	// End the work and prove the waiter returns.
	s.EndWork()

	select {
	case <-waitReturned:
	case <-time.After(time.Second):
		t.Fatal("WaitForWork did not return after EndWork")
	}
}

func TestWorkGateRejectsAfterQuiesce(t *testing.T) {
	s := newTestState()
	s.BeginQuiesce()
	if !errors.Is(s.BeginWork(), ErrSessionQuiescing) {
		t.Fatalf("BeginWork() after quiesce should return ErrSessionQuiescing")
	}
}

func TestResolvePendingForShutdownReleasesWaiters(t *testing.T) {
	s := newTestState()

	// Install a buffered approval channel.
	approvalCh := make(chan UserApprovalDecision, 1)
	s.SetPendingApproval(&PendingToolCall{
		ID:           "tc1",
		Name:         "shell.run",
		Command:      "echo hi",
		ResponseChan: approvalCh,
	})

	// Install a buffered question channel.
	questionCh := make(chan []Answer, 1)
	s.SetPendingQuestion(&PendingQuestion{
		Questions:    []Question{{Question: "Proceed?"}, {Question: "Really?"}},
		ResponseChan: questionCh,
	})

	// Queue steering messages.
	s.PushSteering("msg1")
	s.PushSteering("msg2")

	s.ResolvePendingForShutdown()

	// Approval should produce a denied decision.
	select {
	case dec := <-approvalCh:
		if dec.Approved {
			t.Fatal("approval should be denied during shutdown")
		}
	default:
		t.Fatal("approval channel was not written to")
	}

	// Question should produce one AnswerUnanswered per question, in order.
	select {
	case answers := <-questionCh:
		if len(answers) != 2 {
			t.Fatalf("answers = %d, want 2", len(answers))
		}
		if answers[0].Answer != AnswerUnanswered {
			t.Fatalf("answers[0].Answer = %q, want %q", answers[0].Answer, AnswerUnanswered)
		}
		if answers[1].Answer != AnswerUnanswered {
			t.Fatalf("answers[1].Answer = %q, want %q", answers[1].Answer, AnswerUnanswered)
		}
	default:
		t.Fatal("question channel was not written to")
	}

	// Pending pointers should be nil.
	if s.PendingApproval() != nil {
		t.Fatal("PendingApproval should be nil after ResolvePendingForShutdown")
	}
	if s.PendingQuestion() != nil {
		t.Fatal("PendingQuestion should be nil after ResolvePendingForShutdown")
	}

	// Steering queue should be empty.
	if q := s.SteeringQueue(); len(q) != 0 {
		t.Fatalf("steering queue = %v, want empty", q)
	}
}

func TestResolvePendingForShutdownNeverBlocks(t *testing.T) {
	s := newTestState()

	// Install UNBUFFERED channels with no receivers — ResolvePending
	// must not block on these sends.
	s.SetPendingApproval(&PendingToolCall{
		ID:           "tc2",
		Name:         "file.read",
		Command:      "cat x",
		ResponseChan: make(chan UserApprovalDecision), // unbuffered
	})
	s.SetPendingQuestion(&PendingQuestion{
		Questions:    []Question{{Question: "Go?"}},
		ResponseChan: make(chan []Answer), // unbuffered
	})
	s.PushSteering("steer")

	done := make(chan struct{})
	go func() {
		s.ResolvePendingForShutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ResolvePendingForShutdown blocked on unbuffered channels")
	}

	// State must still be cleared even when sends were dropped.
	if s.PendingApproval() != nil {
		t.Fatal("PendingApproval should be nil after ResolvePendingForShutdown (unbuffered)")
	}
	if s.PendingQuestion() != nil {
		t.Fatal("PendingQuestion should be nil after ResolvePendingForShutdown (unbuffered)")
	}
	if q := s.SteeringQueue(); len(q) != 0 {
		t.Fatalf("steering queue = %v, want empty after ResolvePendingForShutdown", q)
	}
}

// ── end Task 5 tests ────────────────────────────────────────────────────

func TestSteeringQueuePushDrainClear(t *testing.T) {
	state := newTestState()
	state.PushSteering("also update the README")
	state.PushSteering("and add a test")
	got := state.SteeringQueue()
	if len(got) != 2 || got[0] != "also update the README" {
		t.Fatalf("SteeringQueue = %v", got)
	}
	drained := state.DrainSteering()
	if len(drained) != 2 {
		t.Fatalf("DrainSteering = %v", drained)
	}
	if len(state.SteeringQueue()) != 0 {
		t.Fatal("queue not empty after drain")
	}
	state.PushSteering("x")
	state.ClearSteering()
	if len(state.SteeringQueue()) != 0 {
		t.Fatal("queue not empty after clear")
	}
}

// TestSubagentReportQueueSeparateFromSteering guards C1: a background
// subagent's completion report lives in its own queue, so ClearSteering
// (turn-cancel, Ctrl+X) and PopSteering (blank-Enter follow-up) must never
// drop it.
func TestSubagentReportQueueSeparateFromSteering(t *testing.T) {
	state := newTestState()
	state.PushSubagentReport("[subagent 1 finished] the report")
	state.PushSteering("human steering")

	// ClearSteering drops only the human steering, not the report.
	state.ClearSteering()
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("steering queue = %v, want empty after clear", got)
	}
	if got := state.SubagentReports(); len(got) != 1 {
		t.Fatalf("subagent report queue = %v, want the report preserved", got)
	}

	// PopSteering (blank-Enter follow-up) also leaves the report intact.
	state.PushSteering("another steer")
	if _, ok := state.PopSteering(); !ok {
		t.Fatal("PopSteering returned ok=false")
	}
	if got := state.SubagentReports(); len(got) != 1 {
		t.Fatalf("subagent report queue = %v, want the report preserved after PopSteering", got)
	}

	// DrainSubagentReports returns and clears only the report queue.
	drained := state.DrainSubagentReports()
	if len(drained) != 1 || drained[0] != "[subagent 1 finished] the report" {
		t.Fatalf("DrainSubagentReports = %v, want the report", drained)
	}
	if got := state.SubagentReports(); len(got) != 0 {
		t.Fatalf("subagent report queue = %v, want empty after drain", got)
	}
}

func TestSteeringQueuePublishesQueueLenAfterDrainPopAndClear(t *testing.T) {
	state := newTestState()
	broker := pubsub.NewBroker[SteeringEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	state.SetSteeringBroker(broker)

	state.PushSteering("one")
	expectSteeringQueueLen(t, ch, 1)
	state.PushSteering("two")
	expectSteeringQueueLen(t, ch, 2)
	if drained := state.DrainSteering(); len(drained) != 2 {
		t.Fatalf("DrainSteering = %v, want two messages", drained)
	}
	expectSteeringQueueLen(t, ch, 0)

	state.PushSteering("three")
	expectSteeringQueueLen(t, ch, 1)
	if _, ok := state.PopSteering(); !ok {
		t.Fatal("PopSteering returned ok=false")
	}
	expectSteeringQueueLen(t, ch, 0)

	state.PushSteering("four")
	expectSteeringQueueLen(t, ch, 1)
	state.ClearSteering()
	expectSteeringQueueLen(t, ch, 0)
}

func expectSteeringQueueLen(t *testing.T, ch <-chan pubsub.Event[SteeringEvent], want int) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Payload.QueueLen != want {
			t.Fatalf("QueueLen = %d, want %d (event=%+v)", ev.Payload.QueueLen, want, ev.Payload)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for steering QueueLen %d", want)
	}
}

// ── Task 3: LoadError ──────────────────────────────────────────────────

func TestStateLoadErrorReportsColdLoadFailure(t *testing.T) {
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}

	sessionID := "loaderr-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "test", time.Now().UTC()); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Close the DB so loadFromDB will fail.
	dbConn.Close()

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	state := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	if err := state.LoadError(); err == nil {
		t.Fatal("LoadError() = nil, want non-nil after closed DB load")
	}
}

// ── end Task 3 tests ────────────────────────────────────────────────────

func TestStateSetLayersRoundTrip(t *testing.T) {
	s := New(config.Default(), "/tmp/repo", time.Unix(0, 0), Persistence{})
	want := config.Layers{
		Default: config.Default(),
		User:    config.Default(),
		Merged:  config.Default(),
	}
	s.SetLayers(want)
	got := s.Layers()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Layers() = %+v, want %+v", got, want)
	}
}

func TestStateLayersZeroValueByDefault(t *testing.T) {
	s := New(config.Default(), "/tmp/repo", time.Unix(0, 0), Persistence{})
	got := s.Layers()
	// Default Layers{}.Merged is the zero Config — just check provenance is
	// fully "default" for any path, since Merged == User == Default.
	if p := got.ProvenanceOf("anything.here"); p.SetBy != config.LayerDefault {
		t.Errorf("fresh state Layers Provenance = %v, want default", p)
	}
}

func TestBrowserInfoDefault(t *testing.T) {
	state := newTestState()
	bi := state.BrowserInfo()
	if bi.SessionOpen {
		t.Fatal("new state should have SessionOpen=false")
	}
	if bi.Active {
		t.Fatal("new state should have Active=false")
	}
}

func TestSetBrowserInfo(t *testing.T) {
	state := newTestState()
	info := BrowserInfo{
		SessionOpen: true,
		Active:      true,
		ToolName:    "browser.navigate",
		URL:         "https://example.com/docs",
		Title:       "Example Docs",
		Mode:        "standalone",
	}
	state.SetBrowserInfo(info)
	got := state.BrowserInfo()
	if !got.SessionOpen {
		t.Error("SessionOpen not set")
	}
	if got.URL != "https://example.com/docs" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.Title != "Example Docs" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Mode != "standalone" {
		t.Errorf("Mode = %q", got.Mode)
	}
	if !got.Active {
		t.Error("Active not set")
	}
	if got.ToolName != "browser.navigate" {
		t.Errorf("ToolName = %q", got.ToolName)
	}
}

func TestSteeringQueueIsCopy(t *testing.T) {
	state := newTestState()
	state.PushSteering("a")
	got := state.SteeringQueue()
	got[0] = "mutated"
	if state.SteeringQueue()[0] != "a" {
		t.Fatal("SteeringQueue did not return a copy")
	}
}

func TestStatePublishesMessageEvent(t *testing.T) {
	state := newTestState()
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	state.SetEventBroker(broker)
	state.AddMessage(RoleUser, "hello", ContentTypePlain)
	select {
	case ev := <-ch:
		if ev.Type != EventMessageAdded || ev.Payload.Message == nil || ev.Payload.Message.Content != "hello" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message event")
	}
}

func TestStatePublishesApprovalEvent(t *testing.T) {
	state := newTestState()
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	state.SetEventBroker(broker)
	state.SetPendingApproval(&PendingToolCall{Name: "shell.run"})
	select {
	case ev := <-ch:
		if ev.Type != EventPendingApprovalChanged || ev.Payload.PendingApproval == nil {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for approval event")
	}
}

// TestAddMessagePublishesPersistedID verifies that the EventMessageAdded
// payload carries the final persisted id — not a transient in-memory id
// that is re-keyed away milliseconds later (ACP forwards this id).
func TestAddMessagePublishesPersistedID(t *testing.T) {
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
		t.Fatalf("project: %v", err)
	}
	if err := dbConn.CreateSession("evt-sess", projectID, "evt", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Advance the DB id sequence via another session so the transient
	// in-memory id (starts at 1) and the next persisted id differ — in a
	// fresh DB both are 1 and the bug is invisible.
	if err := dbConn.CreateSession("other-sess", projectID, "other", time.Now().UTC()); err != nil {
		t.Fatalf("create other session: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := dbConn.SaveMessage("other-sess", "user", "seed", "plain", time.Now().UTC(), "", 0, false, 0); err != nil {
			t.Fatalf("seed save: %v", err)
		}
	}

	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: "evt-sess", Logger: logger})

	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	state.SetEventBroker(broker)

	state.AddMessage(RoleUser, "hello", ContentTypePlain)

	var evtID int64
	select {
	case ev := <-ch:
		if ev.Type != EventMessageAdded || ev.Payload.Message == nil {
			t.Fatalf("event = %+v", ev)
		}
		evtID = ev.Payload.Message.ID
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message event")
	}

	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].ID != evtID {
		t.Fatalf("event carried id %d but tree holds id %d — transient id leaked to subscribers", evtID, msgs[0].ID)
	}
	if state.LeafID() != evtID {
		t.Fatalf("LeafID = %d, want %d", state.LeafID(), evtID)
	}
}

// TestLoadFromDBDropsTranslationMap: dbIDToImID is only meaningful during
// loadFromDB; it must not be kept for the session's lifetime.
func TestLoadFromDBDropsTranslationMap(t *testing.T) {
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
		t.Fatalf("project: %v", err)
	}
	if err := dbConn.CreateSession("nilmap-sess", projectID, "nilmap", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: "nilmap-sess", Logger: logger})
	first.AddMessage(RoleUser, "hello", ContentTypePlain)

	second := New(config.Default(), "/repo", time.Unix(200, 0), Persistence{DB: dbConn, SessionID: "nilmap-sess", Logger: logger})
	if len(second.Messages()) != 1 {
		t.Fatalf("cold-start messages = %d, want 1", len(second.Messages()))
	}
	if second.dbIDToImID != nil {
		t.Fatal("dbIDToImID should be nil after loadFromDB completes")
	}
}

func TestBeginWorkAndBeginQuiesceConcurrent(t *testing.T) {
	s := newTestState()
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.BeginWork()
			if err := s.BeginWork(); err == nil {
				s.EndWork()
			}
		}()
		go func() {
			defer wg.Done()
			s.BeginQuiesce()
		}()
	}
	wg.Wait()
	// After both gates flip, BeginWork must always return an error.
	if err := s.BeginWork(); !errors.Is(err, ErrSessionQuiescing) {
		t.Fatalf("expected ErrSessionQuiescing after BeginQuiesce, got %v", err)
	}
}

func TestUnansweredAnswers(t *testing.T) {
	qs := []Question{{Question: "q1"}, {Question: "q2"}}
	got := UnansweredAnswers(qs)
	if len(got) != 2 || got[0].Question != "q1" || got[1].Answer != AnswerUnanswered {
		t.Fatalf("UnansweredAnswers = %+v", got)
	}
}

func TestAppendActiveToolCallOutput(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetActiveToolCall(ActiveToolCall{Name: "shell.run", Args: "echo hi", StartedAt: time.Now()})
	s.AppendActiveToolCallOutput("hello")
	s.AppendActiveToolCallOutput(" world")
	atc, _ := s.ActiveToolCall()
	if atc.Output != "hello world" {
		t.Fatalf("Output = %q, want %q", atc.Output, "hello world")
	}
}

// ── Task 7: SDDGate ─────────────────────────────────────────────────────

func TestStateSDDGate(t *testing.T) {
	s := newTestState()
	if g := s.SDDGate(); g.Question != "" {
		t.Fatalf("initial gate should be empty, got %+v", g)
	}
	s.SetSDDGate(SDDGate{TaskN: 1, Question: "spec.md needs approval"})
	g := s.SDDGate()
	if g.TaskN != 1 || g.Question != "spec.md needs approval" {
		t.Fatalf("gate = %+v", g)
	}
	s.ClearSDDGate()
	if g := s.SDDGate(); g.Question != "" {
		t.Fatalf("cleared gate should be empty, got %+v", g)
	}
}

// TestLoggerNeverNil guards the call sites that treat logging as always
// available (agent/runner.go, agent/execute.go): a State without a logger
// used to hand back nil and panic on first use.
func TestLoggerNeverNil(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	if s.Logger() == nil {
		t.Fatal("Logger() = nil; call sites dereference it unconditionally")
	}
	s.Logger().Warn("must not panic")
}

// TestActiveSkillsIsSorted pins A-01: the returned slice renders into the
// system prompt, which is the provider's cache prefix. Map order there meant
// the prompt bytes could change between rebuilds of an identical state,
// silently missing the prompt cache.
func TestActiveSkillsIsSorted(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	for _, name := range []string{"zebra", "alpha", "mike", "bravo"} {
		s.ActivateSkill(name)
	}

	want := []string{"alpha", "bravo", "mike", "zebra"}
	// Repeat: a single pass can match by luck under Go's randomized map order.
	for i := 0; i < 50; i++ {
		got := s.ActiveSkills()
		if !slices.Equal(got, want) {
			t.Fatalf("iteration %d: ActiveSkills() = %v, want %v", i, got, want)
		}
	}
}

// TestMessageTreeSurvivesReallocationAndRebuild pins A-05: msgByID holds
// values, so growth of s.messages and the wholesale slice replacement done by
// rebuildActiveBranch (Rewind/SwitchBranch) cannot desynchronise the tree from
// the active branch.
func TestMessageTreeSurvivesReallocationAndRebuild(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})

	// Enough appends to force several reallocations of the backing array.
	for i := 0; i < 64; i++ {
		s.AddMessage(RoleUser, fmt.Sprintf("msg-%d", i), ContentTypePlain)
	}
	msgs := s.Messages()
	if len(msgs) != 64 {
		t.Fatalf("len(Messages()) = %d, want 64", len(msgs))
	}
	forkFrom := msgs[10].ID

	// Rewind replaces s.messages with a freshly allocated slice.
	s.Rewind(forkFrom)
	s.AddMessage(RoleUser, "branch-b", ContentTypePlain)

	branches := s.Branches()
	if len(branches) != 2 {
		t.Fatalf("expected 2 branch tips after fork, got %d", len(branches))
	}

	// Switching back must reconstruct the original branch verbatim from the
	// tree, including messages recorded before any reallocation.
	s.SwitchBranch(msgs[63].ID)
	got := s.Messages()
	if len(got) != 64 {
		t.Fatalf("after SwitchBranch len = %d, want 64", len(got))
	}
	for i, m := range got {
		if want := fmt.Sprintf("msg-%d", i); m.Content != want {
			t.Fatalf("message %d = %q, want %q", i, m.Content, want)
		}
	}
}

func TestScratchpadSetAndGet(t *testing.T) {
	s := newTestState()
	if err := s.SetScratchpadEntry("key1", "content1", "text"); err != nil {
		t.Fatalf("SetScratchpadEntry: %v", err)
	}
	entry, ok := s.ScratchpadEntry("key1")
	if !ok {
		t.Fatal("entry not found")
	}
	if entry.Content != "content1" || entry.Format != "text" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestScratchpadSetOverwrites(t *testing.T) {
	s := newTestState()
	s.SetScratchpadEntry("k", "old", "text")
	s.SetScratchpadEntry("k", "new", "json")
	entry, ok := s.ScratchpadEntry("k")
	if !ok {
		t.Fatal("entry not found")
	}
	if entry.Content != "new" || entry.Format != "json" {
		t.Fatalf("entry = %+v, want new/json", entry)
	}
}

func TestScratchpadEntryNotFound(t *testing.T) {
	s := newTestState()
	_, ok := s.ScratchpadEntry("missing")
	if ok {
		t.Fatal("should not find missing key")
	}
}

func TestScratchpadDeleteIdempotent(t *testing.T) {
	s := newTestState()
	s.SetScratchpadEntry("k", "v", "text")
	if err := s.DeleteScratchpadEntry("k"); err != nil {
		t.Fatalf("DeleteScratchpadEntry: %v", err)
	}
	_, ok := s.ScratchpadEntry("k")
	if ok {
		t.Fatal("entry should have been deleted")
	}
	// Deleting again should not error.
	if err := s.DeleteScratchpadEntry("k"); err != nil {
		t.Fatalf("idempotent delete should not error: %v", err)
	}
}

func TestScratchpadReturnsDefensiveCopy(t *testing.T) {
	s := newTestState()
	s.SetScratchpadEntry("k", "v", "text")
	entries := s.Scratchpad()
	entries[0].Content = "mutated"
	got := s.Scratchpad()
	if got[0].Content != "v" {
		t.Fatalf("Scratchpad() did not return a defensive copy: %q", got[0].Content)
	}
}

func TestScratchpadSetRejectsEmptyKey(t *testing.T) {
	s := newTestState()
	err := s.SetScratchpadEntry("", "content", "text")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestScratchpadMultipleEntries(t *testing.T) {
	s := newTestState()
	s.SetScratchpadEntry("a", "alpha", "text")
	s.SetScratchpadEntry("b", "beta", "json")
	entries := s.Scratchpad()
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
}

// Entries written under a frozen session clock share an Updated timestamp,
// and they come out of a map, so without a key tie-break the order is
// random and callers that truncate (the context-pack projection) drop a
// different entry on each run.
func TestScratchpadEqualTimestampsOrderByKey(t *testing.T) {
	frozen := time.Unix(100, 0)
	for i := 0; i < 50; i++ {
		s := New(config.Default(), "/repo", frozen, Persistence{}, WithClock(func() time.Time { return frozen }))
		for _, k := range []string{"delta", "bravo", "alpha", "charlie"} {
			if err := s.SetScratchpadEntry(k, "content for "+k, "text"); err != nil {
				t.Fatalf("SetScratchpadEntry(%q): %v", k, err)
			}
		}
		var got []string
		for _, e := range s.Scratchpad() {
			got = append(got, e.Key)
		}
		want := []string{"alpha", "bravo", "charlie", "delta"}
		if !slices.Equal(got, want) {
			t.Fatalf("Scratchpad() order = %v, want %v", got, want)
		}
	}
}

func TestScratchpadSetRejectsEmptyContent(t *testing.T) {
	s := newTestState()
	err := s.SetScratchpadEntry("key", "", "text")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	err = s.SetScratchpadEntry("key", "   ", "text")
	if err == nil {
		t.Fatal("expected error for whitespace-only content")
	}
}

func TestScratchpadSetRejectsOversizedEntry(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          32,
		MaxTotalTokens:      8000,
		MaxEntryTokens:      10,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	// EstimateTokens is ceil(runes/4). 40 chars => 10 tokens, equal to max; 41 chars => 11.
	longContent := strings.Repeat("a", 41)
	err := s.SetScratchpadEntry("big", longContent, "text")
	if err == nil {
		t.Fatal("expected error for oversized entry")
	}
}

func TestScratchpadEvictionByMaxEntries(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          2,
		MaxTotalTokens:      10000,
		MaxEntryTokens:      10000,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	s.SetScratchpadEntry("first", "one", "text")
	time.Sleep(10 * time.Millisecond)
	s.SetScratchpadEntry("second", "two", "text")
	time.Sleep(10 * time.Millisecond)
	s.SetScratchpadEntry("third", "three", "text")

	entries := s.Scratchpad()
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2 after eviction", len(entries))
	}
	// Newest-first order.
	if entries[0].Key != "third" || entries[1].Key != "second" {
		t.Fatalf("entries = %+v, want newest two (third, second)", entries)
	}
	if _, ok := s.ScratchpadEntry("first"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
}

func TestScratchpadEvictionByMaxTotalTokens(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          100,
		MaxTotalTokens:      10,
		MaxEntryTokens:      100,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	// 24 ASCII chars => (24+3)/4 = 6 tokens per entry.
	// Two entries = 12 tokens, exceeding MaxTotalTokens=10, so the oldest is evicted.
	content := strings.Repeat("x", 24)
	s.SetScratchpadEntry("first", content, "text")
	time.Sleep(10 * time.Millisecond)
	s.SetScratchpadEntry("second", content, "text")

	entries := s.Scratchpad()
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1 after token-budget eviction", len(entries))
	}
	if entries[0].Key != "second" {
		t.Fatalf("remaining entry = %q, want second", entries[0].Key)
	}
}

func TestScratchpadEvictionOldestFirst(t *testing.T) {
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          2,
		MaxTotalTokens:      10000,
		MaxEntryTokens:      10000,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{})

	s.SetScratchpadEntry("old", "old content", "text")
	time.Sleep(10 * time.Millisecond)
	s.SetScratchpadEntry("mid", "mid content", "text")
	time.Sleep(10 * time.Millisecond)
	s.SetScratchpadEntry("new", "new content", "text")

	// Update the middle entry so it becomes newest; the originally oldest should still go first.
	time.Sleep(10 * time.Millisecond)
	s.SetScratchpadEntry("mid", "mid content updated", "text")

	entries := s.Scratchpad()
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if entries[0].Key != "mid" {
		t.Fatalf("newest entry = %q, want mid", entries[0].Key)
	}
	if entries[1].Key != "new" {
		t.Fatalf("second entry = %q, want new", entries[1].Key)
	}
	if _, ok := s.ScratchpadEntry("old"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
}

func TestScratchpadColdLoadPopulatesMap(t *testing.T) {
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
	sessionID := "scratchpad-cold-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "cold", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	// Add a message so loadFromDB has a leaf and proceeds to load the scratchpad.
	first.AddMessage(RoleUser, "hello", ContentTypePlain)
	if err := first.SetScratchpadEntry("a", "alpha", "text"); err != nil {
		t.Fatalf("set a: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := first.SetScratchpadEntry("b", "beta", "json"); err != nil {
		t.Fatalf("set b: %v", err)
	}

	second := New(cfg, "/repo", time.Unix(200, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	entries := second.Scratchpad()
	if len(entries) != 2 {
		t.Fatalf("cold-load entries = %d, want 2", len(entries))
	}
	// LoadScratchpad orders newest first; "b" was set second.
	if entries[0].Key != "b" || entries[0].Content != "beta" || entries[0].Format != "json" {
		t.Fatalf("entry[0] = %+v, want b/beta/json", entries[0])
	}
	if entries[1].Key != "a" || entries[1].Content != "alpha" || entries[1].Format != "text" {
		t.Fatalf("entry[1] = %+v, want a/alpha/text", entries[1])
	}
}

func TestScratchpadPersistenceDeletesEvictedEntries(t *testing.T) {
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
	sessionID := "scratchpad-evict-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "evict", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          1,
		MaxTotalTokens:      10000,
		MaxEntryTokens:      10000,
		ProjectionMaxTokens: 1000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	if err := s.SetScratchpadEntry("old", "old content", "text"); err != nil {
		t.Fatalf("set old: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := s.SetScratchpadEntry("new", "new content", "text"); err != nil {
		t.Fatalf("set new: %v", err)
	}

	// The DB should only contain the newest entry.
	loaded, err := dbConn.LoadScratchpad(sessionID)
	if err != nil {
		t.Fatalf("load scratchpad: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Key != "new" {
		t.Fatalf("db keys = %+v, want [new]", loaded)
	}
}

func TestScratchpadSelfEvictedNewEntryNotPersisted(t *testing.T) {
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
	sessionID := "scratchpad-self-evict-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "self-evict", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          100,
		MaxTotalTokens:      5,
		MaxEntryTokens:      100,
		ProjectionMaxTokens: 1000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})

	// 24 ASCII chars => 6 tokens. This is under MaxEntryTokens but exceeds
	// MaxTotalTokens, so the entry is added and then immediately self-evicted.
	if err := s.SetScratchpadEntry("big", strings.Repeat("x", 24), "text"); err != nil {
		t.Fatalf("set big: %v", err)
	}

	if _, ok := s.ScratchpadEntry("big"); ok {
		t.Fatal("self-evicted entry should not be in memory")
	}

	loaded, err := dbConn.LoadScratchpad(sessionID)
	if err != nil {
		t.Fatalf("load scratchpad: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("db rows = %d, want 0 for self-evicted entry", len(loaded))
	}
}

func TestScratchpadColdLoadWithoutMessages(t *testing.T) {
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
	sessionID := "scratchpad-no-msgs-sess"
	if err := dbConn.CreateSession(sessionID, projectID, "no-msgs", time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first := New(cfg, "/repo", time.Unix(100, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	if err := first.SetScratchpadEntry("x", "chi", "text"); err != nil {
		t.Fatalf("set x: %v", err)
	}

	// Second State loads the same session without any persisted messages.
	second := New(cfg, "/repo", time.Unix(200, 0), Persistence{DB: dbConn, SessionID: sessionID, Logger: logger})
	entries := second.Scratchpad()
	if len(entries) != 1 {
		t.Fatalf("cold-load entries = %d, want 1", len(entries))
	}
	if entries[0].Key != "x" || entries[0].Content != "chi" {
		t.Fatalf("entry = %+v, want x/chi", entries[0])
	}
}

// testLogHandler captures slog Records for assertions in tests.
type testLogHandler struct {
	records []slog.Record
}

func (h *testLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *testLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *testLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *testLogHandler) WithGroup(name string) slog.Handler       { return h }

// recordAttrs collects a slog.Record's attributes into a map.
func recordAttrs(r slog.Record) map[string]any {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	return attrs
}

func TestScratchpadDebugLogs(t *testing.T) {
	handler := &testLogHandler{}
	logger := slog.New(handler)
	cfg := config.Default()
	cfg.Scratchpad = config.ScratchpadConfig{
		MaxEntries:          1,
		MaxTotalTokens:      10000,
		MaxEntryTokens:      10000,
		ProjectionMaxTokens: 1000,
	}
	s := New(cfg, "/repo", time.Unix(100, 0), Persistence{Logger: logger})

	if err := s.SetScratchpadEntry("old", "old content", "text"); err != nil {
		t.Fatalf("set old: %v", err)
	}
	if err := s.SetScratchpadEntry("new", "new content", "text"); err != nil {
		t.Fatalf("set new: %v", err)
	}

	_, ok := s.ScratchpadEntry("new")
	if !ok {
		t.Fatal("expected to find new entry")
	}
	_, ok = s.ScratchpadEntry("missing")
	if ok {
		t.Fatal("expected missing entry to not be found")
	}
	if err := s.DeleteScratchpadEntry("new"); err != nil {
		t.Fatalf("delete new: %v", err)
	}
	if err := s.DeleteScratchpadEntry("missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}

	var writes, reads, deletes, evictions int
	for _, r := range handler.records {
		attrs := recordAttrs(r)
		switch r.Message {
		case "scratchpad write":
			writes++
			key, _ := attrs["key"].(string)
			tokens, _ := attrs["tokens"].(int64)
			totalEntries, _ := attrs["total_entries"].(int64)
			var wantTokens int64
			switch key {
			case "old":
				wantTokens = int64(contextpack.EstimateTokens("old content"))
			case "new":
				wantTokens = int64(contextpack.EstimateTokens("new content"))
			default:
				t.Errorf("write log key = %q, want old or new", key)
			}
			if tokens != wantTokens {
				t.Errorf("write log tokens for %s = %d, want %d", key, tokens, wantTokens)
			}
			if totalEntries != 1 {
				t.Errorf("write log total_entries for %s = %d, want 1", key, totalEntries)
			}
		case "scratchpad read":
			reads++
			key, _ := attrs["key"].(string)
			hit, _ := attrs["hit"].(bool)
			switch key {
			case "new":
				if !hit {
					t.Errorf("read log hit for %s = %v, want true", key, hit)
				}
			case "missing":
				if hit {
					t.Errorf("read log hit for %s = %v, want false", key, hit)
				}
			default:
				t.Errorf("read log key = %q, want new or missing", key)
			}
		case "scratchpad delete":
			deletes++
			if key, _ := attrs["key"].(string); key != "new" {
				t.Errorf("delete log key = %q, want new", key)
			}
		case "scratchpad eviction":
			evictions++
			if key, _ := attrs["key"].(string); key != "old" {
				t.Errorf("eviction log key = %q, want old", key)
			}
		}
	}
	if writes != 2 {
		t.Fatalf("got %d scratchpad write logs, want 2", writes)
	}
	if reads != 2 {
		t.Fatalf("got %d scratchpad read logs, want 2", reads)
	}
	if deletes != 1 {
		t.Fatalf("got %d scratchpad delete logs, want 1", deletes)
	}
	if evictions != 1 {
		t.Fatalf("got %d scratchpad eviction logs, want 1", evictions)
	}
}

// TestAddMessageFinalWithUsageStoresUsage: the export needs per-turn token
// usage on the final assistant message (session postmortem 2026-08-06,
// finding 3 — the .usage template block was dead because nothing populated it).
func TestAddMessageFinalWithUsageStoresUsage(t *testing.T) {
	state := newTestState()
	state.AddMessageFinalWithUsage(RoleAssistant, "answer", ContentTypeMarkdown, 5, "12k prompt + 3k completion tokens")

	messages := state.Messages()
	if len(messages) != 1 {
		t.Fatalf("len(Messages()) = %d, want 1", len(messages))
	}
	if messages[0].ToolCallCount != 5 {
		t.Fatalf("ToolCallCount = %d, want 5", messages[0].ToolCallCount)
	}
	if messages[0].Usage != "12k prompt + 3k completion tokens" {
		t.Fatalf("Usage = %q, want %q", messages[0].Usage, "12k prompt + 3k completion tokens")
	}
}

func TestSDDGateCarriesTaskContext(t *testing.T) {
	s := newTestState()
	s.SetSDDGate(SDDGate{
		TaskN:     3,
		TaskTitle: "Add the retry helper",
		Question:  "Should this reuse the existing backoff in internal/retry?",
		Report:    "STATUS: NEEDS_CONTEXT\nQUESTION: Should this reuse...",
	})

	g := s.SDDGate()
	if g.TaskTitle != "Add the retry helper" {
		t.Errorf("TaskTitle = %q, want the asking task's title", g.TaskTitle)
	}
	if g.Report == "" {
		t.Error("Report must carry the implementer's report so the user can see why")
	}
	if g.TaskN != 3 || g.Question == "" {
		t.Errorf("pre-existing fields regressed: %#v", g)
	}
}

func TestClearSDDGateClearsContextToo(t *testing.T) {
	s := newTestState()
	s.SetSDDGate(SDDGate{TaskN: 1, Question: "q", TaskTitle: "t", Report: "r"})
	s.ClearSDDGate()
	if g := s.SDDGate(); g.TaskTitle != "" || g.Report != "" || g.Question != "" {
		t.Errorf("ClearSDDGate left residue: %#v", g)
	}
}

func TestToolCacheEvictsOldestByCount(t *testing.T) {
	s := newTestState()
	for i := 0; i < maxToolCacheEntries+1; i++ {
		s.SetTurnToolResult("file.read", []byte(fmt.Sprintf(`{"n":%d}`, i)),
			registry.ToolResult{Content: "x"})
	}
	if _, ok := s.GetTurnToolResult("file.read", []byte(`{"n":0}`)); ok {
		t.Fatal("oldest entry survived eviction")
	}
	if _, ok := s.GetTurnToolResult("file.read", []byte(fmt.Sprintf(`{"n":%d}`, maxToolCacheEntries))); !ok {
		t.Fatal("newest entry was evicted")
	}
}

func TestToolCacheEvictsByBytes(t *testing.T) {
	s := newTestState()
	big := strings.Repeat("x", maxToolCacheBytes/2+1)
	s.SetTurnToolResult("file.read", []byte(`{"n":1}`), registry.ToolResult{Content: big})
	s.SetTurnToolResult("file.read", []byte(`{"n":2}`), registry.ToolResult{Content: big})
	s.SetTurnToolResult("file.read", []byte(`{"n":3}`), registry.ToolResult{Content: big})
	if _, ok := s.GetTurnToolResult("file.read", []byte(`{"n":1}`)); ok {
		t.Fatal("entry 1 should have been evicted by the byte bound")
	}
	if _, ok := s.GetTurnToolResult("file.read", []byte(`{"n":3}`)); !ok {
		t.Fatal("newest entry was evicted")
	}
}

func TestToolCacheOverwriteDoesNotDoubleCount(t *testing.T) {
	s := newTestState()
	body := strings.Repeat("x", 1000)
	for i := 0; i < 10; i++ {
		s.SetTurnToolResult("file.read", []byte(`{"p":"a"}`), registry.ToolResult{Content: body})
	}
	if _, ok := s.GetTurnToolResult("file.read", []byte(`{"p":"a"}`)); !ok {
		t.Fatal("repeatedly overwritten entry was evicted")
	}
}

func TestClearToolCache(t *testing.T) {
	s := newTestState()
	s.SetTurnToolResult("file.read", []byte(`{}`), registry.ToolResult{Content: "x"})
	s.ClearToolCache()
	if _, ok := s.GetTurnToolResult("file.read", []byte(`{}`)); ok {
		t.Fatal("entry survived ClearToolCache")
	}
}

func TestUpdateContextPackAppliesUnderLock(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	s.UpdateContextPack(func(p contextpack.Pack) contextpack.Pack {
		p.Sections = append(p.Sections, contextpack.Section{Kind: contextpack.SectionRepoCard, Content: "card"})
		return p
	})
	s.UpdateContextPack(func(p contextpack.Pack) contextpack.Pack {
		p.Sections = append(p.Sections, contextpack.Section{Kind: contextpack.SectionPlan, Content: "plan"})
		return p
	})
	pack := s.ContextPack()
	if len(pack.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(pack.Sections))
	}
}

func TestUpdateContextPackConcurrentWritersLoseNothing(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				s.UpdateContextPack(func(p contextpack.Pack) contextpack.Pack {
					p.Sections = append(p.Sections, contextpack.Section{Content: "x"})
					return p
				})
			}
		}()
	}
	wg.Wait()
	if got := len(s.ContextPack().Sections); got != 200 {
		t.Fatalf("sections = %d, want 200 (lost update under concurrency)", got)
	}
}

// TestFinishSubagentReleasesOldChildStates guards M-3: completed subagent
// Child State pointers beyond maxRetainedChildStates are nilled so their
// full State objects can be garbage collected.
func TestFinishSubagentReleasesOldChildStates(t *testing.T) {
	state := newTestState()
	// Register and finish maxRetainedChildStates+5 subagents.
	total := maxRetainedChildStates + 5
	for i := 0; i < total; i++ {
		child := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
		view := state.RegisterSubagent(fmt.Sprintf("child-%d", i), child)
		state.FinishSubagent(view.ID, fmt.Sprintf("done-%d", i), nil)
	}
	views := state.Subagents()
	if len(views) != total {
		t.Fatalf("subagents = %d, want %d", len(views), total)
	}
	// The most recent maxRetainedChildStates completed children should
	// still have their Child pointer; older ones should be nil.
	var withChild, withoutChild int
	for _, v := range views {
		if v.Child != nil {
			withChild++
		} else {
			withoutChild++
		}
	}
	if withChild > maxRetainedChildStates {
		t.Fatalf("retained Child pointers = %d, want at most %d", withChild, maxRetainedChildStates)
	}
	if withoutChild != 5 {
		t.Fatalf("released Child pointers = %d, want 5", withoutChild)
	}
}

// TestSubagentActivityTailCapsReasoningBuffer guards M-4: the reasoning
// tail is capped to maxReasoningTailBytes so a very long reasoning buffer
// does not get fully copied on every call.
func TestSubagentActivityTailCapsReasoningBuffer(t *testing.T) {
	state := newTestState()
	// Build a reasoning buffer well over the cap.
	line := strings.Repeat("a", 100)
	var huge strings.Builder
	for i := 0; i < 200; i++ { // 200 lines * 100 bytes = 20KB > 8KB cap
		huge.WriteString(line)
		huge.WriteString("\n")
	}
	state.BeginStreaming()
	state.AppendThinking(huge.String())

	// Request 5 tail lines — should return 5, not scan the whole buffer.
	tail := state.SubagentActivityTail(5)
	if len(tail) != 5 {
		t.Fatalf("tail = %d lines, want 5", len(tail))
	}
	for _, l := range tail {
		if len(l) != 100 {
			t.Fatalf("tail line length = %d, want 100 (full line from trailing portion)", len(l))
		}
	}
}

// TestShutdownCancelsRunningSubagentsAndClearsReports guards M-5: on
// Shutdown, running subagents are cancelled and the report queue is
// cleared so late reports don't end up in a garbage transcript.
func TestShutdownCancelsRunningSubagentsAndClearsReports(t *testing.T) {
	state := newTestState()
	cancelled := make(chan struct{})
	childState := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{})
	view := state.RegisterSubagent("running child", childState)
	state.SetSubagentCancel(view.ID, func() {
		close(cancelled)
	})
	state.PushSubagentReport("[subagent 1 finished] stale report")

	state.Shutdown()

	// The report queue should be cleared.
	if got := state.SubagentReports(); len(got) != 0 {
		t.Fatalf("report queue after shutdown = %v, want empty", got)
	}
	// The cancel function should have been called.
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("running subagent was not cancelled on shutdown")
	}
}

// StartedAt drives the elapsed-time display, so an args update must leave it
// — and every other field — alone. Replacing the struct would silently
// restart the timer each time the count ticks down.
func TestSetActiveToolCallArgsPreservesOtherFields(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	started := time.Now().Add(-3 * time.Minute)
	s.SetActiveToolCall(ActiveToolCall{
		Name:      "agent.await",
		Args:      "all",
		Path:      "somewhere",
		Output:    "partial",
		StartedAt: started,
	})

	s.SetActiveToolCallArgs("all (2 running)")

	got, ok := s.ActiveToolCall()
	if !ok {
		t.Fatal("no active tool call")
	}
	if got.Args != "all (2 running)" {
		t.Errorf("Args = %q, want the updated value", got.Args)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v — the elapsed timer was reset", got.StartedAt, started)
	}
	if got.Name != "agent.await" || got.Path != "somewhere" || got.Output != "partial" {
		t.Errorf("other fields were disturbed: %+v", got)
	}
}

func TestSetActiveToolCallArgsNoActiveCallIsNoop(t *testing.T) {
	s := New(config.Config{}, t.TempDir(), time.Now(), Persistence{})
	s.SetActiveToolCallArgs("anything") // must not panic
	if _, ok := s.ActiveToolCall(); ok {
		t.Error("an args update must not create an active call")
	}
}
