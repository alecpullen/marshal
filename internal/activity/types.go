// Package activity defines the in-process ownership records used to group
// transcript narration and work. Phase 1 records are intentionally not
// persisted; restored messages therefore have unknown ownership.
package activity

// Ref identifies one run, provider response, narration, or dispatched call.
// Empty fields mean that the source has no known activity attribution.
// IDs are runtime identities and are not stable across process restarts.
type Ref struct {
	RunID       string
	ActorID     string
	ResponseID  string
	NarrationID string
	CallID      string
}

type Source string

const (
	SourceModelProse      Source = "model_prose"
	SourceRuntimeFallback Source = "runtime_fallback"
)

// Narration is the immutable public text accepted for a response. Sequence
// preserves append order within its State, independent of transcript sorting.
type Narration struct {
	ID                string
	RunID             string
	ActorID           string
	ResponseID        string
	BoundaryMessageID int64
	// SourceMessageID links the runtime annotation to the ordinary narration
	// message that carries its public text. It is an in-memory message ID.
	SourceMessageID int64
	Text            string
	Source          Source
	Sequence        uint64
	Closed          bool
}
