//go:build openbsd

package snapshot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// freeSpaceBytes returns the bytes available to an unprivileged writer on the
// filesystem holding path. OpenBSD names the statfs fields F_bsize/F_bavail
// rather than Bsize/Bavail; the value is the same, and F_bavail excludes
// root-reserved blocks.
func freeSpaceBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs: %w", err)
	}
	avail := int64(st.F_bavail)
	if avail < 0 {
		return 0, fmt.Errorf("statfs reported negative available blocks %d", avail)
	}
	return mulBlockBytes(int64(st.F_bsize), avail)
}
