package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Notification event kinds a webhook can subscribe to.
const (
	NotifyNeedsYou     = "needs_you"
	NotifyRunFinished  = "run_finished"
	NotifyBudget       = "budget"
	NotifyAutomation   = "automation"
	NotifyWatchFired   = "watch_fired"
	NotifyNetworkBlock = "network_block"
	// notifyTest is sent by POST /api/notifications/test, to every webhook.
	notifyTest = "test"
)

var notifyEvents = map[string]bool{
	NotifyNeedsYou: true, NotifyRunFinished: true, NotifyBudget: true,
	NotifyAutomation: true, NotifyWatchFired: true, NotifyNetworkBlock: true,
}

const (
	notifyQueueSize     = 256
	notifyMaxInFlight   = 64
	notifyPostTimeout   = 10 * time.Second
	notifySecretTimeout = 5 * time.Second
)

// defaultNotifyBackoff is the wait before each retry of a delivery.
var defaultNotifyBackoff = []time.Duration{2 * time.Second, 8 * time.Second, 30 * time.Second}

var errInvalidNotify = errors.New("bridge: invalid notification config")

// Webhook is one outbound notification target.
type Webhook struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// SecretRef is a vault: reference to the HMAC signing secret. The
	// secret itself is never stored here or returned.
	SecretRef string   `json:"secretRef,omitempty"`
	Events    []string `json:"events"`
}

// NotifyConfig is the notifications section of fleet.json.
type NotifyConfig struct {
	Webhooks []Webhook `json:"webhooks"`
}

func (c NotifyConfig) clone() NotifyConfig {
	out := NotifyConfig{}
	for _, w := range c.Webhooks {
		w.Events = append([]string(nil), w.Events...)
		out.Webhooks = append(out.Webhooks, w)
	}
	return out
}

// Validate checks urls, events and secret refs.
func (c NotifyConfig) Validate() error {
	seen := map[string]bool{}
	for i, w := range c.Webhooks {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("%w: webhook %d: "+format, append([]any{errInvalidNotify, i + 1}, a...)...)
		}
		u, err := url.Parse(w.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return bad("url must be an http or https URL")
		}
		if w.ID == "" || seen[w.ID] {
			return bad("id must be set and unique")
		}
		seen[w.ID] = true
		if w.SecretRef != "" {
			if _, err := ParseSecretRef(w.SecretRef); err != nil {
				return bad("secretRef: %v", err)
			}
		}
		for _, e := range w.Events {
			if !notifyEvents[e] {
				return bad("unknown event %q", e)
			}
		}
	}
	return nil
}

// Notifications returns the webhook configuration.
func (w *Workspace) Notifications() NotifyConfig {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.notifications.clone()
}

// SetNotifications replaces the webhook configuration.
func (w *Workspace) SetNotifications(c NotifyConfig) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.notifications = c.clone()
	return w.save()
}

