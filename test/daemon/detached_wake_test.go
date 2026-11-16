package daemon_test

// TestADetachedSessionsWakeRuns is L7 against the real bridge: a session on a
// project child carries a repeating watch on a command the test controls, its
// screen detaches, the test makes the watch fire, and the session resumes
// itself — its turn runs on the provider fixture while no viewer is on the
// session. A client that attaches afterwards finds the run in the transcript.
//
// Watch resume is on in the child's config: config.Default() enables it, and
// this package's childConfig does not turn it off.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
)

// The wake's two names: what the fixture replies to the turn the wake starts,
// and the watch the test registers.
const (
	wakeReply     = "the wake's reply"
	wakeWatchName = "wake"
)

// directViewer is one ACP connection to the project child that the test speaks
// to itself. Watches in production are started by the bridge rather than by a
// viewer, so the rpc client has no session/watch_start wrapper; the test builds
// the connection and sends the method, and counts as no viewer: it never
// subscribes to the session's status.
type directViewer struct {
	conn *rpc.Conn
	sid  string
}

// attachDirectly attaches to the daemon and speaks ACP over the stream.
func attachDirectly(t *testing.T, h *daemonHarness, sessionID string) *directViewer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	att, err := h.client.Attach(ctx, h.root, sessionID)
	if err != nil {
		t.Fatalf("attach a direct viewer: %v", err)
	}
	conn := rpc.NewConn(rpc.NewStream(att.Conn, att.Conn, rpc.MaxFrameBytes), rpc.Handlers{})
	t.Cleanup(func() { _ = conn.Close() })

	var init acpwire.InitializeResult
	if err := conn.Call(ctx, "initialize", acpwire.InitializeParams{
		ProtocolVersion: acpwire.MarshalProtocolVersion,
		ClientCapabilities: &acpwire.ClientCapabilities{
			Marshal: &acpwire.MarshalCapability{ProtocolVersion: acpwire.MarshalProtocolVersion},
		},
	}, &init); err != nil {
		t.Fatalf("initialize the direct viewer: %v", err)
	}
	return &directViewer{conn: conn, sid: att.SessionID}
}

// close ends this connection's stream.
func (d *directViewer) close() { _ = d.conn.Close() }

// startWatch registers a watch on the session and returns its ID.
func (d *directViewer) startWatch(t *testing.T, spec map[string]any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var res struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := d.conn.Call(ctx, "session/watch_start", map[string]any{
		"sessionId": d.sid,
		"spec":      spec,
	}, &res); err != nil {
		t.Fatalf("session/watch_start: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("session/watch_start named no watch: %+v", res)
	}
	return res.ID
}

// stopWatch ends the watch.
func (d *directViewer) stopWatch(t *testing.T, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var res struct{}
	if err := d.conn.Call(ctx, "session/watch_stop", map[string]any{
		"sessionId": d.sid,
		"id":        id,
	}, &res); err != nil {
		t.Fatalf("session/watch_stop: %v", err)
	}
}

func TestADetachedSessionsWakeRuns(t *testing.T) {
	// The wake's turn is the only turn this test runs, and the reply is what
	// the transcript has to carry afterwards. A repeat watch may fire again
	// before the test stops it, so a few identical replies are scripted: the
	// extra wakes are allowed to run, an unscripted request never is.
	fixture := newProviderFixture(t, []scriptedTurn{
		{Text: wakeReply}, {Text: wakeReply}, {Text: wakeReply},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)

	// The screen: a marshal client on a new session of the project child.
	screen := h.attach("")
	sid := screen.conn.SessionID

	// The watch: a command the test controls, so the fire is the test's to
	// time — `test -f` trips once the flag file exists — repeating, so a fire
	// that races the detach is retried on the next sample, with resume on.
	flag := filepath.Join(h.root, "wake-flag")
	starter := attachDirectly(t, h, sid)
	watchID := starter.startWatch(t, map[string]any{
		"name":       wakeWatchName,
		"kind":       "command",
		"command":    "test -f '" + flag + "'",
		"condition":  "exit_code 0",
		"mode":       "repeat",
		"resume":     true,
		"intervalMs": int64(2_000),
	})

	// Both streams end. The watch lives on the session rather than on a
	// connection, and the session is left with no viewer at all: the screen is
	// gone, so a resume is the session's own to make.
	starter.close()
	screen.close()
	waitWithin(t, 2*time.Minute, "the session's viewers leaving", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		viewers, ok := listing(info, sid)
		if !ok {
			return fmt.Errorf("session %s is not listed", sid)
		}
		if viewers != 0 {
			return fmt.Errorf("session %s still has %d viewers", sid, viewers)
		}
		return nil
	})

	// The test makes the watch fire.
	if err := os.WriteFile(flag, []byte("trip\n"), 0o644); err != nil {
		t.Fatalf("create the watch's flag file: %v", err)
	}

	// With no viewer on the session, the fire is the session's own to resume:
	// the wake's turn runs on the provider fixture.
	waitWithin(t, 2*time.Minute, "the session resuming its watch on the provider fixture", func() error {
		if got := fixture.Completions(); got < 1 {
			return fmt.Errorf("the provider has served %d completions", got)
		}
		return nil
	})

	// The watch repeats, so it is stopped before the session is read: a later
	// fire would be one more wake to account for.
	stopper := attachDirectly(t, h, sid)
	stopper.stopWatch(t, watchID)
	stopper.close()

	// A client that attaches afterwards finds the run in the transcript.
	later := h.attach(sid)
	defer later.close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	waitWithin(t, 2*time.Minute, "the wake's run in the session's transcript", func() error {
		page, err := later.session.Messages(ctx, acpwire.MessageQuery{})
		if err != nil {
			return err
		}
		if !carries(page, wakeReply) {
			return fmt.Errorf("the transcript carries no reply from the wake's run")
		}
		return nil
	})
	for _, r := range fixture.Refusals() {
		t.Errorf("the provider refused a request: %s", r)
	}
}
