//go:build unix

package fleetprofile

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivate refuses a fleet.toml that others can read (decision 3): on unix
// systems it must belong to the user and carry no group or other permission
// bits.
func checkPrivate(path string, info os.FileInfo) error {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if uid := int(st.Uid); uid != os.Getuid() {
			return fmt.Errorf("fleet.toml at %s belongs to uid %d, not you", path, uid)
		}
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("fleet.toml at %s is mode %04o: run chmod 600 %s", path, perm, path)
	}
	return nil
}
