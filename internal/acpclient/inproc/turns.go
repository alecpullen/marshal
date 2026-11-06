package inproc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/acpclient"
	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/watch"
)

// Runner runs one turn of the agent loop. *agent.Runner satisfies it.
type Runner interface {
	Run(ctx context.Context, goal string) error
}

// turnSlot is the run a session has in flight. Its fields are guarded by
// Session.mu.
type turnSlot struct {
	cancel  context.CancelFunc
	token   *session.TurnStopToken // nil until the run registers its stop group
	stopped bool                   // Stop reached the run
}

var (
	errTurnBusy     = &acpwire.Error{Code: acpwire.CodeBusy, Message: "inproc: a run is already in flight"}
	errNoRunner     = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no agent runner"}
	errNoReview     = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no review runner"}
	errNoAgentRun   = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no agent runner for custom agents"}
	errNoSuggest    = errors.New("inproc: no suggestion provider")
	errNoProposal   = errors.New("no subagent factory wired")
	errRangeAndBase = errors.New("review: --base and an explicit range are mutually exclusive")
	errNoAgentName  = errors.New("inproc: an agent run needs an agent name")
)

// Prompt runs one turn with params.Text as the user's message. A run that
// cannot start puts a queued text back on the steering queue, so the caller's
// next attempt still has it.
func (s *Session) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error) {
	if s.closed.Load() {
		return acpwire.PromptResult{}, errSessionClosed
	}
	runner := s.currentRunner()
	if runner == nil {
		s.requeue(params)
		return acpwire.PromptResult{}, errNoRunner
	}
	text := params.Text
	if params.WatchResume != nil {
		text = watch.ResumeGoal(params.WatchResume.Name, s.lastAssistantText(), params.WatchResume.Repeat)
	}
	stopped, started, err := s.runTurn(ctx, func(ctx context.Context) error {
		return runner.Run(ctx, text)
	})
	if err != nil {
		// The queue is the caller's, not the run's: a turn that never started
		// puts a queued text back.
		if code, ok := acpclient.CodeOf(err); ok && (code == acpwire.CodeBusy || code == acpwire.CodeQuiescing) {
			s.requeue(params)
			return acpwire.PromptResult{}, err
		}
	}
	if stopped {
		// A stop that landed before the run began never consumed the text, so
		// put a queued text back rather than dropping it silently.
		if !started {
			s.requeue(params)
		}
		return acpwire.PromptResult{StopReason: acpwire.StopReasonCancelled}, nil
	}
	if err != nil {
		return acpwire.PromptResult{}, err
	}
	return acpwire.PromptResult{StopReason: acpwire.StopReasonEndTurn}, nil
}

// Stop stops the run in flight: the commands attached to it first, then the
// run itself. With nothing running it does nothing.
//
// A Stop that lands as the run completes is indistinguishable from one that
// lands during it: the turn reports cancelled. That is inherent to a stop
// protocol (the stop and the last line race), and harmless — the work the
// caller asked to stop is over either way.
func (s *Session) Stop(ctx context.Context, cause string) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	s.mu.Lock()
	slot := s.turn
	if slot == nil {
		s.mu.Unlock()
		return nil
	}
	slot.stopped = true
	token := slot.token
	s.mu.Unlock()

	if token != nil {
		// Service acceptance precedes agent cancellation (shared contract 3):
		// a stop the service never accepted must not cancel the run.
		if _, err := token.Stop(cause); err != nil && !errors.Is(err, session.ErrNoTurnStopService) {
			s.state.Logger().Warn("turn stop was not accepted by the command service", "error", err)
		}
	}
	slot.cancel()
	return nil
}

// Review runs a reviewer subagent over what params names. A stopped review
// returns no error.
func (s *Session) Review(ctx context.Context, params acpwire.ReviewParams) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if s.review == nil {
		return errNoReview
	}
	if params.Range != "" && params.Base != "" {
		return errRangeAndBase
	}
	stopped, _, err := s.runTurn(ctx, func(ctx context.Context) error {
		return s.review(ctx, params)
	})
	// A stopped review is not an error: the stop already reported the cancel,
	// and the reviewer's own context error is that stop, not a failure.
	if stopped {
		return nil
	}
	return err
}

// RunAgent runs the named custom agent with the goal. A stopped run returns no
// error.
func (s *Session) RunAgent(ctx context.Context, params acpwire.AgentRunParams) error {
	if s.closed.Load() {
		return errSessionClosed
	}
	if s.runAgent == nil {
		return errNoAgentRun
	}
	if strings.TrimSpace(params.Agent) == "" {
		return errNoAgentName
	}
	stopped, _, err := s.runTurn(ctx, func(ctx context.Context) error {
		return s.runAgent(ctx, params)
	})
	// A stopped run is not an error: the stop already reported the cancel, and
	// the agent's own context error is that stop, not a failure.
	if stopped {
		return nil
	}
	return err
}

