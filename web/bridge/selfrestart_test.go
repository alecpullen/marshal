package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// ownBinaryHarness is an attach harness whose fleet has recorded its own
// executable: a file in a temporary directory the test owns, so a test can
// replace it exactly as a build writes over a running daemon's binary.
func ownBinaryHarness(t *testing.T) (*attachHarness, string) {
	t.Helper()
	h := newAttachHarness(t)
	bin := filepath.Join(t.TempDir(), "webbridge")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho webbridge test-old\n"), 0o755); err != nil {
		t.Fatalf("write the own binary: %v", err)
	}
	if err := h.fleet.SetOwnBinary(bin); err != nil {
		t.Fatalf("SetOwnBinary: %v", err)
	}
	return h, bin
}

// replaceOwnBinary writes a build over the recorded executable. The size
// differs, so the replacement is a different binary by sameBinary's rule.
func replaceOwnBinary(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("replace the own binary: %v", err)
	}
}

// awaitSelfRestart waits for the fleet's decision to re-execute.
func awaitSelfRestart(t *testing.T, f *Fleet) SelfRestart {
	t.Helper()
	select {
	case sr := <-f.SelfRestarts():
		return sr
	case <-time.After(5 * time.Second):
		t.Fatal("no SelfRestart was delivered")
		return SelfRestart{}
	}
}

// noSelfRestart fails if the fleet decided to re-execute.
func noSelfRestart(t *testing.T, f *Fleet) {
	t.Helper()
	select {
	case sr := <-f.SelfRestarts():
		t.Fatalf("the bridge restarted unexpectedly: %+v", sr)
	case <-time.After(100 * time.Millisecond):
	}
}

// reportedOwnRestart answers restart, the field GET /api/daemon reports.
func reportedOwnRestart(t *testing.T, h *attachHarness) *DaemonRestart {
	t.Helper()
	var out struct {
		Restart *DaemonRestart `json:"restart"`
	}
	if err := json.Unmarshal(h.infoBytes(), &out); err != nil {
		t.Fatalf("decode /api/daemon: %v", err)
	}
	return out.Restart
}

// postRestart POSTs body to the restart route over the harness's socket and
// returns the streaming answer.
func (h *attachHarness) postRestart(t *testing.T, body string) *http.Response {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", h.socket)
		},
	}}
	req, err := http.NewRequest(http.MethodPost, "http://daemon"+daemonRestartPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the restart request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", daemonRestartPath, err)
	}
	return resp
}

// progressLine decodes one line of the restart route's answer.
func progressLine(t *testing.T, raw string) DaemonRestartProgress {
	t.Helper()
	var line DaemonRestartProgress
	if err := json.Unmarshal([]byte(raw), &line); err != nil {
		t.Fatalf("decode progress line %q: %v", raw, err)
	}
	return line
}

// shortRestartInterval makes a requested restart sweep quickly, so a test does
// not wait the production interval.
func shortRestartInterval(t *testing.T) {
	t.Helper()
	prev := restartRequestedInterval
	restartRequestedInterval = 20 * time.Millisecond
	t.Cleanup(func() { restartRequestedInterval = prev })
}

// TestAReplacedBridgeRestartsWhenIdle: a webbridge written over the running
// daemon's binary restarts it once a sweep finds nothing to wait for.
func TestAReplacedBridgeRestartsWhenIdle(t *testing.T) {
	h, bin := ownBinaryHarness(t)
	ctx := ctlContext(t)
	var checked string
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		checked = path
		return "test-new", nil
	}
	replaceOwnBinary(t, bin, "#!/bin/sh\necho webbridge test-new, a longer build\n")

	h.fleet.SweepRestarts(ctx, time.Now())

	sr := awaitSelfRestart(t, h.fleet)
	if checked != bin {
		t.Errorf("build check ran for %q, want %q", checked, bin)
	}
	if sr.Path != bin || sr.Version != "test-new" || sr.Reason != "a new webbridge build" {
		t.Errorf("SelfRestart = %+v, want %s on test-new for a new webbridge build", sr, bin)
	}
	// At most one decision per process.
	h.fleet.SweepRestarts(ctx, time.Now())
	noSelfRestart(t, h.fleet)
}

