package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestSummarizeAndContinueRebuildsMessages(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"## Current State\nRead a.go; still need to patch b.go."}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	old := []schema.ChatMessage{
		BuildSystemPrompt(RoleGeneral, nil, nil, nil, false),
		{Role: schema.RoleUser, Content: "fix the bug"},
		{Role: schema.RoleUser, Content: "Tool file.read result: huge old output"},
	}
	fresh, err := runner.summarizeAndContinue(context.Background(), p, "test-model", old, "fix the bug", nil)
	if err != nil {
		t.Fatalf("summarizeAndContinue: %v", err)
	}

	// The summarization request must have carried the handoff directive.
	req := p.Requests[0]
	lastMsg := req.Messages[len(req.Messages)-1]
	if !strings.Contains(lastMsg.Content, "ONLY context available") {
		t.Fatalf("summary request missing handoff directive, got: %.120s", lastMsg.Content)
	}

	var joined strings.Builder
	for _, m := range fresh {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "fix the bug") {
		t.Fatal("rebuilt messages lost the original goal")
	}
	if !strings.Contains(joined.String(), "still need to patch b.go") {
		t.Fatal("rebuilt messages missing the summary")
	}
	if strings.Contains(joined.String(), "huge old output") {
		t.Fatal("rebuilt messages still contain the old transcript")
	}
	if fresh[0].Role != schema.RoleSystem {
		t.Fatal("rebuilt messages must start with the system prompt")
	}
}

func TestSummarizeAndContinueErrorsOnEmptySummary(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"   "}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.summarizeAndContinue(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "goal"}}, "goal", nil)
	if err == nil {
		t.Fatal("empty summary must return an error so the caller can terminate the turn (see runner.go summarizeAndContinue path)")
	}
}

// summarizeAndContinue is a from-scratch wire rebuild (the default compaction
// path when rollover is disabled). It must reset skill body ages so the
// fresh wire re-sends full bodies, and it must re-insert the skill hint
// message — mirroring rollover.go exactly. Regression for the review finding
// that the plan missed this fourth wire-rebuild site.
func TestSummarizeAndContinueResetsSkillBodyAgesAndReinsertsHint(t *testing.T) {
	p := &agenttest.ScriptedProvider{Responses: []string{"## Summary\nDone some work."}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)

	idx := skills.NewIndex()
	idx.Set("tdd", skills.Skill{Name: "tdd", Description: "tests first", Body: "FULL BODY TEXT"})
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SkillIndex = idx

	// Activate a skill and age it past BodyFullTurns so it would emit a stub.
	state.Config.Skills.BodyFullTurns = 2
	state.ActivateSkill("tdd")
	state.TickSkillBodyAges()
	state.TickSkillBodyAges()
	if got := state.SkillBodyAge("tdd"); got < 2 {
		t.Fatalf("precondition: age = %d, want >= 2", got)
	}

	// Set a skill hint so we can verify it reappears after compaction.
	runner.skillHints = []string{"tdd"}

	old := []schema.ChatMessage{
		BuildSystemPrompt(RoleGeneral, nil, nil, nil, false),
		{Role: schema.RoleUser, Content: "fix the bug"},
		{Role: schema.RoleUser, Content: "old huge output"},
	}
	fresh, err := runner.summarizeAndContinue(context.Background(), p, "test-model", old, "fix the bug", nil)
	if err != nil {
		t.Fatalf("summarizeAndContinue: %v", err)
	}

	// Body ages must be reset so the fresh wire sends full bodies.
	if got := state.SkillBodyAge("tdd"); got != 0 {
		t.Errorf("body age after compaction = %d, want 0 (ResetAllSkillBodyAges)", got)
	}

	// The fresh wire must contain the full body, not a stub.
	var joined strings.Builder
	for _, m := range fresh {
		joined.WriteString(m.Content)
		joined.WriteString("\n")
	}
	if !strings.Contains(joined.String(), "FULL BODY TEXT") {
		t.Fatal("fresh wire must re-send the full body after age reset, not a stub")
	}
	if strings.Contains(joined.String(), "no longer in context") {
		t.Fatal("fresh wire must not contain a body-age stub message")
	}

	// The hint message must be present (after the system prompt, before the
	// skill body). It should contain the hinted skill's name.
	if !strings.Contains(joined.String(), "tdd") {
		t.Fatal("fresh wire missing the skill hint message")
	}

	// messages[0] must still be the system prompt (hint goes after it).
	if fresh[0].Role != schema.RoleSystem {
		t.Fatal("rebuilt messages must start with the system prompt")
	}
}
