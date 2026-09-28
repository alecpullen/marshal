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

// seedArchive writes one ended generation with the given sequence number and
// turns. Turn seqs are the caller's business: production archives them
// 0-based (rollover.Controller.Archive assigns startSeq+i from 0), so tests
// must seed them that way too or they mask the recall_history numbering.
func seedArchive(t *testing.T, dbConn *db.DB, sessionID, genID string, seq int, turns []db.ArchivedTurn, now time.Time) {
	t.Helper()
	if err := dbConn.BeginGeneration(db.Generation{ID: genID, SessionID: sessionID, Seq: seq, StartedAt: now}); err != nil {
		t.Fatalf("BeginGeneration: %v", err)
	}
	if err := dbConn.ArchiveTurns(genID, turns, 1024, now); err != nil {
		t.Fatalf("ArchiveTurns: %v", err)
	}
	if err := dbConn.EndGeneration(genID, now.Add(time.Minute), "completed"); err != nil {
		t.Fatalf("EndGeneration: %v", err)
	}
}

// seedCurrentSession registers the session row the generation FK points at.
func seedCurrentSession(t *testing.T, dbConn *db.DB, projectID int64, now time.Time) {
	t.Helper()
	if err := dbConn.CreateSession("current", projectID, "current", now); err != nil {
		t.Fatalf("CreateSession: %v", err)
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

// TestTranscriptOffsetPastEndSignalsNoTurns pins the end-of-transcript
// signal: a model that pages past the last turn must be told it is past the
// end, not handed an empty result it could mistake for an empty transcript.
func TestTranscriptOffsetPastEndSignalsNoTurns(t *testing.T) {
	ts, _, state := newTranscriptFixture(t)
	seedTranscript(state, 1) // 2 turns

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"offset":99}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(res.Content, "No turns at offset 99 (2 turn(s) total)") {
		t.Fatalf("expected an explicit past-the-end message, got:\n%s", res.Content)
	}
}

// TestTranscriptArchivedGeneration seeds generation 0 (the seq every session
// actually starts at, via rollover.Controller.Start) with 0-based turn seqs
// (what Controller.Archive writes). Both are what production produces, so
// the test exercises the real numbering rather than a convenient one.
func TestTranscriptArchivedGeneration(t *testing.T) {
	ts, dbConn, _ := newTranscriptFixture(t)
	now := time.Unix(200, 0).UTC()
	seedCurrentSession(t, dbConn, ts.projectID, now)
	seedArchive(t, dbConn, "current", "gen-0", 0, []db.ArchivedTurn{
		{TurnSeq: 0, Role: "system", Content: "archived system prompt", CreatedAt: now},
		{TurnSeq: 1, Role: "assistant", Content: "archived answer text", CreatedAt: now},
	}, now)

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0}`)})
	if err != nil {
		t.Fatalf("transcript_read archived: %v", err)
	}
	if !strings.Contains(res.Content, "Generation 0 (ended)") {
		t.Fatalf("expected the generation header, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "archived answer text") {
		t.Fatalf("expected archived turn content, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "--- turn 0 (system) ---") {
		t.Fatalf("expected the stored turn seq 0 to be printed as-is, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "--- turn 1 (assistant) ---") {
		t.Fatalf("expected stored turn seq 1 to be printed as-is, got:\n%s", res.Content)
	}
}

// TestTranscriptArchivedTurnSeqMatchesRecallHistoryNumbering pins the
// contract the schema advertises: the number recall_history prints next to a
// turn is the number transcript_read's turn_seq accepts, for archived turns.
// The first archived turn is seq 0, so 0 must be selectable and must not be
// mistaken for "field omitted".
func TestTranscriptArchivedTurnSeqMatchesRecallHistoryNumbering(t *testing.T) {
	ts, dbConn, _ := newTranscriptFixture(t)
	now := time.Unix(200, 0).UTC()
	seedCurrentSession(t, dbConn, ts.projectID, now)
	seedArchive(t, dbConn, "current", "gen-0", 0, []db.ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: "first archived turn", CreatedAt: now},
		{TurnSeq: 1, Role: "assistant", Content: "second archived turn", CreatedAt: now},
	}, now)

	// turn_seq 0 selects the turn recall_history reports as turn 0.
	first, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0,"turn_seq":0}`)})
	if err != nil {
		t.Fatalf("transcript_read turn_seq=0: %v", err)
	}
	if !strings.Contains(first.Content, "first archived turn") {
		t.Fatalf("turn_seq 0 should return the first turn, got:\n%s", first.Content)
	}
	if strings.Contains(first.Content, "second archived turn") {
		t.Fatalf("turn_seq 0 should return exactly one turn, got:\n%s", first.Content)
	}

	second, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0,"turn_seq":1}`)})
	if err != nil {
		t.Fatalf("transcript_read turn_seq=1: %v", err)
	}
	if !strings.Contains(second.Content, "second archived turn") || strings.Contains(second.Content, "first archived turn") {
		t.Fatalf("turn_seq 1 should return the second turn, got:\n%s", second.Content)
	}
	if !strings.Contains(second.Content, "--- turn 1 (assistant) ---") {
		t.Fatalf("expected the stored seq in the header, got:\n%s", second.Content)
	}
	// The generation header survives the single-turn path so the model knows
	// which generation it just read.
	if !strings.Contains(second.Content, "Generation 0 (ended)") {
		t.Fatalf("expected the generation header on the turn_seq path, got:\n%s", second.Content)
	}
}

// TestTranscriptArchivedDoesNotSplitQuotedTurnHeader is the regression the row
// read exists for: archived content that quotes a turn header must stay one
// turn. Splitting the rendered dump on that literal fabricated turns, invented
// a role for each, and renumbered them.
func TestTranscriptArchivedDoesNotSplitQuotedTurnHeader(t *testing.T) {
	ts, dbConn, _ := newTranscriptFixture(t)
	now := time.Unix(200, 0).UTC()
	seedCurrentSession(t, dbConn, ts.projectID, now)
	quoted := "here is a transcript excerpt:\n--- turn 7 (assistant) ---\nthat header is quoted literal text"
	seedArchive(t, dbConn, "current", "gen-0", 0, []db.ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: quoted, CreatedAt: now},
		{TurnSeq: 1, Role: "assistant", Content: "the real answer", CreatedAt: now},
	}, now)

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	// The quoted header must stay inside turn 0's body — that is, between
	// turn 0's header and turn 1's — rather than becoming its own turn.
	quotedAt := strings.Index(res.Content, "that header is quoted literal text")
	turn0At := strings.Index(res.Content, "--- turn 0 (user) ---")
	turn1At := strings.Index(res.Content, "--- turn 1 (assistant) ---")
	if quotedAt < 0 {
		t.Fatalf("quoted content should survive intact, got:\n%s", res.Content)
	}
	if !(turn0At < quotedAt && quotedAt < turn1At) {
		t.Fatalf("quoted header should remain inside turn 0's body, got:\n%s", res.Content)
	}

	// The quoted header is not a turn: selecting seq 7 must miss, and the
	// miss must name only the two stored seqs.
	miss, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0,"turn_seq":7}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(miss.Content, "No turn 7 in transcript (2 turn(s) available: 0, 1)") {
		t.Fatalf("the quoted header must not become a selectable turn, got:\n%s", miss.Content)
	}

	// turn_seq must select by stored seq, not by fabricating turns out of
	// the quoted header.
	one, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0,"turn_seq":1}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(one.Content, "the real answer") || strings.Contains(one.Content, "quoted literal text") {
		t.Fatalf("turn_seq 1 should be the real second turn, got:\n%s", one.Content)
	}
}

// TestTranscriptArchivedNotFoundListsStoredSeqs checks that a miss names the
// numbers the headers actually print, including 0.
func TestTranscriptArchivedNotFoundListsStoredSeqs(t *testing.T) {
	ts, dbConn, _ := newTranscriptFixture(t)
	now := time.Unix(200, 0).UTC()
	seedCurrentSession(t, dbConn, ts.projectID, now)
	seedArchive(t, dbConn, "current", "gen-0", 0, []db.ArchivedTurn{
		{TurnSeq: 0, Role: "user", Content: "only turn", CreatedAt: now},
	}, now)

	res, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0,"turn_seq":9}`)})
	if err != nil {
		t.Fatalf("transcript_read: %v", err)
	}
	if !strings.Contains(res.Content, "No turn 9 in transcript (1 turn(s) available: 0)") {
		t.Fatalf("expected the miss to name the stored seqs, got:\n%s", res.Content)
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

// TestTranscriptGenerationSeqZeroWithoutDBErrors is the pointer plumbing's
// regression test: an explicit 0 must reach the archived branch (and fail
// there for want of a db) rather than silently falling through to the live
// transcript.
func TestTranscriptGenerationSeqZeroWithoutDBErrors(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0).UTC(), session.Persistence{SessionID: "current"})
	state.AddMessage(session.RoleUser, "live question", session.ContentTypePlain)
	tool := NewTranscriptTool(state, nil)
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":0}`)})
	if err == nil {
		t.Fatal("generation_seq 0 must take the archived path, not the live one")
	}
	if !strings.Contains(err.Error(), "generation_seq 0") {
		t.Fatalf("expected the error to name generation_seq 0, got: %v", err)
	}
}

func TestTranscriptNegativeGenerationSeqRejected(t *testing.T) {
	ts, _, _ := newTranscriptFixture(t)
	_, err := ts.transcriptReadTool().Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(`{"generation_seq":-1}`)})
	if err == nil {
		t.Fatal("expected a negative generation_seq to be rejected")
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
