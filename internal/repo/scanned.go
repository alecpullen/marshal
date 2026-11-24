package repo

import "marshal/internal/db"

// ScannedFile is the per-file result returned by Scanner.ScanDetailed.
// The Content slice is the bytes that were hashed; downstream consumers
// (e.g. repo.index) use it for symbol extraction without re-reading the
// file from disk.
// When ReadErr is non-nil, Content is nil and the file could not be read
// (e.g. permission race, concurrent deletion). The FileIndex still contains
// the path and language so callers can report which file failed.
type ScannedFile struct {
	db.FileIndex
	Content []byte
	ReadErr error
	// ContentOmitted reports that the scan deliberately retained no content for
	// this file, which is not the same as the file being unreadable. It is set
	// in two cases: the scan's total content budget was exhausted (the file is
	// past the budget), or the file's size could not be measured, so it could
	// not be judged against that budget. ReadErr is nil in the budget case and
	// the FileIndex is valid in both. Files omitted by the budget run from the
	// boundary to the end of the walk, so the retained files form a prefix; a
	// file omitted because its size could not be measured can appear after
	// retained files. Consumers skip both cases alike; the scan's Warnings name
	// the first file of each kind. Consumers must not infer this state from a
	// nil Content: an empty file legitimately has nil Content too.
	ContentOmitted bool
}
