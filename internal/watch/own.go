package watch

import (
	"context"

	"marshal/internal/watch/watchctx"
)

// WithOwner returns a context carrying the subagent owner tag. Set by the
// agent.run handler on the child's context so watch.start calls made by a
// subagent are attributed to it. Re-exported from watchctx so existing
// callers (and watch's own tests) keep working.
func WithOwner(ctx context.Context, owner string) context.Context {
	return watchctx.WithOwner(ctx, owner)
}

// OwnerFromContext returns the subagent owner tag carried in ctx, or "".
// Re-exported from watchctx.
func OwnerFromContext(ctx context.Context) string {
	return watchctx.OwnerFromContext(ctx)
}

// TransferFromSubagent re-parents the owner's once watches to the parent
// (owner "") and stops its repeat watches. Called by the agent.run
// completion/kill path when a subagent ends so its once watches keep
// reporting to the parent queue and its repeat watches do not outlive it.
func (m *Manager) TransferFromSubagent(owner string) {
	if owner == "" {
		return
	}
	m.mu.Lock()
	var stopIDs []string
	var reparent []*watch
	for _, w := range m.watches {
		if w.owner != owner {
			continue
		}
		if w.mode == ModeRepeat {
			stopIDs = append(stopIDs, w.id)
		} else {
			reparent = append(reparent, w)
		}
	}
	m.mu.Unlock()
	for _, w := range reparent {
		w.mu.Lock()
		w.owner = ""
		w.mu.Unlock()
	}
	for _, id := range stopIDs {
		m.Stop(id)
	}
}
