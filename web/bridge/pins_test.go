package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pinRoots returns the root each request of method carried, in order.
func pinRoots(t *testing.T, fake *fakeProjectChild, method string) []string {
	t.Helper()
	var out []string
	for _, params := range fake.calls(method) {
		var p struct {
			Root string `json:"root"`
		}
		if err := json.Unmarshal([]byte(params), &p); err != nil {
			t.Fatalf("decode %s params %q: %v", method, params, err)
		}
		out = append(out, p.Root)
	}
	return out
}

// TestPinningStartsAndHoldsTheChild: pinning a root with no child starts one
// and holds it on the child's own stdio stream.
func TestPinningStartsAndHoldsTheChild(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	ctx := ctlContext(t)
	root := t.TempDir()

	state, err := f.Pin(ctx, root, true)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if !state.Pinned || !state.Running {
		t.Fatalf("Pin answered %+v, want pinned and running", state)
	}
	if want := filepath.Clean(root); state.Root != want {
		t.Fatalf("Pin answered root %q, want %q", state.Root, want)
	}

	fake := fakeFor(filepath.Clean(root))
	if fake == nil {
		t.Fatal("pinning the root started no child")
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("generations = %d, want 1", gens)
	}
	holds := pinRoots(t, fake, "project/hold")
	if len(holds) == 0 {
		t.Fatal("the pinned child was never held")
	}
	for _, got := range holds {
		if got != filepath.Clean(root) {
			t.Fatalf("project/hold root = %q, want %q", got, filepath.Clean(root))
		}
	}
	// The hold belongs to the child's own stream, not to a viewer's.
	onStdio := false
	for _, frame := range fake.framesOn(stdioConn) {
		if frame.method == "project/hold" {
			onStdio = true
		}
	}
	if !onStdio {
		t.Fatal("project/hold did not arrive on the child's stdio stream")
	}
}

