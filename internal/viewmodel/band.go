package viewmodel

import (
	"fmt"
	"time"

	"marshal/internal/app/session"
)

// BandInfo is the pinned band's content: todo progress with its stale
// signal, and the running cards. The TUI and web both draw it from here, so
// the two can never show different numbers.
type BandInfo struct {
	Todos    bool // the turn is working from a list of MinTaskTodos or more
	Done     int
	Total    int
	Active   string // the in-progress item's content, else the first pending one
	Stale    bool
	StaleAge time.Duration
	// StaleSince is when the stale clock started (set while Stale), so a
	// client can tick the age on its own clock.
	StaleSince time.Time
	// StaleAt is when the band would turn stale with no further event: the
	// tag can appear on elapsed time alone, so a projector that refreshes
	// only on events must wake for it. Zero when no such moment is ahead.
	StaleAt time.Time
	Next    []string // waiting items after the active one, in list order
	// Tasks is the whole list with each item's share of the transcript. It
	// is not gated like Todos: the Tasks views show a short list, and one
	// left unfinished when the turn ended, which the band itself drops.
	Tasks    []BandTask
	Entities []BandEntity
}

// BandTask is one todo in the full list the Tasks views show.
type BandTask struct {
	ID, Content, Status    string
	StartedAt, CompletedAt time.Time
	TaskStat
}

// TaskStat is a todo's share of the transcript: the beats bound to it, their
// responses and finished working time. It is counted from the same beats the
// transcript draws under the todo's divider, so the views always agree.
type TaskStat struct {
	Steps int
	Work  time.Duration
	// First is the first beat bound to the todo, where a jump to it lands;
	// the zero NodeID when no beat has worked on it yet.
	First NodeID
}

// TaskStats sums the responses and working time bound to each todo.
func TaskStats(turns []*Node) map[string]TaskStat {
	stats := map[string]TaskStat{}
	for _, turn := range turns {
		for _, n := range turn.Children {
			if n.Kind != KindStep || n.Step == nil || n.Step.TodoID == "" {
				continue
			}
			st := stats[n.Step.TodoID]
			if st.First == (NodeID{}) {
				st.First = n.ID
			}
			st.Steps += max(len(n.Step.StepIDs), 1)
			if s, e := n.Step.Step.StartedAt, n.Step.Step.EndedAt; !s.IsZero() && e.After(s) {
				st.Work += e.Sub(s)
			}
			stats[n.Step.TodoID] = st
		}
	}
	return stats
}

// BandEntity is one running card the band lists.
type BandEntity struct {
	Ref     NodeID
	Label   string // "#3 Pass 1 Task 1" or "job-4"
	Detail  string // latest activity, or the command's first line
	Started time.Time
}

// TodoClockOf reads the todo clock from the transcript: the later of the last
// todo.write and the last user turn's start, plus the list owner's responses
// since. A list carried in from an earlier session therefore starts its clock
// with the turn, not with when it was written.
//
// The list owner is the agent that opened the current turn — the top-level
// runner, whatever its role. That runner counts exactly its own responses
// for the nudge (agent.noteResponse), so the band and the nudge count the
// same steps; a subagent or pipeline role sharing the session is not the
// owner, and its responses are not the list's progress.
func TodoClockOf(s Snapshot) session.TodoClock {
	var c session.TodoClock
	var turn int64
	for _, it := range s.Items {
		if it.Kind == session.KindMessage && session.IsUserTurnMessage(it.Message) {
			turn = it.Message.ID
			if it.Timestamp.After(c.LastWrite) {
				c.LastWrite = it.Timestamp
			}
		}
		if it.Audit != nil && it.Audit.ToolName == todoWriteTool && it.Audit.Error == "" && it.Timestamp.After(c.LastWrite) {
			c.LastWrite = it.Timestamp
		}
	}
	var owner session.Actor
	for _, st := range s.Steps {
		if st.TurnMsgID == turn {
			owner = st.Actor
			break
		}
	}
	for _, st := range s.Steps {
		if st.Actor.Role == owner.Role && st.Actor.Label == owner.Label && st.StartedAt.After(c.LastWrite) {
			c.Responses++
		}
	}
	return c
}

// BandOf builds the band for the latest turn.
func BandOf(s Snapshot, turns []*Node) BandInfo {
	var b BandInfo
	if len(s.Todos) > 0 {
		stats := TaskStats(turns)
		b.Tasks = make([]BandTask, len(s.Todos))
		for i, t := range s.Todos {
			b.Tasks[i] = BandTask{ID: t.ID, Content: ScrubText(t.Content), Status: t.Status,
				StartedAt: t.StartedAt, CompletedAt: t.CompletedAt}
			if t.ID != "" {
				b.Tasks[i].TaskStat = stats[t.ID]
			}
		}
	}
	if len(turns) > 0 && taskMode(s.Todos) && DrivenByTodos(turns[len(turns)-1]) {
		b.Total = len(s.Todos)
		active := -1
		for i, t := range s.Todos {
			switch {
			case t.Status == "completed":
				b.Done++
			case t.Status == "in_progress" && active < 0:
				active = i
			}
		}
		if active < 0 {
			for i, t := range s.Todos {
				if t.Status != "completed" {
					active = i
					break
				}
			}
		}
		if active >= 0 {
			b.Active = ScrubText(s.Todos[active].Content)
			for _, t := range s.Todos[active+1:] {
				if t.Status != "completed" && t.Status != "in_progress" {
					b.Next = append(b.Next, ScrubText(t.Content))
				}
			}
		}
		// A fully completed list is not progress, and an unfinished one is
		// only pinned while a turn runs: either would otherwise hold band
		// rows for the rest of the session. The list is still there for
		// Ctrl+T. Gated here, not per client, so the TUI and web agree.
		b.Todos = s.Busy && b.Done < len(s.Todos)
		clock := TodoClockOf(s)
		b.Stale, b.StaleAge = session.TodoStale(s.Todos, clock, s.Busy, s.Now)
		switch {
		case b.Stale:
			b.StaleSince = clock.LastWrite
		case b.Todos && !clock.LastWrite.IsZero():
			if at := clock.LastWrite.Add(session.TodoStaleAfter); at.After(s.Now) {
				b.StaleAt = at
			}
		}
	}
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			switch {
			case n.Kind == KindSubagent && n.Item != nil && n.Item.Subagent != nil && n.Item.Subagent.Status == session.SubagentRunning:
				v := n.Item.Subagent
				b.Entities = append(b.Entities, BandEntity{Ref: n.ID, Label: fmt.Sprintf("#%d %s", v.ID, ScrubText(v.Label)), Detail: ScrubText(v.CurrentTool), Started: v.StartedAt})
			case n.Job != nil && n.Job.Running:
				b.Entities = append(b.Entities, BandEntity{Ref: n.ID, Label: n.Job.ID, Detail: ScrubText(n.Job.Command), Started: n.JobStart()})
			}
			walk(n.Children)
		}
	}
	walk(turns)
	return b
}
