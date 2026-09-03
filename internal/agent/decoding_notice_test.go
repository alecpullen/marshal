package agent

import (
	"context"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// finalActionJSON is a valid scripted final-action envelope for clean turns.
const finalActionJSON = `{"rationale":"done","action":{"type":"final","content":"Done."}}`

// TestDegradedFirstTurnEmitsStartupNoticeOnce verifies that a degraded
// general runner emits the startup notice exactly once across multiple turns
// (the SetAt must not change on the second turn).
func TestDegradedFirstTurnEmitsStartupNoticeOnce(t *testing.T) {
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
	if notice.Category != session.NoticeProvider || notice.Severity != session.SeverityWarn {
		t.Fatalf("notice = %+v, want NoticeProvider/SeverityWarn", notice)
	}
	firstSetAt := notice.SetAt

	if err := runner.Run(context.Background(), "second turn"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	notice2, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after second turn")
	}
	if !notice2.SetAt.Equal(firstSetAt) {
		t.Fatalf("second-turn SetAt = %v, want unchanged %v (startup notice must fire once)", notice2.SetAt, firstSetAt)
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

// TestSubagentDegradedEmitsNoStartupNotice verifies that a degraded runner
// with a non-general role stays silent (emission is gated to RoleGeneral).
func TestSubagentDegradedEmitsNoStartupNotice(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{finalActionJSON}}
	state := newTestState(t)
	runner := NewRunner(p, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	runner.DecodingDegraded = true
	runner.Role = RoleSubtask

	if err := runner.Run(context.Background(), "subtask turn"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := state.Notice(); ok {
		t.Fatal("subagent runner emitted a startup notice")
	}
}

// TestDegradedParseFailureEmitsEscalationOnce verifies that a degraded
// general runner emits the escalation notice on the first parse failure and
// does not re-emit it on a subsequent failing turn (once per runner
// instance).
func TestDegradedParseFailureEmitsEscalationOnce(t *testing.T) {
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
	if notice.Category != session.NoticeProvider || notice.Severity != session.SeverityWarn {
		t.Fatalf("notice = %+v, want NoticeProvider/SeverityWarn", notice)
	}
	firstSetAt := notice.SetAt

	// Second failing turn: the provider repeats its single non-JSON response,
	// so the ladder trips again — but the escalation notice must not re-emit.
	if err := runner.Run(context.Background(), "broken turn again"); err == nil {
		t.Fatal("second Run succeeded, want parse-failure error")
	}
	notice2, ok := state.Notice()
	if !ok {
		t.Fatal("no notice after second failing turn")
	}
	if !notice2.SetAt.Equal(firstSetAt) {
		t.Fatalf("second SetAt = %v, want unchanged %v (escalation must fire once)", notice2.SetAt, firstSetAt)
	}
}
