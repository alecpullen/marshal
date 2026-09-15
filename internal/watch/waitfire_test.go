package watch

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubSampler returns fixed samples so tests drive fire() deterministically
// through sampleOnce.
type stubSampler struct{ sample Sample }

func (s stubSampler) Sample(context.Context, *watch) (Sample, error) { return s.sample, nil }

// scriptedSampler fails on the first failTimes calls, then yields sample.
// It drives the error -> recovery edge deterministically through sampleOnce.
type scriptedSampler struct {
	failTimes int
	err       error
	calls     atomic.Int32
	sample    Sample
}

func (s *scriptedSampler) Sample(context.Context, *watch) (Sample, error) {
	if int(s.calls.Add(1)) <= s.failTimes {
		return Sample{}, s.err
	}
	return s.sample, nil
}

func TestWaitFireWakesOnFire(t *testing.T) {
	m := newTestManager(t, Deps{})
	m.setSampler(stubSampler{sample: Sample{ExitCode: 0, Stdout: "boom"}})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan Info, 1)
	go func() {
		info, err := m.WaitFire(context.Background(), id)
		if err != nil {
			t.Errorf("WaitFire: %v", err)
		}
		done <- info
	}()
	time.Sleep(20 * time.Millisecond)
	m.sampleOnce(m.getWatch(id))
	select {
	case info := <-done:
		if info.State != StateFired || info.FireCount != 1 || info.LastSample != "boom" {
			t.Fatalf("WaitFire info = %+v, want fired/1/boom", info)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitFire did not wake on fire")
	}
}

func TestWaitFireWakesOnStop(t *testing.T) {
	m := newTestManager(t, Deps{})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan Info, 1)
	go func() {
		info, _ := m.WaitFire(context.Background(), id)
		done <- info
	}()
	time.Sleep(20 * time.Millisecond)
	m.Stop(id)
	select {
	case info := <-done:
		if info.State != StateStopped {
			t.Fatalf("state = %s, want stopped", info.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitFire did not wake on stop")
	}
}

func TestWaitFireRepeatNextFire(t *testing.T) {
	m := newTestManager(t, Deps{})
	m.setSampler(stubSampler{sample: Sample{ExitCode: 0, Stdout: "hit"}})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 0", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 1; i <= 2; i++ {
		done := make(chan Info, 1)
		go func() {
			info, _ := m.WaitFire(context.Background(), id)
			done <- info
		}()
		time.Sleep(20 * time.Millisecond)
		m.sampleOnce(m.getWatch(id))
		select {
		case info := <-done:
			if info.FireCount != i {
				t.Fatalf("fire %d: FireCount = %d", i, info.FireCount)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("WaitFire did not wake on fire %d", i)
		}
	}
}

func TestWaitFireAlreadyGone(t *testing.T) {
	m := newTestManager(t, Deps{})
	m.setSampler(stubSampler{sample: Sample{ExitCode: 0, Stdout: "hit"}})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 0", Mode: ModeOnce, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.sampleOnce(m.getWatch(id)) // fires once-mode watch and removes it
	_, err = m.WaitFire(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "already fired or stopped") {
		t.Fatalf("WaitFire = %v, want already-gone error", err)
	}
}

// TestWaitFireWakesOnErrorRecovery pins the recovery edge: StateError is
// not a WaitFire fast-path (the watch may recover), so a waiter parked on an
// errored watch must be released when the next successful sample flips the
// watch back to StateWatching — not left hanging until the next fire/stop.
func TestWaitFireWakesOnErrorRecovery(t *testing.T) {
	m := newTestManager(t, Deps{})
	// One scripted sampling error, then a healthy sample that does not trip
	// the condition. Two errors would auto-stop the watch, which is a
	// different transition.
	samp := &scriptedSampler{
		failTimes: 1,
		err:       errors.New("sample boom"),
		sample:    Sample{ExitCode: 0, Stdout: "recovered"},
	}
	m.setSampler(samp)
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 7", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// First sample errors: the watch enters StateError (1 < MaxConsecutiveErrors,
	// so no auto-stop).
	m.sampleOnce(m.getWatch(id))
	if info, err := m.Status(id); err != nil || info.State != StateError {
		t.Fatalf("state after error sample = %s (%v), want error", info.State, err)
	}
	done := make(chan Info, 1)
	go func() {
		info, err := m.WaitFire(context.Background(), id)
		if err != nil {
			t.Errorf("WaitFire: %v", err)
		}
		done <- info
	}()
	time.Sleep(20 * time.Millisecond)
	// Second sample succeeds: the watch recovers StateError -> StateWatching
	// without firing (exit 0 does not match exit_code 7). The parked waiter
	// must wake at this transition.
	m.sampleOnce(m.getWatch(id))
	select {
	case info := <-done:
		if info.State != StateWatching {
			t.Fatalf("state after recovery = %s, want watching", info.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitFire did not wake on error recovery")
	}
}

// TestWaitFireFastPathFiredRepeat pins the WaitFire fast-path for a repeat
// watch in StateFired that was not stopped: it must return its snapshot
// immediately rather than parking a waiter. Repeat watches never enter
// StateFired via fire() (only once mode does), so this drives a fired
// repeat watch into that state directly — the same shape a fire()/map
// lookup race can expose to WaitFire.
func TestWaitFireFastPathFiredRepeat(t *testing.T) {
	m := newTestManager(t, Deps{})
	m.setSampler(stubSampler{sample: Sample{ExitCode: 0, Stdout: "hit"}})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 0", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.sampleOnce(m.getWatch(id)) // fires once; repeat mode keeps StateWatching
	w := m.getWatch(id)
	w.mu.Lock()
	w.state = StateFired
	w.mu.Unlock()
	info, err := m.WaitFire(context.Background(), id)
	if err != nil {
		t.Fatalf("WaitFire: %v", err)
	}
	if info.State != StateFired || info.FireCount != 1 {
		t.Fatalf("WaitFire info = %+v, want fired/1", info)
	}
}

func TestWaitFireImmediateTerminal(t *testing.T) {
	m := newTestManager(t, Deps{})
	id, _, err := m.Start(Spec{Name: "t", Kind: KindCommand, Command: "x", Condition: "exit_code 0", Mode: ModeRepeat, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Stop(id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// A repeat watch stopped via Stop keeps the state Stopped but is
	// removed from the map, so a later WaitFire reports gone.
	_, err = m.WaitFire(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "already fired or stopped") {
		t.Fatalf("WaitFire = %v, want gone", err)
	}
}
