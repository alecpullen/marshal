package daemon_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/fleet"
	"marshal/internal/daemon"
	"marshal/internal/fleetclient"
	"marshal/internal/fleetprofile"
	"marshal/internal/launch"
	"marshal/web/bridge"
)

// serverProfileHarness is the local profile's fleet served the way a server is
// (P5 F7, W8): the same host-process fleet, behind a network listener that
// demands a bearer token, over TLS, with a fleet.toml naming it.
//
// It embeds the daemon harness, so the fleet, the project it serves and the
// state directory are exactly the local profile's. The unix socket that harness
// also serves is not what these tests read: every request here goes out over
// the TLS listener, as one to another machine does.
type serverProfileHarness struct {
	*daemonHarness
	// server is the TLS listener the bridge answers on. httptest's certificate
	// covers 127.0.0.1, so the profile's url names that address.
	server *httptest.Server
	// token is the bearer token the listener accepts, and the one token_cmd
	// prints.
	token string
	// profileName and profilePath are the fleet.toml the harness wrote.
	profileName string
	profilePath string
}

// newServerProfileHarness builds the fleet and the profile that names it. A
// server has no daemon info and no socket, so the listener is built with the
// token alone.
func newServerProfileHarness(t *testing.T) *serverProfileHarness {
	t.Helper()
	h := &serverProfileHarness{
		daemonHarness: newDaemonHarness(t),
		token:         "the-server-profile-token",
		profileName:   "bridge",
	}

	api := bridge.NewServer(h.fleet, h.token, "")
	h.server = httptest.NewTLSServer(api)
	t.Cleanup(h.server.Close)

	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	var cert bytes.Buffer
	if err := pem.Encode(&cert, &pem.Block{Type: "CERTIFICATE", Bytes: h.server.Certificate().Raw}); err != nil {
		t.Fatalf("encode the listener's certificate: %v", err)
	}
	if err := os.WriteFile(caFile, cert.Bytes(), 0o600); err != nil {
		t.Fatalf("write the ca_file: %v", err)
	}

	// token_cmd prints the token from a file, as a password manager would: the
	// token is never in fleet.toml (decision 2).
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(h.token+"\n"), 0o600); err != nil {
		t.Fatalf("write the token file: %v", err)
	}

	// The file must be private, or Load refuses it (decision 3).
	h.profilePath = filepath.Join(dir, "fleet.toml")
	profile := fmt.Sprintf("[servers.%s]\nurl = %q\nca_file = %q\ntoken_cmd = %q\n",
		h.profileName, h.server.URL, caFile, "cat "+tokenFile)
	if err := os.WriteFile(h.profilePath, []byte(profile), 0o600); err != nil {
		t.Fatalf("write fleet.toml: %v", err)
	}
	return h
}

// resolve reads the harness's profile the way `marshal fleet --server` does
// before any screen runs (decisions 4 and 5): Load and Lookup name the server,
// Token runs its token_cmd through the real RunShell, and TLSConfig trusts the
// ca_file. It answers the server, the token function a transport carries, and
// the TLS configuration.
func (h *serverProfileHarness) resolve(t *testing.T) (fleetprofile.Server, func(context.Context) (string, error), *tls.Config) {
	t.Helper()
	file, err := fleetprofile.Load(h.profilePath)
	if err != nil {
		t.Fatalf("load fleet.toml: %v", err)
	}
	srv, err := file.Lookup(h.profileName)
	if err != nil {
		t.Fatalf("look up %q: %v", h.profileName, err)
	}
	if srv.URL != h.server.URL {
		t.Fatalf("the profile's url is %q, want the listener's %q", srv.URL, h.server.URL)
	}
	// A profile with token_cmd never reads the keychain, so there is no store
	// to open: nil is the opener that would fail if one were asked for.
	token, err := fleetprofile.Token(context.Background(), srv, nil, fleetprofile.RunShell, io.Discard)
	if err != nil {
		t.Fatalf("resolve the profile's token: %v", err)
	}
	if token != h.token {
		t.Fatalf("token_cmd printed %q, want the listener's token", token)
	}
	tlsConfig, err := fleetprofile.TLSConfig(srv)
	if err != nil {
		t.Fatalf("the profile's TLS configuration: %v", err)
	}
	return srv, func(context.Context) (string, error) { return token, nil }, tlsConfig
}

