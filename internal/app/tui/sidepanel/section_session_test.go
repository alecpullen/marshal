package sidepanel

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

func sessionData(now time.Time) Data {
	st := session.New(config.Config{}, "/tmp", now.Add(-90*time.Second), session.Persistence{})
	return Data{
		Now:   now,
		State: st,
		Totals: db.UsageTotals{
			Turns:            7,
			PromptTokens:     42_100,
			CompletionTokens: 8_300,
		},
	}
}

func TestSessionSectionIdentity(t *testing.T) {
	s := SessionSection{}
	if s.ID() != "session" {
		t.Errorf("ID = %q, want %q", s.ID(), "session")
	}
	if s.Title() != "" {
		t.Errorf("Title = %q, want empty so the footer renders a bare rule", s.Title())
	}
	if s.Clippable() {
		t.Error("Clippable = true, want false")
	}
}

func TestSessionSectionRelevance(t *testing.T) {
	s := SessionSection{}
	var empty Data
	if s.Relevant(empty) {
		t.Error("want irrelevant with no session")
	}
	if !s.Relevant(sessionData(time.Now())) {
		t.Error("want relevant as soon as a session exists, even at turn 0")
	}
}

// The counters must come from the session-scoped totals, not from the recent
// turn rows, and the turn count must not be capped by any row-window limit.
func TestSessionSectionUsesTotals(t *testing.T) {
	now := time.Now()
	rows := SessionSection{}.Render(sessionData(now), 60, 4)
	if len(rows) < 2 {
		t.Fatalf("want at least 2 rows, got %d: %v", len(rows), rows)
	}
	if !strings.Contains(rows[0], "turn 7") {
		t.Errorf("row 0 = %q, want it to report turn 7 from Totals", rows[0])
	}
	if !strings.Contains(rows[1], "42k in") || !strings.Contains(rows[1], "8k out") {
		t.Errorf("row 1 = %q, want totals-derived token counts", rows[1])
	}
}

// Elapsed is session wall-clock from State.StartedAt, not the age of the
// oldest turn row — so it is correct even with zero completed turns.
func TestSessionSectionElapsedFromSessionStart(t *testing.T) {
	now := time.Now()
	d := sessionData(now)
	d.Totals = db.UsageTotals{} // no turns at all
	rows := SessionSection{}.Render(d, 60, 4)
	if !strings.Contains(rows[0], "turn 0") {
		t.Errorf("row 0 = %q, want turn 0", rows[0])
	}
	if !strings.Contains(rows[0], "1m30s") {
		t.Errorf("row 0 = %q, want elapsed 1m30s measured from StartedAt", rows[0])
	}
}

func TestSessionSectionRendersTurnsElapsedAndTokens(t *testing.T) {
	now := time.Now()
	s := SessionSection{}
	rows := s.Render(sessionData(now), 24, 0)
	if len(rows) == 0 {
		t.Fatal("no rows rendered")
	}
	joined := StripANSI(strings.Join(rows, "\n"))

	if !strings.Contains(joined, "turn 7") {
		t.Errorf("want turn count, got %q", joined)
	}
	if !strings.Contains(joined, "1m30s") {
		t.Errorf("want elapsed since session start, got %q", joined)
	}
	// strutil.CompactTokens truncates with integer division: 42100 → "42k",
	// 8300 → "8k". Not "42.1k" — the spec's mockup was illustrative.
	if !strings.Contains(joined, "42k in") {
		t.Errorf("want summed prompt tokens, got %q", joined)
	}
	if !strings.Contains(joined, "8k out") {
		t.Errorf("want summed completion tokens, got %q", joined)
	}
}

func TestSessionSectionRespectsWidth(t *testing.T) {
	s := SessionSection{}
	rows := s.Render(sessionData(time.Now()), 12, 0)
	for i, r := range rows {
		if w := len([]rune(StripANSI(r))); w > 12 {
			t.Errorf("row %d width = %d, want <= 12: %q", i, w, StripANSI(r))
		}
	}
}

func TestSessionSectionOneLine(t *testing.T) {
	s := SessionSection{}
	got := StripANSI(s.OneLine(sessionData(time.Now()), 24))
	if !strings.Contains(got, "turn 7") {
		t.Errorf("want turn count in the one-line summary, got %q", got)
	}
	if len([]rune(got)) > 24 {
		t.Errorf("one-line width = %d, want <= 24", len([]rune(got)))
	}
}

func TestSessionSectionBudgetRow(t *testing.T) {
	s := SessionSection{}
	base := sessionData(time.Now())

	// No budget used -> no tools row.
	rows := s.Render(base, 24, 0)
	joined := StripANSI(strings.Join(rows, "\n"))
	if strings.Contains(joined, "tools") {
		t.Fatalf("no tools row expected with zero budget, got %q", joined)
	}

	// Unlimited budget (Max 0) with tools used.
	st := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	st.SetToolBudget(session.ToolBudget{Used: 7, Max: 0})
	d := base
	d.State = st
	rows = s.Render(d, 24, 0)
	joined = StripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(joined, "tools 7 · no cap") {
		t.Fatalf("want 'tools 7 · no cap', got %q", joined)
	}

	// Capped budget.
	st.SetToolBudget(session.ToolBudget{Used: 7, Max: 100})
	rows = s.Render(d, 24, 0)
	joined = StripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(joined, "tools 7/100") {
		t.Fatalf("want 'tools 7/100', got %q", joined)
	}

	// One-line summary carries the suffix too.
	one := StripANSI(s.OneLine(d, 40))
	if !strings.Contains(one, "tools 7/100") {
		t.Fatalf("one-line summary missing budget, got %q", one)
	}
}
