package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
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
	state := newTestState()
	state.Shutdown()

	select {
	case <-state.Done():
	case <-time.After(time.Second):
		t.Fatal("state was not cancelled")
	}
}

func TestSetProviderErrorStoresAndRetrieves(t *testing.T) {
	state := newTestState()

	testErr := errors.New("provider connection failed")
	state.SetProviderError(testErr)

	got := state.ProviderError()
	if !errors.Is(got, testErr) {
		t.Fatalf("ProviderError() = %v, want %v", got, testErr)
	}
}

func TestSetProviderErrorNilClearsExistingError(t *testing.T) {
	state := newTestState()

	testErr := errors.New("provider connection failed")
	state.SetProviderError(testErr)

	state.SetProviderError(nil)

	got := state.ProviderError()
	if got != nil {
		t.Fatalf("ProviderError() = %v, want nil", got)
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

	state.ClearTurnToolCache()
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
	if g := s.SDDGate(); g.Kind != "" {
		t.Fatalf("initial gate should be empty, got %+v", g)
	}
	s.SetSDDGate(SDDGate{Kind: "spec_approval", Reason: "spec.md needs approval"})
	g := s.SDDGate()
	if g.Kind != "spec_approval" || g.Reason != "spec.md needs approval" {
		t.Fatalf("gate = %+v", g)
	}
	s.ClearSDDGate()
	if g := s.SDDGate(); g.Kind != "" {
		t.Fatalf("cleared gate should be empty, got %+v", g)
	}
}
