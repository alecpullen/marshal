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

func TestSystemAccessDefaultsFalseAndToggles(t *testing.T) {
	s := newTestState()
	if s.SystemAccess() {
		t.Fatal("SystemAccess() = true on a fresh state, want false")
	}
	s.SetSystemAccess(true)
	if !s.SystemAccess() {
		t.Fatal("SystemAccess() = false after SetSystemAccess(true), want true")
	}
	s.SetSystemAccess(false)
	if s.SystemAccess() {
		t.Fatal("SystemAccess() = true after SetSystemAccess(false), want false")
	}
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

// The child-question queue tests that lived here were removed with the
// parent.ask/parent.respond channel in the context-diet Tier 2a cut:
// session.State no longer carries a child-question queue.

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

// TestSteeringTypedFIFOCompatibility pins that the typed FIFO behind the
// steering queue keeps the legacy string API's ordering and copy semantics:
// Push/Queue/Drain/Pop stay first-in-first-out and every returned slice is
// independent of the queue.
func TestSteeringTypedFIFOCompatibility(t *testing.T) {
	state := newTestState()
	state.PushSteering("first")
	state.PushSteering("second")
	state.PushSteering("third")

	queued := state.SteeringQueue()
	if len(queued) != 3 || queued[0] != "first" || queued[1] != "second" || queued[2] != "third" {
		t.Fatalf("SteeringQueue = %v, want [first second third]", queued)
	}
	// Mutating the returned slice must not reach the queue.
	queued[0] = "mutated"
	if got := state.SteeringQueue(); got[0] != "first" {
		t.Fatalf("SteeringQueue = %v, want the queue unaffected by a caller's mutation", got)
	}

	// PopSteering removes the oldest entry only.
	oldest, ok := state.PopSteering()
	if !ok || oldest != "first" {
		t.Fatalf("PopSteering = (%q, %v), want (first, true)", oldest, ok)
	}
	if got := state.SteeringQueue(); len(got) != 2 || got[0] != "second" {
		t.Fatalf("SteeringQueue after pop = %v, want [second third]", got)
	}

	// DrainSteering returns the rest in order and empties the queue.
	drained := state.DrainSteering()
	if len(drained) != 2 || drained[0] != "second" || drained[1] != "third" {
		t.Fatalf("DrainSteering = %v, want [second third]", drained)
	}
	drained[0] = "mutated"
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("SteeringQueue after drain = %v, want empty", got)
	}
	if _, ok := state.PopSteering(); ok {
		t.Fatal("PopSteering on an empty queue returned ok=true")
	}
}

// TestSteeringMessageIDsAreUnique pins that two queued messages with equal
// text still carry distinct internal IDs, so the Send now controller can name
// exactly which entries one accepted request covers.
func TestSteeringMessageIDsAreUnique(t *testing.T) {
	state := newTestState()
	state.PushSteering("same text")
	state.PushSteering("same text")

	state.mu.Lock()
	entries := append([]SteeringMessage(nil), state.steeringQueue...)
	state.mu.Unlock()
	if len(entries) != 2 {
		t.Fatalf("typed queue = %v, want two entries", entries)
	}
	if entries[0].ID == "" || entries[1].ID == "" {
		t.Fatalf("typed queue = %+v, want non-empty IDs", entries)
	}
	if entries[0].ID == entries[1].ID {
		t.Fatalf("typed queue = %+v, want distinct IDs for equal text", entries)
	}
	if entries[0].Text != "same text" || entries[1].Text != "same text" {
		t.Fatalf("typed queue = %+v, want the text preserved", entries)
	}
}

// TestSteeringClearDoesNotTouchReports pins that the typed steering queue
// stays independent of the machine-generated report queues: clearing human
// steering must not drop a background child's completion report.
func TestSteeringClearDoesNotTouchReports(t *testing.T) {
	state := newTestState()
	state.PushSubagentReport("[subagent 1 finished] child report")
	state.PushSteering("human steering")

	state.ClearSteering()
	if got := state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("steering queue = %v, want empty after clear", got)
	}
	if got := state.SubagentReports(); len(got) != 1 || got[0] != "[subagent 1 finished] child report" {
		t.Fatalf("subagent report queue = %v, want the report preserved", got)
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

// ── Task 8: the shared conversation-end gate ──────────────────────────────

// TestWorkMembershipTracksParentChildAndCommand pins the shared gate's
// membership: one item per registered unit of conversation work, each with its
// own stable ID, and each released by exactly the call that pairs with its
// registration.
func TestWorkMembershipTracksParentChildAndCommand(t *testing.T) {
	s := newTestState()
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("EnterSubagent: %v", err)
	}
	release, err := s.AdmitWork(WorkAdmissionRequest{ExecutionID: "exec-1", DisplayID: "job-1", Command: "sleep 30"})
	if err != nil {
		t.Fatalf("AdmitWork: %v", err)
	}

	snap := s.WorkMembership()
	if len(snap.Items) != 3 {
		t.Fatalf("membership = %+v, want parent, child and command", snap.Items)
	}
	seen := map[WorkID]WorkKind{}
	for _, item := range snap.Items {
		if item.ID == "" {
			t.Fatalf("item %+v has no stable ID", item)
		}
		if _, dup := seen[item.ID]; dup {
			t.Fatalf("duplicate work ID %q", item.ID)
		}
		seen[item.ID] = item.Kind
	}
	if seen[snap.Items[0].ID] != WorkKindParent {
		t.Fatalf("first item = %+v, want the parent operation", snap.Items[0])
	}

	// Every release pairs with exactly one registration.
	s.ExitSubagent()
	release.Release()
	release.Release() // idempotent
	s.EndWork()
	if snap := s.WorkMembership(); len(snap.Items) != 0 {
		t.Fatalf("membership after all work finished = %+v, want empty", snap.Items)
	}
	if got := s.OutstandingWork(); len(got) != 0 {
		t.Fatalf("OutstandingWork = %+v, want none", got)
	}
}

// TestAcceptWorkDisplayedRejectsSubsetWithoutFreezing pins contract 7: a
// displayed set that misses a currently active ID is refused, changes nothing and
// stops nothing — including not freezing admission.
func TestAcceptWorkDisplayedRejectsSubsetWithoutFreezing(t *testing.T) {
	s := newTestState()
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	first := s.WorkMembership().Items[0].ID

	// New work arrives after the displayed snapshot was taken.
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("EnterSubagent: %v", err)
	}

	snap, expanded, frozen := s.AcceptWorkDisplayed([]string{string(first)}, true)
	if len(expanded) != 1 {
		t.Fatalf("expanded = %v, want the undisplayed child", expanded)
	}
	if frozen {
		t.Fatal("a refused acceptance must not freeze ordinary admission")
	}
	if s.WorkPhase() != ExitPhaseOpen {
		t.Fatalf("phase after a refused acceptance = %q, want open", s.WorkPhase())
	}
	if len(snap.Items) != 2 {
		t.Fatalf("snapshot = %+v, want both items", snap.Items)
	}
	// Ordinary admission is untouched: a parent operation still starts.
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork after a refused acceptance = %v, want nil", err)
	}
	s.EndWork()
	s.ExitSubagent()
	s.EndWork()
}

// TestAcceptWorkDisplayedAcceptsWithoutReconfirmingFinishedIDs pins the other
// half of contract 7: IDs that are no longer active need no reconfirmation.
func TestAcceptWorkDisplayedAcceptsWithoutReconfirmingFinishedIDs(t *testing.T) {
	s := newTestState()
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	parent := s.WorkMembership().Items[0].ID
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("EnterSubagent: %v", err)
	}
	child := s.WorkMembership().Items[1].ID
	s.ExitSubagent()

	// The displayed set names BOTH, including the child that has since finished.
	_, expanded, frozen := s.AcceptWorkDisplayed([]string{string(parent), string(child)}, true)
	if len(expanded) != 0 {
		t.Fatalf("expanded = %v, want none: a finished ID needs no reconfirmation", expanded)
	}
	if !frozen {
		t.Fatal("a covering displayed set must freeze admission")
	}
	if s.WorkPhase() != ExitPhaseStopping {
		t.Fatalf("phase = %q, want stopping", s.WorkPhase())
	}
	s.EndWork()
}

// TestWorkGateFreezesRegistrationAtomically pins the synchronization boundary: a
// registration that wins the race is in the membership the acceptance reports, and
// one that loses the race is refused. There is no window in which work is both
// admitted and invisible to an accepted end.
func TestWorkGateFreezesRegistrationAtomically(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := newTestState()
		ready := make(chan struct{})
		accepted := make(chan WorkSnapshot, 1)
		var admitErr error
		var wg sync.WaitGroup

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-ready
			_, admitErr = s.AdmitWork(WorkAdmissionRequest{ExecutionID: "exec", DisplayID: "job"})
		}()
		go func() {
			defer wg.Done()
			<-ready
			snap, _, _ := s.AcceptWorkDisplayed(nil, false)
			accepted <- snap
		}()
		close(ready)
		wg.Wait()

		snap := <-accepted
		if admitErr == nil {
			// The registration won: it is part of the membership the acceptance
			// observed, so nothing was admitted invisibly.
			if len(snap.Items) != 1 {
				t.Fatalf("iteration %d: registration succeeded but the accepted membership is %+v", i, snap.Items)
			}
		} else if !errors.Is(admitErr, ErrConversationEnding) {
			t.Fatalf("iteration %d: AdmitWork = %v, want nil or ErrConversationEnding", i, admitErr)
		}
	}
}

