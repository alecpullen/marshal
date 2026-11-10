package service

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
	cancel context.CancelFunc
	token  *session.TurnStopToken // nil until the run registers its stop group
	// releaseRun frees the session's one run slot (P4 D2). It is nil when the
	// session has no state, as some tests' sessions do.
	releaseRun func()
	stopped    bool // Stop reached the run
	// ending marks a stop that is the session's end, which writes no cancel
	// note (P4 D4 decision 6).
	ending bool
	// run is the run in flight as the status reports it. It is set once the
	// run is admitted, and read back when the run ends (P4 D4).
	run *acpwire.Run
}

// runSpec says what runTurn runs: its kind, whether it detaches from its
// caller's context, and the work. finish, when set, runs on the run's own
// goroutine after the work returns and before the note: Prompt's put-back.
type runSpec struct {
	kind   acpwire.RunKind
	detach bool
	work   func(context.Context) error
	finish func(stopped, started bool, err error)
}

// runResult is what a finished run reports to the caller still waiting.
type runResult struct {
	stopped, started bool
	err              error
}

var (
	errTurnBusy     = &acpwire.Error{Code: acpwire.CodeBusy, Message: "inproc: a run is already in flight"}
	errNoRunner     = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no agent runner"}
	errNoReview     = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no review runner"}
	errNoAgentRun   = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no agent runner for custom agents"}
	errNoSuggest    = &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "inproc: no suggestion provider"}
	errNoProposal   = errors.New("no subagent factory wired")
	errRangeAndBase = errors.New("review: --base and an explicit range are mutually exclusive")
	errNoAgentName  = errors.New("inproc: an agent run needs an agent name")
)

// Prompt runs one turn with params.Text as the user's message. A run that
// cannot start puts a queued text back on the steering queue, so the caller's
// next attempt still has it.
//
// The turn is the session's, not this call's (P4 D4): a caller that leaves
// stops waiting, not the turn. Only Stop, the end flow or the session's close
// ends it.
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
	stopped, _, err := s.runTurn(ctx, runSpec{
		kind:   acpwire.RunTurn,
		detach: true,
		work: func(ctx context.Context) error {
			return runner.Run(ctx, text)
		},
		// The queue is the caller's, not the run's: a turn a stop skipped
		// before its work began never consumed the text, so it goes back even
		// when the caller is no longer there to ask for it.
		finish: func(stopped, started bool, _ error) {
			if stopped && !started {
				s.requeue(params)
			}
		},
	})
	if err != nil {
		// A refusal never reached the run, so its put-back stays here.
		if code, ok := acpclient.CodeOf(err); ok && (code == acpwire.CodeBusy || code == acpwire.CodeQuiescing) {
			s.requeue(params)
			return acpwire.PromptResult{}, err
		}
	}
	if stopped {
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
	return s.stopRun(cause, false)
}

// stopRun is Stop's body. ending marks the stop as the session's end, which
// writes no cancel note (P4 D4 decision 6). Stop passes false; Release passes
// true. Neither changes what the stop does.
func (s *Session) stopRun(cause string, ending bool) error {
	s.mu.Lock()
	slot := s.turn
	if slot == nil {
		s.mu.Unlock()
		return nil
	}
	slot.stopped = true
	if ending {
		slot.ending = true
	}
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
// returns no error. A caller that leaves does not stop the review (P4 D4).
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
	stopped, _, err := s.runTurn(ctx, runSpec{
		kind:   acpwire.RunReview,
		detach: true,
		work: func(ctx context.Context) error {
			return s.review(ctx, params)
		},
	})
	// A stopped review is not an error: the stop already reported the cancel,
	// and the reviewer's own context error is that stop, not a failure.
	if stopped {
		return nil
	}
	return err
}

