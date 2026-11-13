package bridge

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"marshal/web/bridge/contract"
)

// inboxMk builds a row the way fleet.test.ts's mk does: project "/p", idle,
// updated at 2024-05-04T12:00:00Z, with the case's overrides applied on top.
func inboxMk(id string, overrides func(*AgentStatus)) AgentStatus {
	a := AgentStatus{
		ID:        id,
		Project:   "/p",
		Status:    contract.AgentIdle,
		UpdatedAt: inboxAt("2024-05-04T12:00:00Z"),
	}
	if overrides != nil {
		overrides(&a)
	}
	return a
}

// inboxAt parses a fixed timestamp in a test.
func inboxAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic("inbox test timestamp: " + err.Error())
	}
	return t
}

// inboxPending is the pending request a row is parked on.
func inboxPending(kind contract.PendingKind, id string) *PendingRequest {
	return &PendingRequest{Kind: kind, ID: id}
}

// inboxSubmission is an intake submission created at the given instant.
func inboxSubmission(id, createdAt string) PendingSpawn {
	return PendingSpawn{ID: id, CreatedAt: inboxAt(createdAt)}
}

// inboxQueueIDs names each queued item the way the browser test does: an
// agent by its agent ID, an intake request by its submission ID.
func inboxQueueIDs(items []inboxItem) []string {
	out := make([]string, 0, len(items))
	for _, i := range items {
		if i.Kind == "agent" {
			out = append(out, i.AgentID)
			continue
		}
		out = append(out, i.PendingID)
	}
	return out
}

