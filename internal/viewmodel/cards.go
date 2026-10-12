package viewmodel

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// JobState is a background job's card: the shell.run row that started it,
// settled in place by the job's exit.
type JobState struct {
	ID, Command string
	Running     bool
	ExitCode    int
	Duration    time.Duration
	Output      string
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

const jobStartPrefix = "started background job "

// JobStartID returns the job ID a shell call started in the background, or ""
// when the call ran in the foreground.
func JobStartID(ev registry.AuditEvent) string {
	if !IsShellFamily(ev.ToolName) || !strings.HasPrefix(ev.ResultSummary, jobStartPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(ev.ResultSummary, jobStartPrefix))
}

// cardIndex is what Build learns about cards from the whole transcript
// before splitting it into turns: a card and its completion can sit in
// different turns.
type cardIndex struct {
	exits    map[string]*session.JobExit // by job ID
	jobCards map[string]NodeID           // job ID → the start row's ID
	markers  []block                     // completion markers, by time
}

func indexCards(items []session.TranscriptItem) cardIndex {
	ci := cardIndex{exits: map[string]*session.JobExit{}, jobCards: map[string]NodeID{}}
	for _, it := range items {
		if it.Audit != nil {
			if id := JobStartID(*it.Audit); id != "" {
				ci.jobCards[id] = ToolID(*it.Audit)
			}
		}
		if it.JobExit != nil {
			ci.exits[it.JobExit.ID] = it.JobExit
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
		case it.JobExit != nil:
			j := it.JobExit
			ref, ok := ci.jobCards[j.ID]
			if !ok {
				ref = NodeID{KindJobExit, "job:" + j.ID}
			}
			ci.markers = append(ci.markers, block{j.At, markerNode(MarkerInfo{
				Refs: []NodeID{ref}, Labels: []string{j.ID}, Failed: j.ExitCode != 0, At: j.At, Dur: j.Duration,
			})})
		}
	}
	sort.SliceStable(ci.markers, func(i, j int) bool { return ci.markers[i].ts.Before(ci.markers[j].ts) })
	return ci
}

func markerNode(m MarkerInfo) *Node {
	n := &Node{ID: NodeID{KindMarker, "marker:" + m.Refs[0].Key}, Kind: KindMarker, Marker: &m}
	n.Version = versionOfMarker(&m)
	return n
}

func versionOfMarker(m *MarkerInfo) uint64 {
	b := newHash()
	b.f("mk|%v|%d|%d|", m.Failed, m.At.UnixNano(), m.Dur)
	for i, r := range m.Refs {
		b.f("r|%s|%s|", r.Key, m.Labels[i])
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
	js := &JobState{ID: id, Command: ToolTarget(row.Tools[0]), Running: true}
	if e, ok := ci.exits[id]; ok {
		js.Running, js.ExitCode, js.Duration, js.Output = false, e.ExitCode, e.Duration, e.Output
	}
	row.Job = js
	row.Live = js.Running
	b := newHash()
	b.f("job|%d|%v|%d|%d|%d|", row.Version, js.Running, js.ExitCode, js.Duration, len(js.Output))
	row.Version = b.sum()
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
