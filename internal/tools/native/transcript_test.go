package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

// newTranscriptFixture builds a toolSet with a live session state (id
// "current") and an in-memory db, mirroring the local fixture style in
// sessions_mail_test.go.
func newTranscriptFixture(t *testing.T) (*toolSet, *db.DB, *session.State) {
	t.Helper()
	tmp := t.TempDir()
	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbConn.Close() })
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	state := session.New(config.Default(), tmp, time.Unix(100, 0).UTC(), session.Persistence{SessionID: "current"})
	ts, err := newToolSet(Options{
		WorkspaceRoot: tmp,
		DB:            dbConn,
		ProjectID:     projectID,
		SessionState:  state,
	})
	if err != nil {
		t.Fatalf("newToolSet: %v", err)
	}
	return ts, dbConn, state
}

// seedTranscript appends alternating user/final-assistant turns so the
// filtered ordinal numbering is predictable.
func seedTranscript(state *session.State, pairs int) {
	for i := 1; i <= pairs; i++ {
		state.AddMessage(session.RoleUser, "user turn "+itoa(i), session.ContentTypePlain)
		state.AddMessageFinal(session.RoleAssistant, "assistant answer "+itoa(i), session.ContentTypeMarkdown)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestTranscriptLiveDefaultShowsFinalAssistant(t *testing.T) {
	ts, _, state := newTranscriptFixture(t)
	state.AddMessage(session.RoleUser, "please summarise", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "here is the full assistant proposal body", session.ContentTypeMarkdown)

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(res.Content, "here is the full assistant proposal body") {
		t.Fatalf("expected full assistant content, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "--- turn 1 (user) ---") {
		t.Fatalf("expected turn 1 header, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "--- turn 2 (assistant) ---") {
		t.Fatalf("expected turn 2 assistant header, got:\n%s", res.Content)
	}
}

func TestTranscriptExcludesNonFinalAssistantByDefault(t *testing.T) {
	ts, _, state := newTranscriptFixture(t)
	state.AddMessage(session.RoleUser, "question", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "narration only", session.ContentTypeNarration)
	state.AddMessageFinal(session.RoleAssistant, "the answer", session.ContentTypeMarkdown)

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if strings.Contains(res.Content, "narration only") {
		t.Fatalf("non-final narration should be hidden by default, got:\n%s", res.Content)
	}

	resAll, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"include_all":true}`)})
	if err != nil {
		t.Fatalf("transcript_read include_all: %v", err)
	}
	if !strings.Contains(resAll.Content, "narration only") {
		t.Fatalf("include_all should reveal narration, got:\n%s", resAll.Content)
	}
}

func TestTranscriptTurnSeqReturnsOneTurn(t *testing.T) {
	ts, _, state := newTranscriptFixture(t)
	seedTranscript(state, 3)

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"turn_seq":3}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(res.Content, "--- turn 3 (user) ---") {
		t.Fatalf("expected turn 3 header, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "--- turn 4") || strings.Contains(res.Content, "--- turn 2") {
		t.Fatalf("turn_seq should return exactly one turn, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "[truncated") {
		t.Fatalf("turn_seq must not emit a continuation footer, got:\n%s", res.Content)
	}
}

func TestTranscriptPagingSlicesAndFooterNamesNextOffset(t *testing.T) {
	ts, _, state := newTranscriptFixture(t)
	seedTranscript(state, 4) // 8 turns

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"offset":2,"limit":2}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	// offset 2 skips the first two turns, so ordinals 3 and 4 are shown.
	if !strings.Contains(res.Content, "--- turn 3 (user) ---") || !strings.Contains(res.Content, "--- turn 4 (assistant) ---") {
		t.Fatalf("expected turns 3 and 4, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "--- turn 5") || strings.Contains(res.Content, "--- turn 2") {
		t.Fatalf("limit=2 should show exactly two turns, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "[truncated — page with offset=4]") {
		t.Fatalf("expected footer naming next offset 4, got:\n%s", res.Content)
	}
}

func TestTranscriptMaxCharsTruncationFooter(t *testing.T) {
	ts, _, state := newTranscriptFixture(t)
	seedTranscript(state, 10) // 20 turns, well over the budget

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"max_chars":120}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(res.Content, "[truncated — page with offset=") {
		t.Fatalf("expected truncation footer, got:\n%s", res.Content)
	}
	if len([]rune(res.Content)) > 400 {
		t.Fatalf("result should be bounded well below the full transcript, got %d runes", len([]rune(res.Content)))
	}
}

func TestTranscriptArchivedGeneration(t *testing.T) {
	ts, dbConn, _ := newTranscriptFixture(t)
	now := time.Unix(200, 0).UTC()
	if err := dbConn.CreateSession("current", ts.projectID, "current", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := dbConn.BeginGeneration(db.Generation{ID: "gen-1", SessionID: "current", Seq: 1, StartedAt: now}); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	turns := []db.ArchivedTurn{
		{TurnSeq: 1, Role: "user", Content: "archived question", CreatedAt: now},
		{TurnSeq: 2, Role: "assistant", Content: "archived answer text", CreatedAt: now},
	}
	if err := dbConn.ArchiveTurns("gen-1", turns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}
	if err := dbConn.EndGeneration("gen-1", now.Add(time.Minute), "completed"); err != nil {
		t.Fatalf("EndGeneration: %v", err)
	}

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":1}`)})
	if err != nil {
		t.Fatalf("transcript_read archived: %v", err)
	}
	if !strings.Contains(res.Content, "archived answer text") {
		t.Fatalf("expected archived turn content, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "--- turn 2 (assistant) ---") {
		t.Fatalf("expected archived turn header, got:\n%s", res.Content)
	}
}

func TestTranscriptGenerationSeqWithoutDBErrors(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0).UTC(), session.Persistence{SessionID: "current"})
	tool := NewTranscriptTool(state, nil)
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":1}`)})
	if err == nil {
		t.Fatal("expected an error when generation_seq is set but no db is available")
	}
}

func TestTranscriptRegisteredAlongsideSessionTools(t *testing.T) {
	root := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0).UTC(), session.Persistence{SessionID: "current"})
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, SessionState: state}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("transcript_read")
	if !ok {
		t.Fatal("transcript_read not registered")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("expected RiskReadOnly, got %v", tool.Risk)
	}
}
