package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"marshal/internal/pipeline"
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
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Git:    &fakeExitGit{dirty: true},
	})
	if _, err := m.Commit(context.Background(),
		json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("Commit with no message and no drafter should fail")
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

func TestVerifyReportsFailureWithOutput(t *testing.T) {
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Verify: func(context.Context, string) (pipeline.VerifyResult, error) {
			return pipeline.VerifyResult{OK: false, FailedCommand: "go test ./...",
				Output: "FAIL marshal/internal/foo"}, nil
		},
	})
	raw, err := m.Verify(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	res := raw.(VerifyReply)
	if res.OK || res.FailedCommand != "go test ./..." {
		t.Fatalf("got %+v", res)
	}
	if res.Output == "" {
		t.Fatal("Output is empty; a gate failure must be actionable without a second fetch")
	}
}

func TestVerifyReportsSkippedSeparatelyFromPass(t *testing.T) {
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Verify: func(context.Context, string) (pipeline.VerifyResult, error) {
			return pipeline.VerifyResult{Skipped: true}, nil
		},
	})
	raw, _ := m.Verify(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	res := raw.(VerifyReply)
	if !res.Skipped {
		t.Fatal("Skipped was not reported")
	}
	if res.OK {
		t.Fatal("a skipped gate must not report OK; it blocks and needs an override")
	}
}

func TestVerifyBoundsOutput(t *testing.T) {
	huge := strings.Repeat("x", 512*1024)
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Verify: func(context.Context, string) (pipeline.VerifyResult, error) {
			return pipeline.VerifyResult{OK: false, Output: huge}, nil
		},
	})
	raw, _ := m.Verify(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	res := raw.(VerifyReply)
	if len(res.Output) >= len(huge) {
		t.Fatalf("output was not bounded (%d bytes); a failing suite can emit megabytes", len(res.Output))
	}
}

func TestCommitDraftsAMessageWhenNoneIsGiven(t *testing.T) {
	git := &fakeExitGit{dirty: true, commitSHA: "abc123"}
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Git:    git,
		DraftMessage: func(context.Context, *ExitRuntime) (string, error) {
			return "Add the --listen flag", nil
		},
	})

	raw, err := m.Commit(context.Background(), json.RawMessage(`{"sessionId":"s1"}`))
	if err != nil {
		t.Fatalf("Commit with no message: %v", err)
	}
	res := raw.(CommitResult)
	if git.committed != "Add the --listen flag" {
		t.Fatalf("committed %q, want the drafted message", git.committed)
	}
	if res.Message != "Add the --listen flag" {
		t.Fatal("the drafted message was not reported back; the operator would never see it")
	}
}

func TestCommitStillPrefersAnExplicitMessage(t *testing.T) {
	git := &fakeExitGit{dirty: true, commitSHA: "abc"}
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Git:    git,
		DraftMessage: func(context.Context, *ExitRuntime) (string, error) {
			return "drafted", nil
		},
	})
	if _, err := m.Commit(context.Background(),
		json.RawMessage(`{"sessionId":"s1","message":"explicit"}`)); err != nil {
		t.Fatal(err)
	}
	if git.committed != "explicit" {
		t.Fatalf("committed %q, want the operator's message", git.committed)
	}
}

func TestCommitFailsClearlyWhenDraftingFails(t *testing.T) {
	m := NewExitManager(ExitManagerConfig{
		Lookup: func(string) (*ExitRuntime, bool) { return &ExitRuntime{Dir: "/work"}, true },
		Git:    &fakeExitGit{dirty: true},
		DraftMessage: func(context.Context, *ExitRuntime) (string, error) {
			return "", errors.New("model unavailable")
		},
	})
	if _, err := m.Commit(context.Background(), json.RawMessage(`{"sessionId":"s1"}`)); err == nil {
		t.Fatal("a failed draft produced a commit with no message")
	}
}
