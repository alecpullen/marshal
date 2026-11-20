package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/subagentwork"
	"marshal/internal/worktree"
)

// openWorkspaceDB opens a real on-disk database and migrates it, so the reopen
// and migration coverage below runs against the production adapter rather than
// an in-memory one.
func openWorkspaceDB(t *testing.T, path string) *DB {
	t.Helper()
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	if err := database.Migrate(); err != nil {
		database.Close()
		t.Fatalf("Migrate: %v", err)
	}
	return database
}

// createWorkspaceOwner creates the session row an owned record references.
func createWorkspaceOwner(t *testing.T, database *DB, sessionID string) {
	t.Helper()
	projectID, err := database.GetOrCreateProject("/repo", "repo")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.CreateSession(sessionID, projectID, "owner", time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("CreateSession(%s): %v", sessionID, err)
	}
}

// workspaceRecordFixture is a record carrying every durable field, so the round
// trip proves the whole record survives and not just its identity.
func workspaceRecordFixture(owner, taskKey, invocationID string) subagentwork.Record {
	exitCode := 1
	dirty := true
	return subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: owner,
		TaskKey:        taskKey,
		InvocationID:   invocationID,
		Binding: subagentwork.Binding{
			Mode:        subagentwork.ModeIsolated,
			Root:        "/repo/.marshal/worktrees/marshal-subagent-" + taskKey,
			ProjectRoot: "/repo",
			Repository:  "/repo/.git",
			Branch:      "marshal/subagent/" + taskKey,
			BaseSHA:     "1111111111111111111111111111111111111111",
		},
		Target: subagentwork.Target{
			Root:       "/repo",
			Repository: "/repo/.git",
			Branch:     "main",
			BaseSHA:    "1111111111111111111111111111111111111111",
		},
		SetupPlan: worktree.SetupPlan{Hooks: []config.WorktreeSetupHook{
			{Command: "go mod download", TimeoutSeconds: 30},
		}},
		SetupWarnings: []string{"seeding skipped node_modules"},
		Outcome:       subagentwork.OutcomeDone,
		Candidate:     "2222222222222222222222222222222222222222",
		Commits:       []string{"2222222222222222222222222222222222222222"},
		ChangedPaths:  []string{"internal/subagentwork/types.go"},
		Dirty:         &dirty,
		Verification: []subagentwork.Verification{
			{Command: "go test ./internal/subagentwork", Outcome: "failed", ExitCode: &exitCode},
		},
		ReportMessageID: 7,
		CleanupState:    subagentwork.CleanupRetained,
	}
}

