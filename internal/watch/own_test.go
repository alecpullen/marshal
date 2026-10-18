package watch

import (
	"sync"
	"testing"
	"time"
)

// TestTransferFromSubagent verifies that TransferFromSubagent re-parents a
// subagent's once watches to the parent (owner "") and stops its repeat
// watches.
func TestTransferFromSubagent(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	// Inject a fake sampler so the once watch can actually fire (the
	// default sourceSampler has a nil RunSample and would error).
	m.setSampler(&fakeSampler{samples: []Sample{
		{Stdout: "a"},
		{Stdout: "b"},
	}})

	// A once watch owned by the subagent.
	onceID, _, err := m.Start(Spec{Name: "once", Kind: KindCommand, Condition: "change", Mode: ModeOnce, Owner: "subagent-7"})
	if err != nil {
		t.Fatal(err)
	}
	// A repeat watch owned by the subagent.
	repeatID, _, err := m.Start(Spec{Name: "repeat", Kind: KindCommand, Condition: "change", Mode: ModeRepeat, Owner: "subagent-7"})
	if err != nil {
		t.Fatal(err)
	}

	m.TransferFromSubagent("subagent-7")

	// The repeat watch must be stopped and removed from the manager.
	if _, err := m.Status(repeatID); err == nil {
		t.Fatalf("repeat watch %s still registered after transfer, want removed", repeatID)
	}

	// The once watch must be re-parented to the parent (owner "").
	onceInfo, err := m.Status(onceID)
	if err != nil {
		t.Fatalf("once watch status: %v", err)
	}
	if onceInfo.Owner != "" {
		t.Fatalf("once watch owner = %q, want \"\" (parent)", onceInfo.Owner)
	}

	// Fire the re-parented once watch; its report must carry owner "".
	w := m.getWatch(onceID)
	if w == nil {
		t.Fatal("once watch not found after transfer")
	}
	m.sampleOnce(w) // baseline
	m.sampleOnce(w) // change -> fire

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	if reports[0].Owner != "" {
		t.Fatalf("fired report owner = %q, want \"\" (parent)", reports[0].Owner)
	}
}

// TestTransferFromSubagentNoopForEmptyOwner guards the empty-owner no-op.
func TestTransferFromSubagentNoopForEmptyOwner(t *testing.T) {
	m := newTestManager(t, Deps{})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	m.TransferFromSubagent("")
	info, err := m.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateWatching {
		t.Fatalf("state = %v, want watching (empty owner must not stop watches)", info.State)
	}
}

// TestTransferFromSubagentIgnoresOtherOwners guards that only the named
// owner's watches are affected.
func TestTransferFromSubagentIgnoresOtherOwners(t *testing.T) {
	m := newTestManager(t, Deps{})
	id, _, err := m.Start(Spec{Name: "mine", Kind: KindCommand, Condition: "change", Mode: ModeRepeat, Owner: "subagent-9"})
	if err != nil {
		t.Fatal(err)
	}
	m.TransferFromSubagent("subagent-7")
	info, err := m.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateWatching {
		t.Fatalf("state = %v, want watching (other owner's watch must survive)", info.State)
	}
}

// TestWatchSnapshotReparentPreservesIdentityAndRevisions pins that re-parenting
// a subagent's once watch to the parent preserves its identity, advances the
// source membership revision, and publishes a copied reparenting wake.
func TestWatchSnapshotReparentPreservesIdentityAndRevisions(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	m := newTestManager(t, Deps{OnEvent: func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}})
	id, _, err := m.Start(Spec{Name: "mine", Kind: KindCommand, Condition: "change", Mode: ModeOnce, Owner: "subagent-7", Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	before := m.Snapshot()
	orig, ok := findInfo(before.Active, id)
	if !ok {
		t.Fatalf("watch %s missing before transfer", id)
	}
	if orig.Owner != "subagent-7" {
		t.Fatalf("owner before transfer = %q, want subagent-7", orig.Owner)
	}

	m.TransferFromSubagent("subagent-7")

	after := m.Snapshot()
	moved, ok := findInfo(after.Active, id)
	if !ok {
		t.Fatalf("watch %s disappeared on reparent", id)
	}
	if moved.Owner != "" {
		t.Fatalf("owner after transfer = %q, want parent", moved.Owner)
	}
	if moved.ID != orig.ID || moved.Name != orig.Name || !moved.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("identity changed on reparent: %+v -> %+v", orig, moved)
	}
	if after.Epoch != before.Epoch {
		t.Fatalf("epoch changed on reparent: %q -> %q", before.Epoch, after.Epoch)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("revision did not advance on reparent: %d -> %d", before.Revision, after.Revision)
	}

	// The reparenting wake is a copy carrying the new parent owner.
	mu.Lock()
	defer mu.Unlock()
	var wake *Event
	for i := range events {
		if events[i].WatchID == id && events[i].Owner == "" {
			wake = &events[i]
		}
	}
	if wake == nil {
		t.Fatalf("no reparenting wake published for %s", id)
	}
	if wake.Info == nil || wake.Info.Owner != "" || !wake.Info.Active {
		t.Fatalf("wake Info = %+v, want a copied active parent-owned snapshot", wake.Info)
	}
	if wake.Epoch != after.Epoch {
		t.Fatalf("wake epoch = %q, want %q", wake.Epoch, after.Epoch)
	}
}

// TestFireOwnerRaceWithTransfer is a -race-visible regression test for the
// owner data race: fire reads w.owner while building the Report, and
// TransferFromSubagent writes w.owner under w.mu. Running them concurrently
// on the same watch must not race. The race detector (go test -race) is what
// catches the unsynchronized read; this test just drives both paths at once.
//
// A ONCE-mode watch is required: TransferFromSubagent sends repeat watches
// down the stop path (which never writes w.owner), but re-parents once
// watches via the reparent loop that executes w.owner = "". Each iteration
// uses a fresh once watch because a once watch auto-removes on fire, so the
// reparent write path is exercised whenever transfer finds the watch before
// fire removes it — which, over many iterations, is guaranteed.
func TestFireOwnerRaceWithTransfer(t *testing.T) {
	m := newTestManager(t, Deps{OnFire: func(r Report) {}})
	for i := 0; i < 500; i++ {
		id, _, err := m.Start(Spec{Name: "race", Kind: KindCommand, Condition: "change", Mode: ModeOnce, Owner: "subagent-7"})
		if err != nil {
			t.Fatal(err)
		}
		w := m.getWatch(id)
		if w == nil {
			t.Fatal("watch not found")
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			m.TransferFromSubagent("subagent-7")
		}()
		go func() {
			defer wg.Done()
			m.fire(w, Sample{Stdout: "x"})
		}()
		wg.Wait()
	}
}
