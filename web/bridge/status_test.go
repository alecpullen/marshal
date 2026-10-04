package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

const statusSecretOutput = "AKIA-SECRET-TOOL-OUTPUT-/etc/shadow"

func statusStack() map[string]any {
	return map[string]any{
		"roots": []string{"t1"},
		"nodes": []any{
			map[string]any{"id": "t1", "kind": "turn"},
			map[string]any{"id": "k1", "kind": "task", "task": map[string]any{"todoId": "1", "content": "Write the parser", "status": "completed", "index": 1, "total": 2, "workMs": 99}},
			map[string]any{"id": "k2", "kind": "task", "task": map[string]any{"todoId": "2", "content": "Wire it up", "status": "in_progress", "index": 2, "total": 2}},
			map[string]any{"id": "s1", "kind": "step", "step": map[string]any{"headline": "Reading the config", "rest": "private narration " + statusSecretOutput}},
			map[string]any{"id": "s2", "kind": "step", "live": true, "step": map[string]any{"headline": "Running the tests", "rest": statusSecretOutput}},
			map[string]any{"id": "c1", "kind": "tool", "tool": map[string]any{"name": "shell", "output": statusSecretOutput, "args": statusSecretOutput}},
			map[string]any{"id": "m1", "kind": "final", "message": map[string]any{"role": "assistant", "content": statusSecretOutput}},
		},
	}
}

// statusFixture is a fleet with one running agent whose stack is full of
// things a status page must never show.
func statusFixture(t *testing.T) (*Server, *Fleet, string) {
	t.Helper()
	f, agentOf := recipeFleet(t, func(fa *fakeAgent) {
		fa.results["session/stack"] = statusStack()
	})
	id, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{Name: "<b>Nightly</b>"})
	if err != nil {
		t.Fatal(err)
	}
	_ = agentOf
	return NewServer(f, "secret-token"), f, id
}

func createLink(t *testing.T, s *Server, agentID string, ttl int) (id, path string) {
	t.Helper()
	body := map[string]any{"agentId": agentID}
	if ttl != 0 {
		body["ttlHours"] = ttl
	}
	rec := doReq(t, s, http.MethodPost, "/api/status-links", body, map[string]string{"Authorization": "Bearer secret-token"})
	var out struct {
		ID, URL   string
		ExpiresAt time.Time
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &out)
	return out.ID, out.URL
}

