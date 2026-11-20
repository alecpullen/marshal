package fleetclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/web/bridge/contract"
)

// fixtureUpdatedAt is the timestamp every fixture row starts at.
var fixtureUpdatedAt = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

// fixtureAgents is GET /api/agents' answer: every AgentStatus field populated.
func fixtureAgents() []contract.AgentStatus {
	pushed := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	return []contract.AgentStatus{
		{
			ID:           "a1",
			Project:      "/repo/a",
			Name:         "Agent One",
			OwnerID:      "owner-1",
			Origin:       "studio",
			ClientID:     "client-1",
			Access:       &contract.AccessState{Mode: "act", Auto: true, System: false, Revision: 3},
			Status:       contract.AgentRunning,
			Activity:     "reading the plan",
			ContextPct:   12,
			ChangedFiles: 2,
			Held:         true,
			Isolated:     true,
			Branch:       "marshal/a1",
			UpdatedAt:    fixtureUpdatedAt,
			SourceKind:   "git",
			TargetBranch: "main",
			PRUrl:        "https://example.test/pr/1",
			PushedAt:     &pushed,
			GateOverride: &contract.GateOverride{Reason: "ship it", At: fixtureUpdatedAt, By: "owner-1"},
			Workspace: &contract.AgentWorkspace{
				Name:    "ws-a",
				Version: 1,
				Source:  contract.WorkspaceFromStudio,
				Timeout: "10m",
			},
		},
		{
			ID:        "a2",
			Project:   "/repo/a",
			Name:      "Agent Two",
			Status:    contract.AgentIdle,
			Activity:  "waiting",
			UpdatedAt: fixtureUpdatedAt,
		},
		{
			ID:         "b1",
			Project:    "/repo/b",
			Name:       "Agent B",
			Status:     contract.AgentAwaitingQuestion,
			ContextPct: 40,
			UpdatedAt:  fixtureUpdatedAt,
			Pending: &contract.PendingRequest{
				Kind:   contract.PendingQuestion,
				ID:     "question-1",
				Params: json.RawMessage(`{"questions":[]}`),
			},
		},
	}
}

// fixtureInbox is GET /api/inbox's answer.
func fixtureInbox() contract.Inbox {
	return contract.Inbox{
		Groups: contract.InboxGroups{
			NeedsYou: []string{"b1"},
			Running:  []string{"a1"},
			Ready:    []string{},
			Earlier:  []string{"a2"},
		},
		Queue: []contract.InboxItem{
			{Kind: contract.InboxItemAgent, AgentID: "b1", At: fixtureUpdatedAt},
			{Kind: contract.InboxItemIntake, Submission: &contract.PendingSubmission{}, At: fixtureUpdatedAt},
		},
	}
}

// fixtureProjects is GET /api/projects' answer.
func fixtureProjects() []contract.ProjectStatus {
	return []contract.ProjectStatus{
		{
			Root:            "/repo/a",
			Available:       true,
			Trust:           contract.TrustTrusted,
			Isolation:       "available",
			OrphanWorktrees: []string{"/repo/a/.marshal/worktrees/orphan"},
		},
		{
			Root:      "/repo/b",
			Available: false,
			Error:     "no such directory",
			Trust:     contract.TrustUntrusted,
			Isolation: "unavailable",
		},
	}
}

// fixtureBudgets is GET /api/budgets' answer.
func fixtureBudgets() contract.BudgetReport {
	return contract.BudgetReport{
		Budgets: contract.Budgets{
			DailyUSD:    20,
			PerAgentUSD: 5,
			OnDailyCap:  "pause",
			OnAgentCap:  "pause",
		},
		Daily: contract.BudgetDaily{Day: "2026-11-14", SpentUSD: 3.5},
		Agents: []contract.BudgetAgentSpend{
			{AgentID: "a1", SpentUSD: 1.25},
		},
		Loaded: true,
	}
}

// fixtureDaemon is GET /api/daemon's answer.
func fixtureDaemon() contract.DaemonInfo {
	return contract.DaemonInfo{
		Version:   "1.2.3",
		Profile:   "local",
		PID:       42,
		StartedAt: 1763000000000,
		Socket:    "/tmp/marshal.sock",
		Projects:  []string{"/repo/a", "/repo/b"},
		Pinned:    []string{"/repo/a"},
		Children: []contract.DaemonChild{
			{
				Root:    "/repo/a",
				Version: "1.2.3",
				Pinned:  true,
				Sessions: []contract.DaemonSession{
					{ID: "session-1", Viewers: 2, LastActive: 1763000000001},
				},
			},
		},
		Refresh: &contract.DaemonRefresh{PID: 43, Restarts: 1},
	}
}

// frameWrite is one frame a test feeds, with the ack that says it was written.
type frameWrite struct {
	raw string
	ack chan struct{}
}

// fakeBridge is a bridge a test drives: fixture answers for the reads, and an
// events stream the test feeds frames into.
type fakeBridge struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	agents     []contract.AgentStatus
	inbox      contract.Inbox
	projects   []contract.ProjectStatus
	budgets    *contract.BudgetReport
	daemon     *contract.DaemonInfo
	reads      map[string]int
	order      []string
	methods    []string
	agentsGate chan struct{}

	// refuse makes the events handler answer with an error instead of a
	// stream; refuseStatus is that answer's status.
	refuse       bool
	refuseStatus int
	// rejectPaths makes only those paths answer 401, so the client meets a
	// rejected token and can recover when the path answers again.
	rejectPaths map[string]bool
	// streams is every stream request's Last-Event-ID, in order, and at is
	// when each arrived.
	streams []string
	at      []time.Time
	// auth is every request's Authorization header, in order.
	auth []string

	events chan frameWrite
	// drop ends the stream a handler is serving: the handler takes it and
	// returns.
	drop chan struct{}
}