// TestAnUnchangedBridgeDoesNotRestart: the build on disk is the one running.
func TestAnUnchangedBridgeDoesNotRestart(t *testing.T) {
	h, _ := ownBinaryHarness(t)
	ctx := ctlContext(t)
	var checks int
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		checks++
		return "test-new", nil
	}

	h.fleet.SweepRestarts(ctx, time.Now())

	noSelfRestart(t, h.fleet)
	if checks != 0 {
		t.Errorf("build checks = %d, want none", checks)
	}
	if restart := reportedOwnRestart(t, h); restart != nil {
		t.Errorf("restart = %+v, want none", restart)
	}
}

// TestTheBridgeWaitsForABusyChild: a child with a prompt in flight holds the
// daemon's own restart, and GET /api/daemon names the line holding it.
func TestTheBridgeWaitsForABusyChild(t *testing.T) {
	h, bin := ownBinaryHarness(t)
	ctx := ctlContext(t)
	root := t.TempDir()
	if _, err := h.fleet.projectChildFor(ctx, root); err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	fakeResult(fake, "project/state", map[string]any{
		"busy": []string{"a prompt is running"}, "version": "v1",
	})
	var checks int
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		checks++
		return "test-new", nil
	}
	replaceOwnBinary(t, bin, "#!/bin/sh\necho webbridge test-new, a longer build\n")

	h.fleet.SweepRestarts(ctx, time.Now())

	noSelfRestart(t, h.fleet)
	if checks != 0 {
		t.Errorf("build checks = %d, want none while a child is busy", checks)
	}
	restart := reportedOwnRestart(t, h)
	if restart == nil {
		t.Fatal("GET /api/daemon reported no restart")
	}
	if restart.WaitingFor != "a prompt is running" {
		t.Errorf("restart.waitingFor = %q, want the busy line", restart.WaitingFor)
	}
	if len(restart.Reasons) != 1 || restart.Reasons[0] != "a new webbridge build" {
		t.Errorf("restart.reasons = %v", restart.Reasons)
	}
}

// TestTheBridgeWaitsForARunningAgent: a fleet agent mid-turn is ended by the
// daemon's restart, so it holds it too (decision 1).
func TestTheBridgeWaitsForARunningAgent(t *testing.T) {
	h, bin := ownBinaryHarness(t)
	ctx := ctlContext(t)
	h.fleet.newRuntime = func(a Agent) (*Child, error) {
		return &Child{Transport: newFakeAgent()}, nil
	}
	id, err := h.fleet.Spawn(ctx, t.TempDir(), SpawnOptions{Name: "worker", Mode: "edit"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	rt, err := h.fleet.runtimeForAgent(id)
	if err != nil {
		t.Fatalf("runtimeForAgent: %v", err)
	}
	// A prompt in flight is what makes the agent's status "running".
	rt.reg.beginTurn(rt.sessionID)
	if got := h.fleet.Snapshot()[0].Status; got != contract.AgentRunning {
		t.Fatalf("agent status = %q, want %q", got, contract.AgentRunning)
	}
	var checks int
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		checks++
		return "test-new", nil
	}
	replaceOwnBinary(t, bin, "#!/bin/sh\necho webbridge test-new, a longer build\n")

	h.fleet.SweepRestarts(ctx, time.Now())

	noSelfRestart(t, h.fleet)
	if checks != 0 {
		t.Errorf("build checks = %d, want none while an agent is running", checks)
	}
	restart := reportedOwnRestart(t, h)
	if restart == nil {
		t.Fatal("GET /api/daemon reported no restart")
	}
	if restart.WaitingFor != "agent worker is running" {
		t.Errorf("restart.waitingFor = %q, want %q", restart.WaitingFor, "agent worker is running")
	}
}

// TestARefusedBridgeBuildKeepsRunning: a build that cannot start is never
// executed, and its refusal is not re-checked until the file changes.
func TestARefusedBridgeBuildKeepsRunning(t *testing.T) {
	h, bin := ownBinaryHarness(t)
	ctx := ctlContext(t)
	var checks int
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		checks++
		return "", errors.New("webbridge: refusing to start\nwith a second line")
	}
	replaceOwnBinary(t, bin, "#!/bin/sh\nexit 1\n")

	h.fleet.SweepRestarts(ctx, time.Now())

	noSelfRestart(t, h.fleet)
	if checks != 1 {
		t.Fatalf("build checks = %d, want 1", checks)
	}
	restart := reportedOwnRestart(t, h)
	if restart == nil {
		t.Fatal("GET /api/daemon reported no restart")
	}
	if restart.Refused != "webbridge: refusing to start" {
		t.Errorf("restart.refused = %q, want the build's first line", restart.Refused)
	}
	if restarts := h.fleet.Snapshot(); len(restarts) != 0 {
		t.Errorf("agents = %+v", restarts)
	}

	// The same file is not checked again: the refusal stands.
	h.fleet.SweepRestarts(ctx, time.Now())
	noSelfRestart(t, h.fleet)
	if checks != 1 {
		t.Errorf("build checks = %d, want the refused build checked once", checks)
	}
}