// TestNestedChildRegistrationCannotHideFromAnAcceptedEnd pins contract 7 for the
// nested case: work a DELEGATE launches is registered in the conversation's gate
// (the command service carries the conversation's admission seam), so a grandchild's
// command is an item of the parent's membership with its own ID — it can never hide
// behind the child that launched it.
func TestNestedChildRegistrationCannotHideFromAnAcceptedEnd(t *testing.T) {
	parent := newTestState()
	// The delegate's own session: depth 1, exactly as an agent.run child has, with
	// its own transcript and no gate of its own.
	child := New(config.Default(), t.TempDir(), time.Unix(100, 0), Persistence{}, WithDepth(subagentMaxDepth))

	if err := parent.BeginWork(); err != nil {
		t.Fatalf("parent BeginWork: %v", err)
	}
	if err := parent.EnterSubagent(); err != nil {
		t.Fatalf("parent EnterSubagent: %v", err)
	}
	// A nested delegate is refused by the depth guard: the delegation tree stops
	// one level down, so the parent's membership names every unit that exists.
	if err := child.EnterSubagent(); !errors.Is(err, ErrSubagentDepthLimit) {
		t.Fatalf("nested EnterSubagent = %v, want ErrSubagentDepthLimit", err)
	}

	// The delegate's own command still lands in the CONVERSATION's membership: the
	// shared service's admission seam is the conversation's gate, so a child cannot
	// launch work the warning would not show.
	childTurn, _ := child.BeginTurnStop("delegate")
	lease, err := parent.AdmitWork(WorkAdmissionRequest{
		ExecutionID: "grandchild-exec",
		DisplayID:   "job-1",
		Command:     "sleep 30",
		Actor:       "delegate",
		TurnID:      childTurn.TurnID(),
	})
	if err != nil {
		t.Fatalf("delegate command admission: %v", err)
	}

	snap := parent.WorkMembership()
	if len(snap.Items) != 3 {
		t.Fatalf("membership = %+v, want the parent, the delegate and its command", snap.Items)
	}
	var commandItem *WorkItem
	for i := range snap.Items {
		if snap.Items[i].Kind == WorkKindCommand {
			commandItem = &snap.Items[i]
		}
	}
	if commandItem == nil {
		t.Fatalf("membership = %+v, want the delegate's command", snap.Items)
	}
	if commandItem.TurnID != childTurn.TurnID() {
		t.Fatalf("command turn = %q, want the delegate's turn %q", commandItem.TurnID, childTurn.TurnID())
	}
	// A displayed subset that omits the delegate's command is refused.
	ids := []string{}
	for _, item := range snap.Items {
		if item.Kind != WorkKindCommand {
			ids = append(ids, string(item.ID))
		}
	}
	_, expanded, frozen := parent.AcceptWorkDisplayed(ids, true)
	if len(expanded) != 1 {
		t.Fatalf("expanded = %v, want the delegate's command", expanded)
	}
	if frozen {
		t.Fatal("a refused acceptance must not freeze")
	}
	lease.Release()
	child.EndTurnStop(childTurn)
	parent.ExitSubagent()
	parent.EndWork()
}

// TestExitAnalysisEpochIsSeparateAndScoped pins step 5's boundary: only scoped
// work may start during the postmortem phase, and the epoch's membership is
// reported separately from the ordinary one.
func TestExitAnalysisEpochIsSeparateAndScoped(t *testing.T) {
	s := newTestState()
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	s.EndWork()
	s.AcceptWorkDisplayed(nil, false)

	// Before the postmortem phase no epoch can open at all.
	if _, err := s.OpenExitAnalysis(); !errors.Is(err, ErrNotPostmortemPhase) {
		t.Fatalf("OpenExitAnalysis before postmortem = %v, want ErrNotPostmortemPhase", err)
	}
	s.SetWorkPhase(ExitPhasePostmortem)
	scope, err := s.OpenExitAnalysis()
	if err != nil {
		t.Fatalf("OpenExitAnalysis: %v", err)
	}
	if _, err := s.OpenExitAnalysis(); !errors.Is(err, ErrExitAnalysisAlreadyOpen) {
		t.Fatalf("second OpenExitAnalysis = %v, want ErrExitAnalysisAlreadyOpen", err)
	}

	// Ordinary work is still refused during the postmortem phase.
	if err := s.BeginWork(); !errors.Is(err, ErrConversationEnding) {
		t.Fatalf("BeginWork during postmortem = %v, want ErrConversationEnding", err)
	}
	// Scoped work is admitted, and only under the open epoch.
	if _, err := s.AdmitWork(WorkAdmissionRequest{ExecutionID: "e", DisplayID: "j", Scope: "not-the-open-epoch"}); !errors.Is(err, ErrNotPostmortemPhase) {
		t.Fatalf("scoped admission under a stale epoch = %v, want ErrNotPostmortemPhase", err)
	}
	lease, err := s.AdmitWork(WorkAdmissionRequest{ExecutionID: "e", DisplayID: "j", Scope: scope.ID()})
	if err != nil {
		t.Fatalf("scoped admission: %v", err)
	}
	snap := s.WorkMembership()
	if len(snap.Items) != 0 {
		t.Fatalf("ordinary membership = %+v, want empty while only analysis runs", snap.Items)
	}
	if len(snap.Analysis) != 1 {
		t.Fatalf("analysis membership = %+v, want the scoped command", snap.Analysis)
	}

	// Closing the epoch refuses further scoped work.
	s.CloseExitAnalysis(scope)
	if _, err := s.AdmitWork(WorkAdmissionRequest{ExecutionID: "e2", DisplayID: "j2", Scope: scope.ID()}); !errors.Is(err, ErrNotPostmortemPhase) {
		t.Fatalf("scoped admission after the epoch closed = %v, want ErrNotPostmortemPhase", err)
	}
	if got := s.OutstandingScopedWork(scope); len(got) != 1 {
		t.Fatalf("OutstandingScopedWork = %+v, want the still-running scoped command", got)
	}
	lease.Release()
	if got := s.OutstandingScopedWork(scope); len(got) != 0 {
		t.Fatalf("OutstandingScopedWork after release = %+v, want empty", got)
	}
}

// TestWaitForWorkDrainReportsOutstandingRatherThanLatching pins the join
// contract: a wait that misses its deadline reports the outstanding work and
// permits a retry.
func TestWaitForWorkDrainReportsOutstandingRatherThanLatching(t *testing.T) {
	s := newTestState()
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := s.WaitForWorkDrain(shortCtx)
	cancelShort()
	if err == nil {
		t.Fatal("WaitForWorkDrain with outstanding work must fail")
	}
	result := s.OutstandingWork()
	if len(result) != 1 || result[0].Kind != WorkKindParent {
		t.Fatalf("OutstandingWork = %+v, want the unsettled parent", result)
	}
	s.EndWork()
	if err := s.WaitForWorkDrain(context.Background()); err != nil {
		t.Fatalf("retry WaitForWorkDrain: %v", err)
	}
}

// TestWorkLabelsAreBoundedAndScrubbed pins step 2's label rule: a label is display
// data, so it is single-line, bounded and carries no secret material.
func TestWorkLabelsAreBoundedAndScrubbed(t *testing.T) {
	long := strings.Repeat("x", 400)
	if got := boundWorkLabel(long); len([]rune(got)) > maxWorkLabelRunes+1 {
		t.Fatalf("label length = %d runes, want it bounded", len([]rune(got)))
	}
	if got := boundWorkLabel("first line\nsecond line"); got != "first line" {
		t.Fatalf("multi-line label = %q, want the first line only", got)
	}
	if got := boundWorkLabel("API_KEY=supersecretvalue123 curl https://example.com"); strings.Contains(got, "supersecretvalue123") {
		t.Fatalf("label leaked a secret: %q", got)
	}
	if got := boundWorkLabel("   "); got != "" {
		t.Fatalf("blank label = %q, want empty", got)
	}
}

// TestParentWorkItemGroupsItsAttachedCommands pins step 2's grouping rule: a
// command admitted under an operation is shown under that operation, but keeps its
// own distinct ID in the membership so new work cannot hide behind the parent.
func TestParentWorkItemGroupsItsAttachedCommands(t *testing.T) {
	s := newTestState()
	if err := s.BeginWork(); err != nil {
		t.Fatalf("BeginWork: %v", err)
	}
	token, _ := s.BeginTurnStop("")
	if err := s.EnterSubagent(); err != nil {
		t.Fatalf("EnterSubagent: %v", err)
	}

	lease, err := s.AdmitWork(WorkAdmissionRequest{ExecutionID: "e", DisplayID: "j", TurnID: token.TurnID()})
	if err != nil {
		t.Fatalf("AdmitWork: %v", err)
	}
	snap := s.WorkMembership()
	var parentID WorkID
	var commandItem *WorkItem
	for i := range snap.Items {
		switch snap.Items[i].Kind {
		case WorkKindParent:
			parentID = snap.Items[i].ID
		case WorkKindCommand:
			commandItem = &snap.Items[i]
		}
	}
	if parentID == "" || commandItem == nil {
		t.Fatalf("membership = %+v, want a parent and a command", snap.Items)
	}
	if commandItem.Group != parentID {
		t.Fatalf("command group = %q, want the parent %q", commandItem.Group, parentID)
	}
	if commandItem.ID == parentID {
		t.Fatal("the command must keep its own ID, not the parent's")
	}
	// The distinct stoppable targets fold the command into its parent.
	if targets := snap.Targets(); len(targets) != 2 {
		t.Fatalf("targets = %v, want the parent (covering its command) and the child", targets)
	}
	lease.Release()
	s.EndTurnStop(token)
	s.ExitSubagent()
	s.EndWork()
}

func TestAppendActiveToolCallOutput(t *testing.T) {
	s := New(config.Default(), t.TempDir(), time.Now(), Persistence{})
	s.SetActiveToolCall(ActiveToolCall{Name: "shell.run", Args: "echo hi", StartedAt: time.Now()})
	s.AppendActiveToolCallOutput("", "hello")
	s.AppendActiveToolCallOutput("", " world")
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

	s.SetActiveToolCallArgs("", "all (2 running)")

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
	s.SetActiveToolCallArgs("", "anything") // must not panic
	if _, ok := s.ActiveToolCall(); ok {
		t.Error("an args update must not create an active call")
	}
}

// ── Effective session access state (system-access approval) ────────────

// The approval and mode machinery reads the mode and the system flag as one
// pair: a reader must never see the mode from one commit and the flag from
// another, and a fresh session reports its configured mode with system
// access off. The reported mode is the normalized configured one, so blank
// and unrecognized values seed the read-only default — the same mapping the
// TUI's seeded-mode label relies on — instead of failing the session.
func TestAccessStateReadsModeAndSystemTogether(t *testing.T) {
	s := newTestState()
	if got := s.AccessState(); got.Mode != "default" || got.System {
		t.Fatalf("fresh AccessState() = %+v, want {Mode: default, System: false}", got)
	}
	for _, tc := range []struct{ configured, want string }{
		{"copilot", "copilot"},
		{" Plan ", "plan"},
		{"", "default"},
		{"nonsense", "default"},
	} {
		cfg := config.Default()
		cfg.Agent.ApprovalMode = tc.configured
		seeded := New(cfg, t.TempDir(), time.Unix(100, 0), Persistence{})
		if got := seeded.AccessState(); got.Mode != tc.want || got.System {
			t.Errorf("configured %q: AccessState() = %+v, want {Mode: %s, System: false}", tc.configured, got, tc.want)
		}
	}

	if err := s.SetSessionMode("auto", func() {}); err != nil {
		t.Fatalf("SetSessionMode(auto) = %v, want nil", err)
	}
	s.SetSystemAccess(true)
	if got := s.AccessState(); got.Mode != "auto" || !got.System {
		t.Fatalf("AccessState() = %+v, want {Mode: auto, System: true}", got)
	}
	if got := s.Config.Agent.ApprovalMode; got != "auto" {
		t.Fatalf("State.Config.Agent.ApprovalMode = %q, want the effective mode", got)
	}
}

