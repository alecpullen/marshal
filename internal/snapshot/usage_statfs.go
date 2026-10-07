//go:build linux || darwin || freebsd || dragonfly

package snapshot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// freeSpaceBytes returns the bytes available to an unprivileged writer on the
// filesystem holding path, using statfs. It is a snapshot: the value can only
// be used to reject a write that does not fit, never to promise one will.
//
// Bavail (not Bfree) is used deliberately: Bfree counts blocks reserved for
// root, which an ordinary writer cannot consume.
func freeSpaceBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs: %w", err)
	}
	avail := int64(st.Bavail)
	if avail < 0 {
		return 0, fmt.Errorf("statfs reported negative available blocks %d", avail)
	}
	return mulBlockBytes(int64(st.Bsize), avail)
}
