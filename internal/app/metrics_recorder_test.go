package app

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/db"
)

func TestMetricsRecorderPersistsTurn(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/tmp/proj", "proj")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession("sess_1", projectID, "", time.Now()); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	record := metricsRecorder(database, projectID, "sess_1", nil)
	record(agent.TurnMetrics{
		StartedAt:  time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		DurationMs: 42,
		Goal:       "eval goal",
		Class:      "question",
		Role:       "general",
		Model:      "test-model",
		Iterations: 2,
		ToolCalls:  1,
		Outcome:    "answered",
	})

	rows, err := database.RecentTurnMetrics(projectID, 5)
	if err != nil {
		t.Fatalf("RecentTurnMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.SessionID != "sess_1" || got.Goal != "eval goal" || got.Outcome != "answered" ||
		got.Iterations != 2 || got.ToolCalls != 1 || got.Model != "test-model" {
		t.Fatalf("row = %+v", got)
	}
}

func TestMetricsRecorderSwallowsInsertFailure(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	database.Close()

	record := metricsRecorder(database, 1, "", nil)
	record(agent.TurnMetrics{Outcome: "answered"})
}