// A user's mode selection belongs to the conversation. Reloading a config
// file that does not itself change approval_mode must keep it, or editing any
// unrelated setting would silently drop the session back to the configured
// mode mid-conversation.
func TestSessionModeOverrideSurvivesUnrelatedConfig(t *testing.T) {
	s := newTestState()
	if err := s.SetSessionMode("auto", func() {}); err != nil {
		t.Fatalf("SetSessionMode(auto) = %v", err)
	}

	cfg := config.Default()
	cfg.Agent.NarrationPrompt = !cfg.Agent.NarrationPrompt // unrelated change
	if got := s.ModeForConfig(cfg); got != "auto" {
		t.Fatalf("ModeForConfig(unchanged configured mode) = %q, want the auto override", got)
	}
	s.RebaseSessionConfig(cfg)

	if got := s.AccessState().Mode; got != "auto" {
		t.Fatalf("mode after an unrelated reload = %q, want auto (override lost)", got)
	}
	if got := s.Config.Agent.NarrationPrompt; got != cfg.Agent.NarrationPrompt {
		t.Fatalf("the unrelated setting was not installed: NarrationPrompt = %v, want %v", got, cfg.Agent.NarrationPrompt)
	}
}

// An explicit change to the configured mode is newer intent than the session
// override: it replaces the user's selection, and a later unrelated reload
// keeps the newly configured mode instead of reviving the old one.
func TestExplicitConfiguredModeChangeClearsOverride(t *testing.T) {
	s := newTestState()
	if err := s.SetSessionMode("auto", func() {}); err != nil {
		t.Fatalf("SetSessionMode(auto) = %v", err)
	}

	changed := config.Default()
	changed.Agent.ApprovalMode = "copilot"
	s.RebaseSessionConfig(changed)

	if got := s.AccessState().Mode; got != "copilot" {
		t.Fatalf("mode after an explicit configured change = %q, want copilot", got)
	}
	// The stale auto override is gone: with it still set, ModeForConfig
	// compares the unchanged configured value and would return auto.
	if got := s.ModeForConfig(changed); got != "copilot" {
		t.Fatalf("ModeForConfig(%q) = %q, want copilot — the auto override survived an explicit change", changed.Agent.ApprovalMode, got)
	}

	later := changed
	later.Agent.NarrationPrompt = !later.Agent.NarrationPrompt
	s.RebaseSessionConfig(later)
	if got := s.AccessState().Mode; got != "copilot" {
		t.Fatalf("mode after a later unrelated reload = %q, want copilot", got)
	}
}

// A rejected selection must be inert: unknown or blank modes, a missing
// applier and a quiescing session all fail before any mutation, and none of
// them may reach the runner's setter or leave a session override behind.
func TestSessionModeFailureDoesNotMutate(t *testing.T) {
	s := newTestState()
	before := s.AccessState()
	called := false
	apply := func() { called = true }

	for _, mode := range []string{"", "bogus", "Auto", " auto"} {
		if err := s.SetSessionMode(mode, apply); err == nil {
			t.Errorf("SetSessionMode(%q) = nil, want an error", mode)
		}
	}
	if err := s.SetSessionMode("auto", nil); err == nil {
		t.Error("SetSessionMode(auto, nil) = nil, want an error for a missing applier")
	}
	if called {
		t.Fatal("a rejected selection reached the runner setter")
	}
	if got := s.AccessState(); got != before {
		t.Fatalf("AccessState() = %+v after rejected selections, want %+v", got, before)
	}
	if got := s.ModeForConfig(config.Default()); got != "default" {
		t.Fatalf("ModeForConfig() = %q after rejected selections, want default (no override)", got)
	}

	s.BeginQuiesce()
	if err := s.SetSessionMode("auto", apply); !errors.Is(err, ErrSessionQuiescing) {
		t.Fatalf("SetSessionMode while quiescing = %v, want ErrSessionQuiescing", err)
	}
	if called {
		t.Fatal("a mode change reached the runner setter after quiesce")
	}
	if got := s.AccessState(); got != before {
		t.Fatalf("AccessState() = %+v after a quiescing rejection, want %+v", got, before)
	}
}

// A new conversation starts from configuration, not from whatever a prior
// conversation was granted or selected — and the config value it is built from
// is not aliased into the session, so one session's mode change cannot seed
// the next one.
func TestNewStateDoesNotInheritAccessOverrides(t *testing.T) {
	cfg := config.Default()
	first := New(cfg, t.TempDir(), time.Unix(100, 0), Persistence{})
	if err := first.SetSessionMode("auto", func() {}); err != nil {
		t.Fatalf("SetSessionMode(auto) = %v", err)
	}
	first.SetSystemAccess(true)

	second := New(cfg, t.TempDir(), time.Unix(200, 0), Persistence{})
	if got := second.AccessState(); got.Mode != "default" || got.System {
		t.Fatalf("second state AccessState() = %+v, want {Mode: default, System: false}", got)
	}
	if got := second.ModeForConfig(cfg); got != "default" {
		t.Fatalf("second state ModeForConfig() = %q, want default", got)
	}
	if got := cfg.Agent.ApprovalMode; got != "default" {
		t.Fatalf("the shared config fixture was mutated to %q", got)
	}
}

// ── Capability access requests settle once ────────────────────────────

// accessFixture bundles the parts every access test needs: a session with a
// live event broker, a pending call, and the events the broker delivered.
type accessFixture struct {
	t      *testing.T
	state  *State
	events <-chan pubsub.Event[Event]
	call   *PendingToolCall
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	state := newTestState()
	broker := pubsub.NewBroker[Event]()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	state.SetEventBroker(broker)
	return &accessFixture{
		t:      t,
		state:  state,
		events: broker.Subscribe(ctx),
		call:   &PendingToolCall{ID: "call-1", Name: "file.edit", ResponseChan: make(chan UserApprovalDecision, 1)},
	}
}

// register publishes an access request for a fresh pending call. Each call
// owns a single-use channel and once, so a second request always gets its own
// call — exactly as the runner would build one.
func (f *accessFixture) register(intent AccessIntent) AccessRequest {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f.t.Cleanup(cancel)
	f.call = &PendingToolCall{ID: "call-1", Name: "file.edit", ResponseChan: make(chan UserApprovalDecision, 1)}
	req, waiting, err := f.state.RegisterAccess(ctx, f.call, intent)
	if err != nil {
		f.t.Fatalf("RegisterAccess(%+v) = %v, want nil", intent, err)
	}
	if !waiting {
		f.t.Fatalf("RegisterAccess(%+v) did not publish a request: %+v", intent, req)
	}
	return req
}

// nextEvent returns the next published event, failing rather than hanging.
func (f *accessFixture) nextEvent() pubsub.Event[Event] {
	f.t.Helper()
	select {
	case ev := <-f.events:
		return ev
	case <-time.After(5 * time.Second):
		f.t.Fatal("timed out waiting for a session event")
		return pubsub.Event[Event]{}
	}
}

// choiceFor builds an approval choice for req that claims the given pair, and
// picks mode as the editing variant the user selected.
func choiceFor(req AccessRequest, current AccessState, mode string) AccessChoice {
	return AccessChoice{RequestID: req.RequestID, SessionID: req.SessionID, Current: current, Approved: true, Mode: mode}
}

// A child session and an occupied slot are the two ways a request could take
// over somebody else's place in the UI. Both must be refused without
// replacing whatever is displayed and without publishing anything.
func TestAccessRegistrationRejectsChildAndOccupiedSlot(t *testing.T) {
	ctx := context.Background()

	child := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{}, WithDepth(1))
	childBroker := pubsub.NewBroker[Event]()
	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child.SetEventBroker(childBroker)
	childEvents := childBroker.Subscribe(childCtx)
	childCall := &PendingToolCall{ID: "child", ResponseChan: make(chan UserApprovalDecision, 1)}
	if _, waiting, err := child.RegisterAccess(ctx, childCall, AccessIntent{Editing: true}); err == nil || waiting {
		t.Fatalf("child RegisterAccess = (waiting %v, err %v), want a refusal", waiting, err)
	}
	if child.PendingApproval() != nil || childCall.Access != nil {
		t.Fatal("a child session published an access request")
	}
	select {
	case ev := <-childEvents:
		t.Fatalf("child session published %q", ev.Type)
	default:
	}

	// A generic ordinary approval already waiting for the user is not
	// displaceable either: the request must fail rather than take its slot.
	s := newTestState()
	ordinary := &PendingToolCall{ID: "ordinary", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	s.SetPendingApproval(ordinary)
	blocked := &PendingToolCall{ID: "access", ResponseChan: make(chan UserApprovalDecision, 1)}
	if _, waiting, err := s.RegisterAccess(ctx, blocked, AccessIntent{System: true}); err == nil || waiting {
		t.Fatalf("RegisterAccess over an occupied slot = (waiting %v, err %v), want a refusal", waiting, err)
	}
	if got := s.PendingApproval(); got != ordinary {
		t.Fatalf("the occupied slot was replaced: %+v", got)
	}
	if blocked.Access != nil {
		t.Fatal("a rejected request still stamped its pending call")
	}
	select {
	case d := <-blocked.ResponseChan:
		t.Fatalf("a rejected request was answered: %+v", d)
	default:
	}

	// The refusal must not have disturbed the ordinary approval: it can still
	// be answered normally.
	ordinary.Respond(UserApprovalDecision{Approved: true})
	if d := <-ordinary.ResponseChan; !d.Approved {
		t.Fatalf("the ordinary approval lost its answer: %+v", d)
	}
}

