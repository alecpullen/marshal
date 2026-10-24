package viewmodel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// legacyWorkEpoch is the stable epoch a command record with no source epoch of
// its own is keyed under, matching the coordinator's projection. The live
// runtime epoch is minted per process, so a base-era record keyed under it would
// change its canonical work key on every restart.
const legacyWorkEpoch = "legacy"

// JobStartOf reports the managed execution a shell-family call started, or
// ok=false when the call did not start a card. A foreground command — even one
// whose audit carries structured execution metadata — has its own tool row and
// must not become a background card.
func JobStartOf(ev registry.AuditEvent) (session.JobStart, bool) {
	if !IsShellFamily(ev.ToolName) {
		return session.JobStart{}, false
	}
	return session.JobStarted(ev)
}

// StartedSubagent is the subagent an agent.run call started in the
// background, 0 when it started none. The notice's structured reference is
// read first; a record that predates it falls back to the summary the tool
// wrote ("started as subagent 3 — …").
func StartedSubagent(ev registry.AuditEvent) int64 {
	if ev.ToolName != "agent.run" || ev.Error != "" {
		return 0
	}
	if ref, ok := ev.Notice.SubagentWork(); ok {
		return ref.AgentID
	}
	var id int64
	if _, err := fmt.Sscanf(ev.ResultSummary, "started as subagent %d", &id); err == nil {
		return id
	}
	return 0
}

// JobStartID returns the job ID a shell call started in the background, or ""
// when the call ran in the foreground. It prefers the structured execution
// record and falls back to the legacy result summary, so a row cannot merge
// into a collapsed run just because its metadata replaced the summary text.
func JobStartID(ev registry.AuditEvent) string {
	js, ok := JobStartOf(ev)
	if !ok {
		return ""
	}
	return js.DisplayID
}

// cardIndex is what Build learns about cards from the whole transcript
// before splitting it into turns: a card and its completion can sit in
// different turns.
type cardIndex struct {
	exits   map[NodeID]*session.JobExit // start row's ID → the exit that pairs with it
	exitRef map[*session.JobExit]NodeID // exit → the start row it pairs with
	starts  map[NodeID]registry.AuditEvent
	done    []doneEvent // completion events, by time
}

// doneEvent is one completion event and the moment it happened.
type doneEvent struct {
	at   time.Time
	node *Node
}

