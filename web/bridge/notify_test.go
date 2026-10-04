package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memSecrets is an in-memory SecretProvider.
type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Name() string { return "mem" }
func (s *memSecrets) Get(_ context.Context, _, ref string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[ref]; ok {
		return v, nil
	}
	return nil, ErrSecretNotFound
}
func (s *memSecrets) Put(_ context.Context, _, ref string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]byte{}
	}
	s.m[ref] = v
	return nil
}
func (s *memSecrets) Delete(context.Context, string, string) error           { return nil }
func (s *memSecrets) List(context.Context, string, string) ([]string, error) { return nil, nil }

type received struct {
	body notification
	sig  string
	raw  []byte
}

type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []received
	hits atomic.Int32
	// status, when set, answers the nth hit (1-based).
	status func(hit int) int
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hit := int(r.hits.Add(1))
		raw, _ := io.ReadAll(req.Body)
		if r.status != nil {
			if code := r.status(hit); code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
		}
		var n notification
		_ = json.Unmarshal(raw, &n)
		r.mu.Lock()
		r.got = append(r.got, received{body: n, sig: req.Header.Get("X-Marshal-Signature"), raw: raw})
		r.mu.Unlock()
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, g := range r.got {
		out = append(out, g.body.Event)
	}
	return out
}

func (r *receiver) wait(t *testing.T, n int) []received {
	t.Helper()
	waitFor(t, 5*time.Second, "webhook deliveries", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.got) >= n
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.got...)
}

// fastBackoff shortens the retry waits of f's notifier. Call it before any
// event is emitted.
func fastBackoff(f *Fleet) {
	f.notify.backoff = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}
}

func notifyFleet(t *testing.T, hooks ...Webhook) *Fleet {
	t.Helper()
	f := testFleet(t)
	if err := f.ws.SetNotifications(NotifyConfig{Webhooks: hooks}); err != nil {
		t.Fatal(err)
	}
	f.SetPublicURLBase("https://studio.example/")
	fastBackoff(f)
	return f
}

func allEvents() []string {
	return []string{NotifyNeedsYou, NotifyRunFinished, NotifyBudget, NotifyAutomation, NotifyWatchFired, NotifyNetworkBlock}
}

func TestNotifyMapsEachDeltaKind(t *testing.T) {
	rc := newReceiver(t)
	f := notifyFleet(t, Webhook{ID: "w1", URL: rc.srv.URL, Events: allEvents()})
	_ = f.ws.PutAgent(Agent{ID: "a1", Name: "builder", Project: "/p", CreatedAt: time.Now(), OwnerID: DefaultOwnerID, Origin: OriginUI})

	deltas := []struct {
		name  string
		delta any
		want  string
	}{
		{"pending", fleetDelta{Kind: "pending", SessionID: "a1", PendingKind: "approval"}, NotifyNeedsYou},
		{"run finished", fleetDelta{Kind: "run", SessionID: "a1", AgentID: "a1",
			Run: json.RawMessage(`{"kind":"sdd","sdd":{"finished":true,"succeeded":true,"planName":"p","endedAt":9}}`)}, NotifyRunFinished},
		{"budget", budgetDelta{Kind: "budget", SessionID: "a1", Scope: "agent", AgentID: "a1", SpentUSD: 2, CapUSD: 1, Action: "pause"}, NotifyBudget},
		{"automation", map[string]any{"kind": "automation", "title": "Reviewed PR 7"}, NotifyAutomation},
		{"watch fired", fleetDelta{Kind: "watch", SessionID: studioOwner, AgentID: studioOwner,
			Watch: json.RawMessage(`{"watchId":"w","name":"cost","state":"fired"}`)}, NotifyWatchFired},
		{"network block", networkBlockDelta{Kind: "network_block", SessionID: "a1", AgentID: "a1", Host: "evil.example"}, NotifyNetworkBlock},
	}
	for _, d := range deltas {
		f.emit(d.delta)
	}
	got := rc.wait(t, len(deltas))
	byEvent := map[string]notification{}
	for _, g := range got {
		byEvent[g.body.Event] = g.body
	}
	for _, d := range deltas {
		n, ok := byEvent[d.want]
		if !ok {
			t.Errorf("%s: no %s notification (got %v)", d.name, d.want, rc.events())
			continue
		}
		if n.Title == "" || n.Text == "" || n.At == 0 {
			t.Errorf("%s: incomplete %+v", d.name, n)
		}
	}
	if n := byEvent[NotifyNeedsYou]; n.AgentID != "a1" || n.URL != "https://studio.example/#chat/a1" || !strings.Contains(n.Title, "builder") {
		t.Errorf("needs_you = %+v", n)
	}
	if n := byEvent[NotifyWatchFired]; n.AgentID != "" || n.URL != "https://studio.example/" {
		t.Errorf("a Studio watch is not an agent: %+v", n)
	}
	if n := byEvent[NotifyNetworkBlock]; !strings.Contains(n.Text, "evil.example") {
		t.Errorf("network_block = %+v", n)
	}
}