// newFakeBridge is a bridge answering the fixtures.
func newFakeBridge(t *testing.T) *fakeBridge {
	t.Helper()
	budgets, daemon := fixtureBudgets(), fixtureDaemon()
	b := &fakeBridge{
		t:           t,
		agents:      fixtureAgents(),
		inbox:       fixtureInbox(),
		projects:    fixtureProjects(),
		budgets:     &budgets,
		daemon:      &daemon,
		reads:       make(map[string]int),
		rejectPaths: make(map[string]bool),
		events:      make(chan frameWrite),
		drop:        make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/agents", b.serveAgents)
	mux.HandleFunc("/api/inbox", b.serveInbox)
	mux.HandleFunc("/api/projects", b.serveProjects)
	mux.HandleFunc("/api/budgets", b.serveBudgets)
	mux.HandleFunc("/api/daemon", b.serveDaemon)
	mux.HandleFunc("/api/events", b.serveEvents)

	// Every request is recorded once, here, and a rejected token answers 401
	// before any handler runs.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.record(r)
		b.mu.Lock()
		rejected := b.rejectPaths[r.URL.Path]
		b.mu.Unlock()
		if rejected {
			http.Error(w, `{"error":"token rejected"}`, http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
	b.srv = httptest.NewServer(handler)
	t.Cleanup(b.srv.Close)
	return b
}

// record notes one request.
func (b *fakeBridge) record(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads[r.URL.Path]++
	b.order = append(b.order, r.URL.Path)
	b.methods = append(b.methods, r.Method)
	b.auth = append(b.auth, r.Header.Get("Authorization"))
}

// holdAgents makes the next GET /api/agents wait for the returned channel to be
// closed.
func (b *fakeBridge) holdAgents() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	gate := make(chan struct{})
	b.agentsGate = gate
	return gate
}

func (b *fakeBridge) serveAgents(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	gate := b.agentsGate
	b.agentsGate = nil
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}

	b.mu.Lock()
	agents := b.agents
	b.mu.Unlock()
	writeJSON(w, agents)
}

func (b *fakeBridge) serveInbox(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	inbox := b.inbox
	b.mu.Unlock()
	writeJSON(w, inbox)
}

func (b *fakeBridge) serveProjects(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	projects := b.projects
	b.mu.Unlock()
	writeJSON(w, projects)
}

func (b *fakeBridge) serveBudgets(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	budgets := b.budgets
	b.mu.Unlock()
	if budgets == nil {
		http.Error(w, `{"error":"no budgets"}`, http.StatusInternalServerError)
		return
	}
	writeJSON(w, *budgets)
}

func (b *fakeBridge) serveDaemon(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	daemon := b.daemon
	b.mu.Unlock()
	if daemon == nil {
		http.Error(w, `{"error":"no daemon"}`, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, *daemon)
}

// serveEvents is the fleet stream: the test's frames, one after another.
func (b *fakeBridge) serveEvents(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("stream") != "fleet" {
		http.Error(w, "bridge: stream must be fleet", http.StatusBadRequest)
		return
	}

	b.mu.Lock()
	b.streams = append(b.streams, r.Header.Get("Last-Event-ID"))
	b.at = append(b.at, time.Now())
	refuse, status := b.refuse, b.refuseStatus
	b.mu.Unlock()

	if refuse {
		if status == 0 {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, "bridge: refusing the stream", status)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "bridge: streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-b.drop:
			// The test asked for the stream to end.
			return
		case frame := <-b.events:
			if _, err := io.WriteString(w, frame.raw); err != nil {
				return
			}
			flusher.Flush()
			close(frame.ack)
		}
	}
}

// dropStream ends the stream the bridge is serving, and returns once the
// handler has taken the drop.
func (b *fakeBridge) dropStream() {
	b.t.Helper()
	select {
	case b.drop <- struct{}{}:
	case <-time.After(10 * time.Second):
		b.t.Fatal("the bridge's events handler never took the drop")
	}
}

// refuseStream makes the events handler answer with an error instead of a
// stream; status 0 is 503.
func (b *fakeBridge) refuseStream(refuse bool, status int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refuse, b.refuseStatus = refuse, status
}

// rejectPath makes one path answer 401, so a single read can meet a rejected
// token while the stream and the other reads carry on.
func (b *fakeBridge) rejectPath(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rejectPaths[path] = true
}

// allowPath lets that path answer normally again, so a rejection can end.
func (b *fakeBridge) allowPath(path string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.rejectPaths, path)
}

// streamRequests is every stream request's Last-Event-ID, in order, and when
// each arrived.
func (b *fakeBridge) streamRequests() ([]string, []time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.streams...), append([]time.Time(nil), b.at...)
}

// requestCount is how many requests the bridge has taken.
func (b *fakeBridge) requestCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.order)
}

