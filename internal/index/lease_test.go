package index

import (
	"os"
	"path/filepath"
	"testing"
)

func leaseRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".marshal"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTryLeaseExcludesASecondHolderUntilReleased(t *testing.T) {
	root := leaseRoot(t)
	release, held, err := TryLease(root)
	if err != nil || !held {
		t.Fatalf("first TryLease = held %v, err %v; want held", held, err)
	}

	// flock belongs to the open file description, so a second open in this
	// process stands in for a second process.
	again, held, err := TryLease(root)
	if err != nil || held || again != nil {
		t.Fatalf("second TryLease while held = held %v, release %v, err %v; want not held, no error", held, again != nil, err)
	}

	release()
	release() // a second release is a no-op

	after, held, err := TryLease(root)
	if err != nil || !held {
		t.Fatalf("TryLease after release = held %v, err %v; want held", held, err)
	}
	after()
}

func TestTryLeaseReportsAnUnopenableLockFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	release, held, err := TryLease(root)
	if err == nil || held || release != nil {
		t.Fatalf("TryLease(missing root) = held %v, release %v, err %v; want an error", held, release != nil, err)
	}
}
