package agent

import (
	"strings"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
)

func TestBuildHistoryMessagesKeepsUserAndFinalAssistantTurns(t *testing.T) {
	prior := []session.Message{
		{Role: session.RoleUser, Content: "first question", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "thinking aloud", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "first answer", ContentType: session.ContentTypeMarkdown, Final: true},
		{Role: session.RoleSystem, Content: "Agent stopped: ...", ContentType: session.ContentTypePlain},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (user turn + final answer): %+v", len(msgs), msgs)
	}
	if msgs[0].Role != schema.RoleUser || msgs[0].Content != "first question" {
		t.Fatalf("msgs[0] = %+v", msgs[0])
	}
	if msgs[1].Role != schema.RoleAssistant || msgs[1].Content != "first answer" {
		t.Fatalf("msgs[1] = %+v", msgs[1])
	}
}

func TestBuildHistoryMessagesDropsOldestBeyondBudget(t *testing.T) {
	long := strings.Repeat("w ", 4000) // ~2000 tokens at 4 chars/token
	prior := []session.Message{
		{Role: session.RoleUser, Content: "old " + long, ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "old answer " + long, ContentType: session.ContentTypeMarkdown, Final: true},
		{Role: session.RoleUser, Content: "recent question", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "recent answer", ContentType: session.ContentTypeMarkdown, Final: true},
	}
	msgs := buildHistoryMessages(prior, 100) // tiny budget: only the recent pair fits
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want the 2 most recent: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "recent question" || msgs[1].Content != "recent answer" {
		t.Fatalf("kept the wrong turns: %+v", msgs)
	}
}

func TestBuildHistoryMessagesSkipsSalvagedAnswers(t *testing.T) {
	prior := []session.Message{
		{Role: session.RoleUser, Content: "q", ContentType: session.ContentTypePlain},
		{Role: session.RoleAssistant, Content: "half-baked salvage", ContentType: session.ContentTypeMarkdown, Final: true, Salvaged: true},
	}
	msgs := buildHistoryMessages(prior, defaultHistoryBudgetTokens)
	for _, m := range msgs {
		if strings.Contains(m.Content, "half-baked salvage") {
			t.Fatal("salvaged answers must not be replayed as reliable history")
		}
	}
}
