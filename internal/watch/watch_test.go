package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
	// The first event is the registration (StateWatching) publish; the fired
	// event follows the sample.
	if events[0].WatchID != id || events[0].State != StateWatching {
		t.Fatalf("first event = %+v, want watching for %s", events[0], id)
	}
	if eventCount < 2 {
		t.Fatalf("expected a fired event after registration, got %d events", eventCount)
	}
	if events[1].WatchID != id || events[1].State != StateFired {
		t.Fatalf("second event = %+v, want fired for %s", events[1], id)
	}
}

// activeIDs indexes an Info slice by registration ID.
func activeIDs(infos []Info) map[string]bool {
	out := make(map[string]bool, len(infos))
	for _, in := range infos {
		out[in.ID] = true
	}
	return out
}

// findInfo returns the Info for id and whether it was present.
func findInfo(infos []Info, id string) (Info, bool) {
	for _, in := range infos {
		if in.ID == id {
			return in, true
		}
	}
	return Info{}, false
}

// containsString reports whether ss contains want.
func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestWatchSnapshotOnceRemovalAndRepeatLiveness pins the membership split: a
// once fire retires the registration (retained in Snapshot.Terminal, gone from
// Active), while a repeat fire keeps the watch live and out of Terminal.
func TestWatchSnapshotOnceRemovalAndRepeatLiveness(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	m.setSampler(&fakeSampler{samples: []Sample{
		{Stdout: "a", ExitCode: 0},
		{Stdout: "b", ExitCode: 0},
		{Stdout: "c", ExitCode: 0},
	}})

	onceID, _, err := m.Start(Spec{Name: "once", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	repeatID, _, err := m.Start(Spec{Name: "repeat", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	m.sampleOnce(m.getWatch(onceID))
	m.sampleOnce(m.getWatch(repeatID))
	m.sampleOnce(m.getWatch(repeatID))

	snap := m.Snapshot()
	if activeIDs(snap.Active)[onceID] {
		t.Fatalf("once watch %s still active after fire", onceID)
	}
	term, ok := findInfo(snap.Terminal, onceID)
	if !ok {
		t.Fatalf("once watch %s missing from Terminal", onceID)
	}
	if term.Active {
		t.Error("retained terminal Info should not be active")
	}
	if term.TerminalReason != terminalReasonCompleted {
		t.Errorf("terminal reason = %q, want %q", term.TerminalReason, terminalReasonCompleted)
	}
	if term.EndedAt.IsZero() {
		t.Error("terminal EndedAt is zero")
	}
	if term.ResultRevision == "" {
		t.Error("terminal ResultRevision is empty")
	}

	if !activeIDs(snap.Active)[repeatID] {
		t.Fatalf("repeat watch %s should remain active", repeatID)
	}
	if _, ok := findInfo(snap.Terminal, repeatID); ok {
		t.Fatalf("repeat watch %s should not be terminal", repeatID)
	}
	rep, _ := findInfo(snap.Active, repeatID)
	if !rep.Active {
		t.Error("active repeat Info has Active=false")
	}
	if rep.FireCount != 2 {
		t.Errorf("repeat FireCount = %d, want 2", rep.FireCount)
	}
}

// TestWatchSnapshotErrorRecoveryIsNotTerminal pins that a recoverable sampling
// error keeps the watch Active (StateError) and out of Terminal, and that the
// recovery edge returns it to watching without a terminal record.
func TestWatchSnapshotErrorRecoveryIsNotTerminal(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	samp := &scriptedSampler{
		failTimes: 1,
		err:       errors.New("sample boom"),
		sample:    Sample{Stdout: "ok", ExitCode: 0},
	}
	m.setSampler(samp)
	id, _, err := m.Start(Spec{Name: "e", Kind: KindCommand, Condition: "exit_code 7", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	m.sampleOnce(m.getWatch(id)) // one error: below the budget, recoverable
	snap := m.Snapshot()
	in, ok := findInfo(snap.Active, id)
	if !ok {
		t.Fatalf("errored watch %s should stay active", id)
	}
	if !in.Active {
		t.Error("recoverable error should keep Active true")
	}
	if in.State != StateError {
		t.Errorf("state = %q, want %q", in.State, StateError)
	}
	if len(snap.Terminal) != 0 {
		t.Errorf("recoverable error produced %d terminal records, want 0", len(snap.Terminal))
	}

	m.sampleOnce(m.getWatch(id)) // success: recovers to watching, no fire
	snap = m.Snapshot()
	in, ok = findInfo(snap.Active, id)
	if !ok {
		t.Fatalf("recovered watch %s should stay active", id)
	}
	if in.State != StateWatching {
		t.Errorf("state after recovery = %q, want %q", in.State, StateWatching)
	}
	if len(snap.Terminal) != 0 {
		t.Errorf("recovery produced %d terminal records, want 0", len(snap.Terminal))
	}
	if in.TerminalReason != "" {
		t.Errorf("active watch carries terminal reason %q", in.TerminalReason)
	}
}

// TestWatchTerminalSaveRetryKeepsMembershipTruthful pins the durable-result
// boundary: the registration is settling while the initial sink is in flight
// and already gone from Active; a failed attempt records SaveErrors; a
// successful RetryTerminalSaves clears it without a second report, event, or
// terminal receipt.
func TestWatchTerminalSaveRetryKeepsMembershipTruthful(t *testing.T) {
	rec := &recorder{}
	var calls atomic.Int32
	var mu sync.Mutex
	var received []Info
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})

	deps := rec.deps()
	deps.OnTerminal = func(in Info) error {
		n := calls.Add(1)
		mu.Lock()
		received = append(received, in)
		mu.Unlock()
		if n == 1 {
			entered <- struct{}{}
			<-gate
			return errors.New("db down")
		}
		return nil
	}
	m := newTestManager(t, deps)
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		m.sampleOnce(m.getWatch(id))
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTerminal was not called")
	}

	// The initial sink is in flight: settling recorded, active membership
	// already released, terminal fact retained.
	snap := m.Snapshot()
	if !containsString(snap.Settling, id) {
		t.Fatalf("Settling = %v, want %s while the initial sink is in flight", snap.Settling, id)
	}
	if activeIDs(snap.Active)[id] {
		t.Fatalf("watch %s should not be active while settling", id)
	}
	if _, ok := findInfo(snap.Terminal, id); !ok {
		t.Fatalf("terminal fact for %s missing while settling", id)
	}

	close(gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sampleOnce did not return")
	}

	snap = m.Snapshot()
	if len(snap.Settling) != 0 {
		t.Fatalf("Settling = %v, want cleared after the initial attempt", snap.Settling)
	}
	if snap.SaveErrors[id] == "" {
		t.Fatalf("SaveErrors = %v, want a recorded failure for %s", snap.SaveErrors, id)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnTerminal calls = %d, want 1 before retry", got)
	}

	if err := m.RetryTerminalSaves(); err != nil {
		t.Fatalf("RetryTerminalSaves: %v", err)
	}
	snap = m.Snapshot()
	if len(snap.SaveErrors) != 0 {
		t.Fatalf("SaveErrors = %v, want cleared after a successful retry", snap.SaveErrors)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("OnTerminal calls = %d, want 2 (initial + retry)", got)
	}
	if got := len(snap.Terminal); got != 1 {
		t.Fatalf("Terminal records = %d, want 1 (no duplicate)", got)
	}
	if fires := rec.firedReports(); len(fires) != 1 {
		t.Fatalf("OnFire calls = %d, want 1 (retry must not re-report)", len(fires))
	}
	if evs := rec.watchEvents(StateFired); len(evs) != 1 {
		t.Fatalf("fired events = %d, want 1 (retry must not re-announce)", len(evs))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 || received[0].ID != id || received[1].ID != id {
		t.Fatalf("OnTerminal Infos = %+v, want the same registration twice", received)
	}
	for _, in := range received {
		if in.Active || in.TerminalReason != terminalReasonCompleted {
			t.Fatalf("OnTerminal Info = %+v, want an inactive completed fact", in)
		}
	}
}

// TestWatchTerminalRetentionIsBounded pins the retention cap: removed
// registrations stay retained for repair, but the map is bounded so a long
// session that churns transient watches cannot accumulate unbounded state nor
// pay an ever-growing deep copy per Snapshot poll. The newest entries survive.
func TestWatchTerminalRetentionIsBounded(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}}})

	total := MaxRetainedTerminals + 20
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		ids = append(ids, id)
		m.sampleOnce(m.getWatch(id)) // fires and retires the once watch
	}

	snap := m.Snapshot()
	if len(snap.Terminal) != MaxRetainedTerminals {
		t.Fatalf("retained terminals = %d, want %d", len(snap.Terminal), MaxRetainedTerminals)
	}
	present := activeIDs(snap.Terminal)
	// The oldest overflow entries are evicted; the newest survive.
	for i, id := range ids {
		_, got := present[id]
		want := i >= total-MaxRetainedTerminals
		if got != want {
			t.Fatalf("terminal %s (index %d) present = %v, want %v", id, i, got, want)
		}
	}
}