func getPublic(t *testing.T, s *Server, path, ip string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":4000"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestStatusLinkCreateListRevoke(t *testing.T) {
	s, f, id := statusFixture(t)
	auth := map[string]string{"Authorization": "Bearer secret-token"}
	linkID, path := createLink(t, s, id, 0)
	if !strings.HasPrefix(path, "/s/") || len(path) < 40 {
		t.Fatalf("url = %q", path)
	}
	token := strings.TrimPrefix(path, "/s/")
	if findEvent(auditTail(t, f), AuditStatusLinkCreated) == nil {
		t.Error("create not audited")
	}
	// The default lifetime is a week.
	var list []map[string]any
	rec := doReq(t, s, http.MethodGet, "/api/status-links", nil, auth)
	decodeBody(t, rec, &list)
	if len(list) != 1 || list[0]["id"] != linkID || list[0]["agentId"] != id {
		t.Fatalf("list = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), token) || strings.Contains(strings.ToLower(rec.Body.String()), "hash") {
		t.Fatalf("list leaks the token or its hash: %s", rec.Body.String())
	}
	exp, _ := time.Parse(time.RFC3339, list[0]["expiresAt"].(string))
	if d := time.Until(exp); d < 167*time.Hour || d > 169*time.Hour {
		t.Errorf("default ttl = %v", d)
	}
	// The token is stored hashed.
	for _, l := range f.ws.StatusLinks() {
		if l.TokenHash != HashToken(token) || l.TokenHash == token {
			t.Errorf("stored link = %+v", l)
		}
	}
	if rec := getPublic(t, s, path+"/data", "10.0.0.1"); rec.Code != http.StatusOK {
		t.Fatalf("data = %d", rec.Code)
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/status-links/"+linkID, nil, auth); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if findEvent(auditTail(t, f), AuditStatusLinkRevoked) == nil {
		t.Error("revoke not audited")
	}
	for _, p := range []string{path, path + "/data"} {
		if rec := getPublic(t, s, p, "10.0.0.1"); rec.Code != http.StatusNotFound {
			t.Errorf("revoked %s = %d", p, rec.Code)
		}
	}
	if rec := doReq(t, s, http.MethodDelete, "/api/status-links/zzz", nil, auth); rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown = %d", rec.Code)
	}
}

func TestStatusLinkCreateValidation(t *testing.T) {
	s, _, id := statusFixture(t)
	auth := map[string]string{"Authorization": "Bearer secret-token"}
	for name, body := range map[string]map[string]any{
		"unknown agent": {"agentId": "ghost"},
		"too long":      {"agentId": id, "ttlHours": 721},
		"negative":      {"agentId": id, "ttlHours": -1},
	} {
		if rec := doReq(t, s, http.MethodPost, "/api/status-links", body, auth); rec.Code < 400 {
			t.Errorf("%s accepted: %d", name, rec.Code)
		}
	}
	if rec := doReq(t, s, http.MethodPost, "/api/status-links", map[string]any{"agentId": id, "ttlHours": 720}, auth); rec.Code != http.StatusCreated {
		t.Errorf("max ttl = %d", rec.Code)
	}
	// Managing links needs the bearer token; the public page does not.
	if rec := doReq(t, s, http.MethodPost, "/api/status-links", map[string]any{"agentId": id}, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("create without auth = %d", rec.Code)
	}
}

func TestStatusPublicNeedsNoAuthButAValidToken(t *testing.T) {
	s, f, id := statusFixture(t)
	_, path := createLink(t, s, id, 1)
	if rec := getPublic(t, s, path, "10.0.0.2"); rec.Code != http.StatusOK {
		t.Fatalf("page = %d", rec.Code)
	}
	for name, p := range map[string]string{
		"unknown":     "/s/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"empty":       "/s/",
		"extra path":  path + "/other",
		"truncated":   path[:len(path)-1],
		"extended":    path + "x",
		"upper-cased": "/s/" + strings.ToUpper(strings.TrimPrefix(path, "/s/")),
	} {
		if rec := getPublic(t, s, p, "10.0.0.2"); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, rec.Code)
		}
	}
	// Expired links are the same 404 as unknown ones.
	links := f.ws.StatusLinks()
	links[0].ExpiresAt = f.now().Add(-time.Second)
	_ = f.ws.PutStatusLink(links[0])
	for _, p := range []string{path, path + "/data"} {
		rec := getPublic(t, s, p, "10.0.0.2")
		if rec.Code != http.StatusNotFound {
			t.Errorf("expired %s = %d", p, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "expired") || strings.Contains(body, "revoked") {
			t.Errorf("404 body distinguishes the reason: %q", body)
		}
	}
	// Only GET.
	req := httptest.NewRequest(http.MethodPost, path, nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST = %d", rec.Code)
	}
}

func TestStatusPublicSecurityHeaders(t *testing.T) {
	s, _, id := statusFixture(t)
	_, path := createLink(t, s, id, 1)
	for _, p := range []string{path, path + "/data", "/s/nope"} {
		h := getPublic(t, s, p, "10.0.0.3").Header()
		if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s headers = %v", p, h)
		}
	}
}

func TestStatusPublicRateLimit(t *testing.T) {
	s, f, id := statusFixture(t)
	_, path := createLink(t, s, id, 1)
	now := time.Now().UTC()
	clock := now
	f.clock = func() time.Time { return clock }
	for i := 0; i < statusRateBurst; i++ {
		if rec := getPublic(t, s, path+"/data", "10.9.9.9"); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d", i+1, rec.Code)
		}
	}
	if rec := getPublic(t, s, path+"/data", "10.9.9.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request 61 = %d, want 429", rec.Code)
	}
	// Another client is unaffected, and the bucket refills at one a second.
	if rec := getPublic(t, s, path+"/data", "10.9.9.8"); rec.Code != http.StatusOK {
		t.Errorf("other IP = %d", rec.Code)
	}
	clock = now.Add(2 * time.Second)
	if rec := getPublic(t, s, path+"/data", "10.9.9.9"); rec.Code != http.StatusOK {
		t.Errorf("after refill = %d", rec.Code)
	}
	// Unknown tokens count against the limit too.
	for i := 0; i < statusRateBurst+5; i++ {
		getPublic(t, s, "/s/guess", "10.7.7.7")
	}
	if rec := getPublic(t, s, "/s/guess", "10.7.7.7"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("guessing was not limited: %d", rec.Code)
	}
}

func TestStatusLimiterPrunesIdleBuckets(t *testing.T) {
	var l statusLimiter
	now := time.Now()
	l.allow("a", now)
	l.allow("b", now)
	l.allow("c", now.Add(statusPruneEvery+time.Minute))
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) != 1 {
		t.Errorf("buckets = %d, want only the live one", len(l.buckets))
	}
}

// keyPaths lists every JSON object key path in v, sorted.
func keyPaths(prefix string, v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			p := prefix + "." + k
			*out = append(*out, p)
			keyPaths(p, vv, out)
		}
	case []any:
		for _, vv := range x {
			keyPaths(prefix+"[]", vv, out)
		}
	}
}

