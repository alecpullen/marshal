package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// finalActionJSON is a valid scripted final-action envelope for clean turns.
const finalActionJSON = `{"rationale":"done","action":{"type":"final","content":"Done."}}`

// TestDegradedTurnStartEmitsStartupNotice verifies that a degraded general
// runner re-emits the startup notice at the start of every turn — the TUI
// auto-dismisses banners (TTL) and clears NoticeProvider on success, so a
// persistent misconfiguration must stay visible. The refreshed SetAt proves
// the re-emission actually happened rather than a stale banner surviving.
func TestDegradedTurnStartEmitsStartupNotice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{finalActionJSON, finalActionJSON}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.DecodingDegraded = true

	if err := runner.Run(context.Background(), "first turn"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	notice, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after first turn")
	}
	if notice.Category != session.NoticeConfig || notice.Severity != session.SeverityWarn {
		t.Fatalf("notice = %+v, want NoticeConfig/SeverityWarn", notice)
	}
	// SetNotice stamps SetAt with wall-clock time.Now(), not the session's
	// injected clock (see session/notice.go SetNotice), so cross-turn SetAt
	// comparisons are real elapsed times. If stamping ever moves to the
	// session clock, these assertions fail loudly (equal timestamps) rather
	// than silently inverting.
	firstSetAt := notice.SetAt

	if err := runner.Run(context.Background(), "second turn"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	notice2, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after second turn")
	}
	if notice2.SetAt.Equal(firstSetAt) {
		t.Fatal("second-turn SetAt unchanged — startup notice must re-emit each turn")
	}
}

// TestNonDegradedFirstTurnEmitsNoNotice verifies that a non-degraded runner
// never emits the startup notice.
func TestNonDegradedFirstTurnEmitsNoNotice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{finalActionJSON}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")

	if err := runner.Run(context.Background(), "first turn"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := state.Notice(); ok {
		t.Fatal("non-degraded runner emitted a notice")
	}
}

// TestDegradedSubagentTurnStartEmitsNoNotice pins the RoleGeneral gate:
// degraded subagent runners stay silent on every turn, not just the first.
func TestDegradedSubagentTurnStartEmitsNoNotice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{finalActionJSON, finalActionJSON}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.DecodingDegraded = true
	runner.Role = RoleSubtask

	if err := runner.Run(context.Background(), "subtask turn"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, ok := state.Notice(); ok {
		t.Fatal("subagent runner emitted a startup notice")
	}
	if err := runner.Run(context.Background(), "subtask turn again"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if _, ok := state.Notice(); ok {
		t.Fatal("subagent runner emitted a startup notice on a later turn")
	}
}

// TestDegradedParseFailureEscalationReFires verifies the escalation notice
// fires on every parse-failure tally — like the startup notice, it must stay
// visible across the TUI's TTL auto-dismiss, so it re-emits rather than
// once-per-session. Also pins the per-provider hint.
func TestDegradedParseFailureEscalationReFires(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"this is not json"}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.DecodingDegraded = true

	if err := runner.Run(context.Background(), "broken turn"); err == nil {
		t.Fatal("Run succeeded, want parse-failure error")
	}
	notice, ok := state.Notice()
	if !ok {
		t.Fatal("no escalation notice after parse failure")
	}
	if notice.Category != session.NoticeConfig || notice.Severity != session.SeverityWarn {
		t.Fatalf("notice = %+v, want NoticeConfig/SeverityWarn", notice)
	}
	if !strings.Contains(notice.Hint, "[providers.scripted]") {
		t.Fatalf("escalation hint missing per-provider hint = %q", notice.Hint)
	}
	firstSetAt := notice.SetAt

	// Second failing turn: the provider repeats its single non-JSON response,
	// so the ladder trips again — the notice must re-fire with a fresh SetAt.
	if err := runner.Run(context.Background(), "broken turn again"); err == nil {
		t.Fatal("second Run succeeded, want parse-failure error")
	}
	notice2, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after second failing turn")
	}
	if notice2.SetAt.Equal(firstSetAt) {
		t.Fatal("second SetAt unchanged — escalation must re-fire per tally")
	}
}

// TestDegradedTruncatedPayloadEscalates pins the truncated-payload tally
// site: a degraded session whose ladder trips via finish-reason=length
// refusals escalates exactly like the unparseable-output path.
func TestDegradedTruncatedPayloadEscalates(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		// Script 'length' for every ladder iteration: unlike Responses,
		// FinishReasons does NOT replay on repeat, so a single entry would
		// let iterations 2+ execute the tool call and never trip the ladder.
		Responses:     []string{`{"rationale":"r","action":{"type":"tool_call","tool":"file.read","args":{}}}`},
		FinishReasons: []string{"length", "length", "length"},
	}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.DecodingDegraded = true

	if err := runner.Run(context.Background(), "truncation ladder"); err == nil {
		t.Fatal("Run succeeded, want truncated-payload ladder failure")
	}
	notice, ok := state.Notice()
	if !ok {
		t.Fatal("no escalation notice after truncated-payload ladder")
	}
	if notice.Category != session.NoticeConfig || notice.Severity != session.SeverityWarn {
		t.Fatalf("notice = %+v, want NoticeConfig/SeverityWarn", notice)
	}
}

// TestDegradedNonReadOnlyActionsEscalate pins the F-SEC-11 tally site: a
// degraded session whose ladder trips via non-read-only actions arrays
// escalates identically (nothing executed, counted as parse failures).
func TestDegradedNonReadOnlyActionsEscalate(t *testing.T) {
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.write",
		Risk: registry.RiskCommand, // not read-only
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "written"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	p := &agenttest.ScriptedProvider{Responses: []string{
		`{"rationale":"bad parallel","actions":[{"type":"tool_call","tool":"demo.write","args":{}}]}`,
		// The violation branch tallies a parse failure but never breaks the
		// loop in-band (the overhead cap is its guardrail), so follow with a
		// valid final instead of expecting a failed turn.
		`{"rationale":"corrected","action":{"type":"final","content":"Done."}}`,
	}}
	state := newTestState(t)
	runner := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.DecodingDegraded = true
	runner.SetForceClass(string(ClassQuestion))
	runner.MaxToolIterations = 5
	runner.MaxRetries = 0

	// The turn may complete (nil error) after the correction; the contract
	// under test is that the violation tally surfaced the escalation notice.
	_ = runner.Run(context.Background(), "read-only ladder")
	notice, ok := state.Notice()
	if !ok {
		t.Fatal("no escalation notice after read-only-violation ladder")
	}
	if notice.Category != session.NoticeConfig || notice.Severity != session.SeverityWarn {
		t.Fatalf("notice = %+v, want NoticeConfig/SeverityWarn", notice)
	}
}
