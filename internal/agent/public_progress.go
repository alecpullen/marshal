package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/activity"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/native"
	"marshal/internal/tools/registry"
)

// decodeEnvelopeProgress deliberately treats progress as optional metadata:
// its failure is recorded separately and never changes action parsing.
func decodeEnvelopeProgress(raw []byte) (*activity.ProgressUpdate, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	update, err := activity.DecodeProgress(raw)
	if err != nil {
		return nil, boundedProgressDiagnostic(err.Error())
	}
	return &update, ""
}

func boundedProgressDiagnostic(message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

// applyParsedProgress applies one already-decoded update to this provider
// response. The returned bool means progress supplied the structured owner.
func applyParsedProgress(state *session.State, response activity.Ref, action ModelAction) (activity.Ref, string, bool) {
	if action.Progress == nil || state == nil {
		return response, action.ProgressDiagnostic, false
	}
	receipt, err := state.ApplyPublicProgress(response, *action.Progress)
	if err != nil {
		return response, boundedProgressDiagnostic(err.Error()), false
	}
	return receipt.Owner, boundedProgressDiagnostic(strings.Join(receipt.Warnings, "; ")), true
}

var (
	errProgressMustLead    = errors.New("progress.update must be the first and only progress call in a response")
	errProgressUnavailable = errors.New("progress.update capability is unavailable")
)

// preflightNativeProgress consumes progress.update before ordinary native
// execution. Only a leading call can be accepted. Work calls and metadata
// replies retain their original response indexes and provider call IDs.
func (r *Runner) preflightNativeProgress(ctx context.Context, calls []schema.ToolCall, response activity.Ref) (activity.Ref, []schema.ChatMessage, []schema.ToolCall, []int, bool) {
	owner := response
	ordered := make([]schema.ChatMessage, len(calls))
	answered := make([]bool, len(calls))
	work := make([]schema.ToolCall, 0, len(calls))
	workIndexes := make([]int, 0, len(calls))
	accepted := false
	for i, call := range calls {
		call.Name = normalizeNativeToolName(r.Registry, call.Name)
		if call.Name != "progress.update" {
			work = append(work, call)
			workIndexes = append(workIndexes, i)
			continue
		}
		var err error
		if i != 0 || accepted {
			err = errProgressMustLead
		} else if cancelErr := ctx.Err(); cancelErr != nil {
			err = cancelErr
		} else {
			tool, ok := r.Registry.Lookup("progress.update")
			if !ok {
				err = errProgressUnavailable
			} else if len(call.Args) > activity.MaxProgressBytes {
				err = fmt.Errorf("progress.update payload exceeds %d bytes", activity.MaxProgressBytes)
			} else if validateErr := registry.ValidateArgs(tool, call.Args); validateErr != nil {
				err = validateErr
			} else {
				receipt, applyErr := native.ApplyPublicProgress(r.State, activity.WithRef(ctx, response), json.RawMessage(call.Args))
				if applyErr != nil {
					err = applyErr
				} else {
					owner = receipt.Owner
					accepted = true
					content := "Progress updated."
					if warning := boundedProgressDiagnostic(strings.Join(receipt.Warnings, "; ")); warning != "" {
						content += " Warning: " + warning
					}
					ordered[i] = schema.ChatMessage{Role: schema.RoleTool, ToolCallID: call.ID, Content: content}
					answered[i] = true
				}
			}
		}
		if err != nil {
			ordered[i] = BuildNativeToolErrorMessage(call.Name, err.Error(), call.ID)
			answered[i] = true
		}
	}
	// Empty slots are filled from the normal executor's replies by RunTask.
	for i := range answered {
		if !answered[i] {
			ordered[i] = schema.ChatMessage{Role: schema.RoleSystem, Content: ""}
		}
	}
	return owner, ordered, work, workIndexes, accepted
}
