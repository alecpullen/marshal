package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/agent"
	"marshal/internal/app/session"
	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/lsp"
	"marshal/internal/snapshot"
	"marshal/internal/subagentwork"
	"marshal/internal/tools/native"
	"marshal/internal/worktree"
)

// subagentWorkspaces returns the conversation's one owned-workspace service,
// creating it on the first build and returning the same instance to every later
// one. A config reload therefore reuses the service, its store and the records
// in it instead of starting a second one — and, because the service is
// instance-scoped, two conversations in one process never share a store.
//
// A build with no database (an embedded fixture) has nowhere durable to record
// ownership, so it wires no service at all: dispatch then keeps the pre-cutover
// binding of the parent's captured checkout rather than claiming an ownership it
// cannot prove.
func (s *conversationCommandService) subagentWorkspaces(database *db.DB) *subagentwork.Service {
	store := s.subagentWorkspaceStore(database)
	if store == nil {
		return nil
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if s.workspaces == nil {
		s.workspaces = subagentwork.NewService(store, worktree.CLIGitOps{})
	}
	return s.workspaces
}

// subagentWorkspaceStore returns the conversation's one durable record store, or
// nil when this build has no database. It is the same instance the service
// writes through, so a restart reconciliation and the dispatch service observe
// one another's writes.
func (s *conversationCommandService) subagentWorkspaceStore(database *db.DB) subagentwork.Store {
	if s == nil || database == nil {
		return nil
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if s.workspaceStore == nil {
		s.workspaceStore = db.NewSubagentWorkspaceStore(database)
	}
	return s.workspaceStore
}

// reconcileRestoredSubagentWorkspaces runs once per conversation owner, before
// any lifecycle tool is registered.
//
// Two things are settled here. First, whatever an interrupted operation left in
// git: an intent whose merge landed, a settled operation whose artifacts are
// already gone, or a target still holding the operation's own half-applied
// merge. That is the service's Reconcile, and it is observational — it aborts no
// merge and removes nothing — so it can only ever record what git already
// proves. Second, execution outcomes: a record this process did not write cannot
// still be running, so an active outcome left by a previous process becomes
// interrupted (the child was up when the process ended) or unknown (allocation
// was never confirmed).
//
// It is deliberately NOT called on a rebuild. A config reload borrows the live
// service, keeps its records and its children, and must never mistake itself for
// a restart; neither does a session with a child still live in this process.
func reconcileRestoredSubagentWorkspaces(ctx context.Context, service *subagentwork.Service, store subagentwork.Store, deps subagentwork.MutationDeps, state *session.State) {
	if state == nil || state.HasRunningSubagent() {
		return
	}
	var records []subagentwork.Record
	if service != nil {
		reconciled, err := service.Reconcile(ctx, state.SessionID(), deps)
		if err != nil {
			state.Logger().Warn("reconcile retained subagent workspaces failed", "error", err)
		}
		records = reconciled
	}
	if records == nil {
		if store == nil {
			return
		}
		listed, err := store.List(ctx, state.SessionID())
		if err != nil {
			state.Logger().Warn("read retained subagent workspaces failed", "error", err)
			return
		}
		records = listed
	}
	for _, rec := range records {
		var outcome string
		switch rec.Outcome {
		case subagentwork.OutcomePreparing:
			// The intent was written but allocation never reported a definite
			// result, so the artifacts on disk are unverified: unknown, not
			// "interrupted", which would claim a child existed.
			outcome = subagentwork.OutcomeUnknown
		case subagentwork.OutcomePrepared, subagentwork.OutcomeRunning:
			outcome = subagentwork.OutcomeInterrupted
		default:
			continue
		}
		updated := rec
		updated.Outcome = outcome
		written, casErr := store.CompareAndSwap(ctx, updated, rec.Revision)
		if casErr != nil {
			state.Logger().Warn("mark retained subagent workspace restarted failed", "error", casErr, "subagent_task", rec.TaskKey)
			continue
		}
		state.ApplySubagentWorkspace(written)
	}
}

// bindSubagentWorkspace fixes one child's whole tool surface to the workspace
// its dispatcher recorded, and returns the native options that express it.
//
// The split it implements is the point of the function: a child shares its
// parent's CONVERSATION (command service, policy, approvals, watches) but
// never its WORKSPACE POINTER. Every resource that names a directory or an
// undo slot is resolved once, here, from the recorded binding, so a parent
// that later rebinds to another checkout cannot retarget a running child.
//
//   - The child's own state is bound to the recorded root with
//     BindIsolatedWorkspace, which also marks it dispatcher-bound: if the root
//     disappears the child fails in it instead of silently landing somewhere
//     else. This happens BEFORE anything reads the child's authority, because
//     the authority snapshot carries the workspace root.
//   - CommandDir and WorkspaceRoot are the bound root, so file resolution and
//     every managed command capture the child's tree rather than following
//     whatever root the parent selects next.
//   - Backup ownership differs by mode. A shared child edits the parent's own
//     checkout, so its /undo evidence must stay in the conversation's slot
//     (BackupState = parent). An isolated child's writes are its own, so the
//     evidence falls back to its own workspace state.
//   - The file ledger is namespaced per isolated task, so a read in one
//     child's copy cannot license an edit in a sibling's different copy.
//     Shared children reuse the conversation's ledger with their own actor
//     identity, which is what keeps one working tree under one conversation.
//   - No language-server lease is borrowed: the parent's LSPSync and LSPIndex
//     are mutable state aimed at whichever root the parent last selected, so a
//     child bound elsewhere must not rescan through them. Diagnostics fall back
//     to the configured root-local checkers, which run in the bound root.
//     Symbols keep only the project index: the parent's query is
//     language-server-first and answers in the PARENT handle's root, so a child
//     handed that query would read the parent's symbols as though they were its
//     own.
//
// The returned options are the base for the child's registration. The caller
// still owns the policy clone, the registry assembly and the scope view,
// because those must see the fully assembled child registry.
func bindSubagentWorkspace(parent, child *session.State, req agent.SubagentRequest, opts native.Options) (native.Options, error) {
	if child == nil {
		return opts, errors.New("agent.run: a child session state is required")
	}
	binding := req.Binding
	root := strings.TrimSpace(binding.Root)
	if root == "" {
		return opts, errors.New("agent.run: the dispatch carries no workspace binding")
	}
	switch binding.Mode {
	case subagentwork.ModeIsolated, subagentwork.ModeShared:
	default:
		return opts, fmt.Errorf("agent.run: workspace mode %q is not one this build can bind", binding.Mode)
	}

	// A child is never offered mode.request: it has no transport of its own,
	// so a capability request would publish a prompt nothing can settle. The
	// scope view also excludes the tool; this keeps the registration honest
	// even for a caller that assembles its own view.
	child.BindIsolatedWorkspace(root)

	opts.SessionState = child
	// The child owns its workspace scope. Sharing the parent's state here is
	// what let a child's backup, active root and previews follow the parent.
	opts.WorkspaceState = child
	opts.WorkspaceRoot = root
	opts.CommandDir = root
	opts.AccessRequests = false
	// Every direct operation and managed command of this child participates in
	// the shared root-use protocol for the repository its dispatcher recorded,
	// so an owned-child lifecycle mutation at that repository refuses while the
	// child is working. The repository comes from the binding, never from the
	// parent's current checkout.
	if strings.TrimSpace(binding.Repository) != "" {
		owner := ""
		if parent != nil {
			owner = parent.SessionID()
		}
		opts.WorkspaceUse = workspaceUseForRepository(binding.Repository, owner)
	}

	opts.BackupState = nil
	switch binding.Mode {
	case subagentwork.ModeShared:
		// The shared child writes into the parent's checkout, so the
		// conversation's /undo must see it — and must keep seeing it after the
		// parent rebinds, which is why the owner is captured here rather than
		// resolved from the parent later.
		opts.BackupState = parent
	case subagentwork.ModeIsolated:
		// An isolated child's writes are its own tree's; borrowing the
		// conversation's ledger would let a read of one root-relative name in
		// this child license an edit of a different copy in a sibling.
		if opts.DB != nil {
			opts.FileTracker = filetrack.New(opts.DB.SQLDB(), subagentLedgerNamespace(parent, req))
		}
	}

	// Diagnostics: keep the project-scoped symbol index, drop the parent's
	// mutable language-server state — and drop its server side from the symbol
	// query too, which answers in the parent handle's root.
	opts.LSPSync = nil
	opts.LSPIndex = nil
	if query, ok := opts.Symbols.(*lsp.SymbolQuery); ok {
		opts.Symbols = query.IndexOnly()
	}
	return opts, nil
}

// subagentLedgerNamespace is the stable ledger key of one isolated child: the
// owning conversation plus the task identity its dispatcher allocated. It is
// derived, never random, so a resumed dispatch reads the same ledger.
func subagentLedgerNamespace(parent *session.State, req agent.SubagentRequest) string {
	owner := ""
	if parent != nil {
		owner = parent.SessionID()
	}
	key := strings.TrimSpace(req.TaskKey)
	if key == "" {
		// A request that arrived without a task key still gets its own
		// namespace rather than the conversation's: the actor ID is the next
		// stable identity the dispatcher allocated for this child.
		key = strings.TrimSpace(req.ActorID)
	}
	if key == "" {
		return owner + "/subagent"
	}
	return owner + "/subagent/" + key
}

// bindSubagentSnapshotter gives a child an exact-root view of the
// conversation's snapshotter, so its pre-write snapshots and restores act on
// the workspace it was bound to and never on whichever checkout the parent
// rebinds to later. A snapshotter that is not the rooted implementation (a
// test fake) is passed through unchanged.
func bindSubagentSnapshotter(snap session.Snapshotter, root string) session.Snapshotter {
	rooted, ok := snap.(*snapshot.Rooted)
	if !ok || strings.TrimSpace(root) == "" {
		return snap
	}
	return rooted.Bound(root)
}

// subagentSnapshotAt returns the exact-root capture callback a managed lifecycle
// mutation uses. The root is the one the RECORD captured — the child's own
// checkout, or the target it is destined for — never the parent's current
// workspace, which by the time of the mutation may be somewhere else entirely.
//
// It reports what actually happened: a published capture's hash, or an empty
// hash when this build has no snapshotter, because fabricating one would claim a
// rollback coverage that does not exist. A store that refuses the capture is an
// error, and the mutation is refused with it rather than proceeding unprotected.
func subagentSnapshotAt(state *session.State) func(context.Context, string) (string, error) {
	return func(ctx context.Context, root string) (string, error) {
		if state == nil || strings.TrimSpace(root) == "" {
			return "", nil
		}
		snap := state.Snapshotter()
		if snap == nil {
			return "", nil
		}
		return bindSubagentSnapshotter(snap, root).Track(ctx)
	}
}
