package acp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"marshal/internal/app/session"
)

var (
	ErrNilPendingQuestion   = errors.New("acp: question bridge: nil pending question")
	ErrQuestionMissingItems = errors.New("acp: question bridge: pending question has no questions")
)

// QuestionRequest is the JSON-RPC payload for `session/request_question`.
// QuestionID lets clients correlate late/duplicate responses; it is
// generated per request.
type QuestionRequest struct {
	SessionID  string             `json:"sessionId"`
	QuestionID string             `json:"questionId"`
	Questions  []session.Question `json:"questions"`
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
//
// Delivery goes through pending.Respond, the canonical responder: a registered
// prompt settles through the session's registry (which arbitrates the answer
// against an accepted steering cancellation), while an unregistered legacy
// fixture keeps its own channel. A canceled operation therefore never receives
// a fabricated answer here.
func (b *QuestionBridge) Ask(ctx context.Context, sessionID string, pending *session.PendingQuestion) error {
	if pending == nil {
		return ErrNilPendingQuestion
	}
	answers, err := b.ask(ctx, sessionID, pending.Questions)
	if err != nil {
		return err
	}
	pending.Respond(answers)
	return nil
}

// promptRPCContext derives the context one outbound prompt RPC runs under: the
// RPC context ends when parent ends, when original ends, or when settled
// closes. A nil original or settled is ignored. It is a child of parent (the
// turn context, so a full turn stop still ends the RPC), also cancelled as soon
// as original — the prompt's own owning operation context, as returned by
// State.PromptContext — is done, and as soon as settled — the channel
// State.PromptSettled answers for the same key — closes. That lets a steering
// interruption, or the session's own settlement of the prompt, end the pending
// request promptly instead of leaving the permission/question goroutine blocked
// until a late client answer.
//
// The returned function stops the watcher and cancels the derived context;
// callers must always call it.
func promptRPCContext(parent, original context.Context, settled <-chan struct{}) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancelCause(parent)
	stop := func() bool { return false }
	if original != nil {
		stop = context.AfterFunc(original, func() {
			cancel(context.Cause(original))
		})
	}
	// One watcher ends the context when the prompt settles. It exits on the
	// context's own end too, so the cleanup below can join it without a leak.
	var watch sync.WaitGroup
	if settled != nil {
		watch.Add(1)
		go func() {
			defer watch.Done()
			select {
			case <-settled:
				cancel(context.Canceled)
			case <-ctx.Done():
			}
		}()
	}
	return ctx, func() {
		// Cancel first, then wait: the watcher's only other exit is this
		// context ending.
		cancel(context.Canceled)
		watch.Wait()
		stop()
		cancel(context.Canceled)
	}
}

// settledElsewhere reports whether settled is closed.
func settledElsewhere(settled <-chan struct{}) bool {
	select {
	case <-settled:
		return true
	default:
		return false
	}
}

// ask is the shared translation core for Ask: it sends the questions to the
// client and applies the same declined / answer-count-mismatch collapse to
// Unanswered. A client/transport error is returned to the caller.
func (b *QuestionBridge) ask(ctx context.Context, sessionID string, questions []session.Question) ([]session.Answer, error) {
	if len(questions) == 0 {
		return nil, ErrQuestionMissingItems
	}
	resp, err := b.client.RequestQuestion(ctx, QuestionRequest{
		SessionID:  sessionID,
		QuestionID: fmt.Sprintf("q_%d_%d", time.Now().UnixNano(), b.idCtr.Add(1)),
		Questions:  questions,
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
