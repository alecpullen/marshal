package worktree

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// targetLockHelperEnv names the environment variable that turns this test
// binary into the lock-holding subprocess the cross-process tests need. No
// ambient process is started: every child is this binary, run by a test, with
// an explicit repository and root.
const targetLockHelperEnv = "MARSHAL_TARGETLOCK_HELPER"

// TestTargetLockHelperProcess is the subprocess half of the cross-process
// tests. It is not a test of its own: without the environment variable it
// skips, so it never runs in an ordinary suite.
//
// Modes:
//
//	hold-use  acquire a shared use lease, announce it, wait for a line on
//	          stdin, release it terminally and exit.
//	crash     acquire a shared use lease, announce it, and exit WITHOUT
//	          releasing it — the owner died before terminal evidence.
//	try-lock  try to take the exclusive target lock and report the outcome for
//	          MARSHAL_TARGETLOCK_ROOT and MARSHAL_TARGETLOCK_OTHER_ROOT.
func TestTargetLockHelperProcess(t *testing.T) {
	mode := os.Getenv(targetLockHelperEnv)
	if mode == "" {
		t.Skip("not the target-lock helper process")
	}
	repository := os.Getenv("MARSHAL_TARGETLOCK_REPOSITORY")
	root := os.Getenv("MARSHAL_TARGETLOCK_ROOT")
	if repository == "" || root == "" {
		fmt.Println("error: repository and root are required")
		os.Exit(2)
	}
	switch mode {
	case "hold-use":
		lease, err := AcquireWorkspaceUse(context.Background(), repository, root, UseIdentity{
			OwnerSessionID: "helper-session",
			ExecutionID:    "helper-execution",
			Kind:           UseKindCommand,
		})
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(2)
		}
		if err := lease.MarkStarted(os.Getpid(), "helper"); err != nil {
			fmt.Println("error:", err)
			os.Exit(2)
		}
		fmt.Println("held")
		reader := bufio.NewReader(os.Stdin)
		_, _ = reader.ReadString('\n')
		if err := lease.Release(true); err != nil {
			fmt.Println("error:", err)
			os.Exit(2)
		}
		fmt.Println("released")
		os.Exit(0)
	case "crash":
		lease, err := AcquireWorkspaceUse(context.Background(), repository, root, UseIdentity{
			OwnerSessionID: "helper-session",
			ExecutionID:    "helper-execution",
			Kind:           UseKindCommand,
		})
		if err != nil {
			fmt.Println("error:", err)
			os.Exit(2)
		}
		if err := lease.MarkStarted(os.Getpid(), "helper"); err != nil {
			fmt.Println("error:", err)
			os.Exit(2)
		}
		fmt.Println("held")
		// Deliberately no Release: the owner dies before terminal evidence.
		os.Exit(0)
	case "try-lock":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		release, err := AcquireTargetLock(ctx, repository, root)
		if err != nil {
			fmt.Println("busy")
		} else {
			fmt.Println("locked")
			release()
		}
		if other := os.Getenv("MARSHAL_TARGETLOCK_OTHER_ROOT"); other != "" {
			if rel, oerr := AcquireTargetLock(ctx, repository, other); oerr != nil {
				fmt.Println("other=busy")
			} else {
				rel()
				fmt.Println("other=locked")
			}
		}
		os.Exit(0)
	default:
		fmt.Println("error: unknown helper mode", mode)
		os.Exit(2)
	}
}

// targetLockProc is one lock-holding subprocess: its process, the stdin pipe
// the test uses to ask for a terminal release, and the lines it announced.
type targetLockProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

// targetLockHelper starts this test binary as a lock-holding subprocess with an
// explicit repository and root. Every wait on its output is a bounded barrier:
// the child announces each step, and a child that never does fails the test
// instead of hanging the suite.
func targetLockHelper(t *testing.T, mode, repository, root string, extraEnv ...string) *targetLockProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestTargetLockHelperProcess")
	cmd.Env = append(os.Environ(),
		targetLockHelperEnv+"="+mode,
		"MARSHAL_TARGETLOCK_REPOSITORY="+repository,
		"MARSHAL_TARGETLOCK_ROOT="+root,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("helper stdout: %v", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	proc := &targetLockProc{cmd: cmd, stdin: stdin, lines: make(chan string, 8)}
	scanner := bufio.NewScanner(stdout)
	go func() {
		defer close(proc.lines)
		for scanner.Scan() {
			proc.lines <- scanner.Text()
		}
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return proc
}

// next reads the child's next announcement.
func (p *targetLockProc) next(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			t.Fatal("helper process exited before reporting a result")
		}
		return line
	case <-time.After(30 * time.Second):
		t.Fatal("helper process did not report a result")
		return ""
	}
}

// release asks the holder to release terminally.
func (p *targetLockProc) release(t *testing.T) {
	t.Helper()
	if _, err := p.stdin.Write([]byte("\n")); err != nil {
		t.Fatalf("ask helper to release: %v", err)
	}
}

func (p *targetLockProc) wait(t *testing.T) {
	t.Helper()
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
}

