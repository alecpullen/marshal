package snapshot

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestBoundFixesTheOperationRoot pins the whole point of Bound: a view created
// for one root keeps acting on it, whatever the receiver's active root does
// afterwards.
func TestBoundFixesTheOperationRoot(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	child := t.TempDir()

	// The receiver follows a mutable root, the way a parent session follows its
	// own workspace rebinds.
	mutable := project
	r := NewRooted(dataDir, project, func() string { return mutable }, 1<<20, nil, slog.Default())
	bound := r.Bound(child)
	if bound == nil {
		t.Fatal("Bound returned nil")
	}

	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(child, "f.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hash, err := bound.Track(ctx)
	if err != nil {
		t.Fatalf("bound Track: %v", err)
	}

	// Move the receiver to a third root. The bound view must not follow it.
	mutable = t.TempDir()
	if err := os.WriteFile(filepath.Join(child, "f.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := bound.Restore(ctx, hash); err != nil {
		t.Fatalf("bound Restore: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(child, "f.txt")); string(data) != "v1" {
		t.Fatalf("bound root f.txt = %q, want v1 (the bound view must not follow the receiver)", data)
	}

	// And the receiver's own view is somewhere else entirely.
	if r.svc().Workspace() == bound.svc().Workspace() {
		t.Fatal("the bound view and the receiver resolved the same workspace")
	}
}

// TestBoundCopiesStoragePolicy pins that a bound view inherits the storage
// policy the receiver was configured with rather than falling back to the
// production defaults behind the caller's back.
func TestBoundCopiesStoragePolicy(t *testing.T) {
	dataDir := t.TempDir()
	project := t.TempDir()
	child := t.TempDir()

	limits := Limits{WorkspaceMaxBytes: 4096}
	globalCalls := 0
	global := func() (Limits, error) {
		globalCalls++
		return Limits{GlobalMaxBytes: 8192}, nil
	}
	r := NewRooted(dataDir, project, func() string { return project }, 1<<20, []string{"vendor"},
		slog.Default(), WithRootedLimits(limits), WithRootedGlobalLimits(global), WithRootedGitBinary("/bin/true"))

	bound := r.Bound(child)
	if bound.dataDir != dataDir {
		t.Errorf("bound dataDir = %q, want the receiver's %q", bound.dataDir, dataDir)
	}
	if bound.projectRoot != project {
		t.Errorf("bound projectRoot = %q, want the receiver's %q", bound.projectRoot, project)
	}
	if bound.maxFile != 1<<20 {
		t.Errorf("bound maxFile = %d, want the receiver's", bound.maxFile)
	}
	if len(bound.ignore) != 1 || bound.ignore[0] != "vendor" {
		t.Errorf("bound ignore = %v, want the receiver's list", bound.ignore)
	}
	if !bound.limitsSet || bound.limits.WorkspaceMaxBytes != limits.WorkspaceMaxBytes {
		t.Errorf("bound limits = %+v (set=%v), want the receiver's %+v", bound.limits, bound.limitsSet, limits)
	}
	if bound.globalLimits == nil {
		t.Fatal("bound view lost the receiver's global-limit reader")
	}
	if _, err := bound.globalLimits(); err != nil || globalCalls != 1 {
		t.Errorf("bound global-limit reader was not the receiver's (calls=%d, err=%v)", globalCalls, err)
	}
	if bound.gitBinary != "/bin/true" {
		t.Errorf("bound gitBinary = %q, want the receiver's override", bound.gitBinary)
	}
	// The per-root service cache is its own, so the two views can never hand
	// each other a service for the wrong root.
	if bound.services != nil {
		t.Error("bound view must start with no shared service cache")
	}
}

// TestBoundOfNilIsNil keeps the nil receiver honest for a caller that binds a
// snapshotter it never had.
func TestBoundOfNilIsNil(t *testing.T) {
	var r *Rooted
	if got := r.Bound("/tmp/whatever"); got != nil {
		t.Fatal("Bound on a nil rooted must be nil")
	}
}
