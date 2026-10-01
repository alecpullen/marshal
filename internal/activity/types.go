// Package activity defines the in-process ownership records used to group
// transcript narration and work. Phase 1 records are intentionally not
// persisted; restored messages therefore have unknown ownership.
package activity

import "context"

// Ref identifies one run, provider response, narration, or dispatched call.
// Empty fields mean that the source has no known activity attribution.
// IDs are runtime identities and are not stable across process restarts.
type Ref struct {
	RunID          string
	ActorID        string
	ResponseID     string
	NarrationID    string
	CallID         string
	ProviderCallID string
}

type contextKey struct{}

// WithRef passes an immutable activity owner through native tool handlers.
func WithRef(ctx context.Context, ref Ref) context.Context {
	return context.WithValue(ctx, contextKey{}, ref)
}

// FromContext returns the captured owner supplied by the runner, if any.
func FromContext(ctx context.Context) (Ref, bool) {
	ref, ok := ctx.Value(contextKey{}).(Ref)
	return ref, ok
}

type Source string

const (
	SourceModelProse         Source = "model_prose"
	SourceRuntimeFallback    Source = "runtime_fallback"
	SourceStructuredProgress Source = "structured_progress"
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
