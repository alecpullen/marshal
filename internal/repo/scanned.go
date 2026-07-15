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
}
