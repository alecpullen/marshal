package daemon_test

// The tests here are P6 4's end-to-end proof against a real bridge and a real
// project child: a child that started on one marshal build is restarted on a
// new one once it is idle (L1), held while a turn or a watch runs (L2),
// refused when the new build cannot start at all (L3), held on the one project
// whose session did not write the user config (L4), and resumed untrusted
// after its project config is edited (L5).
//
// The project child is this test binary, as in the rest of the package.
// ownedBinary copies it, so a test can replace the file the bridge spawns and
// the new process is still the child; main_test.go answers the bridge's
// `--version` check with the build the copy stands for (MARSHAL_DAEMON_TEST_BUILD).

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/tui"
	"marshal/internal/app/tui/reconnect"
	"marshal/internal/daemon"
	"marshal/internal/launch"
)

// marshalBinaryEnv is the variable newDaemonHarness reads the project child's
// binary from.
const marshalBinaryEnv = "MARSHAL_DAEMON_TEST_MARSHAL"

// restartNote is the sweep's own note before a restart: the version it is
// restarting on, then the reasons the child reported (web/bridge/restart.go).
const restartNote = "Restarting on rebuilt: a new marshal build."

// restartLookahead is how far ahead a sweep is asked to look. A sweep keeps a
// child whose sessions are watched and were active within its quiet window
// (two minutes), so a test that wants the restart to happen now sweeps in the
// future, as an idle project's next sweep is.
const restartLookahead = 3 * time.Minute

// ownedBinary copies this test binary into a temporary directory and points
// MARSHAL_DAEMON_TEST_MARSHAL at the copy, so a test can replace the file the
// bridge spawns without touching the binary it is running. It must run before
// newDaemonHarness, which reads the variable.
func ownedBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "marshal")
	if err := os.Rename(freshBinary(t, filepath.Dir(path)), path); err != nil {
		t.Fatalf("place the child's binary at %s: %v", path, err)
	}
	t.Setenv(marshalBinaryEnv, path)
	return path
}

// rebuild writes a fresh copy beside path and renames it over path, so the
// file has a new identity — a new inode, size and modification time — and the
// child's start record reports the build as new. It also names the build the
// copy answers for.
func rebuild(t *testing.T, path, build string) {
	t.Helper()
	tmp := freshBinary(t, filepath.Dir(path))
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		t.Fatalf("replace %s with a new build: %v", path, err)
	}
	t.Setenv(buildEnv, build)
}

// breakBuild replaces path with a script that fails at once, so the bridge's
// check of the new build sees a binary that cannot start.
func breakBuild(t *testing.T, path string) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), "marshal-broken-*")
	if err != nil {
		t.Fatalf("create the broken build beside %s: %v", path, err)
	}
	script := "#!/bin/sh\nprintf 'broken build\\n' >&2\nexit 1\n"
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		t.Fatalf("write the broken build: %v", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		t.Fatalf("close the broken build: %v", err)
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		_ = os.Remove(f.Name())
		t.Fatalf("make the broken build executable: %v", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Remove(f.Name())
		t.Fatalf("replace %s with the broken build: %v", path, err)
	}
}

// freshBinary writes a copy of this test binary into dir, executable, and
// returns its path.
func freshBinary(t *testing.T, dir string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("find this test binary: %v", err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatalf("open this test binary: %v", err)
	}
	defer src.Close()
	dst, err := os.CreateTemp(dir, "marshal-*")
	if err != nil {
		t.Fatalf("create a binary in %s: %v", dir, err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(dst.Name())
		t.Fatalf("copy this test binary: %v", copyErr)
	}
	if err := os.Chmod(dst.Name(), 0o755); err != nil {
		_ = os.Remove(dst.Name())
		t.Fatalf("make the copied binary executable: %v", err)
	}
	return dst.Name()
}

// tryAttach is h.attach without the fatal failure, so a wait can retry it
// while a restart's resume lands.
func tryAttach(h *daemonHarness, id string) (*attachment, error) {
	return tryAttachAt(h, h.root, id)
}

// tryAttachAt is tryAttach for a session on root, which is how the second
// project of L4 is attached.
func tryAttachAt(h *daemonHarness, root, id string) (*attachment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := h.client.Attach(ctx, root, id)
	if err != nil {
		return nil, fmt.Errorf("attach to %q on %s: %w", id, root, err)
	}
	host, err := rpc.Dial(ctx, rpc.NewStream(conn.Conn, conn.Conn, rpc.MaxFrameBytes), rpc.DialOptions{})
	if err != nil {
		_ = conn.Conn.Close()
		return nil, fmt.Errorf("dial the attached stream: %w", err)
	}
	session, err := host.Attach(ctx, conn.SessionID)
	if err != nil {
		_ = host.Close()
		return nil, fmt.Errorf("attach session %s: %w", conn.SessionID, err)
	}
	return &attachment{conn: conn, host: host, session: session}, nil
}

// childAt is root's project child as GET /api/daemon reports it.
func childAt(t *testing.T, h *daemonHarness, root string) *daemon.ChildInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	info, err := h.client.Info(ctx)
	if err != nil {
		t.Fatalf("read the daemon's answer: %v", err)
	}
	want := filepath.Clean(root)
	for i := range info.Children {
		if filepath.Clean(info.Children[i].Root) == want {
			return &info.Children[i]
		}
	}
	t.Fatalf("the daemon lists no child for %s: %+v", root, info.Children)
	return nil
}

