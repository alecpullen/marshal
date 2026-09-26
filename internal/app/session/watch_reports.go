package session

// Watch reports are machine-generated completion messages for
// background watch children. They are queued separately from the
// human steering queue so that ClearSteering (turn-cancel, Ctrl+X) and
// PopSteering (blank-Enter follow-up) never drop a background child's
// report. The runner drains them at loop-top alongside steering and
// injects them into the model wire as user messages.
//
// The queue is coalesced by watch ID: a new fire for a watch that already
// has a pending (un-drained) report replaces that entry rather than
// appending, so each drain delivers at most one message per watch. The
// push site formats the report with the watch's cumulative FiredCount, so
// the folded entry carries a "(fired N times)" note. Persistence happens at
// drain time (the runner persists each drained report as a RoleUser
// message), which bounds the persisted transcript the same way the queue
// is bounded — a watch that fires repeatedly while idle produces one
// persisted message per drain, not one per fire.

// watchReportEntry is one pending report, keyed by watch ID so a later
// fire for the same watch can replace it in place. name/repeat/resume are
// the watch facts the resume pipeline needs (the runner's turn-end
// residual drain arms the resume latch from them); they are primitives,
// not a watch.Report, because the session package cannot import watch
// (watch imports native which imports session).
type watchReportEntry struct {
	id     string
	text   string
	name   string
	repeat bool
	resume bool
}

// DrainedWatchReport is one drained queue entry: the formatted text the
// model wire and the transcript receive, plus the watch facts the resume
// pipeline needs.
type DrainedWatchReport struct {
	Text   string
	Name   string
	Repeat bool
	Resume bool
}

// PushWatchReport records a background watch child's completion report,
// coalescing by watch ID. If a report for the same watch is already
// pending (not yet drained), it is replaced in place with the new text
// (which the push site has formatted with the cumulative FiredCount);
// otherwise the report is appended. name/repeat/resume are the resume
// pipeline's facts — they overwrite the folded entry's fields with the
// latest fire's values. It is drained by the runner at the next loop-top
// (or the next turn's start) and injected into the model context, so a
// report that arrives after the parent turn has ended still reaches the
// model.
func (s *State) PushWatchReport(id string, text string, name string, repeat bool, resume bool) {
	s.mu.Lock()
	for i := range s.watchReports {
		if s.watchReports[i].id == id {
			s.watchReports[i].text = text
			s.watchReports[i].name = name
			s.watchReports[i].repeat = repeat
			s.watchReports[i].resume = resume
			s.mu.Unlock()
			return
		}
	}
	s.watchReports = append(s.watchReports, watchReportEntry{id: id, text: text, name: name, repeat: repeat, resume: resume})
	s.mu.Unlock()
}

// DrainWatchReports returns and clears the watch report queue atomically,
// delivering each pending report in push order. The runner calls this at
// every loop-top, alongside DrainSteering, to inject completed background
// watch child reports into the live model context, and persists each
// drained report's Text as a RoleUser message (the durable copy that
// buildHistoryMessages replays across restart). The resume fields arm the
// turn-end residual path's auto-resume latch.
func (s *State) DrainWatchReports() []DrainedWatchReport {
	s.mu.Lock()
	out := make([]DrainedWatchReport, 0, len(s.watchReports))
	for _, e := range s.watchReports {
		out = append(out, DrainedWatchReport{Text: e.text, Name: e.name, Repeat: e.repeat, Resume: e.resume})
	}
	s.watchReports = nil
	s.mu.Unlock()
	return out
}

// WatchReports returns a copy of the current report queue's formatted
// texts (does not drain). Used by tests and by the TUI to surface pending
// reports.
func (s *State) WatchReports() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.watchReports))
	for _, e := range s.watchReports {
		out = append(out, e.text)
	}
	return out
}

// ClearWatchReports discards the watch report queue without delivering it.
// The runner calls this at turn end so that a report pushed after the
// final loop-top drain (a child that finished late) is not double-
// delivered in the next turn. Because persistence now happens at drain
// time, a report discarded here is also not persisted — this is the
// accepted durability trade-off of coalescing (see the package comment).
func (s *State) ClearWatchReports() {
	s.mu.Lock()
	s.watchReports = nil
	s.mu.Unlock()
}
