package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// fleetRecorder stands in for the fleet's marshal binary: it records what it was
// asked to run, and can be told to take a while or to fail, so a test can see
// what a route ran without a CLI behind it.
type fleetRecorder struct {
	mu      sync.Mutex
	calls   [][]string
	running int
	// overlapped is set when two calls were inside the recorder at once, which
	// is what hostsEdits exists to prevent.
	overlapped bool
	hold       time.Duration
	err        error
	// ctxErr is the error the command's context ended with, which is what a
	// command run through exec.CommandContext reports.
	ctxErr error
}

// command is the fleetCommand this recorder is installed as.
func (f *fleetRecorder) command(ctx context.Context, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.running++
	if f.running > 1 {
		f.overlapped = true
	}
	hold, err := f.hold, f.err
	f.mu.Unlock()

	if hold > 0 {
		select {
		case <-time.After(hold):
		case <-ctx.Done():
		}
	}

	f.mu.Lock()
	f.running--
	if ctxErr := ctx.Err(); ctxErr != nil {
		f.ctxErr = ctxErr
		if err == nil {
			err = ctxErr
		}
	}
	f.mu.Unlock()
	return err
}

// observedCancel is the error the fleet command saw its context end with, or nil
// when it was left to finish.
func (f *fleetRecorder) observedCancel() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxErr
}

// recorded is every call's arguments, in the order they started.
func (f *fleetRecorder) recorded() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.calls...)
}

// nothing fails when the recorder was run at all: a refused request never
// reaches the CLI.
func (f *fleetRecorder) nothing(t *testing.T) {
	t.Helper()
	if calls := f.recorded(); len(calls) != 0 {
		t.Fatalf("the fleet command was run with %q, want it not run at all", calls)
	}
}

// hostEditServer is a server over a test fleet whose fleet command is rec, with
// the tunnel helper's report seeded through the route the helper itself uses
// (one-daemon D7).
func hostEditServer(t *testing.T, rec *fleetRecorder, hosts ...contract.DaemonHost) *Server {
	t.Helper()
	s := NewServer(testFleet(t))
	s.fleetCmd = rec.command
	reportHosts(t, s, hosts...)
	return s
}

// postHost answers one POST /api/hosts through the web listener's handler,
// without a *testing.T: the concurrency test issues it from goroutines.
func postHost(s *Server, body contract.HostAdd) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, hostsPath, bytes.NewReader(data))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// TestAddingAHostRunsFleetAdd is D15: the route runs `fleet add <name> <ssh>`
// and answers the hosts the daemon then reports.
func TestAddingAHostRunsFleetAdd(t *testing.T) {
	box := liveHost("box", "/tmp/box.sock")
	rec := &fleetRecorder{}
	s := hostEditServer(t, rec, box)

	res := doReq(t, s, http.MethodPost, hostsPath, contract.HostAdd{Name: "workshop", SSH: "me@workshop"}, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d, body = %s, want 201", hostsPath, res.Code, res.Body.String())
	}
	calls := rec.recorded()
	if len(calls) != 1 {
		t.Fatalf("the fleet command ran %d times, want once: %q", len(calls), calls)
	}
	want := []string{"add", "workshop", "me@workshop"}
	if strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("the fleet command ran %q, want %q", calls[0], want)
	}
	var got contract.HostList
	decodeBody(t, res, &got)
	if len(got.Hosts) != 1 || got.Hosts[0] != box {
		t.Fatalf("the answer = %+v, want the reported host %+v", got.Hosts, box)
	}
}

// TestAddingAHostPassesTheMarshalPath: the binary to use on the host is the
// caller's, and it is named for the CLI the way the CLI reads it.
func TestAddingAHostPassesTheMarshalPath(t *testing.T) {
	rec := &fleetRecorder{}
	s := hostEditServer(t, rec)

	res := doReq(t, s, http.MethodPost, hostsPath, contract.HostAdd{
		Name: "workshop", SSH: "me@workshop", Marshal: "/opt/marshal/bin/marshal",
	}, nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d, body = %s, want 201", hostsPath, res.Code, res.Body.String())
	}
	want := []string{"add", "workshop", "me@workshop", "--marshal", "/opt/marshal/bin/marshal"}
	if calls := rec.recorded(); len(calls) != 1 || strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("the fleet command ran %q, want %q", calls, want)
	}
}

