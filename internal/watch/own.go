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
//
// Owner is read and mutated under the manager -> watch lock order so a
// concurrent fire reads a consistent owner. Each actual reparenting advances
// the source's membership revision; the reparenting wake is published as a
// copy after every lock is released.
func (m *Manager) TransferFromSubagent(owner string) {
	if owner == "" {
		return
	}

	type reparented struct {
		w    *watch
		info Info
	}
	var wakes []reparented
	var stopIDs []string

	m.mu.Lock()
	for _, w := range m.watches {
		w.mu.Lock()
		if w.owner != owner {
			w.mu.Unlock()
			continue
		}
		if w.mode == ModeRepeat {
			w.mu.Unlock()
			stopIDs = append(stopIDs, w.id)
			continue
		}
		w.owner = ""
		m.revision++
		info := w.snapshotLockedWith(m.epoch, m.revision)
		w.mu.Unlock()
		wakes = append(wakes, reparented{w: w, info: info})
	}
	m.mu.Unlock()

	// Publish the reparenting wakes (deep copies) with no lock held. A wake
	// is informational: it never fires a report or starts a turn.
	for _, r := range wakes {
		info := r.info
		m.publishEvent(r.w, StateWatching, "", "", info.Mode, false, &info)
	}
	for _, id := range stopIDs {
		m.Stop(id)
	}
}
