//go:build linux || darwin

package bridge

import (
	"errors"
	"syscall"
)

// workingDraftOwnerAlive reports whether pid names a live process. Signal 0
// performs the existence and permission check without delivering anything: a
// process owned by somebody else answers EPERM and is still alive.
func workingDraftOwnerAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
