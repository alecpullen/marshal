package daemon_test

import (
	"context"
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

	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(ctx)
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

// TestTheFleetRoutesExistOnTheBridge checks that every route the fleet seam
// names is served on the daemon's socket: a 404 would mean the bridge does not
// serve the path, a 405 that it serves the path but not the method.
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
			url := "http://daemon" + path
			if hasQuery {
				url += "?" + query
			}

			status, err := fleetRouteStatus(httpClient, method, url, path == "/api/events")
			if err != nil {
				t.Errorf("%s %s (%s): %v", method, url, row.Name, err)
				continue
			}
			if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
				t.Errorf("%s %s (%s) answered %d: the bridge does not serve it", method, url, row.Name, status)
			}
		}
	}
}

// fleetRouteStatus makes one route request, bounded by two seconds, and answers
// its status. A stream's body is never read: the fleet stream does not end on
// its own, so the status is all the request is for.
func fleetRouteStatus(client *http.Client, method, url string, stream bool) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build the request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	status := resp.StatusCode
	if stream {
		// Cancel first, then close: the stream ends with the request, and the
		// body is never drained.
		cancel()
		_ = resp.Body.Close()
		return status, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return status, nil
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
