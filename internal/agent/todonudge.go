package agent

import (
	"fmt"
	"strings"
	"time"

	"marshal/internal/app/session"
	"marshal/internal/strutil"
)

// startTodoClock starts the stale clock with the turn, so a list carried in
// from an earlier session is judged from now, not from when it was written.
func (r *Runner) startTodoClock() {
	r.todoMu.Lock()
	defer r.todoMu.Unlock()
	r.todoClock = session.TodoClock{LastWrite: r.Now()}
	r.todoNudged = false
}

// noteResponse counts a model response against the stale clock.
func (r *Runner) noteResponse() {
	r.todoMu.Lock()
	defer r.todoMu.Unlock()
	r.todoClock.Responses++
}

// noteTodoWrite resets the clock after a successful todo.write: the list was
// just maintained, so the countdown starts again.
func (r *Runner) noteTodoWrite() {
	r.todoMu.Lock()
	defer r.todoMu.Unlock()
	r.todoClock = session.TodoClock{LastWrite: r.Now()}
	r.todoNudged = false
}

// maxNudgeItemRunes bounds the item text the reminder quotes. The list holds
// model-authored text of unbounded length, and the reminder is appended to a
// tool result once per list version; without a bound one enormous item would
// ride along whole, every time.
const maxNudgeItemRunes = 120

// todoNudge returns a reminder to append to a tool result when the todo list
// has gone stale (session.TodoStale), once per list version.
//
// It is deliberately once-per-version: a reminder on every call after the
// threshold would be noise the model learns to skip, and the point is to make
// it look at the list, not to nag. Only the agent that owns the list is
// nudged — a subagent cannot see its parent's plan.
//
// Read-only tool calls run in parallel, so this is reached from several
// goroutines at once: the latch and the clock are read and written under
// todoMu, or two calls in one batch could each append the reminder.
func (r *Runner) todoNudge() string {
	r.todoMu.Lock()
	defer r.todoMu.Unlock()
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
	return fmt.Sprintf("\n\n<system-reminder>\nYour todo list hasn't changed in %s / %d responses; %s is still in progress. "+
		"Update it with todo.write to reflect what's done, or clear it if the plan changed.\n</system-reminder>",
		age.Round(time.Minute), r.todoClock.Responses, nudgeQuote(active))
}

// nudgeQuote renders an item's content as a fragment safe to embed inside the
// reminder's fence. The content is model-authored, so it is:
//
//   - bounded, so a single huge item cannot append itself whole;
//   - %q-quoted, so quotes, line breaks and control characters are escaped;
//   - stripped of its angle brackets, so an item containing the literal
//     closing tag cannot end the reminder early and pass the text after it off
//     as marshal's own instruction.
func nudgeQuote(s string) string {
	q := fmt.Sprintf("%q", strutil.Truncate(strings.TrimSpace(s), maxNudgeItemRunes, true))
	return strings.NewReplacer("<", "\\u003c", ">", "\\u003e").Replace(q)
}