func TestNotifyIgnoresDeltasThatAreNotEvents(t *testing.T) {
	rc := newReceiver(t)
	f := notifyFleet(t, Webhook{ID: "w1", URL: rc.srv.URL, Events: allEvents()})
	for _, d := range []any{
		fleetDelta{Kind: "activity", SessionID: "a1", Activity: "read"},
		fleetDelta{Kind: "telemetry", SessionID: "a1"},
		fleetDelta{Kind: "run", SessionID: "a1", Run: json.RawMessage(`{"kind":"sdd","sdd":{"finished":false}}`)},
		fleetDelta{Kind: "watch", SessionID: studioOwner, Watch: json.RawMessage(`{"name":"x","state":"armed"}`)},
		map[string]any{"kind": "project_removed"},
	} {
		f.emit(d)
	}
	// A finished run is notified once, however often its digest repeats.
	fin := fleetDelta{Kind: "run", SessionID: "a1", AgentID: "a1", Run: json.RawMessage(`{"kind":"sdd","sdd":{"finished":true,"planName":"p","endedAt":5}}`)}
	f.emit(fin)
	f.emit(fin)
	rc.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	if got := rc.events(); len(got) != 1 || got[0] != NotifyRunFinished {
		t.Fatalf("events = %v", got)
	}
}

func TestNotifyOnlyDeliversSelectedEvents(t *testing.T) {
	only := newReceiver(t)
	all := newReceiver(t)
	f := notifyFleet(t,
		Webhook{ID: "w1", URL: only.srv.URL, Events: []string{NotifyBudget}},
		Webhook{ID: "w2", URL: all.srv.URL, Events: allEvents()})
	f.emit(fleetDelta{Kind: "pending", SessionID: "a1", PendingKind: "question"})
	f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	all.wait(t, 2)
	only.wait(t, 1)
	time.Sleep(100 * time.Millisecond)
	if got := only.events(); len(got) != 1 || got[0] != NotifyBudget {
		t.Errorf("selective webhook got %v", got)
	}
}

func TestNotifySignatureVerifies(t *testing.T) {
	rc := newReceiver(t)
	f := notifyFleet(t,
		Webhook{ID: "w1", URL: rc.srv.URL, SecretRef: "vault:hooks/w1", Events: allEvents()})
	secrets := &memSecrets{}
	_ = secrets.Put(t.Context(), DefaultOwnerID, "hooks/w1", []byte("s3cret"))
	f.SetSecrets(secrets)
	f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	got := rc.wait(t, 1)[0]
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(got.raw)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); got.sig != want {
		t.Fatalf("signature = %q, want %q", got.sig, want)
	}
}

func TestNotifyUnsignedWithoutASecretAndUnsentWhenTheSecretIsGone(t *testing.T) {
	plain := newReceiver(t)
	broken := newReceiver(t)
	f := notifyFleet(t,
		Webhook{ID: "w1", URL: plain.srv.URL, Events: allEvents()},
		Webhook{ID: "w2", URL: broken.srv.URL, SecretRef: "vault:hooks/gone", Events: allEvents()})
	f.SetSecrets(&memSecrets{})
	f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	if got := plain.wait(t, 1)[0]; got.sig != "" {
		t.Errorf("unsigned webhook carried a signature %q", got.sig)
	}
	time.Sleep(100 * time.Millisecond)
	if n := broken.hits.Load(); n != 0 {
		t.Errorf("a webhook whose secret is missing was sent %d unsigned requests", n)
	}
}

func TestNotifyRetriesAfterAServerError(t *testing.T) {
	rc := newReceiver(t)
	rc.status = func(hit int) int {
		if hit <= 2 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	}
	f := notifyFleet(t, Webhook{ID: "w1", URL: rc.srv.URL, Events: allEvents()})
	f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	rc.wait(t, 1)
	if n := rc.hits.Load(); n != 3 {
		t.Errorf("hits = %d, want 3 (two failures, one success)", n)
	}
}