// notification is the JSON body a webhook receives. `text` also suits a
// Slack-compatible incoming webhook.
type notification struct {
	Event   string `json:"event"`
	At      int64  `json:"at"`
	AgentID string `json:"agentId,omitempty"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	URL     string `json:"url"`
}

type notifier struct {
	f     *Fleet
	queue chan notifyJob
	sem   chan struct{}
	once  sync.Once
	// dropped counts notifications refused by a full queue.
	dropped atomic.Int64
	// client posts deliveries; nil means a plain client with the timeout.
	client *http.Client
	// backoff is the wait before each retry; tests shorten it.
	backoff []time.Duration

	mu       sync.Mutex
	finished map[string]bool // run_finished dedupe
}

type notifyJob struct {
	hook Webhook
	body notification
}

func newNotifier(f *Fleet) *notifier {
	return &notifier{f: f, queue: make(chan notifyJob, notifyQueueSize),
		sem: make(chan struct{}, notifyMaxInFlight), finished: map[string]bool{},
		backoff: defaultNotifyBackoff}
}

// SetPublicURLBase sets the externally reachable base URL used for the
// links in notifications. Empty yields relative links.
func (f *Fleet) SetPublicURLBase(base string) {
	f.notifyMu.Lock()
	f.publicURLBase = strings.TrimRight(base, "/")
	f.notifyMu.Unlock()
}

func (f *Fleet) notifyURL(agentID string) string {
	f.notifyMu.Lock()
	base := f.publicURLBase
	f.notifyMu.Unlock()
	if agentID == "" {
		return base + "/"
	}
	return base + "/#chat/" + url.PathEscape(agentID)
}

// emit appends a delta to the fleet log and offers it to the webhooks.
// Every site that appends to the fleet stream goes through here.
func (f *Fleet) emit(d any) {
	_, _ = f.fleetLog.Append(fleetStreamKey, d)
	f.notify.consider(d)
}

// consider maps a fleet delta to a notification event and queues it for
// each webhook subscribed to it. It never blocks.
func (n *notifier) consider(d any) {
	cfg := n.f.ws.Notifications()
	if len(cfg.Webhooks) == 0 {
		return
	}
	msg, ok := n.fromDelta(d)
	if !ok {
		return
	}
	for _, h := range cfg.Webhooks {
		if !containsString(h.Events, msg.Event) {
			continue
		}
		n.enqueue(h, msg)
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (n *notifier) enqueue(h Webhook, msg notification) {
	n.once.Do(func() { go n.dispatch() })
	select {
	case n.queue <- notifyJob{hook: h, body: msg}:
	default:
		// Log the first drop of a burst and every hundredth after, so a
		// stalled receiver cannot flood the log as well as the queue.
		if n := n.dropped.Add(1); n%100 == 1 {
			slog.Default().Warn("webbridge: notification queue full, dropping", "event", msg.Event, "webhook", h.ID, "dropped", n)
		}
	}
}

// dispatch hands each queued job to its own goroutine, bounded by sem.
func (n *notifier) dispatch() {
	for {
		select {
		case <-n.f.done:
			return
		case job := <-n.queue:
			select {
			case n.sem <- struct{}{}:
			case <-n.f.done:
				return
			}
			go func() {
				defer func() { <-n.sem }()
				n.deliver(job)
			}()
		}
	}
}

func (n *notifier) fromDelta(d any) (notification, bool) {
	raw, err := json.Marshal(d)
	if err != nil {
		return notification{}, false
	}
	var p struct {
		Kind        string          `json:"kind"`
		SessionID   string          `json:"sessionId"`
		AgentID     string          `json:"agentId"`
		PendingKind string          `json:"pendingKind"`
		Run         json.RawMessage `json:"run"`
		Event       json.RawMessage `json:"event"`
		Scope       string          `json:"scope"`
		Action      string          `json:"action"`
		SpentUSD    float64         `json:"spentUsd"`
		CapUSD      float64         `json:"capUsd"`
		Host        string          `json:"host"`
		Title       string          `json:"title"`
		Text        string          `json:"text"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return notification{}, false
	}
	agent := p.AgentID
	if agent == "" {
		agent = p.SessionID
	}
	if agent == studioOwner {
		agent = ""
	}
	name := agent
	if a, ok := n.f.ws.Agent(agent); ok && a.Name != "" {
		name = a.Name
	}
	msg := notification{At: n.f.now().UnixMilli(), AgentID: agent, URL: n.f.notifyURL(agent)}
	switch p.Kind {
	case "pending":
		msg.Event = NotifyNeedsYou
		msg.Title = name + " needs you"
		what := "a decision"
		switch p.PendingKind {
		case "approval":
			what = "an approval"
		case "question":
			what = "an answer"
		}
		msg.Text = name + " is waiting for " + what + "."
	case "run":
		var digest struct {
			SDD struct {
				Finished  bool   `json:"finished"`
				Succeeded bool   `json:"succeeded"`
				PlanName  string `json:"planName"`
				EndedAt   int64  `json:"endedAt"`
			} `json:"sdd"`
		}
		if json.Unmarshal(p.Run, &digest) != nil || !digest.SDD.Finished {
			return notification{}, false
		}
		key := fmt.Sprintf("%s|%s|%d", agent, digest.SDD.PlanName, digest.SDD.EndedAt)
		n.mu.Lock()
		dup := n.finished[key]
		n.finished[key] = true
		if len(n.finished) > 1024 {
			n.finished = map[string]bool{key: true}
		}
		n.mu.Unlock()
		if dup {
			return notification{}, false
		}
		msg.Event = NotifyRunFinished
		outcome := "failed"
		if digest.SDD.Succeeded {
			outcome = "succeeded"
		}
		msg.Title = name + " run " + outcome
		msg.Text = "The run on " + name + " " + outcome + "."
	case "budget":
		msg.Event = NotifyBudget
		msg.Title = "Budget cap reached"
		msg.Text = fmt.Sprintf("The %s budget reached $%.2f of its $%.2f cap (%s).", p.Scope, p.SpentUSD, p.CapUSD, p.Action)
	case "automation":
		msg.Event = NotifyAutomation
		msg.Title, msg.Text = p.Title, p.Text
		if msg.Title == "" {
			msg.Title = "Automation ran"
		}
		if msg.Text == "" {
			msg.Text = msg.Title
		}
	case "watch":
		var ev struct {
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if json.Unmarshal(p.Event, &ev) != nil || ev.State != "fired" {
			return notification{}, false
		}
		msg.Event = NotifyWatchFired
		msg.Title = "Watch fired"
		msg.Text = "The watch " + ev.Name + " fired."
	case "network_block":
		msg.Event = NotifyNetworkBlock
		msg.Title = "Network request blocked"
		msg.Text = name + " tried to reach " + p.Host + ", which its policy blocks."
	default:
		return notification{}, false
	}
	return msg, true
}

