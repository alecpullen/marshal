package rpc

import (
	"context"

	"marshal/internal/acpwire"
)

// Prompt runs one turn with params.Text as the user's message. The text
// travels as one text block, with queued and watch resume alongside it.
func (s *Session) Prompt(ctx context.Context, params acpwire.PromptParams) (acpwire.PromptResult, error) {
	call := acpwire.PromptCallParams{
		SessionID:   s.id,
		Prompt:      []acpwire.TextBlock{{Type: "text", Text: params.Text}},
		Queued:      params.Queued,
		WatchResume: params.WatchResume,
	}
	var result acpwire.PromptResult
	if err := s.conn.Call(ctx, acpwire.MethodPrompt, call, &result); err != nil {
		return acpwire.PromptResult{}, err
	}
	return result, nil
}

// Stop stops the session's run with that cause.
func (s *Session) Stop(ctx context.Context, cause string) error {
	params := acpwire.StopParams{SessionID: s.id, Cause: cause}
	return s.conn.Call(ctx, acpwire.MethodStop, params, nil)
}

// Review runs a reviewer subagent over what params names.
func (s *Session) Review(ctx context.Context, params acpwire.ReviewParams) error {
	call := acpwire.ReviewCallParams{SessionID: s.id, Review: params}
	return s.conn.Call(ctx, acpwire.MethodReview, call, nil)
}

// RunAgent runs the named custom agent with the goal.
func (s *Session) RunAgent(ctx context.Context, params acpwire.AgentRunParams) error {
	call := acpwire.AgentRunCallParams{SessionID: s.id, Run: params}
	return s.conn.Call(ctx, acpwire.MethodAgentRun, call, nil)
}

// Suggest asks the session's model for the user's most likely next reply to
// lastMessage. "" means it has none.
func (s *Session) Suggest(ctx context.Context, lastMessage string) (string, error) {
	params := acpwire.SuggestParams{SessionID: s.id, LastMessage: lastMessage}
	var result acpwire.SuggestResult
	if err := s.conn.Call(ctx, acpwire.MethodSuggest, params, &result); err != nil {
		return "", err
	}
	return result.Text, nil
}

// VerifyProposal proposes the commands that build and test the repository.
func (s *Session) VerifyProposal(ctx context.Context) (acpwire.VerifyCommands, error) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.VerifyCommands
	if err := s.conn.Call(ctx, acpwire.MethodVerifyProposal, params, &result); err != nil {
		return acpwire.VerifyCommands{}, err
	}
	return result, nil
}

// Steer queues text for the turn to read at its next step: no delivery, so the
// agent queues it.
func (s *Session) Steer(ctx context.Context, text string) error {
	params := acpwire.SteerCallParams{SessionID: s.id, Text: text}
	return s.conn.Call(ctx, acpwire.MethodSteer, params, nil)
}

// SendNow sends req's text to the running turn at its safe boundary, and
// returns the receipt.
func (s *Session) SendNow(ctx context.Context, req acpwire.SendNowRequest) (acpwire.SteeringReceipt, error) {
	params := acpwire.SteerCallParams{
		SessionID: s.id,
		Text:      req.Text,
		Delivery:  acpwire.SteerDeliveryNow,
		TurnID:    req.TurnID,
		RequestID: req.RequestID,
	}
	var result acpwire.SteeringReceipt
	if err := s.conn.Call(ctx, acpwire.MethodSteer, params, &result); err != nil {
		return acpwire.SteeringReceipt{}, err
	}
	return result, nil
}

// PopSteering removes and returns the oldest queued steering text. It reports
// false when the queue is empty, and on a failed call, as a closed session
// answers in process.
func (s *Session) PopSteering(ctx context.Context) (string, bool) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.SteeringPopResult
	if err := s.conn.Call(ctx, acpwire.MethodSteeringPop, params, &result); err != nil {
		return "", false
	}
	return result.Text, result.OK
}

// ClearSteering drops the queued steering text.
func (s *Session) ClearSteering(ctx context.Context) error {
	params := acpwire.ControlParams{SessionID: s.id}
	return s.conn.Call(ctx, acpwire.MethodSteeringClear, params, nil)
}

// modeParams is session/set_mode's params. ACP names system access
// system_access, which acpwire's camelCase field tags cannot carry, so the call
// has its own struct. A nil field is omitted, leaving it alone.
type modeParams struct {
	SessionID    string  `json:"sessionId"`
	Mode         *string `json:"mode,omitempty"`
	Auto         *bool   `json:"auto,omitempty"`
	SystemAccess *bool   `json:"system_access,omitempty"`
}

// SetInteraction applies one interaction patch and returns the committed
// state.
func (s *Session) SetInteraction(ctx context.Context, patch acpwire.InteractionPatch) (acpwire.Interaction, error) {
	params := modeParams{
		SessionID:    s.id,
		Mode:         patch.Mode,
		Auto:         patch.Auto,
		SystemAccess: patch.System,
	}
	var result acpwire.Interaction
	if err := s.conn.Call(ctx, acpwire.MethodSetMode, params, &result); err != nil {
		return acpwire.Interaction{}, err
	}
	return result, nil
}

// SetEffort sets the session's effort override, or clears it for "", and
// returns when the change applies.
func (s *Session) SetEffort(ctx context.Context, level string) (string, error) {
	params := acpwire.EffortParams{SessionID: s.id, Effort: level}
	var result acpwire.EffortResult
	if err := s.conn.Call(ctx, acpwire.MethodSetEffort, params, &result); err != nil {
		return "", err
	}
	return result.AppliesFrom, nil
}

// SetSudoUnlock turns sudo's own timestamp grace on or off for the session.
func (s *Session) SetSudoUnlock(ctx context.Context, on bool) error {
	params := acpwire.SudoUnlockParams{SessionID: s.id, On: on}
	return s.conn.Call(ctx, acpwire.MethodSetSudoUnlock, params, nil)
}

// CancelSubagent asks the running subagent with that ID to stop. It reports
// false when no running subagent has the ID, and on a failed call.
func (s *Session) CancelSubagent(ctx context.Context, subagentID int64) bool {
	params := acpwire.SubagentCancelParams{SessionID: s.id, SubagentID: subagentID}
	var result acpwire.SubagentCancelResult
	if err := s.conn.Call(ctx, acpwire.MethodSubagentCancel, params, &result); err != nil {
		return false
	}
	return result.Cancelled
}

// ClearFinishedRun clears a finished run's progress and its event log.
func (s *Session) ClearFinishedRun(ctx context.Context) error {
	params := acpwire.ControlParams{SessionID: s.id}
	return s.conn.Call(ctx, acpwire.MethodSDDClear, params, nil)
}

// TakeWatchResume takes the watch-resume latch. ok is false when the latch is
// not set, and on a failed call.
func (s *Session) TakeWatchResume(ctx context.Context) (name string, repeat bool, ok bool) {
	params := acpwire.ControlParams{SessionID: s.id}
	var result acpwire.WatchResumeTakeResult
	if err := s.conn.Call(ctx, acpwire.MethodWatchResumeTake, params, &result); err != nil {
		return "", false, false
	}
	return result.Name, result.Repeat, result.OK
}
