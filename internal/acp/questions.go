package acp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"marshal/internal/app/session"
)

var (
	ErrNilPendingQuestion      = errors.New("acp: question bridge: nil pending question")
	ErrNilPendingChildQuestion = errors.New("acp: question bridge: nil pending child question")
	ErrQuestionMissingItems    = errors.New("acp: question bridge: pending question has no questions")
)

// QuestionRequest is the JSON-RPC payload for `session/request_question`.
// QuestionID lets clients correlate late/duplicate responses; it is
// generated per request.
type QuestionRequest struct {
	SessionID  string             `json:"sessionId"`
	QuestionID string             `json:"questionId"`
	Questions  []session.Question `json:"questions"`
	// FromSubagent, when non-empty, attributes the question to a background
	// subagent so the client can render attribution (e.g. "from subagent
	// <desc>"). It is additive and omitted for parent-loop questions, so the
	// wire shape of an ordinary QuestionRequest is unchanged.
	FromSubagent string `json:"fromSubagent,omitempty"`
}

// QuestionResponse is the JSON-RPC result for `session/request_question`.
// Declined maps to the Unanswered sentinel for every question. Answers must
// contain exactly one Answer per Question, in order; anything else is
// treated as declined.
type QuestionResponse struct {
	Answers  []session.Answer `json:"answers,omitempty"`
	Declined bool             `json:"declined,omitempty"`
}

// QuestionClient is the outbound question request surface used by
// QuestionBridge. Mirrors PermissionClient: production is backed by
// Server.Request; tests drive it with a fake.
type QuestionClient interface {
	RequestQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error)
}

// serverQuestionClient is the production QuestionClient: it sends an
// outbound `session/request_question` request to the connected client via
// Server.Request and returns the response.
type serverQuestionClient struct {
	server *Server
}

func (c *serverQuestionClient) RequestQuestion(ctx context.Context, req QuestionRequest) (QuestionResponse, error) {
	var resp QuestionResponse
	if err := c.server.Request(ctx, "session/request_question", req, &resp); err != nil {
		return QuestionResponse{}, err
	}
	return resp, nil
}

// QuestionBridge translates session.PendingQuestion into a QuestionRequest,
// asks the QuestionClient, and writes the answers back through
// pending.Respond (sync.Once-guarded, so a turn cancel racing the response
// cannot deadlock or double-send).
type QuestionBridge struct {
	client QuestionClient
	idCtr  atomic.Int64
}

func NewQuestionBridge(client QuestionClient) *QuestionBridge {
	if client == nil {
		panic("acp: NewQuestionBridge requires a non-nil QuestionClient")
	}
	return &QuestionBridge{client: client}
}

// Ask sends the pending question to the client and delivers the answers. A
// declined response or an answer-count mismatch collapses to the Unanswered
// sentinel so the runner always receives exactly one Answer per Question, in
// order. A client/transport error is returned to the caller, which falls
// back to Unanswered as well.
func (b *QuestionBridge) Ask(ctx context.Context, sessionID string, pending *session.PendingQuestion) error {
	if pending == nil {
		return ErrNilPendingQuestion
	}
	answers, err := b.ask(ctx, sessionID, pending.Questions, "")
	if err != nil {
		return err
	}
	pending.Respond(answers)
	return nil
}

// AskChild translates a session.PendingChildQuestion into the same
// QuestionRequest wire shape as Ask, tagging it with a child attribution (so
// the client renders "from subagent <desc>"), and writes the answers back
// through pending.Respond. It reuses the same translation and Unanswered
// fallback as Ask rather than forking the logic.
func (b *QuestionBridge) AskChild(ctx context.Context, sessionID string, pending *session.PendingChildQuestion) error {
	if pending == nil {
		return ErrNilPendingChildQuestion
	}
	answers, err := b.ask(ctx, sessionID, pending.Questions, childAttribution(pending.ChildDesc))
	if err != nil {
		return err
	}
	pending.Respond(answers)
	return nil
}

// childAttribution renders the human-readable attribution string for a
// question escalated from a background subagent. An unknown/empty description
// still attributes to "a subagent" rather than dropping the provenance.
func childAttribution(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "from subagent"
	}
	return "from subagent " + desc
}

// ask is the shared translation core for Ask and AskChild: it sends the
// questions to the client (with an optional child attribution) and applies
// the same declined / answer-count-mismatch collapse to Unanswered. A
// client/transport error is returned to the caller.
func (b *QuestionBridge) ask(ctx context.Context, sessionID string, questions []session.Question, fromSubagent string) ([]session.Answer, error) {
	if len(questions) == 0 {
		return nil, ErrQuestionMissingItems
	}
	resp, err := b.client.RequestQuestion(ctx, QuestionRequest{
		SessionID:    sessionID,
		QuestionID:   fmt.Sprintf("q_%d_%d", time.Now().UnixNano(), b.idCtr.Add(1)),
		Questions:    questions,
		FromSubagent: fromSubagent,
	})
	if err != nil {
		return nil, err
	}
	answers := resp.Answers
	if resp.Declined || len(answers) != len(questions) {
		answers = session.UnansweredAnswers(questions)
	}
	return answers, nil
}