// feed writes one raw frame and waits until the bridge has written it.
func (b *fakeBridge) feed(raw string) {
	b.t.Helper()
	ack := make(chan struct{})
	select {
	case b.events <- frameWrite{raw: raw, ack: ack}:
	case <-time.After(10 * time.Second):
		b.t.Fatal("the bridge's events handler never took a frame")
	}
	select {
	case <-ack:
	case <-time.After(10 * time.Second):
		b.t.Fatal("the bridge never wrote a frame")
	}
}

// delta writes one data frame.
func (b *fakeBridge) delta(payload string) {
	b.t.Helper()
	b.feed("data: " + payload + "\n\n")
}

func (b *fakeBridge) setAgents(agents []contract.AgentStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.agents = agents
}

func (b *fakeBridge) setInbox(inbox contract.Inbox) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inbox = inbox
}

func (b *fakeBridge) setProjects(projects []contract.ProjectStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projects = projects
}

func (b *fakeBridge) setBudgets(report contract.BudgetReport) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.budgets = &report
}

func (b *fakeBridge) setDaemon(info contract.DaemonInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.daemon = &info
}

// readCount is how many requests one path took.
func (b *fakeBridge) readCount(path string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reads[path]
}

// requests is the paths requested, in order, and the methods used.
func (b *fakeBridge) requests() ([]string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.order...), append([]string(nil), b.methods...)
}

// startClient connects a client to the bridge, closing it after the test.
func (b *fakeBridge) startClient(t *testing.T, local bool, opts ...Option) *Client {
	t.Helper()
	return b.startNamedClient(t, "fake", local, opts...)
}

// startNamedClient connects a client named server to the bridge, closing it
// after the test.
func (b *fakeBridge) startNamedClient(t *testing.T, server string, local bool, opts ...Option) *Client {
	t.Helper()
	return startTransport(t, Transport{
		Server:  server,
		BaseURL: b.srv.URL,
		HTTP:    b.srv.Client(),
		Local:   local,
	}, opts...)
}

// startTransport connects a client over tr, closing it after the test.
func startTransport(t *testing.T, tr Transport, opts ...Option) *Client {
	t.Helper()
	c := New(tr, opts...)
	c.Start(context.Background())
	t.Cleanup(func() {
		c.Close()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("the client's goroutine did not return after Close")
		}
	})
	return c
}

// writeJSON answers with v.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// mustJSON encodes v for a frame.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return string(raw)
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitConnected waits for the first full read and the connected state.
func waitConnected(t *testing.T, c *Client) {
	t.Helper()
	waitFor(t, "the first read", func() bool {
		s := c.State()
		return s.Loaded && s.Conn == acpclient.FleetConnected
	})
}

// drainChanged consumes one Changed signal.
func drainChanged(t *testing.T, c *Client) {
	t.Helper()
	select {
	case <-c.Changed():
	case <-time.After(10 * time.Second):
		t.Fatal("no Changed signal to drain")
	}
}

func TestTheFirstReadFillsTheState(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, true)
	waitConnected(t, c)

	s := c.State()
	if s.Server != "fake" {
		t.Errorf("Server = %q, want %q", s.Server, "fake")
	}
	if s.Conn != acpclient.FleetConnected {
		t.Errorf("Conn = %v, want FleetConnected", s.Conn)
	}
	if s.ConnErr != "" {
		t.Errorf("ConnErr = %q, want empty", s.ConnErr)
	}
	if !s.Loaded {
		t.Error("Loaded = false, want true")
	}
	if s.Epoch != 1 {
		t.Errorf("Epoch = %d, want 1", s.Epoch)
	}
	if !reflect.DeepEqual(s.Agents, fixtureAgents()) {
		t.Errorf("Agents = %+v, want %+v", s.Agents, fixtureAgents())
	}
	if !reflect.DeepEqual(s.Inbox, fixtureInbox()) {
		t.Errorf("Inbox = %+v, want %+v", s.Inbox, fixtureInbox())
	}
	if !reflect.DeepEqual(s.Projects, fixtureProjects()) {
		t.Errorf("Projects = %+v, want %+v", s.Projects, fixtureProjects())
	}
	if s.Budgets == nil {
		t.Fatal("Budgets = nil, want the bridge's report")
	}
	if !reflect.DeepEqual(*s.Budgets, fixtureBudgets()) {
		t.Errorf("Budgets = %+v, want %+v", *s.Budgets, fixtureBudgets())
	}
	if s.Gates == nil {
		t.Fatal("Gates = nil, want an empty map")
	}
	if len(s.Gates) != 0 {
		t.Errorf("Gates = %+v, want empty", s.Gates)
	}
	if s.Daemon == nil {
		t.Fatal("Daemon = nil, want the daemon's info")
	}
	if !reflect.DeepEqual(*s.Daemon, fixtureDaemon()) {
		t.Errorf("Daemon = %+v, want %+v", *s.Daemon, fixtureDaemon())
	}

	// The stream opens before the reads, and nothing but GET is requested.
	order, methods := b.requests()
	want := []string{"/api/events", "/api/agents", "/api/inbox", "/api/projects", "/api/budgets", "/api/daemon"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("requests = %v, want %v", order, want)
	}
	for i, method := range methods {
		if method != http.MethodGet {
			t.Errorf("request %d used %s, want GET", i, method)
		}
	}
}

