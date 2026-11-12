package daemon_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/db"
)

// TestAPinnedProjectIndexesWithNoSession is W12's proof: a pin alone — no
// attach, no session — starts the project's child, holds the project, and the
// hold opens the host, runs its startup scan and turns on its watcher.
func TestAPinnedProjectIndexesWithNoSession(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	// A source file before the pin, so the startup scan the hold triggers has
	// something to find.
	const first = "main.go"
	createMarshalDir(t, h.root)
	writeSource(t, h.root, first)

	// No attach came before this: the child the pin starts is the only thing
	// that can answer project/hold, and a hold that fails fails the pin.
	state, err := h.client.Pin(ctx, h.root, true)
	if err != nil {
		t.Fatalf("pin %s: %v", h.root, err)
	}
	if !state.Pinned || !state.Running {
		t.Fatalf("the pin answered %+v, want pinned and running", state)
	}

	// The daemon reports the pin, and the one child serving it. Nothing has
	// attached, so that child holds no session.
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		if !listsRoot(info.Pinned, h.root) {
			return fmt.Errorf("pinned does not list %q: %v", h.root, info.Pinned)
		}
		if len(info.Children) != 1 {
			return fmt.Errorf("the daemon lists %d children, want 1", len(info.Children))
		}
		child := info.Children[0]
		if child.Root != h.root {
			return fmt.Errorf("the child serves %q, want %q", child.Root, h.root)
		}
		if !child.Pinned {
			return fmt.Errorf("the child for %q is not marked pinned", child.Root)
		}
		if len(child.Sessions) != 0 {
			return fmt.Errorf("the child tracks %d sessions, want none", len(child.Sessions))
		}
		return nil
	})

	// No session ever started, so the file index is the hold's doing: it
	// opened the host and ran the startup scan.
	waitForFileIndex(t, h.root, first)

	// The project sets no indexing.watch, so the second file can only be
	// indexed by the watcher the hold turned on.
	const second = "later.go"
	writeSource(t, h.root, second)
	waitForFileIndex(t, h.root, second)
}

// TestUnpinningKeepsTheChildForItsSessions pins a project, attaches a client
// and unpins: the pin goes, the child stays for the session it serves.
func TestUnpinningKeepsTheChildForItsSessions(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	createMarshalDir(t, h.root)
	if state, err := h.client.Pin(ctx, h.root, true); err != nil {
		t.Fatalf("pin %s: %v", h.root, err)
	} else if !state.Pinned || !state.Running {
		t.Fatalf("the pin answered %+v, want pinned and running", state)
	}

	a := h.attach("")
	defer a.close()
	id := a.conn.SessionID

	state, err := h.client.Pin(ctx, h.root, false)
	if err != nil {
		t.Fatalf("unpin %s: %v", h.root, err)
	}
	if state.Pinned {
		t.Fatalf("the unpin answered %+v, want not pinned", state)
	}
	if !state.Running {
		t.Fatalf("the unpin answered %+v: the child serving a session stopped", state)
	}

	// The child stays, it is no longer pinned, and the session is still on it.
	waitFor(t, func() error {
		info, err := h.client.Info(ctx)
		if err != nil {
			return err
		}
		if listsRoot(info.Pinned, h.root) {
			return fmt.Errorf("pinned still lists %q: %v", h.root, info.Pinned)
		}
		if len(info.Children) != 1 {
			return fmt.Errorf("the daemon lists %d children, want 1", len(info.Children))
		}
		child := info.Children[0]
		if child.Root != h.root {
			return fmt.Errorf("the child serves %q, want %q", child.Root, h.root)
		}
		if child.Pinned {
			return fmt.Errorf("the child for %q is still marked pinned", child.Root)
		}
		for _, s := range child.Sessions {
			if s.ID == id {
				return nil
			}
		}
		return fmt.Errorf("session %s is not listed on the child", id)
	})

	// The session still answers: a live call the child has to serve, on top of
	// the status attach installed.
	if got := a.session.Status().WorkingDir; got != h.root {
		t.Fatalf("the session's working directory is %q, want %q", got, h.root)
	}
	if _, err := a.session.Messages(ctx, acpwire.MessageQuery{}); err != nil {
		t.Fatalf("messages after the unpin: %v", err)
	}
}

// TestAPinNeedsAnExistingDirectory pins the refusal a pin of a missing
// directory gets, and that it records nothing.
func TestAPinNeedsAnExistingDirectory(t *testing.T) {
	h := newDaemonHarness(t)
	ctx := callCtx(t)

	missing := filepath.Join(filepath.Dir(h.root), "not-a-project")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("%s is supposed to be missing: %v", missing, err)
	}

	_, err := h.client.Pin(ctx, missing, true)
	if err == nil {
		t.Fatalf("pinning %s succeeded", missing)
	}
	if !strings.Contains(err.Error(), "root must be an existing directory") {
		t.Fatalf("the refusal reads %q, want the daemon's sentence", err)
	}

	info, err := h.client.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if len(info.Pinned) != 0 {
		t.Fatalf("a refused pin left %v pinned", info.Pinned)
	}
}

// createMarshalDir makes root look like a project a session has opened: the
// host keeps its database under <root>/.marshal, and the runtime creates that
// directory when a session starts. A project nothing has opened yet has none,
// and a hold — which opens the host itself — needs one to exist.
func createMarshalDir(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatalf("create .marshal: %v", err)
	}
}

// writeSource writes a Go source file called name into root.
func writeSource(t *testing.T, root, name string) {
	t.Helper()
	src := "package main\n\nfunc " + strings.TrimSuffix(name, ".go") + "() {}\n"
	if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// listsRoot reports whether roots holds root.
func listsRoot(roots []string, root string) bool {
	for _, r := range roots {
		if r == root {
			return true
		}
	}
	return false
}

// waitForFileIndex polls the project's own database until its file index holds
// rel, or sixty seconds pass. The hold that pins the project is what creates
// the database, so an open before then simply fails and is polled again.
func waitForFileIndex(t *testing.T, root, rel string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		holds, err := fileIndexHolds(root, rel)
		if err != nil {
			last = err
		} else if holds {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the project's file index never held %q: %v", rel, last)
}

// fileIndexHolds reports whether the project database at root has a project
// row whose file index holds rel.
func fileIndexHolds(root, rel string) (bool, error) {
	database, err := db.Open(db.Path(root))
	if err != nil {
		return false, fmt.Errorf("open the project database: %w", err)
	}
	defer database.Close()

	project, err := database.GetProjectByRoot(root)
	if err != nil {
		return false, err
	}
	files, err := database.GetFileIndex(project.ID, 0)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if f.Path == rel {
			return true, nil
		}
	}
	return false, fmt.Errorf("%q is not in the file index (%d files)", rel, len(files))
}
