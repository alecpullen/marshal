package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/lsp"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/worktree"
)

// flakyRepositoryGit fails its first repository lookup, then answers, which is
// how a transient git failure during a lease is modelled.
type flakyRepositoryGit struct {
	worktree.CLIGitOps
	mu    sync.Mutex
	calls int
}

func (g *flakyRepositoryGit) RepositoryID(string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.calls == 1 {
		return "", errors.New("transient git failure")
	}
	return "/repo/.git", nil
}

func (g *flakyRepositoryGit) lookups() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// TestIdleGateRetriesAFailedRepositoryLookup pins that a FAILED identity lookup
// is not cached as an identity. Caching "" would claim the root is not a git
// checkout for the rest of the process, and the mutation path consults this
// cache before the fresh identity checks, so a single transient failure would
// silently drop the exclusive cross-process freeze on that root.
func TestIdleGateRetriesAFailedRepositoryLookup(t *testing.T) {
	previous := repositoryMissTTL
	repositoryMissTTL = 0
	t.Cleanup(func() { repositoryMissTTL = previous })

	git := &flakyRepositoryGit{}
	gate := newSubagentIdleGate(nil, nil, nil, git)

	if got, err := gate.repository("/tmp/marshal-review-root"); err != nil || got != "" {
		t.Fatalf("failed lookup = (%q, %v), want an empty identity", got, err)
	}
	got, err := gate.repository("/tmp/marshal-review-root")
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if got != "/repo/.git" {
		t.Fatalf("second lookup = %q, want the identity: a failure is not an identity", got)
	}
	if again, _ := gate.repository("/tmp/marshal-review-root"); again != "/repo/.git" {
		t.Fatalf("cached lookup = %q, want the identity", again)
	}
	if calls := git.lookups(); calls != 2 {
		t.Fatalf("repository lookups = %d, want 2 (a success is cached)", calls)
	}
}

// TestChildSymbolsQueryKeepsOnlyTheProjectIndex pins that a child bound to its
// own worktree never answers symbol lookups through the PARENT's language
// server. That query relativizes results to the parent handle's root, so a child
// handed it would read the parent's symbols in paths that look like its own.
func TestChildSymbolsQueryKeepsOnlyTheProjectIndex(t *testing.T) {
	root := t.TempDir()
	parentQuery := lsp.NewSymbolQuery(&lsp.Handle{}, nil, 7)
	f := newWorkspaceFixture(t, root, func(_ *config.Config, opts *native.Options) {
		opts.Symbols = parentQuery
	})

	childState := session.New(f.cfg, root, time.Unix(200, 0), session.Persistence{SessionID: "child-symbols"})
	bound, err := bindSubagentWorkspace(f.parent, childState, agent.SubagentRequest{
		TaskKey: "task-symbols",
		ActorID: "actor-symbols",
		Binding: subagentwork.Binding{Mode: subagentwork.ModeIsolated, Root: root, ProjectRoot: f.parent.WorkingDir},
		Target:  subagentwork.Target{Root: f.parent.WorkingDir},
	}, native.Options{Symbols: parentQuery})
	if err != nil {
		t.Fatalf("bindSubagentWorkspace: %v", err)
	}
	if bound.Symbols == nil {
		t.Fatal("the child lost its symbol query")
	}
	if bound.Symbols == native.SymbolQuerier(parentQuery) {
		t.Fatal("the child kept the parent's language-server-backed symbol query")
	}
	if _, ok := bound.Symbols.(*lsp.SymbolQuery); !ok {
		t.Fatalf("child symbols = %T, want the project-index query", bound.Symbols)
	}
}

// gitReviewRepo builds a repository with two commits and returns its directory,
// the first commit and the second.
func gitReviewRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=marshal test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=marshal test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name, content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		run("add", "-A")
		run("commit", "-m", message)
		return run("rev-parse", "HEAD")
	}
	run("init", "-b", "main")
	first := commit("a.txt", "a\n", "first")
	second := commit("b.txt", "b\n", "second")
	return dir, first, second
}

// TestRestartReconcileSettlesAJournalledIntegration pins the wiring: the owner
// restart path must run the service's Reconcile, not only the outcome pass. An
// intent whose work is provably in the target is then recorded as integrated
// instead of staying a pending operation until some tool happens to retry.
func TestRestartReconcileSettlesAJournalledIntegration(t *testing.T) {
	repo, first, second := gitReviewRepo(t)
	repository, err := worktree.CLIGitOps{}.RepositoryID(repo)
	if err != nil {
		t.Fatalf("RepositoryID: %v", err)
	}

	store := subagentwork.NewMemoryStore()
	svc := subagentwork.NewService(store, worktree.CLIGitOps{})
	const owner = "restart-reconcile-owner"
	rec := subagentwork.Record{
		Version:        subagentwork.RecordVersion,
		OwnerSessionID: owner,
		TaskKey:        "t-integrated",
		InvocationID:   "inv-integrated",
		Outcome:        subagentwork.OutcomeDone,
		Binding: subagentwork.Binding{
			Mode:       subagentwork.ModeIsolated,
			Root:       filepath.Join(t.TempDir(), "gone"),
			Repository: repository,
		},
		Candidate: first,
		Target: subagentwork.Target{
			Root:       repo,
			Repository: repository,
		},
		Integration: &subagentwork.Integration{
			OperationID:    "op-restart",
			Kind:           subagentwork.KindIntegrate,
			State:          subagentwork.IntegrationIntent,
			ExpectedSource: first,
			BeforeTarget:   first,
		},
	}
	if _, _, err := store.Create(context.Background(), rec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = second

	state := session.New(config.Default(), repo, time.Unix(300, 0), session.Persistence{SessionID: owner})
	reconcileRestoredSubagentWorkspaces(context.Background(), svc, store, subagentwork.MutationDeps{}, state)

	got, err := store.Get(context.Background(), owner, "t-integrated")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Integration == nil || got.Integration.State != subagentwork.IntegrationIntegrated {
		t.Fatalf("integration after the restart = %+v, want integrated", got.Integration)
	}
	// The reconciled state lands in the store, which is what agent.await and the
	// lifecycle tools read; session state is refreshed by the next observation,
	// not by reconciliation itself.
	if retained := state.SubagentWorkspaces(); len(retained) != 0 {
		t.Fatalf("retained records = %d, want reconciliation to write only the store", len(retained))
	}
}
