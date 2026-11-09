package session

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/db"
	"marshal/internal/pubsub"
)

// transcriptTestState is a two-turn transcript: turn1, a1, turn2, a2.
func transcriptTestState() *State {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	s.AddMessage(RoleUser, "turn1", ContentTypePlain)
	s.AddMessage(RoleAssistant, "a1", ContentTypeMarkdown)
	s.AddMessage(RoleUser, "turn2", ContentTypePlain)
	s.AddMessage(RoleAssistant, "a2", ContentTypeMarkdown)
	return s
}

// subscribeTranscript attaches a broker to s and returns its subscription.
func subscribeTranscript(t *testing.T, s *State) <-chan pubsub.Event[Event] {
	t.Helper()
	broker := pubsub.NewBroker[Event]()
	s.SetEventBroker(broker)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return broker.Subscribe(ctx)
}

// drainEvents discards whatever is already buffered, so a row's setup calls
// cannot be mistaken for the row's own publish.
func drainEvents(ch <-chan pubsub.Event[Event]) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// expectOneTranscriptChanged fails unless the next event is exactly one
// transcript_changed, with nothing behind it.
func expectOneTranscriptChanged(t *testing.T, ch <-chan pubsub.Event[Event]) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscription closed before the change")
		}
		if ev.Type != EventTranscriptChanged {
			t.Fatalf("event type = %q, want %q", ev.Type, EventTranscriptChanged)
		}
	case <-time.After(time.Second):
		t.Fatal("no transcript_changed event")
	}
	select {
	case ev := <-ch:
		t.Fatalf("a second event (%q) followed transcript_changed", ev.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTheSilentTranscriptCallsPublish(t *testing.T) {
	calls := []struct {
		name    string
		prepare func(t *testing.T, s *State)
		call    func(t *testing.T, s *State)
	}{
		{
			name: "rewind",
			call: func(t *testing.T, s *State) { s.Rewind(s.Messages()[2].ID) },
		},
		{
			name: "switch_branch",
			prepare: func(t *testing.T, s *State) {
				s.Rewind(s.Messages()[2].ID)
			},
			call: func(t *testing.T, s *State) { s.SwitchBranch(s.Branches()[0]) },
		},
		{
			name: "clear_messages",
			call: func(t *testing.T, s *State) { s.ClearMessages() },
		},
		{
			name: "log_thinking",
			call: func(t *testing.T, s *State) { s.LogThinking(ThinkingEntry{Text: "pondering"}) },
		},
		{
			name: "set_todos",
			call: func(t *testing.T, s *State) {
				if err := s.SetTodos([]db.TodoItem{{ID: "t1", Content: "a", Status: "pending"}}); err != nil {
					t.Fatalf("SetTodos: %v", err)
				}
			},
		},
		{
			name: "add_run_event",
			call: func(t *testing.T, s *State) { s.AddRunEvent(RunEvent{Kind: RunEventCommit, Title: "abc123"}) },
		},
		{
			name:    "clear_run_events",
			prepare: func(t *testing.T, s *State) { s.AddRunEvent(RunEvent{Kind: RunEventCommit, Title: "abc123"}) },
			call:    func(t *testing.T, s *State) { s.ClearRunEvents() },
		},
		{
			name: "set_workspace",
			call: func(t *testing.T, s *State) {
				s.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo-wt", Branch: "wt"})
			},
		},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			s := transcriptTestState()
			ch := subscribeTranscript(t, s)
			if c.prepare != nil {
				c.prepare(t, s)
			}
			drainEvents(ch)
			c.call(t, s)
			expectOneTranscriptChanged(t, ch)
		})
	}
}

func TestANoOpPublishesNothing(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T, s *State)
		call func(t *testing.T, s *State)
	}{
		{
			name: "rewind_of_an_unknown_message",
			call: func(t *testing.T, s *State) { s.Rewind(999) },
		},
		{
			name: "workspace_that_does_not_change",
			set: func(t *testing.T, s *State) {
				s.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo", Branch: ""})
			},
			call: func(t *testing.T, s *State) {
				s.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo", Branch: ""})
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := transcriptTestState()
			if c.set != nil {
				c.set(t, s)
			}
			ch := subscribeTranscript(t, s)
			c.call(t, s)
			select {
			case ev := <-ch:
				t.Fatalf("%s published %q, want nothing", c.name, ev.Type)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func TestTheCallsPublishWithoutABroker(t *testing.T) {
	calls := []struct {
		name string
		call func(t *testing.T, s *State)
	}{
		{"rewind", func(t *testing.T, s *State) { s.Rewind(s.Messages()[2].ID) }},
		{"switch_branch", func(t *testing.T, s *State) { s.SwitchBranch(s.Branches()[0]) }},
		{"clear_messages", func(t *testing.T, s *State) { s.ClearMessages() }},
		{"log_thinking", func(t *testing.T, s *State) { s.LogThinking(ThinkingEntry{Text: "x"}) }},
		{"set_todos", func(t *testing.T, s *State) {
			if err := s.SetTodos([]db.TodoItem{{ID: "t1", Content: "a", Status: "pending"}}); err != nil {
				t.Fatalf("SetTodos: %v", err)
			}
		}},
		{"add_run_event", func(t *testing.T, s *State) { s.AddRunEvent(RunEvent{Kind: RunEventCommit, Title: "abc"}) }},
		{"clear_run_events", func(t *testing.T, s *State) { s.ClearRunEvents() }},
		{"set_workspace", func(t *testing.T, s *State) {
			s.SetWorkspace(Workspace{ProjectRoot: "/repo", ActiveRoot: "/repo-wt", Branch: "wt"})
		}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			s := transcriptTestState()
			c.call(t, s) // must not panic with no broker attached
		})
	}
}
