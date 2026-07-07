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
		SoftStalls:       1,
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
