package acp

import (
	"context"
	"encoding/json"
	"testing"
)

// fakeExitGit records calls and returns scripted results.
type fakeExitGit struct {
	dirty     bool
	dirtyErr  error
	commitSHA string
	commitErr error
	committed string // message passed to CommitAll
}

func (f *fakeExitGit) IsDirty(dir string) (bool, error) { return f.dirty, f.dirtyErr }
func (f *fakeExitGit) CommitAll(dir, message string) (string, error) {
	f.committed = message
	return f.commitSHA, f.commitErr
}

func testExitManager(t *testing.T, git *fakeExitGit) *ExitManager {
	t.Helper()
	return NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) {
			return &ExitRuntime{Dir: "/work"}, true
		},
		Git: git,
	})
}

func TestCommitCommitsADirtyTree(t *testing.T) {
	git := &fakeExitGit{dirty: true, commitSHA: "abc123"}
	m := testExitManager(t, git)

	raw, err := m.Commit(context.Background(),
		json.RawMessage(`{"sessionId":"s1","message":"do the thing"}`))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	res := raw.(CommitResult)
	if res.Commit != "abc123" || res.Clean {
		t.Fatalf("got %+v, want commit abc123 and Clean=false", res)
	}
	if git.committed != "do the thing" {
		t.Fatalf("commit message = %q", git.committed)
	}
}

func TestCommitOnACleanTreeIsANoOp(t *testing.T) {
	git := &fakeExitGit{dirty: false}
	m := testExitManager(t, git)

	raw, err := m.Commit(context.Background(),
		json.RawMessage(`{"sessionId":"s1","message":"x"}`))
	if err != nil {
		t.Fatalf("Commit on a clean tree returned an error: %v", err)
	}
	res := raw.(CommitResult)
	if !res.Clean {
		t.Fatal("Clean = false on an already-committed tree")
	}
	if git.committed != "" {
		t.Fatal("CommitAll was called on a clean tree")
	}
}

func TestCommitRejectsAnEmptyMessage(t *testing.T) {
	m := testExitManager(t, &fakeExitGit{dirty: true})
	if _, err := m.Commit(context.Background(),
		json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("Commit accepted an empty message")
	}
}

func TestCommitRejectsAnUnknownSession(t *testing.T) {
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return nil, false },
		Git:    &fakeExitGit{},
	})
	if _, err := m.Commit(context.Background(),
		json.RawMessage(`{"sessionId":"nope","message":"x"}`)); err == nil {
		t.Fatal("Commit accepted an unknown session")
	}
}
