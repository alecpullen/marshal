package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/tools/native"
)

// fakeSampler returns a scripted sequence of samples/errors.
type fakeSampler struct {
	mu      sync.Mutex
	samples []Sample
	errs    []error
	idx     int
}

func (f *fakeSampler) Sample(ctx context.Context, w *watch) (Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx < len(f.errs) && f.errs[f.idx] != nil {
		err := f.errs[f.idx]
		f.idx++
		return Sample{}, err
	}
	if f.idx < len(f.samples) {
		s := f.samples[f.idx]
		f.idx++
		return s, nil
	}
	// Default: repeat the last sample (no change).
	if len(f.samples) > 0 {
		return f.samples[len(f.samples)-1], nil
	}
	return Sample{}, nil
}

func newTestManager(t *testing.T, deps Deps) *Manager {
	t.Helper()
	m := NewManager(context.Background(), deps)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.Close(ctx)
	})
	return m
}

func TestStartRejectsUnknownKind(t *testing.T) {
	m := newTestManager(t, Deps{})
	_, _, err := m.Start(Spec{Name: "x", Kind: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown watch kind") {
		t.Fatalf("expected unknown kind error, got %v", err)
	}
}

func TestStartRejectsBadCondition(t *testing.T) {
	m := newTestManager(t, Deps{})
	_, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown condition type") {
		t.Fatalf("expected condition error, got %v", err)
	}
}

func TestStartRejectsJobWithUserCondition(t *testing.T) {
	m := newTestManager(t, Deps{JobLookup: func(id string) (native.JobInfo, bool) {
		return native.JobInfo{ID: id}, true
	}})
	_, _, err := m.Start(Spec{Name: "x", Kind: KindJob, JobID: "job-1", Condition: "exit_code 0"})
	if err == nil || !strings.Contains(err.Error(), "user-supplied condition not allowed") {
		t.Fatalf("expected job condition error, got %v", err)
	}
}

func TestStartRejectsUnknownJobID(t *testing.T) {
	m := newTestManager(t, Deps{JobLookup: func(id string) (native.JobInfo, bool) {
		return native.JobInfo{}, false
	}})
	_, _, err := m.Start(Spec{Name: "x", Kind: KindJob, JobID: "job-99"})
	if err == nil || !strings.Contains(err.Error(), "unknown job ID") {
		t.Fatalf("expected unknown job error, got %v", err)
	}
}