// TestTheViewersHearTheDaemonRestart: an attached client reads the restart
// frame the announcement writes, before the fleet's decision is taken.
func TestTheViewersHearTheDaemonRestart(t *testing.T) {
	h, bin := ownBinaryHarness(t)
	ctx := ctlContext(t)
	root := t.TempDir()
	res := h.attach(http.MethodPost, root, "", true)
	if res.status != http.StatusSwitchingProtocols {
		t.Fatalf("attach = %d, want 101", res.status)
	}
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		return "test-new", nil
	}
	replaceOwnBinary(t, bin, "#!/bin/sh\necho webbridge test-new, a longer build\n")

	// A live viewer keeps its child busy within the quiet window, so the sweep
	// is run at a moment past that window: the child is quiet by then.
	h.fleet.SweepRestarts(ctx, time.Now().Add(restartQuiet+time.Minute))

	line := res.readLine(t)
	var frame struct {
		Method string `json:"method"`
		Params struct {
			Reason  string `json:"reason"`
			Version string `json:"version"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &frame); err != nil {
		t.Fatalf("decode the announcement %q: %v", line, err)
	}
	if frame.Method != restartingMethod {
		t.Fatalf("frame method = %q, want %q: %s", frame.Method, restartingMethod, line)
	}
	if frame.Params.Reason != "a new webbridge build" || frame.Params.Version != "test-new" {
		t.Errorf("announcement = %+v, want test-new for a new webbridge build", frame.Params)
	}
	sr := awaitSelfRestart(t, h.fleet)
	if sr.Reason != "a new webbridge build" || sr.Version != "test-new" {
		t.Errorf("SelfRestart = %+v", sr)
	}
}

// busyChild starts a project child whose project/state answers busy.
func busyChild(t *testing.T, h *attachHarness, busy []string) (string, *fakeProjectChild) {
	t.Helper()
	root := t.TempDir()
	if _, err := h.fleet.projectChildFor(ctlContext(t), root); err != nil {
		t.Fatalf("projectChildFor: %v", err)
	}
	fake := h.fake(root)
	if fake == nil {
		t.Fatal("no fake was built for the root")
	}
	fakeResult(fake, "project/state", map[string]any{"busy": busy, "version": "v1"})
	return root, fake
}

// TestTheRestartRouteStreamsUntilIdle: the route answers one line per sweep,
// and the last one is the restart, after which the answer ends.
func TestTheRestartRouteStreamsUntilIdle(t *testing.T) {
	shortRestartInterval(t)
	h, _ := ownBinaryHarness(t)
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		return "test-new", nil
	}
	_, fake := busyChild(t, h, []string{"a prompt is running"})

	resp := h.postRestart(t, `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s = %d, want 200", daemonRestartPath, resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", got)
	}
	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() {
		t.Fatalf("the answer ended with no line: %v", lines.Err())
	}
	first := progressLine(t, lines.Text())
	if len(first.WaitingFor) != 1 || first.WaitingFor[0] != "a prompt is running" {
		t.Fatalf("first line = %+v, want the busy line", first)
	}
	if first.Restarting {
		t.Fatalf("first line already restarted: %+v", first)
	}

	// The prompt ends, so the next sweep fires. A repeated waiting line is
	// possible while the child is still busy; the answer ends with the fire.
	fakeResult(fake, "project/state", map[string]any{"version": "v1"})
	var fired DaemonRestartProgress
	for lines.Scan() {
		line := progressLine(t, lines.Text())
		if line.Restarting {
			fired = line
			break
		}
	}
	if !fired.Restarting {
		t.Fatalf("the answer ended without a restarting line: %v", lines.Err())
	}
	if fired.Version != "test-new" {
		t.Errorf("restarting version = %q, want test-new", fired.Version)
	}
	if lines.Scan() {
		t.Fatalf("the answer continued after the restart: %s", lines.Text())
	}
}

