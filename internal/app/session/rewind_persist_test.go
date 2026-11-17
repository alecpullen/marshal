package session

import (
	"database/sql"
	"testing"
)

// TestRewindBeforeFirstTurnPersistsEmptyRoot pins the persisted encoding of a
// rewind to before the first turn (AUD-09): the leaf is not a message id, and
// the session records that the branch was explicitly emptied.
func TestRewindBeforeFirstTurnPersistsEmptyRoot(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	s.AddMessage(RoleUser, "turn1", ContentTypePlain)
	s.AddMessage(RoleAssistant, "a1", ContentTypePlain)

	firstID := s.Messages()[0].ID
	if leaf := s.Rewind(firstID); leaf != 0 {
		t.Fatalf("Rewind before the first turn returned leaf %d, want 0", leaf)
	}

	var leaf sql.NullInt64
	var emptied int64
	row := d.SQLDB().QueryRow(`SELECT leaf_message_id, branch_emptied FROM agent_sessions WHERE id = ?`, sid)
	if err := row.Scan(&leaf, &emptied); err != nil {
		t.Fatalf("read persisted branch state: %v", err)
	}
	if leaf.Valid && leaf.Int64 != 0 {
		t.Fatalf("persisted leaf_message_id = %d, want NULL (a message id resurrects the branch)", leaf.Int64)
	}
	if emptied != 1 {
		t.Fatalf("persisted branch_emptied = %d, want 1 (the empty root must be explicit)", emptied)
	}
}

// TestRewindBeforeFirstTurnSurvivesReload is the completion condition for
// AUD-09: after a reload (including the Migrate() every startup runs), the
// rewound-to-root session loads with an empty transcript instead of the
// branch the user rewound away from.
func TestRewindBeforeFirstTurnSurvivesReload(t *testing.T) {
	d, sid := newStepTestDB(t)
	s := persistedState(t, d, sid)
	s.AddMessage(RoleUser, "turn1", ContentTypePlain)
	s.AddMessage(RoleAssistant, "a1", ContentTypePlain)
	s.Rewind(s.Messages()[0].ID)

	// Every startup runs Migrate(), including the leaf backfill.
	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	reloaded := persistedState(t, d, sid)
	if got := len(reloaded.Messages()); got != 0 {
		t.Fatalf("reloaded messages = %d, want 0: the rewound branch came back", got)
	}
	if leaf := reloaded.LeafID(); leaf != 0 {
		t.Fatalf("reloaded leaf = %d, want 0", leaf)
	}
}
