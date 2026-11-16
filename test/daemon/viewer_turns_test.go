package daemon_test

// The three tests here are plan 4c's proof of a viewer's turn against a real
// bridge: the turn runs on the project child's service, started by the viewer
// rather than by the bridge's own connection, and the bridge — which follows
// the agent's session after the spawn — reports it to the fleet. Each test
// spawns a fleet agent on the harness's project and attaches to its session
// over the child's viewer socket with viewerOf, as plan 4a's tests do.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/fleetclient"
	"marshal/web/bridge/contract"
)

// promptViewer starts a turn from the viewer's side and reports its error on
// the channel. Session.Prompt blocks until the turn ends, and the test needs
// the bridge while the turn is in flight.
func promptViewer(t *testing.T, ctx context.Context, viewer *attachment, text string) <-chan error {
	t.Helper()
	promptErr := make(chan error, 1)
	go func() {
		_, err := viewer.session.Prompt(ctx, acpwire.PromptParams{Text: text})
		promptErr <- err
	}()
	return promptErr
}

// commitMarshalIgnored commits a .gitignore covering .marshal/ into the
// harness's project, the way a repository carries one. The project child
// writes that file itself when it is missing, and an uncommitted .gitignore is
// a changed file of its own, so the turn's changed-files count would include
// it. Committed, it leaves the turn's file as the only change telemetry
// reports.
func commitMarshalIgnored(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".marshal/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"-c", "user.email=test@example.com", "-c", "user.name=Test",
			"commit", "-q", "-m", "ignore .marshal/"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
}

// TestAViewersTurnShowsAsRunningInTheFleet is the fleet's own view of a turn it
// did not start: the bridge follows the agent's session, the agent reports the
// service's run, and GET /api/agents plus the inbox say so while the turn runs
// and stop saying so when it ends.
func TestAViewersTurnShowsAsRunningInTheFleet(t *testing.T) {
	// The first reply is held on the gate, so the run is in flight while the
	// fleet is asked about it; the tool call it carries is what changes the
	// tree, so the changed-files count after the turn is the turn's own.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "file.write", ToolArgs: mustJSON(t, map[string]string{
			"path":    "viewer-turn.txt",
			"content": "written by the viewer's turn\n",
		}), gate: gate},
		{Text: "the turn is done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	commitMarshalIgnored(t, h.root)
	ctx := callCtx(t)

	// The client's stream outlives the waits below. run() returns when its
	// context ends and its refetches then fail silently, so State freezes at
	// its last value: on callCtx's 60s, a two-minute wait would burn its whole
	// budget polling stale rows and blame the fleet state. Close is the
	// teardown.
	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(context.Background())
	defer client.Close()

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	viewer := viewerOf(t, h, agentSID)
	defer viewer.close()
	promptErr := promptViewer(t, ctx, viewer, "write the note")

	// While the turn runs the bridge reports it running, and the inbox lists
	// it under running — the bridge did not start this turn, so this is the
	// followed session's report arriving.
	waitWithin(t, 2*time.Minute, "the fleet reporting the viewer's turn as running", func() error {
		st, ok := agentStatus(h, agentID)
		if !ok {
			return fmt.Errorf("agent %s is not in the fleet snapshot", agentID)
		}
		if st.Status != contract.AgentRunning {
			return fmt.Errorf("the agent is %q, want %q", st.Status, contract.AgentRunning)
		}
		if !inGroup(client.State().Inbox.Groups.Running, agentID) {
			return fmt.Errorf("the inbox does not list %s as running: %+v",
				agentID, client.State().Inbox.Groups)
		}
		return nil
	})

	// Opening the gate serves the held reply, so the tool call runs and the
	// scripted turn after it ends the run.
	close(gate)
	waitPrompt(t, promptErr)

	// The turn's end reaches the bridge too: the agent goes idle, reports the
	// one file the turn changed, and the inbox drops it from running.
	waitWithin(t, 2*time.Minute, "the fleet reporting the viewer's turn as ended", func() error {
		st, ok := agentStatus(h, agentID)
		if !ok {
			return fmt.Errorf("agent %s left the fleet snapshot", agentID)
		}
		if st.Status != contract.AgentIdle {
			return fmt.Errorf("the agent is %q, want %q", st.Status, contract.AgentIdle)
		}
		if st.ChangedFiles != 1 {
			return fmt.Errorf("the agent reports %d changed files, want the turn's 1", st.ChangedFiles)
		}
		if inGroup(client.State().Inbox.Groups.Running, agentID) {
			return fmt.Errorf("the inbox still lists %s as running: %+v",
				agentID, client.State().Inbox.Groups)
		}
		return nil
	})
	if got := fixture.Completions(); got != 2 {
		t.Fatalf("the provider served %d completions, want 2 (the tool call and the reply)", got)
	}
	for _, r := range fixture.Refusals() {
		t.Errorf("the provider refused a request: %s", r)
	}
}

