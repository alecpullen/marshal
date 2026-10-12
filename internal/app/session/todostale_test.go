package session

import (
	"testing"
	"time"

	"marshal/internal/db"
)

func inProg() []db.TodoItem {
	return []db.TodoItem{{ID: "a", Content: "A", Status: "in_progress"}, {ID: "b", Content: "B", Status: "pending"}}
}

func TestTodoStaleNeedsBothTimeAndResponses(t *testing.T) {
	t0 := time.Unix(1000, 0)
	cases := []struct {
		name string
		age  time.Duration
		resp int
		want bool
	}{
		{"both met", 5 * time.Minute, 8, true},
		{"time short", 5*time.Minute - time.Second, 8, false},
		{"responses short", 6 * time.Minute, 7, false},
	}
	for _, c := range cases {
		got, _ := TodoStale(inProg(), TodoClock{LastWrite: t0, Responses: c.resp}, true, t0.Add(c.age))
		if got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestTodoStaleNeverWhenIdleOrNothingInProgress(t *testing.T) {
	t0 := time.Unix(1000, 0)
	c := TodoClock{LastWrite: t0, Responses: 20}
	now := t0.Add(time.Hour)
	if s, _ := TodoStale(inProg(), c, false, now); s {
		t.Error("idle")
	}
	if s, _ := TodoStale(nil, c, true, now); s {
		t.Error("empty list")
	}
	done := []db.TodoItem{{ID: "a", Status: "completed"}, {ID: "b", Status: "pending"}}
	if s, _ := TodoStale(done, c, true, now); s {
		t.Error("no in-progress item")
	}
}

// Review focus 4.
func TestTodoStaleClockStartsAtTurnStart(t *testing.T) {
	if s, _ := TodoStale(inProg(), TodoClock{Responses: 50}, true, time.Unix(1e9, 0)); s {
		t.Fatal("a zero clock (no write, no turn start) is never stale")
	}
}