// A request's identity is the session's, not the provider's: two registrations
// that reuse the same tool-call ID get distinct request IDs, and the copies
// handed to subscribers carry metadata but no way to answer.
func TestAccessRequestIdentityAndSnapshotIsolation(t *testing.T) {
	f := newAccessFixture(t)
	first := f.register(AccessIntent{System: true})
	if first.RequestID == "" {
		t.Fatal("RegisterAccess returned an empty request ID")
	}
	ev := f.nextEvent()
	if ev.Type != EventPendingApprovalChanged {
		t.Fatalf("registration published %q, want %q", ev.Type, EventPendingApprovalChanged)
	}
	if ev.Payload.PendingApproval == nil || ev.Payload.PendingApproval.Access == nil {
		t.Fatalf("the registration event carried no access metadata: %+v", ev.Payload.PendingApproval)
	}
	if ev.Payload.PendingApproval.Access.RequestID != first.RequestID {
		t.Fatalf("event request ID = %q, want %q", ev.Payload.PendingApproval.Access.RequestID, first.RequestID)
	}
	if ev.Payload.PendingApproval.Access.StepID != f.call.StepID {
		t.Fatalf("event step ID = %d, want %d", ev.Payload.PendingApproval.Access.StepID, f.call.StepID)
	}
	if ev.Payload.PendingApproval.Access.Actor != "Marshal" {
		t.Fatalf("event actor = %q, want Marshal for a stepless call", ev.Payload.PendingApproval.Access.Actor)
	}

	// The event copy must not be able to settle the request: no channel, and
	// Respond is inert for a typed request even when one is forged.
	if env := ev.Payload.PendingApproval.ResponseChan; env != nil {
		t.Fatalf("the event copy carried a response channel: %v", env)
	}
	ev.Payload.PendingApproval.Respond(UserApprovalDecision{Approved: true, Access: &AccessResult{Status: AccessGranted}})
	select {
	case d := <-f.call.ResponseChan:
		t.Fatalf("an event copy settled the canonical request: %+v", d)
	default:
	}

	// Cancelling through the canonical request frees the slot, and a second
	// request that reuses the same provider ID must not reuse the request ID.
	if !f.state.CancelAccess(first.RequestID, AccessCanceled, "test") {
		t.Fatal("CancelAccess(canonical request) = false, want true")
	}
	<-f.call.ResponseChan
	second := f.register(AccessIntent{System: true})
	if second.RequestID == first.RequestID {
		t.Fatalf("two requests shared the request ID %q", second.RequestID)
	}
}

// The acknowledgment is the contract: a caller must not be told the
// capability is available until the resolver's setter has actually run.
func TestAccessResolveAppliesBeforeAcknowledgment(t *testing.T) {
	f := newAccessFixture(t)
	req := f.register(AccessIntent{Editing: true, Reason: "edit a file"})

	entered := make(chan struct{})
	release := make(chan struct{})
	prepared := false
	prepare := func(AccessRequest, AccessChoice) (AccessCommit, error) {
		prepared = true
		return AccessCommit{Effective: AccessState{Mode: "edit"}, Apply: func() {
			close(entered)
			<-release
		}}, nil
	}
	done := make(chan AccessResult, 1)
	go func() {
		result, _ := f.state.ResolveAccess(choiceFor(req, req.Current, "edit"), prepare)
		done <- result
	}()

	<-entered
	// Apply is inside the resolver: the waiter must still be empty-handed and
	// the session must not yet report the new mode.
	select {
	case d := <-f.call.ResponseChan:
		t.Fatalf("the grant was acknowledged before it was applied: %+v", d)
	default:
	}
	if got := f.state.AccessState(); got.Mode == "edit" {
		t.Fatal("the mode moved before Apply returned")
	}
	if !prepared {
		t.Fatal("prepare was never consulted")
	}

	close(release)
	result := <-done

	if result.Status != AccessGranted {
		t.Fatalf("ResolveAccess = %+v, want a grant", result)
	}
	if got, want := result.Effective, (AccessState{Mode: "edit"}); got != want {
		t.Fatalf("result effective = %+v, want %+v", got, want)
	}
	if got := f.state.AccessState(); got != (AccessState{Mode: "edit"}) {
		t.Fatalf("AccessState() = %+v after the grant, want {edit false}", got)
	}

	d := <-f.call.ResponseChan
	if !d.Approved || d.Access == nil {
		t.Fatalf("delivered decision = %+v, want an approved typed grant", d)
	}
	if d.Access.Status != AccessGranted {
		t.Fatalf("delivered access status = %q, want %q", d.Access.Status, AccessGranted)
	}
	if d.Access.Effective != result.Effective {
		t.Fatalf("delivered effective = %+v, want %+v", d.Access.Effective, result.Effective)
	}
	if f.state.PendingApproval() != nil {
		t.Fatal("the settled request still occupies the slot")
	}
}

// Cancellation that is already visible when the gate is taken beats the
// choice: nothing is prepared, nothing is applied, and the waiter learns it
// was cancelled.
func TestAccessCancelWinsBeforeAcceptance(t *testing.T) {
	f := newAccessFixture(t)

	// The registration's own context is cancelled, the way a turn being
	// interrupted cancels the request it made.
	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	req, waiting, err := f.state.RegisterAccess(callCtx, f.call, AccessIntent{Editing: true})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%+v, %v, %v), want a published request", req, waiting, err)
	}
	cancelCall()

	called := false
	result, err := f.state.ResolveAccess(choiceFor(req, req.Current, "edit"), func(AccessRequest, AccessChoice) (AccessCommit, error) {
		called = true
		return AccessCommit{Effective: AccessState{Mode: "edit"}, Apply: func() { called = true }}, nil
	})
	if err != nil {
		t.Fatalf("ResolveAccess on a canceled request = %v, want nil (a typed cancellation)", err)
	}
	if result.Status != AccessCanceled {
		t.Fatalf("ResolveAccess = %+v, want AccessCanceled", result)
	}
	if called {
		t.Fatal("a canceled request reached prepare or apply")
	}
	if got := f.state.AccessState(); got != (AccessState{Mode: "default"}) {
		t.Fatalf("AccessState() = %+v after cancellation, want default/false", got)
	}
	if d := <-f.call.ResponseChan; d.Approved || d.Access == nil || d.Access.Status != AccessCanceled {
		t.Fatalf("delivered decision = %+v, want an unapproved typed cancellation", d)
	}
}

