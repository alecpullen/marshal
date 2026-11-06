package inproc

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/db"
)

// openHistoryTestDB opens a migrated database with one project, the way the
// sheet tests build theirs.
func openHistoryTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	return database, projectID
}

// TestPromptHistoryRoundTrips pins the two history calls against a real
// database: a saved prompt comes back newest first, and limit bounds it.
func TestPromptHistoryRoundTrips(t *testing.T) {
	ctx := context.Background()
	database, projectID := openHistoryTestDB(t)
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{DB: database, SessionID: "s1"})
	_, sess := New(Deps{State: state, ProjectID: projectID})

	for _, text := range []string{"first", "second"} {
		if err := sess.SavePrompt(ctx, text); err != nil {
			t.Fatalf("SavePrompt(%q): %v", text, err)
		}
	}

	got, err := sess.RecentPrompts(ctx, 1)
	if err != nil {
		t.Fatalf("RecentPrompts(1): %v", err)
	}
	if want := []string{"second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RecentPrompts(1) = %v, want %v", got, want)
	}

	got, err = sess.RecentPrompts(ctx, 10)
	if err != nil {
		t.Fatalf("RecentPrompts(10): %v", err)
	}
	if want := []string{"second", "first"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RecentPrompts(10) = %v, want %v", got, want)
	}
}

// TestPromptHistoryWithoutADatabase pins that history is project state: a
// session with no database, and one with a database but no project, returns
// none and saves nothing without failing.
func TestPromptHistoryWithoutADatabase(t *testing.T) {
	ctx := context.Background()
	database, _ := openHistoryTestDB(t)

	cases := []struct {
		name string
		deps Deps
	}{
		{
			name: "no database",
			deps: Deps{State: newState(t, "s1"), ProjectID: 7},
		},
		{
			name: "no project",
			deps: Deps{
				State: session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{DB: database, SessionID: "s2"}),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sess := New(tc.deps)
			got, err := sess.RecentPrompts(ctx, 10)
			if err != nil {
				t.Fatalf("RecentPrompts: %v", err)
			}
			if got != nil {
				t.Errorf("RecentPrompts = %v, want nil", got)
			}
			if err := sess.SavePrompt(ctx, "not saved"); err != nil {
				t.Errorf("SavePrompt = %v, want nil", err)
			}
		})
	}
}

// TestMessagesSelectsByRoleFinalityAndLast pins the query: role, finality with
// a non-blank answer, and the Last cap that reports the count it cut.
func TestMessagesSelectsByRoleFinalityAndLast(t *testing.T) {
	ctx := context.Background()
	state := newState(t, "s1")
	state.AddMessage(session.RoleUser, "one", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "draft", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "answer", session.ContentTypePlain)
	state.AddMessage(session.RoleUser, "two", session.ContentTypePlain)
	state.AddMessageFinal(session.RoleAssistant, "   ", session.ContentTypePlain)
	_, sess := New(Deps{State: state})

	t.Run("role", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: acpwire.RoleUser})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		want := []acpwire.HistoryMessage{
			{Role: acpwire.RoleUser, Content: "one"},
			{Role: acpwire.RoleUser, Content: "two"},
		}
		if !reflect.DeepEqual(got.Messages, want) {
			t.Errorf("Messages = %+v, want %+v", got.Messages, want)
		}
		if got.Total != 2 {
			t.Errorf("Total = %d, want 2", got.Total)
		}
	})

	t.Run("role and last", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: acpwire.RoleAssistant, Last: 1})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		want := []acpwire.HistoryMessage{{Role: acpwire.RoleAssistant, Content: "   ", Final: true}}
		if !reflect.DeepEqual(got.Messages, want) {
			t.Errorf("Messages = %+v, want %+v", got.Messages, want)
		}
		if got.Total != 3 {
			t.Errorf("Total = %d, want 3", got.Total)
		}
	})

	t.Run("final", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: acpwire.RoleAssistant, Final: true, Last: 1})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		want := []acpwire.HistoryMessage{{Role: acpwire.RoleAssistant, Content: "answer", Final: true}}
		if !reflect.DeepEqual(got.Messages, want) {
			t.Errorf("Messages = %+v, want %+v", got.Messages, want)
		}
		if got.Total != 1 {
			t.Errorf("Total = %d, want 1", got.Total)
		}
	})

	t.Run("last", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Last: 1})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(got.Messages) != 1 {
			t.Errorf("Messages = %+v, want one message", got.Messages)
		}
		if got.Total != 5 {
			t.Errorf("Total = %d, want every message in the state", got.Total)
		}
	})

	t.Run("last beyond the total", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Last: 10})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(got.Messages) != 5 {
			t.Errorf("Messages = %+v, want every message, uncut", got.Messages)
		}
		if got.Total != 5 {
			t.Errorf("Total = %d, want 5", got.Total)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		got, err := sess.Messages(ctx, acpwire.MessageQuery{Role: "system"})
		if err != nil {
			t.Fatalf("Messages: %v", err)
		}
		if len(got.Messages) != 0 {
			t.Errorf("Messages = %+v, want none", got.Messages)
		}
		if got.Total != 0 {
			t.Errorf("Total = %d, want 0", got.Total)
		}
	})
}
