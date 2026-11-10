package daemon_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/launch"
	"marshal/internal/trust"
)

// approvalCommand is the command the scripted agent runs. Act mode with Auto
// off does not by itself ask for a shell command: the project's own
// confirmation rule is what puts the approval in front of the viewer, which is
// also what the rule below pins.
const approvalCommand = "rm -f approval-marker.txt"

// writeApprovalPolicy gives the project a confirmation rule for exactly this
// command and passthrough execution, so the command runs on the host rather
// than inside the restricted sandbox. The workspace is trusted below, which is
// what makes this project layer apply.
func writeApprovalPolicy(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".marshal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	policy := `[tools.shell.confirm]
commands = ["` + approvalCommand + `"]

[tools.shell.sandbox]
backend = "passthrough"
unsafe_passthrough = true
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(policy), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// trustProject seeds the trust store the project child reads, so the project's
// own config is merged instead of being ignored as untrusted. The store lives
// under this process's data dir, which the child inherits.
func trustProject(t *testing.T, root string) {
	t.Helper()
	hash, err := trust.ConfigHashFor(root)
	if err != nil {
		t.Fatalf("hash project config: %v", err)
	}
	if hash == "" {
		t.Fatal("the workspace has no project config for the trust record to cover")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("find home directory: %v", err)
	}
	store := trust.NewStore(config.DataDir(home))
	if err := store.SetTrust(trust.Canonicalize(root), true, hash); err != nil {
		t.Fatalf("seed trust store: %v", err)
	}
}

// waitRunInFlight waits until the attached session reports a run in flight. A
// prompt that returns first is reported as the failure it is, rather than as a
// timeout: its error is the reason the run never started.
func waitRunInFlight(t *testing.T, a *attachment, promptErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if a.session.Status().Run != nil {
			return
		}
		select {
		case err := <-promptErr:
			t.Fatalf("the prompt returned without starting a run: %v (status %+v)", err, a.session.Status())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no run started: status %+v, pending %+v", a.session.Status(), a.session.Pending())
}

// waitApproval waits for the shell.run approval to reach the viewer and returns
// its key. A timeout dumps the run's state: no approval means the turn either
// never reached the tool or asked somewhere else, and the dump says which.
func waitApproval(t *testing.T, ctx context.Context, b *attachment, fixture *providerFixture) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, ap := range b.session.Pending().Approvals {
			if ap.Tool == "shell.run" {
				return ap.Key
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := b.session.Status()
	page, _ := b.session.Messages(ctx, acpwire.MessageQuery{})
	t.Fatalf("no shell.run approval reached the reattached client: run=%+v lastRun=%+v pending=%+v completions=%d\n%s",
		st.Run, st.LastRun, b.session.Pending(), fixture.Completions(), transcript(page))
	return ""
}

// transcript renders a message page for a failure dump.
func transcript(page acpwire.MessagePage) string {
	var b strings.Builder
	for _, m := range page.Messages {
		content := m.Content
		if len(content) > 400 {
			content = content[:400] + "…"
		}
		fmt.Fprintf(&b, "  [%s] %s\n", m.Role, content)
	}
	return b.String()
}
func waitViewers(t *testing.T, h *daemonHarness, id string, want int) {
	t.Helper()
	waitFor(t, func() error {
		info, err := h.client.Info(context.Background())
		if err != nil {
			return err
		}
		got, ok := listing(info, id)
		if !ok {
			return fmt.Errorf("session %s is not listed", id)
		}
		if got != want {
			return fmt.Errorf("session %s has %d viewers, want %d", id, got, want)
		}
		return nil
	})
}

// waitOutcome waits until the session's last finished run is id with want.
func waitOutcome(t *testing.T, a *attachment, id string, want acpwire.RunOutcome) {
	t.Helper()
	waitFor(t, func() error {
		last := a.session.Status().LastRun
		if last == nil {
			return fmt.Errorf("no finished run yet")
		}
		if last.ID != id {
			return fmt.Errorf("the last run is %s, want %s", last.ID, id)
		}
		if last.Outcome != want {
			return fmt.Errorf("the run ended %q, want %q", last.Outcome, want)
		}
		return nil
	})
}

// TestAReattachedClientSeesTheRunToItsEnd is W1: a run started by one client
// survives that client leaving and is followed to its outcome by the next.
func TestAReattachedClientSeesTheRunToItsEnd(t *testing.T) {
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{
		{ToolName: "shell.run", ToolArgs: mustJSON(t, map[string]string{"command": approvalCommand}), gate: gate},
		{Text: "reattached and done"},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	// The project's confirmation rule is what makes the shell call ask, and
	// the trust record is what makes the project's own config apply. Both
	// land before the child starts, which the first attach does.
	writeApprovalPolicy(t, h.root)
	trustProject(t, h.root)

	// Client A starts the run and leaves while the provider still holds it.
	a := h.attach("")
	id := a.conn.SessionID
	ctx := callCtx(t)
	started := make(chan struct{})
	promptErr := make(chan error, 1)
	go func() {
		close(started)
		_, err := a.session.Prompt(ctx, acpwire.PromptParams{Text: "run the command"})
		promptErr <- err
	}()
	<-started
	waitRunInFlight(t, a, promptErr)
	runID := a.session.Status().Run.ID
	a.close()
	waitViewers(t, h, id, 0)

	// Client B attaches by ID and finds the same run in flight.
	b := h.attach(id)
	defer b.close()
	if got := b.session.Status().Run; got == nil {
		t.Fatal("the reattached client does not show the run in flight")
	} else if got.ID != runID {
		t.Fatalf("the reattached client shows run %q, want %q", got.ID, runID)
	}

	// The provider now answers; the agent asks for approval, which arrives in
	// the new viewer's pending mirror rather than the one that left.
	close(gate)
	key := waitApproval(t, ctx, b, fixture)
	if err := b.session.AnswerApproval(ctx, key, acpwire.ApprovalAnswer{Decision: acpwire.ApprovalOnce}); err != nil {
		t.Fatalf("answer the approval: %v", err)
	}

	waitOutcome(t, b, runID, acpwire.RunDone)
	page, err := b.session.Messages(ctx, acpwire.MessageQuery{})
	if err != nil {
		t.Fatalf("messages: %v", err)
	}
	if !carries(page, "reattached and done") {
		t.Fatal("the reattached client's transcript does not carry the turn's reply")
	}
	if got := fixture.Completions(); got != 2 {
		t.Fatalf("the provider saw %d completions, want exactly 2", got)
	}
	for _, r := range fixture.Refusals() {
		t.Errorf("the provider refused a request: %s", r)
	}
}

// TestTheTUIDetachesFromARunningTurn is W5: the TUI detaches from a run it
// started, prints how to come back, and the run finishes without it.
func TestTheTUIDetachesFromARunningTurn(t *testing.T) {
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{
		{Text: "the turn outlived the viewer", gate: gate},
	})
	fixture.useProvider(t)
	h := newDaemonHarness(t)

	a := h.attach("")
	id := a.conn.SessionID
	ctx := callCtx(t)
	started := make(chan struct{})
	promptErr := make(chan error, 1)
	go func() {
		close(started)
		_, err := a.session.Prompt(ctx, acpwire.PromptParams{Text: "wait for me"})
		promptErr <- err
	}()
	<-started
	waitRunInFlight(t, a, promptErr)
	a.close()
	waitViewers(t, h, id, 0)

	// The TUI attaches from inside the project, as a user would after cd-ing
	// there, and detaches once its viewer is on the session.
	t.Chdir(h.root)
	var out bytes.Buffer
	runner := func(ctx context.Context, _ tea.Model, _ io.Writer) launch.ProgramResult {
		waitViewers(t, h, id, 1)
		return launch.ProgramResult{Detached: id}
	}
	if err := launch.Attach(ctx, &out, h.socket, id, launch.WithProgramRunner(runner)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !strings.Contains(out.String(), "marshal attach "+id) {
		t.Fatalf("the detach's output %q does not name the reattach command", out.String())
	}

	// The run is still in flight for the next viewer, and finishes without the
	// TUI that started it.
	b := h.attach(id)
	defer b.close()
	if got := b.session.Status().Run; got == nil {
		t.Fatal("the run did not survive the detach")
	}
	runID := b.session.Status().Run.ID
	close(gate)
	waitOutcome(t, b, runID, acpwire.RunDone)
}