// deliver posts one notification, retrying on failure.
func (n *notifier) deliver(job notifyJob) {
	body, err := json.Marshal(job.body)
	if err != nil {
		return
	}
	var sig string
	if job.hook.SecretRef != "" {
		s, err := n.sign(job.hook.SecretRef, body)
		if err != nil {
			slog.Default().Warn("webbridge: notification not signed, not sent", "webhook", job.hook.ID, "err", err)
			return
		}
		sig = s
	}
	var last error
	for attempt := 0; ; attempt++ {
		if last = n.post(job.hook.URL, body, sig); last == nil {
			return
		}
		if attempt >= len(n.backoff) {
			break
		}
		select {
		case <-time.After(n.backoff[attempt]):
		case <-n.f.done:
			return
		}
	}
	slog.Default().Warn("webbridge: notification delivery failed", "webhook", job.hook.ID, "event", job.body.Event, "err", last)
}

func (n *notifier) sign(ref string, body []byte) (string, error) {
	path, err := ParseSecretRef(ref)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifySecretTimeout)
	defer cancel()
	key, err := n.f.secrets.Get(ctx, DefaultOwnerID, path)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil)), nil
}

func (n *notifier) post(target string, body []byte, sig string) error {
	ctx, cancel := context.WithTimeout(context.Background(), notifyPostTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "marshal-webbridge")
	if sig != "" {
		req.Header.Set("X-Marshal-Signature", sig)
	}
	client := n.client
	if client == nil {
		// Redirects are not followed: a receiver must not be able to bounce
		// the bridge's POST to an address it was never configured with.
		client = &http.Client{Timeout: notifyPostTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook answered %s", resp.Status)
	}
	return nil
}

func (s *Server) notificationsGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.fleet.ws.Notifications()
	if cfg.Webhooks == nil {
		cfg.Webhooks = []Webhook{}
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) notificationsPut(w http.ResponseWriter, r *http.Request) {
	var cfg NotifyConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	for i := range cfg.Webhooks {
		if cfg.Webhooks[i].ID == "" {
			cfg.Webhooks[i].ID = newAgentID()
		}
		if cfg.Webhooks[i].Events == nil {
			cfg.Webhooks[i].Events = []string{}
		}
	}
	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.fleet.ws.SetNotifications(cfg); err != nil {
		writeErr(w, err)
		return
	}
	s.fleet.auditf(AuditEvent{Event: AuditNotificationsSaved, OwnerID: DefaultOwnerID,
		Detail: fmt.Sprintf("%d webhooks", len(cfg.Webhooks))})
	s.notificationsGet(w, r)
}

// notificationsTest sends a test event to every webhook, whatever events
// it subscribed to.
func (s *Server) notificationsTest(w http.ResponseWriter, r *http.Request) {
	cfg := s.fleet.ws.Notifications()
	msg := notification{Event: notifyTest, At: s.fleet.now().UnixMilli(), Title: "Test notification",
		Text: "This is a test from the Marshal Studio.", URL: s.fleet.notifyURL("")}
	for _, h := range cfg.Webhooks {
		s.fleet.notify.enqueue(h, msg)
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"sent": len(cfg.Webhooks)})
}

func (s *Server) notificationRoutes() {
	s.mux.HandleFunc("GET /api/notifications", s.notificationsGet)
	s.mux.HandleFunc("PUT /api/notifications", s.notificationsPut)
	s.mux.HandleFunc("POST /api/notifications/test", s.notificationsTest)
}
