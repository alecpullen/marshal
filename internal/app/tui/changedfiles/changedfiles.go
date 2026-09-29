// Package changedfiles reads the working tree's diff against a base ref for
// the side panel's changed-files section and for the conversation
// inspector's Changes view.
//
// The package is telemetry: it must never break a turn or block a render.
// Unlike the implementation it replaces, it does not erase the difference
// between "nothing changed" and "the read failed" — every read returns a
// Snapshot whose Status says which of the two happened, and every failure
// carries the error that caused it.
//
// ReadSnapshot (snapshot.go) is the real entry point. Read, below, is the
// legacy adapter kept for callers that have not migrated yet.
package changedfiles

import (
	"context"
	"time"

	"marshal/internal/app/tui/sidepanel"
)

// readTimeout bounds the git subprocesses the compatibility Read runs. A
// slow or hung git must not stall the caller.
const readTimeout = 2 * time.Second

// Read returns the files changed in workingDir since baseRef, including
// untracked-but-staged files and untracked-and-unstaged new files.
//
// It is a thin compatibility adapter over ReadSnapshot, kept with its exact
// original signature so existing callers (the TUI rail's refreshRailChanged
// and the ACP turn telemetry) keep compiling.
//
// It also keeps the original "nil on any failure" contract, which is exactly
// the flaw ReadSnapshot exists to fix: the old Read returned nil on EVERY
// error, so an empty result and a failure were indistinguishable. New code
// should call ReadSnapshot and read Snapshot.Status instead.
//
// Two behaviours changed deliberately, because the old ones were wrong:
//
//   - An untracked file no longer reports an invented `Added: 1`. It reports
//     Added 0 with CountsKnown false on the Snapshot; this adapter has no
//     field to carry the flag, so it reports the honest zero. The plan bans
//     the invention: "Report unknown counts explicitly rather than inventing
//     Added: 1 for untracked files."
//   - Counts come from the worktree against one resolved base OID. The old
//     implementation merged a `--cached` pass that OVERWROTE the worktree
//     counts, so a file whose index and worktree differed was reported with
//     the index's numbers.
func Read(workingDir, baseRef string) []sidepanel.ChangedFile {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	snap := ReadSnapshot(ctx, workingDir, baseRef)
	if snap.Status != StatusOK || len(snap.Files) == 0 {
		// nil, not an empty slice: the original Read returned nil for a clean
		// tree as well as for a failure, and callers (and tests) rely on that
		// exact shape. ReadSnapshot is where the two become distinguishable.
		return nil
	}
	out := make([]sidepanel.ChangedFile, 0, len(snap.Files))
	for _, f := range snap.Files {
		out = append(out, sidepanel.ChangedFile{
			Path:    f.Path,
			Status:  f.Status,
			Added:   f.Added,
			Removed: f.Removed,
		})
	}
	return out
}
