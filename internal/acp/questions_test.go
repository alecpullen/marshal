package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"marshal/internal/app/session"
)

// TestQuestionRequestOptionsWireShape pins the JSON shape of question
// options on the ACP wire. session.QuestionOption marshals as an object
// ({"label":...}) rather than the bare string older clients may expect,
// so this test makes that protocol change deliberate and visible instead
// of silent. The bundled web client tolerates both shapes (see
// web/ui/src/lib/fleet.ts toOption), but an out-of-tree client that
// assumes strings would break here.
func TestQuestionRequestOptionsWireShape(t *testing.T) {
	req := QuestionRequest{
		SessionID:  "sess_1",
		QuestionID: "q_1",
		Questions: []session.Question{{
			Question: "pick",
			Options: []session.QuestionOption{
				{Label: "a"},
				{Label: "b", Description: "second"},
			},
		}},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `"options":[{"label":"a"},{"label":"b","description":"second"}]`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("options wire shape changed; want substring %s in:\n%s", want, raw)
	}
}

type fakeQuestionClient struct {
	resp    QuestionResponse
	err     error
	calls   int
	lastReq QuestionRequest
}

func (f *fakeQuestionClient) RequestQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return QuestionResponse{}, f.err
	}
	return f.resp, nil
}

func newPendingQuestion(questions ...session.Question) (*session.PendingQuestion, chan []session.Answer) {
	ch := make(chan []session.Answer, 1)
	return &session.PendingQuestion{Questions: questions, ResponseChan: ch}, ch
}

func TestQuestionBridgeDeliversAnswers(t *testing.T) {
	pending, ch := newPendingQuestion(session.Question{Question: "pick", Options: []session.QuestionOption{{Label: "a"}, {Label: "b"}}})
	client := &fakeQuestionClient{resp: QuestionResponse{
		Answers: []session.Answer{{Question: "pick", Answer: "a"}},
	}}
	bridge := NewQuestionBridge(client)

	if err := bridge.Ask(context.Background(), "sess_1", pending); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := <-ch
	if len(got) != 1 || got[0].Answer != "a" {
		t.Fatalf("answers = %#v", got)
	}
	if client.lastReq.SessionID != "sess_1" {
		t.Fatalf("SessionID = %q", client.lastReq.SessionID)
	}
	if client.lastReq.QuestionID == "" {
		t.Fatal("QuestionID must be set")
	}
	if len(client.lastReq.Questions) != 1 || client.lastReq.Questions[0].Question != "pick" {
		t.Fatalf("Questions = %#v", client.lastReq.Questions)
	}
}

// The AskChild / attribution tests that lived here were removed with the
// subagent question channel in the context-diet Tier 2a cut: the question
// bridge now serves parent-loop question.ask only.

func TestQuestionBridgeDeclinedMapsToUnanswered(t *testing.T) {
	pending, ch := newPendingQuestion(session.Question{Question: "pick"})
	client := &fakeQuestionClient{resp: QuestionResponse{Declined: true}}
	bridge := NewQuestionBridge(client)

	if err := bridge.Ask(context.Background(), "sess_1", pending); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := <-ch
	if len(got) != 1 || got[0].Answer != session.AnswerUnanswered {
		t.Fatalf("answers = %#v, want Unanswered sentinel", got)
	}
}

func TestQuestionBridgeAnswerCountMismatchMapsToUnanswered(t *testing.T) {
	pending, ch := newPendingQuestion(
		session.Question{Question: "one"},
		session.Question{Question: "two"},
	)
	client := &fakeQuestionClient{resp: QuestionResponse{
		Answers: []session.Answer{{Question: "one", Answer: "x"}}, // only 1 of 2
	}}
	bridge := NewQuestionBridge(client)

	if err := bridge.Ask(context.Background(), "sess_1", pending); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	got := <-ch
	if len(got) != 2 || got[0].Answer != session.AnswerUnanswered || got[1].Answer != session.AnswerUnanswered {
		t.Fatalf("answers = %#v, want all Unanswered", got)
	}
}

func TestQuestionBridgeUniqueIDsUnderRapidCalls(t *testing.T) {
	client := &fakeQuestionClient{resp: QuestionResponse{Declined: true}}
	bridge := NewQuestionBridge(client)

	var ids []string
	for i := 0; i < 100; i++ {
		pending, _ := newPendingQuestion(session.Question{Question: "q"})
		if err := bridge.Ask(context.Background(), "sess_test", pending); err != nil {
			t.Fatalf("Ask #%d: %v", i, err)
		}
		ids = append(ids, client.lastReq.QuestionID)
	}

	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate question ID: %s", id)
		}
		seen[id] = true
	}
	if len(ids) != 100 {
		t.Fatalf("expected 100 IDs, got %d", len(ids))
	}
}

