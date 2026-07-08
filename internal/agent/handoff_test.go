package agent

import (
	"context"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

func TestSummarizeAndContinueRebuildsMessages(t *testing.T) {
	p := &scriptedProvider{responses: []string{"## Current State\nRead a.go; still need to patch b.go."}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	old := []schema.ChatMessage{
		BuildSystemPrompt(RoleGeneral, nil, nil, nil),
		{Role: schema.RoleUser, Content: "fix the bug"},
		{Role: schema.RoleUser, Content: "Tool file.read result: huge old output"},
	}
	fresh, err := runner.summarizeAndContinue(context.Background(), p, "test-model", old, "fix the bug")
	if err != nil {
		t.Fatalf("summarizeAndContinue: %v", err)
	}

	// The summarization request must have carried the handoff directive.
	req := p.requests[0]
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
	p := &scriptedProvider{responses: []string{"   "}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	_, err := runner.summarizeAndContinue(context.Background(), p, "test-model",
		[]schema.ChatMessage{{Role: schema.RoleUser, Content: "goal"}}, "goal")
	if err == nil {
		t.Fatal("empty summary must return an error so the caller can fall back to compactMessages")
	}
}
