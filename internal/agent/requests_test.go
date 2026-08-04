package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestRequestQuestionsRecordsQAInTranscript pins the dedupe contract: the
// popup owns question text while asking, so nothing is written to the
// transcript up front; after the user answers, exactly one compact Q&A
// record appears, omitting unanswered questions.
func TestRequestQuestionsRecordsQAInTranscript(t *testing.T) {
	state := newTestState(t)
	r := NewRunner(&agenttest.ScriptedProvider{}, registry.New(), policy.NewEngine(&config.Config{}, nil), state, "test-model")

	resCh := make(chan []session.Answer, 1)
	go func() {
		answers, _ := r.requestQuestions(context.Background(), []session.Question{
			{Question: "Which auth?"},
			{Question: "Keep legacy?"},
		})
		resCh <- answers
	}()

	deadline := time.Now().Add(2 * time.Second)
	for state.PendingQuestion() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if state.PendingQuestion() == nil {
		t.Fatal("PendingQuestion never appeared")
	}

	// While the question is pending, nothing about it may be in the
	// transcript yet.
	for _, m := range state.Messages() {
		if strings.Contains(m.Content, "Which auth?") {
			t.Fatalf("question text written to transcript before the answer: %q", m.Content)
		}
	}

	state.PendingQuestion().Respond([]session.Answer{
		{Question: "Which auth?", Answer: "JWT"},
		{Question: "Keep legacy?", Answer: session.AnswerUnanswered},
	})
	<-resCh

	for _, m := range state.Messages() {
		if m.Role == session.RoleAssistant && strings.Contains(m.Content, "Which auth?") {
			t.Fatalf("question text leaked into transcript as assistant message: %q", m.Content)
		}
	}
	var records []string
	for _, m := range state.Messages() {
		if m.Role == session.RoleUser {
			records = append(records, m.Content)
		}
	}
	want := "- \"Which auth?\": \"JWT\""
	if len(records) != 1 || records[0] != want {
		t.Fatalf("transcript records = %v, want exactly [%q]", records, want)
	}
}