func TestNotifyGivesUpAfterThreeRetries(t *testing.T) {
	rc := newReceiver(t)
	rc.status = func(int) int { return http.StatusBadGateway }
	f := notifyFleet(t, Webhook{ID: "w1", URL: rc.srv.URL, Events: allEvents()})
	f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	waitFor(t, 5*time.Second, "four attempts", func() bool { return rc.hits.Load() >= 4 })
	time.Sleep(100 * time.Millisecond)
	if n := rc.hits.Load(); n != 4 {
		t.Errorf("hits = %d, want 4 (one try, three retries)", n)
	}
}

func TestNotifyEmitNeverBlocksOnAHangingReceiver(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	f := notifyFleet(t, Webhook{ID: "w1", URL: srv.URL, Events: allEvents()})
	start := time.Now()
	for i := 0; i < notifyQueueSize+notifyMaxInFlight+100; i++ {
		f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("emit blocked for %v behind a hung receiver", d)
	}
}

func TestNotifyRoutes(t *testing.T) {
	rc := newReceiver(t)
	f := testFleet(t)
	fastBackoff(f)
	f.audit = NewAuditLog(t.TempDir())
	s := NewServer(f, "", "https://studio.example")

	put := func(body any) int { return doReq(t, s, http.MethodPut, "/api/notifications", body, nil).Code }
	good := map[string]any{"webhooks": []map[string]any{{"id": "w1", "url": rc.srv.URL, "events": []string{NotifyBudget}}}}
	for name, body := range map[string]any{
		"ftp url":       map[string]any{"webhooks": []map[string]any{{"id": "w", "url": "ftp://x/y", "events": []string{}}}},
		"no host":       map[string]any{"webhooks": []map[string]any{{"id": "w", "url": "http://", "events": []string{}}}},
		"unknown event": map[string]any{"webhooks": []map[string]any{{"id": "w", "url": rc.srv.URL, "events": []string{"nope"}}}},
		"bad secretRef": map[string]any{"webhooks": []map[string]any{{"id": "w", "url": rc.srv.URL, "secretRef": "../x", "events": []string{}}}},
		"duplicate id":  map[string]any{"webhooks": []map[string]any{{"id": "w", "url": rc.srv.URL}, {"id": "w", "url": rc.srv.URL}}},
	} {
		if code := put(body); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code := put(good); code != http.StatusOK {
		t.Fatalf("put = %d", code)
	}
	if findEvent(auditTail(t, f), AuditNotificationsSaved) == nil {
		t.Error("save not audited")
	}
	var cfg NotifyConfig
	decodeBody(t, doReq(t, s, http.MethodGet, "/api/notifications", nil, nil), &cfg)
	if len(cfg.Webhooks) != 1 || cfg.Webhooks[0].ID != "w1" {
		t.Fatalf("get = %+v", cfg)
	}

	// The test event goes out whatever the webhook subscribed to.
	rec := doReq(t, s, http.MethodPost, "/api/notifications/test", nil, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("test = %d", rec.Code)
	}
	got := rc.wait(t, 1)[0].body
	if got.Event != "test" || got.URL != "https://studio.example/" {
		t.Errorf("test notification = %+v", got)
	}
}

func TestNotifyEmptyConfigIsAnEmptyList(t *testing.T) {
	f := testFleet(t)
	rec := doReq(t, NewServer(f, ""), http.MethodGet, "/api/notifications", nil, nil)
	if strings.TrimSpace(rec.Body.String()) != `{"webhooks":[]}` {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestNotifyDoesNotFollowRedirects(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { internalHits.Add(1) }))
	t.Cleanup(internal.Close)
	var hooks atomic.Int32
	bouncer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hooks.Add(1)
		http.Redirect(w, r, internal.URL, http.StatusTemporaryRedirect) // 307 keeps the POST
	}))
	t.Cleanup(bouncer.Close)
	f := notifyFleet(t, Webhook{ID: "w1", URL: bouncer.URL, Events: allEvents()})
	f.emit(budgetDelta{Kind: "budget", Scope: "daily", Action: "warn"})
	waitFor(t, 5*time.Second, "delivery attempts", func() bool { return hooks.Load() >= 4 })
	time.Sleep(100 * time.Millisecond)
	if n := internalHits.Load(); n != 0 {
		t.Fatalf("the redirect target was contacted %d times", n)
	}
}
