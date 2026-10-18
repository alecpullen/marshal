package acp

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"marshal/internal/app/session"
)

// scriptedQuestionClient answers every request deterministically from the
// question text and records the order in which it was asked, thread-safely:
// the turn forwarder runs one bridge goroutine per question event.
type scriptedQuestionClient struct {
	mu    sync.Mutex
	asked []string
}

func (c *scriptedQuestionClient) RequestQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, q := range req.Questions {
		c.asked = append(c.asked, q.Question)
	}
	var answers []session.Answer
	for _, q := range req.Questions {
		answers = append(answers, session.Answer{Question: q.Question, Answer: "answer to " + q.Question})
	}
	return QuestionResponse{Answers: answers}, nil
}

func (c *scriptedQuestionClient) askedQuestions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
}

// waitRegisteredAnswer reads the one canonical delivery on the runner's own
// response channel and requires the scripted answer for the named question.
func waitRegisteredAnswer(ctx context.Context, q *session.PendingQuestion, question string) error {
	want := "answer to " + question
	select {
	case a := <-q.ResponseChan:
		if len(a) != 1 || a[0].Question != question || a[0].Answer != want {
			return fmt.Errorf("answers = %#v, want %q -> %q", a, question, want)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("the turn ended while %q was still pending: %w", question, ctx.Err())
	}
}

// TestACPTurnDeliversTwoRegisteredQuestionsInOneTurn pins the per-turn question
// dedupe identity. A registered question publishes an event copy that carries
// the canonical key but no response channel, so keying the turn's dedupe on
// pending.ResponseChan collapses every registered question in the turn onto one
// shared nil key and silently skips the second one — the runner then blocks
// forever on a question that was never asked. Each registered question in one
// turn must reach the client and receive its answer through its own key.
func TestACPTurnDeliversTwoRegisteredQuestionsInOneTurn(t *testing.T) {
	client := &scriptedQuestionClient{}
	h := newSendNowHarnessWith(t, true, &fakePermissionClient{}, client)
	// A skipped second question leaves the runner blocked; the harness cancel
	// tears the turn down however the test finishes.
	t.Cleanup(func() { h.cancel() })

	h.run = RunnerFunc(func(ctx context.Context, prompt string) error {
		turnID, err := h.state.BeginSteeringTurn(ctx)
		if err != nil {
			return err
		}
		defer h.state.EndSteeringTurn(turnID, "test complete")
		opCtx, ref, err := h.state.BeginSteeringOperation(ctx, turnID, session.OperationModel)
		if err != nil {
			return err
		}

		q1 := &session.PendingQuestion{
			Questions:    []session.Question{{Question: "first"}},
			ResponseChan: make(chan []session.Answer, 1),
		}
		key1, err := h.state.RegisterQuestion(opCtx, q1)
		if err != nil {
			return err
		}
		if err := waitRegisteredAnswer(ctx, q1, "first"); err != nil {
			return err
		}
		h.state.ClearPrompt(key1)
		h.state.FinishSteeringOperation(ref, nil)

		// A second, independent question later in the same turn. Its event copy
		// is another registered prompt with no response channel, so it must be
		// deduped by its own key — not by a channel that isn't there.
		q2 := &session.PendingQuestion{
			Questions:    []session.Question{{Question: "second"}},
			ResponseChan: make(chan []session.Answer, 1),
		}
		key2, err := h.state.RegisterQuestion(ctx, q2)
		if err != nil {
			return err
		}
		defer h.state.ClearPrompt(key2)
		return waitRegisteredAnswer(ctx, q2, "second")
	})
	done := h.start()

	// Both registered questions must reach the bridge. With channel-keyed
	// dedupe the second event copy is dropped before it is ever asked.
	waitFor(t, "the second registered question to reach the bridge", func() bool {
		return len(client.askedQuestions()) >= 2
	})

	if err := h.awaitTurn(done); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}
	asked := client.askedQuestions()
	if len(asked) != 2 || asked[0] != "first" || asked[1] != "second" {
		t.Fatalf("the question bridge was asked %v, want [first second] in order", asked)
	}
}