// Once the grant has passed its final cancellation check the application wins
// even if the requester is cancelled mid-Apply: the acknowledgment arrives
// afterwards, and a late cancellation cannot revoke what was granted.
func TestAccessAcceptedCommitWinsLaterCancellation(t *testing.T) {
	f := newAccessFixture(t)
	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	req, waiting, err := f.state.RegisterAccess(callCtx, f.call, AccessIntent{System: true})
	if err != nil || !waiting {
		t.Fatalf("RegisterAccess = (%+v, %v, %v), want a published request", req, waiting, err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan AccessResult, 1)
	go func() {
		result, _ := f.state.ResolveAccess(choiceFor(req, req.Current, ""), func(AccessRequest, AccessChoice) (AccessCommit, error) {
			return AccessCommit{Effective: AccessState{Mode: "default", System: true}, Apply: func() {
				close(entered)
				<-release
			}}, nil
		})
		done <- result
	}()

	<-entered
	// Cancel while Apply is in flight: the request is past its last check, so
	// this cannot stop the application.
	cancelCall()
	select {
	case d := <-f.call.ResponseChan:
		t.Fatalf("a result was delivered during Apply: %+v", d)
	default:
	}
	select {
	case result := <-done:
		t.Fatalf("ResolveAccess returned during Apply: %+v", result)
	default:
	}

	close(release)
	result := <-done
	if result.Status != AccessGranted {
		t.Fatalf("ResolveAccess = %+v, want the accepted grant", result)
	}
	d := <-f.call.ResponseChan
	if !d.Approved || d.Access == nil || d.Access.Status != AccessGranted {
		t.Fatalf("delivered decision = %+v, want one typed grant", d)
	}
	// Exactly one result, and the late cancellation cannot undo the grant.
	f.call.Respond(UserApprovalDecision{Approved: false})
	if extra := <-f.call.ResponseChan; extra.Approved || extra.Access != nil {
		t.Fatalf("a second decision was delivered: %+v", extra)
	}
	if f.state.CancelAccess(req.RequestID, AccessCanceled, "too late") {
		t.Fatal("CancelAccess settled an already-settled request")
	}
	if got := f.state.AccessState(); got != (AccessState{Mode: "default", System: true}) {
		t.Fatalf("AccessState() = %+v after the late cancellation, want the granted pair", got)
	}
}

// A choice made against a pair the session has already left must not apply:
// the request survives, nothing is prepared, and the refreshed choice for the
// current pair then resolves normally.
func TestAccessRefreshRetainsPendingAndResetsNoAuthority(t *testing.T) {
	f := newAccessFixture(t)
	req := f.register(AccessIntent{Editing: true})
	shown := req.Current

	// Something else grants system access while the panel is on screen, so the
	// pair the user was shown is no longer the live one.
	f.state.SetSystemAccess(true)

	prepared := false
	prepare := func(AccessRequest, AccessChoice) (AccessCommit, error) {
		prepared = true
		return AccessCommit{Effective: AccessState{Mode: "edit"}, Apply: func() {}}, nil
	}
	result, err := f.state.ResolveAccess(choiceFor(req, shown, "edit"), prepare)
	if err != nil {
		t.Fatalf("ResolveAccess(stale pair) = %v, want nil", err)
	}
	if result.Status != AccessRefresh {
		t.Fatalf("ResolveAccess(stale pair) = %+v, want AccessRefresh", result)
	}
	if result.Effective != (AccessState{Mode: "default", System: true}) {
		t.Fatalf("refresh effective = %+v, want the current pair", result.Effective)
	}
	if prepared {
		t.Fatal("a stale choice reached prepare")
	}
	select {
	case d := <-f.call.ResponseChan:
		t.Fatalf("a refresh settled the request: %+v", d)
	default:
	}
	if got := f.state.AccessState(); got != (AccessState{Mode: "default", System: true}) {
		t.Fatalf("AccessState() = %+v after a refresh, want the unrelated grant only", got)
	}

	// The refreshed request reports the live pair and still needs editing, so
	// a choice made against it resolves and grants.
	refreshed, ok := f.state.AccessRequest()
	if !ok {
		t.Fatal("the request was dropped by a refresh")
	}
	if refreshed.Current != result.Effective {
		t.Fatalf("refreshed request Current = %+v, want %+v", refreshed.Current, result.Effective)
	}
	if refreshed.RequestID != req.RequestID {
		t.Fatalf("refresh changed the request ID: %q → %q", req.RequestID, refreshed.RequestID)
	}
	if !refreshed.MissingEditing || refreshed.MissingSystem {
		t.Fatalf("refreshed request flags = %+v, want editing missing only", refreshed)
	}
	result, err = f.state.ResolveAccess(choiceFor(refreshed, refreshed.Current, "edit"), func(AccessRequest, AccessChoice) (AccessCommit, error) {
		return AccessCommit{Effective: AccessState{Mode: "edit", System: true}, Apply: func() {}}, nil
	})
	if err != nil || result.Status != AccessGranted {
		t.Fatalf("ResolveAccess(refreshed) = (%+v, %v), want a grant", result, err)
	}
}

// Every invalid commit must fail closed: the waiter is unblocked with an
// unapproved, failed result, no setter runs, and the pair does not move.
func TestAccessInvalidCommitFailsWithoutPartialGrant(t *testing.T) {
	cases := []struct {
		name    string
		intent  AccessIntent
		choice  func(AccessRequest) AccessChoice
		prepare AccessPrepare
	}{
		{
			name:   "invalid editing variant",
			intent: AccessIntent{Editing: true},
			// A blank mode may not fill an editing gap.
			choice:  func(req AccessRequest) AccessChoice { return choiceFor(req, req.Current, "") },
			prepare: func(AccessRequest, AccessChoice) (AccessCommit, error) { return AccessCommit{Apply: func() {}}, nil },
		},
		{
			name:   "unexpected target",
			intent: AccessIntent{System: true},
			choice: func(req AccessRequest) AccessChoice { return choiceFor(req, req.Current, "") },
			prepare: func(AccessRequest, AccessChoice) (AccessCommit, error) {
				return AccessCommit{Effective: AccessState{Mode: "auto"}, Apply: func() {}}, nil
			},
		},
		{
			name:   "nil apply",
			intent: AccessIntent{System: true},
			choice: func(req AccessRequest) AccessChoice { return choiceFor(req, req.Current, "") },
			prepare: func(AccessRequest, AccessChoice) (AccessCommit, error) {
				return AccessCommit{Effective: AccessState{Mode: "default", System: true}}, nil
			},
		},
		{
			name:    "nil prepare",
			intent:  AccessIntent{System: true},
			choice:  func(req AccessRequest) AccessChoice { return choiceFor(req, req.Current, "") },
			prepare: nil,
		},
		{
			name:   "prepare error",
			intent: AccessIntent{System: true},
			choice: func(req AccessRequest) AccessChoice { return choiceFor(req, req.Current, "") },
			prepare: func(AccessRequest, AccessChoice) (AccessCommit, error) {
				return AccessCommit{}, errors.New("policy says no")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAccessFixture(t)
			req := f.register(tc.intent)
			before := f.state.AccessState()

			result, err := f.state.ResolveAccess(tc.choice(req), tc.prepare)
			if err == nil {
				t.Fatalf("ResolveAccess = %+v, want a non-nil error", result)
			}
			if result.Status != AccessFailed {
				t.Fatalf("ResolveAccess = %+v, want AccessFailed", result)
			}
			if result.Message == "" {
				t.Fatal("a failure carried no message")
			}
			if result.Effective != before {
				t.Fatalf("failure effective = %+v, want the unchanged pair %+v", result.Effective, before)
			}
			if got := f.state.AccessState(); got != before {
				t.Fatalf("AccessState() = %+v after a failure, want %+v", got, before)
			}
			d := <-f.call.ResponseChan
			if d.Approved {
				t.Fatalf("a failed request was approved: %+v", d)
			}
			if d.Access == nil || d.Access.Status != AccessFailed || d.Access.Message == "" {
				t.Fatalf("delivered decision = %+v, want a typed failure with a message", d)
			}
			if f.state.PendingApproval() != nil {
				t.Fatal("a failed request still occupies the slot")
			}
		})
	}
}

// Duplicate, foreign and late answers must be inert: they cannot grant, cannot
// erase the request that is actually pending, and cannot leak into whatever
// request comes next.
func TestAccessDuplicateForeignAndLateChoicesCannotSettleNewRequest(t *testing.T) {
	f := newAccessFixture(t)
	applied := 0
	prepare := func(req AccessRequest, choice AccessChoice) (AccessCommit, error) {
		target := req.Current
		if req.MissingEditing {
			target.Mode = choice.Mode
		}
		if req.MissingSystem {
			target.System = true
		}
		return AccessCommit{Effective: target, Apply: func() { applied++ }}, nil
	}

	first := f.register(AccessIntent{System: true})
	if result, err := f.state.ResolveAccess(choiceFor(first, first.Current, ""), prepare); err != nil || result.Status != AccessGranted {
		t.Fatalf("first ResolveAccess = (%+v, %v), want a grant", result, err)
	}
	<-f.call.ResponseChan
	if applied != 1 {
		t.Fatalf("apply ran %d times for one grant", applied)
	}

	// A duplicate of the settled choice is expired, and the late cancellation
	// of the settled request cannot reach a later one.
	if result, err := f.state.ResolveAccess(choiceFor(first, first.Current, ""), prepare); err == nil || result.Status != AccessExpired {
		t.Fatalf("duplicate ResolveAccess = (%+v, %v), want AccessExpired", result, err)
	}
	if f.state.CancelAccess(first.RequestID, AccessCanceled, "late") {
		t.Fatal("CancelAccess settled an already-settled request")
	}
	// A choice naming a request that never existed is expired too.
	ghost := AccessChoice{RequestID: "not-a-request", SessionID: first.SessionID, Current: first.Current, Approved: true}
	if result, err := f.state.ResolveAccess(ghost, prepare); err == nil || result.Status != AccessExpired {
		t.Fatalf("foreign ResolveAccess = (%+v, %v), want AccessExpired", result, err)
	}
	if applied != 1 {
		t.Fatalf("an inert choice reached apply: %d applications", applied)
	}

	// The next request is untouched by the stale IDs above.
	second := f.register(AccessIntent{Editing: true})
	if second.RequestID == first.RequestID {
		t.Fatal("the second request reused the first request ID")
	}
	if result, err := f.state.ResolveAccess(choiceFor(first, first.Current, ""), prepare); err == nil || result.Status != AccessExpired {
		t.Fatalf("stale choice against a live request = (%+v, %v), want AccessExpired", result, err)
	}
	if got := f.state.PendingApproval(); got == nil || got.Access == nil || got.Access.RequestID != second.RequestID {
		t.Fatalf("the live request was disturbed: %+v", got)
	}

	// The generic teardown path must not silently withdraw it either.
	f.state.SetPendingApproval(nil)
	if got := f.state.PendingApproval(); got == nil || got.Access == nil || got.Access.RequestID != second.RequestID {
		t.Fatalf("SetPendingApproval(nil) withdrew a live access request: %+v", got)
	}

	// An ordinary approval arriving while the request waits is denied, and
	// the request survives to be answered normally.
	interloper := &PendingToolCall{ID: "interloper", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	f.state.SetPendingApproval(interloper)
	if d := <-interloper.ResponseChan; d.Approved {
		t.Fatalf("an interloper was approved: %+v", d)
	}
	if got := f.state.PendingApproval(); got == nil || got.Access == nil || got.Access.RequestID != second.RequestID {
		t.Fatalf("an interloper displaced the access request: %+v", got)
	}

	result, err := f.state.ResolveAccess(choiceFor(second, second.Current, "edit"), prepare)
	if err != nil || result.Status != AccessGranted {
		t.Fatalf("ResolveAccess(second) = (%+v, %v), want a grant", result, err)
	}
	if applied != 2 {
		t.Fatalf("apply ran %d times, want one per grant", applied)
	}
	if d := <-f.call.ResponseChan; !d.Approved {
		t.Fatalf("the second grant decision = %+v, want approved", d)
	}

	// With the slot free again, ordinary approvals behave as before,
	// including an ordinary P1→P2 replacement.
	p1 := &PendingToolCall{ID: "p1", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	p2 := &PendingToolCall{ID: "p2", Name: "shell.run", ResponseChan: make(chan UserApprovalDecision, 1)}
	f.state.SetPendingApproval(p1)
	f.state.SetPendingApproval(p2)
	if got := f.state.PendingApproval(); got != p2 {
		t.Fatalf("ordinary replacement = %+v, want %+v", got, p2)
	}
	p2.Respond(UserApprovalDecision{Approved: true})
	if d := <-p2.ResponseChan; !d.Approved {
		t.Fatalf("the replacement could not be answered: %+v", d)
	}
}

// Denial and "already available" both report the pair that is actually in
// force, and neither is a reason to move authority.
func TestAccessDeniedAndAvailableReturnActualState(t *testing.T) {
	// A session already holding everything it is asked for answers
	// immediately: the satisfied registration takes no slot and publishes
	// nothing, and a denial reports the actual pair.
	s := newTestState()
	if err := s.SetSessionMode("auto", func() {}); err != nil {
		t.Fatalf("SetSessionMode(auto) = %v", err)
	}
	s.SetSystemAccess(true)
	satisfied := &PendingToolCall{ID: "satisfied", ResponseChan: make(chan UserApprovalDecision, 1)}
	immediate, waiting, err := s.RegisterAccess(context.Background(), satisfied, AccessIntent{Editing: true, System: true})
	if err != nil || waiting {
		t.Fatalf("RegisterAccess on a satisfied session = (%+v, waiting %v, err %v), want an immediate answer", immediate, waiting, err)
	}
	if immediate.RequestID != "" {
		t.Fatalf("a satisfied registration allocated a request ID: %q", immediate.RequestID)
	}
	if immediate.Current != (AccessState{Mode: "auto", System: true}) {
		t.Fatalf("satisfied registration Current = %+v, want {auto true}", immediate.Current)
	}
	if immediate.MissingEditing || immediate.MissingSystem {
		t.Fatalf("satisfied registration reported missing capabilities: %+v", immediate)
	}
	if s.PendingApproval() != nil {
		t.Fatal("a satisfied registration took the pending slot")
	}
	if _, ok := s.AccessRequest(); ok {
		t.Fatal("a satisfied registration published a request")
	}

	// A denial needs a request, so start from a session that lacks something.
	f := newAccessFixture(t)
	req := f.register(AccessIntent{System: true})
	denial := choiceFor(req, req.Current, "")
	denial.Approved = false
	prepared := false
	result, err := f.state.ResolveAccess(denial, func(AccessRequest, AccessChoice) (AccessCommit, error) {
		prepared = true
		return AccessCommit{Effective: AccessState{System: true}, Apply: func() {}}, nil
	})
	if err != nil {
		t.Fatalf("ResolveAccess(denied) = %v, want nil", err)
	}
	if result.Status != AccessDenied {
		t.Fatalf("ResolveAccess(denied) = %+v, want AccessDenied", result)
	}
	if result.Effective != req.Current || result.Effective != f.state.AccessState() {
		t.Fatalf("denial effective = %+v, want the actual pair %+v", result.Effective, req.Current)
	}
	if prepared {
		t.Fatal("a denial consulted the resolver")
	}
	if d := <-f.call.ResponseChan; d.Approved || d.Access == nil || d.Access.Status != AccessDenied {
		t.Fatalf("delivered decision = %+v, want an unapproved denial", d)
	}
	if got := f.state.AccessState(); got != req.Current {
		t.Fatalf("AccessState() = %+v after a denial, want %+v", got, req.Current)
	}

	// A request whose capability arrives while it waits resolves as available
	// without invoking the resolver's setter. The panel refreshes first, so the
	// choice carries the live pair (as it must) and still settles as available.
	f2 := newAccessFixture(t)
	f2.register(AccessIntent{System: true})
	f2.state.SetSystemAccess(true)
	live, ok := f2.state.AccessRequest()
	if !ok {
		t.Fatal("the request was dropped when its capability arrived")
	}
	if live.MissingEditing || live.MissingSystem {
		t.Fatalf("the request still reports missing capabilities: %+v", live)
	}
	settled := false
	result, err = f2.state.ResolveAccess(choiceFor(live, live.Current, ""), func(AccessRequest, AccessChoice) (AccessCommit, error) {
		settled = true
		return AccessCommit{Effective: AccessState{Mode: "default", System: true}, Apply: func() { settled = true }}, nil
	})
	if err != nil {
		t.Fatalf("ResolveAccess(satisfied later) = %v, want nil", err)
	}
	if result.Status != AccessAvailable {
		t.Fatalf("ResolveAccess(satisfied later) = %+v, want AccessAvailable", result)
	}
	if settled {
		t.Fatal("an already-available request reached the resolver")
	}
	if result.Effective != (AccessState{Mode: "default", System: true}) {
		t.Fatalf("available effective = %+v, want the actual pair", result.Effective)
	}
	if d := <-f2.call.ResponseChan; d.Approved || d.Access == nil || d.Access.Status != AccessAvailable {
		t.Fatalf("delivered decision = %+v, want an unapproved available result", d)
	}
}

// Shutdown and a concurrent approval settle the same request exactly once:
// the waiter always gets one canonical result, and no post-shutdown grant
// slips through.
func TestAccessShutdownSettlesOnce(t *testing.T) {
	t.Run("shutdown settles a waiting request", func(t *testing.T) {
		f := newAccessFixture(t)
		req := f.register(AccessIntent{System: true})
		if _, ok := f.state.AccessRequest(); !ok {
			t.Fatal("the request was not visible before shutdown")
		}
		f.state.ResolvePendingForShutdown()

		d := <-f.call.ResponseChan
		if d.Approved {
			t.Fatalf("shutdown delivered an approval: %+v", d)
		}
		if d.Access == nil || d.Access.RequestID != req.RequestID || d.Access.Status != AccessCanceled {
			t.Fatalf("shutdown decision = %+v, want a typed cancellation of %q", d, req.RequestID)
		}
		if f.state.PendingApproval() != nil {
			t.Fatal("shutdown left the request pending")
		}
		if _, ok := f.state.AccessRequest(); ok {
			t.Fatal("shutdown left the request readable")
		}
		if f.state.CancelAccess(req.RequestID, AccessCanceled, "after shutdown") {
			t.Fatal("a settled request was settled again")
		}
		// The channel is closed and carries nothing further.
		if d, ok := <-f.call.ResponseChan; ok || d.Access != nil || d.Approved {
			t.Fatalf("the canonical channel carried a second result: (%+v, %v)", d, ok)
		}
	})

	t.Run("no post-shutdown grant", func(t *testing.T) {
		f := newAccessFixture(t)
		req := f.register(AccessIntent{System: true})

		// Hold the gate inside a grant's Apply and quiesce from another
		// goroutine: the grant has passed its last check, so it must finish
		// and be acknowledged, and the shutdown must not produce a second
		// result for it.
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan AccessResult, 1)
		go func() {
			result, _ := f.state.ResolveAccess(choiceFor(req, req.Current, ""), func(AccessRequest, AccessChoice) (AccessCommit, error) {
				return AccessCommit{Effective: AccessState{Mode: "default", System: true}, Apply: func() {
					close(entered)
					<-release
				}}, nil
			})
			done <- result
		}()
		<-entered

		quiesced := make(chan struct{})
		go func() {
			f.state.BeginQuiesce()
			f.state.ResolvePendingForShutdown()
			close(quiesced)
		}()

		// The grant holds the access gate: shutdown cannot proceed past it.
		select {
		case <-quiesced:
			t.Fatal("shutdown completed while a grant was being applied")
		case <-time.After(50 * time.Millisecond):
		}

		close(release)
		if result := <-done; result.Status != AccessGranted {
			t.Fatalf("the accepted grant = %+v, want AccessGranted", result)
		}
		<-quiesced

		d := <-f.call.ResponseChan
		if !d.Approved || d.Access == nil || d.Access.Status != AccessGranted {
			t.Fatalf("delivered decision = %+v, want one typed grant", d)
		}
		if _, ok := <-f.call.ResponseChan; ok {
			t.Fatal("the channel carried a second result")
		}
		if got := f.state.AccessState(); got != (AccessState{Mode: "default", System: true}) {
			t.Fatalf("AccessState() = %+v, want the granted pair", got)
		}
	})

	t.Run("a quiescing session refuses new requests", func(t *testing.T) {
		f := newAccessFixture(t)
		f.state.BeginQuiesce()
		call := &PendingToolCall{ID: "late", ResponseChan: make(chan UserApprovalDecision, 1)}
		if _, waiting, err := f.state.RegisterAccess(context.Background(), call, AccessIntent{System: true}); !errors.Is(err, ErrSessionQuiescing) || waiting {
			t.Fatalf("RegisterAccess while quiescing = (waiting %v, err %v), want ErrSessionQuiescing", waiting, err)
		}
		if call.Access != nil || f.state.PendingApproval() != nil {
			t.Fatal("a quiescing session published a request")
		}
	})
}

// sendNowFixture is a session with one active eligible turn, one open
// operation and a scripted interruption handler. It lets the Send now
// arbitration tests drive acceptance without a real process or provider.
type sendNowFixture struct {
	t     *testing.T
	state *State
	turn  string
	ref   OperationRef
	opCtx context.Context

	mu    sync.Mutex
	calls []OperationRef
	begin func(ref OperationRef)
	stop  OperationStop
	err   error
}

func newSendNowFixture(t *testing.T) *sendNowFixture {
	t.Helper()
	f := &sendNowFixture{t: t, state: newTestState()}
	f.state.SetOperationInterrupt(f.interrupt)
	turn, err := f.state.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	f.turn = turn
	opCtx, ref, err := f.state.BeginSteeringOperation(context.Background(), turn, OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation = %v, want nil", err)
	}
	f.opCtx, f.ref = opCtx, ref
	return f
}

func (f *sendNowFixture) interrupt(_ context.Context, ref OperationRef) (OperationStop, error) {
	f.mu.Lock()
	f.calls = append(f.calls, ref)
	begin := f.begin
	f.mu.Unlock()
	if begin != nil {
		begin(ref)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stop, f.err
}

func (f *sendNowFixture) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *sendNowFixture) request(id, text string) SendNowRequest {
	return SendNowRequest{TurnID: f.turn, RequestID: id, Text: text}
}

// beginOperation opens a second operation on the fixture's turn.
func (f *sendNowFixture) beginOperation() (context.Context, OperationRef) {
	f.t.Helper()
	ctx, ref, err := f.state.BeginSteeringOperation(context.Background(), f.turn, OperationModel)
	if err != nil {
		f.t.Fatalf("BeginSteeringOperation = %v, want nil", err)
	}
	return ctx, ref
}

// TestSendNowAcceptanceFIFOAndDuplicateIDs pins the one-entry-per-submission
// rule: queue order is stable, a duplicate request ID returns its existing
// receipt without appending or stopping again even after a flush drained its
// queue, conflicting reuse is refused, and a post-closure retry rejects.
func TestSendNowAcceptanceFIFOAndDuplicateIDs(t *testing.T) {
	f := newSendNowFixture(t)
	f.state.PushSteering("queued")
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}

	first, err := f.state.SubmitSendNow(f.request("r1", "steer one"))
	if err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	if first.Status != SteeringAccepted {
		t.Fatalf("receipt status = %q, want accepted", first.Status)
	}
	if got := f.state.SteeringQueue(); len(got) != 2 || got[0] != "queued" || got[1] != "steer one" {
		t.Fatalf("queue = %v, want [queued steer one]", got)
	}

	// A retry of the same ID and text is idempotent and returns the latest
	// receipt for that ID, without appending a second entry.
	retry, err := f.state.SubmitSendNow(f.request("r1", "steer one"))
	if err != nil {
		t.Fatalf("duplicate SubmitSendNow = %v, want nil", err)
	}
	if retry.RequestID != "r1" {
		t.Fatalf("duplicate receipt = %+v, want the request r1", retry)
	}
	if got := f.state.SteeringQueue(); len(got) != 2 {
		t.Fatalf("queue after duplicate = %v, want the two entries unchanged", got)
	}

	// Reusing the ID with different text is a conflict, never a second entry.
	if _, err := f.state.SubmitSendNow(f.request("r1", "steer two")); !errors.Is(err, errSteeringConflict) {
		t.Fatalf("conflicting reuse = %v, want errSteeringConflict", err)
	}

	second, err := f.state.SubmitSendNow(f.request("r2", "steer three"))
	if err != nil {
		t.Fatalf("second SubmitSendNow = %v, want nil", err)
	}
	if second.RequestID != "r2" {
		t.Fatalf("second receipt = %+v, want r2", second)
	}
	if got := f.state.SteeringQueue(); len(got) != 3 || got[2] != "steer three" {
		t.Fatalf("queue = %v, want three entries in acceptance order", got)
	}

	// The flush consumed its queue; a retried flush stays idempotent.
	batch, err := f.state.StageSteering(f.turn)
	if err != nil {
		t.Fatalf("StageSteering = %v, want nil", err)
	}
	ids := make([]string, 0, len(batch.Messages))
	for _, m := range batch.Messages {
		ids = append(ids, m.ID)
	}
	f.state.MarkSteeringSubmitted(f.turn, ids)

	afterFlush, err := f.state.SubmitSendNow(f.request("r1", "steer one"))
	if err != nil {
		t.Fatalf("retry after flush = %v, want nil", err)
	}
	if afterFlush.RequestID != "r1" {
		t.Fatalf("retry after flush = %+v, want the same request", afterFlush)
	}
	if afterFlush.Version < first.Version {
		t.Fatalf("retry version = %d, want at least the accepted version %d", afterFlush.Version, first.Version)
	}

	// A retry must not stop the operation a second time.
	f.state.FinishSteeringOperation(f.ref, nil)
	if got := f.callCount(); got != 1 {
		t.Fatalf("interruption handler calls = %d, want exactly 1", got)
	}

	// After closure an old turn ID rejects even for a previously accepted ID.
	f.state.EndSteeringTurn(f.turn, "turn finished")
	if _, err := f.state.SubmitSendNow(f.request("r1", "steer one")); !errors.Is(err, errSteeringInactive) {
		t.Fatalf("post-closure retry = %v, want errSteeringInactive", err)
	}
}

// TestSendNowIntentPrecedesCancellationSignal pins that acceptance marks the
// captured operation as requested before any cancellation signal is sent, so
// an admission check that races with the request already sees the intent.
func TestSendNowIntentPrecedesCancellationSignal(t *testing.T) {
	f := newSendNowFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.begin = func(OperationRef) {
		close(entered)
		<-release
	}

	if _, err := f.state.SubmitSendNow(f.request("r1", "stop now")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the interruption handler never ran")
	}

	// The operation is still inside Begin (no cancellation signal yet), but
	// the intent is already visible and admission is already refused.
	if !SteeringRequested(f.opCtx) {
		t.Fatal("SteeringRequested = false while the interruption worker is still in Begin")
	}
	if _, _, err := f.state.BeginSteeringOperation(context.Background(), f.turn, OperationTools); !errors.Is(err, ErrSendNow) {
		t.Fatalf("BeginSteeringOperation during steering = %v, want ErrSendNow", err)
	}
	if err := CheckSteering(f.opCtx); !errors.Is(err, ErrSendNow) {
		t.Fatalf("CheckSteering = %v, want ErrSendNow", err)
	}
	select {
	case <-f.opCtx.Done():
		t.Fatal("the operation context was cancelled before the handler's Begin returned")
	default:
	}

	close(release)
	f.state.FinishSteeringOperation(f.ref, nil)
	select {
	case <-f.opCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the operation context was never cancelled")
	}
}

// TestSendNowCoalescesCapturedOperationOnly pins that several requests for one
// operation share its single stop, and that a late completion of an old
// operation cannot cancel the replacement that already took its place.
func TestSendNowCoalescesCapturedOperationOnly(t *testing.T) {
	f := newSendNowFixture(t)
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) {
		return []SteeringOutcome{{Kind: "command", State: "interrupted", ExecutionID: "exec-1"}}, nil
	}}

	if _, err := f.state.SubmitSendNow(f.request("r1", "one")); err != nil {
		t.Fatalf("SubmitSendNow r1 = %v, want nil", err)
	}
	if _, err := f.state.SubmitSendNow(f.request("r2", "two")); err != nil {
		t.Fatalf("SubmitSendNow r2 = %v, want nil", err)
	}

	// Finishing joins the worker, so the call count is settled, not sampled.
	outcomes := f.state.FinishSteeringOperation(f.ref, nil)
	if got := f.callCount(); got != 1 {
		t.Fatalf("interruption handler calls = %d, want 1 shared stop for both requests", got)
	}
	if len(outcomes) != 1 || outcomes[0].ExecutionID != "exec-1" {
		t.Fatalf("outcomes = %+v, want the worker's real result merged in", outcomes)
	}

	// Deliver the accepted steering, as the checkpoint would, so a new
	// operation is admitted. A replacement is running now; the old
	// operation's delayed cleanup must not clear or cancel it.
	delivered, err := f.state.StageSteering(f.turn)
	if err != nil {
		t.Fatalf("StageSteering = %v, want nil", err)
	}
	ids := make([]string, 0, len(delivered.Messages))
	for _, m := range delivered.Messages {
		ids = append(ids, m.ID)
	}
	f.state.MarkSteeringSubmitted(f.turn, ids)

	nextCtx, nextRef := f.beginOperation()
	if snap := f.state.SteeringSnapshot(); snap.OperationID != nextRef.ID {
		t.Fatalf("snapshot operation = %q, want the replacement %q", snap.OperationID, nextRef.ID)
	}
	f.state.FinishSteeringOperation(f.ref, nil)
	if snap := f.state.SteeringSnapshot(); snap.OperationID != nextRef.ID {
		t.Fatalf("snapshot operation after late cleanup = %q, want %q", snap.OperationID, nextRef.ID)
	}
	select {
	case <-nextCtx.Done():
		t.Fatalf("the replacement operation was cancelled by the old operation's late completion: %v", context.Cause(nextCtx))
	default:
	}
}

