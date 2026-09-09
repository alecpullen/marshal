package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// newSkillGateTestRunner builds a runner wired for skill-gate tests: a
// scripted provider, a registry with the real skill.load/skill.unload
// tools, and a skill index containing "test-skill" (and "other-skill").
func newSkillGateTestRunner(t *testing.T, state *session.State) (*Runner, *agenttest.ScriptedProvider) {
	t.Helper()
	idx := skills.NewIndex()
	idx.Set("test-skill", skills.Skill{Name: "test-skill", Description: "a test skill", Body: "TEST SKILL BODY"})
	idx.Set("other-skill", skills.Skill{Name: "other-skill", Description: "another", Body: "OTHER BODY"})
	reg := registry.New()
	skills.RegisterTool(reg, idx, state)
	pol := policy.NewEngine(&config.Config{}, nil)
	p := &agenttest.ScriptedProvider{}
	r := NewRunner(p, reg, pol, state, "test-model")
	r.NativeTools = true
	r.SkillIndex = idx
	return r, p
}

// skillLoadCall is the scripted tool call the provider emits for one
// skill.load attempt.
func skillLoadCall(name string) []schema.ToolCall {
	raw, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		panic(err)
	}
	return []schema.ToolCall{{ID: "call_skill_" + name, Name: "skill.load", Args: raw}}
}

// answerPendingSkillGate answers the next n pending skill-gate prompts with
// choice, in order, from a single goroutine. It claims the pending slot
// (SetPendingSkillGate(nil)) before sending on ResponseChan — the same
// claim-before-send pattern answerPendingQuestions uses to avoid a
// double-send deadlock. The channel reports each prompt's skill name so
// tests can assert how many prompts fired and for which skills.
func answerPendingSkillGate(state *session.State, n int, choice session.SkillGateChoice) <-chan string {
	choices := make([]session.SkillGateChoice, n)
	for i := range choices {
		choices[i] = choice
	}
	return answerPendingSkillGateChoices(state, choices)
}

// answerPendingSkillGateChoices answers successive pending prompts with the
// given choices, one choice per prompt, in order.
func answerPendingSkillGateChoices(state *session.State, choices []session.SkillGateChoice) <-chan string {
	promptCh := make(chan string, len(choices))
	go func() {
		for _, choice := range choices {
			for {
				sg := state.PendingSkillGate()
				if sg == nil {
					time.Sleep(time.Millisecond)
					continue
				}
				name := sg.Skill
				state.SetPendingSkillGate(nil) // claim before answering
				promptCh <- name
				sg.ResponseChan <- choice
				break
			}
		}
	}()
	return promptCh
}

// runSkillGateTurn drives one RunTask whose provider scripts a single
// skill.load call followed by a final answer.
func runSkillGateTurn(t *testing.T, r *Runner, p *agenttest.ScriptedProvider, skill string) (*Task, error) {
	t.Helper()
	p.Responses = []string{"loading skill", "Done."}
	p.ToolCalls = [][]schema.ToolCall{skillLoadCall(skill), nil}
	return r.RunTask(context.Background(), "use the skill")
}

// promptCount drains the answerer channel without blocking: it reports how
// many prompts fired by the time the turn finished.
func drainPrompts(ch <-chan string) []string {
	var out []string
	for {
		select {
		case name := <-ch:
			out = append(out, name)
		default:
			return out
		}
	}
}

// TestSkillGatePromptsOnUnknownWindowAndAllowsOnce covers the plan's
// "prompts on unknown window" bullet: a default state has no resolved
// route, so TurnUsage reports window 0 and the gate must still prompt.
func TestSkillGatePromptsOnUnknownWindowAndAllowsOnce(t *testing.T) {
	state := newTestState(t)
	state.Config.Skills.LoadGateThresholdTokens = 131072
	r, p := newSkillGateTestRunner(t, state)
	prompts := answerPendingSkillGate(state, 1, session.SkillGateAllowOnce)

	task, err := runSkillGateTurn(t, r, p, "test-skill")
	if err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if task.Summary != "Done." {
		t.Fatalf("Summary = %q, want Done.", task.Summary)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 || got[0] != "test-skill" {
		t.Fatalf("prompts = %v, want exactly one for test-skill", got)
	}
	// The load actually happened: the skill body reached the transcript.
	foundBody := false
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSkillBody && strings.Contains(m.Content, "TEST SKILL BODY") {
			foundBody = true
		}
	}
	if !foundBody {
		t.Fatal("skill body not loaded after allow-once")
	}
}

