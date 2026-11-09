package service

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/app/session"
	"marshal/internal/commands"
)

// TestOldActionsExpire checks the session keeps only the newest results' actions:
// an ID past the bound comes back stale instead of running.
func TestOldActionsExpire(t *testing.T) {
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "counter",
		Handler: func(*session.State, []string) commands.Result {
			return commands.Panel("counter", false, []commands.Row{{
				Text:        "run",
				ActionLabel: "run",
				Action:      func(*session.State) commands.Result { return commands.Text("ran") },
			}})
		},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, sess := New(Deps{State: newState(t, "s1"), Commands: reg})

	ids := make([]string, 0, maxActionResults+1)
	for i := 0; i < maxActionResults+1; i++ {
		res, err := sess.Command(context.Background(), "counter", nil)
		if err != nil {
			t.Fatalf("Command %d: %v", i, err)
		}
		if res.Doc == nil || len(res.Doc.Rows) != 1 {
			t.Fatalf("Command %d returned no action row", i)
		}
		ids = append(ids, res.Doc.Rows[0].ActionID)
	}

	if _, err := sess.CommandAction(context.Background(), ids[0]); !errors.Is(err, errStaleAction) {
		t.Errorf("CommandAction(%s) = %v, want errStaleAction", ids[0], err)
	}
	newest := ids[len(ids)-1]
	res, err := sess.CommandAction(context.Background(), newest)
	if err != nil {
		t.Fatalf("CommandAction(%s), the newest ID: %v", newest, err)
	}
	if res.Text != "ran" {
		t.Errorf("CommandAction(%s).Text = %q, want %q", newest, res.Text, "ran")
	}
}
