package subagentwork

import (
	"context"
	"errors"
	"testing"
)

// TestWorkspaceStoreOwnerAndInvocationIsolation pins the store's ownership
// rules: records are owner-scoped, (owner, task key) is the primary key,
// (owner, invocation) deduplicates one logical dispatch, and a compare-and-swap
// that loses its revision race is an error rather than a silent overwrite.
func TestWorkspaceStoreOwnerAndInvocationIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	first := Record{
		Version:        RecordVersion,
		OwnerSessionID: "owner-a",
		TaskKey:        "task-1",
		InvocationID:   "inv-1",
		Outcome:        OutcomePrepared,
	}
	createdRecord, created, err := store.Create(ctx, first)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created {
		t.Fatal("Create reported an existing record for a new owner and task key")
	}
	if createdRecord.Revision != 1 {
		t.Fatalf("created revision = %d, want 1", createdRecord.Revision)
	}

	// The same task spelling under another owner is a different record: one
	// session's workspace never shadows another's.
	otherOwner := first
	otherOwner.OwnerSessionID = "owner-b"
	otherRecord, created, err := store.Create(ctx, otherOwner)
	if err != nil {
		t.Fatalf("Create for a second owner: %v", err)
	}
	if !created {
		t.Fatal("the same task spelling under another owner was not isolated")
	}
	if otherRecord.OwnerSessionID != "owner-b" || otherRecord.Revision != 1 {
		t.Fatalf("second owner record = %+v, want its own revision-1 record", otherRecord)
	}

	// A duplicate owner and invocation returns the existing record, even when
	// the request spelled a different task key.
	duplicateInvocation := first
	duplicateInvocation.TaskKey = "task-2"
	deduped, created, err := store.Create(ctx, duplicateInvocation)
	if err != nil {
		t.Fatalf("Create duplicate invocation: %v", err)
	}
	if created {
		t.Fatal("a duplicate owner and invocation created a second record")
	}
	if deduped.TaskKey != "task-1" {
		t.Fatalf("duplicate invocation returned task %q, want the original task-1", deduped.TaskKey)
	}

	// A duplicate owner and task key likewise returns the record it owns.
	if _, created, err = store.Create(ctx, first); err != nil || created {
		t.Fatalf("duplicate task key: created = %v, err = %v, want false and no error", created, err)
	}

	// Lookups are owner-scoped and never cross owners.
	if _, err := store.Get(ctx, "owner-c", "task-1"); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("cross-owner Get error = %v, want ErrRecordNotFound", err)
	}
	if _, err := store.ByInvocation(ctx, "owner-c", "inv-1"); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("cross-owner ByInvocation error = %v, want ErrRecordNotFound", err)
	}
	byInvocation, err := store.ByInvocation(ctx, "owner-a", "inv-1")
	if err != nil {
		t.Fatalf("ByInvocation: %v", err)
	}
	if byInvocation.TaskKey != "task-1" {
		t.Fatalf("ByInvocation returned task %q, want task-1", byInvocation.TaskKey)
	}
	list, err := store.List(ctx, "owner-a")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].TaskKey != "task-1" {
		t.Fatalf("owner-a list = %+v, want only task-1", list)
	}

	// A compare-and-swap updates only the expected revision and returns the
	// incremented record.
	done := createdRecord
	done.Outcome = OutcomeDone
	updated, err := store.CompareAndSwap(ctx, done, createdRecord.Revision)
	if err != nil {
		t.Fatalf("CompareAndSwap: %v", err)
	}
	if updated.Revision != createdRecord.Revision+1 {
		t.Fatalf("updated revision = %d, want %d", updated.Revision, createdRecord.Revision+1)
	}
	if updated.Outcome != OutcomeDone {
		t.Fatalf("updated outcome = %q, want %q", updated.Outcome, OutcomeDone)
	}

	// A stale compare-and-swap is an error, and the stored record is untouched.
	if _, err := store.CompareAndSwap(ctx, done, createdRecord.Revision); !errors.Is(err, ErrStaleRecord) {
		t.Fatalf("stale CompareAndSwap error = %v, want ErrStaleRecord", err)
	}
	stored, err := store.Get(ctx, "owner-a", "task-1")
	if err != nil {
		t.Fatalf("Get after stale swap: %v", err)
	}
	if stored.Revision != updated.Revision || stored.Outcome != OutcomeDone {
		t.Fatalf("stored record = %+v, want the committed revision %d", stored, updated.Revision)
	}

	// A version this package cannot read is refused rather than stored.
	future := Record{Version: 2, OwnerSessionID: "owner-a", TaskKey: "task-3", InvocationID: "inv-3"}
	if _, _, err := store.Create(ctx, future); err == nil {
		t.Fatal("Create accepted an unsupported record version")
	}
}
