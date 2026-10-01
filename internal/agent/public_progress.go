package agent

import (
	"strings"

	"marshal/internal/activity"
	"marshal/internal/app/session"
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
