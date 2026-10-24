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

// JobState is a background job's card: the shell.run row that started it,
// settled in place by the job's exit.
type JobState struct {
	ID, Command string
	Running     bool
	// Lost: the session ended while the job ran, so its outcome is unknown.
	// A lost job is not running and has no exit code.
	Lost     bool
	ExitCode int
	// Exit is the observed exit code when the process produced one. Nil means
	// no exit was observed — a lost job, a start-up failure, or a legacy
	// record — and must never be read as success. ExitCode keeps the legacy
	// field's -1-as-unknown convention for clients that predate it.
	Exit *int
	// Status is the execution's lifecycle status ("running", "stopping",
	// "completed", "failed", "killed", "timed_out"); Attachment is how the
	// launching caller related to it ("background" for a card). Both are empty
	// on a legacy record, which the renderer must present as unknown rather
	// than as success.
	Status     string
	Attachment string
	// StopCause is an accepted explicit stop request's cause, recorded
	// separately from the outcome it produced.
	StopCause string
	// Truncated reports whether the job's retained output hit its cap.
	Truncated bool
	Duration  time.Duration
	// Output is the job's output tail (the session caps it), scrubbed when
	// the card settles; renderers scrub again at their own boundary.
	Output string
}

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
	exits   map[NodeID]*session.JobExit // start row's ID → the exit that settles it
	exitRef map[*session.JobExit]NodeID // exit → the start row it settles
	starts  map[NodeID]registry.AuditEvent
	done    []doneEvent // completion events, by time
}

// doneEvent is one completion event and the moment it happened.
type doneEvent struct {
	at   time.Time
	node *Node
}

// indexCards pairs each job exit with the start it settles.
//
// Structured records pair by execution identity first, so it does not matter
// which order they reached the transcript in: a completion can be written before
// the audit row of the call that started it, and display IDs repeat across
// resumes (job-1 in every process). Only the records without an identity fall
// back to the legacy rule — an exit settles the latest start of its display ID
// before it, and each start at most once.
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

// settleJob attaches a job card to a shell.run row that started one. A row
// whose call started no card — a foreground command, even one whose audit
// carries structured execution metadata — is left alone: its own tool row is
// the whole record of it.
func (ci cardIndex) settleJob(row *Node) {
	if row.Kind != KindTool || len(row.Tools) != 1 {
		return
	}
	js, ok := JobStartOf(row.Tools[0])
	if !ok {
		return
	}
	card := &JobState{
		ID:      js.DisplayID,
		Command: ScrubText(ToolTarget(row.Tools[0])),
		Running: true,
	}
	// The start's own metadata describes the execution as it was admitted: the
	// attachment it ended up with, its status and its identity. A card that
	// never settles (a lost job) still shows them. The same record carries the
	// stable work key the pinned surface pairs this dispatch row by.
	if js.Structured {
		ex := js.Execution
		card.Attachment = string(ex.Attachment)
		card.Status, card.StopCause, card.Truncated = ex.State, ex.StopCause, ex.Truncated
		if ex.ExecutionID != "" {
			// A base-era record has an execution identity but no source epoch;
			// key it under the stable legacy epoch the coordinator uses, or the
			// row's key would never match the projected item's.
			epoch := ex.SourceEpoch
			if epoch == "" {
				epoch = legacyWorkEpoch
			}
			row.WorkKey = WorkKey(ex.ConversationID, epoch, workKindCommand, ex.ExecutionID)
		}
	}
	if e, ok := ci.exits[row.ID]; ok {
		card.Running, card.Lost, card.ExitCode, card.Duration = false, e.Lost, e.ExitCode, e.Duration
		card.Exit = e.Exit
		if e.State != "" {
			card.Status = e.State
		}
		if e.StopCause != "" {
			card.StopCause = e.StopCause
		}
		card.Truncated = card.Truncated || e.Truncated
		// Block, not Text: an expanded job card renders the output's own
		// line structure, so its newlines are content. The session bounds
		// the output, so this one scrub per build stays cheap.
		card.Output = ScrubBlock(e.Output)
	}
	row.Job = card
	row.Live = card.Running
	row.Version = versionOfJob(row.Version, card)
}

// versionOfJob hashes a card's rendered state onto its row. Every field the
// renderer reads is part of the hash: a status or truncation change that left
// the exit code alone must still invalidate a cached render.
func versionOfJob(rowVersion uint64, js *JobState) uint64 {
	b := newHash()
	b.f("job|%d|%v|%v|%d|%d|%d|", rowVersion, js.Running, js.Lost, js.ExitCode, js.Duration, len(js.Output))
	b.f("%s|%s|%s|%v|%v|", js.Status, js.Attachment, js.StopCause, js.Truncated, exitCodeOrUnknown(js.Exit))
	return b.sum()
}

// exitCodeOrUnknown renders an optional exit code for hashing, keeping "no exit
// observed" distinct from zero.
func exitCodeOrUnknown(code *int) string {
	if code == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *code)
}

// JobStart is when a job card's shell call was dispatched, or the zero time
// when the card carries no call. Build always pairs a job with the single call
// that started it, but a tree that has been rebuilt or filtered can hold a job
// whose runs are gone, and neither the band nor the TUI may index an empty
// slice for it: the card still renders, with its elapsed time unknown.
func (n *Node) JobStart() time.Time {
	if len(n.Tools) == 0 {
		return time.Time{}
	}
	return n.Tools[0].Timestamp
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
