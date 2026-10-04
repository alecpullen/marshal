// Package changedfiles reads the working tree's diff against a base ref for
// the session sheet's changed-files section, the status line's ±N files
// segment and ACP turn telemetry.
//
// The package is telemetry: it must never break a turn or block a render. It
// does not erase the difference between "nothing changed" and "the read
// failed": ReadSnapshot (snapshot.go) returns a Snapshot whose Status says
// which of the two happened, and every failure carries the error that caused
// it. Read, below, is the simpler view for callers that only need rows.
package changedfiles

import (
	"context"
	"time"

	"marshal/internal/app/tui/sessionsheet"
)

// readTimeout bounds the git subprocesses Read runs. A slow or hung git must
// not stall the caller.
const readTimeout = 2 * time.Second

// Read returns the files changed in workingDir since baseRef, including
// staged additions and untracked, unignored new files. It returns nil both
// for a clean tree and for any failure; callers that must tell those apart
// use ReadSnapshot.
//
// Counts come from the worktree against one resolved base OID. An untracked
// or binary file reports Added and Removed as 0 because git gives no line
// counts for it; Snapshot.CountsKnown carries that distinction, which a
// sessionsheet.ChangedFile has no field for.
func Read(workingDir, baseRef string) []sessionsheet.ChangedFile {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	snap := ReadSnapshot(ctx, workingDir, baseRef)
	if snap.Status != StatusOK || len(snap.Files) == 0 {
		// nil, not an empty slice, for a clean tree as well as a failure:
		// callers (and tests) rely on that exact shape.
		return nil
	}
	out := make([]sessionsheet.ChangedFile, 0, len(snap.Files))
	for _, f := range snap.Files {
		out = append(out, sessionsheet.ChangedFile{
			Path:    f.Path,
			Status:  f.Status,
			Added:   f.Added,
			Removed: f.Removed,
		})
	}
	return out
}
