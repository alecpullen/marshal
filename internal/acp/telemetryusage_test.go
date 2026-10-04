package acp

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

func openUsageTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := d.Migrate(); err != nil {
		t.Fatal(err)
	}
	pid, err := d.GetOrCreateProject("/tmp/usage", "usage")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateSession("s1", pid, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	return d, pid
}

func insertUsage(t *testing.T, d *db.DB, pid int64, cents int64) int64 {
	t.Helper()
	id, err := d.InsertTurnMetrics(db.TurnMetricsRow{
		ProjectID: pid, SessionID: "s1", StartedAt: time.Now(), DurationMs: 50,
		Role: "implementer", Provider: "p", Model: "m", PromptTokens: 10,
		CompletionTokens: 5, EstimatedCostCents: cents, EstimatedCostMicroUSD: cents * 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func telemetryUsage(t *testing.T, m *TurnManager, st *session.State, d *db.DB, pid int64) []TelemetryUsageRow {
	t.Helper()
	got := m.buildTelemetry("s1", st, d, pid)
	if usage, ok := got["usage"]; ok {
		return usage.([]TelemetryUsageRow)
	}
	return nil
}

func TestTelemetryUsageRowsAreIncremental(t *testing.T) {
	d, pid := openUsageTestDB(t)
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(string, any) error { return nil },
	})
	if rows := telemetryUsage(t, m, st, d, pid); rows != nil {
		t.Fatalf("empty db rows = %v", rows)
	}
	id1 := insertUsage(t, d, pid, 250)
	id2 := insertUsage(t, d, pid, 5)
	rows := telemetryUsage(t, m, st, d, pid)
	if len(rows) != 2 || rows[0].ID != id1 || rows[1].ID != id2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].CostUSD != 2.5 || rows[0].CostMicroUSD != 2_500_000 || rows[0].Role != "implementer" || rows[0].PromptTokens != 10 || rows[0].StartedAt == 0 {
		t.Fatalf("row = %+v", rows[0])
	}
	if rows := telemetryUsage(t, m, st, d, pid); rows != nil {
		t.Fatalf("second call rows = %v", rows)
	}
	id3 := insertUsage(t, d, pid, 100)
	if rows := telemetryUsage(t, m, st, d, pid); len(rows) != 1 || rows[0].ID != id3 {
		t.Fatalf("third rows = %+v", rows)
	}
}

func TestTelemetryWithoutDBOmitsUsage(t *testing.T) {
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(string, any) error { return nil },
	})
	if _, ok := m.buildTelemetry("s1", st, nil, 0)["usage"]; ok {
		t.Fatal("usage present without a DB")
	}
}

func TestDropRunClearsUsageHighWater(t *testing.T) {
	d, pid := openUsageTestDB(t)
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(string, any) error { return nil },
	})
	insertUsage(t, d, pid, 1)
	telemetryUsage(t, m, st, d, pid)
	m.dropStacks("s1")
	m.usageMu.Lock()
	_, ok := m.usageHW["s1"]
	m.usageMu.Unlock()
	if ok {
		t.Fatal("usageHW entry survived dropStacks")
	}
}
