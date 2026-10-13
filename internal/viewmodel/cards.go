package viewmodel

import (
	"fmt"
	"sort"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// JobState is a background job's card: the shell.run row that started it,
// settled in place by the job's exit.
type JobState struct {
	ID, Command string
	Running     bool
	// Lost: the session ended while the job ran, so its outcome is unknown.
	// A lost job is not running and has no exit code.
	Lost     bool
	ExitCode int
	Duration time.Duration
	// Output is the job's output tail (the session caps it), scrubbed when
	// the card settles; renderers scrub again at their own boundary.
	Output string
}

// MarkerInfo is a one-line completion marker at the moment cards finished.
// The cards themselves stay where they were dispatched.
type MarkerInfo struct {
	Refs   []NodeID // the cards this marker reports on, in order
	Labels []string // "#3 Pass 1 Task 1", "job-6"
	Failed bool     // any ref failed
	At     time.Time
	Dur    time.Duration // the single ref's run time; 0 when merged
}

// JobStartID returns the job ID a shell call started in the background, or ""
// when the call ran in the foreground.
func JobStartID(ev registry.AuditEvent) string {
	if !IsShellFamily(ev.ToolName) {
		return ""
	}
	return session.JobStartedID(ev.ResultSummary)
}

// cardIndex is what Build learns about cards from the whole transcript
// before splitting it into turns: a card and its completion can sit in
// different turns.
type cardIndex struct {
	exits   map[NodeID]*session.JobExit // start row's ID → the exit that settles it
	exitRef map[*session.JobExit]NodeID // exit → the start row it settles
	markers []block                     // completion markers, by time
}

// indexCards pairs each job exit with the start it settles. Job IDs restart
// at job-1 in every process, so an ID alone is ambiguous across a resume: an
// exit settles the latest start of its ID before it, and each start at most
// once.
func indexCards(items []session.TranscriptItem) cardIndex {
	ci := cardIndex{exits: map[NodeID]*session.JobExit{}, exitRef: map[*session.JobExit]NodeID{}}
	pending := map[string]NodeID{} // job ID → its latest unsettled start
	for _, it := range items {
		if it.Audit != nil {
			if id := JobStartID(*it.Audit); id != "" {
				pending[id] = ToolID(*it.Audit)
			}
		}
		if e := it.JobExit; e != nil {
			if ref, ok := pending[e.ID]; ok {
				ci.exits[ref] = e
				ci.exitRef[e] = ref
				delete(pending, e.ID)
			}
		}
	}
	for _, it := range items {
		switch {
		case it.Subagent != nil && it.Subagent.Status != session.SubagentRunning && !it.Subagent.EndedAt.IsZero():
			v := it.Subagent
			ci.markers = append(ci.markers, block{v.EndedAt, markerNode(MarkerInfo{
				Refs:   []NodeID{{KindSubagent, fmt.Sprintf("sub:%d", v.ID)}},
				Labels: []string{fmt.Sprintf("#%d %s", v.ID, v.Label)},
				Failed: v.Status == session.SubagentFailed,
				At:     v.EndedAt, Dur: v.EndedAt.Sub(v.StartedAt),
			})})
		case it.JobExit != nil && !it.JobExit.Lost:
			// A lost job has no finish to mark: its card says so instead.
			j := it.JobExit
			ref, ok := ci.exitRef[j]
			if !ok {
				// No start to point at (it sits on a rewound branch). The
				// time keeps two such exits of a reused ID apart.
				ref = NodeID{KindJobExit, fmt.Sprintf("job:%s@%d", j.ID, j.At.UnixNano())}
			}
			ci.markers = append(ci.markers, block{j.At, markerNode(MarkerInfo{
				Refs: []NodeID{ref}, Labels: []string{j.ID}, Failed: j.ExitCode != 0, At: j.At, Dur: j.Duration,
			})})
		}
	}
	sort.SliceStable(ci.markers, func(i, j int) bool { return ci.markers[i].ts.Before(ci.markers[j].ts) })
	return ci
}

// markerNode builds a completion marker's node. A marker's text is
// model-authored — a subagent's label, a job's ID — so it is scrubbed here,
// at the one place every marker is constructed, rather than at each renderer.
func markerNode(m MarkerInfo) *Node {
	labels := make([]string, len(m.Labels))
	for i, l := range m.Labels {
		labels[i] = ScrubText(l)
	}
	m.Labels = labels
	n := &Node{ID: NodeID{KindMarker, "marker:" + markerKey(m)}, Kind: KindMarker, Marker: &m}
	n.Version = versionOfMarker(&m)
	return n
}

// markerKey is a marker's identity: the first card it reports on. A marker
// with no refs — which Build never produces — falls back to its timestamp, so
// it still has a distinct key rather than the empty key every such marker
// would otherwise share.
func markerKey(m MarkerInfo) string {
	if len(m.Refs) > 0 {
		return m.Refs[0].Key
	}
	return fmt.Sprintf("t%d", m.At.UnixNano())
}

func versionOfMarker(m *MarkerInfo) uint64 {
	b := newHash()
	b.f("mk|%v|%d|%d|", m.Failed, m.At.UnixNano(), m.Dur)
	for i, r := range m.Refs {
		label := ""
		if i < len(m.Labels) {
			label = m.Labels[i]
		}
		b.f("r|%s|%s|", r.Key, label)
	}
	return b.sum()
}

// settleJob attaches a job card to a shell.run row that started one.
func (ci cardIndex) settleJob(row *Node) {
	if row.Kind != KindTool || len(row.Tools) != 1 {
		return
	}
	id := JobStartID(row.Tools[0])
	if id == "" {
		return
	}
	js := &JobState{ID: id, Command: ScrubText(ToolTarget(row.Tools[0])), Running: true}
	if e, ok := ci.exits[row.ID]; ok {
		js.Running, js.Lost, js.ExitCode, js.Duration = false, e.Lost, e.ExitCode, e.Duration
		// Block, not Text: an expanded job card renders the output's own
		// line structure, so its newlines are content. The session bounds
		// the output, so this one scrub per build stays cheap.
		js.Output = ScrubBlock(e.Output)
	}
	row.Job = js
	row.Live = js.Running
	b := newHash()
	b.f("job|%d|%v|%v|%d|%d|%d|", row.Version, js.Running, js.Lost, js.ExitCode, js.Duration, len(js.Output))
	row.Version = b.sum()
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

// markersIn returns the markers whose time falls in [from, to). A zero from
// is unbounded below (the preamble turn); a zero to is unbounded above (the
// last turn).
func (ci cardIndex) markersIn(from, to time.Time) []block {
	var out []block
	for _, m := range ci.markers {
		if (from.IsZero() || !m.ts.Before(from)) && (to.IsZero() || m.ts.Before(to)) {
			out = append(out, m)
		}
	}
	return out
}

// coalesceMarkers merges markers with nothing between them into one.
func coalesceMarkers(nodes []*Node) []*Node {
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		if k := len(out); k > 0 && n.Kind == KindMarker && out[k-1].Kind == KindMarker {
			prev := out[k-1].Marker
			merged := MarkerInfo{
				Refs:   append(append([]NodeID{}, prev.Refs...), n.Marker.Refs...),
				Labels: append(append([]string{}, prev.Labels...), n.Marker.Labels...),
				Failed: prev.Failed || n.Marker.Failed,
				At:     n.Marker.At,
			}
			out[k-1] = markerNode(merged)
			continue
		}
		out = append(out, n)
	}
	return out
}
