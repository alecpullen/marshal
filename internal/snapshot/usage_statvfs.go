//go:build netbsd || solaris

package snapshot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// freeSpaceBytes returns the bytes available to an unprivileged writer on the
// filesystem holding path. NetBSD and Solaris expose statvfs rather than
// statfs; Bavail is the field an unprivileged writer can actually consume.
func freeSpaceBytes(path string) (int64, error) {
	var st unix.Statvfs_t
	if err := unix.Statvfs(path, &st); err != nil {
		return 0, fmt.Errorf("statvfs: %w", err)
	}
	avail := int64(st.Bavail)
	if avail < 0 {
		return 0, fmt.Errorf("statvfs reported negative available blocks %d", avail)
	}
	return mulBlockBytes(int64(st.Bsize), avail)
}
