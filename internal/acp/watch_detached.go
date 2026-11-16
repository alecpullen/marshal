package acp

import (
	"context"
	"sync"

	"marshal/internal/acp/service"
	"marshal/internal/acpwire"
	"marshal/internal/app"
	"marshal/internal/watch"
)

// marshalViewers counts each session's marshal viewers (decision 1): the
// connections on a viewers host that have a marshal client subscribed to the
// session's status. A session with one is on someone's screen, so a wake
// resumes nothing by itself — the screen does.
type marshalViewers struct {
	mu     sync.Mutex
	counts map[string]int
}

// newMarshalViewers returns an empty counter.
func newMarshalViewers() *marshalViewers {
	return &marshalViewers{counts: map[string]int{}}
}

// add counts one more viewer of sessionID and returns its release. The release
// is idempotent, so a connection's cleanup and its forwarder's own end may both
// call it, in either order; a nil counter counts nothing.
func (v *marshalViewers) add(sessionID string) (release func()) {
	if v == nil {
		return func() {}
	}
	v.mu.Lock()
	v.counts[sessionID]++
	v.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			v.mu.Lock()
			defer v.mu.Unlock()
			if n := v.counts[sessionID]; n > 1 {
				v.counts[sessionID] = n - 1
				return
			}
			delete(v.counts, sessionID)
		})
	}
}

// any reports whether sessionID has a marshal viewer.
func (v *marshalViewers) any(sessionID string) bool {
	if v == nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.counts[sessionID] > 0
}

// bindDetachedResume binds rt's watch-resume hook to a resume through svc
// (decisions 2, 3). A host that serves viewers has no connection of its own
// bound to the session's turns — a viewer only reads — so the wake is the
// session's own: the hook runs the screen's own call, svc.Prompt with the
// watch's name and whether it repeats, on a goroutine so the watch's goroutine
// never waits for a turn.
//
// It does nothing when the config gate is off, an SDD gate is pending, a run is
// in flight, or the session has a marshal viewer: the second of those is the
// screen's to answer, and the last means the screen resumes the watch itself,
// exactly as bindWatchResume leaves a marshal connection's hook alone.
//
// A non-marshal connection's later binding replaces it, as bindWatchResume
// already does: the cell is replace-on-write.
func bindDetachedResume(rt *app.Runtime, svc *service.Session, sessionID string, viewers *marshalViewers) {
	if rt == nil || rt.WatchResume == nil || svc == nil {
		return
	}
	rt.WatchResume.Set(func(r watch.Report) {
		if !r.Resume || rt.State == nil {
			return
		}
		if !rt.State.Config.Watch.ResumeEnabled {
			return
		}
		if rt.State.SDDGate().Question != "" {
			return
		}
		// A run in flight drains its own reports at the turn's loop top, and a
		// second run would be refused anyway.
		if svc.Status().Run != nil {
			return
		}
		// A marshal viewer is the session's screen: it takes the latch and
		// sends the prompt itself.
		if viewers.any(sessionID) {
			return
		}
		go func() {
			// The watch's goroutine must not wait for a turn: the prompt runs
			// the session's turn, on the session's own goroutine.
			_, _ = svc.Prompt(context.Background(), acpwire.PromptParams{
				WatchResume: &acpwire.WatchResume{Name: r.Name, Repeat: r.Mode == watch.ModeRepeat},
			})
		}()
	})
}
