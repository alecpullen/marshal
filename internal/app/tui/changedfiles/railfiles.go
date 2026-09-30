package changedfiles

import (
	"marshal/internal/app/tui/sidepanel"
)

// RailFiles adapts a Snapshot to the rows the side panel renders.
//
// It is the ONE place a Snapshot becomes rail rows, so the rail and the Changes
// inspector — which will render the richer Snapshot directly — cannot disagree
// about what changed: both read the same reading, and only the presentation
// differs.
//
// Two deliberate losses, both because the rail's row type cannot express the
// distinction:
//
//   - A failed read returns no rows at all. The row type has no error slot, and
//     a partial list from a half-finished read would look as authoritative as a
//     complete one. An empty rail is the honest rendering of "we could not
//     tell"; the inspector, which CAN say so, reports the status in words.
//   - An unknown count becomes zero. The alternative is to invent a number,
//     which is what the previous implementation did (every untracked file was
//     reported as "added 1"). A wrong number on screen is indistinguishable
//     from a right one, so showing nothing is strictly better.
func RailFiles(snap Snapshot) []sidepanel.ChangedFile {
	if snap.Status != StatusOK {
		return nil
	}
	if len(snap.Files) == 0 {
		return nil
	}
	rows := make([]sidepanel.ChangedFile, 0, len(snap.Files))
	for _, f := range snap.Files {
		row := sidepanel.ChangedFile{
			Path:   f.Path,
			Status: f.Status,
		}
		if f.CountsKnown {
			row.Added = f.Added
			row.Removed = f.Removed
		}
		if row.Status == 0 {
			// A file with no status letter still needs one to render: the rail
			// prints the letter as its marker. 'M' is the honest default only
			// because the snapshot classified it as changed; the Kind is the
			// authority and the letter is a rendering convenience.
			row.Status = statusLetterForKind(f.Kind)
		}
		rows = append(rows, row)
	}
	return rows
}

// statusLetterForKind maps a FileKind to the letter the rail renders.
func statusLetterForKind(kind FileKind) rune {
	switch kind {
	case FileAdded, FileUntracked:
		return 'A'
	case FileDeleted:
		return 'D'
	case FileRenamed:
		return 'R'
	default:
		return 'M'
	}
}