// TestAPinnedChildArmsNoLinger: a pinned root's child is kept by the pin, so
// nothing arms a linger for it — not the start, and not a re-arm after an
// attach that left no session behind.
func TestAPinnedChildArmsNoLinger(t *testing.T) {
	f, _ := projectFleet(t)
	shortenLinger(t, f)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	if _, err := f.Pin(ctx, root, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if n := timers.count(); n != 0 {
		t.Fatalf("armed timers = %d, want none for a pinned root", n)
	}

	pc, ok := projectEntry(f, filepath.Clean(root))
	if !ok {
		t.Fatal("the pinned child is not registered")
	}
	// An attach that opens and closes a session leaves the child empty, which
	// is exactly the case the linger exists for.
	sid, err := pc.reg.New(ctx, AgentPath(pc.root), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	if err := pc.reg.Close(ctx, sid); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	f.rearmLinger(pc)
	if n := timers.count(); n != 0 {
		t.Fatalf("armed timers = %d, want none after a re-arm on a pinned root", n)
	}
}

// TestPinningARunningChildHoldsIt: a child an attach already started has not
// held its root, so the pin holds it.
func TestPinningARunningChildHoldsIt(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
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
	if holds := fake.calls("project/hold"); len(holds) != 0 {
		t.Fatalf("an unpinned child was held: %v", holds)
	}

	if _, err := f.Pin(ctx, root, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	holds := pinRoots(t, fake, "project/hold")
	if len(holds) != 1 || holds[0] != pc.root {
		t.Fatalf("project/hold roots = %v, want exactly %q", holds, pc.root)
	}
	if gens := fake.generations(); gens != 1 {
		t.Fatalf("generations = %d, want the running child to be kept", gens)
	}
}

// TestARestartedPinnedChildHoldsAgain: a restarted generation has forgotten
// the hold, so it holds the root again after its own initialize.
func TestARestartedPinnedChildHoldsAgain(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	ctx := ctlContext(t)
	root := t.TempDir()

	if _, err := f.Pin(ctx, root, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	fake := fakeFor(filepath.Clean(root))
	if fake == nil {
		t.Fatal("no fake was built")
	}
	before := len(fake.calls("project/hold"))

	fake.die()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if fake.generations() >= 2 && len(fake.calls("project/hold")) > before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restarted child did not hold again: generations = %d, holds = %v",
				fake.generations(), fake.calls("project/hold"))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The hold must follow the restarted generation's own initialize.
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

// TestUnpinningReleasesAndLingers: unpinning releases the root and puts the
// linger back, so the child is reaped once it is empty.
func TestUnpinningReleasesAndLingers(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	if _, err := f.Pin(ctx, root, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	fake := fakeFor(filepath.Clean(root))
	if fake == nil {
		t.Fatal("no fake was built")
	}

	state, err := f.Pin(ctx, root, false)
	if err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if state.Pinned || !state.Running {
		t.Fatalf("unpin answered %+v, want not pinned and running", state)
	}
	releases := pinRoots(t, fake, "project/release")
	if len(releases) != 1 || releases[0] != filepath.Clean(root) {
		t.Fatalf("project/release roots = %v, want exactly %q", releases, filepath.Clean(root))
	}
	if n := timers.count(); n != 1 {
		t.Fatalf("armed timers = %d, want one linger after the unpin", n)
	}

	timers.at(0).fire()

	fake.waitStopped(t, 5*time.Second)
	if !fake.wasStopped() {
		t.Fatal("the lingered child was not stopped")
	}
	if entry, ok := projectEntry(f, filepath.Clean(root)); ok {
		t.Fatalf("the lingered child is still registered: %+v", entry)
	}
}

// TestAFailedHoldKeepsThePin: a hold the agent refuses fails the pin, and the
// root stays pinned so the next start tries again (decision 4).
func TestAFailedHoldKeepsThePin(t *testing.T) {
	f, _ := projectFleetWith(t, func(fake *fakeProjectChild) {
		fake.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
			if method == "project/hold" {
				return nil, &rpcError{Code: -32000, Message: "hold refused"}, true
			}
			return nil, nil, false
		}
	})
	shortenLinger(t, f)
	ctx := ctlContext(t)
	root := t.TempDir()

	_, err := f.Pin(ctx, root, true)
	if err == nil {
		t.Fatal("a failed hold was reported as a pin")
	}
	if !strings.Contains(err.Error(), "hold refused") {
		t.Fatalf("error = %v, want the agent's refusal", err)
	}
	if !f.ws.IsPinned(filepath.Clean(root)) {
		t.Fatal("the root is not pinned after a failed hold")
	}
}

// TestStartPinnedStartsEveryPinnedChild: every pinned root that is still a
// directory gets a child that holds it, and one that is gone is skipped.
func TestStartPinnedStartsEveryPinnedChild(t *testing.T) {
	f, fakeFor := projectFleet(t)
	shortenLinger(t, f)
	ctx := ctlContext(t)
	roots := []string{t.TempDir(), t.TempDir()}
	gone := filepath.Join(t.TempDir(), "removed")
	for _, root := range append(append([]string(nil), roots...), gone) {
		if err := f.ws.SetPinned(root, true); err != nil {
			t.Fatalf("SetPinned %s: %v", root, err)
		}
	}

	f.StartPinned(ctx)

	for _, root := range roots {
		clean := filepath.Clean(root)
		fake := fakeFor(clean)
		if fake == nil {
			t.Fatalf("StartPinned started no child for %s", root)
		}
		holds := pinRoots(t, fake, "project/hold")
		if len(holds) != 1 || holds[0] != clean {
			t.Fatalf("project/hold roots for %s = %v, want exactly %q", root, holds, clean)
		}
	}
	if fake := fakeFor(filepath.Clean(gone)); fake != nil {
		t.Fatal("StartPinned started a child for a root that is not a directory")
	}
}

// TestAPinNeedsADirectory: a pin of a root that is not an existing directory
// is refused, while the unpin of the same root is not.
func TestAPinNeedsADirectory(t *testing.T) {
	f, _ := projectFleet(t)
	ctx := ctlContext(t)
	missing := filepath.Join(t.TempDir(), "gone")

	if _, err := f.Pin(ctx, missing, true); err == nil {
		t.Fatal("a pin of a missing directory was accepted")
	}
	if f.ws.IsPinned(filepath.Clean(missing)) {
		t.Fatal("the refused pin was recorded")
	}
	if _, err := f.Pin(ctx, "relative/root", true); err == nil {
		t.Fatal("a pin of a relative root was accepted")
	}

	state, err := f.Pin(ctx, missing, false)
	if err != nil {
		t.Fatalf("unpin of a missing root: %v", err)
	}
	if state.Pinned || state.Running {
		t.Fatalf("unpin answered %+v, want not pinned and not running", state)
	}
}

// pinRoute issues one pin request against an arbitrary handler, so a test can
// drive the socket handler without a token or the network handler with one.
func pinRoute(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, bytes.NewReader(data)))
	return rec
}

// TestThePinRouteIsTheSocketsOnly is decision 3: the socket handler serves the
// pin route, and the network listener answers it 404 as it answers attach.
func TestThePinRouteIsTheSocketsOnly(t *testing.T) {
	f, _ := projectFleet(t)
	shortenLinger(t, f)
	s := NewServer(f)
	root := t.TempDir()
	body := map[string]any{"root": root, "pinned": true}

	rec := pinRoute(t, s.SocketHandler(), http.MethodPost, pinPath, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("socket %s status = %d, body = %s", pinPath, rec.Code, rec.Body.String())
	}
	var got PinState
	decodeBody(t, rec, &got)
	if want := filepath.Clean(root); got.Root != want || !got.Pinned || !got.Running {
		t.Fatalf("pin answered %+v, want %q pinned and running", got, want)
	}

	// The network listener never changes what the daemon keeps running.
	if rec := pinRoute(t, s, http.MethodPost, pinPath, body); rec.Code != http.StatusNotFound {
		t.Fatalf("network %s = %d, body = %s; want 404", pinPath, rec.Code, rec.Body.String())
	}
}

// TestThePinRouteRefusesABadRequest: only a POST reaches the fleet, and only
// with a root the daemon could start a child in.
func TestThePinRouteRefusesABadRequest(t *testing.T) {
	f, _ := projectFleet(t)
	shortenLinger(t, f)
	sock := NewServer(f).SocketHandler()

	rec := pinRoute(t, sock, http.MethodGet, pinPath, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s = %d, body = %s; want 405", pinPath, rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want %q", allow, http.MethodPost)
	}

	if rec := pinRoute(t, sock, http.MethodPost, pinPath,
		map[string]any{"root": "relative/root", "pinned": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("relative root = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}

	missing := filepath.Join(t.TempDir(), "gone")
	rec = pinRoute(t, sock, http.MethodPost, pinPath, map[string]any{"root": missing, "pinned": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing directory = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeBody(t, rec, &body)
	if body["error"] == "" {
		t.Fatalf("body = %q, want an error", rec.Body.String())
	}
}

// TestALingerThatFiresAfterAPinKeepsTheChild is decision 2's re-check: a
// linger armed before the pin must not reap the child once the pin lands.
func TestALingerThatFiresAfterAPinKeepsTheChild(t *testing.T) {
	f, _ := projectFleet(t)
	shortenLinger(t, f)
	timers := captureTimers(f)
	ctx := ctlContext(t)
	root := t.TempDir()

	// Start the child unpinned, so its linger is armed.
	pc, err := f.projectChildFor(ctx, root)
	if err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	f.rearmLinger(pc)
	if n := timers.count(); n != 1 {
		t.Fatalf("armed timers = %d, want 1 for an unpinned empty child", n)
	}

	// The pin lands while the timer is armed.
	if _, err := f.Pin(ctx, root, true); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// The timer fires: the pin keeps the child.
	timers.at(0).fire()
	if _, ok := projectEntry(f, filepath.Clean(root)); !ok {
		t.Fatal("the linger reaped a child the pin keeps")
	}
}

// TestThePinRouteAnswersItsRefusals: a bridge without a fleet answers 501, and
// a child that cannot hold answers 502 with the agent's sentence.
func TestThePinRouteAnswersItsRefusals(t *testing.T) {
	t.Run("a server without a fleet", func(t *testing.T) {
		srv := NewServer(nil)
		rec := pinRoute(t, srv.SocketHandler(), http.MethodPost, pinPath,
			map[string]any{"root": t.TempDir(), "pinned": true})
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("pin on a server without a fleet = %d, body = %s; want 501", rec.Code, rec.Body.String())
		}
		var body map[string]string
		decodeBody(t, rec, &body)
		if body["error"] == "" {
			t.Fatalf("501 body = %q, want a JSON error", rec.Body.String())
		}
	})

	t.Run("a child that cannot hold", func(t *testing.T) {
		f, _ := projectFleetWith(t, func(fake *fakeProjectChild) {
			fake.handler = func(method string, _ json.RawMessage) (any, *rpcError, bool) {
				if method == "project/hold" {
					return nil, &rpcError{Code: -32603, Message: "hold refused"}, true
				}
				return nil, nil, false
			}
		})
		shortenLinger(t, f)
		rec := pinRoute(t, NewServer(f).SocketHandler(), http.MethodPost, pinPath,
			map[string]any{"root": t.TempDir(), "pinned": true})
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("pin whose hold fails = %d, body = %s; want 502", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "hold refused") {
			t.Fatalf("502 body = %q, want the agent's sentence", rec.Body.String())
		}
	})
}
