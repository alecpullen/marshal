package daemon_test

// W9 is a containerized agent's session driven end to end through the real
// agent: `acp.ListenAndServeWithViewers` serves a control socket and a viewer
// socket in this process, the control client opens a session in a project as
// the bridge's connection would, and a marshal client on the viewer socket
// opens the session screen, runs a turn from it, and is refused a user-config
// write with decision 2's sentence.
//
// Nothing here is faked: the agent is the one `marshal acp --listen` with
// viewers runs, its runtime starts on the harness's provider fixture, and the
// frame is the one Bubble Tea would paint.

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acp"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/tui"
)

// containerRefusal is the trailer a containerized agent's viewer answers every
// method off its allowlist with (decision 2).
const containerRefusal = "is not available for a containerized agent"

// setValueRefusal is decision 2's sentence for the settings' write the session
// screen would otherwise make, verbatim.
const setValueRefusal = "config/set_value is not available for a containerized agent"

// serveContainerPair serves a control socket and a viewer socket over one host,
// the viewers on the container allowlist, as `marshal acp --listen` with
// viewers does. It returns the two socket paths and the project directory a
// session can be opened in; the run ends with the test.
func serveContainerPair(t *testing.T) (control, viewers, project string) {
	t.Helper()
	// A unix socket path is capped near 104 bytes on macOS, so the directory is
	// short rather than t.TempDir()'s, as the daemon harness's is.
	dir, err := os.MkdirTemp("", "p58")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	project = filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("create project: %v", err)
	}
	initProject(t, project)

	control = filepath.Join(dir, "control.sock")
	viewers = filepath.Join(dir, "viewers.sock")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- acp.ListenAndServeWithViewers(ctx, "unix", control, viewers, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("acp.ListenAndServeWithViewers: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("acp.ListenAndServeWithViewers did not return after its context ended")
		}
	})

	// Both sockets bind before either is served, and a dial would otherwise race
	// the goroutine that binds them.
	waitFor(t, func() error {
		for _, path := range []string{control, viewers} {
			if _, err := os.Stat(path); err != nil {
				return err
			}
		}
		return nil
	})
	return control, viewers, project
}

// openControlSession opens a session in project over the control socket, as the
// bridge's own connection does: initialize with the protocol version alone,
// then session/new naming the project's working directory.
func openControlSession(t *testing.T, path, project string) string {
	t.Helper()
	ctx := callCtx(t)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the control socket %s: %v", path, err)
	}
	client := rpc.NewConn(rpc.NewStream(conn, conn, rpc.MaxFrameBytes), rpc.Handlers{})
	t.Cleanup(func() {
		_ = client.Close()
		_ = conn.Close()
	})

	var initialized acpwire.InitializeResult
	if err := client.Call(ctx, "initialize", map[string]any{"protocolVersion": 1}, &initialized); err != nil {
		t.Fatalf("initialize on the control socket: %v", err)
	}
	var opened struct {
		SessionID string `json:"sessionId"`
	}
	params := map[string]any{"cwd": project, "mcpServers": []any{}}
	if err := client.Call(ctx, acpwire.MethodSessionNew, params, &opened); err != nil {
		t.Fatalf("session/new on the control socket: %v", err)
	}
	if opened.SessionID == "" {
		t.Fatal("session/new on the control socket named no session")
	}
	return opened.SessionID
}

// TestAContainerViewerDrivesTheSessionScreen is W9 at the agent's end: the
// viewer opens the screen the control connection's session belongs to, runs a
// turn on it, and is refused the write that would reach the host's config.
func TestAContainerViewerDrivesTheSessionScreen(t *testing.T) {
	const prompt = "say something through the container's viewer"
	const reply = "the reply from the container's agent"

	fixture := newProviderFixture(t, []scriptedTurn{{Text: reply}})
	// The agent runs in this process, so the fixture's config is the process's:
	// useProvider points MARSHAL_CONFIG_DIR at it before the first runtime
	// starts.
	fixture.useProvider(t)

	control, viewerSocket, project := serveContainerPair(t)

	// 1. The bridge's connection opens the session the fleet will show.
	sessionID := openControlSession(t, control, project)

	// 2. A marshal client on the viewer socket attaches to it: the connection
	// the containerized agent's screen runs on.
	ctx := callCtx(t)
	conn, err := net.Dial("unix", viewerSocket)
	if err != nil {
		t.Fatalf("dial the viewer socket %s: %v", viewerSocket, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	host, err := rpc.Dial(ctx, rpc.NewStream(conn, conn, rpc.MaxFrameBytes), rpc.DialOptions{})
	if err != nil {
		t.Fatalf("dial the viewer stream: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	session, err := host.Attach(ctx, sessionID)
	if err != nil {
		t.Fatalf("attach session %s over the viewer socket: %v", sessionID, err)
	}

	// 3. The session screen the fleet screen opens renders over it, and its
	// first frame carries no refusal: everything the screen reads to open and
	// draw is on the container's list.
	m := tui.New(host, session, tui.WithAgentSession("container-agent", "container agent"))
	frame := sessionFrame(m)
	if frame == "" {
		t.Fatal("the session screen rendered no frame")
	}
	if strings.Contains(frame, containerRefusal) {
		t.Fatalf("the session screen's first frame carries a refusal:\n%s", frame)
	}

	// 4. A prompt from the viewer's side runs a real turn on the session's
	// runtime, and the reply is in the transcript the viewer reads.
	if _, err := session.Prompt(ctx, acpwire.PromptParams{Text: prompt}); err != nil {
		t.Fatalf("prompt through the viewer: %v", err)
	}
	page, err := session.Messages(ctx, acpwire.MessageQuery{})
	if err != nil {
		t.Fatalf("messages through the viewer: %v", err)
	}
	if !carries(page, reply) {
		t.Fatalf("the transcript the viewer read does not carry %q: %+v", reply, page.Messages)
	}

	// 5. A user-config write is off the container's list: the viewer is
	// answered with decision 2's sentence and the file is not touched.
	userConfig := session.Status().UserConfigPath
	if userConfig == "" {
		t.Fatal("the session names no user config file")
	}
	before, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("read the user config %s: %v", userConfig, err)
	}
	if err := host.SaveUserValue(ctx, "tools.shell.allow_network", true); err == nil {
		t.Fatalf("the viewer wrote %s", userConfig)
	} else if !strings.Contains(err.Error(), setValueRefusal) {
		t.Fatalf("the viewer's config/set_value = %q, want %q", err, setValueRefusal)
	}
	after, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("re-read the user config %s: %v", userConfig, err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the refused write changed %s:\n--- before ---\n%s\n--- after ---\n%s", userConfig, before, after)
	}

	if got := fixture.Completions(); got != 1 {
		t.Fatalf("the provider served %d completions, want the turn's 1", got)
	}
	for _, r := range fixture.Refusals() {
		t.Errorf("the provider refused a request: %s", r)
	}
}
