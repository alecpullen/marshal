package postmortem

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"marshal/internal/app/session"
)

// TestBuildReportsPreflightLatency is the acceptance criterion for the section:
// the hidden prompt-preparation work reaches the report with complete
// aggregates, the slowest turn first, and the unspanned remainder stated
// instead of being blamed on a phase.
func TestBuildReportsPreflightLatency(t *testing.T) {
	state := testState(t, testConfig(), nil)
	state.AddMessage(session.RoleUser, "first", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "ok", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "second", session.ContentTypePlain)
	state.RecordPreflightTiming(120, []session.PhaseTiming{
		{Name: "route", DurationMs: 10},
		{Name: "history", DurationMs: 40},
	})
	state.RecordPreflightTiming(300, []session.PhaseTiming{
		{Name: "route", DurationMs: 20},
		{Name: "history", DurationMs: 90},
	})

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lat := report.Latency
	if lat == nil {
		t.Fatal("latency section missing")
	}
	if lat.Source != "live" {
		t.Errorf("Source = %q, want live", lat.Source)
	}
	if lat.TurnsMeasured != 2 {
		t.Errorf("TurnsMeasured = %d, want 2", lat.TurnsMeasured)
	}
	if lat.SessionTurns != 2 {
		t.Errorf("SessionTurns = %d, want 2", lat.SessionTurns)
	}
	if lat.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0", lat.Dropped)
	}

	wantPhases := []LatencyPhase{
		{Phase: "history", Count: 2, TotalMs: 130, MaxMs: 90, MaxTurn: 2},
		{Phase: "route", Count: 2, TotalMs: 30, MaxMs: 20, MaxTurn: 2},
	}
	if !reflect.DeepEqual(lat.Phases, wantPhases) {
		t.Errorf("Phases = %+v, want %+v", lat.Phases, wantPhases)
	}

	if len(lat.Examples) != 2 {
		t.Fatalf("Examples = %+v, want 2", lat.Examples)
	}
	// Slowest first: turn 2 at 300ms, with 190ms no phase span covered.
	slow := lat.Examples[0]
	if slow.Turn != 2 || slow.PromptReadyMs != 300 || slow.UnattributedMs != 190 {
		t.Errorf("slowest example = %+v, want turn 2 at 300ms with 190ms unattributed", slow)
	}
	if !reflect.DeepEqual(slow.Phases, []LatencyPhaseTiming{
		{Phase: "route", DurationMs: 20},
		{Phase: "history", DurationMs: 90},
	}) {
		t.Errorf("slowest example phases = %+v, want execution order preserved", slow.Phases)
	}
	if fast := lat.Examples[1]; fast.Turn != 1 || fast.PromptReadyMs != 120 || fast.UnattributedMs != 70 {
		t.Errorf("second example = %+v, want turn 1 at 120ms with 70ms unattributed", fast)
	}

	// The section is part of the report's wire contract.
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var back Report
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if back.Latency == nil || back.Latency.TurnsMeasured != 2 || len(back.Latency.Examples) != 2 {
		t.Errorf("round-tripped latency = %+v", back.Latency)
	}
}

// TestBuildOmitsUnmeasuredLatency keeps "unavailable" distinguishable from
// "fast": a session that measured nothing must not carry a zero-latency
// section, because a reader would average that zero into the session.
func TestBuildOmitsUnmeasuredLatency(t *testing.T) {
	state := testState(t, testConfig(), nil)
	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if report.Latency != nil {
		t.Fatalf("Latency = %+v, want nil", report.Latency)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(data), `"latency"`) {
		t.Error("unmeasured latency must be omitted from the JSON, not null")
	}
}

// TestBuildKeepsSlowestLatencyExamples holds the section to its example bound
// and proves the bound keeps the turns worth reading.
func TestBuildKeepsSlowestLatencyExamples(t *testing.T) {
	state := testState(t, testConfig(), nil)
	for i := 1; i <= maxLatencyExamples+8; i++ {
		state.RecordPreflightTiming(int64(i), []session.PhaseTiming{
			{Name: "route", DurationMs: int64(i)},
		})
	}

	report, err := Build(state, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lat := report.Latency
	if lat == nil {
		t.Fatal("latency section missing")
	}
	if len(lat.Examples) != maxLatencyExamples {
		t.Fatalf("Examples = %d, want %d", len(lat.Examples), maxLatencyExamples)
	}
	if lat.Examples[0].PromptReadyMs != int64(maxLatencyExamples+8) {
		t.Errorf("slowest example = %dms, want %dms", lat.Examples[0].PromptReadyMs, maxLatencyExamples+8)
	}
	wantLast := int64(8) + 1
	if got := lat.Examples[len(lat.Examples)-1].PromptReadyMs; got != wantLast {
		t.Errorf("fastest retained example = %dms, want %dms", got, wantLast)
	}
	if lat.TurnsMeasured != maxLatencyExamples+8 {
		t.Errorf("TurnsMeasured = %d, want %d", lat.TurnsMeasured, maxLatencyExamples+8)
	}
}
