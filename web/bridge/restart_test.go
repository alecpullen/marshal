package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// restartFleet builds a project fleet with one started project child and a
// marshal binary of its own, so a sweep can stat the binary and check it.
func restartFleet(t *testing.T) (*Fleet, *fakeProjectChild, *projectChild, string) {
	t.Helper()
	f, fakeFor := projectFleet(t)
	ctx := ctlContext(t)
	root := t.TempDir()
	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := fakeFor(pc.root)
	if fake == nil {
		t.Fatal("no fake was built")
	}
	f.marshalBin = p6Script(t, p6Dir(t), "marshal", "#!/bin/sh\necho marshal v2\n")
	return f, fake, pc, pc.root
}

// fakeResult sets one of the fake's fixed answers under its own lock, so a
// test never races the child's serving goroutines.
func fakeResult(fake *fakeProjectChild, method string, value any) {
	fake.mu.Lock()
	fake.results[method] = value
	fake.mu.Unlock()
}

// reportedRestart answers the restart GET /api/daemon reports for root, and
// whether the root was reported at all.
func reportedRestart(t *testing.T, f *Fleet, root string) (*DaemonRestart, bool) {
	t.Helper()
	for _, child := range f.projectChildren() {
		if child.Root == root {
			return child.Restart, true
		}
	}
	return nil, false
}

// staleState is the project/state answer of a child running an old build.
func staleState() map[string]any {
	return map[string]any{"stale": []string{"a new marshal build"}, "version": "v1"}
}

