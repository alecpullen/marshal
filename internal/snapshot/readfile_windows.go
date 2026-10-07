//go:build windows

package snapshot

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileID is the identity of an open file, as observed at one instant. See the
// Unix implementation for what the exact-read protocol does with it.
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

// openWorkspaceFile opens a workspace path for reading without following a
// final reparse point (symlink or junction), and returns the identity observed
// at that moment.
//
// FILE_FLAG_OPEN_REPARSE_POINT is the Windows equivalent of O_NOFOLLOW: without
// it the handle would resolve through the link and read content from outside the
// workspace. The attribute check afterwards turns "opened the link itself" into
// a clean refusal rather than an attempt to read a reparse point as data.
func openWorkspaceFile(path string) (*os.File, fileID, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fileID{}, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return nil, fileID{}, err
	}
	f := os.NewFile(uintptr(h), path)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = f.Close()
		return nil, fileID{}, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = f.Close()
		return nil, fileID{}, errFinalSymlink
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, fileID{}, &unsupportedTypeError{Path: path, Description: "directory"}
	}
	if kind, err := windows.GetFileType(h); err == nil && kind != windows.FILE_TYPE_DISK {
		// A pipe, character device, or console cannot be captured as a file.
		_ = f.Close()
		return nil, fileID{}, &unsupportedTypeError{Path: path, Description: "non-disk device"}
	}
	return f, identifyHandle(&info), nil
}

// openWorkspaceSymlink reads a symbolic link's target text without following
// it. Windows reports a reparse point's target through os.Readlink, which does
// not dereference it.
func openWorkspaceSymlink(path string) (string, error) {
	return os.Readlink(path)
}

// fileIDFromHandle returns the identity of an already-open file.
func fileIDFromHandle(f *os.File) (fileID, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return fileID{}, err
	}
	id := identifyHandle(&info)
	stat, err := f.Stat()
	if err != nil {
		return fileID{}, err
	}
	id.size = stat.Size()
	id.mtime = stat.ModTime().UnixNano()
	id.mode = stat.Mode()
	return id, nil
}

// identifyHandle builds an identity from the handle's own metadata. The file
// index plus the volume serial number is Windows' equivalent of device+inode: a
// path replaced by a different file has a different index.
func identifyHandle(info *windows.ByHandleFileInformation) fileID {
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	return fileID{
		device: uint64(info.VolumeSerialNumber),
		inode:  uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		size:   size,
		// The write time is a FILETIME; it is used only for comparison, so its
		// epoch does not matter.
		mtime: int64(info.LastWriteTime.HighDateTime)<<32 | int64(info.LastWriteTime.LowDateTime),
	}
}