// indexCards pairs each job exit with the start it belongs to, so the exit
// can become a completion event.
//
// Structured records pair by execution identity first, so it does not matter
// which order they reached the transcript in: a completion can be written before
// the audit row of the call that started it, and display IDs repeat across
// resumes (job-1 in every process). Only the records without an identity fall
// back to the legacy rule — an exit pairs with the latest start of its display
// ID before it, and each start at most once.
func indexCards(items []session.TranscriptItem) cardIndex {
	ci := cardIndex{exits: map[NodeID]*session.JobExit{}, exitRef: map[*session.JobExit]NodeID{},
		starts: map[NodeID]registry.AuditEvent{}}

	// Identity pass: every structured start, keyed by the identity its exit
	// carries, then every exit — a lost one included — matched against it. A
	// lost exit keeps the identity of the start it belongs to, so a card that
	// will never settle is still paired with its own start rather than with a
	// later start that reused the display ID.
	byIdentity := map[string]NodeID{}
	for _, it := range items {
		if it.Audit == nil {
			continue
		}
		js, ok := JobStartOf(*it.Audit)
		if !ok || !js.Structured || js.Identity == "" {
			continue
		}
		byIdentity["exec:"+js.Identity] = ToolID(*it.Audit)
		ci.starts[ToolID(*it.Audit)] = *it.Audit
	}
	for _, it := range items {
		e := it.JobExit
		if e == nil {
			continue
		}
		identity := e.Identity()
		if !strings.HasPrefix(identity, "exec:") {
			continue
		}
		if ref, ok := byIdentity[identity]; ok {
			ci.exits[ref] = e
			ci.exitRef[e] = ref
		}
	}

	// Legacy pass: order-based pairing by display ID for everything the
	// identity pass did not settle — a record written before structured
	// metadata existed has nothing else to pair on.
	pending := map[string]NodeID{} // job ID → its latest unsettled start
	for _, it := range items {
		if it.Audit != nil {
			if id := JobStartID(*it.Audit); id != "" {
				pending[id] = ToolID(*it.Audit)
				ci.starts[ToolID(*it.Audit)] = *it.Audit
			}
		}
		if e := it.JobExit; e != nil {
			if _, settled := ci.exitRef[e]; settled {
				// Already paired by identity; it settles no other start.
				continue
			}
			ref, ok := pending[e.ID]
			if !ok {
				continue
			}
			if _, settled := ci.exits[ref]; settled {
				// The start this display ID resolves to was paired by
				// identity, so this exit belongs to an earlier start of the
				// same display ID — or to one whose call is gone.
				continue
			}
			ci.exits[ref] = e
			ci.exitRef[e] = ref
			delete(pending, e.ID)
		}
	}
	// Find each subagent's dispatch row: the agent.run audit that started it.
	// A view pairs by its work key when it has one, else by the last dispatch
	// of its ID that is not after its end.
	var dispatches []registry.AuditEvent
	for _, it := range items {
		if it.Audit != nil && StartedSubagent(*it.Audit) > 0 {
			dispatches = append(dispatches, *it.Audit)
		}
	}
	dispatchRow := func(v *session.SubagentView) NodeID {
		if v.WorkKey != "" {
			for i := range dispatches {
				if dispatchWorkKey(&dispatches[i]) == v.WorkKey {
					return ToolID(dispatches[i])
				}
			}
		}
		var found NodeID
		for i := range dispatches {
			if StartedSubagent(dispatches[i]) != v.ID {
				continue
			}
			if !v.EndedAt.IsZero() && dispatches[i].Timestamp.After(v.EndedAt) {
				continue
			}
			found = ToolID(dispatches[i])
		}
		return found
	}

	for _, it := range items {
		switch {
		case it.Subagent != nil && it.Subagent.Status != session.SubagentRunning && !it.Subagent.EndedAt.IsZero():
			v := it.Subagent
			ci.done = append(ci.done, doneEvent{at: v.EndedAt,
				node: completionNode(subagentCompletion(v, dispatchRow(v)), fmt.Sprintf("sub:%d", v.ID))})
		case it.JobExit != nil:
			// Every exit gets an event, a lost one included: a job whose
			// session ended still finished, and its event follows the beat
			// that launched it.
			j := it.JobExit
			ref, ok := ci.exitRef[j]
			var start *registry.AuditEvent
			refKey := ""
			if ok {
				refKey = ref.Key
				ev := ci.starts[ref]
				start = &ev
			} else {
				// No start to point at (it sits on a rewound branch). The
				// time keeps two such exits of a reused ID apart.
				refKey = fmt.Sprintf("job:%s@%d", j.ID, j.At.UnixNano())
			}
			ci.done = append(ci.done, doneEvent{at: j.At, node: completionNode(jobCompletion(j, start), refKey)})
		}
	}
	sort.SliceStable(ci.done, func(i, j int) bool { return ci.done[i].at.Before(ci.done[j].at) })
	return ci
}

// commandWorkKey is the stable conversation-work identity a shell-family call
// dispatched, or "" when the call started no managed execution. It is the key
// the coordinator projects the execution under, so a retained dispatch row
// pairs with the pinned Work view by identity rather than by display text.
func commandWorkKey(ev registry.AuditEvent) string {
	js, ok := JobStartOf(ev)
	if !ok || !js.Structured || js.Execution.ExecutionID == "" {
		return ""
	}
	// A base-era record has an execution identity but no source epoch; key it
	// under the stable legacy epoch the coordinator uses, or the row's key
	// would never match the projected item's.
	epoch := js.Execution.SourceEpoch
	if epoch == "" {
		epoch = legacyWorkEpoch
	}
	return WorkKey(js.Execution.ConversationID, epoch, workKindCommand, js.Execution.ExecutionID)
}

// completionsIn returns the completion events whose time falls in [from, to).
// A zero from is unbounded below (the preamble turn); a zero to is unbounded
// above (the last turn).
func (ci cardIndex) completionsIn(from, to time.Time) []block {
	var out []block
	for _, d := range ci.done {
		if (from.IsZero() || !d.at.Before(from)) && (to.IsZero() || d.at.Before(to)) {
			out = append(out, block{d.at, d.node})
		}
	}
	return out
}
