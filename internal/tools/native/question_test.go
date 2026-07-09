package native

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

func TestQuestionAskBuildsPendingQuestion(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.questionAskTool()

	args, _ := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{"question": "Which auth?", "options": []string{"JWT", "OAuth"}},
			{"question": "Keep legacy?"},
		},
	})

	type result struct {
		res registry.ToolResult
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
		resultCh <- result{res, err}
	}()

	pq := waitPendingQuestion(t, state)
	if len(pq.Questions) != 2 {
		t.Fatalf("Questions len = %d, want 2", len(pq.Questions))
	}
	if pq.Questions[0].Question != "Which auth?" {
		t.Fatalf("Questions[0].Question = %q", pq.Questions[0].Question)
	}
	if !pq.Questions[0].Multi && len(pq.Questions[0].Options) != 2 {
		t.Fatalf("Questions[0].Options = %v", pq.Questions[0].Options)
	}

	pq.ResponseChan <- []session.Answer{
		{Question: "Which auth?", Answer: "JWT"},
		{Question: "Keep legacy?", Answer: session.AnswerUnanswered},
	}

	res := <-resultCh
	if res.err != nil {
		t.Fatalf("handler error: %v", res.err)
	}
	if res.res.Summary != "user answered" {
		t.Fatalf("summary = %q, want user answered", res.res.Summary)
	}
	if state.PendingQuestion() != nil {
		t.Fatal("pending question not cleared")
	}
}

func TestQuestionAskRequiresAtLeastOneQuestion(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.questionAskTool()

	args, _ := json.Marshal(map[string]any{"questions": []any{}})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected error for empty questions array")
	}
}

func TestAskUserAliasWrapsSingleQuestion(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.askUserTool()

	args, _ := json.Marshal(map[string]any{"question": "Should we archive or delete?"})

	type result struct {
		res registry.ToolResult
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
		resultCh <- result{res, err}
	}()

	pq := waitPendingQuestion(t, state)
	if len(pq.Questions) != 1 || pq.Questions[0].Question != "Should we archive or delete?" {
		t.Fatalf("Questions = %+v", pq.Questions)
	}

	pq.ResponseChan <- []session.Answer{
		{Question: "Should we archive or delete?", Answer: "archive"},
	}

	res := <-resultCh
	if res.err != nil {
		t.Fatalf("handler error: %v", res.err)
	}
	if res.res.Summary != "user answered" {
		t.Fatalf("summary = %q, want user answered", res.res.Summary)
	}
}

func waitPendingQuestion(t *testing.T, state *session.State) *session.PendingQuestion {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pq := state.PendingQuestion(); pq != nil {
			return pq
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for pending question to be set")
	return nil
}