func TestQuestionBridgeClientErrorReturnsError(t *testing.T) {
	pending, _ := newPendingQuestion(session.Question{Question: "pick"})
	client := &fakeQuestionClient{err: errors.New("transport dead")}
	bridge := NewQuestionBridge(client)

	if err := bridge.Ask(context.Background(), "sess_1", pending); err == nil {
		t.Fatal("expected error, got nil")
	}
}

// gatedQuestionClient is a QuestionClient whose calls park until the test
// releases them. With ignoreCtx it also ignores context cancellation, so a test
// can deliver a late answer after an interruption.
type gatedQuestionClient struct {
	mu        sync.Mutex
	ignoreCtx bool
	ctxs      []context.Context
	gates     []chan QuestionResponse
	errCount  int
}

func (c *gatedQuestionClient) RequestQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	c.mu.Lock()
	gate := make(chan QuestionResponse, 1)
	c.ctxs = append(c.ctxs, ctx)
	c.gates = append(c.gates, gate)
	ignore := c.ignoreCtx
	c.mu.Unlock()
	if ignore {
		return <-gate, nil
	}
	select {
	case r := <-gate:
		return r, nil
	case <-ctx.Done():
		c.mu.Lock()
		c.errCount++
		c.mu.Unlock()
		return QuestionResponse{}, ctx.Err()
	}
}

func (c *gatedQuestionClient) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.gates)
}

func (c *gatedQuestionClient) errors() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errCount
}

func (c *gatedQuestionClient) ctx(i int) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ctxs[i]
}

func (c *gatedQuestionClient) release(i int, r QuestionResponse) {
	c.mu.Lock()
	gate := c.gates[i]
	c.mu.Unlock()
	gate <- r
}

// testSendNowCancelsQuestionRPC pins that an interrupted question RPC ends only
// that request: the outbound call stops on the operation's cancellation, the
// turn continues, and no answer is fabricated from the interrupted prompt.
func testSendNowCancelsQuestionRPC(t *testing.T) {
	gated := &gatedQuestionClient{} // honours ctx: the RPC stops on interruption
	h := newSendNowHarnessWith(t, true, &fakePermissionClient{}, gated)

	firstCanceled := make(chan struct{})
	secondPrompt := make(chan struct{})
	secondAnswered := make(chan struct{})
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
			Questions:    []session.Question{{Question: "pick"}},
			ResponseChan: make(chan []session.Answer, 1),
		}
		key1, err := h.state.RegisterQuestion(opCtx, q1)
		if err != nil {
			return err
		}
		select {
		case <-q1.ResponseChan:
			// The canonical settlement identifies the interruption; it is never
			// a fabricated human answer.
			if errors.Is(h.state.PromptCause(key1), session.ErrSendNow) {
				close(firstCanceled)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		h.state.ClearPrompt(key1)
		h.state.FinishSteeringOperation(ref, nil)

		// A newer, independent question under the turn itself.
		q2 := &session.PendingQuestion{
			Questions:    []session.Question{{Question: "later"}},
			ResponseChan: make(chan []session.Answer, 1),
		}
		key2, err := h.state.RegisterQuestion(ctx, q2)
		if err != nil {
			return err
		}
		defer h.state.ClearPrompt(key2)
		close(secondPrompt)
		select {
		case <-q2.ResponseChan:
			close(secondAnswered)
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	done := h.start()
	waitFor(t, "the first question RPC", func() bool { return gated.count() >= 1 })
	turnID := h.status().TurnID

	if _, err := h.steerNow(turnID, "r-q", "stop"); err != nil {
		t.Fatalf("steerNow: %v", err)
	}
	waitClosed(t, firstCanceled, "the interrupted question to settle")
	waitClosed(t, secondPrompt, "the newer question")

	// The question bridge returned because the RPC context stopped, which the
	// turn loop must treat as the end of that one request — not as a fatal
	// client failure and not as an answer.
	waitFor(t, "the bridge to observe the cancelled RPC", func() bool { return gated.errors() >= 1 })
	if !h.manager.HasActiveTurn(h.session) {
		t.Fatal("a steering cancellation of a question RPC cancelled the whole turn")
	}
	if err := gated.ctx(0).Err(); err == nil {
		t.Fatal("the outbound question RPC context was not cancelled by the interruption")
	}
	select {
	case <-secondAnswered:
		t.Fatal("the newer question was answered while it was still pending")
	default:
	}
	h.cancel()
	if err := h.awaitTurn(done); err != nil {
		t.Fatalf("PromptTurn: %v", err)
	}
}