// TestSkillGatePromptsBelowThreshold covers the plan's "prompts below
// threshold" bullet with a KNOWN small window. RunTask resolves the route
// after test setup and SetTurnBudget overwrites any window set via
// SetTurnContextWindow, so the window must arrive through a resolved
// route (the same path production windows take).
func TestSkillGatePromptsBelowThreshold(t *testing.T) {
	state := newTestState(t)
	state.Config.Skills.LoadGateThresholdTokens = 131072
	r, p := newSkillGateTestRunner(t, state)
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "small", Model: "small-32k", ContextWindow: 32000, MaxOutputTokens: 2048},
	}}
	prompts := answerPendingSkillGate(state, 1, session.SkillGateAllowOnce)

	if _, err := runSkillGateTurn(t, r, p, "test-skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 || got[0] != "test-skill" {
		t.Fatalf("prompts = %v, want exactly one for test-skill (window 32000 <= threshold 131072)", got)
	}
}

func TestSkillGateReFetchAlsoPrompts(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	// Pre-activate the skill so the load is a re-fetch of an active skill.
	state.ActivateSkill("test-skill")
	prompts := answerPendingSkillGate(state, 1, session.SkillGateAllowOnce)

	if _, err := runSkillGateTurn(t, r, p, "test-skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 {
		t.Fatalf("re-fetch of an active skill must still prompt, got %d prompts", len(got))
	}
}

func TestSkillGateSkipsAboveThreshold(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	// RunTask resolves the route after test setup and SetTurnBudget
	// overwrites any window set via SetTurnContextWindow, so the
	// above-threshold window must arrive through a resolved route.
	r.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "large", Model: "large-262k", ContextWindow: 262144, MaxOutputTokens: 4096},
	}}

	if _, err := runSkillGateTurn(t, r, p, "test-skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if state.PendingSkillGate() != nil {
		t.Fatal("no prompt should be pending above the threshold")
	}
	// The gate skipped, so the load must actually have dispatched.
	foundBody := false
	for _, m := range state.Messages() {
		if m.ContentType == session.ContentTypeSkillBody && strings.Contains(m.Content, "TEST SKILL BODY") {
			foundBody = true
		}
	}
	if !foundBody {
		t.Fatal("skill body not loaded — the gate did not dispatch above the threshold")
	}
}

func TestSkillGateDisabledAtZeroThreshold(t *testing.T) {
	state := newTestState(t)
	state.Config.Skills.LoadGateThresholdTokens = 0
	r, p := newSkillGateTestRunner(t, state)

	if _, err := runSkillGateTurn(t, r, p, "test-skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if state.PendingSkillGate() != nil {
		t.Fatal("threshold 0 must disable the gate entirely")
	}
}

func TestSkillGateAutoModeAutoAllows(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	r.SetApprovalMode(policy.ModeAuto)

	if _, err := runSkillGateTurn(t, r, p, "test-skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if state.PendingSkillGate() != nil {
		t.Fatal("auto mode must auto-allow without prompting")
	}
}

func TestSkillGateSubagentNeverGates(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	r.Role = RoleSubtask

	if _, err := runSkillGateTurn(t, r, p, "test-skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	if state.PendingSkillGate() != nil {
		t.Fatal("subagent runners must never gate")
	}
}