// Suggest asks the session's model for the user's most likely next reply.
// It takes no turn slot, no work and no stop registration (decision 13).
func (s *Session) Suggest(ctx context.Context, lastMessage string) (string, error) {
	if s.closed.Load() {
		return "", errSessionClosed
	}
	if s.suggest == nil {
		return "", errNoSuggest
	}
	return s.suggest(ctx, lastMessage)
}

// VerifyProposal runs the one-shot subagent that proposes the repository's
// build and test commands. Like Suggest it takes no turn slot (decision 13).
func (s *Session) VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error) {
	if s.closed.Load() {
		return acpwire.VerifyCommands{}, errSessionClosed
	}
	if s.verifyProposal == nil {
		return acpwire.VerifyCommands{}, errNoProposal
	}
	return s.verifyProposal(ctx)
}

// runTurn is the one path every run takes (decision 2). It reports whether
// Stop reached the run, whether the run's work began at all, and the run's
// error or the refusal.
//
// started is false when a stop (or a pending one) skipped the turn before it
// reached run, so a caller holding the text can put it back: nothing consumed
// it. It is true once run has been entered, even if the stop then cancels it.
func (s *Session) runTurn(ctx context.Context, run func(context.Context) error) (stopped bool, started bool, err error) {
	runCtx, cancel := context.WithCancel(ctx)
	slot := &turnSlot{cancel: cancel}

	// One run at a time: the slot is taken before any work registers, so a
	// second caller is refused rather than queued behind the first. A session
	// that closed since Prompt's first check is refused here: this closes the
	// window between that check and the slot.
	s.mu.Lock()
	if s.closed.Load() {
		s.mu.Unlock()
		cancel()
		return false, false, errSessionClosed
	}
	if s.turn != nil {
		s.mu.Unlock()
		cancel()
		return false, false, errTurnBusy
	}
	s.turn = slot
	s.mu.Unlock()

	if err := s.state.BeginWork(); err != nil {
		s.mu.Lock()
		if s.turn == slot {
			s.turn = nil
		}
		s.mu.Unlock()
		cancel()
		// A session that is ending refuses the turn as quiescing does: both
		// are admission refusals the TUI names, not failed turns.
		if errors.Is(err, session.ErrSessionQuiescing) || errors.Is(err, session.ErrConversationEnding) {
			return false, false, &acpwire.Error{Code: acpwire.CodeQuiescing, Message: err.Error()}
		}
		return false, false, err
	}

	// The cleanup runs in the order the turn's readers expect: the session is
	// idle first, the slot is released next, then the stop group and the work
	// registration are cleared and the run's context is cancelled last.
	defer func() {
		s.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
		s.mu.Lock()
		stopped = slot.stopped
		if s.turn == slot {
			s.turn = nil
		}
		token := slot.token
		s.mu.Unlock()
		s.state.EndTurnStop(token)
		s.state.EndWork()
		cancel()
	}()

	token, pendingCause := s.state.BeginTurnStop("")
	s.mu.Lock()
	slot.token = token
	if pendingCause != "" {
		// BeginTurnStop has already stopped the group for that cause, so a
		// stop that landed before this run started skips it outright.
		slot.stopped = true
	}
	stopped = slot.stopped
	s.mu.Unlock()

	if stopped {
		return true, false, nil
	}
	if err := runCtx.Err(); err != nil {
		return false, false, err
	}
	// The run's work begins here: from this point the text is consumed even if
	// a stop cancels it.
	return false, true, runError(callRun(runCtx, run))
}

// callRun runs one turn's work. A panic fails the turn, as runAgentCmd has it,
// instead of taking the process down.
func callRun(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in agent run: %v", r)
		}
	}()
	return run(ctx)
}

// currentRunner is Deps.Runner's runner, or nil. Status reports AgentReady
// from it.
func (s *Session) currentRunner() Runner {
	if s.runner == nil {
		return nil
	}
	return s.runner()
}

// requeue puts a turn that never started back on the steering queue when its
// text came from there. It appends, so a put-back lands behind anything queued
// after it; the text is preserved, its original position is not.
func (s *Session) requeue(params acpwire.PromptParams) {
	if params.Queued {
		s.state.PushSteering(params.Text)
	}
}
