package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClassifyWatchEvent(t *testing.T) {
	params := json.RawMessage(`{"sessionId":"s-1","update":{"kind":"watch","event":{"watchId":"w1","state":"fired"}}}`)
	d, ok := classifyNotification("session/update", params)
	if !ok || d.Kind != "watch" || !strings.Contains(string(d.Watch), `"watchId":"w1"`) {
		t.Fatalf("delta = %+v ok=%v", d, ok)
	}
	if _, ok := classifyNotification("session/update",
		json.RawMessage(`{"sessionId":"s-1","update":{"kind":"watch"}}`)); ok {
		t.Fatal("a watch update with no event was classified")
	}
}

func TestWatchesListMergesStudioAndAgents(t *testing.T) {
	ctl := newFakeAgent()
	f := ctlFleet(t, ctl)
	var mu sync.Mutex
	agents := map[string]*fakeAgent{}
	n := 0
	f.newRuntime = func(a Agent) (*Child, error) {
		fa := newFakeAgent()
		mu.Lock()
		n++
		if n == 2 {
			fa.caps = map[string]any{} // an agent with no watchAccess
		}
		agents[a.ID] = fa
		mu.Unlock()
		return &Child{Transport: fa}, nil
	}
	ctl.results["session/watch_list"] = map[string]any{"watches": []map[string]any{{"id": "w1", "name": "disk"}}}
	withWatch, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	withoutCap, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agents[withWatch].results["session/watch_list"] = map[string]any{"watches": []map[string]any{{"id": "w9", "name": "ci"}}}
	agents[withoutCap].results["session/watch_list"] = map[string]any{"watches": []map[string]any{{"id": "w5", "name": "never"}}}

	rec := doReq(t, NewServer(f, ""), http.MethodGet, "/api/watches", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	decodeBody(t, rec, &got)
	owners := map[string]string{}
	for _, w := range got {
		owners[w["id"].(string)] = w["agentId"].(string)
	}
	if len(got) != 2 || owners["w1"] != "studio" || owners["w9"] != withWatch {
		t.Fatalf("merged list = %s", rec.Body.String())
	}
	if n := len(agents[withoutCap].calls("session/watch_list")); n != 0 {
		t.Fatal("an agent without watchAccess was asked for its watches")
	}
}

func TestWatchesListToleratesAnUnsupportedControlAgent(t *testing.T) {
	ctl := newFakeAgent()
	ctl.handler = func(m string, _ json.RawMessage) (any, *rpcError, bool) {
		if m == "session/watch_list" {
			return nil, &rpcError{Code: -32601, Message: "method not found"}, true
		}
		return nil, nil, false
	}
	f := ctlFleet(t, ctl)
	rec := doReq(t, NewServer(f, ""), http.MethodGet, "/api/watches", nil, nil)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestWatchesStartAndStopProxy(t *testing.T) {
	ctl := newFakeAgent()
	f := ctlFleet(t, ctl)
	ctl.results["session/watch_start"] = map[string]any{"id": "w1", "name": "disk"}
	s := NewServer(f, "")

	rec := doReq(t, s, http.MethodPost, "/api/watches", map[string]any{
		"spec": map[string]any{"name": "disk", "kind": "command", "command": "df"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	decodeBody(t, rec, &res)
	if res["id"] != "w1" || res["agentId"] != "studio" {
		t.Fatalf("start result = %v", res)
	}
	got := ctl.calls("session/watch_start")
	if len(got) != 1 || !strings.Contains(got[0], `"sessionId":"s-1"`) || !strings.Contains(got[0], `"command":"df"`) {
		t.Fatalf("watch_start = %v", got)
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/watches/studio/w1", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body.String())
	}
	if got := ctl.calls("session/watch_stop"); len(got) != 1 || !strings.Contains(got[0], `"id":"w1"`) {
		t.Fatalf("watch_stop = %v", got)
	}
	events := auditTail(t, f)
	if findEvent(events, AuditWatchStarted) == nil || findEvent(events, AuditWatchStopped) == nil {
		t.Fatalf("watch lifecycle not audited: %+v", events)
	}
	if rec := doReq(t, s, http.MethodPost, "/api/watches", map[string]any{}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("start without spec = %d", rec.Code)
	}
}

func TestWatchesStartOnAnAgentRoutesToItsSession(t *testing.T) {
	f, _, agentOf := agentFleet(t)
	id, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	if err != nil {
		t.Fatal(err)
	}
	agentOf(id).results["session/watch_start"] = map[string]any{"id": "w3"}
	s := NewServer(f, "")
	rec := doReq(t, s, http.MethodPost, "/api/watches", map[string]any{
		"agentId": id, "spec": map[string]any{"name": "x"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	if got := agentOf(id).calls("session/watch_start"); len(got) != 1 || !strings.Contains(got[0], `"sessionId":"s-1"`) {
		t.Fatalf("agent watch_start = %v", got)
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/watches/"+id+"/w3", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/watches/nope/w3", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("stop on an unknown agent = %d", rec.Code)
	}
}

func TestWatchesRerouteIsAllowedOnlyOnStudioWatches(t *testing.T) {
	f, _, _ := agentFleet(t)
	id, _ := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{})
	rec := doReq(t, NewServer(f, ""), http.MethodPost, "/api/watches", map[string]any{
		"agentId": id, "spec": map[string]any{"name": "x"},
		"onTrip": map[string]any{"reroute": map[string]any{"role": "implementer", "preset": "fast"}},
	}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Studio") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

// routingHarness is a control agent whose config behaves like the engine's
// routing section: set_routing stores what it is given.
type routingHarness struct {
	t   *testing.T
	f   *Fleet
	s   *Server
	ctl *fakeAgent
	mu  sync.Mutex
	// profiles is the stored routing, role -> binding JSON by profile.
	profiles map[string]map[string]json.RawMessage
}

func newRoutingHarness(t *testing.T) *routingHarness {
	t.Helper()
	ctl := newFakeAgent()
	f := ctlFleet(t, ctl)
	h := &routingHarness{t: t, f: f, ctl: ctl, s: NewServer(f, ""), profiles: map[string]map[string]json.RawMessage{
		"main": {
			"implementer": json.RawMessage(`{"preset":"slow"}`),
			"reviewer":    json.RawMessage(`{"preset":"slow"}`),
		},
	}}
	ctl.results["session/watch_start"] = map[string]any{"id": "w1", "name": "disk"}
	ctl.handler = func(method string, params json.RawMessage) (any, *rpcError, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch method {
		case "config/get":
			return map[string]any{
				"profiles":       h.profiles,
				"defaultProfile": "main",
				"presets":        map[string]any{"slow": map[string]any{}, "fast": map[string]any{}},
				"roles":          []string{"implementer", "reviewer"},
				"budgets":        Budgets{},
			}, nil, true
		case "config/set_routing":
			var p struct {
				Profiles map[string]map[string]json.RawMessage `json:"profiles"`
			}
			_ = json.Unmarshal(params, &p)
			h.profiles = p.Profiles
			return map[string]any{}, nil, true
		}
		return nil, nil, false
	}
	return h
}

func (h *routingHarness) binding(role string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(h.profiles["main"][role])
}

func (h *routingHarness) startRerouteWatch() {
	h.t.Helper()
	rec := doReq(h.t, h.s, http.MethodPost, "/api/watches", map[string]any{
		"spec":   map[string]any{"name": "disk"},
		"onTrip": map[string]any{"reroute": map[string]any{"role": "implementer", "preset": "fast"}},
	}, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
}

func (h *routingHarness) fire(state string) {
	h.ctl.notify("session/update", map[string]any{"sessionId": "s-1", "update": map[string]any{
		"kind": "watch", "event": map[string]any{"watchId": "w1", "name": "disk", "state": state},
	}})
}

func rerouteDeltas(f *Fleet) []rerouteDelta {
	var out []rerouteDelta
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var d rerouteDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "reroute" {
			out = append(out, d)
		}
	}
	return out
}

func TestRerouteAppliesOnFireAndUndoRestores(t *testing.T) {
	h := newRoutingHarness(t)
	h.startRerouteWatch()
	if _, ok := h.f.ws.WatchRule("w1"); !ok {
		t.Fatal("the reroute rule was not stored")
	}

	h.fire("watching")
	time.Sleep(50 * time.Millisecond)
	if h.binding("implementer") != `{"preset":"slow"}` {
		t.Fatal("a watch that has not fired rerouted anyway")
	}

	h.fire("fired")
	waitFor(t, 5*time.Second, "reroute applied", func() bool { return h.binding("implementer") == `{"preset":"fast"}` })
	if h.binding("reviewer") != `{"preset":"slow"}` {
		t.Fatal("a reroute disturbed another role's binding")
	}
	var deltas []rerouteDelta
	waitFor(t, 5*time.Second, "reroute delta", func() bool {
		deltas = rerouteDeltas(h.f)
		return len(deltas) == 1
	})
	d := deltas[0]
	if d.Role != "implementer" || d.Watch != "disk" || d.From != "slow" || d.To != "fast" || d.ID == "" || d.At == 0 {
		t.Fatalf("delta = %+v", d)
	}
	e := findEvent(auditTail(t, h.f), AuditModelsChanged)
	if e == nil || e.Reason != "watch:disk" {
		t.Fatalf("reroute not audited with its reason: %+v", e)
	}
	if rule, ok := h.f.ws.WatchRule("w1"); ok && rule.Reroute != nil {
		t.Fatal("the reroute outlived its firing")
	}
	// The watch event itself reaches the fleet stream, tagged studio.
	var sawWatch bool
	for _, ev := range h.f.fleetLog.Tail(fleetStreamKey) {
		var fd fleetDelta
		if json.Unmarshal(ev.Data, &fd) == nil && fd.Kind == "watch" && fd.AgentID == "studio" && fd.SessionID == "studio" {
			sawWatch = true
		}
	}
	if !sawWatch {
		t.Fatal("no studio watch delta on the fleet stream")
	}

	// A repeated fired event must not reroute again.
	h.fire("fired")
	time.Sleep(50 * time.Millisecond)
	if n := len(h.ctl.calls("config/set_routing")); n != 1 {
		t.Fatalf("set_routing sent %d times, want 1", n)
	}

	rec := doReq(t, h.s, http.MethodPost, "/api/reroutes/"+d.ID+"/undo", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("undo = %d %s", rec.Code, rec.Body.String())
	}
	if h.binding("implementer") != `{"preset":"slow"}` {
		t.Fatalf("undo left %s", h.binding("implementer"))
	}
	if rec := doReq(t, h.s, http.MethodPost, "/api/reroutes/"+d.ID+"/undo", nil, nil); rec.Code != http.StatusConflict {
		t.Fatalf("second undo = %d, want 409", rec.Code)
	}
	if rec := doReq(t, h.s, http.MethodPost, "/api/reroutes/nope/undo", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown undo = %d, want 404", rec.Code)
	}
}

func TestRerouteUndoRefusesToOverwriteALaterEdit(t *testing.T) {
	h := newRoutingHarness(t)
	h.startRerouteWatch()
	h.fire("fired")
	waitFor(t, 5*time.Second, "reroute applied", func() bool { return len(rerouteDeltas(h.f)) == 1 })
	id := rerouteDeltas(h.f)[0].ID

	h.mu.Lock()
	h.profiles["main"]["implementer"] = json.RawMessage(`{"preset":"someone-elses-choice"}`)
	h.mu.Unlock()

	rec := doReq(t, h.s, http.MethodPost, "/api/reroutes/"+id+"/undo", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("undo = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if h.binding("implementer") != `{"preset":"someone-elses-choice"}` {
		t.Fatal("undo overwrote a later edit")
	}
}

func TestRerouteRuleIsValidatedAtCreation(t *testing.T) {
	h := newRoutingHarness(t)
	for name, rule := range map[string]map[string]any{
		"unknown preset": {"role": "implementer", "preset": "nope"},
		"unknown role":   {"role": "wizard", "preset": "fast"},
		"missing role":   {"preset": "fast"},
	} {
		rec := doReq(t, h.s, http.MethodPost, "/api/watches", map[string]any{
			"spec": map[string]any{"name": "x"}, "onTrip": map[string]any{"reroute": rule},
		}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	if n := len(h.ctl.calls("session/watch_start")); n != 0 {
		t.Fatalf("an invalid rule still started a watch")
	}
}

func TestStoppingAWatchDropsItsRule(t *testing.T) {
	h := newRoutingHarness(t)
	h.startRerouteWatch()
	if rec := doReq(t, h.s, http.MethodDelete, "/api/watches/studio/w1", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d", rec.Code)
	}
	if _, ok := h.f.ws.WatchRule("w1"); ok {
		t.Fatal("a stopped watch kept its reroute rule")
	}
}

func TestWorkspaceV7MigratesAndKeepsWatchRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.json")
	v7 := `{"version":7,"projects":["/p"],"agents":[{"id":"a1","project":"/p","ownerId":"local","origin":"ui"}]}`
	if err := os.WriteFile(path, []byte(v7), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(path)
	if backup, err := ws.Load(); err != nil || backup != "" {
		t.Fatalf("a v7 file was quarantined: %q, %v", backup, err)
	}
	if _, ok := ws.Agent("a1"); !ok {
		t.Fatal("v7 agent lost in migration")
	}
	rule := WatchRule{Reroute: &RerouteRule{Role: "implementer", Preset: "fast"}}
	if err := ws.PutWatchRule("w1", rule); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var onDisk struct {
		Version    int                  `json:"version"`
		WatchRules map[string]WatchRule `json:"watchRules"`
	}
	if err := json.Unmarshal(data, &onDisk); err != nil || onDisk.Version != workspaceVersion {
		t.Fatalf("on disk: version %d, %v", onDisk.Version, err)
	}
	reloaded := NewWorkspace(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.WatchRule("w1")
	if !ok || got.Reroute == nil || got.Reroute.Preset != "fast" {
		t.Fatalf("rule after reload = %+v ok=%v", got, ok)
	}
	if err := reloaded.DeleteWatchRule("w1"); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.DeleteWatchRule("never-existed"); err != nil {
		t.Fatalf("deleting an absent rule: %v", err)
	}
	if _, ok := reloaded.WatchRule("w1"); ok {
		t.Fatal("rule survived deletion")
	}
}

// listedWatch fetches GET /api/watches and returns the row with the id.
func listedWatch(t *testing.T, s *Server, id string) map[string]json.RawMessage {
	t.Helper()
	var rows []map[string]json.RawMessage
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/watches", nil, nil), &rows)
	for _, r := range rows {
		if string(r["id"]) == `"`+id+`"` {
			return r
		}
	}
	t.Fatalf("watch %s not listed: %v", id, rows)
	return nil
}

func TestWatchesListMergesTheStoredTripActions(t *testing.T) {
	h := newRoutingHarness(t)
	h.ctl.results["session/watch_list"] = map[string]any{"watches": []map[string]any{
		{"id": "w1", "name": "disk", "state": "watching"},
		{"id": "w9", "name": "other", "state": "watching"},
	}}
	rec := doReq(t, h.s, http.MethodPost, "/api/watches", map[string]any{
		"spec":   map[string]any{"name": "disk", "notify": false, "resume": true},
		"onTrip": map[string]any{"reroute": map[string]any{"role": "implementer", "preset": "fast"}},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}

	row := listedWatch(t, h.s, "w1")
	if string(row["notify"]) != "false" || string(row["resume"]) != "true" {
		t.Fatalf("notify/resume = %s/%s", row["notify"], row["resume"])
	}
	var onTrip struct {
		Notify  bool         `json:"notify"`
		Resume  bool         `json:"resume"`
		Reroute *RerouteRule `json:"reroute"`
	}
	if err := json.Unmarshal(row["onTrip"], &onTrip); err != nil {
		t.Fatal(err)
	}
	if onTrip.Notify || !onTrip.Resume || onTrip.Reroute == nil ||
		onTrip.Reroute.Role != "implementer" || onTrip.Reroute.Preset != "fast" {
		t.Fatalf("onTrip = %s", row["onTrip"])
	}
	// A watch the bridge never stored a rule for is left as the engine sent it.
	other := listedWatch(t, h.s, "w9")
	if _, ok := other["onTrip"]; ok {
		t.Fatalf("an unknown watch gained onTrip: %v", other)
	}

	// Once the reroute has fired, notify and resume stay listed; the spent
	// reroute does not.
	h.fire("fired")
	waitFor(t, 5*time.Second, "reroute applied", func() bool { return h.binding("implementer") == `{"preset":"fast"}` })
	row = listedWatch(t, h.s, "w1")
	if string(row["resume"]) != "true" || strings.Contains(string(row["onTrip"]), "reroute") {
		t.Fatalf("after firing: %v", row)
	}
}

func TestWatchesListDefaultsNotifyToTrue(t *testing.T) {
	h := newRoutingHarness(t)
	h.ctl.results["session/watch_list"] = map[string]any{"watches": []map[string]any{{"id": "w1", "name": "disk"}}}
	if rec := doReq(t, h.s, http.MethodPost, "/api/watches", map[string]any{"spec": map[string]any{"name": "disk"}}, nil); rec.Code != http.StatusOK {
		t.Fatalf("start = %d", rec.Code)
	}
	row := listedWatch(t, h.s, "w1")
	if string(row["notify"]) != "true" || string(row["resume"]) != "false" {
		t.Fatalf("notify/resume = %s/%s", row["notify"], row["resume"])
	}
}
