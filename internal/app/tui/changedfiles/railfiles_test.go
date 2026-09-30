package changedfiles

import (
	"strings"
	"testing"

	"marshal/internal/app/tui/sidepanel"
)

// TestRailFilesMapsAKnownSnapshot pins the adapter the rail reads through: a
// successful snapshot's files become rail rows with their counts.
func TestRailFilesMapsAKnownSnapshot(t *testing.T) {
	snap := Snapshot{
		Status: StatusOK,
		Files: []File{
			{Path: "a.go", Status: 'M', Added: 3, Removed: 1, CountsKnown: true},
			{Path: "b.go", Status: 'A', Added: 7, CountsKnown: true},
		},
	}

	got := RailFiles(snap)
	if len(got) != 2 {
		t.Fatalf("RailFiles returned %d rows, want 2", len(got))
	}
	if got[0].Path != "a.go" || got[0].Status != 'M' || got[0].Added != 3 || got[0].Removed != 1 {
		t.Fatalf("row 0 = %+v, want a.go M +3 -1", got[0])
	}
	if got[1].Path != "b.go" || got[1].Added != 7 {
		t.Fatalf("row 1 = %+v, want b.go +7", got[1])
	}
}

// TestRailFilesReportsUnknownCountsAsZero pins the honest degradation: the rail
// row type has no way to say "unknown", so an unknown count becomes zero rather
// than a fabricated number. Fabricating one — the old implementation wrote
// Added: 1 for every untracked file — is worse than showing nothing, because a
// wrong number is indistinguishable from a right one on screen.
func TestRailFilesReportsUnknownCountsAsZero(t *testing.T) {
	snap := Snapshot{
		Status: StatusOK,
		Files: []File{
			{Path: "untracked.txt", Status: 'A', CountsKnown: false},
			{Path: "bin.dat", Kind: FileBinary, Status: 'M', CountsKnown: false},
		},
	}

	got := RailFiles(snap)
	if len(got) != 2 {
		t.Fatalf("RailFiles returned %d rows, want 2", len(got))
	}
	for i, row := range got {
		if row.Added != 0 || row.Removed != 0 {
			t.Fatalf("row %d = %+v, want zero counts rather than a fabricated number", i, row)
		}
	}
}

// TestRailFilesIsEmptyForAFailedSnapshot pins that a failure never renders as a
// list. The rail cannot express an error, and a partial list built from a
// half-finished read would look authoritative.
func TestRailFilesIsEmptyForAFailedSnapshot(t *testing.T) {
	for _, status := range []Status{
		StatusMissingBase, StatusNotARepo, StatusTimeout,
		StatusCancelled, StatusFailed,
	} {
		snap := Snapshot{
			Status: status,
			// Deliberately non-empty: a failure that happens to have files
			// must still not render them, because the read did not finish.
			Files: []File{{Path: "partial.go", Status: 'M', CountsKnown: true}},
		}
		if got := RailFiles(snap); len(got) != 0 {
			t.Errorf("RailFiles(%s) returned %d rows, want none", status, len(got))
		}
	}
}

// TestRailFilesPreservesOrder pin that the rail renders rows in the order the
// snapshot produced them. Re-ordering would make the list jump between turns
// for no reason a user could see.
func TestRailFilesPreservesOrder(t *testing.T) {
	snap := Snapshot{
		Status: StatusOK,
		Files: []File{
			{Path: "zzz.go", CountsKnown: true},
			{Path: "aaa.go", CountsKnown: true},
			{Path: "mmm.go", CountsKnown: true},
		},
	}
	got := RailFiles(snap)
	var paths []string
	for _, r := range got {
		paths = append(paths, r.Path)
	}
	if strings.Join(paths, ",") != "zzz.go,aaa.go,mmm.go" {
		t.Fatalf("order = %v, want the snapshot's own order", paths)
	}
}

// TestRailFilesRenameUsesTheNewPath pins which path a rename row shows: the
// working tree's current path, because that is the file that exists and the one
// a user can open.
func TestRailFilesRenameUsesTheNewPath(t *testing.T) {
	snap := Snapshot{
		Status: StatusOK,
		Files: []File{
			{Path: "new.go", OldPath: "old.go", Kind: FileRenamed, Status: 'R', CountsKnown: true},
		},
	}
	got := RailFiles(snap)
	if len(got) != 1 {
		t.Fatalf("RailFiles returned %d rows, want 1", len(got))
	}
	if got[0].Path != "new.go" {
		t.Fatalf("path = %q, want the current path new.go", got[0].Path)
	}
}

// TestRailFilesEmptySnapshotIsEmpty pins the clean-tree case: no files in, no
// rows out, no error.
func TestRailFilesEmptySnapshotIsEmpty(t *testing.T) {
	if got := RailFiles(Snapshot{Status: StatusOK}); len(got) != 0 {
		t.Fatalf("RailFiles of a clean snapshot returned %d rows", len(got))
	}
	if got := RailFiles(Snapshot{}); len(got) != 0 {
		t.Fatalf("RailFiles of a zero snapshot returned %d rows", len(got))
	}
}

// TestRailFilesReturnsTheRowTypeTheRailRenders pins the return type. It is
// asserted rather than assumed because the rail's section renders
// sidepanel.ChangedFile, and a signature drift here would be caught only at the
// call site.
func TestRailFilesReturnsTheRowTypeTheRailRenders(t *testing.T) {
	var rows []sidepanel.ChangedFile = RailFiles(Snapshot{Status: StatusOK})
	_ = rows
}
