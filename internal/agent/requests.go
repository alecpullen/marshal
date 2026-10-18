package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
)

// requestApproval blocks until the TUI (or any caller driving
// session.PendingToolCall) resolves the pending approval, or ctx is
// cancelled. It registers the request in the session's canonical prompt
// registry (Task 3) and waits on its response channel: the registry arbitrates
// a user decision against an accepted steering cancellation, so a grant that
// was claimed first runs to completion and a cancellation that won leaves no
// approval behind. Clearing the slot is compare-and-clear on the registered
// key, so a late cleanup cannot erase a newer request — and there is no
// wall-clock timeout.
func (r *Runner) requestApproval(ctx context.Context, tool registry.Tool, toolName string, args json.RawMessage, argsMap map[string]interface{}, reason string) (approved bool, edited string, err error) {
	command, _ := argsMap["command"].(string)
	if command == "" {
		command = toolName
	}

	diff := ""
	if patches, ok := patch.FromToolArgs(toolName, argsMap); ok {
		if preview, previewErr := PreviewFilePatches(r.State.WorkingDir, patches, r.State.SystemAccess()); previewErr == nil {
			diff = preview
		}
	}

	tc := &session.PendingToolCall{
		ID:           fmt.Sprintf("call_%d", r.Now().UnixNano()),
		Name:         toolName,
		Args:         string(args),
		Command:      command,
		Risk:         string(tool.Risk),
		Reason:       reason,
		Diff:         diff,
		Schema:       tool.Description,
		StepID:       r.curStep,
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	key, regErr := r.State.RegisterApproval(ctx, tc)
	if regErr != nil {
		// Registration is refused when the slot is occupied or the operation
		// was already interrupted. The first is a programming error (approvals
		// serialize on approvalMu); the second is steering, and must not be
		// reported as a user denial.
		if steeringStopped(ctx, regErr) || errors.Is(regErr, context.Canceled) || session.SteeringRequested(ctx) {
			return false, "", session.ErrSendNow
		}
		return false, "", regErr
	}
	defer r.State.ClearPrompt(key)

	label := fmt.Sprintf("waiting for approval: %s", command)
	r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	select {
	case decision := <-tc.ResponseChan:
		if decision.Canceled {
			// The session settled this request as interrupted, not answered.
			// The distinct cause is on the registry record, so the runner
			// reports steering rather than inventing a denial.
			return false, "", session.ErrSendNow
		}
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		// Settle by key: this also waits for a response claim that already
		// won, so an approved decision is never rolled back and the runner
		// returns only after that settle finished. The cause distinguishes
		// steering from a plain turn cancel or a shutdown.
		r.State.CancelPrompt(key, session.SteeringTurnContext(ctx).Err())
		switch {
		case errors.Is(r.State.PromptCause(key), session.ErrSendNow):
			return false, "", session.ErrSendNow
		case r.State.PromptCause(key) == nil:
			// An answer claim won just before this cancellation and still owns
			// its decision: honor it rather than discarding a grant the session
			// already acknowledged.
			select {
			case decision := <-tc.ResponseChan:
				return decision.Approved, decision.Edited, nil
			default:
			}
		}
		return false, "", ctx.Err()
	}
}

func (r *Runner) requestAnswer(ctx context.Context, question string) (string, error) {
	answers, err := r.requestQuestions(ctx, []session.Question{{Question: question}})
	if err != nil {
		return "", err
	}
	if len(answers) == 0 {
		return "", nil
	}
	return answers[0].Answer, nil
}

// requestQuestions blocks on the TUI for one or more structured Answers.
// It produces the same shape the native question.ask tool produces.
//
// Neither questions nor approvals carry a wall-clock timeout: a user
// reading and answering a prompt is not a hung request, and failing the
// turn after a few minutes punishes them for thinking. The wait ends on a
// decision, on ctx cancellation (turn cancel/shutdown), and on
// State.ResolvePendingForShutdown, which answers every pending question
// with Unanswered. Registration is canonical (Task 3) so a canceled question
// settles through the registry with its distinct cause instead of being
// mistaken for a human answer.
func (r *Runner) requestQuestions(ctx context.Context, questions []session.Question) ([]session.Answer, error) {
	q := &session.PendingQuestion{
		Questions:    questions,
		ResponseChan: make(chan []session.Answer, 1),
	}
	key, regErr := r.State.RegisterQuestion(ctx, q)
	if regErr != nil {
		if steeringStopped(ctx, regErr) || errors.Is(regErr, context.Canceled) || session.SteeringRequested(ctx) {
			return nil, session.ErrSendNow
		}
		return nil, regErr
	}
	defer r.State.ClearPrompt(key)

	label := buildQuestionLabel(questions)
	r.State.SetActivity(session.Activity{Kind: session.ActivityQuestion, Label: label, StartedAt: r.Now()})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	select {
	case answers := <-q.ResponseChan:
		if cause := r.State.PromptCause(key); cause != nil {
			// A canceled question delivers the Unanswered compatibility
			// sentinel on its channel; the registry's cause is what
			// distinguishes that from a real decline, and the caller must see
			// the interruption, never a fabricated answer.
			return nil, cause
		}
		recordQuestionAnswers(r.State, answers)
		return answers, nil
	case <-ctx.Done():
		r.State.CancelPrompt(key, session.SteeringTurnContext(ctx).Err())
		if cause := r.State.PromptCause(key); cause != nil {
			return nil, cause
		}
		return nil, ctx.Err()
	}
}

// buildQuestionLabel returns a human-readable activity label that includes a
// preview of the first question so the user knows what they are being asked.
func buildQuestionLabel(questions []session.Question) string {
	if len(questions) == 0 {
		return "waiting for your answer"
	}
	q := strutil.Truncate(questions[0].Question, 40, true)
	if len(questions) == 1 {
		return "waiting for your answer: " + q
	}
	return fmt.Sprintf("waiting for your answer (Q1/%d): %s", len(questions), q)
}

// recordQuestionAnswers writes the permanent transcript record of a
// question round-trip. The popup owns the question text while asking
// (internal/app/tui/question.go), so the transcript keeps only this
// compact Q&A entry — written once here for every dispatch path (native
// question.ask, native ask_user, JSON envelope). Declined and unanswered
// questions are omitted; nothing is recorded when every question went
// unanswered.
func recordQuestionAnswers(state *session.State, answers []session.Answer) {
	var lines []string
	for _, a := range answers {
		if a.Answer == "" || a.Answer == session.AnswerUnanswered {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %q: %q", a.Question, a.Answer))
	}
	if len(lines) > 0 {
		state.AddMessage(session.RoleUser, strings.Join(lines, "\n"), session.ContentTypePlain)
	}
}

// requestSkillGate blocks until the TUI (or the ACP permission bridge)
// resolves the pending skill-gate prompt, or ctx is cancelled. Mirrors
// requestApproval's protocol with the dedicated PendingSkillGate slot, and
// registers canonically (Task 3) so a canceled gate settles by key: the
// returned ERROR carries the distinct interruption cause rather than a
// fabricated SkillGateDeny, and clearing is compare-and-clear on that key.
// There is no wall-clock timeout; the wait ends on a choice, ctx cancellation
// (turn cancel/shutdown), or State.ResolvePendingForShutdown (which answers
// with SkillGateDeny).
func (r *Runner) requestSkillGate(ctx context.Context, name string) (session.SkillGateChoice, error) {
	description := ""
	if r.SkillIndex != nil {
		if skill, ok := r.SkillIndex.Load(name); ok {
			description = skill.Description
		}
	}
	_, window := r.State.TurnUsage()
	windowLabel := "unknown"
	if window > 0 {
		windowLabel = "≤ " + strutil.CompactTokens(window) + " tokens"
	}
	sg := &session.PendingSkillGate{
		Skill:        name,
		Description:  description,
		Reason:       fmt.Sprintf("Context window is %s; loading skills consumes context. Allow this load?", windowLabel),
		ResponseChan: make(chan session.SkillGateChoice, 1),
	}
	key, regErr := r.State.RegisterSkillGate(ctx, sg)
	if regErr != nil {
		if steeringStopped(ctx, regErr) || errors.Is(regErr, context.Canceled) || session.SteeringRequested(ctx) {
			return session.SkillGateDeny, session.ErrSendNow
		}
		return session.SkillGateDeny, regErr
	}
	defer r.State.ClearPrompt(key)

	label := fmt.Sprintf("waiting for skill gate decision: %s", name)
	r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})
	defer r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})

	select {
	case choice := <-sg.ResponseChan:
		if cause := r.State.PromptCause(key); errors.Is(cause, session.ErrSendNow) {
			// Canceled, not denied: a cancellation must not be recorded as a
			// sticky skill denial, so the distinct cause travels out as the
			// error and the caller skips its deny bookkeeping.
			return session.SkillGateDeny, session.ErrSendNow
		}
		return choice, nil
	case <-ctx.Done():
		r.State.CancelPrompt(key, session.SteeringTurnContext(ctx).Err())
		if cause := r.State.PromptCause(key); errors.Is(cause, session.ErrSendNow) {
			return session.SkillGateDeny, session.ErrSendNow
		}
		return session.SkillGateDeny, ctx.Err()
	}
}