// RunAgent runs the named custom agent with the goal. A stopped run returns no
// error. A caller that leaves does not stop the run (P4 D4).
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
	stopped, _, err := s.runTurn(ctx, runSpec{
		kind:   acpwire.RunAgent,
		detach: true,
		work: func(ctx context.Context) error {
			return s.runAgent(ctx, params)
		},
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
// A run is the session's, not its caller's (P4 D4). A detaching kind — every
// kind but author — runs on the session's context, so a caller whose call ends
// answers at once and leaves the run going; only Stop, the end flow or the
// session's close ends it. An authoring run stays its caller's, because only
// that caller reads its result.
//
// started is false when a stop (or a pending one) skipped the run before it
// reached the work, so a caller holding the text can put it back: nothing
// consumed it. It is true once the work has been entered, even if the stop
// then cancels it.
func (s *Session) runTurn(ctx context.Context, spec runSpec) (stopped bool, started bool, err error) {
	parent := ctx
	if spec.detach {
		parent = s.ctx
	}
	runCtx, cancel := context.WithCancel(parent)
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

	// The session's one run slot is claimed at admission (P4 D2): a run on
	// ACP's turn path holds the same claim, so a second run is refused here
	// rather than running beside the first. A state with no claim (nil) always
	// succeeds, as it has no slot to share.
	releaseRun, claimed := s.state.ClaimRun()
	if !claimed {
		s.mu.Lock()
		if s.turn == slot {
			s.turn = nil
		}
		s.mu.Unlock()
		cancel()
		return false, false, errTurnBusy
	}
	slot.releaseRun = releaseRun

	// The run is registered as a cancellable parent operation, so the
	// conversation's end pass (stopEndWork) cancels the run's context through
	// this hook. Without it, a run that outlives its caller would leave the
	// runtime's quiesce waiting on work nothing can stop (P4 D4).
	if err := s.state.BeginWorkCancelable(cancel); err != nil {
		s.mu.Lock()
		if s.turn == slot {
			s.turn = nil
		}
		s.mu.Unlock()
		slot.releaseRun()
		slot.releaseRun = nil
		cancel()
		// A session that is ending refuses the turn as quiescing does: both
		// are admission refusals the TUI names, not failed turns.
		if errors.Is(err, session.ErrSessionQuiescing) || errors.Is(err, session.ErrConversationEnding) {
			return false, false, &acpwire.Error{Code: acpwire.CodeQuiescing, Message: err.Error()}
		}
		return false, false, err
	}

	// Admission stays on the caller's goroutine, so every refusal and every
	// stop that skipped the run is still answered synchronously.
	token, pendingCause := s.state.BeginTurnStop("")
	s.mu.Lock()
	slot.token = token
	// The run is in flight from here: its ID is its stop group's turn ID.
	slot.run = &acpwire.Run{
		ID:        token.TurnID(),
		Kind:      spec.kind,
		StartedAt: acpwire.Millis(s.state.Now()),
	}
	if pendingCause != "" {
		// BeginTurnStop has already stopped the group for that cause, so a
		// stop that landed before this run started skips it outright.
		slot.stopped = true
	}
	s.mu.Unlock()

	// The run and its whole ending belong to the session, so a caller that
	// leaves cannot truncate them (P4 D4 decision 1).
	done := make(chan runResult, 1)
	go func() {
		// A panic in the run's ending is answered as a failed run rather than
		// taking the process down: the caller is waiting on done, and endRun's
		// own net has freed the run slot by the time this recover runs.
		defer func() {
			if r := recover(); r != nil {
				done <- runResult{started: true, err: fmt.Errorf("run ending panicked: %v", r)}
			}
		}()
		done <- s.endRun(slot, spec, runCtx)
	}()

	select {
	case res := <-done:
		return res.stopped, res.started, res.err
	case <-ctx.Done():
		// The caller left: its call answers with its context's error, and the
		// run goes on until the session stops it.
		return false, true, ctx.Err()
	}
}

// endRun runs a run's work and then its whole ending on the run's own
// goroutine (P4 D4 decision 8): the finish hook, the outcome note, the last
// run, and then today's cleanup — idle, the stop group, the work registration
// and the run's context.
func (s *Session) endRun(slot *turnSlot, spec runSpec, runCtx context.Context) runResult {
	// A panic after the work — in the finish hook, the note, or the cleanup
	// below — would otherwise leave the session's run slot claimed for good.
	// The work itself is already panic-safe (callRun recovers); this is the net
	// for everything after it. The normal path nils the claim when it releases
	// it, so the two can never both free the slot.
	defer func() {
		s.mu.Lock()
		releaseRun := slot.releaseRun
		slot.releaseRun = nil
		if s.turn == slot {
			s.turn = nil
		}
		s.mu.Unlock()
		if releaseRun != nil {
			releaseRun()
		}
	}()

	s.mu.Lock()
	skipped := slot.stopped
	s.mu.Unlock()

	var err error
	started := false
	if !skipped {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			err = ctxErr
		} else {
			// The run's work begins here: from this point the text is consumed
			// even if a stop cancels it.
			started = true
			err = runError(callRun(runCtx, spec.work))
		}
	}

	s.mu.Lock()
	stopped := slot.stopped
	s.mu.Unlock()
	if spec.finish != nil {
		spec.finish(stopped, started, err)
	}

	outcome := runOutcome(stopped, runCtx, err)
	endedAt := acpwire.Millis(s.state.Now())

	// The note is the agent's, not the caller's (P4 D6): it is written here,
	// on the run's own goroutine, so it lands even when the caller has gone.
	// A user's stop is the only stop that writes the cancel note, and a
	// failure is the only outcome that writes the failure note.
	s.mu.Lock()
	userStop := stopped && !slot.ending && !s.closed.Load()
	s.mu.Unlock()
	if note := runNote(spec.kind, outcome, userStop, err); note != "" {
		s.state.AddMessage(session.RoleSystem, note, session.ContentTypePlain)
	}

	// lastRun and the slot's release happen in one critical section, so a
	// status read never shows neither or both.
	s.mu.Lock()
	slotRun := slot.run
	if slotRun != nil {
		result := &acpwire.RunResult{
			ID:        slotRun.ID,
			Kind:      slotRun.Kind,
			StartedAt: slotRun.StartedAt,
			EndedAt:   endedAt,
			Outcome:   outcome,
		}
		if outcome == acpwire.RunFailed {
			result.Error = wireErrorOf(err)
		}
		s.lastRun = result
	}
	if s.turn == slot {
		s.turn = nil
	}
	// The run's claim is released in the same critical section that clears
	// the slot (P4 D2): a refused start sees the slot free the moment the run
	// has ended, and a status read never shows a run with no slot.
	if slot.releaseRun != nil {
		slot.releaseRun()
		slot.releaseRun = nil
	}
	token := slot.token
	s.mu.Unlock()

	// The rest of the cleanup runs in the order the turn's readers expect: the
	// session is idle, then the stop group and the work registration are
	// cleared, and the run's context is cancelled last.
	s.state.SetActivity(session.Activity{Kind: session.ActivityIdle})
	s.state.EndTurnStop(token)
	s.state.EndWork()
	slot.cancel()

	return runResult{stopped: stopped, started: started, err: err}
}

// runOutcome is how a finished run ended, in the order the plan checks them
// (P4 D4 decision 5): a run its gate paused is gated even when stopped, and a
// stopped or context-cancelled run is cancelled whatever it returned.
func runOutcome(stopped bool, runCtx context.Context, err error) acpwire.RunOutcome {
	switch {
	case acpclient.IsCode(err, acpwire.CodeHumanGateRequired):
		return acpwire.RunGated
	case stopped || errors.Is(err, context.Canceled) || runCtx.Err() != nil:
		return acpwire.RunCancelled
	case err != nil:
		return acpwire.RunFailed
	default:
		return acpwire.RunDone
	}
}

// runNote is the outcome note a run of kind writes, or "" for none (P4 D6).
// The cancel note is the user's stop and nothing else; the failure note is
// every failed run, worded for the kind that failed.
func runNote(kind acpwire.RunKind, outcome acpwire.RunOutcome, userStop bool, err error) string {
	switch outcome {
	case acpwire.RunCancelled:
		if userStop {
			return "Agent turn cancelled."
		}
	case acpwire.RunFailed:
		if err == nil {
			return ""
		}
		if kind == acpwire.RunAuthor {
			return "✘ Plan authoring failed: " + firstLine(err.Error())
		}
		return "✘ Turn failed: " + firstLine(err.Error())
	}
	return ""
}

// wireErrorOf is err as the wire reports a failed run's error: a coded error
// as itself, and any other as an uncoded one with its own sentence (P4 D4
// decision 5).
func wireErrorOf(err error) *acpwire.Error {
	if err == nil {
		return nil
	}
	var coded *acpwire.Error
	if errors.As(err, &coded) {
		return coded
	}
	return &acpwire.Error{Message: err.Error()}
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