// childRestart is the harness root's project child.
func childRestart(t *testing.T, h *daemonHarness) *daemon.ChildInfo {
	t.Helper()
	return childAt(t, h, h.root)
}

// TestARebuiltMarshalReachesAnIdleProject is L1: the file the bridge spawns is
// replaced by a new build, a sweep restarts the idle child on it, and the
// screen that lost its connection comes back on the same session — with its
// draft, its transcript, and the sweep's note.
func TestARebuiltMarshalReachesAnIdleProject(t *testing.T) {
	// A user config exists, so the screen opens on its composer rather than
	// the first-run connect panel: this test types into it.
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	bin := ownedBinary(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

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

	// launch.Attach resolves the project from the working directory.
	t.Chdir(h.root)

	var (
		passes int
		typed  tui.Draft
		waited string
	)
	swept := make(chan struct{})
	runner := func(runCtx context.Context, m tea.Model, _ io.Writer) launch.ProgramResult {
		switch model := m.(type) {
		case tui.Model:
			passes++
			if passes == 1 {
				// The first pass types the draft, then rebuilds the binary
				// the bridge spawns and starts the sweep that restarts the
				// child on it. The restart ends this screen's connection.
				screen, draft := typeDraft(t, model, draftText)
				typed = draft
				rebuild(t, bin, "rebuilt")
				go func() {
					defer close(swept)
					h.fleet.SweepRestarts(context.Background(), time.Now().Add(restartLookahead))
				}()
				select {
				case <-runCtx.Done():
				case <-time.After(2 * time.Minute):
					t.Fatal("the screen's context never ended after the restart")
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
			} else {
				if !carries(page, note) {
					t.Errorf("the resumed session's transcript does not hold %q: %+v", note, page.Messages)
				}
				if !carries(page, restartNote) {
					t.Errorf("the resumed session's transcript does not hold %q: %+v", restartNote, page.Messages)
				}
			}
			return launch.ProgramResult{}

		case reconnect.Model:
			// The waiting screen between the two: it is read wide so the
			// restart's sentence stays on one row, then driven until launch's
			// attempts end it.
			sized, _ := model.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
			waiting := sized.(reconnect.Model)
			waited = waiting.View().Content
			waitOut(t, waiting)
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
	select {
	case <-swept:
	case <-time.After(2 * time.Minute):
		t.Fatal("the sweep never returned")
	}
	if !strings.Contains(waited, "is restarting: a new marshal build") {
		t.Errorf("the waiting screen does not say why it waits:\n%s", waited)
	}
}

// TestARunningTurnHoldsTheRestart is L2's turn half: a stale child with a run
// in flight holds its restart, and restarts once the run has ended.
func TestARunningTurnHoldsTheRestart(t *testing.T) {
	// The one turn holds on the gate, so the run is in flight when the sweep
	// looks at the child.
	gate := make(chan struct{})
	fixture := newProviderFixture(t, []scriptedTurn{{Text: "the held reply", gate: gate}})
	fixture.useProvider(t)
	bin := ownedBinary(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	a := h.attach("")
	id := a.conn.SessionID
	promptErr := promptViewer(t, ctx, a, "hold the turn open")
	waitRunInFlight(t, a, promptErr)

	rebuild(t, bin, "rebuilt")
	h.fleet.SweepRestarts(ctx, time.Now().Add(restartLookahead))

	// The child reports the restart it holds, and what holds it, to its
	// sessions' status.
	waitWithin(t, 30*time.Second, "the held restart reaching the session's status", func() error {
		restart := a.session.Status().Restart
		if restart == nil {
			return fmt.Errorf("the status carries no restart")
		}
		if !strings.HasPrefix(restart.WaitingFor, "a run in") {
			return fmt.Errorf("the restart waits for %q, want a run in …", restart.WaitingFor)
		}
		return nil
	})
	// Held means held: the child is still the one serving the session, so the
	// run is still in flight and the daemon still holds the restart.
	if got := childRestart(t, h); got.Restart == nil || !strings.HasPrefix(got.Restart.WaitingFor, "a run in") {
		t.Fatalf("the child's restart = %+v, want it held on a run", got.Restart)
	}
	if got := a.session.Status().Run; got == nil {
		t.Fatal("the sweep ended the turn: the child restarted under a running run")
	}

	// The turn ends, so nothing is busy and the next sweep restarts the child.
	close(gate)
	waitPrompt(t, promptErr)
	h.fleet.SweepRestarts(ctx, time.Now().Add(restartLookahead))

	var back *attachment
	waitFor(t, func() error {
		got, err := tryAttach(h, id)
		if err != nil {
			return err
		}
		back = got
		return nil
	})
	defer back.close()
	if got := childRestart(t, h); got.Restart != nil {
		t.Fatalf("the child still holds a restart after the sweep: %+v", got.Restart)
	}
	waitForNote(t, ctx, back, restartNote)
}

// TestAnActiveWatchHoldsTheRestart is L2's watch half: a watching session
// holds the restart, and stopping the watch lets the next sweep take it.
func TestAnActiveWatchHoldsTheRestart(t *testing.T) {
	// A configured model, so the child's runtime is built with a watch
	// manager: the watch is what this test holds the restart on, and no turn
	// runs, so the fixture has no scripted reply to give.
	fixture := newProviderFixture(t, nil)
	fixture.useProvider(t)
	// The child writes its process ID when it starts (main_test.go), which is
	// how a restart is visible: a restarted child is a new process serving the
	// same project. The variable is read when the child is spawned, so it is
	// set before the harness starts one.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv(childPIDEnv, pidFile)
	bin := ownedBinary(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// The screen: a marshal client on a new session of the project child, as
	// the session the watch lives on.
	screen := h.attach("")
	sid := screen.conn.SessionID

	// The watch: a command that never succeeds, because its flag file never
	// exists, so the session is watching for as long as the test needs.
	flag := filepath.Join(h.root, "hold-flag")
	starter := attachDirectly(t, h, sid)
	watchID := starter.startWatch(t, map[string]any{
		"name":       "hold",
		"kind":       "command",
		"command":    "test -f '" + flag + "'",
		"condition":  "exit_code 0",
		"mode":       "once",
		"intervalMs": int64(60_000),
	})
	starter.close()
	screen.close()

	rebuild(t, bin, "rebuilt")
	h.fleet.SweepRestarts(ctx, time.Now().Add(restartLookahead))

	first := childPID(t, pidFile)
	held := childRestart(t, h)
	if held.Restart == nil {
		t.Fatal("the sweep restarted a child with a watch running")
	}
	if !strings.HasPrefix(held.Restart.WaitingFor, "watch ") {
		t.Fatalf("the restart waits for %q, want watch <name> in …", held.Restart.WaitingFor)
	}
	if got := childPID(t, pidFile); got != first {
		t.Fatalf("the child restarted under a running watch: process %d, was %d", got, first)
	}

	// The watch ends, so the next sweep restarts the child.
	stopper := attachDirectly(t, h, sid)
	stopper.stopWatch(t, watchID)
	stopper.close()

	h.fleet.SweepRestarts(ctx, time.Now().Add(restartLookahead))
	waitFor(t, func() error {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(string(data)); got == strconv.Itoa(first) {
			return fmt.Errorf("the child is still process %s", got)
		}
		return nil
	})
}

// TestABrokenBuildIsRefused is L3: a build that cannot start is refused
// before the child is touched, the project is told why, and the sessions keep
// answering.
func TestABrokenBuildIsRefused(t *testing.T) {
	bin := ownedBinary(t)
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	a := h.attach("")
	defer a.close()
	const note = "a line before the broken build"
	if err := a.session.AddNote(ctx, acpwire.Note{Text: note, Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("add the note: %v", err)
	}

	breakBuild(t, bin)
	h.fleet.SweepRestarts(ctx, time.Now().Add(restartLookahead))

	waitForNote(t, ctx, a, "A new marshal build could not start (broken build); this project stays on")
	child := childRestart(t, h)
	if child.Restart == nil {
		t.Fatal("the daemon reports no restart for the child that stayed on its build")
	}
	if got := child.Restart.Refused; got != "broken build" {
		t.Fatalf("the refusal = %q, want %q", got, "broken build")
	}

	// The child was never touched: the session still holds its transcript and
	// still answers a note.
	waitForNote(t, ctx, a, note)
	const after = "still attached after the refused build"
	if err := a.session.AddNote(ctx, acpwire.Note{Text: after, Role: acpwire.RoleUser}); err != nil {
		t.Fatalf("add a note to the untouched child: %v", err)
	}
	waitForNote(t, ctx, a, after)
}

// TestAUserConfigWriteMarksOnlyTheOtherProject is L4's bridge half: a user
// config write through one project's session leaves that project current —
// the child made the write — and marks the other project stale, which the
// daemon reports as a restart held for a quiet moment.
func TestAUserConfigWriteMarksOnlyTheOtherProject(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// A second project, initialized and registered, with a child of its own.
	second := filepath.Join(filepath.Dir(h.root), "second")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatalf("create the second project: %v", err)
	}
	initProject(t, second)
	if err := h.ws.AddProject(second); err != nil {
		t.Fatalf("add the second project: %v", err)
	}

	first := h.attach("")
	defer first.close()
	other, err := tryAttachAt(h, second, "")
	if err != nil {
		t.Fatalf("attach the second project: %v", err)
	}
	defer other.close()

	// The first project's session writes the user config, which both children
	// read: the writer's own child is not stale, the other one is.
	if err := first.host.SaveUserValue(ctx, "tools.shell.allow_network", true); err != nil {
		t.Fatalf("write the user config through the first project's session: %v", err)
	}

	// A sweep now restarts nothing: both children are viewed and were active
	// inside the quiet window.
	h.fleet.SweepRestarts(ctx, time.Now())

	if got := childRestart(t, h); got.Restart != nil {
		t.Fatalf("the project whose session wrote the config holds a restart: %+v", got.Restart)
	}
	held := childAt(t, h, second)
	if held.Restart == nil {
		t.Fatal("the other project holds no restart after the user config write")
	}
	if want := []string{"config.toml changed"}; !reflect.DeepEqual(held.Restart.Reasons, want) {
		t.Fatalf("the held restart's reasons = %v, want %v", held.Restart.Reasons, want)
	}
	if got := held.Restart.WaitingFor; got != "a quiet moment" {
		t.Fatalf("the held restart waits for %q, want %q", got, "a quiet moment")
	}
}

// TestAnEditedProjectConfigResumesUntrusted is L5: editing the project config
// after it was trusted makes the child stale, its restart starts a child that
// asks again, and the resumed session runs without the edited config.
func TestAnEditedProjectConfigResumesUntrusted(t *testing.T) {
	trustUserConfig(t)
	h := newDaemonHarness(t)
	writeTrustProjectConfig(t, h.root, "before-edit")
	ctx := callCtx(t)

	a := h.attach("")
	defer a.close()
	id := a.conn.SessionID
	if got := a.host.TrustPrompt(); !got.InPlace {
		t.Fatalf("the first session's prompt = %+v, want it asked in place", got)
	}
	if err := a.host.DecideTrust(ctx, h.root, acpwire.TrustPermanent); err != nil {
		t.Fatalf("trust/decide: %v", err)
	}
	waitForTrusted(t, a, "before-edit")

	// The edit is a change to a file the child recorded at start, so the sweep
	// restarts it — on a child that cannot apply a config the store's hash no
	// longer covers.
	writeTrustProjectConfig(t, h.root, "after-edit")
	h.fleet.SweepRestarts(ctx, time.Now().Add(restartLookahead))

	var back *attachment
	waitFor(t, func() error {
		got, err := tryAttach(h, id)
		if err != nil {
			return err
		}
		back = got
		return nil
	})
	defer back.close()

	if got := back.host.TrustPrompt(); !got.InPlace {
		t.Fatalf("the resumed session's prompt = %+v, want it asked in place", got)
	}
	if back.session.Status().Trusted {
		t.Fatal("the resumed session started trusted on a config the store does not cover")
	}
	if got := back.session.Config().Project.Name; got == "after-edit" {
		t.Fatalf("the resumed session applies the unapproved config: project name is %q", got)
	}
}