func TestTargetLockCoordinatesProcessesAndAliases(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(repository, "checkout")
	other := filepath.Join(repository, "other")
	for _, dir := range []string{root, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// An alias of the same target: a redundant ".." segment, and — where the
	// platform supports it — a symlink to the root. Both must resolve to the
	// one canonical target the first process holds.
	alias := filepath.Join(root, "sub", "..")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(repository, "alias-link")); err == nil {
		alias = filepath.Join(repository, "alias-link")
	}

	holder := targetLockHelper(t, "hold-use", repository, root)
	if line := holder.next(t); line != "held" {
		t.Fatalf("helper reported %q, want held", line)
	}

	// A second process using a canonical alias of the same target cannot take
	// lifecycle exclusivity for as long as the first holds it.
	second := targetLockHelper(t, "try-lock", repository, alias,
		"MARSHAL_TARGETLOCK_OTHER_ROOT="+other)
	if line := second.next(t); line != "busy" {
		t.Fatalf("second process reported %q for the aliased target, want busy", line)
	}
	// An unrelated target is free: the alias resolution must not serialise the
	// whole repository.
	if line := second.next(t); line != "other=locked" {
		t.Fatalf("second process reported %q for an unrelated target, want other=locked", line)
	}

	// Once the holder reports terminal release, the target becomes available.
	holder.release(t)
	if line := holder.next(t); line != "released" {
		t.Fatalf("holder reported %q, want released", line)
	}
	holder.wait(t)

	third := targetLockHelper(t, "try-lock", repository, alias)
	if line := third.next(t); line != "locked" {
		t.Fatalf("after terminal release the alias reported %q, want locked", line)
	}
}

func TestUnknownWorkspaceUseIsNotIdle(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(repository, "checkout")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := resolveTarget(repository, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.prepare(); err != nil {
		t.Fatal(err)
	}

	// A lock directory that exists with no lock file and no note is evidence of
	// nothing: creating the directory is not liveness evidence, so the target is
	// idle.
	release, err := AcquireTargetLock(context.Background(), repository, root)
	if err != nil {
		t.Fatalf("an empty lock directory must not block: %v", err)
	}
	release()

	// Unresolved retained execution evidence — a note this build cannot
	// interpret — prevents cleanup.
	unknown := filepath.Join(paths.dir, "use-unknown-version.json")
	if err := os.WriteFile(unknown, []byte(`{"version":99,"root":"`+root+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireTargetLock(context.Background(), repository, root); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("unknown note: got %v, want ErrTargetBusy", err)
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}

	// A lease released without terminal settlement is retained as unknown use
	// by this process, and blocks exactly the same way.
	lease, err := AcquireWorkspaceUse(context.Background(), repository, root, UseIdentity{Kind: UseKindTool})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(false); err != nil {
		t.Fatalf("non-terminal release: %v", err)
	}
	notes, err := readUseNotes(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !notes[0].Unknown {
		t.Fatalf("notes after non-terminal release = %+v, want one unknown note", notes)
	}
	if _, err := AcquireTargetLock(context.Background(), repository, root); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("abandoned use: got %v, want ErrTargetBusy", err)
	}
}

func TestIdleLeaseOwnerCrashRetainsUnknownCommand(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(repository, "checkout")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	crasher := targetLockHelper(t, "crash", repository, root)
	if line := crasher.next(t); line != "held" {
		t.Fatalf("helper reported %q, want held", line)
	}
	crasher.wait(t)

	// The owner is dead, so its descriptor is gone — but the note it published
	// is evidence of an end it never reported. A new runtime refuses cleanup
	// instead of treating the unlocked file as idle.
	paths, err := resolveTarget(repository, root)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := readUseNotes(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("notes after owner crash = %d, want 1", len(notes))
	}
	if !notes[0].Started {
		t.Fatal("the note should retain the process-start evidence the owner published")
	}
	if _, err := AcquireTargetLock(context.Background(), repository, root); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("after owner crash: got %v, want ErrTargetBusy", err)
	}
	if !strings.Contains(notes[0].describe(), "helper-execution") {
		t.Fatalf("note should name the command identity, got %q", notes[0].describe())
	}
}

func TestIdleLeaseOtherProcessCommand(t *testing.T) {
	repository := t.TempDir()
	root := filepath.Join(repository, "checkout")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	holder := targetLockHelper(t, "hold-use", repository, root)
	if line := holder.next(t); line != "held" {
		t.Fatalf("helper reported %q, want held", line)
	}
	// A command claimed by another process blocks lifecycle exclusivity here.
	if _, err := AcquireTargetLock(context.Background(), repository, root); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("while another process holds a command: got %v, want ErrTargetBusy", err)
	}
	// Observed terminal release frees it.
	holder.release(t)
	if line := holder.next(t); line != "released" {
		t.Fatalf("holder reported %q, want released", line)
	}
	holder.wait(t)
	release, err := AcquireTargetLock(context.Background(), repository, root)
	if err != nil {
		t.Fatalf("after observed terminal release: %v", err)
	}
	release()
}
