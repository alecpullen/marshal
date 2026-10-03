package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// noopRegistry has one read-only tool that records what the state looked like
// while it ran.
func noopRegistry(onRun func()) *registry.Registry {
	reg := registry.New()
	reg.Register(registry.Tool{
		Name: "noop.tool", Description: "does nothing", Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			if onRun != nil {
				onRun()
			}
			return registry.ToolResult{Summary: "ok"}, nil
		},
	})
	return reg
}

func nativeToolCall(id string) []schema.ToolCall {
	return []schema.ToolCall{{ID: id, Name: "noop.tool", Args: json.RawMessage(`{}`)}}
}

func newStepRunner(t *testing.T, p *agenttest.ScriptedProvider, reg *registry.Registry) (*Runner, *session.State) {
	t.Helper()
	state := newTestState(t)
	r := NewRunner(p, reg, policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.NativeTools = true
	r.SetForceClass(string(ClassQuestion))
	return r, state
}

func TestEachModelResponseIsOneStepAndStampsItsOutput(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Thinking:      []string{"look at the guard", ""},
		Responses:     []string{"Checking the guard first.", "all done"},
		ToolCalls:     [][]schema.ToolCall{nativeToolCall("tc1"), nil},
		FinishReasons: []string{"tool_calls", "stop"},
	}
	var during session.ActiveToolCall
	var state *session.State
	reg := noopRegistry(func() { during, _ = state.ActiveToolCall() })
	r, st := newStepRunner(t, p, reg)
	state = st

	if err := r.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	steps := state.Steps()
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want one per model response (2): %+v", len(steps), steps)
	}
	for _, s := range steps {
		if s.EndedAt.IsZero() {
			t.Errorf("step %d left open", s.ID)
		}
		if s.Actor.Role != "" || s.Actor.Model != "test-model" {
			t.Errorf("orchestrator step actor = %+v", s.Actor)
		}
	}
	first := steps[0].ID

	var narration *session.Message
	for _, m := range state.Messages() {
		m := m
		if m.ContentType == session.ContentTypeNarration {
			narration = &m
		}
	}
	if narration == nil || narration.StepID != first {
		t.Fatalf("narration = %+v, want stamped with step %d", narration, first)
	}
	var audit *registry.AuditEvent
	var thought *session.ThinkingEntry
	for _, it := range state.Transcript() {
		switch it.Kind {
		case session.KindAudit:
			audit = it.Audit
		case session.KindThinking:
			thought = it.Thinking
		}
	}
	if audit == nil || audit.StepID != first || audit.ToolCallID != "tc1" || audit.Model != "test-model" {
		t.Fatalf("audit = %+v", audit)
	}
	if audit.AgentRole != "" {
		t.Errorf("orchestrator audit AgentRole = %q, want empty", audit.AgentRole)
	}
	if thought == nil || thought.StepID != first {
		t.Fatalf("thinking = %+v, want stamped with step %d", thought, first)
	}
	if during.StepID != first || during.ToolCallID != "tc1" {
		t.Fatalf("active tool call while running = %+v", during)
	}
	if _, ok := state.ActiveToolCall(); ok {
		t.Error("active tool call not cleared")
	}
}