func TestDeltasDuringTheFirstReadAreKept(t *testing.T) {
	b := newFakeBridge(t)
	gate := b.holdAgents()
	c := b.startClient(t, false)

	// A delta that arrives while the agents read is held open must survive the
	// read: it is applied after it, not overwritten by it.
	b.delta(`{"kind":"activity","sessionId":"a1","activity":"arrived during the read"}`)
	close(gate)

	waitConnected(t, c)
	waitFor(t, "the kept delta", func() bool {
		for _, a := range c.State().Agents {
			if a.ID == "a1" {
				return a.Activity == "arrived during the read"
			}
		}
		return false
	})
}

func TestAPendingDeltaRefetchesTheAgents(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, false)
	waitConnected(t, c)

	parked := fixtureAgents()
	parked[0].Status = contract.AgentAwaitingApproval
	parked[0].Activity = "parked on an approval"
	b.setAgents(parked)

	b.delta(`{"kind":"pending","sessionId":"a1","pendingKind":"approval"}`)
	waitFor(t, "the pending refetch", func() bool {
		s := c.State()
		return len(s.Agents) > 0 &&
			s.Agents[0].Activity == "parked on an approval" &&
			s.Agents[0].Status == contract.AgentAwaitingApproval
	})
	if got := b.readCount("/api/agents"); got != 2 {
		t.Fatalf("/api/agents reads = %d, want 2 (the connect's and the pending refetch's)", got)
	}

	// This path is not rate limited: a second pending delta refetches at once,
	// even inside the inbox path's one-second interval.
	parked[0].Activity = "parked again"
	b.setAgents(parked)
	b.delta(`{"kind":"pending","sessionId":"a1","pendingKind":"approval"}`)
	waitFor(t, "the second pending refetch", func() bool {
		s := c.State()
		return len(s.Agents) > 0 && s.Agents[0].Activity == "parked again"
	})
	if got := b.readCount("/api/agents"); got != 3 {
		t.Fatalf("/api/agents reads = %d, want 3 (every pending delta refetches)", got)
	}
}

func TestABudgetDeltaRefetchesTheBudgets(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, false)
	waitConnected(t, c)

	spent := fixtureBudgets()
	spent.Daily.SpentUSD = 9.5
	b.setBudgets(spent)

	b.delta(`{"kind":"budget","scope":"daily","spentUsd":9.5,"capUsd":20,"action":"warn"}`)
	waitFor(t, "the budgets refetch", func() bool {
		s := c.State()
		return s.Budgets != nil && s.Budgets.Daily.SpentUSD == 9.5
	})
	if got := b.readCount("/api/budgets"); got != 2 {
		t.Fatalf("/api/budgets reads = %d, want 2 (the connect's and the budget delta's)", got)
	}
}

func TestAnInboxNamingAnUnknownAgentRefetchesAtMostOnceASecond(t *testing.T) {
	b := newFakeBridge(t)
	// A long interval makes the limit a fact rather than a race against the
	// clock. The default is one second.
	c := b.startClient(t, false, WithRefetchEvery(time.Hour))
	waitConnected(t, c)

	grown := fixtureAgents()
	grown = append(grown, contract.AgentStatus{ID: "a3", Project: "/repo/a", Status: contract.AgentIdle})
	b.setAgents(grown)

	inbox := fixtureInbox()
	inbox.Groups.Ready = []string{"a3"}
	b.delta(`{"kind":"inbox","inbox":` + mustJSON(t, inbox) + `}`)

	waitFor(t, "the first inbox refetch", func() bool {
		return len(c.State().Agents) == len(grown)
	})
	if got := b.readCount("/api/agents"); got != 2 {
		t.Fatalf("/api/agents reads = %d, want 2 (the connect's and the inbox refetch's)", got)
	}

	// A second inbox naming another unknown agent inside the interval is not
	// refetched: the limit holds.
	inbox.Groups.Ready = []string{"a3", "a4"}
	b.delta(`{"kind":"inbox","inbox":` + mustJSON(t, inbox) + `}`)
	waitFor(t, "the second inbox", func() bool {
		return sameStrings(c.State().Inbox.Groups.Ready, []string{"a3", "a4"})
	})
	if got := b.readCount("/api/agents"); got != 2 {
		t.Fatalf("/api/agents reads = %d, want 2 (the refetch is limited to one per interval)", got)
	}
}

func TestAGroupChangeRefetchesTheAgents(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, false)
	waitConnected(t, c)

	if got := b.readCount("/api/agents"); got != 1 {
		t.Fatalf("/api/agents reads = %d, want 1 after the connect", got)
	}

	// Every agent the inbox names is known; only a1's group moves, from running
	// to ready. That alone means the rows behind the inbox are stale.
	moved := fixtureInbox()
	moved.Groups.Running = nil
	moved.Groups.Ready = []string{"a1"}
	b.delta(`{"kind":"inbox","inbox":` + mustJSON(t, moved) + `}`)

	waitFor(t, "the group change's refetch", func() bool {
		return b.readCount("/api/agents") == 2
	})
}

// TestAnInboxDeltaChangingOnlyATitleFiresAChange checks the queue's submissions
// are compared by what the screen reads: a submission whose title moved is a
// change, even when its id and the item's At are the same.
func TestAnInboxDeltaChangingOnlyATitleFiresAChange(t *testing.T) {
	b := newFakeBridge(t)
	// The connect reads a submission that has a title, so the delta can move
	// only that.
	queued := fixtureInbox()
	queued.Queue[1].Submission = &contract.PendingSubmission{ID: "sub-1", Title: "the old title"}
	b.setInbox(queued)

	c := b.startClient(t, false)
	waitConnected(t, c)
	drainChanged(t, c)

	renamed := fixtureInbox()
	renamed.Queue[1].Submission = &contract.PendingSubmission{ID: "sub-1", Title: "the new title"}
	b.delta(`{"kind":"inbox","inbox":` + mustJSON(t, renamed) + `}`)

	select {
	case <-c.Changed():
	case <-time.After(10 * time.Second):
		t.Fatal("the title change fired no Changed signal")
	}
	if got := c.State().Inbox.Queue[1].Submission; got == nil || got.Title != "the new title" {
		t.Errorf("the queue's submission = %+v, want the delta's title", got)
	}
}