// TestSendNowStageSubmitAndRetryExactlyOnce pins the acceptance/submission
// split: staged text stays undelivered until the model request acknowledges it,
// and a retry neither re-stages nor re-persists it.
func TestSendNowStageSubmitAndRetryExactlyOnce(t *testing.T) {
	f := newSendNowFixture(t)
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}
	f.state.PushSteering("while busy")

	if _, err := f.state.SubmitSendNow(f.request("r1", "steer me")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}

	batch, err := f.state.StageSteering(f.turn)
	if err != nil {
		t.Fatalf("StageSteering = %v, want nil", err)
	}
	if len(batch.Messages) != 2 || batch.Messages[0].Text != "while busy" || batch.Messages[1].Text != "steer me" {
		t.Fatalf("staged batch = %+v, want both entries in order", batch.Messages)
	}

	// Staging is not delivery: the request is still undelivered.
	if !f.state.HasImmediateSteering(f.turn) {
		t.Fatal("HasImmediateSteering = false after staging, want true until submission")
	}
	// A second stage returns nothing: the ledger hands each entry out once.
	again, err := f.state.StageSteering(f.turn)
	if err != nil {
		t.Fatalf("second StageSteering = %v, want nil", err)
	}
	if len(again.Messages) != 0 || len(again.Notes) != 0 {
		t.Fatalf("second StageSteering = %+v, want an empty batch", again)
	}

	toSubmit := f.state.SteeringToSubmit(f.turn)
	if len(toSubmit.Messages) != 2 {
		t.Fatalf("SteeringToSubmit = %+v, want both staged entries", toSubmit.Messages)
	}
	ids := []string{toSubmit.Messages[0].ID, toSubmit.Messages[1].ID}
	f.state.MarkSteeringSubmitted(f.turn, ids)
	f.state.MarkSteeringSubmitted(f.turn, ids)

	if f.state.HasImmediateSteering(f.turn) {
		t.Fatal("HasImmediateSteering = true after every covered ID was submitted")
	}
	if retry := f.state.SteeringToSubmit(f.turn); len(retry.Messages) != 0 {
		t.Fatalf("SteeringToSubmit after submission = %+v, want nothing to re-persist", retry.Messages)
	}

	receipt, err := f.state.SubmitSendNow(f.request("r1", "steer me"))
	if err != nil {
		t.Fatalf("retry = %v, want nil", err)
	}
	if receipt.Status != SteeringDelivered {
		t.Fatalf("receipt status after submission = %q, want delivered", receipt.Status)
	}
}

