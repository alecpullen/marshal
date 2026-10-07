//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// fileID is the identity of an open file, as observed at one instant. The
// exact-read protocol compares the identity taken when the file was opened with
// the identity after the read: a mismatch means the bytes that were hashed are
// not the bytes the read produced.
//
// The comparable fields (device, inode, size, mtime, mode) are exactly the ones
// a rename-over or a truncate-and-rewrite changes. Comparing them is not a
// substitute for reading a stable file; it is how an unstable one is detected.
type fileID struct {
	device uint64
	inode  uint64
	size   int64
	mtime  int64
	mode   os.FileMode
}

// sameContentAs reports whether two identities describe the same unchanged
// content. The mode is compared through its type bits only, because a
// permission-only change between open and read does not alter what was read.
func (id fileID) sameContentAs(other fileID) bool {
	return id.device == other.device &&
		id.inode == other.inode &&
		id.size == other.size &&
		id.mtime == other.mtime &&
		id.mode.Type() == other.mode.Type()
}

// openWorkspaceFile opens a workspace path for reading without following a
// final symbolic link, and returns the identity observed at that moment.
//
// O_NOFOLLOW is the load-bearing flag: without it, a path that was classified
// as a regular file and then replaced by a symlink would be read through the
// link, pulling content from outside the workspace into the snapshot.
//
// O_NONBLOCK matters too. Opening a named pipe read-only blocks until a writer
// appears, which would hang a capture forever on a path this function is about
// to refuse anyway; with O_NONBLOCK the open returns and the type check below
// rejects it.
func openWorkspaceFile(path string) (*os.File, fileID, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		// With O_NOFOLLOW, ELOOP means exactly "the final component is a
		// symbolic link". Reporting only the generic "too many levels" error
		// would leave the caller unable to tell a link it must not follow from
		// a path it could not open at all.
		if errors.Is(err, syscall.ELOOP) {
			return nil, fileID{}, fmt.Errorf("%w: %v", errFinalSymlink, err)
		}
		return nil, fileID{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	id, err := fileIDFromHandle(f)
	if err != nil {
		_ = f.Close()
		return nil, fileID{}, err
	}
	if id.mode&os.ModeSymlink != 0 {
		_ = f.Close()
		return nil, fileID{}, errFinalSymlink
	}
	if !id.mode.IsRegular() {
		_ = f.Close()
		return nil, fileID{}, &unsupportedTypeError{Path: path, Description: describeMode(id.mode)}
	}
	return f, id, nil
}

// openWorkspaceSymlink reads a symbolic link's target text without following it.
//
// The target TEXT is the blob content, exactly as Git records it. Reading it
// through os.Readlink is the one operation that never dereferences the link, so
// a link pointing outside the workspace contributes a path string, never the
// content it points at.
func openWorkspaceSymlink(path string) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	return target, nil
}

// fileIDFromHandle returns the identity of an already-open file.
func fileIDFromHandle(f *os.File) (fileID, error) {
	info, err := f.Stat()
	if err != nil {
		return fileID{}, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return fileID{}, err
	}
	return fileID{
		device: uint64(st.Dev),
		inode:  uint64(st.Ino),
		size:   info.Size(),
		mtime:  info.ModTime().UnixNano(),
		mode:   statMode(&st, info),
	}, nil
}

// statMode reconstructs the file type from the raw stat mode, so the type check
// and the device/inode reading come from the same syscall result rather than
// from two stats that could disagree.
func statMode(st *syscall.Stat_t, info os.FileInfo) os.FileMode {
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		return info.Mode() | os.ModeDir
	case syscall.S_IFLNK:
		return info.Mode() | os.ModeSymlink
	case syscall.S_IFIFO:
		return info.Mode() | os.ModeNamedPipe
	case syscall.S_IFSOCK:
		return info.Mode() | os.ModeSocket
	case syscall.S_IFCHR:
		return info.Mode() | os.ModeDevice | os.ModeCharDevice
	case syscall.S_IFBLK:
		return info.Mode() | os.ModeDevice
	}
	return info.Mode()
}
