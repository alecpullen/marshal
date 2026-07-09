package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"marshal/internal/pubsub"
	"marshal/internal/tools/native"
)

// pumpJobEvents subscribes to the job broker and returns a tea.Cmd that
// blocks until the next JobEvent arrives (or ctx is cancelled), then
// returns a jobCountMsg. The model calls this repeatedly from Update to
// keep the pump alive — one in-flight subscription at a time, not one
// goroutine per event (F19 R2: "one goroutine per program, not per event
// type"). The *tea.Program is not retained by the app, so background
// goroutines cannot call program.Send; this tea.Cmd is the bridge.
func pumpJobEvents(ctx context.Context, b *pubsub.Broker[native.JobEvent]) tea.Cmd {
	ch := b.Subscribe(ctx)
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil // broker closed / ctx cancelled → pump stops
		}
		return jobCountMsg{count: ev.Payload.Count}
	}
}