// TestAViewersTurnsApprovalReachesTheFleet is a viewer's turn parked on an
// approval: the bridge lists it as the agent's pending request and under
// needs-you, and the dashboard's answer through POST /api/permissions/{id}
// finishes the viewer's turn and drops the viewer's own copy.
func TestAViewersTurnsApprovalReachesTheFleet(t *testing.T) {
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand})},
		{Text: "done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	// The project's confirmation rule is what makes the shell call ask, and the
	// trust record is what makes the project's own config apply. Both land
	// before the spawn, which starts the child.
	writeApprovalPolicy(t, h.root)
	trustProject(t, h.root)
	ctx := callCtx(t)

	// The stream outlives the waits below, as the first test's does.
	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(context.Background())
	defer client.Close()

	agentID, agentSID := spawnFleetAgent(t, h, ctx)
	viewer := viewerOf(t, h, agentSID)
	defer viewer.close()
	promptErr := promptViewer(t, ctx, viewer, "run the command")

	// The viewer's turn parks on the approval, and the bridge shows it as the
	// agent's pending request and as an inbox item that needs you.
	var pending contract.PendingRequest
	waitWithin(t, 2*time.Minute, "the viewer's approval reaching the fleet", func() error {
		found := false
		for _, row := range client.State().Agents {
			if row.ID != agentID {
				continue
			}
			found = true
			if row.Pending == nil {
				return fmt.Errorf("the agent has nothing pending (status %q)", row.Status)
			}
			if row.Pending.Kind != contract.PendingApproval {
				return fmt.Errorf("the agent's pending request is %q, want an approval", row.Pending.Kind)
			}
			pending = *row.Pending
		}
		if !found {
			return fmt.Errorf("agent %s is not among the %d rows", agentID, len(client.State().Agents))
		}
		if !inGroup(client.State().Inbox.Groups.NeedsYou, agentID) {
			return fmt.Errorf("the inbox does not list %s under needs-you: %+v",
				agentID, client.State().Inbox.Groups)
		}
		return nil
	})

	// The dashboard's answer goes to the tool call's ID, as the web UI posts it.
	if err := client.AnswerPermission(ctx, pending.ID, contract.Decision{Approved: true}); err != nil {
		t.Fatalf("answer the viewer's approval through the bridge: %v", err)
	}
	waitPrompt(t, promptErr)

	// The request is settled, so the viewer's mirror no longer lists it.
	waitWithin(t, 30*time.Second, "the viewer's mirror dropping the answered approval", func() error {
		if approvals := viewer.session.Pending().Approvals; len(approvals) != 0 {
			return fmt.Errorf("the viewer still mirrors %d approvals", len(approvals))
		}
		return nil
	})
	if got := fixture.Completions(); got != 2 {
		t.Fatalf("the provider served %d completions, want 2 (the tool call and the reply)", got)
	}
	for _, r := range fixture.Refusals() {
		t.Errorf("the provider refused a request: %s", r)
	}
}

// TestTheFleetCancelsAViewersTurn is the cancel direction: the viewer's turn is
// held mid-run, POST /api/sessions/{id}/cancel ends it, and the viewer's own
// status reports the run cancelled.
func TestTheFleetCancelsAViewersTurn(t *testing.T) {
	// The one turn holds on the gate, so the run is still in flight when the
	// fleet cancels it.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{{Text: "the held reply", gate: gate}})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// The stream outlives the waits below, as the first test's does.
	client := fleetclient.New(fleetclient.Socket(h.socket))
	client.Start(context.Background())
	defer client.Close()

	_, agentSID := spawnFleetAgent(t, h, ctx)
	viewer := viewerOf(t, h, agentSID)
	defer viewer.close()
	promptErr := promptViewer(t, ctx, viewer, "hold the turn open")

	waitRunInFlight(t, viewer, promptErr)
	runID := viewer.session.Status().Run.ID

	// The route names the session, not the agent: registryForSession tries a
	// session ID first.
	if err := client.Cancel(ctx, agentSID); err != nil {
		t.Fatalf("cancel the viewer's turn through the bridge: %v", err)
	}

	// The cancel stops the run the service owns, and the viewer's mirror of
	// the last run reports it cancelled.
	waitOutcome(t, viewer, runID, acpwire.RunCancelled)

	// The prompt call returns with the stop rather than blocking forever. Its
	// own result is not what this test asserts — the status above is — so a
	// non-nil error here is reported, not failed on.
	select {
	case err := <-promptErr:
		t.Logf("the cancelled prompt returned: %v", err)
	case <-time.After(2 * time.Minute):
		t.Fatal("the viewer's prompt did not return after the fleet cancelled its turn")
	}
	// The cancelled turn is the only refusal this test expects: the scripted
	// provider records a gated turn cut short by its context as a refusal, and
	// that cancellation is the thing under test. Any other refusal would be a
	// request this test never asked for.
	for _, r := range fixture.Refusals() {
		if r == "turn 0 cancelled while gated" {
			continue
		}
		t.Errorf("the provider refused a request: %s", r)
	}
}