// TestSendNowClosureRetainsUnsubmittedWithoutMarkerDuplicate pins that text the
// interruption marker already reported is not requeued by turn closure, while
// otherwise-unsubmitted text survives as ordinary follow-up content.
func TestSendNowClosureRetainsUnsubmittedWithoutMarkerDuplicate(t *testing.T) {
	f := newSendNowFixture(t)
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}

	if _, err := f.state.SubmitSendNow(f.request("r1", "reported by the marker")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	if _, err := f.state.StageSteering(f.turn); err != nil {
		t.Fatalf("StageSteering = %v, want nil", err)
	}

	archived := f.state.DrainSteeringForInterrupt()
	if len(archived) != 1 || archived[0] != "reported by the marker" {
		t.Fatalf("DrainSteeringForInterrupt = %v, want the pending text once", archived)
	}
	if again := f.state.DrainSteeringForInterrupt(); len(again) != 0 {
		t.Fatalf("second DrainSteeringForInterrupt = %v, want nothing twice", again)
	}

	// The marker reported the text, but no model request ever submitted it, so
	// the receipt stays truthfully undelivered rather than claiming delivery.
	receipt, err := f.state.SubmitSendNow(f.request("r1", "reported by the marker"))
	if err != nil {
		t.Fatalf("retry after drain = %v, want nil", err)
	}
	if receipt.Status == SteeringDelivered {
		t.Fatal("receipt claims delivered after only the interruption marker reported the text")
	}

	f.state.EndSteeringTurn(f.turn, "hard stop")
	if got := f.state.SteeringQueue(); len(got) != 0 {
		t.Fatalf("queue after closure = %v, want the archived text not requeued", got)
	}
	if f.state.HasImmediateSteering(f.turn) {
		t.Fatal("the closed turn still owes the transcript text the marker already reported")
	}

	// Ordinary unsubmitted text, by contrast, becomes follow-up content.
	f2 := newSendNowFixture(t)
	if _, err := f2.state.SubmitSendNow(f2.request("r1", "keep me")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	f2.state.EndSteeringTurn(f2.turn, "hard stop")
	if got := f2.state.SteeringQueue(); len(got) != 1 || got[0] != "keep me" {
		t.Fatalf("queue after closure = %v, want the undelivered text retained", got)
	}
	if f2.state.SteeringSnapshot().Active {
		t.Fatal("snapshot still reports an active turn after EndSteeringTurn")
	}
}

// TestSendNowCompletionAdmissionRace pins that final publication and an
// accepted deliverable message cannot both win: accepted work refuses
// finalization, and a closing turn refuses a new request.
func TestSendNowCompletionAdmissionRace(t *testing.T) {
	t.Run("accepted work prevents finalization", func(t *testing.T) {
		f := newSendNowFixture(t)
		f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}
		if _, err := f.state.SubmitSendNow(f.request("r1", "do not finish yet")); err != nil {
			t.Fatalf("SubmitSendNow = %v, want nil", err)
		}
		if f.state.TryFinishSteeringTurn(f.turn) {
			t.Fatal("TryFinishSteeringTurn = true with an accepted, undelivered message")
		}
		// Deliver it, then finalization is admitted exactly once.
		batch, err := f.state.StageSteering(f.turn)
		if err != nil {
			t.Fatalf("StageSteering = %v, want nil", err)
		}
		ids := []string{batch.Messages[0].ID}
		f.state.MarkSteeringSubmitted(f.turn, ids)
		if !f.state.TryFinishSteeringTurn(f.turn) {
			t.Fatal("TryFinishSteeringTurn = false after the message was delivered")
		}
		if f.state.TryFinishSteeringTurn(f.turn) {
			t.Fatal("TryFinishSteeringTurn succeeded twice for one turn")
		}
	})

	t.Run("a closing turn refuses a new request", func(t *testing.T) {
		f := newSendNowFixture(t)
		if !f.state.TryFinishSteeringTurn(f.turn) {
			t.Fatal("TryFinishSteeringTurn = false with no outstanding work")
		}
		f.state.PushSteering("queued before the race")
		if _, err := f.state.SubmitSendNow(f.request("late", "too late")); !errors.Is(err, errSteeringClosing) {
			t.Fatalf("SubmitSendNow on a closing turn = %v, want errSteeringClosing", err)
		}
		if got := f.state.SteeringQueue(); len(got) != 1 || got[0] != "queued before the race" {
			t.Fatalf("queue = %v, want the pre-existing entry preserved", got)
		}
	})
}