// TestSkillGateDenyMessageVerbatim covers the plan's "deny message verbatim"
// bullet at the unit boundary: the gate's returned tool message carries the
// guidance text and the skill name. The message is fed back to the model as
// a RoleTool message; it is not written to the user-facing transcript, so
// the assertion runs against executeToolCall's return value.
func TestSkillGateDenyMessageVerbatim(t *testing.T) {
	state := newTestState(t)
	r, _ := newSkillGateTestRunner(t, state)
	prompts := answerPendingSkillGate(state, 1, session.SkillGateDeny)

	raw, err := json.Marshal(map[string]string{"name": "test-skill"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := r.executeToolCall(context.Background(), ModelAction{Type: ActionToolCall, Tool: "skill.load", Args: raw, ToolCallID: "call_gate_deny"})
	if err != nil {
		t.Fatalf("executeToolCall err = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "you do not need the skill") || !strings.Contains(msgs[0].Content, `"test-skill"`) {
		t.Fatalf("deny message = %q, want the verbatim guidance including the quoted skill name", msgs[0].Content)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 || got[0] != "test-skill" {
		t.Fatalf("prompts = %v, want exactly one for test-skill", got)
	}
	decisions := state.SkillGateDecisions()
	if len(decisions) != 1 || decisions[0].Skill != "test-skill" || decisions[0].Allowed || decisions[0].Count != 1 {
		t.Fatalf("decisions = %+v, want one deny entry for test-skill with count 1", decisions)
	}
}

// TestSkillGateDenyIsSticky covers the sticky half: after one deny, the next
// identical attempt is refused without a second prompt appearing.
func TestSkillGateDenyIsSticky(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	prompts := answerPendingSkillGate(state, 1, session.SkillGateDeny)

	p.Responses = []string{"loading skill", "trying again", "Done."}
	p.ToolCalls = [][]schema.ToolCall{skillLoadCall("test-skill"), skillLoadCall("test-skill"), nil}
	if _, err := r.RunTask(context.Background(), "use the skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 {
		t.Fatalf("prompts = %v, want exactly one (second attempt must be sticky-denied)", got)
	}
	decisions := state.SkillGateDecisions()
	if len(decisions) != 1 || decisions[0].Skill != "test-skill" || decisions[0].Allowed || decisions[0].Count != 2 {
		t.Fatalf("decisions = %+v, want one deny entry for test-skill with count 2", decisions)
	}
}

func TestSkillGateThirdAttemptRePrompts(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	// Deny the first prompt; the sticky deny then refuses attempts 2 and 3
	// silently; on the 3rd RE-attempt (4th call) the deny counter has hit
	// the SkillGateRePromptEvery multiple and the gate prompts again, which
	// the answerer allows so the turn can finish.
	prompts := answerPendingSkillGateChoices(state, []session.SkillGateChoice{session.SkillGateDeny, session.SkillGateAllowOnce})

	p.Responses = append(scriptRepeats(4, "loading skill"), "Done.")
	p.ToolCalls = [][]schema.ToolCall{
		skillLoadCall("test-skill"),
		skillLoadCall("test-skill"),
		skillLoadCall("test-skill"),
		skillLoadCall("test-skill"),
		nil,
	}
	if _, err := r.RunTask(context.Background(), "use the skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := drainPrompts(prompts)
	if len(got) != 2 || got[0] != "test-skill" || got[1] != "test-skill" {
		t.Fatalf("prompts = %v, want two (initial prompt + bounded re-prompt on the 3rd re-attempt)", got)
	}
}

func TestSkillGateAllowSkillSticky(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	prompts := answerPendingSkillGate(state, 1, session.SkillGateAllowSkill)

	p.Responses = []string{"loading skill", "loading again", "Done."}
	p.ToolCalls = [][]schema.ToolCall{skillLoadCall("test-skill"), skillLoadCall("test-skill"), nil}
	if _, err := r.RunTask(context.Background(), "use the skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 {
		t.Fatalf("prompts = %v, want one (session allow must stick)", got)
	}
	decisions := state.SkillGateDecisions()
	if len(decisions) != 1 || decisions[0].Skill != "test-skill" || !decisions[0].Allowed {
		t.Fatalf("decisions = %+v, want one allowed entry for test-skill", decisions)
	}
}

func TestSkillGateAllowAllDisablesGate(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	prompts := answerPendingSkillGate(state, 1, session.SkillGateAllowAll)

	p.Responses = []string{"loading skill", "loading another", "Done."}
	p.ToolCalls = [][]schema.ToolCall{skillLoadCall("test-skill"), skillLoadCall("other-skill"), nil}
	if _, err := r.RunTask(context.Background(), "use the skill"); err != nil {
		t.Fatalf("RunTask err = %v", err)
	}
	got := drainPrompts(prompts)
	if len(got) != 1 {
		t.Fatalf("prompts = %v, want one (allow-all must cover the second skill too)", got)
	}
	if state.SkillGateEnabled() {
		t.Fatal("allow-all must disable the gate for the session")
	}
}

func TestSkillGateCancelledPromptRecordsNothing(t *testing.T) {
	state := newTestState(t)
	r, p := newSkillGateTestRunner(t, state)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the turn as soon as the gate prompt appears.
	go func() {
		for {
			if state.PendingSkillGate() != nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	p.Responses = []string{"loading skill", "Done."}
	p.ToolCalls = [][]schema.ToolCall{skillLoadCall("test-skill"), nil}
	if _, err := r.RunTask(ctx, "use the skill"); err != nil && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("RunTask err = %v, want context cancellation or graceful handling", err)
	}
	if decisions := state.SkillGateDecisions(); len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none (a cancelled prompt is not a deny)", decisions)
	}
}

// TestSkillGateCancelledPromptMessage covers the message half of the
// cancelled-prompt bullet: a ctx already cancelled when the gate fires ends
// the wait immediately, and the model receives the prompt-cancelled
// guidance instead of a load.
func TestSkillGateCancelledPromptMessage(t *testing.T) {
	state := newTestState(t)
	r, _ := newSkillGateTestRunner(t, state)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the gate fires: the wait ends immediately

	raw, err := json.Marshal(map[string]string{"name": "test-skill"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := r.executeToolCall(ctx, ModelAction{Type: ActionToolCall, Tool: "skill.load", Args: raw, ToolCallID: "call_gate_cancel"})
	if err != nil {
		t.Fatalf("executeToolCall err = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "skill load was not approved (prompt cancelled)") {
		t.Fatalf("message = %q, want the prompt-cancelled guidance", msgs[0].Content)
	}
	if decisions := state.SkillGateDecisions(); len(decisions) != 0 {
		t.Fatalf("decisions = %+v, want none (a cancelled prompt is not a deny)", decisions)
	}
	if state.PendingSkillGate() != nil {
		t.Fatal("pending skill gate must be cleared after cancellation")
	}
}