// TestWatchTerminalRetentionKeepsSettling pins that eviction never drops an
// entry whose registration is still settling: its durable sink is in flight
// and RetryTerminalSaves needs the retained evidence. The protected entry is
// the oldest, so without the guard it would be the first evicted.
func TestWatchTerminalRetentionKeepsSettling(t *testing.T) {
	entered := make(chan struct{})
	gate := make(chan struct{})
	var firstID string

	deps := (&recorder{}).deps()
	deps.OnTerminal = func(in Info) error {
		// Block only the oldest registration's initial sink, leaving it
		// settling while newer terminals churn through the map.
		if firstID != "" && in.ID == firstID {
			close(entered)
			<-gate
		}
		return nil
	}
	m := newTestManager(t, deps)
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}}})

	id, _, err := m.Start(Spec{Name: "t0", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	firstID = id

	done := make(chan struct{})
	go func() {
		m.sampleOnce(m.getWatch(id))
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTerminal for the settling watch was not called")
	}

	total := MaxRetainedTerminals + 10
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		nid, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		ids = append(ids, nid)
		m.sampleOnce(m.getWatch(nid)) // fires and retires the once watch
	}

	snap := m.Snapshot()
	if !containsString(snap.Settling, id) {
		t.Fatalf("Settling = %v, want %s while its sink is in flight", snap.Settling, id)
	}
	if _, ok := findInfo(snap.Terminal, id); !ok {
		t.Fatalf("settling terminal %s was evicted despite an in-flight sink", id)
	}
	if len(snap.Terminal) != MaxRetainedTerminals {
		t.Fatalf("retained terminals = %d, want %d", len(snap.Terminal), MaxRetainedTerminals)
	}
	if _, ok := findInfo(snap.Terminal, ids[len(ids)-1]); !ok {
		t.Fatalf("newest terminal %s was evicted", ids[len(ids)-1])
	}

	// Release the blocked sink so the goroutine and its cleanup can finish.
	close(gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sampleOnce for the settling watch did not return")
	}
}