// TestSendNowHoldAndCommitBoundary pins that Send now never silently unholds
// the session, that an admitted commit unit finishes, that a later unit is
// refused, and that the release is idempotent.
func TestSendNowHoldAndCommitBoundary(t *testing.T) {
	f := newSendNowFixture(t)
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}
	f.state.SetHold(true)

	release, err := BeginSteeringMutation(f.opCtx)
	if err != nil {
		t.Fatalf("BeginSteeringMutation = %v, want nil", err)
	}
	if _, err := f.state.SubmitSendNow(f.request("r1", "interrupt")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	// The admitted unit still finishes.
	release()

	if !f.state.Held() {
		t.Fatal("SetHold lost the hold: Send now must not silently resume the session")
	}
	snap := f.state.SteeringSnapshot()
	if !snap.Held {
		t.Fatal("snapshot Held = false while the session is held")
	}
	// A unit that starts after the interruption is refused.
	if _, err := BeginSteeringMutation(f.opCtx); !errors.Is(err, ErrSendNow) {
		t.Fatalf("BeginSteeringMutation after interruption = %v, want ErrSendNow", err)
	}
	// Releasing twice is harmless.
	release()

	f.state.FinishSteeringOperation(f.ref, nil)
	f.state.SetHold(false)
}

// TestSendNowContextCausePreservesRealErrors pins that only a genuine steering
// cancellation is relabelled: a root cancel and a real operation failure keep
// their own cause.
func TestSendNowContextCausePreservesRealErrors(t *testing.T) {
	f := newSendNowFixture(t)
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}

	network := errors.New("dial tcp: connection refused")
	if IsSteeringCancellation(f.opCtx, network) {
		t.Fatal("IsSteeringCancellation = true for a real command/network failure")
	}
	if IsSteeringCancellation(f.opCtx, context.Canceled) {
		t.Fatal("IsSteeringCancellation = true before any steering was requested")
	}
	if err := CheckSteering(f.opCtx); err != nil {
		t.Fatalf("CheckSteering = %v, want nil while nothing was requested", err)
	}

	if _, err := f.state.SubmitSendNow(f.request("r1", "steer")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	if !IsSteeringCancellation(f.opCtx, ErrSendNow) {
		t.Fatal("IsSteeringCancellation = false for ErrSendNow after acceptance")
	}
	select {
	case <-f.opCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the operation context was never cancelled after acceptance")
	}
	if !errors.Is(context.Cause(f.opCtx), ErrSendNow) {
		t.Fatalf("operation cause = %v, want ErrSendNow", context.Cause(f.opCtx))
	}

	// A genuine cancellation of the whole session is not steering.
	root := newTestState()
	rootTurn, err := root.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	rootCtx, _, err := root.BeginSteeringOperation(context.Background(), rootTurn, OperationTools)
	if err != nil {
		t.Fatalf("BeginSteeringOperation = %v, want nil", err)
	}
	root.Shutdown()
	f.state.FinishSteeringOperation(f.ref, nil)
	if IsSteeringCancellation(rootCtx, rootCtx.Err()) {
		t.Fatal("IsSteeringCancellation = true for a session shutdown")
	}
}

// TestSendNowInvalidTargetAndUnsupportedAreNoOps pins that every rejection
// leaves the queue, the turn and the running operation untouched.
func TestSendNowInvalidTargetAndUnsupportedAreNoOps(t *testing.T) {
	t.Run("no active turn", func(t *testing.T) {
		s := newTestState()
		s.PushSteering("keep me")
		if _, err := s.SubmitSendNow(SendNowRequest{TurnID: "t1", RequestID: "r1", Text: "x"}); !errors.Is(err, errSteeringInactive) {
			t.Fatalf("SubmitSendNow with no turn = %v, want errSteeringInactive", err)
		}
		if got := s.SteeringQueue(); len(got) != 1 || got[0] != "keep me" {
			t.Fatalf("queue = %v, want it unchanged", got)
		}
	})

	t.Run("no handler installed", func(t *testing.T) {
		s := newTestState()
		turn, err := s.BeginSteeringTurn(context.Background())
		if err != nil {
			t.Fatalf("BeginSteeringTurn = %v, want nil", err)
		}
		if snap := s.SteeringSnapshot(); snap.Supported {
			t.Fatal("snapshot Supported = true with no interruption handler")
		}
		if _, err := s.SubmitSendNow(SendNowRequest{TurnID: turn, RequestID: "r1", Text: "x"}); !errors.Is(err, errSteeringUnsupported) {
			t.Fatalf("SubmitSendNow without a handler = %v, want errSteeringUnsupported", err)
		}
		if got := s.SteeringQueue(); len(got) != 0 {
			t.Fatalf("queue = %v, want nothing appended", got)
		}
	})

	t.Run("stale and malformed targets", func(t *testing.T) {
		f := newSendNowFixture(t)
		f.state.PushSteering("keep me")

		if _, err := f.state.SubmitSendNow(SendNowRequest{TurnID: "not-this-turn", RequestID: "r1", Text: "x"}); !errors.Is(err, errSteeringTarget) {
			t.Fatalf("wrong turn = %v, want errSteeringTarget", err)
		}
		if _, err := f.state.SubmitSendNow(SendNowRequest{SessionID: "other-session", TurnID: f.turn, RequestID: "r1", Text: "x"}); !errors.Is(err, errSteeringTarget) {
			t.Fatalf("wrong session = %v, want errSteeringTarget", err)
		}
		if _, err := f.state.SubmitSendNow(SendNowRequest{TurnID: f.turn, RequestID: "", Text: "x"}); !errors.Is(err, errSteeringRequestID) {
			t.Fatalf("missing request ID = %v, want errSteeringRequestID", err)
		}
		if got := f.state.SteeringQueue(); len(got) != 1 || got[0] != "keep me" {
			t.Fatalf("queue after rejections = %v, want it unchanged", got)
		}
		if f.state.HasImmediateSteering(f.turn) {
			t.Fatal("a rejected request registered immediate work")
		}
		if f.callCount() != 0 {
			t.Fatalf("interruption handler calls = %d, want 0 after rejections", f.callCount())
		}
	})

	t.Run("whitespace-only text with an empty queue is rejected", func(t *testing.T) {
		f := newSendNowFixture(t)
		if _, err := f.state.SubmitSendNow(f.request("r1", "   ")); !errors.Is(err, errSteeringEmptyFlush) {
			t.Fatalf("whitespace-only text with an empty queue = %v, want errSteeringEmptyFlush", err)
		}
		if f.state.HasImmediateSteering(f.turn) {
			t.Fatal("an empty flush registered immediate work")
		}
		if f.callCount() != 0 {
			t.Fatalf("interruption handler calls = %d, want 0 after a rejected flush", f.callCount())
		}
	})

	t.Run("a whitespace-only request flushes an existing queue", func(t *testing.T) {
		f := newSendNowFixture(t)
		f.state.PushSteering("queued")
		receipt, err := f.state.SubmitSendNow(f.request("r1", "  "))
		if err != nil {
			t.Fatalf("flush = %v, want nil", err)
		}
		if receipt.Status != SteeringAccepted {
			t.Fatalf("flush receipt = %+v, want accepted", receipt)
		}
		if got := f.state.SteeringQueue(); len(got) != 1 {
			t.Fatalf("flush appended an empty entry: queue = %v", got)
		}
	})
}

// TestSendNowEventsAreIndependentSnapshots pins that published snapshots and
// receipts are deep copies with their own revisions, and that a receipt never
// carries raw output text.
func TestSendNowEventsAreIndependentSnapshots(t *testing.T) {
	f := newSendNowFixture(t)
	broker := pubsub.NewBroker[SteeringEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := broker.Subscribe(ctx)
	f.state.SetSteeringBroker(broker)

	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) {
		return []SteeringOutcome{{
			Kind:        "command",
			State:       "interrupted",
			ExecutionID: "exec-7",
			Summary:     "stopped by user steering",
		}}, nil
	}}
	if _, err := f.state.SubmitSendNow(f.request("r1", "steer")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	f.state.FinishSteeringOperation(f.ref, nil)

	var accepted, settled *SteeringReceipt
	deadline := time.After(2 * time.Second)
	for settled == nil {
		select {
		case ev := <-events:
			if ev.Payload.Receipt == nil {
				continue
			}
			r := ev.Payload.Receipt
			if r.Status == SteeringAccepted {
				accepted = r
				continue
			}
			// The settled receipt is the one carrying the worker's real
			// outcomes; an earlier interrupting update carries none.
			if len(r.Outcomes) > 0 {
				settled = r
			}
		case <-deadline:
			t.Fatalf("timed out: accepted = %+v, settled = %+v", accepted, settled)
		}
	}
	if accepted == nil {
		t.Fatal("the accepted receipt was never published")
	}

	if settled.Version <= accepted.Version {
		t.Fatalf("settled receipt version = %d, want it above the accepted version %d", settled.Version, accepted.Version)
	}
	if len(settled.Outcomes) != 1 || settled.Outcomes[0].ExecutionID != "exec-7" {
		t.Fatalf("settled outcomes = %+v, want the worker's real target identity", settled.Outcomes)
	}
	for _, o := range settled.Outcomes {
		if strings.Contains(o.Summary, "stdout") || len(o.Summary) > 512 {
			t.Fatalf("receipt outcome carries unbounded or raw output: %+v", o)
		}
	}

	// Mutating a received copy must not reach session state.
	settled.Outcomes[0].ExecutionID = "tampered"
	settled.Status = SteeringUndelivered
	again, err := f.state.SubmitSendNow(f.request("r1", "steer"))
	if err != nil {
		t.Fatalf("retry = %v, want nil", err)
	}
	if len(again.Outcomes) != 1 || again.Outcomes[0].ExecutionID != "exec-7" {
		t.Fatalf("state changed through a received receipt copy: %+v", again.Outcomes)
	}
	if again.Status == SteeringUndelivered {
		t.Fatal("a subscriber's mutation of a receipt reached session state")
	}

	snap := f.state.SteeringSnapshot()
	if snap.Version == 0 {
		t.Fatal("snapshot version did not advance")
	}
	snap.TurnID = "tampered"
	if f.state.SteeringSnapshot().TurnID == "tampered" {
		t.Fatal("a subscriber's mutation of a snapshot reached session state")
	}
}