func TestStatusDataIsExactlyTheWhitelist(t *testing.T) {
	s, f, id := statusFixture(t)
	_, path := createLink(t, s, id, 1)
	rec := getPublic(t, s, path+"/data", "10.0.0.5")
	if rec.Code != http.StatusOK {
		t.Fatalf("data = %d %s", rec.Code, rec.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	var keys []string
	keyPaths("", data, &keys)
	keys = dedupe(keys)
	want := []string{".elapsedMs", ".headline", ".name", ".progress", ".progress.done", ".progress.total",
		".project", ".status", ".tasks", ".tasks[].content", ".tasks[].index", ".tasks[].status"}
	if strings.Join(keys, ",") != strings.Join(dedupe(want), ",") {
		t.Fatalf("keys =\n%v\nwant\n%v", keys, want)
	}
	body := rec.Body.String()
	for _, secret := range []string{statusSecretOutput, "private narration", "shadow", "shell"} {
		if strings.Contains(body, secret) {
			t.Errorf("data leaks %q: %s", secret, body)
		}
	}
	a, _ := f.ws.Agent(id)
	if strings.Contains(body, a.Project) {
		t.Errorf("data leaks the project path: %s", body)
	}
	if data["project"] != baseName(a.Project) || data["name"] != "<b>Nightly</b>" {
		t.Errorf("name/project = %v / %v", data["name"], data["project"])
	}
	if data["headline"] != "Running the tests" {
		t.Errorf("headline = %v, want the live step's", data["headline"])
	}
	prog := data["progress"].(map[string]any)
	if prog["done"] != float64(1) || prog["total"] != float64(2) {
		t.Errorf("progress = %v", prog)
	}
	tasks := data["tasks"].([]any)
	if len(tasks) != 2 || tasks[0].(map[string]any)["status"] != "done" || tasks[1].(map[string]any)["status"] != "active" {
		t.Errorf("tasks = %v", tasks)
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func TestStatusDataPrefersThePlanRun(t *testing.T) {
	f, _ := recipeFleet(t, func(fa *fakeAgent) {
		fa.results["session/stack"] = map[string]any{"roots": []string{}, "nodes": []any{}}
		fa.results["session/run"] = map[string]any{"kind": "sdd", "sdd": map[string]any{
			"totalTasks": 3, "doneTasks": 3, "finished": true, "succeeded": true, "ledgerPath": "/secret/ledger", "error": statusSecretOutput,
			"tasks": []any{
				map[string]any{"n": 1, "title": "One", "status": "done", "stages": []any{map[string]any{"detail": statusSecretOutput}}},
				map[string]any{"n": 2, "title": "Two", "status": "done"},
				map[string]any{"n": 3, "title": "Three", "status": "done"},
			}}}
	})
	id, err := f.Spawn(t.Context(), t.TempDir(), SpawnOptions{Name: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(f, "")
	_, path := createLink(t, s, id, 1)
	rec := getPublic(t, s, path+"/data", "10.0.0.6")
	var data statusData
	decodeBody(t, rec, &data)
	if data.Progress.Done != 3 || data.Progress.Total != 3 || len(data.Tasks) != 3 || data.Tasks[2].Content != "Three" || data.Status != "finished" {
		t.Fatalf("data = %+v", data)
	}
	if strings.Contains(rec.Body.String(), "/secret") || strings.Contains(rec.Body.String(), statusSecretOutput) {
		t.Errorf("data leaks run internals: %s", rec.Body.String())
	}
}

func TestStatusPageEscapesTheAgentName(t *testing.T) {
	s, _, id := statusFixture(t) // the agent is named <b>Nightly</b>
	_, path := createLink(t, s, id, 1)
	rec := getPublic(t, s, path, "10.0.0.7")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if strings.Contains(body, "<b>Nightly</b>") || !strings.Contains(body, "&lt;b&gt;Nightly&lt;/b&gt;") {
		t.Errorf("agent name not escaped: %s", body[:min(len(body), 400)])
	}
	if strings.Contains(body, "/api/") || strings.Contains(body, "href=") {
		t.Error("the page links to Studio")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("csp = %q", csp)
	}
}

func TestStatusPausedAgentStillServes(t *testing.T) {
	s, f, id := statusFixture(t)
	_, path := createLink(t, s, id, 1)
	if err := f.Pause(id); err != nil {
		t.Fatal(err)
	}
	rec := getPublic(t, s, path+"/data", "10.0.0.8")
	var data statusData
	decodeBody(t, rec, &data)
	if rec.Code != http.StatusOK || data.Status != "idle" || data.Name == "" || data.Tasks == nil {
		t.Fatalf("paused data = %d %s", rec.Code, rec.Body.String())
	}
}
