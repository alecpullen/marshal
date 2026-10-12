package agent

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

// fakeClock is the runner's Now, so the stale thresholds can be crossed
// without waiting five real minutes.
type fakeClock struct{ now time.Time }

// newNudgeRunner builds the smallest runner the stale-todo nudge reads: a
// state to hold the list and a clock to measure it against.
func newNudgeRunner(t *testing.T) (*Runner, *session.State, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(10_000, 0)}
	st := session.New(config.Config{}, t.TempDir(), clock.now, session.Persistence{})
	r := &Runner{State: st, Now: func() time.Time { return clock.now }}
	return r, st, clock
}

func nudgeList() []db.TodoItem {
	return []db.TodoItem{
		{ID: "a", Content: "Task 0", Status: "in_progress"},
		{ID: "b", Content: "Task 1", Status: "pending"},
	}
}

func TestTodoNudgeOncePerListVersion(t *testing.T) {
	r, st, clock := newNudgeRunner(t)
	if err := st.SetTodos(nudgeList()); err != nil {
		t.Fatal(err)
	}
	r.todoClock = session.TodoClock{LastWrite: clock.now, Responses: session.TodoStaleResponses}
	clock.now = clock.now.Add(session.TodoStaleAfter)

	first := r.todoNudge()
	if !strings.Contains(first, "<system-reminder>") || !strings.Contains(first, `"Task 0"`) {
		t.Fatalf("first = %q", first)
	}
	if again := r.todoNudge(); again != "" {
		t.Fatalf("must not repeat: %q", again)
	}
	r.noteTodoWrite()
	if after := r.todoNudge(); after != "" {
		t.Fatalf("a write resets the clock: %q", after)
	}
}

// A subagent does not own the parent's list, so it must never be nudged about
// it: the reminder would tell a child to maintain a plan it cannot see.
func TestTodoNudgeNeverForSubagents(t *testing.T) {
	clock := &fakeClock{now: time.Unix(10_000, 0)}
	child := session.New(config.Config{}, t.TempDir(), clock.now, session.Persistence{}, session.WithDepth(1))
	if err := child.SetTodos(nudgeList()); err != nil {
		t.Fatal(err)
	}
	r := &Runner{State: child, Now: func() time.Time { return clock.now }}
	r.todoClock = session.TodoClock{LastWrite: clock.now, Responses: 99}
	clock.now = clock.now.Add(time.Hour)
	if got := r.todoNudge(); got != "" {
		t.Fatalf("subagent nudged: %q", got)
	}
}

// Review focus 4: a list carried in from an earlier session is judged from
// the turn's start, not from when it was written.
func TestNudgeClockStartsAtRunTask(t *testing.T) {
	r, _, clock := newNudgeRunner(t)
	r.startTodoClock()
	if !r.todoClock.LastWrite.Equal(clock.now) || r.todoClock.Responses != 0 {
		t.Fatalf("clock = %+v", r.todoClock)
	}
	if r.todoNudged {
		t.Fatal("a new turn must clear the once-per-version latch")
	}
}

// noteResponse is what makes a turn stale in the response dimension; without
// it the time threshold alone could never fire.
func TestNoteResponseCountsTheClock(t *testing.T) {
	r, _, _ := newNudgeRunner(t)
	r.startTodoClock()
	for i := 0; i < 3; i++ {
		r.noteResponse()
	}
	if r.todoClock.Responses != 3 {
		t.Fatalf("responses = %d, want 3", r.todoClock.Responses)
	}
}
