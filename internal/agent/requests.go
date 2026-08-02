package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// requestApproval blocks until the TUI (or any caller driving
// session.PendingToolCall) resolves the pending approval, or ctx is
// cancelled. It follows the exact protocol internal/app/tui/model.go already
// implements for Milestone F/G: set PendingApproval, wait on ResponseChan,
// clear PendingApproval.
func (r *Runner) requestApproval(ctx context.Context, tool registry.Tool, toolName string, args json.RawMessage, argsMap map[string]interface{}, reason string) (approved bool, edited string, err error) {
	command, _ := argsMap["command"].(string)
	if command == "" {
		command = toolName
	}

	diff := ""
	if toolName == "file.write_patch" {
		if patchText, ok := argsMap["patch"].(string); ok {
			if preview, previewErr := PreviewPatchDiff(r.State.WorkingDir, patchText); previewErr == nil {
				diff = preview
			}
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
		ResponseChan: make(chan session.UserApprovalDecision, 1),
	}
	r.State.SetPendingApproval(tc)

	label := fmt.Sprintf("waiting for approval: %s", command)
	r.State.SetActivity(session.Activity{Kind: session.ActivityApproval, Label: label, StartedAt: r.Now()})

	timeout := r.effectiveApprovalTimeout()
	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ctx.Err()
	case <-time.After(timeout):
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return false, "", ErrRequestTimedOut
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
// Unlike requestApproval there is no wall-clock timeout here: a user
// reading and answering a question is not a hung request, and failing the
// turn after a few minutes punishes them for thinking. The wait still ends
// on ctx cancellation (turn cancel/shutdown) and on
// State.ResolvePendingForShutdown, which answers every pending question
// with Unanswered.
func (r *Runner) requestQuestions(ctx context.Context, questions []session.Question) ([]session.Answer, error) {
	for _, q := range questions {
		r.State.AddMessage(session.RoleAssistant, q.Question, session.ContentTypeMarkdown)
	}
	q := &session.PendingQuestion{
		Questions:    questions,
		ResponseChan: make(chan []session.Answer, 1),
	}
	r.State.SetPendingQuestion(q)
	label := buildQuestionLabel(questions)
	r.State.SetActivity(session.Activity{Kind: session.ActivityQuestion, Label: label, StartedAt: r.Now()})

	select {
	case answers := <-q.ResponseChan:
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return answers, nil
	case <-ctx.Done():
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return nil, ctx.Err()
	}
}

// effectiveApprovalTimeout returns the approval-wait timeout to use, falling
// back to a sensible default if r.ApprovalTimeout is zero.
func (r *Runner) effectiveApprovalTimeout() time.Duration {
	if r.ApprovalTimeout > 0 {
		return r.ApprovalTimeout
	}
	return defaultApprovalTimeout
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
