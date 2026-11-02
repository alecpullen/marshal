package inproc

import (
	"context"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
	"marshal/internal/pubsub"
	"marshal/internal/repo/gitinfo"
	"marshal/internal/tools/native"
	"marshal/internal/viewmodel"
	"marshal/internal/watch"
)

// newSession builds one session over d.State and subscribes it to the
// runtime's brokers. It keeps d.GitInfo, or gitinfo.Read when the caller
// supplied none.
func newSession(d Deps) *Session {
	gitInfo := d.GitInfo
	if gitInfo == nil {
		gitInfo = gitinfo.Read
	}
	parent := d.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Session{
		state:         d.State,
		gitInfo:       gitInfo,
		projectID:     d.ProjectID,
		headSHA:       gitinfo.HeadSHA,
		refs:          d.ReferenceFinder,
		accessRequest: d.AccessRequest,
		resolveAccess: d.ResolveAccess,
		work:          d.Work,
		ctx:           ctx,
		cancel:        cancel,
		changed:       make(chan struct{}, 1),
		receipts:      map[string]session.SteeringReceipt{},
		localKeys:     map[any]string{},
	}
	s.subscribe(d)
	return s
}

// subscribe starts the session's event loop over whichever brokers the runtime
// supplied. A nil broker is not subscribed, and a session with none starts no
// goroutine.
func (s *Session) subscribe(d Deps) {
	var (
		jobs      <-chan pubsub.Event[native.JobEvent]
		watches   <-chan pubsub.Event[watch.Event]
		steering  <-chan pubsub.Event[session.SteeringEvent]
		workspace <-chan pubsub.Event[session.WorkspaceEvent]
		subagents <-chan pubsub.Event[session.SubagentEvent]
		work      <-chan pubsub.Event[viewmodel.WorkSnapshot]
	)
	if d.Jobs != nil {
		jobs = d.Jobs.Subscribe(s.ctx)
	}
	// The watch subscription is terminal: a dropped resume would lose a wake.
	if d.Watches != nil {
		watches = d.Watches.Subscribe(s.ctx, pubsub.WithTerminal[watch.Event]())
	}
	if d.Steering != nil {
		steering = d.Steering.Subscribe(s.ctx)
	}
	if d.Workspace != nil {
		workspace = d.Workspace.Subscribe(s.ctx)
	}
	if d.Subagents != nil {
		subagents = d.Subagents.Subscribe(s.ctx)
	}
	// The work subscription leads, and its snapshot seeds the session, so the
	// first Work read already has a value.
	if d.Work != nil {
		work = d.Work.SubscribeWork(s.ctx)
		s.seedWork(d.Work.WorkSnapshot())
	}
	if jobs == nil && watches == nil && steering == nil && workspace == nil && subagents == nil && work == nil {
		return
	}
	go s.run(jobs, watches, steering, workspace, subagents, work)
}

// run signals Changed after each event from any broker. A channel that closes
// is dropped; the loop ends when the context is done or every channel it held
// has closed, and it closes Changed on the way out.
func (s *Session) run(
	jobs <-chan pubsub.Event[native.JobEvent],
	watches <-chan pubsub.Event[watch.Event],
	steering <-chan pubsub.Event[session.SteeringEvent],
	workspace <-chan pubsub.Event[session.WorkspaceEvent],
	subagents <-chan pubsub.Event[session.SubagentEvent],
	work <-chan pubsub.Event[viewmodel.WorkSnapshot],
) {
	defer s.closeSignal()
	for jobs != nil || watches != nil || steering != nil || workspace != nil || subagents != nil || work != nil {
		select {
		case <-s.ctx.Done():
			return
		case _, ok := <-jobs:
			if !ok {
				jobs = nil
				continue
			}
			s.signal()
		case ev, ok := <-watches:
			if !ok {
				watches = nil
				continue
			}
			if ev.Payload.Resume {
				s.noteResume(ev.Payload.Name, ev.Payload.Mode == watch.ModeRepeat)
			}
			s.signal()
		case ev, ok := <-steering:
			if !ok {
				steering = nil
				continue
			}
			if ev.Payload.Receipt != nil {
				s.absorbReceipt(*ev.Payload.Receipt)
			}
			s.signal()
		case _, ok := <-workspace:
			if !ok {
				workspace = nil
				continue
			}
			s.signal()
		case _, ok := <-subagents:
			if !ok {
				subagents = nil
				continue
			}
			s.signal()
		case ev, ok := <-work:
			if !ok {
				work = nil
				continue
			}
			if s.absorbWork(ev.Payload) {
				s.signal()
			}
		}
	}
}

// Changed fires after anything Status or Work returns may have changed.
func (s *Session) Changed() <-chan struct{} { return s.changed }

// signal wakes a reader without blocking. A closed channel takes no send.
func (s *Session) signal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changedClosed {
		return
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// closeSignal closes Changed once.
func (s *Session) closeSignal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changedClosed {
		return
	}
	s.changedClosed = true
	close(s.changed)
}

// close marks the session closed, releases its subscriptions and closes
// Changed.
func (s *Session) close() {
	s.closed.Store(true)
	s.cancel()
	s.closeSignal()
}

// absorbReceipt keeps r unless the stored receipt for its request ID has a
// version at least as high.
func (s *Session) absorbReceipt(r session.SteeringReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.receipts[r.RequestID]; ok && prev.Version >= r.Version {
		return
	}
	s.receipts[r.RequestID] = r
}

// latestReceipt returns the stored receipt with the highest version among those
// for turnID, or among all of them when turnID is empty.
//
// When turnID names a turn, receipts from earlier turns are dropped on the way
// past: a session never needs them again, and keeping them would grow the map
// for the session's whole life and make every Status scan all of it. The map is
// therefore bounded by the current turn's requests, not by the session's.
func (s *Session) latestReceipt(turnID string) (session.SteeringReceipt, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best session.SteeringReceipt
	found := false
	for id, r := range s.receipts {
		if turnID != "" && r.TurnID != turnID {
			delete(s.receipts, id)
			continue
		}
		if !found || r.Version > best.Version {
			best, found = r, true
		}
	}
	return best, found
}

// noteResume replaces the pending resume slot and increments its sequence.
func (s *Session) noteResume(name string, repeat bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeSeq++
	s.resume = &acpwire.WatchResume{Name: name, Repeat: repeat, Seq: s.resumeSeq}
}

// watchResume returns a copy of the pending resume slot, or nil.
func (s *Session) watchResume() *acpwire.WatchResume {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resume == nil {
		return nil
	}
	cp := *s.resume
	return &cp
}