// TestInboxGroupAgents ports groupAgents' three cases from fleet.test.ts
// (web/ui/src/lib/fleet.test.ts:267) and adds the stable-sort cases the
// browser never had. The bridge compares timestamps as times, not strings,
// so a difference below the second orders the rows.
func TestInboxGroupAgents(t *testing.T) {
	cases := []struct {
		name   string
		agents []AgentStatus
		want   inboxGroups
	}{
		{
			name: "buckets agents by what they need",
			agents: []AgentStatus{
				inboxMk("asks", func(a *AgentStatus) {
					a.Status = contract.AgentAwaitingApproval
					a.Pending = inboxPending(contract.PendingApproval, "t")
				}),
				inboxMk("busy", func(a *AgentStatus) { a.Status = contract.AgentRunning }),
				inboxMk("done", func(a *AgentStatus) { a.ChangedFiles = 3 }),
				inboxMk("shipped", func(a *AgentStatus) {
					a.ChangedFiles = 3
					a.PRUrl = "https://x/pr/1"
				}),
				inboxMk("quiet", nil),
				inboxMk("broken", func(a *AgentStatus) { a.Status = contract.AgentError }),
			},
			// Earlier keeps broken first (rank 1) and then shipped, quiet in
			// snapshot order: both rank 3 with equal timestamps, the inputs
			// are in that order, and the sort is stable. The browser compares
			// this group as a set, so only the members matter there.
			want: inboxGroups{
				NeedsYou: []string{"asks"},
				Running:  []string{"busy"},
				Ready:    []string{"done"},
				Earlier:  []string{"broken", "shipped", "quiet"},
			},
		},
		{
			name: "never lists a running agent that is waiting on you under Running",
			agents: []AgentStatus{
				inboxMk("a", func(a *AgentStatus) {
					a.Status = contract.AgentRunning
					a.Pending = inboxPending(contract.PendingQuestion, "q")
				}),
			},
			want: inboxGroups{
				NeedsYou: []string{"a"},
				Running:  []string{},
				Ready:    []string{},
				Earlier:  []string{},
			},
		},
		{
			name: "sorts by pending kind, then recency",
			agents: []AgentStatus{
				inboxMk("q", func(a *AgentStatus) {
					a.Pending = inboxPending(contract.PendingQuestion, "1")
					a.UpdatedAt = inboxAt("2024-05-04T13:00:00Z")
				}),
				inboxMk("old", func(a *AgentStatus) {
					a.Pending = inboxPending(contract.PendingApproval, "2")
					a.UpdatedAt = inboxAt("2024-05-04T10:00:00Z")
				}),
				inboxMk("new", func(a *AgentStatus) {
					a.Pending = inboxPending(contract.PendingApproval, "3")
					a.UpdatedAt = inboxAt("2024-05-04T12:00:00Z")
				}),
			},
			want: inboxGroups{
				NeedsYou: []string{"new", "old", "q"},
				Running:  []string{},
				Ready:    []string{},
				Earlier:  []string{},
			},
		},
		{
			name: "sorts sub-second updates newest first, newer row last in the snapshot",
			agents: []AgentStatus{
				inboxMk("older", func(a *AgentStatus) { a.UpdatedAt = inboxAt("2024-05-04T12:00:00.100Z") }),
				inboxMk("newer", func(a *AgentStatus) { a.UpdatedAt = inboxAt("2024-05-04T12:00:00.200Z") }),
			},
			want: inboxGroups{
				NeedsYou: []string{},
				Running:  []string{},
				Ready:    []string{},
				Earlier:  []string{"newer", "older"},
			},
		},
		{
			name: "sorts sub-second updates newest first, newer row first in the snapshot",
			agents: []AgentStatus{
				inboxMk("newer", func(a *AgentStatus) { a.UpdatedAt = inboxAt("2024-05-04T12:00:00.200Z") }),
				inboxMk("older", func(a *AgentStatus) { a.UpdatedAt = inboxAt("2024-05-04T12:00:00.100Z") }),
			},
			want: inboxGroups{
				NeedsYou: []string{},
				Running:  []string{},
				Ready:    []string{},
				Earlier:  []string{"newer", "older"},
			},
		},
		{
			name: "keeps the snapshot order on a tie, first row first",
			agents: []AgentStatus{
				inboxMk("a", nil),
				inboxMk("b", nil),
			},
			want: inboxGroups{
				NeedsYou: []string{},
				Running:  []string{},
				Ready:    []string{},
				Earlier:  []string{"a", "b"},
			},
		},
		{
			name: "keeps the snapshot order on a tie, second row first",
			agents: []AgentStatus{
				inboxMk("b", nil),
				inboxMk("a", nil),
			},
			want: inboxGroups{
				NeedsYou: []string{},
				Running:  []string{},
				Ready:    []string{},
				Earlier:  []string{"b", "a"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeInbox(tc.agents, nil).Groups
			assertIDs(t, "needsYou", got.NeedsYou, tc.want.NeedsYou)
			assertIDs(t, "running", got.Running, tc.want.Running)
			assertIDs(t, "ready", got.Ready, tc.want.Ready)
			assertIDs(t, "earlier", got.Earlier, tc.want.Earlier)
		})
	}
}

// TestInboxMergesIntakeIntoNeedsYou ports buildInbox's merge case from
// inbox.test.ts and adds the agent/submission tie the browser never had. The
// bridge has no owner scope, so theirs stays in Running.
func TestInboxMergesIntakeIntoNeedsYou(t *testing.T) {
	agents := []AgentStatus{
		inboxMk("ask", func(a *AgentStatus) {
			a.Pending = inboxPending(contract.PendingApproval, "t")
			a.UpdatedAt = inboxAt("2024-05-04T12:00:00Z")
		}),
		inboxMk("busy", func(a *AgentStatus) { a.Status = contract.AgentRunning }),
		inboxMk("done", func(a *AgentStatus) { a.ChangedFiles = 2 }),
		inboxMk("theirs", func(a *AgentStatus) {
			a.Status = contract.AgentRunning
			a.OwnerID = "bob"
		}),
	}

	cases := []struct {
		name     string
		agents   []AgentStatus
		pending  []PendingSpawn
		wantQ    []string
		wantRun  []string
		wantRead []string
	}{
		{
			name:     "merges intake into Needs you, oldest first",
			agents:   agents,
			pending:  []PendingSpawn{inboxSubmission("early", "2024-05-04T09:00:00Z"), inboxSubmission("late", "2024-05-04T15:00:00Z")},
			wantQ:    []string{"early", "ask", "late"},
			wantRun:  []string{"busy", "theirs"},
			wantRead: []string{"done"},
		},
		{
			name:     "keeps the agent first when an agent and a submission share an instant",
			agents:   agents,
			pending:  []PendingSpawn{inboxSubmission("same", "2024-05-04T12:00:00Z")},
			wantQ:    []string{"ask", "same"},
			wantRun:  []string{"busy", "theirs"},
			wantRead: []string{"done"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeInbox(tc.agents, tc.pending)
			assertIDs(t, "queue", inboxQueueIDs(got.Queue), tc.wantQ)
			assertIDs(t, "running", got.Groups.Running, tc.wantRun)
			assertIDs(t, "ready", got.Groups.Ready, tc.wantRead)
		})
	}
}

// TestInboxEncodesEmptyLists pins decision 4 on the wire: an empty inbox is
// four empty arrays and an empty queue, never null.
func TestInboxEncodesEmptyLists(t *testing.T) {
	data, err := json.Marshal(computeInbox(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"groups":{"needsYou":[],"running":[],"ready":[],"earlier":[]},"queue":[]}`
	if string(data) != want {
		t.Fatalf("empty inbox encodes as\n%s\nwant\n%s", data, want)
	}
}

// TestFleetInbox checks that Inbox is computeInbox over the snapshot and the
// workspace's intake list.
func TestFleetInbox(t *testing.T) {
	f := testFleet(t)
	if err := f.ws.PutAgent(Agent{ID: "a1", Project: "/p", CreatedAt: inboxAt("2024-05-04T12:00:00Z")}); err != nil {
		t.Fatal(err)
	}
	if err := f.ws.PutPending(inboxSubmission("p1", "2024-05-04T09:00:00Z")); err != nil {
		t.Fatal(err)
	}

	got := f.Inbox()
	assertIDs(t, "earlier", got.Groups.Earlier, []string{"a1"})
	assertIDs(t, "queue", inboxQueueIDs(got.Queue), []string{"p1"})
	if got.Queue[0].Kind != "intake" {
		t.Fatalf("queue kind = %q, want intake", got.Queue[0].Kind)
	}
	if want := inboxAt("2024-05-04T09:00:00Z"); !got.Queue[0].At.Equal(want) {
		t.Fatalf("queue at = %s, want %s", got.Queue[0].At, want)
	}
}

// TestTheInboxRouteAnswersTheGroups checks GET /api/inbox end to end: the
// answer is the fleet's live inbox, so each agent lands in the group its
// state puts it in, and an intake submission is in the needs-you queue.
func TestTheInboxRouteAnswersTheGroups(t *testing.T) {
	e, s, root := projectEnv(t)
	busy, err := e.f.Spawn(ctlContext(t), root, SpawnOptions{Name: "busy", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := e.f.Spawn(ctlContext(t), root, SpawnOptions{Name: "quiet", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	// The running agent: a live session with its turn marked busy. The
	// quiet one stays idle with no changed files, which is Earlier.
	rt, err := e.f.runtimeForAgent(busy)
	if err != nil {
		t.Fatal(err)
	}
	rt.reg.mu.Lock()
	rt.reg.sessions[rt.sessionID].Busy = true
	rt.reg.mu.Unlock()

	if err := e.f.ws.PutPending(inboxSubmission("sub-1", "2024-05-04T09:00:00Z")); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, s, http.MethodGet, "/api/inbox", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/inbox = %d %s", rec.Code, rec.Body)
	}
	var got inboxView
	decodeBody(t, rec, &got)

	assertIDs(t, "needsYou", got.Groups.NeedsYou, []string{})
	assertIDs(t, "running", got.Groups.Running, []string{busy})
	assertIDs(t, "ready", got.Groups.Ready, []string{})
	assertIDs(t, "earlier", got.Groups.Earlier, []string{quiet})
	assertIDs(t, "queue", inboxQueueIDs(got.Queue), []string{"sub-1"})
	if got.Queue[0].Kind != "intake" {
		t.Fatalf("queue kind = %q, want intake", got.Queue[0].Kind)
	}
}

// TestTheInboxRouteNeedsTheToken checks that the route sits behind the same
// bearer check as the rest of /api/.
func TestTheInboxRouteNeedsTheToken(t *testing.T) {
	const token = "tok"
	e := newWSSpawnEnv(t)
	s := NewServer(e.f, token)
	auth := map[string]string{"Authorization": "Bearer " + token}

	if rec := doReq(t, s, http.MethodGet, "/api/inbox", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/inbox without a token = %d, want 401", rec.Code)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/inbox", nil, auth); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/inbox with a token = %d, want 200", rec.Code)
	}
	// The same pair for GET /api/agents, the route it is registered beside.
	if rec := doReq(t, s, http.MethodGet, "/api/agents", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/agents without a token = %d, want 401", rec.Code)
	}
	if rec := doReq(t, s, http.MethodGet, "/api/agents", nil, auth); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/agents with a token = %d, want 200", rec.Code)
	}
}

// inboxShorten sets the inbox loop's settle delay and tick. The loop reads
// both fresh each turn, so this takes effect without restarting it.
func inboxShorten(t *testing.T, f *Fleet, delay, every time.Duration) {
	t.Helper()
	f.inboxMu.Lock()
	f.inboxDelay, f.inboxEvery = delay, every
	f.inboxMu.Unlock()
}

// inboxProbe emits a delta that is not an inbox delta, which wakes the loop
// the way any delta on the fleet stream does.
func inboxProbe(f *Fleet) { f.emit(map[string]string{"kind": "probe"}) }

// inboxDeltas returns the inbox deltas on the fleet stream, oldest first.
func inboxDeltas(f *Fleet) []inboxDelta {
	var out []inboxDelta
	for _, e := range f.fleetLog.Tail(fleetStreamKey) {
		var d inboxDelta
		if json.Unmarshal(e.Data, &d) == nil && d.Kind == "inbox" {
			out = append(out, d)
		}
	}
	return out
}

// TestTheInboxIsSentOnceThenOnlyOnChange pins decision 3: the first value the
// loop computes is always sent, and a wake that changes nothing adds none.
func TestTheInboxIsSentOnceThenOnlyOnChange(t *testing.T) {
	f := testFleet(t)
	inboxShorten(t, f, 5*time.Millisecond, time.Hour)

	inboxProbe(f)
	waitFor(t, 5*time.Second, "the first inbox delta", func() bool { return len(inboxDeltas(f)) == 1 })

	// A second wake, with nothing changed, recomputes and sends nothing.
	inboxProbe(f)
	waitFor(t, 5*time.Second, "the second recompute", func() bool { return f.inboxRecomputes() == 2 })
	time.Sleep(50 * time.Millisecond)
	if n := len(inboxDeltas(f)); n != 1 {
		t.Fatalf("inbox deltas after an unchanged recompute = %d, want 1", n)
	}
}

// TestAPendingRequestMovesTheAgentIntoNeedsYou: a pending request, parked the
// way the classifier's registry hook parks one, reaches an inbox delta whose
// needsYou names the agent.
func TestAPendingRequestMovesTheAgentIntoNeedsYou(t *testing.T) {
	f := testFleet(t)
	inboxShorten(t, f, 5*time.Millisecond, time.Hour)
	id, err := f.Spawn(t.Context(), "/home/u/a", SpawnOptions{Name: "agent one", Mode: "edit"})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := f.runtimeForAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	reg := rt.reg
	reg.permMu.Lock()
	reg.permissions["tc1"] = make(chan Decision, 1)
	reg.permSession["tc1"] = rt.sessionID
	reg.permMu.Unlock()
	// The registry's pending state is the status; the event is the payload
	// the snapshot carries, exactly as awaitPermission leaves it.
	reg.emitEvent(rt.sessionID, map[string]any{
		"type":       "permission_request",
		"toolCallId": "tc1",
		"params":     json.RawMessage(`{"sessionId":"` + rt.sessionID + `","toolCallId":"tc1","toolName":"shell.run","command":"go test"}`),
	})

	inboxProbe(f)
	waitFor(t, 5*time.Second, "an inbox delta naming the agent under needsYou", func() bool {
		for _, d := range inboxDeltas(f) {
			if slices.Contains(d.Inbox.Groups.NeedsYou, id) {
				return true
			}
		}
		return false
	})
}

// TestTheTickCatchesAChangeWithNoEvent: a submission written with PutPending,
// with no delta emitted for it, shows up in an inbox delta within a few ticks.
func TestTheTickCatchesAChangeWithNoEvent(t *testing.T) {
	f := testFleet(t)
	inboxShorten(t, f, 5*time.Millisecond, 5*time.Millisecond)

	// One probe settles the loop onto the shortened intervals; the change
	// that follows is then seen by the tick alone.
	inboxProbe(f)
	waitFor(t, 5*time.Second, "the loop's first recompute", func() bool { return f.inboxRecomputes() >= 1 })

	if err := f.ws.PutPending(inboxSubmission("sub-1", "2024-05-04T09:00:00Z")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the submission in an inbox delta", func() bool {
		for _, d := range inboxDeltas(f) {
			if slices.Contains(inboxQueueIDs(d.Inbox.Queue), "sub-1") {
				return true
			}
		}
		return false
	})
}

// TestABurstCostsOneRecompute: a hundred deltas inside the settle delay cost
// at most two recomputes, because the wake signal buffers one.
func TestABurstCostsOneRecompute(t *testing.T) {
	f := testFleet(t)
	inboxShorten(t, f, 50*time.Millisecond, time.Hour)

	for i := 0; i < 100; i++ {
		inboxProbe(f)
	}
	waitFor(t, 5*time.Second, "the burst's recompute", func() bool { return f.inboxRecomputes() >= 1 })
	time.Sleep(150 * time.Millisecond)

	if n := f.inboxRecomputes(); n > 2 {
		t.Fatalf("recomputes for a hundred-delta burst = %d, want at most 2", n)
	}
	if n := len(inboxDeltas(f)); n > 2 {
		t.Fatalf("inbox deltas for a hundred-delta burst = %d, want at most 2", n)
	}
}

// TestTheInboxDeltaDoesNotWakeTheLoop: the loop's own delta must not feed
// back, so one wake gives one recompute and no chain.
func TestTheInboxDeltaDoesNotWakeTheLoop(t *testing.T) {
	f := testFleet(t)
	inboxShorten(t, f, 5*time.Millisecond, time.Hour)

	inboxProbe(f)
	waitFor(t, 5*time.Second, "one recompute", func() bool { return f.inboxRecomputes() == 1 })
	time.Sleep(100 * time.Millisecond)

	if n := f.inboxRecomputes(); n != 1 {
		t.Fatalf("recomputes after one wake = %d, want 1", n)
	}
	if n := len(inboxDeltas(f)); n != 1 {
		t.Fatalf("inbox deltas after one wake = %d, want 1", n)
	}
}

// TestTheLoopStopsWithTheFleet: Close ends the loop, even mid-delay, and no
// inbox delta follows it.
func TestTheLoopStopsWithTheFleet(t *testing.T) {
	f := testFleet(t)
	inboxShorten(t, f, 50*time.Millisecond, 5*time.Millisecond)
	inboxProbe(f)
	waitFor(t, 5*time.Second, "the first inbox delta", func() bool { return len(inboxDeltas(f)) == 1 })

	// Wake it again and close while it waits the delay.
	inboxProbe(f)
	f.Close()
	select {
	case <-f.inboxStopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the inbox loop did not return after Close")
	}

	before, sent := f.inboxRecomputes(), len(inboxDeltas(f))
	inboxProbe(f)
	time.Sleep(100 * time.Millisecond)
	if n := f.inboxRecomputes(); n != before {
		t.Fatalf("recomputes after Close = %d, want %d", n, before)
	}
	if n := len(inboxDeltas(f)); n != sent {
		t.Fatalf("inbox deltas after Close = %d, want %d", n, sent)
	}
}

// assertIDs compares an inbox list against the expected IDs.
func assertIDs(t *testing.T, list string, got, want []string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil; every list must be non-nil", list)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", list, got, want)
	}
}
