//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeClock is a deterministic stand-in for time.Now/time.Sleep: sleep advances
// the clock by exactly the requested duration and records the request, so a
// timeout of any length is exercised without wall-clock waiting.
type fakeClock struct {
	t      time.Time
	sleeps []time.Duration
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(d time.Duration) {
	c.t = c.t.Add(d)
	c.sleeps = append(c.sleeps, d)
}

// groupProbe returns a probe that replays results in order and reports the
// group ids it was asked about. The last result repeats once the script is
// exhausted.
func groupProbe(results []error, probed *[]int) func(int) error {
	calls := 0
	return func(pgid int) error {
		*probed = append(*probed, pgid)
		err := results[len(results)-1]
		if calls < len(results) {
			err = results[calls]
		}
		calls++
		return err
	}
}

// probeBound is the largest number of probes a bounded wait may perform for the
// given timeout: one per poll interval, plus the probe taken at the deadline.
func probeBound(timeout, interval time.Duration) int {
	return int(timeout/interval) + 2
}

// A group that cannot be signalled yet but disappears shortly after must be
// reported gone, not failed: EPERM during leader reaping is transient.
func TestWaitForGroupGoneTransientEPERM(t *testing.T) {
	const timeout = 50 * time.Millisecond
	var probed []int
	clock := &fakeClock{}
	probe := groupProbe([]error{syscall.EPERM, syscall.EPERM, syscall.ESRCH}, &probed)

	if err := waitForGroupGone(4321, timeout, probe, clock.now, clock.sleep); err != nil {
		t.Fatalf("waitForGroupGone = %v, want nil after EPERM then ESRCH", err)
	}
	if len(probed) != 3 {
		t.Fatalf("probed %d times (%v), want 3", len(probed), probed)
	}
	if len(clock.sleeps) != 2 {
		t.Fatalf("slept %d times (%v), want 2", len(clock.sleeps), clock.sleeps)
	}
	for _, d := range clock.sleeps {
		if d != processGroupPollInterval {
			t.Fatalf("slept %s, want the production poll interval %s", d, processGroupPollInterval)
		}
	}
}

// A group that stays inaccessible is not gone. The final EPERM must remain
// visible through errors.Is and the message must carry the group id, so the
// failure can be diagnosed.
func TestWaitForGroupGonePersistentEPERM(t *testing.T) {
	const timeout = 20 * time.Millisecond
	var probed []int
	clock := &fakeClock{}
	probe := groupProbe([]error{syscall.EPERM}, &probed)

	err := waitForGroupGone(9876, timeout, probe, clock.now, clock.sleep)
	if err == nil {
		t.Fatal("waitForGroupGone = nil for a group that never became signalable")
	}
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("waitForGroupGone error = %v, want it to wrap syscall.EPERM", err)
	}
	if !strings.Contains(err.Error(), "9876") {
		t.Fatalf("waitForGroupGone error = %q, want it to name group 9876", err)
	}

	// The timeout is bounded by the injected clock, not by how long the group
	// stays inaccessible: probe count and sleep count must stay small.
	if max := probeBound(timeout, processGroupPollInterval); len(probed) > max {
		t.Fatalf("probed %d times, want at most %d", len(probed), max)
	}
	if max := probeBound(timeout, processGroupPollInterval) - 1; len(clock.sleeps) > max {
		t.Fatalf("slept %d times, want at most %d", len(clock.sleeps), max)
	}
	if got, want := len(clock.sleeps), len(probed)-1; got != want {
		t.Fatalf("slept %d times for %d probes, want %d", got, len(probed), want)
	}
}

// A group with live members is only ever an error. It must never be reported
// gone, and the wait must stop at the deadline.
func TestWaitForGroupGoneLiveUntilTimeout(t *testing.T) {
	const timeout = 20 * time.Millisecond
	var probed []int
	clock := &fakeClock{}
	probe := groupProbe([]error{nil}, &probed)

	err := waitForGroupGone(555, timeout, probe, clock.now, clock.sleep)
	if err == nil {
		t.Fatal("waitForGroupGone = nil for a group with live members")
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ESRCH) {
		t.Fatalf("waitForGroupGone error = %v, want a plain timeout error", err)
	}
	if !strings.Contains(err.Error(), "still has live members") {
		t.Fatalf("waitForGroupGone error = %q, want the live-members timeout", err)
	}
	if max := probeBound(timeout, processGroupPollInterval); len(probed) > max {
		t.Fatalf("probed %d times, want at most %d", len(probed), max)
	}
	if got, want := len(clock.sleeps), len(probed)-1; got != want {
		t.Fatalf("slept %d times for %d probes, want %d", got, len(probed), want)
	}
	// The injected clock only moves by the requested sleeps, so the whole wait
	// is bounded by the poll interval, never by wall-clock time.
	if want := time.Duration(len(clock.sleeps)) * processGroupPollInterval; clock.t.Sub(time.Time{}) != want {
		t.Fatalf("fake clock advanced to %s, want %s", clock.t.Sub(time.Time{}), want)
	}
}

// An error that says nothing about the group must surface immediately: waiting
// it out would only hide the problem.
func TestWaitForGroupGoneUnexpectedError(t *testing.T) {
	const timeout = time.Second
	boom := fmt.Errorf("probe backend exploded: %w", syscall.EINVAL)
	var probed []int
	clock := &fakeClock{}
	probe := groupProbe([]error{boom}, &probed)

	err := waitForGroupGone(777, timeout, probe, clock.now, clock.sleep)
	if err == nil {
		t.Fatal("waitForGroupGone = nil for an unexpected probe error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("waitForGroupGone error = %v, want it to wrap the probe error", err)
	}
	if errors.Is(err, syscall.EPERM) {
		t.Fatalf("waitForGroupGone error = %v, want the probe error, not EPERM", err)
	}
	if len(probed) != 1 {
		t.Fatalf("probed %d times (%v), want exactly 1", len(probed), probed)
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("slept %d times (%v), want none", len(clock.sleeps), clock.sleeps)
	}
}

// No group at all is trivially gone, and no zero or negative group may ever be
// signalled: the probe takes a positive pgid because callers negate it.
func TestWaitForGroupGoneNoGroupSignalled(t *testing.T) {
	for _, pgid := range []int{0, -1, -4242} {
		var probed []int
		clock := &fakeClock{}
		probe := groupProbe([]error{nil}, &probed)

		if err := waitForGroupGone(pgid, time.Second, probe, clock.now, clock.sleep); err != nil {
			t.Fatalf("waitForGroupGone(%d) = %v, want nil", pgid, err)
		}
		if len(probed) != 0 {
			t.Fatalf("waitForGroupGone(%d) probed %v, want no probe", pgid, probed)
		}
		if len(clock.sleeps) != 0 {
			t.Fatalf("waitForGroupGone(%d) slept %v, want no sleep", pgid, clock.sleeps)
		}
	}

	// A real wait must hand the probe a positive group id, never the negation.
	var probed []int
	clock := &fakeClock{}
	probe := groupProbe([]error{syscall.ESRCH}, &probed)
	if err := waitForGroupGone(1234, time.Second, probe, clock.now, clock.sleep); err != nil {
		t.Fatalf("waitForGroupGone(1234) = %v, want nil", err)
	}
	if len(probed) != 1 || probed[0] != 1234 {
		t.Fatalf("probe received %v, want [1234]", probed)
	}
}