func TestRoleRunnerStampsOwner(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses:     []string{"Reviewing.", "approved"},
		ToolCalls:     [][]schema.ToolCall{nativeToolCall("tc1"), nil},
		FinishReasons: []string{"tool_calls", "stop"},
	}
	r, state := newStepRunner(t, p, noopRegistry(nil))
	r.Role = RoleSDDBranchReviewer
	if err := r.Run(context.Background(), "review"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st := state.Steps()[0]
	if st.Actor.Role != "sdd_branch_reviewer" || st.Actor.Label != "branch reviewer" {
		t.Errorf("actor = %+v, want sdd_branch_reviewer / branch reviewer", st.Actor)
	}
	for _, it := range state.Transcript() {
		if it.Kind == session.KindAudit && it.Audit.AgentRole != "sdd_branch_reviewer" {
			t.Errorf("audit AgentRole = %q", it.Audit.AgentRole)
		}
	}

	r2, state2 := newStepRunner(t, &agenttest.ScriptedProvider{Responses: []string{"done"}}, noopRegistry(nil))
	r2.Role = RoleSDDReviewer
	r2.ActorLabel = "reviewer #2"
	if err := r2.Run(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := state2.Steps()[0].Actor.Label; got != "reviewer #2" {
		t.Errorf("explicit ActorLabel lost: %q", got)
	}
}

func TestEnvelopeRationaleBecomesNarrationAndCallsGetIDs(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"Reading the config.","action":{"type":"tool_call","tool":"noop.tool","args":{}}}`,
			`{"rationale":"Wrapping up.","action":{"type":"final","content":"done"}}`,
		},
	}
	r, state := newStepRunner(t, p, noopRegistry(nil))
	r.NativeTools = false
	if err := r.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var narrations []string
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeNarration {
			narrations = append(narrations, m.Content)
		}
	}
	if len(narrations) != 1 || narrations[0] != "Reading the config." {
		t.Fatalf("narrations = %q; the tool_call rationale must show and the final's must not", narrations)
	}
	first := state.Steps()[0].ID
	for _, it := range state.Transcript() {
		if it.Kind == session.KindAudit {
			want := "s" + itoa(first) + "-a0"
			if it.Audit.ToolCallID != want || it.Audit.StepID != first {
				t.Errorf("envelope audit = id %q step %d, want %q / %d", it.Audit.ToolCallID, it.Audit.StepID, want, first)
			}
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEnvelopeBatchGetsPerActionIDs(t *testing.T) {
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			`{"rationale":"Reading two things.","actions":[{"type":"tool_call","tool":"noop.tool","args":{}},{"type":"tool_call","tool":"noop.tool","args":{}}]}`,
			`{"rationale":"x","action":{"type":"final","content":"done"}}`,
		},
	}
	r, state := newStepRunner(t, p, noopRegistry(nil))
	r.NativeTools = false
	if err := r.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ids := map[string]bool{}
	for _, it := range state.Transcript() {
		if it.Kind == session.KindAudit {
			ids[it.Audit.ToolCallID] = true
		}
	}
	first := state.Steps()[0].ID
	if len(ids) != 2 || !ids["s"+itoa(first)+"-a0"] || !ids["s"+itoa(first)+"-a1"] {
		t.Fatalf("batch call ids = %v", ids)
	}
}

func TestApprovalRequestCarriesStep(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")
	r.curStep = 7
	go func() {
		_, _, _ = r.requestApproval(context.Background(), registry.Tool{Name: "t", Risk: registry.RiskCommand}, "t", nil, map[string]interface{}{}, "why")
	}()
	deadline := time.After(2 * time.Second)
	for state.PendingApproval() == nil {
		select {
		case <-deadline:
			t.Fatal("no pending approval")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	pending := state.PendingApproval()
	if pending.StepID != 7 {
		t.Fatalf("pending StepID = %d, want 7", pending.StepID)
	}
	pending.ResponseChan <- session.UserApprovalDecision{}
}

func TestLooksLikeIntentOnly(t *testing.T) {
	long := "I'll " + strings.Repeat("read the file and then keep going ", 10)
	cases := []struct {
		text string
		want bool
	}{
		{"I'll read parser.go next.", true},
		{"Let me check the tests", true},
		{"Now I will update the guard. Then run tests.", true},
		{"i'm going to grep for it", true},
		{"Going to look at the logs.", true},
		{"", false},
		{"The parser handles empty input in guard.go.", false},
		{"I'll do this. Then that. And then the other thing.", false}, // 3 terminators
		{"I'll show you:\n```go\nfmt.Println()\n```", false},
		{long, false},
		{"Done. I'll leave the rest to you.", false}, // does not open with intent
	}
	for _, c := range cases {
		if got := looksLikeIntentOnly(c.text); got != c.want {
			t.Errorf("looksLikeIntentOnly(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func runWithIntentReplies(t *testing.T, nudge bool, replies []string, toolFirst bool) (*session.State, *schema.ChatRequest, *TurnMetrics, *agenttest.ScriptedProvider) {
	t.Helper()
	p := &agenttest.ScriptedProvider{}
	if toolFirst {
		p.Responses = append(p.Responses, "Reading the file.")
		p.ToolCalls = append(p.ToolCalls, nativeToolCall("tc1"))
	}
	for _, rep := range replies {
		p.Responses = append(p.Responses, rep)
		p.ToolCalls = append(p.ToolCalls, nil)
	}
	r, state := newStepRunner(t, p, noopRegistry(nil))
	r.IntentNudge = nudge
	var got *TurnMetrics
	r.MetricsObserver = func(m TurnMetrics) { got = &m }
	if err := r.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return state, nil, got, p
}

func nudgeCount(state *session.State) int {
	n := 0
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem && m.Content == intentNudgeMessage {
			n++
		}
	}
	return n
}

func TestIntentNudgeFiresOncePerTurn(t *testing.T) {
	state, _, metrics, p := runWithIntentReplies(t, true, []string{"I'll read parser.go next.", "I'll read parser.go next."}, true)
	if got := nudgeCount(state); got != 1 {
		t.Fatalf("nudges = %d, want exactly 1", got)
	}
	if metrics == nil || metrics.IntentNudges != 1 {
		t.Fatalf("IntentNudges = %+v, want 1", metrics)
	}
	var final string
	for _, m := range state.Messages() {
		if m.Final {
			final = m.Content
		}
	}
	if final != "I'll read parser.go next." {
		t.Errorf("the repeated reply must be accepted as final, got %q", final)
	}
	if p.Calls != 3 {
		t.Errorf("provider calls = %d, want 3 (tool, intent, repeat)", p.Calls)
	}
}

func TestIntentNudgeDoesNotFire(t *testing.T) {
	cases := []struct {
		name      string
		nudge     bool
		replies   []string
		toolFirst bool
	}{
		{"disabled", false, []string{"I'll read parser.go next."}, true},
		{"no prior tool call", true, []string{"I'll read parser.go next."}, false},
		{"long text", true, []string{"I'll " + strings.Repeat("go on and on about it ", 15)}, true},
		{"code fence", true, []string{"I'll run:\n```sh\ngo test\n```"}, true},
		{"real answer", true, []string{"The guard lives in guard.go."}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, _, metrics, _ := runWithIntentReplies(t, c.nudge, c.replies, c.toolFirst)
			if got := nudgeCount(state); got != 0 {
				t.Fatalf("nudges = %d, want 0", got)
			}
			if metrics != nil && metrics.IntentNudges != 0 {
				t.Fatalf("IntentNudges = %d, want 0", metrics.IntentNudges)
			}
		})
	}
}

func TestNarrationDirectiveInPrompt(t *testing.T) {
	build := func(native, narration bool) string {
		return BuildSystemPromptWithSystem(SystemPromptOptions{
			Role: RoleGeneral, NativeTools: native, Narration: narration, Mode: policy.ModeEdit,
		}).Content
	}
	if !strings.Contains(build(true, true), "begin that same response with one short sentence") {
		t.Error("native prompt with narration on must carry the directive")
	}
	if strings.Contains(build(true, false), "begin that same response with one short sentence") {
		t.Error("narration_prompt = false must remove the directive")
	}
	if strings.Contains(build(false, true), "begin that same response with one short sentence") {
		t.Error("envelope mode must never carry the directive")
	}
}