// TestWorkspaceStoreDurableRoundTripAndReopen pins the production adapter: the
// whole versioned record round-trips, a duplicate dispatch is deduplicated, a
// stale swap is refused, and the record is still readable after the database is
// closed and reopened (which re-runs the migrations).
func TestWorkspaceStoreDurableRoundTripAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agents.db")
	database := openWorkspaceDB(t, path)
	createWorkspaceOwner(t, database, "owner-a")
	createWorkspaceOwner(t, database, "owner-b")
	store := NewSubagentWorkspaceStore(database)

	// An existing work-view record is still readable once the new migration has
	// run, and it stays readable after the reopen.
	workView := WorkViewRecord{Version: 1, Acknowledged: []WorkAcknowledgment{
		{TaskKey: "work:watch", ResultRevision: "rev-1"},
	}}
	if err := database.SaveWorkView("owner-a", workView); err != nil {
		t.Fatalf("SaveWorkView: %v", err)
	}

	created, ok, err := store.Create(ctx, workspaceRecordFixture("owner-a", "task-1", "inv-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !ok {
		t.Fatal("Create reported an existing record for a new owner and task key")
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", created.Revision)
	}

	got, err := store.Get(ctx, "owner-a", "task-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !recordsEqualFixture(got, created) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, created)
	}
	byInvocation, err := store.ByInvocation(ctx, "owner-a", "inv-1")
	if err != nil {
		t.Fatalf("ByInvocation: %v", err)
	}
	if !recordsEqualFixture(byInvocation, created) {
		t.Fatalf("ByInvocation round trip:\n got %+v\nwant %+v", byInvocation, created)
	}

	// A duplicate owner and invocation returns the record it owns, even when the
	// request spelled a different task key.
	deduped, ok, err := store.Create(ctx, workspaceRecordFixture("owner-a", "task-2", "inv-1"))
	if err != nil {
		t.Fatalf("Create duplicate invocation: %v", err)
	}
	if ok {
		t.Fatal("a duplicate owner and invocation created a second record")
	}
	if deduped.TaskKey != "task-1" {
		t.Fatalf("duplicate invocation returned task %q, want task-1", deduped.TaskKey)
	}

	// A stale swap is an error and leaves the stored record alone.
	done := created
	done.Outcome = subagentwork.OutcomeStopped
	if _, err := store.CompareAndSwap(ctx, done, created.Revision+5); !errors.Is(err, subagentwork.ErrStaleRecord) {
		t.Fatalf("stale CompareAndSwap error = %v, want ErrStaleRecord", err)
	}
	updated, err := store.CompareAndSwap(ctx, done, created.Revision)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if updated.Revision != created.Revision+1 || updated.Outcome != subagentwork.OutcomeStopped {
		t.Fatalf("updated record = %+v, want revision %d with the stopped outcome", updated, created.Revision+1)
	}

	// Owner scoping: another session cannot read or list the record.
	if _, err := store.Get(ctx, "owner-b", "task-1"); !errors.Is(err, subagentwork.ErrRecordNotFound) {
		t.Fatalf("cross-owner Get error = %v, want ErrRecordNotFound", err)
	}
	if _, err := store.ByInvocation(ctx, "owner-b", "inv-1"); !errors.Is(err, subagentwork.ErrRecordNotFound) {
		t.Fatalf("cross-owner ByInvocation error = %v, want ErrRecordNotFound", err)
	}
	otherList, err := store.List(ctx, "owner-b")
	if err != nil {
		t.Fatalf("List(owner-b): %v", err)
	}
	if len(otherList) != 0 {
		t.Fatalf("owner-b list = %+v, want it empty", otherList)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening runs the migrations again and the record is still there.
	reopened := openWorkspaceDB(t, path)
	defer reopened.Close()
	reopenedStore := NewSubagentWorkspaceStore(reopened)
	persisted, err := reopenedStore.Get(ctx, "owner-a", "task-1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if persisted.Revision != updated.Revision || persisted.Outcome != subagentwork.OutcomeStopped {
		t.Fatalf("persisted record = %+v, want the committed revision %d", persisted, updated.Revision)
	}
	list, err := reopenedStore.List(ctx, "owner-a")
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if len(list) != 1 || list[0].TaskKey != "task-1" {
		t.Fatalf("owner-a list after reopen = %+v, want only task-1", list)
	}
	// The pre-existing work-view record is untouched by the new migration.
	reloaded, err := reopened.LoadWorkView("owner-a")
	if err != nil {
		t.Fatalf("LoadWorkView after reopen: %v", err)
	}
	if len(reloaded.Acknowledged) != 1 || reloaded.Acknowledged[0].TaskKey != "work:watch" {
		t.Fatalf("work view after reopen = %+v, want the saved acknowledgment", reloaded)
	}

	// The migration is recorded, and the table exists.
	var version int
	if err := reopened.sqlDB.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version < 5 {
		t.Fatalf("schema version = %d, want at least the subagent workspace migration", version)
	}
	var table string
	if err := reopened.sqlDB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='session_subagent_workspaces'`,
	).Scan(&table); err != nil {
		t.Fatalf("subagent workspace table is missing: %v", err)
	}
}

// TestWorkspaceStoreRejectsUnsupportedAndUnownedRecords pins the two edges of
// the adapter: a stored record version this build cannot read is an error rather
// than a blank record, and a record must belong to a real session.
func TestWorkspaceStoreRejectsUnsupportedAndUnownedRecords(t *testing.T) {
	ctx := context.Background()
	database := openWorkspaceDB(t, filepath.Join(t.TempDir(), "agents.db"))
	defer database.Close()
	createWorkspaceOwner(t, database, "owner-a")
	store := NewSubagentWorkspaceStore(database)

	// A stored future version is an explicit error, and the stored data stays.
	const future = `{"version":2,"owner_session_id":"owner-a","task_key":"future","invocation_id":"inv-future"}`
	if _, err := database.sqlDB.Exec(
		`INSERT INTO session_subagent_workspaces (`+subagentWorkspaceColumns+`) VALUES (?, ?, ?, ?, ?)`,
		"owner-a", "future", "inv-future", 1, future,
	); err != nil {
		t.Fatalf("insert future record: %v", err)
	}
	if _, err := store.Get(ctx, "owner-a", "future"); err == nil {
		t.Fatal("Get returned a blank success for a record version this build cannot read")
	}
	var stored string
	if err := database.sqlDB.QueryRow(
		`SELECT record_json FROM session_subagent_workspaces WHERE owner_session_id=? AND task_key=?`,
		"owner-a", "future",
	).Scan(&stored); err != nil {
		t.Fatalf("read stored future record: %v", err)
	}
	if stored != future {
		t.Fatalf("stored future record changed:\n got %s\nwant %s", stored, future)
	}

	// Saving a version this build does not write is refused.
	futureRecord := workspaceRecordFixture("owner-a", "task-future", "inv-task-future")
	futureRecord.Version = 2
	if _, _, err := store.Create(ctx, futureRecord); err == nil {
		t.Fatal("Create accepted an unsupported record version")
	}

	// A record whose owner session does not exist is refused by the foreign key:
	// an unowned record would outlive the session that owns the work.
	unowned := workspaceRecordFixture("no-such-session", "task-u", "inv-u")
	if _, _, err := store.Create(ctx, unowned); err == nil {
		t.Fatal("Create accepted a record for a session that does not exist")
	}
}

// recordsEqualFixture compares the durable fields of two records. It exists so a
// failure prints both records rather than a bare "not equal".
func recordsEqualFixture(a, b subagentwork.Record) bool {
	if a.Version != b.Version || a.Revision != b.Revision ||
		a.OwnerSessionID != b.OwnerSessionID || a.TaskKey != b.TaskKey ||
		a.InvocationID != b.InvocationID || a.Binding != b.Binding ||
		a.Target != b.Target || a.Outcome != b.Outcome ||
		a.Candidate != b.Candidate || a.SourceError != b.SourceError ||
		a.ReportMessageID != b.ReportMessageID || a.CleanupState != b.CleanupState {
		return false
	}
	if len(a.Commits) != len(b.Commits) || len(a.ChangedPaths) != len(b.ChangedPaths) ||
		len(a.SetupWarnings) != len(b.SetupWarnings) || len(a.Verification) != len(b.Verification) ||
		len(a.SetupPlan.Hooks) != len(b.SetupPlan.Hooks) {
		return false
	}
	for i := range a.Commits {
		if a.Commits[i] != b.Commits[i] {
			return false
		}
	}
	for i := range a.ChangedPaths {
		if a.ChangedPaths[i] != b.ChangedPaths[i] {
			return false
		}
	}
	for i := range a.SetupWarnings {
		if a.SetupWarnings[i] != b.SetupWarnings[i] {
			return false
		}
	}
	for i := range a.SetupPlan.Hooks {
		if a.SetupPlan.Hooks[i] != b.SetupPlan.Hooks[i] {
			return false
		}
	}
	for i := range a.Verification {
		av, bv := a.Verification[i], b.Verification[i]
		if av.Command != bv.Command || av.Outcome != bv.Outcome {
			return false
		}
		if (av.ExitCode == nil) != (bv.ExitCode == nil) || (av.ExitCode != nil && *av.ExitCode != *bv.ExitCode) {
			return false
		}
	}
	if (a.Dirty == nil) != (b.Dirty == nil) || (a.Dirty != nil && *a.Dirty != *b.Dirty) {
		return false
	}
	if (a.Integration == nil) != (b.Integration == nil) {
		return false
	}
	if a.Integration != nil && *a.Integration != *b.Integration {
		return false
	}
	return true
}
