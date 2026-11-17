package daemon_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpwire"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/reconnect"
	"marshal/internal/launch"
)

// draftText is what the test types into the screen: the composer's text, and
// what the screen that comes back has to hold.
const draftText = "keep this draft through the restart"

// TestAScreenComesBackAfterTheChildRestarts is L1's screen half against a real
// bridge: the project child serving a session dies as a crash ends it, the
// bridge respawns it as it respawns a crashed child, and the loop reconnects to
// the same session — whose screen opens with the draft the lost one held, on a
// session that still carries its transcript.
func TestAScreenComesBackAfterTheChildRestarts(t *testing.T) {
	// A user config exists, so the screen opens on its composer rather than
	// the first-run connect panel: this test types into it.
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// The child writes its process ID here when it starts (main_test.go), so
	// the test can end it: GET /api/daemon reports none.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(childPIDEnv, pidFile)

	// The session the screen runs on: opened here, with a line in its
	// transcript, so the reconnected screen has an ID and a transcript to
	// check. The attachment goes before launch opens its own viewer.
	opened := h.attach("")
	id := opened.conn.SessionID
	const note = "a line from before the restart"
	if err := opened.session.AddNote(ctx, acpwire.Note{Text: note, Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("add the note: %v", err)
	}
	opened.close()

	// launch.Attach resolves the project from the working directory, as the
	// user who cd-ed into it does.
	t.Chdir(h.root)

	var (
		passes int
		typed  tui.Draft
	)
	runner := func(runCtx context.Context, m tea.Model, _ io.Writer) launch.ProgramResult {
		switch model := m.(type) {
		case tui.Model:
			passes++
			if passes == 1 {
				// The first pass types the draft, then ends the child's
				// process and waits for its own context to end: the bridge
				// ends the relay when the child goes, which is what cancels
				// this screen.
				screen, draft := typeDraft(t, model, draftText)
				typed = draft
				endTheChild(t, pidFile)
				select {
				case <-runCtx.Done():
				case <-time.After(2 * time.Minute):
					t.Fatal("the screen's context never ended after the child was ended")
				}
				return launch.ProgramResult{Draft: screen.Draft()}
			}
			// The second pass runs on the session the first one was on.
			if got := model.Session().ID(); got != id {
				t.Errorf("the screen came back on session %q, want %q", got, id)
			}
			if got := model.Draft(); !reflect.DeepEqual(got, typed) {
				t.Error("the screen came back without the draft the lost one held")
			}
			page, err := model.Session().Messages(runCtx, acpwire.MessageQuery{})
			if err != nil {
				t.Errorf("read the resumed session's transcript: %v", err)
			} else if !carries(page, note) {
				t.Errorf("the resumed session's transcript does not hold %q: %+v", note, page.Messages)
			}
			return launch.ProgramResult{}

		case reconnect.Model:
			// The waiting screen between the two: launch dials while it runs,
			// so it is driven here until launch's attempts end it.
			waitOut(t, model)
			return launch.ProgramResult{}
		}
		t.Fatalf("the loop ran a %T", m)
		return launch.ProgramResult{}
	}

	if err := launch.Attach(ctx, io.Discard, h.socket, id, launch.WithProgramRunner(runner)); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if passes != 2 {
		t.Fatalf("session screens = %d, want 2: the screen did not come back", passes)
	}
}

// typeDraft types text into the screen's composer with key messages, as the
// user does, and returns the model it leaves and the draft the screen holds.
func typeDraft(t *testing.T, m tui.Model, text string) (tui.Model, tui.Draft) {
	t.Helper()
	for _, r := range text {
		next, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		typed, ok := next.(tui.Model)
		if !ok {
			t.Fatalf("the screen updated to %T, want tui.Model", next)
		}
		m = typed
	}
	if m.Draft().Empty() {
		t.Fatalf("typing %q left an empty draft", text)
	}
	return m, m.Draft()
}

// endTheChild ends the child's process the way a crash does: SIGTERM is not
// the bridge's Stop, so its supervisor respawns it.
func endTheChild(t *testing.T, pidFile string) {
	t.Helper()
	pid := childPID(t, pidFile)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("end the child's process %d: %v", pid, err)
	}
}

// childPID waits for the child to write its process ID and returns it.
func childPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the child never wrote its process id to %s", path)
	return 0
}

// waitOut runs the waiting screen until launch's attempts end it, as the
// program would, and fails the test if it never ends.
//
// Launch sends each failed attempt on a channel only the screen reads, so a
// runner that ignored the screen would leave the attempts blocked on that send
// until the window was over. The timeout is the bound: a screen that never
// ends is a failure, not a hang.
func waitOut(t *testing.T, m reconnect.Model) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		driveWaiting(m)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("the waiting screen never ended")
	}
}

// driveWaiting runs the waiting screen's commands and feeds what they produce
// back into Update, until the screen quits — a tea.Program's loop, for a model
// this test only needs to read.
func driveWaiting(m tea.Model) {
	pending := []tea.Cmd{m.Init()}
	for len(pending) > 0 {
		cmd := pending[0]
		pending = pending[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			pending = append(pending, batch...)
			continue
		}
		if _, quitting := msg.(tea.QuitMsg); quitting {
			return
		}
		if msg == nil {
			continue
		}
		next, nextCmd := m.Update(msg)
		m = next
		if nextCmd != nil {
			pending = append(pending, nextCmd)
		}
	}
}
