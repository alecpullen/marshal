package agent

import (
	"fmt"
	"time"

	"marshal/internal/app/session"
)

// startTodoClock starts the stale clock with the turn, so a list carried in
// from an earlier session is judged from now, not from when it was written.
func (r *Runner) startTodoClock() {
	r.todoClock = session.TodoClock{LastWrite: r.Now()}
	r.todoNudged = false
}

// noteResponse counts a model response against the stale clock.
func (r *Runner) noteResponse() { r.todoClock.Responses++ }

// noteTodoWrite resets the clock after a successful todo.write: the list was
// just maintained, so the countdown starts again.
func (r *Runner) noteTodoWrite() {
	r.todoClock = session.TodoClock{LastWrite: r.Now()}
	r.todoNudged = false
}

// todoNudge returns a reminder to append to a tool result when the todo list
// has gone stale (session.TodoStale), once per list version.
//
// It is deliberately once-per-version: a reminder on every call after the
// threshold would be noise the model learns to skip, and the point is to make
// it look at the list, not to nag. Only the agent that owns the list is
// nudged — a subagent cannot see its parent's plan.
func (r *Runner) todoNudge() string {
	if r.todoNudged || r.State.SubagentDepth() > 0 {
		return ""
	}
	todos := r.State.Todos()
	stale, age := session.TodoStale(todos, r.todoClock, true, r.Now())
	if !stale {
		return ""
	}
	active := ""
	for _, t := range todos {
		if t.Status == "in_progress" {
			active = t.Content
			break
		}
	}
	r.todoNudged = true
	return fmt.Sprintf("\n\n<system-reminder>\nYour todo list hasn't changed in %s / %d responses; %q is still in progress. "+
		"Update it with todo.write to reflect what's done, or clear it if the plan changed.\n</system-reminder>",
		age.Round(time.Minute), r.todoClock.Responses, active)
}
