package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

	pq.Respond([]session.Answer{
		{Question: "Which auth?", Answer: "JWT"},
		{Question: "Keep legacy?", Answer: session.AnswerUnanswered},
	})

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

func TestQuestionToolDescriptionsGuideUsage(t *testing.T) {
	tools := &toolSet{}

	ask := tools.questionAskTool()
	for _, want := range []string{"options", "never as lettered text", "mode.request"} {
		if !strings.Contains(ask.Description, want) {
			t.Errorf("question.ask description missing %q\n%s", want, ask.Description)
		}
	}
}

func TestQuestionAskAcceptsObjectOptionsEndToEnd(t *testing.T) {
	state := session.New(config.Config{}, "/tmp", time.Now(), session.Persistence{})
	tools := &toolSet{sessionState: state}
	tool := tools.questionAskTool()

	args := json.RawMessage(`{"questions":[{"question":"Pick","options":[{"label":"a","description":"d"}]}]}`)

	if err := ValidateQuestionAsk(args); err != nil {
		t.Fatalf("ValidateQuestionAsk rejected object options: %v", err)
	}

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
	if len(pq.Questions) != 1 {
		t.Fatalf("Questions len = %d, want 1", len(pq.Questions))
	}
	opts := pq.Questions[0].Options
	if len(opts) != 1 {
		t.Fatalf("Options len = %d, want 1", len(opts))
	}
	if opts[0].Label != "a" || opts[0].Description != "d" {
		t.Fatalf("option = %+v, want {Label:a Description:d}", opts[0])
	}

	pq.Respond([]session.Answer{{Question: "Pick", Answer: "a"}})
	if res := <-resultCh; res.err != nil {
		t.Fatalf("handler error: %v", res.err)
	}
}

func TestValidateQuestionAskRejectsMalformedShape(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"options not an array", `{"questions":[{"question":"Pick","options":{"label":"a"}}]}`},
		{"option object missing label", `{"questions":[{"question":"Pick","options":[{"description":"d"}]}]}`},
		{"missing question field", `{"questions":[{"options":["a"]}]}`},
		{"empty questions", `{"questions":[]}`},
		{"question not a string", `{"questions":[{"question":5,"options":["a"]}]}`},
		{"option label not a string", `{"questions":[{"question":"Pick","options":[{"label":5}]}]}`},
		{"questions not an array", `{"questions":{"question":"Pick"}}`},
		{"null option", `{"questions":[{"question":"Pick","options":["a",null]}]}`},
		{"null option alone", `{"questions":[{"question":"Pick","options":[null]}]}`},
		{"empty option label", `{"questions":[{"question":"Pick","options":[{"label":""}]}]}`},
		{"multi not a boolean", `{"questions":[{"question":"Pick","multi":"yes"}]}`},
		{"description not a string", `{"questions":[{"question":"Pick","options":[{"label":"a","description":5}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateQuestionAsk(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "example:") {
				t.Errorf("error missing example: %v", err)
			}
			// A top-level shape error (questions is not an array at all)
			// has no indexed field to name, so it is allowed to identify the
			// field by name instead of by path.
			if !strings.Contains(err.Error(), "questions[0]") &&
				!strings.Contains(err.Error(), "questions must be") &&
				!strings.Contains(err.Error(), "'questions' array") {
				t.Errorf("error missing field path: %v", err)
			}
		})
	}
}

func TestValidateQuestionAskAcceptsMixedOptions(t *testing.T) {
	raw := json.RawMessage(`{"questions":[{"question":"Pick","options":["simple",{"label":"rich","description":"more"}]}]}`)
	if err := ValidateQuestionAsk(raw); err != nil {
		t.Fatalf("ValidateQuestionAsk rejected mixed options: %v", err)
	}
}

// TestValidateQuestionAskRejectsUnknownProperties pins the strictness the
// declared schema enforces. The validator and the schema must agree: a
// single call goes through ValidateQuestionAsk, while the same tool inside
// an actions[] batch goes through registry.ValidateArgs against the
// schema. Tolerating a stray key in one path only would make the identical
// payload succeed as a single call and fail in a batch.
func TestValidateQuestionAskRejectsUnknownProperties(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"top level", `{"questions":[{"question":"Pick"}],"extra":"x"}`},
		{"question object", `{"questions":[{"question":"Pick","extra":true}]}`},
		{"option object", `{"questions":[{"question":"Pick","options":[{"label":"a","extra":1}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateQuestionAsk(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), "unknown propert") {
				t.Errorf("error must name the unknown property: %v", err)
			}
			if !strings.Contains(err.Error(), "example:") {
				t.Errorf("error missing example: %v", err)
			}
		})
	}
}

