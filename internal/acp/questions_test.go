package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestQuestionBridgeAskChildAttributesAndDeliversAnswers(t *testing.T) {
	ch := make(chan []session.Answer, 1)
	pending := &session.PendingChildQuestion{
		ChildID:      7,
		ChildDesc:    "researcher",
		Questions:    []session.Question{{Question: "pick", Options: []session.QuestionOption{{Label: "a"}, {Label: "b"}}}},
		ResponseChan: ch,
	}
	client := &fakeQuestionClient{resp: QuestionResponse{
		Answers: []session.Answer{{Question: "pick", Answer: "a"}},
	}}
	bridge := NewQuestionBridge(client)

	if err := bridge.AskChild(context.Background(), "sess_1", pending); err != nil {
		t.Fatalf("AskChild: %v", err)
	}
	if client.lastReq.FromSubagent != "from subagent researcher" {
		t.Fatalf("FromSubagent = %q, want %q", client.lastReq.FromSubagent, "from subagent researcher")
	}
	if client.lastReq.SessionID != "sess_1" || len(client.lastReq.Questions) != 1 {
		t.Fatalf("question request = %#v", client.lastReq)
	}
	got := <-ch
	if len(got) != 1 || got[0].Answer != "a" {
		t.Fatalf("answers = %#v, want one answer %q", got, "a")
	}
}

func TestQuestionBridgeAskChildNil(t *testing.T) {
	bridge := NewQuestionBridge(&fakeQuestionClient{})
	if err := bridge.AskChild(context.Background(), "sess_1", nil); err == nil {
		t.Fatal("expected error for nil pending child question, got nil")
	}
}

func TestQuestionBridgeAskChildEmptyDescStillAttributes(t *testing.T) {
	if got := childAttribution("  "); got != "from subagent" {
		t.Fatalf("childAttribution(blank) = %q, want %q", got, "from subagent")
	}
	if got := childAttribution(" researcher "); got != "from subagent researcher" {
		t.Fatalf("childAttribution(trim) = %q, want %q", got, "from subagent researcher")
	}
}

// TestQuestionRequestFromSubagentWireShape pins the additive attribution
// field: a parent-loop question omits it entirely, while a child question
// carries it so the client can render provenance.
func TestQuestionRequestFromSubagentWireShape(t *testing.T) {
	parent, err := json.Marshal(QuestionRequest{SessionID: "s", QuestionID: "q_1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(parent), "fromSubagent") {
		t.Fatalf("parent question must omit fromSubagent, got:\n%s", parent)
	}

	child, err := json.Marshal(QuestionRequest{SessionID: "s", QuestionID: "q_2", FromSubagent: "from subagent researcher"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(child), `"fromSubagent":"from subagent researcher"`) {
		t.Fatalf("child question missing attribution, got:\n%s", child)
	}
}

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
