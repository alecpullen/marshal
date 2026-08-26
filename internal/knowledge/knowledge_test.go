package knowledge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/llm/provider"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/registry"
)

type fakeProvider struct {
	response string
	err      error
	requests []schema.ChatRequest
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Models(ctx context.Context) ([]schema.ModelInfo, error) {
	return nil, nil
}
func (p *fakeProvider) Capabilities(ctx context.Context) schema.ProviderCapabilities {
	return schema.ProviderCapabilities{}
}
func (p *fakeProvider) Chat(ctx context.Context, req schema.ChatRequest) (<-chan schema.ChatEvent, error) {
	p.requests = append(p.requests, req)
	ch := make(chan schema.ChatEvent, 2)
	if p.err != nil {
		ch <- schema.ChatEvent{Type: schema.ChatEventError, Err: p.err}
		close(ch)
		return ch, nil
	}
	ch <- schema.ChatEvent{Type: schema.ChatEventDelta, Delta: p.response}
	ch <- schema.ChatEvent{Type: schema.ChatEventDone}
	close(ch)
	return ch, nil
}

type fakeRouteResolver struct {
	route routing.Route
	prov  provider.Provider
	err   error
}

func (r *fakeRouteResolver) Resolve(class string) (routing.Route, provider.Provider, error) {
	if r.err != nil {
		return routing.Route{}, nil, r.err
	}
	return r.route, r.prov, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate failed: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject failed: %v", err)
	}
	return database, projectID
}

func newTestState(t *testing.T, workingDir string) *session.State {
	t.Helper()
	return session.New(config.Default(), workingDir, time.Unix(100, 0), session.Persistence{})
}

func knowledgeRoute() routing.Route {
	return routing.Route{Role: routing.RoleKnowledge, Preset: routing.ModelPreset{Name: "tiny", Model: "tiny-model"}}
}

func TestEndSessionPersistsSummaryMemoriesAndFileSummaries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bar.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}

	database, projectID := newTestDB(t)
	sessionID := "sess-1"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	state := newTestState(t, dir)
	state.AddMessage(session.RoleUser, "Fix the bug in bar.go", session.ContentTypePlain)
	state.LogToolCall(registry.AuditEvent{
		ToolName:      "file.write_patch",
		ResultSummary: "applied patch",
		FilesChanged:  []string{"bar.go"},
	})
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "bar.go", Hash: "h1", LastIndexedAt: time.Unix(100, 0)},
	}); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	response := `{"session_summary":"Fixed the bug.","memories":[{"kind":"fact","content":"Uses SQLite for persistence"}],"file_summaries":{"bar.go":"Defines package foo"}}`
	prov := &fakeProvider{response: response}
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: prov}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    dir,
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	memories, err := database.GetMemories(projectID)
	if err != nil {
		t.Fatalf("GetMemories failed: %v", err)
	}
	if len(memories) != 1 || memories[0].Content != "Uses SQLite for persistence" {
		t.Fatalf("memories = %#v, want one fact memory", memories)
	}
	if memories[0].Confidence != db.MemoryConfidenceTentative {
		t.Fatalf("Confidence = %q, want tentative", memories[0].Confidence)
	}
	if memories[0].SourceSessionID != sessionID {
		t.Fatalf("SourceSessionID = %q, want %q", memories[0].SourceSessionID, sessionID)
	}

	gotSession, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if gotSession.Summary != "Fixed the bug." {
		t.Fatalf("Summary = %q, want %q", gotSession.Summary, "Fixed the bug.")
	}
	if gotSession.EndedAt == nil {
		t.Fatal("EndedAt is nil, want set")
	}

	files, err := database.GetFileIndex(projectID, 0)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %#v, want one row", files)
	}
	if files[0].Path != "bar.go" {
		t.Fatalf("Path = %q, want %q", files[0].Path, "bar.go")
	}
	if files[0].Summary != "Defines package foo" {
		t.Fatalf("Summary = %q, want %q", files[0].Summary, "Defines package foo")
	}
	if len(prov.requests) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(prov.requests))
	}
	if prov.requests[0].Stream {
		t.Fatal("knowledge chat request used streaming, want non-streaming")
	}
}