func TestKindsOutsideTheLoopChangeNothing(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, false)
	waitConnected(t, c)
	drainChanged(t, c)

	before := c.State()

	for _, payload := range []string{
		`{"kind":"watch","sessionId":"a1","event":{"path":"/repo/a"}}`,
		`{"kind":"reroute","id":"r1","from":"provider-a","to":"provider-b"}`,
		`{"kind":"automation","type":"review_draft","id":"aut-1","status":"done"}`,
		`{"kind":"network_block","agentId":"a1","host":"example.test"}`,
		`{"kind":"not_a_kind","sessionId":"a1","activity":"ignored"}`,
	} {
		b.delta(payload)
		// The frame is local: a change would have landed well inside this
		// window. The check is that nothing fires.
		time.Sleep(150 * time.Millisecond)
		if n := len(c.Changed()); n != 0 {
			t.Fatalf("%s fired Changed", payload)
		}
	}

	// A frame that does change a row proves the stream reached the client.
	b.delta(`{"kind":"activity","sessionId":"b1","activity":"marker"}`)
	waitFor(t, "the marker delta", func() bool {
		for _, a := range c.State().Agents {
			if a.ID == "b1" {
				return a.Activity == "marker"
			}
		}
		return false
	})

	after := c.State()
	for i := range after.Agents {
		if after.Agents[i].ID == "b1" {
			after.Agents[i].Activity = before.Agents[i].Activity
			after.Agents[i].UpdatedAt = before.Agents[i].UpdatedAt
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the kinds outside the loop changed the state:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestAnOverflowRefetchesEverythingAndBumpsTheEpoch(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, true)
	waitConnected(t, c)

	if got := c.State().Epoch; got != 1 {
		t.Fatalf("Epoch = %d, want 1 after the connect", got)
	}

	agents := fixtureAgents()
	agents[0].Activity = "after the overflow"
	b.setAgents(agents)

	inbox := fixtureInbox()
	inbox.Groups.NeedsYou = []string{"a1"}
	b.setInbox(inbox)

	projects := fixtureProjects()
	projects[0].Available = false
	b.setProjects(projects)

	budgets := fixtureBudgets()
	budgets.Daily.SpentUSD = 11
	b.setBudgets(budgets)

	daemon := fixtureDaemon()
	daemon.PID = 99
	b.setDaemon(daemon)

	b.delta(`{"type":"replay_overflow"}`)

	waitFor(t, "the overflow's refetches", func() bool {
		s := c.State()
		return s.Epoch == 2 && len(s.Agents) > 0 && s.Agents[0].Activity == "after the overflow"
	})

	s := c.State()
	if !sameStrings(s.Inbox.Groups.NeedsYou, []string{"a1"}) {
		t.Errorf("Inbox.Groups.NeedsYou = %v, want [a1]", s.Inbox.Groups.NeedsYou)
	}
	if len(s.Projects) == 0 || s.Projects[0].Available {
		t.Errorf("Projects = %+v, want the reread answer", s.Projects)
	}
	if s.Budgets == nil || s.Budgets.Daily.SpentUSD != 11 {
		t.Errorf("Budgets = %+v, want the reread report", s.Budgets)
	}
	if s.Daemon == nil || s.Daemon.PID != 99 {
		t.Errorf("Daemon = %+v, want the reread info", s.Daemon)
	}

	for _, path := range []string{"/api/agents", "/api/inbox", "/api/projects", "/api/budgets", "/api/daemon"} {
		if got := b.readCount(path); got != 2 {
			t.Errorf("%s reads = %d, want 2 (the connect's and the overflow's)", path, got)
		}
	}
}

func TestStateIsACopy(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, true)
	waitConnected(t, c)

	first := c.State()
	first.Agents[0].Activity = "mutated"
	first.Agents[0].Access.Mode = "mutated"
	first.Agents = append(first.Agents, contract.AgentStatus{ID: "mutated"})
	first.Projects[0].Root = "mutated"
	first.Projects[0].OrphanWorktrees[0] = "mutated"
	first.Inbox.Groups.NeedsYou[0] = "mutated"
	first.Inbox.Queue[0].AgentID = "mutated"
	first.Gates["a1"] = contract.GateRecord{}

	if first.Budgets == nil || len(first.Budgets.Agents) == 0 {
		t.Fatal("Budgets is missing an agent spend; the fixture needs one")
	}
	first.Budgets.Agents[0].SpentUSD = 999

	if first.Daemon == nil || len(first.Daemon.Projects) == 0 || len(first.Daemon.Pinned) == 0 ||
		len(first.Daemon.Children) == 0 || len(first.Daemon.Children[0].Sessions) == 0 {
		t.Fatal("Daemon's fixture is missing a list; it needs them all")
	}
	first.Daemon.Projects[0] = "mutated"
	first.Daemon.Pinned[0] = "mutated"
	first.Daemon.Children[0].Sessions[0].ID = "mutated"
	first.Daemon.Refresh.Restarts = 99

	if first.Agents[0].Workspace == nil || first.Agents[0].GateOverride == nil || first.Agents[0].PushedAt == nil {
		t.Fatal("the first row's fixture is missing a pointed-to value; it needs Workspace, GateOverride and PushedAt")
	}
	first.Agents[0].Workspace.Name = "mutated"
	first.Agents[0].GateOverride.Reason = "mutated"
	*first.Agents[0].PushedAt = time.Time{}

	if first.Agents[2].Pending == nil || len(first.Agents[2].Pending.Params) == 0 {
		t.Fatal("the third row's fixture is missing a pending request with params; the test needs one")
	}
	first.Agents[2].Pending.ID = "mutated"
	first.Agents[2].Pending.Params[0] = 'x'

	if len(first.Inbox.Queue) < 2 || first.Inbox.Queue[1].Submission == nil {
		t.Fatal("the inbox fixture is missing a queue item with a submission; the test needs one")
	}
	first.Inbox.Queue[1].Submission.ID = "mutated"
	first.Inbox.Queue[1].Submission.Title = "mutated"

	second := c.State()
	if !reflect.DeepEqual(second.Agents, fixtureAgents()) {
		t.Errorf("Agents = %+v, want the fixtures untouched", second.Agents)
	}
	if !reflect.DeepEqual(second.Projects, fixtureProjects()) {
		t.Errorf("Projects = %+v, want the fixtures untouched", second.Projects)
	}
	if !reflect.DeepEqual(second.Inbox, fixtureInbox()) {
		t.Errorf("Inbox = %+v, want the fixtures untouched", second.Inbox)
	}
	if len(second.Gates) != 0 {
		t.Errorf("Gates = %+v, want it untouched", second.Gates)
	}
	if !reflect.DeepEqual(*second.Budgets, fixtureBudgets()) {
		t.Errorf("Budgets = %+v, want the fixtures untouched", *second.Budgets)
	}
	if !reflect.DeepEqual(*second.Daemon, fixtureDaemon()) {
		t.Errorf("Daemon = %+v, want the fixtures untouched", *second.Daemon)
	}
}

