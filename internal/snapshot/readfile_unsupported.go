//go:build !linux && !darwin && !freebsd && !openbsd && !dragonfly && !netbsd && !solaris && !windows

package snapshot

import "os"

// fileID is the identity of an open file. On a platform without a verified
// no-follow open, capture cannot be performed at all (ownership already fails
// closed here), so this type exists only to keep the shared code compiling.
type fileID struct {
	device uint64
	inode  uint64
	size   int64
	mtime  int64
	mode   os.FileMode
}

// sameContentAs reports whether two identities describe the same unchanged
// content.
func (id fileID) sameContentAs(other fileID) bool {
	return id.device == other.device &&
		id.inode == other.inode &&
		id.size == other.size &&
		id.mtime == other.mtime &&
		id.mode.Type() == other.mode.Type()
}

// openWorkspaceFile fails closed: with no verified no-follow open there is no
// way to promise that the bytes captured came from the path that was
// classified, so capture must not proceed.
func openWorkspaceFile(string) (*os.File, fileID, error) {
	return nil, fileID{}, storeErrorf(ReasonUnsupportedPlatform,
		"no-follow file open is not available on this platform")
}

// openWorkspaceSymlink fails closed for the same reason.
func openWorkspaceSymlink(string) (string, error) {
	return "", storeErrorf(ReasonUnsupportedPlatform,
		"no-follow symlink read is not available on this platform")
}

// fileIDFromHandle fails closed for the same reason.
func fileIDFromHandle(*os.File) (fileID, error) {
	return fileID{}, storeErrorf(ReasonUnsupportedPlatform,
		"no-follow file open is not available on this platform")
}
