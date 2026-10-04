package bridge

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func networkDeltas(f *Fleet) []networkBlockDelta {
	var out []networkBlockDelta
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var d networkBlockDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "network_block" {
			out = append(out, d)
		}
	}
	return out
}

func fleetHasDelta(f *Fleet, kind, agent string) bool {
	for _, d := range networkDeltas(f) {
		if d.Kind == kind && d.AgentID == agent {
			return true
		}
	}
	return false
}

func rec(at time.Time, agent, ws, host, decision string, up, down int64, injected bool) EgressRecord {
	return EgressRecord{At: at.UnixMilli(), AgentID: agent, Workspace: ws, Host: host, Port: 443,
		Decision: decision, BytesUp: up, BytesDown: down, Injected: injected}
}

func TestNetLogAggregates(t *testing.T) {
	n := NewNetLog(t.TempDir())
	now := time.Now()
	err := n.Append([]EgressRecord{
		rec(now.Add(-3*time.Second), "a1", "dev", "Example.com", "allow", 10, 100, false),
		rec(now.Add(-2*time.Second), "a1", "dev", "example.com", "allow", 5, 50, true),
		rec(now.Add(-1*time.Second), "a2", "dev", "example.com", "block", 0, 0, false),
		rec(now, "a1", "ops", "other.org", "allow", 1, 1, false),
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := n.Hosts("dev", "")
	if len(rows) != 1 {
		t.Fatalf("workspace rows = %+v", rows)
	}
	r := rows[0]
	if r.Host != "example.com" || r.Requests != 3 || r.Blocked != 1 || r.BytesUp != 15 || r.BytesDown != 150 || !r.Injected || r.Decision != "block" {
		t.Fatalf("workspace aggregate = %+v", r)
	}
	if got := n.Hosts("", "a1"); len(got) != 2 || got[0].Host != "other.org" {
		t.Fatalf("agent rows (newest first) = %+v", got)
	}
	if got := n.Hosts("", ""); len(got) != 2 {
		t.Fatalf("fleet rows = %+v", got)
	}
	tot := n.Agents("")
	if len(tot) != 2 || tot[0].Agent != "a1" || tot[0].Hosts != 2 || tot[0].Requests != 3 || tot[1].Blocked != 1 {
		t.Fatalf("agent totals = %+v", tot)
	}
	if got := n.Agents("ops"); len(got) != 1 || got[0].Agent != "a1" {
		t.Fatalf("workspace-filtered totals = %+v", got)
	}
}

func TestNetLogRebuildsFromFilesAndCleansUp(t *testing.T) {
	state := t.TempDir()
	n := NewNetLog(state)
	now := time.Now()
	n.Append([]EgressRecord{rec(now, "a1", "dev", "keep.com", "allow", 1, 2, false)})
	dir := filepath.Join(state, "network")
	day := func(offset int) string { return dayName(now.AddDate(0, 0, -offset)) }
	write := func(name string, r EgressRecord) {
		b, _ := json.Marshal(r)
		os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o600)
	}
	write(day(3), rec(now.AddDate(0, 0, -3), "a1", "dev", "three-days.com", "allow", 1, 1, false))
	write(day(10), rec(now.AddDate(0, 0, -10), "a1", "dev", "ten-days.com", "allow", 1, 1, false))
	write(day(29), rec(now.AddDate(0, 0, -29), "a1", "dev", "old-but-kept.com", "allow", 1, 1, false))
	write(day(45), rec(now.AddDate(0, 0, -45), "a1", "dev", "ancient.com", "allow", 1, 1, false))
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	// A torn last line is skipped, not fatal.
	f, _ := os.OpenFile(filepath.Join(dir, day(3)), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"host":"torn`)
	f.Close()

	n2 := NewNetLog(state)
	hosts := map[string]bool{}
	for _, r := range n2.Hosts("dev", "") {
		hosts[r.Host] = true
	}
	if !hosts["keep.com"] || !hosts["three-days.com"] {
		t.Fatalf("recent files not rebuilt: %v", hosts)
	}
	if hosts["ten-days.com"] || hosts["old-but-kept.com"] || hosts["ancient.com"] {
		t.Fatalf("files outside the 7-day window were aggregated: %v", hosts)
	}
	if _, err := os.Stat(filepath.Join(dir, day(45))); !os.IsNotExist(err) {
		t.Error("a 45-day-old file survived cleanup")
	}
	for _, d := range []int{10, 29} {
		if _, err := os.Stat(filepath.Join(dir, day(d))); err != nil {
			t.Errorf("%d-day-old file was deleted early: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("cleanup removed a file that is not a day file")
	}
}

func TestNetLogRequestsAreTodaysNewestFirstAndCapped(t *testing.T) {
	n := NewNetLog(t.TempDir())
	now := time.Now()
	var batch []EgressRecord
	for i := 0; i < netLogMaxRequests+20; i++ {
		batch = append(batch, rec(now.Add(time.Duration(i)*time.Millisecond), "a1", "dev", "h.com", "allow", int64(i), 0, false))
	}
	batch = append(batch, rec(now, "a2", "ops", "x.com", "block", 0, 0, false))
	if err := n.Append(batch); err != nil {
		t.Fatal(err)
	}
	all := n.Requests("", "")
	if len(all) != netLogMaxRequests || all[0].Host != "x.com" {
		t.Fatalf("len %d first %+v", len(all), all[0])
	}
	got := n.Requests("dev", "a1")
	if len(got) != netLogMaxRequests || got[0].BytesUp < got[1].BytesUp {
		t.Fatalf("filtered = %d, first bytesUp %d, second %d", len(got), got[0].BytesUp, got[1].BytesUp)
	}
	if got := n.Requests("", "nobody"); got == nil || len(got) != 0 {
		t.Fatalf("empty result should be [], got %v", got)
	}
	// Yesterday's records are not in the requests view.
	n.Append([]EgressRecord{rec(now.AddDate(0, 0, -1), "a3", "dev", "old.com", "allow", 0, 0, false)})
	for _, r := range n.Requests("", "a3") {
		t.Fatalf("yesterday's record in requests: %+v", r)
	}
}

func TestNetworkHTTPViews(t *testing.T) {
	f := testFleetWithAudit(t)
	s := NewServer(f, "")
	f.netlog = NewNetLog(t.TempDir())
	now := time.Now()
	f.netlog.Append([]EgressRecord{
		rec(now, "a1", "dev", "api.example.com", "allow", 1, 1, true),
		rec(now, "a1", "dev", "granted.com", "allow", 1, 1, false),
		rec(now, "a1", "dev", "listed.com", "allow", 1, 1, false),
		rec(now, "a1", "dev", "evil.com", "block", 0, 0, false),
	})
	// No live agent: rules fall back to the recorded decision.
	var res struct {
		ProcessMode bool
		Rows        []netRow
	}
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/network?workspace=dev", nil, nil), &res)
	if !res.ProcessMode || len(res.Rows) != 4 {
		t.Fatalf("view = %+v", res)
	}
	byHost := map[string]string{}
	for _, r := range res.Rows {
		byHost[r.Host] = r.Rule
	}
	if byHost["evil.com"] != "blocked" || byHost["api.example.com"] != "injected" {
		t.Fatalf("fallback rules = %v", byHost)
	}
	// With a live agent the rule comes from its current policy.
	h := newEgressHost(f)
	h.agents["a1"] = &egressAgent{workspace: "dev"}
	h.agents["a1"].policy = EgressAgentPolicy{Mode: EgressModeAllowlist, Allow: []string{"listed.com"}, Grants: []string{"granted.com"},
		Inject: map[string]EgressInjection{"api.example.com": {Header: "X", Value: "v"}}}
	h.agents["a1"].policy.Allow = append(h.agents["a1"].policy.Allow, "api.example.com")
	h.mode = egressModeContainer
	f.egress = h
	for _, q := range []string{"/api/network?workspace=dev", "/api/network?agent=a1&view=hosts"} {
		decodeBody(t, doReq(t, s, http.MethodGet, q, nil, nil), &res)
		if res.ProcessMode {
			t.Fatal("container mode reported as process mode")
		}
		byHost = map[string]string{}
		for _, r := range res.Rows {
			byHost[r.Host] = r.Rule
		}
		want := map[string]string{"api.example.com": "injected", "granted.com": "granted", "listed.com": "allowlisted", "evil.com": "blocked"}
		for host, rule := range want {
			if byHost[host] != rule {
				t.Errorf("%s: %s rule = %q, want %q", q, host, byHost[host], rule)
			}
		}
	}
	var reqs []EgressRecord
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/network?view=requests&agent=a1", nil, nil), &reqs)
	if len(reqs) != 4 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var agents []AgentTotals
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/network?view=agents", nil, nil), &agents)
	if len(agents) != 1 || agents[0].Hosts != 4 || agents[0].Blocked != 1 {
		t.Fatalf("agents = %+v", agents)
	}
	if c := doReq(t, s, http.MethodGet, "/api/network?view=nope", nil, nil).Code; c != http.StatusBadRequest {
		t.Fatalf("bad view = %d", c)
	}
}

func TestNetworkHTTPDecisions(t *testing.T) {
	ctx := t.Context()
	f, _ := testEgressFleet(t, false)
	f.audit = NewAuditLog(t.TempDir())
	if err := f.StartEgress(ctx); err != nil {
		t.Fatal(err)
	}
	s := NewServer(f, "")
	f.ws.PutAgent(Agent{ID: "a1", Project: "/p", OwnerID: DefaultOwnerID})
	f.egress.register(ctx, "a1", "dev", EgressSpec{Mode: EgressModeAllowlist, Allow: []string{"example.com"}})
	post := func(body map[string]string) (int, string) {
		rec := doReq(t, s, http.MethodPost, "/api/network/decisions", body, nil)
		return rec.Code, rec.Body.String()
	}
	if c, b := post(map[string]string{"agentId": "a1", "host": "registry.npmjs.org", "decision": "allow-agent"}); c != 200 {
		t.Fatalf("allow-agent = %d %s", c, b)
	}
	pol := f.egress.snapshot().Agents["a1"]
	if len(pol.Grants) != 1 || pol.Grants[0] != "registry.npmjs.org" || !allowed(pol, "registry.npmjs.org") || allowed(pol, "evil.com") {
		t.Fatalf("grant not applied: %+v", pol)
	}
	if c, _ := post(map[string]string{"agentId": "a1", "host": "evil.com", "decision": "block"}); c != 200 {
		t.Fatalf("block = %d", c)
	}
	if pol := f.egress.snapshot().Agents["a1"]; allowed(pol, "evil.com") {
		t.Fatal("block changed the policy")
	}
	if c, b := post(map[string]string{"agentId": "a1", "host": "x.com", "decision": "add-to-workspace"}); c != http.StatusNotImplemented || !strings.Contains(b, "add_to_workspace_unsupported") {
		t.Fatalf("add-to-workspace without templates = %d %s", c, b)
	}
	f.addToWorkspace = func(_ *http.Request, a Agent, host string) (any, error) {
		return map[string]any{"draft": true, "agent": a.ID, "host": host}, nil
	}
	rec := doReq(t, s, http.MethodPost, "/api/network/decisions", map[string]string{"agentId": "a1", "host": "x.com", "decision": "add-to-workspace"}, nil)
	var out map[string]any
	decodeBody(t, rec, &out)
	if rec.Code != 200 || out["draft"] != true || out["host"] != "x.com" {
		t.Fatalf("add-to-workspace = %d %v", rec.Code, out)
	}
	for name, body := range map[string]map[string]string{
		"bad decision": {"agentId": "a1", "host": "x.com", "decision": "yolo"},
		"url as host":  {"agentId": "a1", "host": "https://x.com/a", "decision": "block"},
		"wildcard":     {"agentId": "a1", "host": "*.com", "decision": "allow-agent"},
		"empty host":   {"agentId": "a1", "host": "", "decision": "block"},
	} {
		if c, _ := post(body); c != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, c)
		}
	}
	if c, _ := post(map[string]string{"agentId": "ghost", "host": "x.com", "decision": "block"}); c != http.StatusNotFound {
		t.Errorf("unknown agent = %d", c)
	}
	f.ws.PutAgent(Agent{ID: "a2", Project: "/p", OwnerID: DefaultOwnerID})
	if c, _ := post(map[string]string{"agentId": "a2", "host": "x.com", "decision": "allow-agent"}); c != http.StatusConflict {
		t.Errorf("allow-agent for an unproxied agent = %d", c)
	}
	n := 0
	for _, e := range auditTail(t, f) {
		if e.Event == AuditNetworkDecision {
			n++
		}
	}
	if n != 3 { // allow-agent, block, add-to-workspace
		t.Fatalf("network_decision audit entries = %d, want 3", n)
	}
}

func TestBlockedDeltasAreDeduplicated(t *testing.T) {
	f := testFleetWithAudit(t)
	f.noteBlocked("a1", "Evil.com")
	f.noteBlocked("a1", "evil.com.")
	f.noteBlocked("a1", "other.com")
	f.noteBlocked("a2", "evil.com")
	ds := networkDeltas(f)
	if len(ds) != 3 {
		t.Fatalf("deltas = %+v", ds)
	}
	if ds[0].SessionID != "a1" || ds[0].AgentID != "a1" || ds[0].Host != "evil.com" || ds[0].At == 0 {
		t.Fatalf("delta = %+v", ds[0])
	}
	// After ten minutes the same pair notifies again.
	f.blockedMu.Lock()
	f.blockedSeen["a1|evil.com"] = time.Now().Add(-11 * time.Minute)
	f.blockedMu.Unlock()
	f.noteBlocked("a1", "evil.com")
	if len(networkDeltas(f)) != 4 {
		t.Fatalf("expired entry did not notify again: %d", len(networkDeltas(f)))
	}
}
