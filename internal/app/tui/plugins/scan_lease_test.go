package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// scanTempParent creates a temporary parent laid out the way a scan publishes
// it: <parent>/clone, so the lease's ownership covers the whole parent.
func scanTempParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "scan")
	if err := os.MkdirAll(filepath.Join(parent, "clone"), 0o755); err != nil {
		t.Fatalf("create scan parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parent, "clone", "hooks.toml"), []byte("# x\n"), 0o644); err != nil {
		t.Fatalf("write clone content: %v", err)
	}
	return parent
}

func mustBeDeleted(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be deleted, Stat error = %v", path, err)
	}
}

func mustSurvive(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to survive: %v", path, err)
	}
}

// TestScanLeaseClosePublishClaimRaces pins the lease's ownership rules against
// every order the panel can produce: a close that lands before the worker
// publishes, a close after it published, a claim followed by a close, and a
// worker that fails after publishing. Whatever the order, exactly one owner
// ends up responsible for the directory and nothing is leaked.
func TestScanLeaseClosePublishClaimRaces(t *testing.T) {
	t.Run("close cancels before the worker publishes", func(t *testing.T) {
		cancelled := make(chan struct{})
		l := newScanLease(func() { close(cancelled) })
		l.Close()
		select {
		case <-cancelled:
		default:
			t.Fatal("close did not cancel the scan")
		}

		parent := scanTempParent(t)
		if l.Publish(parent) {
			t.Fatal("a closed lease accepted a directory it will never clean up")
		}
		mustBeDeleted(t, parent)

		l.Close() // repeated close is inert
	})

	t.Run("publish then close deletes the unclaimed parent", func(t *testing.T) {
		l := newScanLease(nil)
		parent := scanTempParent(t)
		if !l.Publish(parent) {
			t.Fatal("an open lease refused a directory")
		}
		l.Close()
		l.Close()
		mustBeDeleted(t, parent)
	})

	t.Run("claim before close keeps the parent", func(t *testing.T) {
		l := newScanLease(nil)
		parent := scanTempParent(t)
		if !l.Publish(parent) {
			t.Fatal("an open lease refused a directory")
		}
		got, ok := l.Claim()
		if !ok || got != parent {
			t.Fatalf("Claim() = %q, %v; want %q, true", got, ok, parent)
		}
		if _, second := l.Claim(); second {
			t.Fatal("a lease was claimed twice")
		}
		l.Close()
		// The claimant owns the directory now: closing the lease must not
		// delete content it no longer controls.
		mustSurvive(t, parent)
	})

	t.Run("failed scan publishes then closes", func(t *testing.T) {
		l := newScanLease(nil)
		parent := scanTempParent(t)
		if !l.Publish(parent) {
			t.Fatal("an open lease refused a directory")
		}
		l.Close() // the worker's error path
		mustBeDeleted(t, parent)
	})

	t.Run("claim of an unpublished lease fails", func(t *testing.T) {
		l := newScanLease(nil)
		defer l.Close()
		if dir, ok := l.Claim(); ok {
			t.Fatalf("Claim() on an unpublished lease returned %q, true", dir)
		}
	})

	// A publish that races a close from another goroutine is sequenced through
	// channels, and both legal orders must end with the directory gone.
	t.Run("publish racing a close leaks nothing", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			l := newScanLease(nil)
			parent := scanTempParent(t)
			published := make(chan bool, 1)
			start := make(chan struct{})
			go func() {
				<-start
				published <- l.Publish(parent)
			}()
			close(start)
			l.Close()
			<-published
			mustBeDeleted(t, parent)
		}
	})

	// A claim that races a close must be consistent: a successful claim keeps
	// the directory, a failed one means the close deleted it.
	t.Run("claim racing a close stays consistent", func(t *testing.T) {
		for i := 0; i < 20; i++ {
			l := newScanLease(nil)
			parent := scanTempParent(t)
			if !l.Publish(parent) {
				t.Fatal("an open lease refused a directory")
			}
			done := make(chan struct{})
			var (
				claimed bool
				dir     string
			)
			go func() {
				dir, claimed = l.Claim()
				close(done)
			}()
			l.Close()
			<-done
			if claimed {
				if dir != parent {
					t.Fatalf("Claim() = %q, want %q", dir, parent)
				}
				mustSurvive(t, parent)
				os.RemoveAll(parent)
				continue
			}
			mustBeDeleted(t, parent)
		}
	})
}
