//go:build linux || darwin

package bridge

import (
	"os"
	"syscall"
)

// dirOwnedBy reports whether fi belongs to uid. The stat layout behind
// os.FileInfo is per system, and both systems that can vouch for a peer
// lay it out as syscall.Stat_t, so the check lives here rather than in
// each platform file.
func dirOwnedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == uid
}
