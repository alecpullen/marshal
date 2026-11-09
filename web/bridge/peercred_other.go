//go:build !linux && !darwin

package bridge

import (
	"errors"
	"net"
	"os"
)

// peerCredsSupported says this system cannot read a socket peer's user
// ID, so the local profile refuses to start here (decision 7): a daemon
// that cannot tell who is on the other end of its socket must not serve
// one.
const peerCredsSupported = false

// peerUID has no portable implementation. ListenLocal refuses before it
// can matter; this stays an error so a future caller fails closed.
func peerUID(net.Conn) (int, error) {
	return 0, errors.New("bridge: peer credentials are not supported on this system")
}

// dirOwnedBy cannot tell on this system, and is unreachable in practice:
// ListenLocal refuses where peerCredsSupported is false.
func dirOwnedBy(os.FileInfo, int) bool { return true }