func TestStartRejectsJobWithoutLookup(t *testing.T) {
	m := newTestManager(t, Deps{})
	_, _, err := m.Start(Spec{Name: "x", Kind: KindJob, JobID: "job-1"})
	if err == nil || !strings.Contains(err.Error(), "job lookup not configured") {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestStartClampsInterval(t *testing.T) {
	m := newTestManager(t, Deps{})
	id, note, err := m.Start(Spec{Name: "x", Kind: KindCommand, Interval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if note == "" || !strings.Contains(note, "clamped") {
		t.Fatalf("expected clamp note, got %q", note)
	}
	info, err := m.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	if info.Interval != MinInterval {
		t.Fatalf("interval = %v, want %v", info.Interval, MinInterval)
	}
}

func TestStartDedupsName(t *testing.T) {
	m := newTestManager(t, Deps{})
	id1, _, err := m.Start(Spec{Name: "build", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := m.Start(Spec{Name: "build", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	id3, _, err := m.Start(Spec{Name: "build", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	info1, _ := m.Status(id1)
	info2, _ := m.Status(id2)
	info3, _ := m.Status(id3)
	if info1.Name != "build" || info2.Name != "build-2" || info3.Name != "build-3" {
		t.Fatalf("dedup names = %q, %q, %q", info1.Name, info2.Name, info3.Name)
	}
}

func TestStartCapReached(t *testing.T) {
	m := newTestManager(t, Deps{})
	for i := 0; i < MaxWatches; i++ {
		if _, _, err := m.Start(Spec{Name: "w", Kind: KindCommand}); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	_, _, err := m.Start(Spec{Name: "overflow", Kind: KindCommand})
	if err == nil || !strings.Contains(err.Error(), "watch cap reached") {
		t.Fatalf("expected cap error, got %v", err)
	}
	if !strings.Contains(err.Error(), "w1") || !strings.Contains(err.Error(), "w10") {
		t.Fatalf("cap error should list active IDs, got %v", err)
	}
}

func TestStartRejectsClosedManager(t *testing.T) {
	m := NewManager(context.Background(), Deps{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("expected ErrManagerClosed, got %v", err)
	}
}

func TestListSortedByCreatedAtThenID(t *testing.T) {
	m := newTestManager(t, Deps{})
	// Start three watches. The sort is by CreatedAt then ID. Because the
	// starts happen within the same nanosecond, CreatedAt values are equal
	// and the ID tiebreak applies: w1, w2, w3.
	id1, _, _ := m.Start(Spec{Name: "a", Kind: KindCommand})
	id2, _, _ := m.Start(Spec{Name: "b", Kind: KindCommand})
	id3, _, _ := m.Start(Spec{Name: "c", Kind: KindCommand})
	list := m.List()
	if len(list) != 3 {
		t.Fatalf("list len = %d, want 3", len(list))
	}
	// With equal CreatedAt, the ID tiebreak yields w1, w2, w3.
	if list[0].ID != id1 || list[1].ID != id2 || list[2].ID != id3 {
		t.Fatalf("list order = %s, %s, %s; want %s, %s, %s", list[0].ID, list[1].ID, list[2].ID, id1, id2, id3)
	}
}

func TestListSortedByCreatedAtWhenDistinct(t *testing.T) {
	m := newTestManager(t, Deps{})
	// Force distinct CreatedAt values by starting with a small sleep between
	// registrations so the CreatedAt ordering dominates the ID tiebreak.
	id1, _, _ := m.Start(Spec{Name: "a", Kind: KindCommand})
	time.Sleep(2 * time.Millisecond)
	id2, _, _ := m.Start(Spec{Name: "b", Kind: KindCommand})
	time.Sleep(2 * time.Millisecond)
	id3, _, _ := m.Start(Spec{Name: "c", Kind: KindCommand})
	list := m.List()
	if len(list) != 3 {
		t.Fatalf("list len = %d, want 3", len(list))
	}
	// Distinct CreatedAt: oldest first regardless of ID.
	if list[0].ID != id1 || list[1].ID != id2 || list[2].ID != id3 {
		t.Fatalf("list order = %s, %s, %s; want %s, %s, %s", list[0].ID, list[1].ID, list[2].ID, id1, id2, id3)
	}
}

func TestStatusNotFound(t *testing.T) {
	m := newTestManager(t, Deps{})
	_, err := m.Status("w99")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestStopIdempotent(t *testing.T) {
	m := newTestManager(t, Deps{})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	// First stop succeeds with no note.
	note, err := m.Stop(id)
	if err != nil || note != "" {
		t.Fatalf("first stop note=%q err=%v", note, err)
	}
	// Second stop is idempotent with a note.
	note, err = m.Stop(id)
	if err != nil || note != "was already gone" {
		t.Fatalf("second stop note=%q err=%v", note, err)
	}
	// Unknown ID is also idempotent.
	note, err = m.Stop("w99")
	if err != nil || note != "was already gone" {
		t.Fatalf("unknown stop note=%q err=%v", note, err)
	}
}

func TestCloseJoinsGoroutinesAndRejectsStarts(t *testing.T) {
	m := NewManager(context.Background(), Deps{})
	if _, _, err := m.Start(Spec{Name: "x", Kind: KindCommand}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := m.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, _, err := m.Start(Spec{Name: "y", Kind: KindCommand}); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("expected ErrManagerClosed after close, got %v", err)
	}
}

func TestFireOnceAutoRemoves(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "hello", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 0"})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)

	mu.Lock()
	reportCount := len(reports)
	mu.Unlock()
	if reportCount != 1 {
		t.Fatalf("reports = %d, want 1", reportCount)
	}
	if !reports[0].AutoRemoved {
		t.Error("once-mode report should mark AutoRemoved")
	}
	// Once mode auto-removes: Status should now fail.
	if _, err := m.Status(id); err == nil {
		t.Error("once-mode watch should be removed after firing")
	}
}

func TestFireRepeatFiresMultiple(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	// exit_code 0 fires on every sample (no baseline skip for non-change).
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}, {Stdout: "b", ExitCode: 0}, {Stdout: "c", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)
	m.sampleOnce(w)
	m.sampleOnce(w)

	mu.Lock()
	reportCount := len(reports)
	mu.Unlock()
	if reportCount != 3 {
		t.Fatalf("reports = %d, want 3", reportCount)
	}
	// Repeat mode stays registered.
	if _, err := m.Status(id); err != nil {
		t.Fatalf("repeat watch should remain: %v", err)
	}
}

func TestChangeConditionSkipsBaseline(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	// Static source: same sample every time. change should never fire.
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "same", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // baseline
	m.sampleOnce(w) // no change
	m.sampleOnce(w) // no change

	mu.Lock()
	reportCount := len(reports)
	mu.Unlock()
	if reportCount != 0 {
		t.Fatalf("change on static source fired %d times, want 0", reportCount)
	}
}

func TestChangeConditionFiresOnChange(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}, {Stdout: "b", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "change", Mode: ModeRepeat})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // baseline "a"
	m.sampleOnce(w) // "b" changed -> fire

	mu.Lock()
	reportCount := len(reports)
	mu.Unlock()
	if reportCount != 1 {
		t.Fatalf("change on changed source fired %d times, want 1", reportCount)
	}
}

func TestErrorBudgetAutoStops(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	m.setSampler(&fakeSampler{errs: []error{
		errors.New("e1"), errors.New("e2"), errors.New("e3"),
	}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)
	m.sampleOnce(w)
	m.sampleOnce(w) // third error -> auto-stop

	mu.Lock()
	reportCount := len(reports)
	mu.Unlock()
	if reportCount != 1 {
		t.Fatalf("error reports = %d, want 1", reportCount)
	}
	if !reports[0].AutoRemoved {
		t.Error("error auto-stop report should mark AutoRemoved")
	}
	// Auto-stopped watch is removed.
	if _, err := m.Status(id); err == nil {
		t.Error("auto-stopped watch should be removed")
	}
}

func TestErrorBudgetIsConsecutive(t *testing.T) {
	var mu sync.Mutex
	var reports []Report
	m := newTestManager(t, Deps{OnFire: func(r Report) {
		mu.Lock()
		reports = append(reports, r)
		mu.Unlock()
	}})
	// Two errors, a success, then two more errors. The budget is consecutive,
	// so the success resets it and the watch must NOT auto-stop.
	m.setSampler(&fakeSampler{
		errs:    []error{errors.New("e1"), errors.New("e2"), nil, errors.New("e3"), errors.New("e4")},
		samples: []Sample{{Stdout: "ok", ExitCode: 0}},
	})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // e1
	m.sampleOnce(w) // e2
	m.sampleOnce(w) // success -> resets budget
	m.sampleOnce(w) // e3
	m.sampleOnce(w) // e4

	mu.Lock()
	reportCount := len(reports)
	mu.Unlock()
	if reportCount != 0 {
		t.Fatalf("error reports = %d, want 0 (budget is consecutive)", reportCount)
	}
	// The watch must still be registered (not auto-stopped).
	if _, err := m.Status(id); err != nil {
		t.Fatalf("watch should remain registered: %v", err)
	}
}

func TestSuccessRestoresStateFromError(t *testing.T) {
	m := newTestManager(t, Deps{})
	m.setSampler(&fakeSampler{
		errs:    []error{errors.New("e1"), nil},
		samples: []Sample{{Stdout: "ok", ExitCode: 0}},
	})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // e1 -> StateError
	info, _ := m.Status(id)
	if info.State != StateError {
		t.Fatalf("state after error = %q, want %q", info.State, StateError)
	}
	m.sampleOnce(w) // success -> back to watching
	info, _ = m.Status(id)
	if info.State != StateWatching {
		t.Fatalf("state after success = %q, want %q", info.State, StateWatching)
	}
}

func TestStopJobWatchTerminatesGoroutine(t *testing.T) {
	m := newTestManager(t, Deps{JobLookup: func(id string) (native.JobInfo, bool) {
		return native.JobInfo{ID: id}, true
	}})
	id, _, err := m.Start(Spec{Name: "job", Kind: KindJob, JobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Stop the job watch. This cancels the watch context; the job goroutine
	// must exit promptly (it must not wait for the manager to close).
	if _, err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	// With only this one goroutine registered, wg.Wait() returning means the
	// job watch goroutine has exited. Bounded wait keeps the test
	// deterministic (no sleep).
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// goroutine exited promptly
	case <-time.After(2 * time.Second):
		t.Fatal("job watch goroutine did not exit after Stop (leak)")
	}
}

func TestOnEventPublished(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	m := newTestManager(t, Deps{OnEvent: func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}})
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "hello", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "x", Kind: KindCommand, Condition: "exit_code 0"})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w)

	mu.Lock()
	eventCount := len(events)
	mu.Unlock()
	if eventCount == 0 {
		t.Fatal("expected at least one event")
	}
	if events[0].WatchID != id || events[0].State != StateFired {
		t.Fatalf("event = %+v, want fired for %s", events[0], id)
	}
}
