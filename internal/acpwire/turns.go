package acpwire

// PromptParams is one prompt for a session's turn (C8).
type PromptParams struct {
	// Text is the user's message. P3 sends it as one text content block.
	Text string `json:"text"`
	// Queued says the text came off the steering queue. When the turn cannot
	// start, the session puts the text back on the queue.
	Queued bool `json:"queued,omitempty"`
}

// PromptResult is how a turn ended.
type PromptResult struct {
	StopReason string `json:"stopReason"`
}

// How a turn ended, as PromptResult reports it. They are ACP's stop reasons.
const (
	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"
)

// ReviewParams is one /review run: the reviewer's focus, an optional model,
// and what to review. At most one of Range and Base is set; with neither, the
// reviewer reviews the uncommitted changes.
type ReviewParams struct {
	Focus string `json:"focus,omitempty"`
	Model string `json:"model,omitempty"`
	Range string `json:"range,omitempty"` // an explicit git range
	Base  string `json:"base,omitempty"`  // a ref: review from its merge base with HEAD
}

// AgentRunParams runs one custom agent, by name, with a goal.
type AgentRunParams struct {
	Agent string `json:"agent"`
	Goal  string `json:"goal,omitempty"`
}

// VerifyCommands is a build and test command pair, as VerifyProposal proposes
// it. An empty command means the step does not apply.
type VerifyCommands struct {
	Build string `json:"build,omitempty"`
	Test  string `json:"test,omitempty"`
}