// TestAServerProfileOpensAnAgentOverTLS is W8: a fleet on another machine,
// read through its fleet.toml — HTTPS with the server's certificate in
// ca_file, and a bearer token from token_cmd — lists an agent, opens its
// session through the network attach, and reads the turn's transcript with
// none of the session's filesystem on this machine.
func TestAServerProfileOpensAnAgentOverTLS(t *testing.T) {
	const prompt = "say something across the network"
	const reply = "the reply a viewer reads over TLS"

	fixture := newProviderFixture(t, []scriptedTurn{{Text: reply}})
	fixture.useProvider(t)
	h := newServerProfileHarness(t)
	ctx := callCtx(t)

	// 1. A git-sourced agent, as TestAnOwnChildsAgentOpensAsAViewer spawns it:
	// it runs on a child of its own, whose viewer socket the bridge keeps.
	bare := bareRepoForOwnChild(t)
	if err := h.ws.PutRepo(bridge.Repo{ID: ownChildRepoID, URL: bare, OwnerID: bridge.DefaultOwnerID}); err != nil {
		t.Fatalf("register the repository: %v", err)
	}
	agentID, err := h.fleet.Spawn(ctx, h.root, bridge.SpawnOptions{
		Name: "server agent", RepoID: ownChildRepoID,
	})
	if err != nil {
		t.Fatalf("spawn a git-sourced agent: %v", err)
	}
	agent, ok := h.ws.Agent(agentID)
	if !ok || agent.SessionID == "" {
		t.Fatalf("agent %s carries no session id", agentID)
	}
	waitFor(t, func() error {
		sockets, err := filepath.Glob(filepath.Join(h.stateDir, "v", "*.sock"))
		if err != nil {
			return err
		}
		if len(sockets) != 1 {
			return fmt.Errorf("the state directory holds %d viewer sockets, want the own child's one", len(sockets))
		}
		return nil
	})

	// 2. One turn on the agent's own child: the transcript read below is its.
	waitPrompt(t, promptAgent(t, h.daemonHarness, ctx, agent.SessionID, prompt))

	// 3. The profile, resolved as `marshal fleet --server` resolves it.
	srv, token, tlsConfig := h.resolve(t)

	// 4. The fleet client reads the bridge over HTTPS with the token: the
	// agent is one of the rows it lists.
	client := fleetclient.New(fleetclient.HTTPS(srv.Name, srv.URL, tlsConfig, token))
	client.Start(context.Background())
	defer client.Close()
	waitFor(t, func() error {
		rows := client.State().Agents
		for _, row := range rows {
			if row.ID == agentID {
				return nil
			}
		}
		return fmt.Errorf("the fleet client lists %d agents, none of them %s", len(rows), agentID)
	})

	// 5. The remote client opens the agent's session through the network
	// attach, and a screen dialled without a shared filesystem reads the turn.
	remote, err := daemon.Remote(srv.URL, tlsConfig, token)
	if err != nil {
		t.Fatalf("daemon.Remote: %v", err)
	}
	att, err := remote.AttachAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("attach to agent %q over TLS: %v", agentID, err)
	}
	if got := att.SessionID; got != agent.SessionID {
		_ = att.Conn.Close()
		t.Fatalf("the attach named session %q, want the agent's %q", got, agent.SessionID)
	}
	host, err := rpc.Dial(ctx, rpc.NewStream(att.Conn, att.Conn, rpc.MaxFrameBytes), rpc.DialOptions{SharedFilesystem: false})
	if err != nil {
		_ = att.Conn.Close()
		t.Fatalf("dial the attached stream: %v", err)
	}
	if host.SharedFilesystem() {
		_ = host.Close()
		t.Fatal("a server's session reports a shared filesystem")
	}
	session, err := host.Attach(ctx, att.SessionID)
	if err != nil {
		_ = host.Close()
		t.Fatalf("attach session %s: %v", att.SessionID, err)
	}
	viewer := &attachment{conn: att, host: host, session: session}
	assertTranscript(t, viewer, prompt, reply)

	// 6. The viewer leaves, and the fleet still lists the agent: the session
	// outlives the viewer that read it, as it does on the daemon.
	viewer.close()
	waitFor(t, func() error {
		for _, row := range client.State().Agents {
			if row.ID == agentID {
				return nil
			}
		}
		return fmt.Errorf("agent %s left the fleet when its viewer did", agentID)
	})
}