func TestEndSessionSkipsWhenNoUserMessages(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-empty"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	resolver := &fakeRouteResolver{err: errors.New("should not be called")}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil || got.Summary != "" {
		t.Fatalf("expected no session-end write, got %#v", got)
	}
}

func TestEndSessionSwallowsRouteResolutionError(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-route-err"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: &fakeRouteResolver{err: errors.New("no route configured")},
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected no session-end write after route error, got %#v", got)
	}
}

func TestEndSessionSwallowsChatError(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-chat-err"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{err: errors.New("connection refused")}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected no session-end write after chat error, got %#v", got)
	}
}

func TestEndSessionSwallowsParseFailure(t *testing.T) {
	database, projectID := newTestDB(t)
	sessionID := "sess-parse-err"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	state := newTestState(t, t.TempDir())
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{response: "not json at all"}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    t.TempDir(),
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	got, err := database.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.EndedAt != nil {
		t.Fatalf("expected no session-end write after parse failure, got %#v", got)
	}
}

func TestEndSessionIgnoresFileSummaryOutsideTouchedFiles(t *testing.T) {
	dir := t.TempDir()
	database, projectID := newTestDB(t)
	sessionID := "sess-untouched"
	if err := database.CreateSession(sessionID, projectID, "", time.Unix(100, 0)); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "untouched.go", Hash: "h1", LastIndexedAt: time.Unix(100, 0)},
	}); err != nil {
		t.Fatalf("SaveFileIndex failed: %v", err)
	}

	state := newTestState(t, dir)
	state.AddMessage(session.RoleUser, "hello", session.ContentTypePlain)
	// No tool calls logged, so FilesChanged is empty — nothing is "touched".

	response := `{"session_summary":"did nothing","memories":[],"file_summaries":{"untouched.go":"should be ignored"}}`
	resolver := &fakeRouteResolver{route: knowledgeRoute(), prov: &fakeProvider{response: response}}

	EndSession(context.Background(), EndSessionInput{
		DB:            database,
		ProjectID:     projectID,
		SessionID:     sessionID,
		State:         state,
		RouteResolver: resolver,
		WorkingDir:    dir,
		Now:           func() time.Time { return time.Unix(200, 0) },
		Logger:        testLogger(),
	})

	files, err := database.GetFileIndex(projectID, 0)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Summary != "" {
		t.Fatalf("files = %#v, want summary untouched (empty)", files)
	}
}

func TestReadTouchedFilesTruncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	largePath := "big.txt"
	content := strings.Repeat("x", 100000)
	if err := os.WriteFile(filepath.Join(dir, largePath), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	auditLog := []registry.AuditEvent{
		{FilesChanged: []string{largePath}},
	}
	got := readTouchedFiles(dir, auditLog, 65536)
	if len(got) != 1 {
		t.Fatalf("expected 1 file, got %d", len(got))
	}
	result := got[largePath]
	if !strings.Contains(result, "[... file truncated at 65536 bytes ...]") {
		t.Fatalf("expected truncation marker, got %d bytes", len(result))
	}
	if len(result) > 65536+100 { // marker text is short
		t.Fatalf("result too large: %d bytes", len(result))
	}
}

func TestReadTouchedFilesReadsSmallFile(t *testing.T) {
	dir := t.TempDir()
	smallPath := "small.txt"
	content := "hello world"
	if err := os.WriteFile(filepath.Join(dir, smallPath), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	auditLog := []registry.AuditEvent{
		{FilesChanged: []string{smallPath}},
	}
	got := readTouchedFiles(dir, auditLog, 65536)
	if got[smallPath] != content {
		t.Fatalf("expected full content, got %q", got[smallPath])
	}
}

func TestHasUserMessage(t *testing.T) {
	msgs := []session.Message{
		{Role: session.RoleSystem, Content: "system note"},
	}
	if HasUserMessage(msgs) {
		t.Fatal("HasUserMessage = true for system-only messages")
	}
	msgs = append(msgs, session.Message{Role: session.RoleUser, Content: "hello"})
	if !HasUserMessage(msgs) {
		t.Fatal("HasUserMessage = false when a user message is present")
	}
}
