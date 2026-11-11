package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helperShell is the POSIX shell the supervisor's tests script with.
const helperShell = "/bin/sh"

// requireHelperShell skips a supervisor test where /bin/sh does not exist:
// every script these tests run is a POSIX shell script.
func requireHelperShell(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(helperShell); err != nil {
		t.Skipf("%s is not available: %v", helperShell, err)
	}
}

// scriptArgv is one shell script as a Helper's argv.
func scriptArgv(script string) []string {
	return []string{helperShell, "-c", script}
}

// helperRun is a Helper running in the background under a cancellable
// context.
type helperRun struct {
	helper *Helper
	cancel context.CancelFunc
	done   chan struct{}
}

// runHelper starts h and stops it when the test ends.
func runHelper(t *testing.T, h *Helper) *helperRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &helperRun{helper: h, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		h.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(30 * time.Second):
			t.Errorf("the helper did not stop")
		}
	})
	return r
}

// waitPID waits until the helper reports a process ID.
func (r *helperRun) waitPID(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if pid, _ := r.helper.State(); pid != 0 {
			return pid
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the helper reported no process ID")
	return 0
}

// TestTheHelperRuns: a helper that stays up reports its process ID, keeps
// it, and is not counted as restarting.
func TestTheHelperRuns(t *testing.T) {
	requireHelperShell(t)
	h := &Helper{Argv: scriptArgv("sleep 5"), Backoff: 10 * time.Millisecond}
	r := runHelper(t, h)

	pid := r.waitPID(t)
	// Several backoffs pass with the helper still up, so a supervisor that
	// restarted a live helper would be caught here.
	time.Sleep(50 * time.Millisecond)
	again, restarts := h.State()
	if again != pid {
		t.Fatalf("pid = %d after the wait, want the first pid %d", again, pid)
	}
	if restarts != 0 {
		t.Fatalf("restarts = %d while the helper ran, want 0", restarts)
	}
}

// TestAnExitingHelperIsRestarted: a helper that exits at once is restarted
// with growing delays capped at MaxBackoff, and its restart count rises.
func TestAnExitingHelperIsRestarted(t *testing.T) {
	requireHelperShell(t)
	h := &Helper{
		Argv:       scriptArgv("exit 0"),
		Backoff:    40 * time.Millisecond,
		MaxBackoff: 160 * time.Millisecond,
		SteadyRun:  time.Hour, // every run ends too fast to reset the delay
	}
	runHelper(t, h)

	// The times at which the restart count rises: the gaps between them are
	// the delays the supervisor waited.
	var stamps []time.Time
	began := time.Now()
	seen := 0
	for len(stamps) < 4 && time.Since(began) < 20*time.Second {
		if _, restarts := h.State(); restarts > seen {
			seen = restarts
			stamps = append(stamps, time.Now())
		}
		time.Sleep(time.Millisecond)
	}
	if len(stamps) < 4 {
		t.Fatalf("the helper restarted %d times, want 4", len(stamps))
	}

	var gaps []time.Duration
	for i := 1; i < len(stamps); i++ {
		gaps = append(gaps, stamps[i].Sub(stamps[i-1]))
	}
	// Backoff, twice Backoff, MaxBackoff: the delay doubles and then holds.
	for i, gap := range gaps {
		if gap < h.Backoff {
			t.Fatalf("gap %d = %s, shorter than Backoff %s", i, gap, h.Backoff)
		}
		if gap > h.MaxBackoff+150*time.Millisecond {
			t.Fatalf("gap %d = %s, not capped at MaxBackoff %s", i, gap, h.MaxBackoff)
		}
	}
	if last := gaps[len(gaps)-1]; last < 2*gaps[0] {
		t.Fatalf("gaps did not grow: %v", gaps)
	}
	if _, restarts := h.State(); restarts < len(stamps) {
		t.Fatalf("restarts = %d, want at least %d", restarts, len(stamps))
	}
}

// waitFile waits until path appears.
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
}

// TestTheHelperStopsWithTheContext: cancelling the context sends SIGTERM to
// a helper that traps it and exits, and Run returns before StopTimeout.
func TestTheHelperStopsWithTheContext(t *testing.T) {
	requireHelperShell(t)
	marker := filepath.Join(t.TempDir(), "term")
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("HELPER_TERM_MARKER", marker)
	t.Setenv("HELPER_READY", ready)
	// The helper signals that its trap is in place: a SIGTERM before that
	// would kill the shell by default and prove nothing.
	script := "trap 'printf term > \"$HELPER_TERM_MARKER\"; exit 0' TERM\n" +
		"printf ready > \"$HELPER_READY\"\n" +
		"while :; do sleep 0.02; done\n"
	h := &Helper{
		Argv:        scriptArgv(script),
		StopTimeout: 20 * time.Second, // the trap, not the kill, ends it
	}
	r := runHelper(t, h)
	waitFile(t, ready)

	began := time.Now()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the context ended")
	}
	if took := time.Since(began); took >= h.StopTimeout {
		t.Fatalf("Run took %s, at or past StopTimeout %s", took, h.StopTimeout)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the helper did not run its SIGTERM trap: %v", err)
	}
}

// TestAHelperIgnoringSIGTERMIsKilled: a helper that ignores SIGTERM is
// killed once StopTimeout passes, and Run still returns.
func TestAHelperIgnoringSIGTERMIsKilled(t *testing.T) {
	requireHelperShell(t)
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("HELPER_READY", ready)
	// The helper signals that it ignores SIGTERM already: a SIGTERM before
	// that would kill the shell by default and prove nothing.
	script := "trap '' TERM\n" +
		"printf ready > \"$HELPER_READY\"\n" +
		"while :; do sleep 0.02; done\n"
	h := &Helper{
		Argv:        scriptArgv(script),
		StopTimeout: 200 * time.Millisecond,
	}
	r := runHelper(t, h)
	waitFile(t, ready)

	began := time.Now()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the helper ignored SIGTERM")
	}
	if took := time.Since(began); took < h.StopTimeout {
		t.Fatalf("Run returned after %s, before StopTimeout %s: the helper "+
			"ignoring SIGTERM was not killed", took, h.StopTimeout)
	}
}

// TestTheDaemonReportsItsHelper: GET /api/daemon answers the running
// helper's process ID, and leaves the key out when there is no helper.
func TestTheDaemonReportsItsHelper(t *testing.T) {
	requireHelperShell(t)
	s := daemonTestServer(t)
	h := &Helper{Argv: scriptArgv("sleep 5")}
	r := runHelper(t, h)
	pid := r.waitPID(t)
	s.SetRefreshHelper(h)

	rec := viaHandler(t, s.SocketHandler(), http.MethodGet, "/api/daemon")
	if rec.Code != http.StatusOK {
		t.Fatalf("socket /api/daemon status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got DaemonInfo
	decodeBody(t, rec, &got)
	if got.Refresh == nil {
		t.Fatalf("/api/daemon reported no refresh helper: %s", rec.Body.String())
	}
	if got.Refresh.PID != pid || got.Refresh.Restarts != 0 {
		t.Fatalf("refresh = %+v, want pid %d and no restarts", *got.Refresh, pid)
	}

	// A daemon with no helper leaves the key out entirely.
	plain := daemonTestServer(t)
	rec = viaHandler(t, plain.SocketHandler(), http.MethodGet, "/api/daemon")
	var fields map[string]json.RawMessage
	decodeBody(t, rec, &fields)
	if _, ok := fields["refresh"]; ok {
		t.Fatalf("a daemon with no helper reported refresh: %s", rec.Body.String())
	}
}
