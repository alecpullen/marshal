package watch

import (
	"context"
	"strings"
	"testing"
	"time"
)

// stubSampler returns fixed samples so tests drive fire() deterministically
// through sampleOnce.
type stubSampler struct{ sample Sample }

func (s stubSampler) Sample(context.Context, *watch) (Sample, error) { return s.sample, nil }

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
