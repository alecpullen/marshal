//go:build linux || darwin || freebsd || openbsd || dragonfly || netbsd || solaris

package snapshot

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// platformSupportsOwnership reports whether this platform can provide safe
// advisory locking and process-tree control. Supported Unix platforms can.
const platformSupportsOwnership = true

// allocatedSize returns the bytes the filesystem actually allocated to path,
// measured without following symlinks. st_blocks counts 512-byte units on
// every supported Unix, so the multiplication is unit-independent.
//
// A directory's own allocation is reported too; because the caller takes the
// larger of this and the rounded logical length, a directory always accounts
// for at least one allocation unit.
func allocatedSize(path string, _ os.FileInfo) (int64, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return 0, fmt.Errorf("lstat: %w", err)
	}
	blocks := int64(st.Blocks)
	if blocks < 0 {
		return 0, fmt.Errorf("negative block count %d", blocks)
	}
	// Overflow here would need a multi-exbibyte file, which cannot exist on
	// the filesystems this supports. Report it rather than wrapping.
	const blockSize = 512
	if blocks > (1<<62)/blockSize {
		return 0, overflowError("allocated size", blocks, blockSize)
	}
	return blocks * blockSize, nil
}