func TestChangedCoalesces(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, false)
	waitConnected(t, c)
	drainChanged(t, c)

	if n := len(c.Changed()); n != 0 {
		t.Fatalf("buffered signals = %d, want 0 after draining the connect's", n)
	}

	b.delta(`{"kind":"activity","sessionId":"a1","activity":"one"}`)
	b.delta(`{"kind":"activity","sessionId":"a1","activity":"two"}`)
	b.delta(`{"kind":"activity","sessionId":"a1","activity":"three"}`)

	// Three changes, one signal: the send never blocks, and a signal already
	// waiting is not queued twice.
	waitFor(t, "the third activity", func() bool {
		for _, a := range c.State().Agents {
			if a.ID == "a1" {
				return a.Activity == "three"
			}
		}
		return false
	})
	if n := len(c.Changed()); n != 1 {
		t.Fatalf("buffered signals = %d, want 1", n)
	}
	<-c.Changed()
	if n := len(c.Changed()); n != 0 {
		t.Fatalf("buffered signals = %d, want 0 after one receive", n)
	}
}

func TestTheDaemonIsReadOnlyLocally(t *testing.T) {
	t.Run("a server never reads the daemon", func(t *testing.T) {
		b := newFakeBridge(t)
		c := b.startClient(t, false)
		waitConnected(t, c)

		if got := b.readCount("/api/daemon"); got != 0 {
			t.Errorf("/api/daemon reads = %d, want 0 for a server", got)
		}
		if s := c.State(); s.Daemon != nil {
			t.Errorf("Daemon = %+v, want nil for a server", s.Daemon)
		}

		_, methods := b.requests()
		for i, method := range methods {
			if method != http.MethodGet {
				t.Errorf("request %d used %s, want GET", i, method)
			}
		}
	})

	t.Run("the local daemon is read", func(t *testing.T) {
		b := newFakeBridge(t)
		c := b.startClient(t, true)
		waitConnected(t, c)

		if got := b.readCount("/api/daemon"); got != 1 {
			t.Errorf("/api/daemon reads = %d, want 1 for the local daemon", got)
		}
		s := c.State()
		if s.Daemon == nil {
			t.Fatal("Daemon = nil, want the local daemon's info")
		}
		if want := fixtureDaemon(); !reflect.DeepEqual(*s.Daemon, want) {
			t.Errorf("Daemon = %+v, want %+v", *s.Daemon, want)
		}

		_, methods := b.requests()
		for i, method := range methods {
			if method != http.MethodGet {
				t.Errorf("request %d used %s, want GET", i, method)
			}
		}
	})
}

// TestAServerNamesTheBridgeOnScreen checks the transport's own name reaches the
// state, so the screen can tell the socket from a server.
func TestAServerNamesTheBridgeOnScreen(t *testing.T) {
	b := newFakeBridge(t)
	c := New(Transport{Server: "studio", BaseURL: b.srv.URL, HTTP: b.srv.Client()})
	defer c.Close()
	if got := c.State().Server; got != "studio" {
		t.Fatalf("Server = %q, want %q", got, "studio")
	}
	if got := c.State().Conn; got != acpclient.FleetConnecting {
		t.Fatalf("Conn = %v, want FleetConnecting before Start", got)
	}
}

