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
	Next     []string // waiting items after the active one, in list order
	Entities []BandEntity
}

// BandEntity is one running card the band lists.
type BandEntity struct {
	Ref     NodeID
	Label   string // "#3 Pass 1 Task 1" or "job-4"
	Detail  string // latest activity, or the command's first line
	Started time.Time
}

// TodoClockOf reads the todo clock from the transcript: the later of the last
// todo.write and the last user turn's start, plus the orchestrator responses
// since. A list carried in from an earlier session therefore starts its clock
// with the turn, not with when it was written.
func TodoClockOf(s Snapshot) session.TodoClock {
	var c session.TodoClock
	for _, it := range s.Items {
		if it.Kind == session.KindMessage && session.IsUserTurnMessage(it.Message) && it.Timestamp.After(c.LastWrite) {
			c.LastWrite = it.Timestamp
		}
		if it.Audit != nil && it.Audit.ToolName == todoWriteTool && it.Audit.Error == "" && it.Timestamp.After(c.LastWrite) {
			c.LastWrite = it.Timestamp
		}
	}
	for _, st := range s.Steps {
		// A subagent's own responses are not the list owner's progress.
		if st.Actor.Role == "" && st.StartedAt.After(c.LastWrite) {
			c.Responses++
		}
	}
	return c
}

// BandOf builds the band for the latest turn.
func BandOf(s Snapshot, turns []*Node) BandInfo {
	var b BandInfo
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
		// A fully completed list is not progress: it must not hold band rows
		// for the rest of the session. The list is still there for Ctrl+T.
		b.Todos = b.Done < len(s.Todos)
		b.Stale, b.StaleAge = session.TodoStale(s.Todos, TodoClockOf(s), s.Busy, s.Now)
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