// TestAnAddThatFailsAnswersTheReason: a host the CLI would not add is the
// caller's mistake, answered 400 with the reason the CLI gave — and without the
// CLI's name for the command, which the caller already knows.
func TestAnAddThatFailsAnswersTheReason(t *testing.T) {
	rec := &fleetRecorder{err: errors.New("marshal fleet add: workshop is already in fleet.toml")}
	s := hostEditServer(t, rec)

	res := doReq(t, s, http.MethodPost, hostsPath, contract.HostAdd{Name: "workshop", SSH: "me@workshop"}, nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an add that failed = %d, body = %s, want 400", res.Code, res.Body.String())
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if want := "workshop is already in fleet.toml"; body["error"] != want {
		t.Fatalf("the answer = %q, want the reason %q", body["error"], want)
	}
}

// TestAnAddRefusesAFlagLikeValue: nothing that looks like a flag reaches the
// CLI, and neither does an empty name or destination.
func TestAnAddRefusesAFlagLikeValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  contract.HostAdd
		field string
	}{
		{"a flag for a name", contract.HostAdd{Name: "--no-notify", SSH: "me@workshop"}, "name"},
		{"a flag for a destination", contract.HostAdd{Name: "workshop", SSH: "-oProxyCommand=touch /tmp/x"}, "ssh"},
		{"a flag for the marshal path", contract.HostAdd{Name: "workshop", SSH: "me@workshop", Marshal: "-oX"}, "marshal"},
		{"no name", contract.HostAdd{SSH: "me@workshop"}, "name"},
		{"no destination", contract.HostAdd{Name: "workshop"}, "ssh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fleetRecorder{}
			s := hostEditServer(t, rec)

			res := doReq(t, s, http.MethodPost, hostsPath, tc.body, nil)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("POST %s = %d, body = %s, want 400", hostsPath, res.Code, res.Body.String())
			}
			if body := res.Body.String(); !strings.Contains(body, tc.field) {
				t.Fatalf("the refusal = %s, want it to name %q", body, tc.field)
			}
			rec.nothing(t)
		})
	}
}

// TestAnAddRefusesAnUnknownField: the body's every field is known, so a typo is
// refused rather than dropped — a caller who wrote "marshal" as "mashal" would
// otherwise get the host's default binary without ever being told.
func TestAnAddRefusesAnUnknownField(t *testing.T) {
	rec := &fleetRecorder{}
	s := hostEditServer(t, rec)

	body := map[string]string{"name": "workshop", "ssh": "me@workshop", "mashal": "/opt/marshal/bin/marshal"}
	res := doReq(t, s, http.MethodPost, hostsPath, body, nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("POST %s with an unknown field = %d, body = %s, want 400", hostsPath, res.Code, res.Body.String())
	}
	if answer := res.Body.String(); !strings.Contains(answer, "mashal") {
		t.Fatalf("the refusal = %s, want it to name the unknown field", answer)
	}
	rec.nothing(t)
}

// TestRemovingAHostRunsFleetRemove: the name comes from the path, the CLI is
// run with `remove`, and the answer is the hosts after it.
func TestRemovingAHostRunsFleetRemove(t *testing.T) {
	box := liveHost("box", "/tmp/box.sock")
	rec := &fleetRecorder{}
	s := hostEditServer(t, rec, box)

	res := doReq(t, s, http.MethodDelete, hostForwardPrefix+"workshop", nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("DELETE %sworkshop = %d, body = %s, want 200", hostForwardPrefix, res.Code, res.Body.String())
	}
	want := []string{"remove", "workshop"}
	if calls := rec.recorded(); len(calls) != 1 || strings.Join(calls[0], "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("the fleet command ran %q, want %q", calls, want)
	}
	var got contract.HostList
	decodeBody(t, res, &got)
	if len(got.Hosts) != 1 || got.Hosts[0] != box {
		t.Fatalf("the answer = %+v, want the reported host %+v", got.Hosts, box)
	}
}

