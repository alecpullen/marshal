package watch

import (
	"sync"
	"testing"
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

// TestFireOwnerRaceWithTransfer is a -race-visible regression test for the
// owner data race: fire reads w.owner while building the Report, and
// TransferFromSubagent writes w.owner under w.mu. Running them concurrently
// on the same watch must not race. The race detector (go test -race) is what
// catches the unsynchronized read; this test just drives both paths at once.
func TestFireOwnerRaceWithTransfer(t *testing.T) {
	m := newTestManager(t, Deps{OnFire: func(r Report) {}})
	id, _, err := m.Start(Spec{Name: "race", Kind: KindCommand, Condition: "change", Mode: ModeRepeat, Owner: "subagent-7"})
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
		for i := 0; i < 200; i++ {
			m.fire(w, Sample{Stdout: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.TransferFromSubagent("subagent-7")
		}
	}()
	wg.Wait()
}
