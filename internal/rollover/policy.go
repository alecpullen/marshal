// Package rollover provides pure policy decision logic for determining
// when a context rollover is due.
package rollover

const (
	// PolicyContextPercent fires when the percentage of the context window
	// consumed reaches the configured threshold.
	PolicyContextPercent = "context_percent"

	// PolicyTurnCount fires when the number of turns in the current
	// generation reaches the configured threshold.
	PolicyTurnCount = "turn_count"

	// PolicyCallerCheckpoint fires only when the caller explicitly
	// requests a checkpoint or when the turn-count backstop is reached.
	PolicyCallerCheckpoint = "caller_checkpoint"
)

// Policy describes the rollover decision policy.
type Policy struct {
	// Mode is one of PolicyContextPercent, PolicyTurnCount, or
	// PolicyCallerCheckpoint.
	Mode string

	// ContextPercent is the threshold (0–100) for the context_percent
	// policy. Ignored for other modes.
	ContextPercent int

	// TurnCount is the threshold for the turn_count policy and the
	// turn-count backstop for context_percent and caller_checkpoint
	// modes. Ignored for turn_count mode when zero.
	TurnCount int
}

// Signal carries the runtime state used by Decide.
type Signal struct {
	// TurnsInGeneration is the number of turns completed in the current
	// generation.
	TurnsInGeneration int

	// Tokens is the number of tokens consumed so far in the current
	// generation.
	Tokens int

	// ContextWindow is the total context window size in tokens. A value
	// of zero means the window size is unknown.
	ContextWindow int

	// Requested is true when the caller explicitly requests a checkpoint.
	Requested bool
}

// Decide evaluates the policy against the signal and returns true when a
// rollover is due.
//
// Decide is pure: it has no side effects, no I/O, no clock, and no database
// access. It must never guess a context window size; an unknown window
// (ContextWindow == 0) disables the percentage rule rather than inventing
// a denominator.
func Decide(p Policy, s Signal) bool {
	switch p.Mode {
	case PolicyContextPercent:
		// Context-percent rule: only fires when the window is known.
		percentFires := false
		if s.ContextWindow > 0 {
			percentFires = (s.Tokens * 100 / s.ContextWindow) >= p.ContextPercent
		}
		// Turn-count backstop: fires when TurnsInGeneration >= TurnCount.
		turnFires := p.TurnCount > 0 && s.TurnsInGeneration >= p.TurnCount
		return percentFires || turnFires

	case PolicyTurnCount:
		return p.TurnCount > 0 && s.TurnsInGeneration >= p.TurnCount

	case PolicyCallerCheckpoint:
		// Fires when the caller explicitly requests a checkpoint or
		// when the turn-count backstop is reached.
		turnFires := p.TurnCount > 0 && s.TurnsInGeneration >= p.TurnCount
		return s.Requested || turnFires

	default:
		// Unknown policy mode never fires.
		return false
	}
}