// TestTheRestartRouteNowDoesNotWait: --now asks for the restart with no sweep.
func TestTheRestartRouteNowDoesNotWait(t *testing.T) {
	h, _ := ownBinaryHarness(t)
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		return "test-new", nil
	}
	// A busy child would hold a waiting request: --now does not wait for it.
	busyChild(t, h, []string{"a prompt is running"})

	resp := h.postRestart(t, `{"now": true}`)
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() {
		t.Fatalf("the answer ended with no line: %v", lines.Err())
	}
	line := progressLine(t, lines.Text())
	if !line.Restarting || line.Version != "test-new" {
		t.Fatalf("answer = %+v, want restarting on test-new", line)
	}
	if len(line.WaitingFor) != 0 {
		t.Errorf("answer waited for %v", line.WaitingFor)
	}
	if lines.Scan() {
		t.Fatalf("the answer continued after the restart: %s", lines.Text())
	}
}

// TestClosingTheRequestWithdrawsIt: closing the request ends its wait, and
// nothing restarts afterwards because the binary on disk did not change.
func TestClosingTheRequestWithdrawsIt(t *testing.T) {
	shortRestartInterval(t)
	h, _ := ownBinaryHarness(t)
	h.fleet.checkBuild = func(ctx context.Context, path string) (string, error) {
		return "test-new", nil
	}
	_, fake := busyChild(t, h, []string{"a prompt is running"})

	resp := h.postRestart(t, `{}`)
	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() {
		t.Fatalf("the answer ended with no line: %v", lines.Err())
	}
	if line := progressLine(t, lines.Text()); len(line.WaitingFor) != 1 {
		t.Fatalf("first line = %+v, want the busy line", line)
	}
	// The client goes away; the request withdraws by its context alone.
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close the request: %v", err)
	}
	// Enough sweeps for a request that outlived its client to have fired.
	time.Sleep(10 * restartRequestedInterval)

	fakeResult(fake, "project/state", map[string]any{"version": "v1"})
	h.fleet.SweepRestarts(ctlContext(t), time.Now())
	noSelfRestart(t, h.fleet)
}

// TestTheRestartRouteIsSocketOnly: the network listener never restarts the
// daemon.
func TestTheRestartRouteIsSocketOnly(t *testing.T) {
	h, _ := ownBinaryHarness(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, daemonRestartPath, strings.NewReader(`{}`))
	h.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ServeHTTP POST %s = %d, want 404: %s", daemonRestartPath, rec.Code, rec.Body)
	}
}

// TestTheRestartRouteNeedsAnOwnBinary: a daemon that never recorded its own
// executable cannot restart itself (decision 7).
func TestTheRestartRouteNeedsAnOwnBinary(t *testing.T) {
	h := newAttachHarness(t)
	resp := h.postRestart(t, `{}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("POST %s = %d, want 501", daemonRestartPath, resp.StatusCode)
	}
	var answer struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if answer.Error != "this daemon cannot restart itself" {
		t.Errorf("refusal = %q", answer.Error)
	}
}
