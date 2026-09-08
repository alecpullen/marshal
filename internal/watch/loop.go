package watch

import (
	"context"
	"fmt"
	"time"
)

// sourceSampler dispatches to the real per-source sampler based on the
// watch's kind. It is the production sampler wired in NewManager.
type sourceSampler struct {
	deps Deps
}

func (s sourceSampler) Sample(ctx context.Context, w *watch) (Sample, error) {
	switch w.kind {
	case KindCommand:
		return s.sampleCommand(ctx, w)
	case KindFile:
		return s.sampleFile(ctx, w)
	case KindJob:
		return s.sampleJob(ctx, w)
	default:
		return Sample{}, fmt.Errorf("unknown watch kind %q", w.kind)
	}
}

// runWatch dispatches to the source-specific loop.
func (m *Manager) runWatch(w *watch) {
	defer m.wg.Done()
	if w.kind == KindJob {
		m.runJobWatch(w)
		return
	}
	m.runPollWatch(w)
}

// runPollWatch is the interval-poll loop for command/file sources. It samples
// on a ticker at the clamped interval.
func (m *Manager) runPollWatch(w *watch) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.managerCtx.Done():
			return
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			m.sampleOnce(w)
		}
	}
}

// runJobWatch is the event-driven loop for job sources. It does one
// synchronous state check at goroutine start (so a job already finished at
// registration fires deterministically), then subscribes to the job event
// stream and fires when the watched job reaches a terminal state.
func (m *Manager) runJobWatch(w *watch) {
	// Synchronous initial check: if the job is already terminal, fire
	// immediately and exit.
	if m.checkJobTerminal(w) {
		return
	}
	if m.deps.SubscribeJobs == nil {
		// No subscription configured; just wait for shutdown (mirrors the
		// task-1 behavior so a bare job watch is inert, not erroring).
		select {
		case <-m.managerCtx.Done():
		case <-w.ctx.Done():
		}
		return
	}
	ch, unsubscribe := m.deps.SubscribeJobs(w.ctx)
	if unsubscribe != nil {
		defer unsubscribe()
	}
	for {
		select {
		case <-m.managerCtx.Done():
			return
		case <-w.ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if m.checkJobTerminalFromEvent(w, ev) {
				return
			}
		}
	}
}