// TestWatchLateSampleCannotResurrectRemovedRegistration pins that a sample
// already in flight for a removed registration cannot recreate membership,
// re-report, or add a second terminal record.
func TestWatchLateSampleCannotResurrectRemovedRegistration(t *testing.T) {
	rec := &recorder{}
	m := newTestManager(t, rec.deps())
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}}})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	m.sampleOnce(w) // fires and retires the once watch

	// Late samples for the same (now removed) registration.
	m.sampleOnce(w)
	m.sampleOnce(w)

	snap := m.Snapshot()
	if len(snap.Active) != 0 {
		t.Fatalf("Active = %v, want empty; a late sample resurrected membership", activeIDs(snap.Active))
	}
	if len(snap.Terminal) != 1 {
		t.Fatalf("Terminal = %d, want 1", len(snap.Terminal))
	}
	if term, _ := findInfo(snap.Terminal, id); term.TerminalReason != terminalReasonCompleted {
		t.Fatalf("terminal reason = %q, want %q", term.TerminalReason, terminalReasonCompleted)
	}
	if fires := rec.firedReports(); len(fires) != 1 {
		t.Fatalf("OnFire calls = %d, want 1", len(fires))
	}
}

// TestWatchSnapshotEpochAndCopies pins that the epoch is stable per manager
// and distinct across managers, that snapshot sample rings are deep copies,
// and that a published terminal event's Info pointer is a copy that cannot
// reach the retained terminal fact.
func TestWatchSnapshotEpochAndCopies(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	m := newTestManager(t, Deps{OnEvent: func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}})
	m.setSampler(&fakeSampler{samples: []Sample{{Stdout: "a", ExitCode: 0}, {Stdout: "b", ExitCode: 0}}})

	other := NewManager(context.Background(), Deps{})
	otherCtx, otherCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer otherCancel()
	defer func() { _ = other.Close(otherCtx) }()

	// A repeat watch that never trips (exit_code 7) accumulates samples.
	repID, _, err := m.Start(Spec{Name: "c", Kind: KindCommand, Condition: "exit_code 7", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	m.sampleOnce(m.getWatch(repID))
	m.sampleOnce(m.getWatch(repID))

	first := m.Snapshot()
	if first.Epoch == "" {
		t.Fatal("snapshot epoch is empty")
	}
	if o := other.Snapshot(); o.Epoch == "" || o.Epoch == first.Epoch {
		t.Fatalf("other manager epoch = %q, want non-empty and distinct from %q", o.Epoch, first.Epoch)
	}
	if len(first.Active) != 1 {
		t.Fatalf("Active = %d, want 1", len(first.Active))
	}
	if first.Active[0].Epoch != first.Epoch {
		t.Fatalf("Active epoch = %q, want %q", first.Active[0].Epoch, first.Epoch)
	}
	before := len(first.Active[0].Samples)
	if before == 0 {
		t.Fatal("expected recorded samples")
	}

	// Mutating a returned snapshot must not reach the manager.
	first.Active[0].Samples = append(first.Active[0].Samples, SamplePoint{})
	first.Active[0].Epoch = "tampered"
	second := m.Snapshot()
	if got := len(second.Active[0].Samples); got != before {
		t.Fatalf("snapshot samples aliased: got %d, want %d", got, before)
	}
	if second.Active[0].Epoch != first.Epoch {
		t.Fatalf("snapshot epoch aliased: %q, want %q", second.Active[0].Epoch, first.Epoch)
	}

	// A once fire retains a terminal fact and publishes an event whose Info is
	// a copy: mutating it must not reach the retained terminal record.
	onceID, _, err := m.Start(Spec{Name: "d", Kind: KindCommand, Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	m.sampleOnce(m.getWatch(onceID))

	mu.Lock()
	var firedInfo *Info
	for i := range events {
		if events[i].WatchID == onceID && events[i].Info != nil {
			firedInfo = events[i].Info
		}
	}
	mu.Unlock()
	if firedInfo == nil {
		t.Fatalf("no fired event carried Info for %s", onceID)
	}
	firedInfo.TerminalReason = "tampered"
	firedInfo.Samples = append(firedInfo.Samples, SamplePoint{})

	third := m.Snapshot()
	term, ok := findInfo(third.Terminal, onceID)
	if !ok {
		t.Fatalf("terminal fact for %s missing", onceID)
	}
	if term.TerminalReason != terminalReasonCompleted {
		t.Fatalf("retained terminal reason = %q (event Info aliased)", term.TerminalReason)
	}
}

// TestWatchSnapshotCloseFinalizesOnlyJoinedRegistrations pins that a close
// whose join times out finalizes nothing (the registration stays active), a
// later successful close finalizes it exactly once with the shutdown reason,
// and a repeated close does not report a second terminal receipt.
func TestWatchSnapshotCloseFinalizesOnlyJoinedRegistrations(t *testing.T) {
	var calls atomic.Int32
	m := newTestManager(t, Deps{OnTerminal: func(Info) error {
		calls.Add(1)
		return nil
	}})
	m.setSampler(&fakeSampler{})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Condition: "exit_code 7", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	// Force the first join to time out. A positive Add while the counter is
	// already non-zero is permitted by sync.WaitGroup.
	m.wg.Add(1)
	release := make(chan struct{})
	go func() {
		defer m.wg.Done()
		<-release
	}()

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer timeoutCancel()
	if err := m.Close(timeoutCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Close = %v, want context deadline exceeded", err)
	}
	snap := m.Snapshot()
	if len(snap.Terminal) != 0 {
		t.Fatalf("timed-out close finalized %d registrations, want 0", len(snap.Terminal))
	}
	if len(snap.Active) != 1 {
		t.Fatalf("Active = %d, want the registration still active", len(snap.Active))
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("OnTerminal calls after timed-out close = %d, want 0", got)
	}

	close(release)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if err := m.Close(closeCtx); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	snap = m.Snapshot()
	if len(snap.Active) != 0 {
		t.Fatalf("Active = %d after successful close, want 0", len(snap.Active))
	}
	term, ok := findInfo(snap.Terminal, id)
	if !ok {
		t.Fatalf("registration %s not retained after close", id)
	}
	if term.TerminalReason != terminalReasonShutdown {
		t.Fatalf("terminal reason = %q, want %q", term.TerminalReason, terminalReasonShutdown)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnTerminal calls = %d, want exactly 1", got)
	}

	// A repeated close must not finalize or report a second time.
	if err := m.Close(closeCtx); err != nil {
		t.Fatalf("third Close: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnTerminal calls = %d after repeated close, want 1", got)
	}
}

// TestStartRejectsJobRepeatModeWithoutRetainingTheLock pins that the job+repeat
// rejection releases the manager mutex. The rejection sits inside the locked
// region, so a bare return would hold m.mu for the rest of the process and wedge
// every later Start/List/Snapshot/Stop/Close plus the coordinator's poll.
func TestStartRejectsJobRepeatModeWithoutRetainingTheLock(t *testing.T) {
	m := newTestManager(t, Deps{JobLookup: func(id string) (native.JobInfo, bool) {
		return native.JobInfo{ID: id}, true
	}})
	if _, _, err := m.Start(Spec{Name: "x", Kind: KindJob, JobID: "job-1", Mode: ModeRepeat}); err == nil ||
		!strings.Contains(err.Error(), "repeat mode not allowed") {
		t.Fatalf("expected a repeat-mode rejection, got %v", err)
	}
	// Everything that takes the manager lock must still return.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := m.Start(Spec{Name: "ok", Kind: KindJob, JobID: "job-1"}); err != nil {
			t.Errorf("start after a rejected repeat watch: %v", err)
		}
		_ = m.List()
		_ = m.Snapshot()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the manager stayed locked after a rejected job repeat watch")
	}
}

// TestSampleErrorAfterStopNeverResurrectsTheWatch pins that a sample error
// arriving after the watch was retired can neither mutate its state nor publish
// a state change. The sampler samples under the manager context, so a sample in
// flight when Stop retires the watch returns afterwards; without the terminal
// guard the removed watch flips to StateError and announces itself active again,
// contradicting the removal event the lane already received.
func TestSampleErrorAfterStopNeverResurrectsTheWatch(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	m := newTestManager(t, Deps{
		// A non-terminal job watch is inert: its loop never samples, so the only
		// error delivered here is the one this test calls in directly.
		JobLookup: func(id string) (native.JobInfo, bool) { return native.JobInfo{ID: id}, true },
		OnEvent: func(e Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
		},
	})
	id, _, err := m.Start(Spec{Name: "job", Kind: KindJob, JobID: "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	w := m.getWatch(id)
	if w == nil {
		t.Fatal("the watch was not registered")
	}
	if _, err := m.Stop(id); err != nil {
		t.Fatalf("stop: %v", err)
	}
	w.mu.Lock()
	stopped := w.state
	w.mu.Unlock()

	m.handleSampleError(w, errors.New("late sample failure"))

	w.mu.Lock()
	got := w.state
	w.mu.Unlock()
	if got != stopped {
		t.Fatalf("state = %v, want it left at %v", got, stopped)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range events {
		if e.State == StateError {
			t.Fatalf("a retired watch republished %s: %+v", e.State, e)
		}
	}
}