// TestSocketIsTheLocalDaemonsTransport checks the transport the socket builds.
func TestSocketIsTheLocalDaemonsTransport(t *testing.T) {
	tr := Socket("/tmp/marshal-test.sock")
	if tr.Server != "local" {
		t.Errorf("Server = %q, want %q", tr.Server, "local")
	}
	if tr.BaseURL != "http://daemon" {
		t.Errorf("BaseURL = %q, want %q", tr.BaseURL, "http://daemon")
	}
	if !tr.Local {
		t.Error("Local = false, want true")
	}
	if tr.HTTP == nil || tr.HTTP.Transport == nil {
		t.Fatal("HTTP = nil, want a transport dialling the socket")
	}
	if fmt.Sprint(tr.HTTP.Timeout) != "0s" {
		t.Errorf("HTTP.Timeout = %v, want none: the stream is long-lived", tr.HTTP.Timeout)
	}
}

// streamGaps is the wait between every pair of stream requests, in arrival
// order.
func streamGaps(times []time.Time) []time.Duration {
	gaps := make([]time.Duration, 0, len(times)-1)
	for i := 1; i < len(times); i++ {
		gaps = append(gaps, times[i].Sub(times[i-1]))
	}
	return gaps
}

// TestADroppedStreamReconnectsWithTheLastEventID checks a dropped stream comes
// back, resumed from the last frame the client applied.
func TestADroppedStreamReconnectsWithTheLastEventID(t *testing.T) {
	b := newFakeBridge(t)
	// A wait long enough that the reconnecting state is observable.
	c := b.startClient(t, false, WithBackoff(30*time.Millisecond, 100*time.Millisecond))
	waitConnected(t, c)

	b.feed(`id: 7
data: {"kind":"activity","sessionId":"a1","activity":"before the drop"}

`)
	waitFor(t, "the identified frame", func() bool {
		for _, a := range c.State().Agents {
			if a.ID == "a1" {
				return a.Activity == "before the drop"
			}
		}
		return false
	})

	b.dropStream()

	// The drop shows as reconnecting, with why it ended, before the retry.
	waitFor(t, "the reconnecting state", func() bool {
		return c.State().Conn == acpclient.FleetReconnecting
	})
	if got := c.State().ConnErr; got == "" {
		t.Error("ConnErr is empty while reconnecting, want why the stream ended")
	}

	waitFor(t, "the reconnect", func() bool {
		s := c.State()
		return s.Conn == acpclient.FleetConnected && s.Epoch == 2
	})

	ids, _ := b.streamRequests()
	if len(ids) < 2 {
		t.Fatalf("stream requests = %v, want the connect's and the reconnect's", ids)
	}
	if ids[0] != "" {
		t.Errorf("the first stream's Last-Event-ID = %q, want none", ids[0])
	}
	if ids[1] != "7" {
		t.Errorf("the reconnect's Last-Event-ID = %q, want 7", ids[1])
	}
	if got := c.State().Epoch; got != 2 {
		t.Errorf("Epoch = %d, want 2 after the reconnect", got)
	}
}

// TestTheBackoffDoublesToItsMaximum checks the wait after each failed attempt:
// the first wait, doubling each failure, capped at the maximum.
func TestTheBackoffDoublesToItsMaximum(t *testing.T) {
	b := newFakeBridge(t)
	b.refuseStream(true, http.StatusServiceUnavailable)
	// Waits of 5ms, 10ms, 20ms, then 20ms: small enough to keep the test quick,
	// large enough that the doubling is measurable.
	b.startClient(t, false, WithBackoff(5*time.Millisecond, 20*time.Millisecond))

	// The first attempt plus four failed retries.
	waitFor(t, "four failed retries", func() bool {
		ids, _ := b.streamRequests()
		return len(ids) >= 5
	})
	_, times := b.streamRequests()
	gaps := streamGaps(times)

	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond}
	for i, w := range want {
		if i >= len(gaps) {
			break
		}
		// A timer never fires early; the slack absorbs scheduling delay.
		if gaps[i] < w*3/4 {
			t.Errorf("wait %d = %v, want at least %v", i+1, gaps[i], w*3/4)
		}
	}
	// The fourth wait is the maximum, not double the third: the backoff
	// stopped growing.
	if len(gaps) > 3 && gaps[3] > gaps[2]+10*time.Millisecond {
		t.Errorf("wait 4 = %v, want it capped at the maximum like wait 3's %v", gaps[3], gaps[2])
	}
}

// TestTheBackoffResetsAfterAMinuteConnected checks a stream that stayed up long
// enough earns a fresh first wait.
func TestTheBackoffResetsAfterAMinuteConnected(t *testing.T) {
	b := newFakeBridge(t)
	b.refuseStream(true, http.StatusServiceUnavailable)
	c := b.startClient(t, false,
		WithBackoff(20*time.Millisecond, 160*time.Millisecond),
		WithResetAfter(20*time.Millisecond))

	// Three failed attempts grow the wait to its maximum.
	waitFor(t, "three failed attempts", func() bool {
		_, times := b.streamRequests()
		return len(times) >= 4
	})
	_, failed := b.streamRequests()
	gaps := streamGaps(failed)
	if len(gaps) < 3 || gaps[2] < 20*time.Millisecond*3/4 {
		t.Fatalf("gaps = %v, want the third wait at the maximum", gaps)
	}

	// The bridge accepts, and the stream stays up past the reset.
	b.refuseStream(false, 0)
	waitConnected(t, c)
	time.Sleep(60 * time.Millisecond)

	// The stream drops, the bridge refuses again, and the next attempt waits
	// the first wait rather than the maximum it had grown to.
	_, up := b.streamRequests()
	count := len(up)
	b.refuseStream(true, http.StatusServiceUnavailable)
	b.dropStream()
	dropped := time.Now()

	waitFor(t, "the retry after the drop", func() bool {
		_, times := b.streamRequests()
		return len(times) > count
	})
	_, after := b.streamRequests()
	wait := after[count].Sub(dropped)
	if wait >= 60*time.Millisecond {
		t.Errorf("the retry after the reset waited %v, want the first wait (~20ms), not the maximum", wait)
	}
}

