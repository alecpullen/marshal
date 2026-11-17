package native

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
)

// writeTempContents writes the payload into the temp file. Overridden in tests
// to simulate ENOSPC / a partial write.
var writeTempContents = func(f *os.File, data []byte) error { _, err := f.Write(data); return err }

// renameTempFile replaces the destination with the temp file. Overridden in
// tests to simulate a failed commit.
var renameTempFile = func(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

// atomicTempSeq makes the temp file's name unique within the process; O_EXCL
// rejects the residual collisions with another writer.
var atomicTempSeq atomic.Uint64

// writeFileAtomic replaces path's contents with data without ever destroying
// the destination before the new bytes are on disk.
//
// os.WriteFile opens the destination with O_TRUNC, so a failure part-way
// through (ENOSPC, a quota, EIO) leaves the original truncated or empty while
// the tool reports an error — the write destroyed the only copy of the file.
// Here the payload goes to a temp file in the destination's own directory (so
// the final rename cannot cross a filesystem), and the destination is replaced
// only by that rename, which is atomic. Every failure before the rename leaves
// the destination byte-for-byte unchanged, and the temp file is always removed.
//
// mode is the destination's current mode when exists, else the mode a new file
// should get. The caller has already resolved path against the workspace roots.
func writeFileAtomic(path string, data []byte, mode os.FileMode, exists bool) error {
	target, err := resolveWriteTarget(path)
	if err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}

	if exists {
		// Reproduce os.WriteFile's refusal on an unwritable destination before
		// anything is staged: an atomic replace would otherwise swap out a file
		// the user made deliberately read-only. This open does not truncate, so
		// a failure here is harmless.
		f, err := os.OpenFile(target, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("write file %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("write file %s: %w", path, err)
		}
	}

	tempName := filepath.Join(filepath.Dir(target), fmt.Sprintf(
		".%s.marshal-tmp-%d-%d", filepath.Base(target), os.Getpid(), atomicTempSeq.Add(1)))
	// An existing destination is created 0600 and chmodded to its exact mode;
	// a new one is created with the requested mode so the umask applies exactly
	// as it does for os.WriteFile.
	createMode := mode
	if exists {
		createMode = 0o600
	}
	f, err := os.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, createMode)
	if err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		if rmErr := os.Remove(tempName); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Warn("file write: could not remove temp file", "path", tempName, "error", rmErr)
		}
	}()

	if exists {
		if err := f.Chmod(mode); err != nil {
			f.Close()
			return fmt.Errorf("write file %s: %w", path, err)
		}
	}
	// ENOSPC, a quota and EIO surface here, while the destination is untouched.
	if err := writeTempContents(f, data); err != nil {
		f.Close()
		return fmt.Errorf("write file %s: %w", path, err)
	}
	// Durability of the payload before the commit: a rename that reached the
	// disk ahead of its data would leave the destination empty after a crash.
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("write file %s: %w", path, err)
	}
	// An unchecked close can hide a write error, so it is checked explicitly.
	if err := f.Close(); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}

	// The commit point: before this the destination still holds its original
	// bytes, after it the new bytes are in place.
	if err := renameTempFile(tempName, target); err != nil {
		return fmt.Errorf("write file %s: %w", path, err)
	}
	committed = true

	// Best effort: make the rename itself durable. The write has already
	// committed, so a failure here is logged and never fails the tool.
	syncDir(filepath.Dir(target))
	return nil
}

// resolveWriteTarget follows a symlinked destination so the write changes the
// link's target instead of replacing the link with a regular file, which is
// what os.WriteFile does today.
func resolveWriteTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	// A dangling link: the write creates its target, as os.WriteFile would.
	dir, dirErr := filepath.EvalSymlinks(filepath.Dir(path))
	if dirErr != nil {
		return "", dirErr
	}
	return filepath.Join(dir, filepath.Base(path)), nil
}

// syncDir makes a directory entry durable, best effort: the rename has already
// committed, so a failure here can only cost durability, never the write.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		slog.Warn("file write: could not open parent directory to sync", "dir", dir, "error", err)
		return
	}
	if err := d.Sync(); err != nil {
		slog.Warn("file write: could not sync parent directory", "dir", dir, "error", err)
	}
	if err := d.Close(); err != nil {
		slog.Warn("file write: could not close parent directory", "dir", dir, "error", err)
	}
}
