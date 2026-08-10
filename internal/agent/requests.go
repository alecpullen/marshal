package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
	"marshal/internal/tools/registry"
)

// requestApproval blocks until the TUI (or any caller driving
// session.PendingToolCall) resolves the pending approval, or ctx is
// cancelled. It follows the exact protocol internal/app/tui/model.go already
// implements for Milestone F/G: set PendingApproval, wait on ResponseChan,
// clear PendingApproval. The wait ends only on a decision, ctx
// cancellation (turn cancel/shutdown), or State.ResolvePendingForShutdown —
// there is no wall-clock timeout.
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

	select {
	case decision := <-tc.ResponseChan:
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
		return decision.Approved, decision.Edited, nil
	case <-ctx.Done():
		r.State.SetPendingApproval(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
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
// with Unanswered.
func (r *Runner) requestQuestions(ctx context.Context, questions []session.Question) ([]session.Answer, error) {
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
		recordQuestionAnswers(r.State, answers)
		return answers, nil
	case <-ctx.Done():
		r.State.SetPendingQuestion(nil)
		r.State.SetActivity(session.Activity{Kind: session.ActivityIdle})
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