// TestARemoveRefusesAFlagLikeName: the name comes out of the path, so it is
// checked before the CLI sees it — as it arrives, and percent-encoded, which
// decodes to the same flag-shaped name.
func TestARemoveRefusesAFlagLikeName(t *testing.T) {
	for _, path := range []string{hostForwardPrefix + "-oX", hostForwardPrefix + "%2DoX"} {
		t.Run(path, func(t *testing.T) {
			rec := &fleetRecorder{}
			s := hostEditServer(t, rec)

			res := doReq(t, s, http.MethodDelete, path, nil, nil)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("DELETE %s = %d, body = %s, want 400", path, res.Code, res.Body.String())
			}
			if answer := res.Body.String(); !strings.Contains(answer, "name") {
				t.Fatalf("the refusal = %s, want it to name the field", answer)
			}
			rec.nothing(t)
		})
	}
}

// TestHostEditsRunOneAtATime: both routes write the one fleet.toml, so a second
// edit waits for the first rather than interleaving with it (D15).
func TestHostEditsRunOneAtATime(t *testing.T) {
	rec := &fleetRecorder{hold: 25 * time.Millisecond}
	s := hostEditServer(t, rec)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, host := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = postHost(s, contract.HostAdd{Name: host, SSH: "me@" + host}).Code
		}()
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusCreated {
			t.Fatalf("add %d = %d, want 201", i, code)
		}
	}
	if calls := rec.recorded(); len(calls) != 2 {
		t.Fatalf("the fleet command ran %d times, want twice: %q", len(calls), calls)
	}
	rec.mu.Lock()
	overlapped := rec.overlapped
	rec.mu.Unlock()
	if overlapped {
		t.Fatal("two fleet edits overlapped, want one at a time")
	}
}

// TestAHostEditStopsWhenItsCallerGoesAway exercises the budget editHosts puts on
// one edit (hostEditTimeout): the command runs under the request's context, so a
// caller that goes away ends it instead of leaving a write nobody waits for.
func TestAHostEditStopsWhenItsCallerGoesAway(t *testing.T) {
	rec := &fleetRecorder{hold: time.Hour}
	s := hostEditServer(t, rec)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel() // the caller is gone before the edit starts

	req := httptest.NewRequest(http.MethodPost, hostsPath,
		strings.NewReader(`{"name":"workshop","ssh":"me@workshop"}`)).WithContext(ctx)
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	s.ServeHTTP(res, req)

	if err := rec.observedCancel(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the fleet command saw %v, want its context cancelled", err)
	}
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an edit whose caller went away = %d, body = %s, want 400", res.Code, res.Body.String())
	}
}

