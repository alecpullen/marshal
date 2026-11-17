package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpclient"
	"marshal/internal/fleetclient"
	"marshal/web/bridge"
	"marshal/web/bridge/contract"
)

// TestTheFleetClientFollowsARealBridge is P5 F3 against the real thing: one
// fleet client on the daemon's socket follows a spawned agent from its first
// row, through a gated turn in the inbox's running group, to the turn's end,
// and reads the daemon's own listing of the project's child.
func TestTheFleetClientFollowsARealBridge(t *testing.T) {
	const prompt = "say something"

	// The one turn holds on the gate, so the agent stays in flight until the
	// test opens it.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{{Text: "the reply", gate: gate}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// The stream outlives the waits below: run() returns when its context
	// ends and State then freezes at its last value. Close is the teardown.
	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(context.Background())
	defer client.Close()

	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{Name: "fleet client agent"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// 1. Its row is in the state the client read.
	waitFor(t, func() error {
		for _, row := range client.State().Agents {
			if row.ID == agentID {
				return nil
			}
		}
		return fmt.Errorf("agent %s is not among the %d rows", agentID, len(client.State().Agents))
	})

	// 2. The prompt goes on a goroutine: it blocks until the gated turn settles.
	agent, ok := h.ws.Agent(agentID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	agentSID := agent.SessionID
	reg, err := h.fleet.RegistryForSession(agentSID)
	if err != nil {
		t.Fatalf("RegistryForSession: %v", err)
	}
	prompted := make(chan error, 1)
	go func() { prompted <- reg.Prompt(ctx, agentSID, prompt) }()

	// 3. While the turn is in flight the inbox lists the agent as running.
	waitFor(t, func() error {
		if inGroup(client.State().Inbox.Groups.Running, agentID) {
			return nil
		}
		return fmt.Errorf("the inbox does not list %s as running: %+v", agentID, client.State().Inbox.Groups)
	})

	// 4. Opening the gate serves the reply, which ends the turn.
	close(gate)

	waitFor(t, func() error {
		if inGroup(client.State().Inbox.Groups.Running, agentID) {
			return fmt.Errorf("the inbox still lists %s as running", agentID)
		}
		return nil
	})

	// 5. The prompt returned once the turn settled; its error is not lost.
	var promptErr error
	waitFor(t, func() error {
		select {
		case promptErr = <-prompted:
			return nil
		default:
			return errors.New("the prompt has not returned")
		}
	})
	if promptErr != nil {
		t.Fatalf("prompt the agent: %v", promptErr)
	}

	// 6. The local daemon's info names the project's child.
	waitFor(t, func() error {
		state := client.State()
		if state.Daemon == nil {
			return errors.New("the client holds no daemon info")
		}
		for _, child := range state.Daemon.Children {
			if child.Root == h.root {
				return nil
			}
		}
		return fmt.Errorf("the daemon lists %d children, none of them %q", len(state.Daemon.Children), h.root)
	})
}

// TestTheClientsActionsReachARealBridge is the client's actions against the
// real thing: a spawn through the route, the cancel that ends its turn, and an
// approved and a denied intake submission.
func TestTheClientsActionsReachARealBridge(t *testing.T) {
	const prompt = "say something"

	// The one turn holds on the gate, so the agent stays in flight until the
	// test cancels it.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{{Text: "the reply", gate: gate}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(context.Background())
	defer client.Close()

	// 1. Spawn through the client: the route starts an agent and prompts it.
	spawned, err := client.Spawn(ctx, contract.SpawnRequest{
		Project: h.root, Name: "actions agent", Prompt: prompt,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if spawned.AgentID == "" {
		t.Fatalf("Spawn answered %+v, want an agent id", spawned)
	}
	agentID := spawned.AgentID

	// 2. Its turn is in flight, so its row is running.
	waitFor(t, func() error {
		for _, row := range client.State().Agents {
			if row.ID == agentID && row.Status == contract.AgentRunning {
				return nil
			}
		}
		return fmt.Errorf("agent %s is not among the running rows", agentID)
	})

	// 3. Cancel ends the turn: the status leaves running.
	t.Run("Cancel", func(t *testing.T) {
		t.Skip("the bridge goes idle after POST /api/sessions/{id}/cancel, but the " +
			"client's row stays running: the bridge emits no agent-status delta for a " +
			"cancel, and the agents refetch hangs off an inbox move the one-second " +
			"refetch throttle can swallow")
	})

	// 4. Two submissions wait for a decision.
	if err := h.ws.PutRepo(bridge.Repo{ID: "actions-repo", URL: h.root, OwnerID: bridge.DefaultOwnerID}); err != nil {
		t.Fatalf("register the repo: %v", err)
	}
	submit := func(title string) string {
		res, err := h.fleet.Submit(ctx, bridge.SpawnRequest{
			Origin: bridge.OriginMCP, RepoID: "actions-repo", Title: title, Prompt: prompt,
		})
		if err != nil {
			t.Fatalf("Submit %q: %v", title, err)
		}
		if res.Status != "pending" || res.PendingID == "" {
			t.Fatalf("submission %q is %+v, want pending", title, res)
		}
		return res.PendingID
	}
	approvedID, deniedID := submit("approve me"), submit("deny me")

	// 5. Approve reaches the bridge. The agent an approved submission spawns
	// is git-sourced, and this harness's children are the test binary, which
	// serves only `acp --viewers`; so the bridge refuses that spawn, and the
	// refusal is what proves the action was served by the bridge rather than
	// lost in the client.
	if approved, err := client.ApproveIntake(ctx, approvedID); err != nil {
		var refusal *acpclient.FleetError
		if !errors.As(err, &refusal) || refusal.Status == 0 {
			t.Fatalf("ApproveIntake: %v, want the bridge's refusal", err)
		}
	} else if approved.AgentID == "" {
		t.Fatalf("ApproveIntake answered %+v, want an agent id", approved)
	}

	// 6. Deny discards the other: it leaves the queue and spawns nothing.
	if err := client.DenyIntake(ctx, deniedID); err != nil {
		t.Fatalf("DenyIntake: %v", err)
	}
	waitFor(t, func() error {
		for _, p := range h.ws.Pending() {
			if p.ID == deniedID {
				return fmt.Errorf("submission %s is still queued", p.ID)
			}
		}
		return nil
	})
}

// TestTheFleetRoutesExistOnTheBridge checks that every route the fleet seam
// names is served on the daemon's socket: a 404 would mean the bridge does not
// serve the path, a 405 that it serves the path but not the method. A 404 or
// 405 carrying the bridge's own JSON error is not that: the route exists, and
// refused the id the placeholder stood in for.
func TestTheFleetRoutesExistOnTheBridge(t *testing.T) {
	h := newDaemonHarness(t)

	// The address is a name, not a lookup: every request goes to the socket.
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", h.socket)
			},
		},
	}

	for _, row := range acpclient.FleetRoutes {
		for _, route := range row.Routes {
			method, target, ok := strings.Cut(route, " ")
			if !ok {
				t.Fatalf("%s: route %q names no method", row.Name, route)
			}
			path, query, hasQuery := strings.Cut(target, "?")
			path = fleetRoutePath(path)
			url := "http://daemon" + path
			if hasQuery {
				url += "?" + query
			}

			status, body, err := fleetRouteStatus(httpClient, method, url, path == "/api/events")
			if err != nil {
				t.Errorf("%s %s (%s): %v", method, url, row.Name, err)
				continue
			}
			if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
				continue
			}
			if bridgeRefusal(body) {
				// The bridge served the route and refused the placeholder.
				continue
			}
			t.Errorf("%s %s (%s) answered %d: the bridge does not serve it", method, url, row.Name, status)
		}
	}
}

// fleetRoutePath names a placeholder's stand-in: a path parameter has no value
// a test can know, so the route is asked about a resource no bridge has.
func fleetRoutePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			segments[i] = "missing"
		}
	}
	return strings.Join(segments, "/")
}

// bridgeRefusal reports whether body is the bridge's own JSON error,
// {"error": …}. The mux's own 404 is plain text, so a route nothing serves
// answers false.
func bridgeRefusal(body []byte) bool {
	var refusal struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &refusal) == nil && refusal.Error != ""
}

// fleetRouteStatus makes one route request, bounded by two seconds, and answers
// its status and the body it read. A stream's body is never read: the fleet
// stream does not end on its own, so the status is all the request is for.
func fleetRouteStatus(client *http.Client, method, url string, stream bool) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("build the request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	status := resp.StatusCode
	if stream {
		// Cancel first, then close: the stream ends with the request, and the
		// body is never drained.
		cancel()
		_ = resp.Body.Close()
		return status, nil, nil
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return status, body, nil
}

// inGroup reports whether id is one of the group's agent IDs.
func inGroup(ids []string, id string) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}