// TestQuestionAskSchemaAndValidatorAgreeOnUnknownProperties verifies both
// paths reject the same payload, so a model cannot get a different verdict
// depending on whether it batched the call.
func TestQuestionAskSchemaAndValidatorAgreeOnUnknownProperties(t *testing.T) {
	tools := &toolSet{}
	tool := tools.questionAskTool()

	raw := json.RawMessage(`{"questions":[{"question":"Pick","options":[{"label":"a","extra":1}]}]}`)
	if err := ValidateQuestionAsk(raw); err == nil {
		t.Fatal("ValidateQuestionAsk accepted an unknown property")
	}
	if err := registry.ValidateArgs(tool, raw); err == nil {
		t.Fatal("declared schema accepted an unknown property")
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

// TestQuestionSteeringCancelAndAnswerRace pins the two settlements of a
// question owned by an interruptible operation, and that neither leaves a
// stale clear behind.
//
// A human answer that claims the prompt first is the tool's result: the model
// is told what the user chose, not that the operation was interrupted. A
// cancellation that claims it first is reported as the distinct steering cause
// — never as a fabricated answer — and a late answer cannot settle a prompt
// that no longer exists.
func TestQuestionSteeringCancelAndAnswerRace(t *testing.T) {
	args, err := json.Marshal(map[string]any{
		"questions": []map[string]any{{"question": "Which auth?", "options": []string{"JWT", "OAuth"}}},
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	// startQuestion runs one question.ask under opCtx and returns its outcome
	// channel. It takes no *testing.T so it is safe inside the goroutine a
	// blocking handler needs.
	startQuestion := func(state *session.State, opCtx context.Context) <-chan toolOutcome {
		tools := &toolSet{sessionState: state}
		tool := tools.questionAskTool()
		done := make(chan toolOutcome, 1)
		go func() {
			res, err := tool.Handler(opCtx, registry.ToolCall{ID: "call-q", Name: "question.ask", Args: args})
			done <- toolOutcome{res: res, err: err}
		}()
		return done
	}

	t.Run("a winning answer is the result", func(t *testing.T) {
		state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
		opCtx, turn, _ := steeringToolContext(t, state, nil)
		done := startQuestion(state, opCtx)

		pq := waitPendingQuestion(t, state)
		retired := pq.Key
		answers := []session.Answer{{Question: "Which auth?", Answer: "JWT"}}
		pq.Respond(answers)

		out := awaitOutcome(t, done)
		if out.err != nil {
			t.Fatalf("a winning answer was reported as an interruption: %v", out.err)
		}
		if out.res.Summary != "user answered" {
			t.Fatalf("summary = %q, want the answer result", out.res.Summary)
		}
		if !strings.Contains(out.res.Content, `"Which auth?"="JWT"`) {
			t.Fatalf("content = %q, want the user's actual answer", out.res.Content)
		}

		// A newer question is now the one on screen. Neither the retired
		// request's cleanup nor the old operation's cancellation may clear it.
		newer := &session.PendingQuestion{
			Questions:    []session.Question{{Question: "Keep legacy?"}},
			ResponseChan: make(chan []session.Answer, 1),
		}
		if _, err := state.RegisterQuestion(context.Background(), newer); err != nil {
			t.Fatalf("RegisterQuestion: %v", err)
		}
		state.ClearPrompt(retired)
		if got := state.PendingQuestion(); got == nil || got.Key.ID != newer.Key.ID {
			t.Fatalf("the retired key's cleanup cleared a newer question: %+v", got)
		}
		steerNow(t, state, turn, "r1")
		if got := state.PendingQuestion(); got == nil || got.Key.ID != newer.Key.ID {
			t.Fatalf("the old operation's cancellation reached an independent request: %+v", got)
		}
		// Clean up the newer request so nothing outlives the test.
		state.CancelPrompt(newer.Key, session.ErrSendNow)
	})

	t.Run("a winning cancellation is the distinct cause", func(t *testing.T) {
		state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{})
		opCtx, turn, _ := steeringToolContext(t, state, nil)
		done := startQuestion(state, opCtx)

		pq := waitPendingQuestion(t, state)
		canceled := pq.Key

		// The interruption owns the settlement from acceptance onwards.
		steerNow(t, state, turn, "r1")

		out := awaitOutcome(t, done)
		if !errors.Is(out.err, session.ErrSendNow) {
			t.Fatalf("canceled question error = %v, want the distinct steering cause", out.err)
		}
		if strings.Contains(out.res.Content, "user answered") || strings.Contains(out.res.Content, "JWT") {
			t.Fatalf("a canceled question reported a fabricated human answer: %q", out.res.Content)
		}

		// A late answer on the settled request settles nothing.
		if state.ResolveQuestion(canceled, []session.Answer{{Question: "Which auth?", Answer: "OAuth"}}) {
			t.Fatal("a late answer claimed a question the interruption had already settled")
		}
		if state.PendingQuestion() != nil {
			t.Fatal("the canceled question's slot was left pending")
		}
	})
}