// TestAServerProfileRefusesAWrongToken: the bridge rejects another token's
// attach with 401, and the fleet client's state is rejected rather than
// retried (F3).
func TestAServerProfileRefusesAWrongToken(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	h := newServerProfileHarness(t)
	ctx := callCtx(t)

	agentID, _ := spawnFleetAgent(t, h.daemonHarness, ctx)
	srv, _, tlsConfig := h.resolve(t)

	wrong := func(context.Context) (string, error) { return "not-the-token", nil }

	// 1. The attach is refused by the bearer check.
	remote, err := daemon.Remote(srv.URL, tlsConfig, wrong)
	if err != nil {
		t.Fatalf("daemon.Remote: %v", err)
	}
	att, err := remote.AttachAgent(ctx, agentID)
	assertAttachRefused(t, att, err, http.StatusUnauthorized)

	// 2. The fleet client stops on the same answer: a rejected token is never
	// retried.
	client := fleetclient.New(fleetclient.HTTPS(srv.Name, srv.URL, tlsConfig, wrong))
	client.Start(context.Background())
	defer client.Close()
	waitFor(t, func() error {
		if got := client.State().Conn; got != acpclient.FleetRejected {
			return fmt.Errorf("the fleet client's state is %v (%s), want rejected", got, client.State().ConnErr)
		}
		return nil
	})
}

// TestTheServerFleetOpensAnAgentAndComesBack is W8's loop: launch.ServerFleet
// resolves the profile, opens the agent's session over TLS from the fleet
// program, and comes back to the fleet with the agent's row selected.
func TestTheServerFleetOpensAnAgentAndComesBack(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	h := newServerProfileHarness(t)
	ctx := callCtx(t)

	agentID, _ := spawnFleetAgent(t, h.daemonHarness, ctx)
	agent, ok := h.ws.Agent(agentID)
	if !ok {
		t.Fatalf("agent %s has no record", agentID)
	}
	srv, _, _ := h.resolve(t)

	var fleetPasses int
	runner := func(_ context.Context, m tea.Model, _ io.Writer) launch.ProgramResult {
		switch model := m.(type) {
		case fleet.Model:
			fleetPasses++
			if fleetPasses == 1 {
				// The operator opens the agent's row: this is the target the
				// screen's key produces for it.
				return launch.ProgramResult{Open: fleet.Target{
					AgentID:   agentID,
					AgentName: agent.Name,
					Root:      h.root,
				}}
			}
			// Back on the fleet, the agent is the selected row.
			if got := fleetSelected(t, model); got != agentID {
				t.Errorf("the fleet came back with %q selected, want the agent %q", got, agentID)
			}
			return launch.ProgramResult{}

		case tui.Model:
			if got := model.AgentSession(); got != agentID {
				t.Errorf("the session screen is agent %q's, want %q", got, agentID)
			}
			return launch.ProgramResult{}
		}
		t.Fatalf("the fleet loop ran a %T", m)
		return launch.ProgramResult{}
	}

	// The token and the TLS configuration are the profile's: ServerFleet
	// resolves both through fleetprofile before any screen runs, and every
	// request the loop makes is dialled over them.
	err := launch.ServerFleet(ctx, io.Discard, srv,
		launch.WithProgramRunner(runner), launch.WithNotify(false))
	if err != nil {
		t.Fatalf("ServerFleet: %v", err)
	}
	if fleetPasses != 2 {
		t.Fatalf("the fleet ran %d times, want 2", fleetPasses)
	}
}
