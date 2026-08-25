package db

import (
	"path/filepath"
	"testing"
	"time"
)

func openMetricsTestDB(t *testing.T) (*DB, int64) {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/tmp/proj", "proj")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}

func sampleRow(projectID int64, sessionID string) TurnMetricsRow {
	return TurnMetricsRow{
		ProjectID:        projectID,
		SessionID:        sessionID,
		StartedAt:        time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		DurationMs:       1234,
		Class:            "question",
		Role:             "general",
		Provider:         "scripted",
		Model:            "test-model",
		Goal:             "how does pkg work?",
		Iterations:       3,
		ToolCalls:        2,
		ToolErrors:       1,
		CacheHits:        1,
		ParseFailures:    1,
		HardStalls:       0,
		Outcome:          "answered",
		SalvageReason:    "",
		PromptTokens:     17,
		CompletionTokens: 8,
	}
}

func TestInsertAndRecentTurnMetricsRoundTrip(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	if err := database.CreateSession("sess_1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	want := sampleRow(projectID, "sess_1")
	id, err := database.InsertTurnMetrics(want)
	if err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertTurnMetrics returned id 0")
	}

	rows, err := database.RecentTurnMetrics(projectID, 10)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	want.ID = got.ID
	if got != want {
		t.Fatalf("row mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestInsertTurnMetricsNullSessionID(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	row := sampleRow(projectID, "")
	if _, err := database.InsertTurnMetrics(row); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	rows, err := database.RecentTurnMetrics(projectID, 1)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 || rows[0].SessionID != "" {
		t.Fatalf("rows = %+v, want one row with empty SessionID", rows)
	}
}

func TestRecentTurnMetricsClampsNonPositiveLimit(t *testing.T) {
	db, projectID := openMetricsTestDB(t)
	if err := db.CreateSession("s1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := db.InsertTurnMetrics(sampleRow(projectID, "s1")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	for _, limit := range []int{0, -1, -100} {
		rows, err := db.RecentTurnMetrics(projectID, limit)
		if err != nil {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if len(rows) != 1 {
			t.Errorf("limit=%d: expected 1 row, got %d", limit, len(rows))
		}
	}
}

func TestRecentTurnMetricsNewestFirstAndLimited(t *testing.T) {
	database, projectID := openMetricsTestDB(t)

	for i := 0; i < 3; i++ {
		row := sampleRow(projectID, "")
		row.Iterations = i + 1
		if _, err := database.InsertTurnMetrics(row); err != nil {
			t.Fatalf("InsertTurnMetrics %d: %v", i, err)
		}
	}
	rows, err := database.RecentTurnMetrics(projectID, 2)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (limit)", len(rows))
	}
	if rows[0].Iterations != 3 || rows[1].Iterations != 2 {
		t.Fatalf("order = %d,%d; want newest first (3,2)", rows[0].Iterations, rows[1].Iterations)
	}
}

func TestInsertAndRecentTurnMetricsWithNewFields(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	if err := database.CreateSession("sess_newfields", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	want := sampleRow(projectID, "sess_newfields")
	want.ReasoningTokens = 42
	want.CacheReadTokens = 10
	want.CacheWriteTokens = 5
	want.EstimatedCostCents = 99

	id, err := database.InsertTurnMetrics(want)
	if err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	if id == 0 {
		t.Fatal("InsertTurnMetrics returned id 0")
	}

	rows, err := database.RecentTurnMetrics(projectID, 10)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	want.ID = got.ID
	if got != want {
		t.Fatalf("row mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestInsertTurnMetricsPrunesOldRows(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	for i := 0; i < maxTurnMetricsRows+2; i++ {
		row := sampleRow(projectID, "")
		row.Iterations = i + 1
		if _, err := database.InsertTurnMetrics(row); err != nil {
			t.Fatalf("InsertTurnMetrics %d: %v", i, err)
		}
	}
	// RecentTurnMetrics caps its result at recentTurnMetricsMaxLimit, so verify
	// the total pruned row count directly and then check the capped view.
	var total int
	if err := database.sqlDB.QueryRow("SELECT COUNT(*) FROM turn_metrics WHERE project_id = ?", projectID).Scan(&total); err != nil {
		t.Fatalf("count turn_metrics: %v", err)
	}
	if total != maxTurnMetricsRows {
		t.Fatalf("after prune got %d total rows, want %d", total, maxTurnMetricsRows)
	}

	rows, err := database.RecentTurnMetrics(projectID, recentTurnMetricsMaxLimit)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != recentTurnMetricsMaxLimit {
		t.Fatalf("RecentTurnMetrics returned %d rows, want %d", len(rows), recentTurnMetricsMaxLimit)
	}
	// RecentTurnMetrics returns newest first. We inserted max+2 rows with
	// iterations 1..max+2; pruning deletes the two oldest, so the newest
	// retained row has iterations max+2 and the oldest retained has 3.
	wantNewest := maxTurnMetricsRows + 2
	wantOldestVisible := wantNewest - recentTurnMetricsMaxLimit + 1
	if rows[0].Iterations != wantNewest {
		t.Errorf("newest iterations = %d, want %d", rows[0].Iterations, wantNewest)
	}
	if rows[len(rows)-1].Iterations != wantOldestVisible {
		t.Errorf("oldest visible iterations = %d, want %d", rows[len(rows)-1].Iterations, wantOldestVisible)
	}
}

func TestAggregateTurnMetrics(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	if err := database.CreateSession("sess_agg", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Insert three rows: two gpt-4o (different costs), one local.
	rows := []TurnMetricsRow{
		{
			ProjectID: projectID, SessionID: "sess_agg",
			StartedAt:  time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
			DurationMs: 100, Class: "question", Role: "general",
			Provider: "openai", Model: "gpt-4o",
			Goal: "q1", Iterations: 1, ToolCalls: 0, ToolErrors: 0,
			CacheHits: 0, ParseFailures: 0, HardStalls: 0,
			Outcome: "answered", SalvageReason: "",
			PromptTokens: 10, CompletionTokens: 20,
			ReasoningTokens: 0, CacheReadTokens: 5, CacheWriteTokens: 2,
			EstimatedCostCents: 30,
		},
		{
			ProjectID: projectID, SessionID: "sess_agg",
			StartedAt:  time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC),
			DurationMs: 200, Class: "question", Role: "general",
			Provider: "openai", Model: "gpt-4o",
			Goal: "q2", Iterations: 2, ToolCalls: 1, ToolErrors: 0,
			CacheHits: 1, ParseFailures: 0, HardStalls: 0,
			Outcome: "answered", SalvageReason: "",
			PromptTokens: 15, CompletionTokens: 25,
			ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 1,
			EstimatedCostCents: 50,
		},
		{
			ProjectID: projectID, SessionID: "sess_agg",
			StartedAt:  time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC),
			DurationMs: 50, Class: "question", Role: "general",
			Provider: "ollama", Model: "local-model",
			Goal: "q3", Iterations: 1, ToolCalls: 0, ToolErrors: 0,
			CacheHits: 0, ParseFailures: 0, HardStalls: 0,
			Outcome: "answered", SalvageReason: "",
			PromptTokens: 5, CompletionTokens: 3,
			ReasoningTokens: 0, CacheReadTokens: 0, CacheWriteTokens: 0,
			EstimatedCostCents: 2,
		},
	}
	for i, r := range rows {
		if _, err := database.InsertTurnMetrics(r); err != nil {
			t.Fatalf("InsertTurnMetrics[%d]: %v", i, err)
		}
	}

	totals, breakdown, err := database.AggregateTurnMetrics(projectID)
	if err != nil {
		t.Fatalf("AggregateTurnMetrics: %v", err)
	}

	// Grand totals: (10+15+5)=30 prompt, (20+25+3)=48 completion,
	// (0+5+0)=5 reasoning, (5+3+0)=8 cache_read, (2+1+0)=3 cache_write,
	// (30+50+2)=82 cost, 3 turns.
	if totals.PromptTokens != 30 {
		t.Errorf("PromptTokens = %d, want 30", totals.PromptTokens)
	}
	if totals.CompletionTokens != 48 {
		t.Errorf("CompletionTokens = %d, want 48", totals.CompletionTokens)
	}
	if totals.ReasoningTokens != 5 {
		t.Errorf("ReasoningTokens = %d, want 5", totals.ReasoningTokens)
	}
	if totals.CacheReadTokens != 8 {
		t.Errorf("CacheReadTokens = %d, want 8", totals.CacheReadTokens)
	}
	if totals.CacheWriteTokens != 3 {
		t.Errorf("CacheWriteTokens = %d, want 3", totals.CacheWriteTokens)
	}
	if totals.EstimatedCostCents != 82 {
		t.Errorf("EstimatedCostCents = %d, want 82", totals.EstimatedCostCents)
	}
	if totals.Turns != 3 {
		t.Errorf("Turns = %d, want 3", totals.Turns)
	}

	// Breakdown: two entries, sorted by cost DESC (gpt-4o first, then local).
	if len(breakdown) != 2 {
		t.Fatalf("len(breakdown) = %d, want 2", len(breakdown))
	}

	// First: gpt-4o (cost 30+50=80)
	b0 := breakdown[0]
	if b0.Provider != "openai" || b0.Model != "gpt-4o" {
		t.Errorf("breakdown[0] = %s/%s, want openai/gpt-4o", b0.Provider, b0.Model)
	}
	if b0.PromptTokens != 25 {
		t.Errorf("b0.PromptTokens = %d, want 25", b0.PromptTokens)
	}
	if b0.CompletionTokens != 45 {
		t.Errorf("b0.CompletionTokens = %d, want 45", b0.CompletionTokens)
	}
	if b0.ReasoningTokens != 5 {
		t.Errorf("b0.ReasoningTokens = %d, want 5", b0.ReasoningTokens)
	}
	if b0.CacheReadTokens != 8 {
		t.Errorf("b0.CacheReadTokens = %d, want 8", b0.CacheReadTokens)
	}
	if b0.CacheWriteTokens != 3 {
		t.Errorf("b0.CacheWriteTokens = %d, want 3", b0.CacheWriteTokens)
	}
	if b0.EstimatedCostCents != 80 {
		t.Errorf("b0.EstimatedCostCents = %d, want 80", b0.EstimatedCostCents)
	}
	if b0.Turns != 2 {
		t.Errorf("b0.Turns = %d, want 2", b0.Turns)
	}

	// Second: local-model (cost 2)
	b1 := breakdown[1]
	if b1.Provider != "ollama" || b1.Model != "local-model" {
		t.Errorf("breakdown[1] = %s/%s, want ollama/local-model", b1.Provider, b1.Model)
	}
	if b1.PromptTokens != 5 {
		t.Errorf("b1.PromptTokens = %d, want 5", b1.PromptTokens)
	}
	if b1.CompletionTokens != 3 {
		t.Errorf("b1.CompletionTokens = %d, want 3", b1.CompletionTokens)
	}
	if b1.ReasoningTokens != 0 {
		t.Errorf("b1.ReasoningTokens = %d, want 0", b1.ReasoningTokens)
	}
	if b1.CacheReadTokens != 0 {
		t.Errorf("b1.CacheReadTokens = %d, want 0", b1.CacheReadTokens)
	}
	if b1.CacheWriteTokens != 0 {
		t.Errorf("b1.CacheWriteTokens = %d, want 0", b1.CacheWriteTokens)
	}
	if b1.EstimatedCostCents != 2 {
		t.Errorf("b1.EstimatedCostCents = %d, want 2", b1.EstimatedCostCents)
	}
	if b1.Turns != 1 {
		t.Errorf("b1.Turns = %d, want 1", b1.Turns)
	}
}

func TestSessionUsageScopesToSession(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	// turn_metrics.session_id is a FK to agent_sessions(id), so the sessions
	// must exist before their rows can be inserted.
	if err := database.CreateSession("session-a", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession(session-a): %v", err)
	}
	if err := database.CreateSession("session-b", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession(session-b): %v", err)
	}

	a := sampleRow(projectID, "session-a")
	a.PromptTokens, a.CompletionTokens = 100, 10
	if _, err := database.InsertTurnMetrics(a); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}
	if _, err := database.InsertTurnMetrics(a); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	b := sampleRow(projectID, "session-b")
	b.PromptTokens, b.CompletionTokens = 999, 999
	if _, err := database.InsertTurnMetrics(b); err != nil {
		t.Fatalf("InsertTurnMetrics: %v", err)
	}

	got, err := database.SessionUsage(projectID, "session-a")
	if err != nil {
		t.Fatalf("SessionUsage: %v", err)
	}
	if got.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (session-b's row must not count)", got.Turns)
	}
	if got.PromptTokens != 200 {
		t.Errorf("PromptTokens = %d, want 200", got.PromptTokens)
	}
	if got.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", got.CompletionTokens)
	}
}

// A session with no rows must yield zeroes, not a NULL-scan error. This is
// the state the rail is in for every brand-new session.
func TestSessionUsageEmptySessionIsZero(t *testing.T) {
	database, projectID := openMetricsTestDB(t)
	got, err := database.SessionUsage(projectID, "never-used")
	if err != nil {
		t.Fatalf("SessionUsage on an empty session: %v", err)
	}
	if got.Turns != 0 || got.PromptTokens != 0 || got.CompletionTokens != 0 {
		t.Errorf("got %+v, want zero totals", got)
	}
}