// TestAStaleIdleChildIsRestarted: a stale child nobody is using is restarted
// once, on the new build, with one note before it.
func TestAStaleIdleChildIsRestarted(t *testing.T) {
	f, fake, pc, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", staleState())
	var checks int
	f.checkBuild = func(ctx context.Context, bin string) (string, error) {
		checks++
		return "v2", nil
	}
	sid, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 2 {
		t.Fatalf("generations = %d, want 2", gen)
	}
	if checks != 1 {
		t.Errorf("build checks = %d, want 1", checks)
	}
	notes := fake.calls("project/note")
	if len(notes) != 1 {
		t.Fatalf("project/note sent %d times, want 1: %v", len(notes), notes)
	}
	if text, _ := paramsOf(t, notes[0])["text"].(string); text != "Restarting on v2: a new marshal build." {
		t.Errorf("project/note text = %q", text)
	}
	// The restart resumes the sessions it interrupted.
	deadline := time.Now().Add(10 * time.Second)
	for !resumedSessions(fake)[sid] {
		if time.Now().After(deadline) {
			t.Fatalf("session %s was not resumed after the restart", sid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if restart, ok := reportedRestart(t, f, root); !ok || restart != nil {
		t.Fatalf("restart = %+v (reported %v), want nil", restart, ok)
	}
}

// TestACurrentChildIsLeftAlone: a current child is neither checked nor
// restarted, and nothing is said to its sessions.
func TestACurrentChildIsLeftAlone(t *testing.T) {
	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", map[string]any{"version": "v1"})
	var checks int
	f.checkBuild = func(ctx context.Context, bin string) (string, error) {
		checks++
		return "v2", nil
	}

	f.SweepRestarts(ctx, time.Now())

	if checks != 0 {
		t.Errorf("build checks = %d, want none", checks)
	}
	if notes := fake.calls("project/note"); len(notes) != 0 {
		t.Errorf("project/note sent %d times, want none: %v", len(notes), notes)
	}
	if signals := fake.signalsSeen(); signals != 0 {
		t.Errorf("stop signals = %d, want none", signals)
	}
	if gen := fake.generations(); gen != 1 {
		t.Errorf("generations = %d, want 1", gen)
	}
	if restart, ok := reportedRestart(t, f, root); !ok || restart != nil {
		t.Errorf("restart = %+v, want nil", restart)
	}
}

// TestABusyChildWaits: a child with a run in flight is not restarted, and the
// restart it holds says what it waits for.
func TestABusyChildWaits(t *testing.T) {
	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", map[string]any{
		"stale":   []string{"a new marshal build"},
		"busy":    []string{"a run in s-1"},
		"version": "v1",
	})
	f.checkBuild = func(ctx context.Context, bin string) (string, error) {
		t.Error("the build was checked for a child that is busy")
		return "v2", nil
	}

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 1 {
		t.Errorf("generations = %d, want the busy child kept", gen)
	}
	restart, ok := reportedRestart(t, f, root)
	if !ok || restart == nil {
		t.Fatal("no restart was reported for the busy child")
	}
	if restart.WaitingFor != "a run in s-1" {
		t.Errorf("waitingFor = %q, want %q", restart.WaitingFor, "a run in s-1")
	}
	if want := []string{"a new marshal build"}; !reflect.DeepEqual(restart.Reasons, want) {
		t.Errorf("reasons = %v, want %v", restart.Reasons, want)
	}
}

// TestAViewedChildWaitsForAQuietMoment: a child someone is watching and was
// just active in waits for a quiet moment; one quiet by now restarts.
func TestAViewedChildWaitsForAQuietMoment(t *testing.T) {
	f, fake, pc, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", staleState())
	f.checkBuild = func(ctx context.Context, bin string) (string, error) { return "v2", nil }
	sid, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if !pc.reg.attachViewer(sid) {
		t.Fatal("the session was not tracked")
	}
	pc.reg.touch(sid)
	now := time.Now()

	f.SweepRestarts(ctx, now)

	if gen := fake.generations(); gen != 1 {
		t.Fatalf("generations = %d, want the viewed child kept", gen)
	}
	restart, ok := reportedRestart(t, f, root)
	if !ok || restart == nil {
		t.Fatal("no restart was reported for the viewed child")
	}
	if restart.WaitingFor != waitingForQuiet {
		t.Errorf("waitingFor = %q, want %q", restart.WaitingFor, waitingForQuiet)
	}

	f.SweepRestarts(ctx, now.Add(3*time.Minute))

	if gen := fake.generations(); gen != 2 {
		t.Fatalf("generations = %d, want the child restarted once quiet", gen)
	}
}

// TestAChildWithNoViewerNeedsNoQuietPeriod: a session nobody is watching does
// not hold the restart back, however recently it ran.
func TestAChildWithNoViewerNeedsNoQuietPeriod(t *testing.T) {
	f, fake, pc, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", staleState())
	f.checkBuild = func(ctx context.Context, bin string) (string, error) { return "v2", nil }
	sid, err := pc.reg.New(ctx, AgentPath(root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	pc.reg.touch(sid)

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 2 {
		t.Fatalf("generations = %d, want the unwatched child restarted", gen)
	}
}

// TestARefusedBuildIsNotInstalled: a build that cannot start is never
// installed, its failure is said once, and only a new file is checked again.
func TestARefusedBuildIsNotInstalled(t *testing.T) {
	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", staleState())
	var checks int
	f.checkBuild = func(ctx context.Context, bin string) (string, error) {
		checks++
		return "", errors.New("broken build")
	}

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 1 {
		t.Fatalf("generations = %d, want the refused child kept", gen)
	}
	notes := fake.calls("project/note")
	if len(notes) != 1 {
		t.Fatalf("project/note sent %d times, want 1: %v", len(notes), notes)
	}
	want := "A new marshal build could not start (broken build); this project stays on v1."
	if text, _ := paramsOf(t, notes[0])["text"].(string); text != want {
		t.Errorf("project/note text =\n%q\nwant\n%q", text, want)
	}
	restart, ok := reportedRestart(t, f, root)
	if !ok || restart == nil {
		t.Fatal("no restart was reported for the refused build")
	}
	if restart.Refused != "broken build" {
		t.Errorf("refused = %q, want %q", restart.Refused, "broken build")
	}

	// The same file is not checked again, so nothing more is said.
	f.SweepRestarts(ctx, time.Now())
	if checks != 1 {
		t.Errorf("build checks = %d, want the refused file checked once", checks)
	}
	if notes := fake.calls("project/note"); len(notes) != 1 {
		t.Errorf("project/note sent %d times, want still 1: %v", len(notes), notes)
	}

	// A new build on disk is a new identity, so it is checked again.
	if err := os.WriteFile(f.marshalBin, []byte("#!/bin/sh\necho marshal v3, a longer build\n"), 0o755); err != nil {
		t.Fatalf("rewrite the marshal binary: %v", err)
	}
	f.SweepRestarts(ctx, time.Now())
	if checks != 2 {
		t.Errorf("build checks = %d, want the new file checked", checks)
	}
}

// TestABuildThatGoesBusyWaits: a child that goes busy between the state and
// the note keeps its restart for the next sweep (decision 2).
func TestABuildThatGoesBusyWaits(t *testing.T) {
	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	var asked int32
	fake.mu.Lock()
	fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		if method != "project/state" {
			return nil, nil, false
		}
		if atomic.AddInt32(&asked, 1) == 1 {
			return staleState(), nil, true
		}
		return map[string]any{
			"stale":   []string{"a new marshal build"},
			"busy":    []string{"a run in s-1"},
			"version": "v1",
		}, nil, true
	}
	fake.mu.Unlock()
	f.checkBuild = func(ctx context.Context, bin string) (string, error) { return "v2", nil }

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 1 {
		t.Errorf("generations = %d, want the child that went busy kept", gen)
	}
	for _, params := range fake.calls("project/note") {
		if text, _ := paramsOf(t, params)["text"].(string); strings.Contains(text, "Restarting") {
			t.Errorf("a restart note was sent to a child that went busy: %q", text)
		}
	}
	restart, ok := reportedRestart(t, f, root)
	if !ok || restart == nil {
		t.Fatal("no restart was reported")
	}
	if restart.WaitingFor != "a run in s-1" {
		t.Errorf("waitingFor = %q, want %q", restart.WaitingFor, "a run in s-1")
	}
}

// TestAnUnansweredStateSkipsTheChild: a state the bridge could not read skips
// the child and leaves the view the last sweep had of it.
func TestAnUnansweredStateSkipsTheChild(t *testing.T) {
	f, fake, pc, root := restartFleet(t)
	ctx := ctlContext(t)
	before := &DaemonRestart{Reasons: []string{"a new marshal build"}, WaitingFor: waitingForQuiet}
	pc.mu.Lock()
	pc.restart = before
	pc.mu.Unlock()

	fake.mu.Lock()
	fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		if method != "project/state" {
			return nil, nil, false
		}
		return nil, &rpcError{Code: -32000, Message: "no state"}, true
	}
	fake.mu.Unlock()

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 1 {
		t.Errorf("generations = %d, want the child kept", gen)
	}
	if notes := fake.calls("project/note"); len(notes) != 0 {
		t.Errorf("project/note sent %d times, want none: %v", len(notes), notes)
	}
	restart, ok := reportedRestart(t, f, root)
	if !ok || restart == nil {
		t.Fatal("the previous restart was dropped")
	}
	if !reflect.DeepEqual(restart, before) {
		t.Errorf("restart = %+v, want the previous %+v", restart, before)
	}
}

// TestAnAttachWaitsForTheRestart: an attach that arrives while a restart is in
// flight waits for it, and reaches the child only once the new generation is
// initialized (decision 4).
func TestAnAttachWaitsForTheRestart(t *testing.T) {
	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", staleState())

	released := make(chan struct{})
	checkEntered := make(chan struct{}, 1)
	releaseCheck := sync.OnceFunc(func() { close(released) })
	defer releaseCheck()
	f.checkBuild = func(ctx context.Context, bin string) (string, error) {
		checkEntered <- struct{}{}
		<-released
		return "v2", nil
	}

	// The new generation's handshake is held open, so the attach below lands
	// in the middle of the restart rather than after it.
	initEntered := make(chan struct{}, 1)
	initDone := make(chan struct{})
	releaseInit := sync.OnceFunc(func() { close(initDone) })
	defer releaseInit()
	fake.mu.Lock()
	fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		if method == "initialize" {
			initEntered <- struct{}{}
			<-initDone
		}
		return nil, nil, false
	}
	fake.mu.Unlock()

	swept := make(chan struct{})
	go func() {
		defer close(swept)
		f.SweepRestarts(ctx, time.Now())
	}()
	<-checkEntered
	releaseCheck()
	<-initEntered

	attached := make(chan error, 1)
	go func() {
		pc, err := f.projectChildFor(ctx, root)
		if err != nil {
			attached <- err
			return
		}
		_, err = pc.reg.New(ctx, AgentPath(root), "")
		attached <- err
	}()
	select {
	case err := <-attached:
		t.Fatalf("the attach returned during the restart: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseInit()
	if err := <-attached; err != nil {
		t.Fatalf("attach: %v", err)
	}
	<-swept

	// By the fake's frame order, the attach's session followed the new
	// generation's initialize.
	lastInit, firstAttach := -1, -1
	for i, rec := range fake.record() {
		switch rec.frame.method {
		case "initialize":
			lastInit = i
		case "session/new":
			if firstAttach < 0 {
				firstAttach = i
			}
		}
	}
	if lastInit < 0 || firstAttach < 0 {
		t.Fatalf("initialize at %d, session/new at %d", lastInit, firstAttach)
	}
	if firstAttach < lastInit {
		t.Fatal("the attach opened its session before the new generation was initialized")
	}
}

// TestARestartThatDoesNotComeBackDropsTheChild: a restart whose new generation
// never starts leaves no child behind (decision 7).
func TestARestartThatDoesNotComeBackDropsTheChild(t *testing.T) {
	old := recycleTimeout
	recycleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { recycleTimeout = old })

	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	fakeResult(fake, "project/state", staleState())
	f.checkBuild = func(ctx context.Context, bin string) (string, error) { return "v2", nil }
	// The replacement generation cannot start.
	fake.mu.Lock()
	fake.openFailFrom = 2
	fake.mu.Unlock()

	f.SweepRestarts(ctx, time.Now())

	if _, ok := projectEntry(f, root); ok {
		t.Fatal("a child whose restart did not come back was kept")
	}
	fake.waitStopped(t, 5*time.Second)
}

// TestThePinIsHeldAgainAfterARestart: a restarted generation has forgotten the
// hold, so the sweep's restart holds the pinned root again.
func TestThePinIsHeldAgainAfterARestart(t *testing.T) {
	f, fake, _, root := restartFleet(t)
	ctx := ctlContext(t)
	if _, err := f.Pin(ctx, root, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	before := len(fake.calls("project/hold"))
	fakeResult(fake, "project/state", staleState())
	f.checkBuild = func(ctx context.Context, bin string) (string, error) { return "v2", nil }

	f.SweepRestarts(ctx, time.Now())

	if gen := fake.generations(); gen != 2 {
		t.Fatalf("generations = %d, want 2", gen)
	}
	if holds := fake.calls("project/hold"); len(holds) <= before {
		t.Fatalf("project/hold sent %d times, want one more than %d", len(holds), before)
	}
	lastInit, lastHold := -1, -1
	for i, rec := range fake.record() {
		switch rec.frame.method {
		case "initialize":
			lastInit = i
		case "project/hold":
			lastHold = i
		}
	}
	if lastInit < 0 || lastHold < 0 {
		t.Fatalf("initialize at %d, project/hold at %d", lastInit, lastHold)
	}
	if lastHold < lastInit {
		t.Fatal("the restarted child held its root before it was initialized")
	}
}

// TestBuildVersionOf: the version is --version's second field, else its whole
// first line (decision 9).
func TestBuildVersionOf(t *testing.T) {
	cases := []struct{ stdout, want string }{
		{"marshal v1.2.3 (commit abcdef0)\n", "v1.2.3"},
		{"webbridge dev+abc\n", "dev+abc"},
		{"v1.0.0", "v1.0.0"},
		{"\n", ""},
	}
	for _, c := range cases {
		if got := buildVersionOf(c.stdout); got != c.want {
			t.Errorf("buildVersionOf(%q) = %q, want %q", c.stdout, got, c.want)
		}
	}
}

// TestAChildThatGoesAwayBeforeTheRestartLeavesNoMarker: a child that is gone by
// the time the sweep means to recycle it leaves no recycling marker open, and
// no ready flag set. A marker nothing closes would make every later attach wait
// on it until its own context ends.
func TestAChildThatGoesAwayBeforeTheRestartLeavesNoMarker(t *testing.T) {
	f, _, pc, _ := restartFleet(t)
	ctx := ctlContext(t)

	// The child went away between the sweep's checks and the note: a linger
	// timer's stop, or a failed start's cleanup, leaves the entry like this.
	pc.mu.Lock()
	pc.child, pc.ready = nil, false
	pc.mu.Unlock()

	f.recycleProjectChild(ctx, pc, projectState{Stale: []string{"a new marshal build"}}, "v2")

	pc.mu.Lock()
	recycling, ready := pc.recycling, pc.ready
	pc.mu.Unlock()
	if recycling != nil {
		t.Fatal("the restart left a recycling marker open: every later attach would wait on it")
	}
	if ready {
		t.Error("the child is reported ready with nothing running")
	}
	// The sweep goes on: a child left in this state is skipped, not wedged.
	done := make(chan struct{})
	go func() {
		f.SweepRestarts(ctx, time.Now())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("a sweep after the abandoned restart did not return")
	}
}