// TestTheHostRoutesAreGuarded: a request another site's page made is refused by
// plan 3's guard before the route sees it, so the CLI is never run for it (D13).
func TestTheHostRoutesAreGuarded(t *testing.T) {
	rec := &fleetRecorder{}
	s := hostEditServer(t, rec)

	res := doReq(t, s, http.MethodPost, hostsPath,
		contract.HostAdd{Name: "workshop", SSH: "me@workshop"},
		map[string]string{"Sec-Fetch-Site": "cross-site"})
	if res.Code != http.StatusForbidden {
		t.Fatalf("a cross-site POST %s = %d, body = %s, want 403", hostsPath, res.Code, res.Body.String())
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if body["error"] != crossSiteRefused {
		t.Fatalf("the refusal = %q, want %q", body["error"], crossSiteRefused)
	}
	rec.nothing(t)
}

// TestTheForwardStillOwnsLongerPaths: the remove route names one segment, so a
// path below a host's name is still the forward to that host's socket (D10).
func TestTheForwardStillOwnsLongerPaths(t *testing.T) {
	rec := &fleetRecorder{}
	tunnel := newFakeTunnel(t, okTunnel())
	s := hostEditServer(t, rec, liveHost("box", tunnel.socket))

	res := doReq(t, s, http.MethodDelete, hostForwardPrefix+"box/api/agents/x", nil, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("a forwarded delete = %d, body = %s, want the host's own 200", res.Code, res.Body.String())
	}
	seen := tunnel.next(t)
	if seen.method != http.MethodDelete || seen.path != "/api/agents/x" {
		t.Fatalf("the host was sent %s %s, want DELETE /api/agents/x", seen.method, seen.path)
	}
	rec.nothing(t)
}

// TestTheDefaultFleetCommandCarriesTheLastLine: the CLI reports over several
// lines, so it is the last one that failed that the caller reads (D15).
func TestTheDefaultFleetCommandCarriesTheLastLine(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "marshal")
	// The stand-in reports the argv it was given when it is not `fleet add …`:
	// a wrong assembly then answers the test with that line instead of the
	// reason, so the assertion on the reason fails.
	script := `#!/bin/sh
case "$1 $2" in
  'fleet add') ;;
  *) echo "marshal fleet add: unexpected argv: $*" >&2; exit 9 ;;
esac
echo 'marshal fleet add: reading fleet.toml' >&2
echo 'marshal fleet add: workshop is already in fleet.toml' >&2
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write the stand-in marshal binary: %v", err)
	}
	f := testFleet(t)
	f.marshalBin = bin
	s := NewServer(f)

	res := doReq(t, s, http.MethodPost, hostsPath, contract.HostAdd{Name: "workshop", SSH: "me@workshop"}, nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an add the CLI refused = %d, body = %s, want 400", res.Code, res.Body.String())
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if want := "workshop is already in fleet.toml"; body["error"] != want {
		t.Fatalf("the answer = %q, want the second line %q", body["error"], want)
	}
}

// TestAFleetCommandThatCannotRunAnswersASentence: when the command never
// starts, the error names a binary path and a syscall on the daemon's machine,
// which is not the caller's to read.
func TestAFleetCommandThatCannotRunAnswersASentence(t *testing.T) {
	f := testFleet(t)
	missing := filepath.Join(t.TempDir(), "no-such-marshal")
	f.marshalBin = missing
	s := NewServer(f)

	res := doReq(t, s, http.MethodPost, hostsPath, contract.HostAdd{Name: "workshop", SSH: "me@workshop"}, nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an add whose command could not run = %d, body = %s, want 400", res.Code, res.Body.String())
	}
	var body map[string]string
	decodeBody(t, res, &body)
	if body["error"] != fleetStartFailure {
		t.Fatalf("the answer = %q, want %q", body["error"], fleetStartFailure)
	}
	if strings.Contains(res.Body.String(), missing) {
		t.Fatalf("the answer %s names the binary path, want it not to", res.Body.String())
	}
}

// TestTheHostEditRoutesNeedAFleet: a bridge built over a registry keeps no
// fleet.toml, so neither route pretends to edit one.
func TestTheHostEditRoutesNeedAFleet(t *testing.T) {
	s := NewServer(&Registry{})

	if res := doReq(t, s, http.MethodPost, hostsPath, contract.HostAdd{Name: "workshop", SSH: "me@workshop"}, nil); res.Code != http.StatusNotImplemented {
		t.Fatalf("POST %s without a fleet = %d, body = %s, want 501", hostsPath, res.Code, res.Body.String())
	}
	if res := doReq(t, s, http.MethodDelete, hostForwardPrefix+"workshop", nil, nil); res.Code != http.StatusNotImplemented {
		t.Fatalf("DELETE without a fleet = %d, body = %s, want 501", res.Code, res.Body.String())
	}
}
