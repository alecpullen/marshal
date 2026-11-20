package subagentwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/worktree"
)

// TestInspectChecksRealRepositoryOwnership pins the ownership recheck: a
// workspace that was replaced, re-pointed or pivoted is recorded as an ownership
// error, and its new contents are never adopted as this task's candidate.
func TestInspectChecksRealRepositoryOwnership(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		tamper func(t *testing.T, rec Record)
	}{
		{
			name: "replaced branch",
			tamper: func(t *testing.T, rec Record) {
				t.Helper()
				gitIn(t, rec.Binding.Root, "checkout", "-b", "hijacked")
			},
		},
		{
			name: "changed common directory",
			tamper: func(t *testing.T, rec Record) {
				t.Helper()
				// Re-point the checkout at another repository's git directory:
				// the path is unchanged, the repository is not.
				other := newGitFixture(t)
				gitdir := filepath.Join(other.dir, ".git")
				if err := os.WriteFile(filepath.Join(rec.Binding.Root, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600); err != nil {
					t.Fatalf("re-point checkout: %v", err)
				}
			},
		},
		{
			name: "symlink pivot",
			tamper: func(t *testing.T, rec Record) {
				t.Helper()
				// Replace the recorded path with a symlink to another checkout.
				other := newGitFixture(t)
				moved := rec.Binding.Root + "-moved"
				if err := os.Rename(rec.Binding.Root, moved); err != nil {
					t.Fatalf("move workspace aside: %v", err)
				}
				if err := os.Symlink(other.dir, rec.Binding.Root); err != nil {
					t.Fatalf("pivot workspace: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			store := NewMemoryStore()
			svc := NewService(store, worktree.CLIGitOps{})
			req := isolatedRequest(f.dir, f.dir)
			rec, err := svc.Prepare(ctx, req)
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			// Observe the healthy workspace first, so the tampered checkout has
			// a real candidate to be compared against.
			before, err := svc.Inspect(ctx, req.OwnerSessionID, req.TaskKey)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if before.SourceError != "" {
				t.Fatalf("inspection of a healthy workspace reported %q", before.SourceError)
			}
			if before.Candidate == "" {
				t.Fatal("inspection of a healthy workspace recorded no candidate")
			}

			tc.tamper(t, rec)

			after, err := svc.Inspect(ctx, req.OwnerSessionID, req.TaskKey)
			if err != nil {
				t.Fatalf("Inspect after tampering: %v", err)
			}
			if after.SourceError == "" {
				t.Fatal("Inspect accepted a workspace it no longer owns")
			}
			if !strings.Contains(after.SourceError, "identity") {
				t.Fatalf("ownership error = %q, want it to name the identity mismatch", after.SourceError)
			}
			if after.Dirty != nil {
				t.Fatalf("tampered workspace reported dirty = %v, want unknown", *after.Dirty)
			}
			// The tampered checkout is not adopted as this task's candidate.
			if after.Candidate != before.Candidate {
				t.Fatalf("candidate changed from %s to %s after the ownership failure", before.Candidate, after.Candidate)
			}
			if len(after.Commits) != 0 || len(after.ChangedPaths) != 0 {
				t.Fatalf("tampered workspace contributed evidence: %+v", after)
			}
			// The execution outcome is preserved: an ownership problem is not a
			// reason to rewrite what the run reported.
			if after.Outcome != before.Outcome {
				t.Fatalf("outcome changed from %q to %q", before.Outcome, after.Outcome)
			}
			stored, err := store.Get(ctx, req.OwnerSessionID, req.TaskKey)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if stored.SourceError == "" || stored.Candidate != before.Candidate {
				t.Fatalf("stored record = %+v, want the recorded ownership error", stored)
			}
		})
	}
}

// TestCompletionKeepsPartialEvidence pins completion: the workspace's real
// candidate, commits, changed paths and dirty state are observed, and the
// reported outcome is never promoted to success.
func TestCompletionKeepsPartialEvidence(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	store := NewMemoryStore()
	svc := NewService(store, worktree.CLIGitOps{})
	req := isolatedRequest(f.dir, f.dir)
	rec, err := svc.Prepare(ctx, req)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// The child commits one file and leaves another change uncommitted.
	if got := rec.CleanupState; got != CleanupRetained {
		t.Fatalf("cleanup state = %q, want %q", got, CleanupRetained)
	}
	child := rec.Binding.Root
	writeIn(t, child, "work.go", "package work\n")
	gitIn(t, child, "add", "-A")
	gitIn(t, child, "commit", "-m", "child work")
	writeIn(t, child, "base.txt", "uncommitted child edit\n")
	childHead := gitIn(t, child, "rev-parse", "HEAD")

	exitCode := 0
	completed, err := svc.Complete(ctx, req.OwnerSessionID, req.TaskKey, Completion{
		Outcome:         OutcomeFailed,
		ReportMessageID: 42,
		Verification:    []Verification{{Command: "go test ./...", Outcome: "failed", ExitCode: &exitCode}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// The outcome is exactly the reported one: commits do not make a failed run
	// successful.
	if completed.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q, want the reported %q", completed.Outcome, OutcomeFailed)
	}
	if completed.Candidate != childHead {
		t.Fatalf("candidate = %s, want the observed child HEAD %s", completed.Candidate, childHead)
	}
	if len(completed.Commits) != 1 || completed.Commits[0] != childHead {
		t.Fatalf("commits = %v, want only %s", completed.Commits, childHead)
	}
	if len(completed.ChangedPaths) != 1 || completed.ChangedPaths[0] != "work.go" {
		t.Fatalf("changed paths = %v, want only work.go", completed.ChangedPaths)
	}
	if completed.Dirty == nil || !*completed.Dirty {
		t.Fatalf("dirty = %v, want the observed uncommitted change", completed.Dirty)
	}
	if completed.ReportMessageID != 42 {
		t.Fatalf("report message id = %d, want 42", completed.ReportMessageID)
	}
	if len(completed.Verification) != 1 || completed.Verification[0].Command != "go test ./..." {
		t.Fatalf("verification = %+v, want the reported command", completed.Verification)
	}
	if completed.CleanupState != CleanupRetained {
		t.Fatalf("cleanup state = %q, want the retained candidate", completed.CleanupState)
	}

	// Stopped and unknown outcomes stay as reported, and keep the evidence.
	for _, outcome := range []string{OutcomeStopped, OutcomeUnknown} {
		got, err := svc.Complete(ctx, req.OwnerSessionID, req.TaskKey, Completion{Outcome: outcome})
		if err != nil {
			t.Fatalf("Complete(%s): %v", outcome, err)
		}
		if got.Outcome != outcome {
			t.Fatalf("outcome %q was promoted to %q", outcome, got.Outcome)
		}
		if got.Candidate != childHead || len(got.Commits) != 1 {
			t.Fatalf("Complete(%s) lost the observed evidence: %+v", outcome, got)
		}
		if got.ReportMessageID != 42 {
			t.Fatalf("Complete(%s) cleared the report message id", outcome)
		}
	}

	// An outcome outside the recorded vocabulary is refused rather than stored.
	if _, err := svc.Complete(ctx, req.OwnerSessionID, req.TaskKey, Completion{Outcome: "succeeded"}); err == nil {
		t.Fatal("Complete accepted an outcome outside the recorded vocabulary")
	}
	// Another owner cannot complete this record.
	if _, err := svc.Complete(ctx, "owner-2", req.TaskKey, Completion{Outcome: OutcomeDone}); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("cross-owner Complete error = %v, want ErrRecordNotFound", err)
	}
}
