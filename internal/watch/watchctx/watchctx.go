// Package watchctx holds the subagent owner-context helpers used to stamp
// watch.start calls made from a subagent. It is a leaf package (imports only
// the standard library) so that internal/agent can use it without pulling in
// internal/watch's dependency on internal/tools/native, which would create a
// test-only import cycle (native's test package imports internal/agent).
package watchctx

import "context"

// ownerCtxKey is the context key carrying the subagent owner tag for
// watch.start calls made from a subagent. The agent.run handler sets it on
// the child's context after registration; the watch.start handler reads it
// at call time to stamp Spec.Owner.
type ownerCtxKey struct{}

// WithOwner returns a context carrying the subagent owner tag. Set by the
// agent.run handler on the child's context so watch.start calls made by a
// subagent are attributed to it.
func WithOwner(ctx context.Context, owner string) context.Context {
	return context.WithValue(ctx, ownerCtxKey{}, owner)
}

// OwnerFromContext returns the subagent owner tag carried in ctx, or "".
func OwnerFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ownerCtxKey{}).(string); ok {
		return v
	}
	return ""
}