// TestTheStateGoesDownAfterAMinuteOfFailures checks the state goes down after
// downAfter of failures, keeps retrying, and comes back when the bridge does.
func TestTheStateGoesDownAfterAMinuteOfFailures(t *testing.T) {
	b := newFakeBridge(t)
	b.refuseStream(true, http.StatusServiceUnavailable)
	c := b.startClient(t, false,
		WithBackoff(2*time.Millisecond, 8*time.Millisecond),
		WithDownAfter(30*time.Millisecond))

	waitFor(t, "the down state", func() bool {
		return c.State().Conn == acpclient.FleetDown
	})
	if got := c.State().ConnErr; got == "" {
		t.Error("ConnErr is empty while down, want why the last attempt failed")
	}

	// Down still retries, at the maximum wait.
	retries := b.requestCount()
	waitFor(t, "more retries while down", func() bool {
		return b.requestCount() > retries
	})

	b.refuseStream(false, 0)
	waitFor(t, "the recovery", func() bool {
		s := c.State()
		return s.Conn == acpclient.FleetConnected && s.ConnErr == ""
	})
}

// TestA401IsAnOrdinaryFailure checks a 401 is a failure like any other: the
// client reconnects, and it connects once the bridge answers again.
func TestA401IsAnOrdinaryFailure(t *testing.T) {
	b := newFakeBridge(t)
	// The connect's first read is refused, so every attempt fails.
	b.rejectPath("/api/agents")
	c := b.startClient(t, false, WithBackoff(2*time.Millisecond, 8*time.Millisecond))

	waitFor(t, "the reconnect", func() bool {
		return c.State().Conn == acpclient.FleetReconnecting
	})
	if got := c.State().ConnErr; !strings.Contains(got, "401") {
		t.Errorf("ConnErr = %q, want the 401's message", got)
	}

	// The bridge answers again: the next attempt connects.
	b.allowPath("/api/agents")
	waitConnected(t, c)
}

// TestTheDaemonsSessionsAreReread checks the local daemon's info stays current
// while the stream is up, and that an unchanged answer fires nothing.
func TestTheDaemonsSessionsAreReread(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, true, WithDaemonEvery(10*time.Millisecond))
	waitConnected(t, c)
	drainChanged(t, c)

	grown := fixtureDaemon()
	grown.Children[0].Sessions = append(grown.Children[0].Sessions,
		contract.DaemonSession{ID: "session-2", Viewers: 1, LastActive: 1763000000002})
	b.setDaemon(grown)

	waitFor(t, "the daemon reread", func() bool {
		d := c.State().Daemon
		return d != nil && len(d.Children) > 0 && len(d.Children[0].Sessions) == 2
	})
	drainChanged(t, c)

	// The answer has not changed: several more rereads fire nothing.
	time.Sleep(40 * time.Millisecond)
	if n := len(c.Changed()); n != 0 {
		t.Errorf("buffered Changed signals = %d, want 0: the daemon's answer did not differ", n)
	}
}

// TestCloseStopsTheClient checks Close ends the goroutine, the stream and the
// timers, is safe twice, and that Start after Close does nothing.
func TestCloseStopsTheClient(t *testing.T) {
	b := newFakeBridge(t)
	c := New(Transport{
		Server:  "fake",
		BaseURL: b.srv.URL,
		HTTP:    b.srv.Client(),
		Local:   true,
	}, WithDaemonEvery(10*time.Millisecond))
	c.Start(context.Background())
	waitConnected(t, c)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close waits for the run goroutine: it has returned when Close does.
	select {
	case <-c.done:
	default:
		t.Fatal("the client's goroutine is still running after Close returned")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// Neither the stream nor the daemon timers ask anything more.
	count := b.requestCount()
	time.Sleep(40 * time.Millisecond)
	if got := b.requestCount(); got != count {
		t.Fatalf("%d requests after Close, want none", got-count)
	}

	// Start after Close does nothing.
	c.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	if got := b.requestCount(); got != count {
		t.Fatalf("%d requests after Start on a closed client, want none", got-count)
	}
}

// TestCloseClosesTheChangeSignal checks the client says it is closed, so a
// reader waiting on its change signal learns there is nothing more to wait for.
func TestCloseClosesTheChangeSignal(t *testing.T) {
	b := newFakeBridge(t)
	c := b.startClient(t, false)
	waitConnected(t, c)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for {
		select {
		case _, open := <-c.Changed():
			if !open {
				return // the signal is closed
			}
			// A buffered signal: take it and look again.
		case <-time.After(time.Second):
			t.Fatal("the change signal was not closed by Close")
		}
	}
}
