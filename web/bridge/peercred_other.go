//go:build !linux && !darwin

package bridge

import (
	"errors"
	"net"
	"os"
)

// peerUID has no portable implementation. Refusing is deliberate: the
// local profile must not start where it cannot tell who is on the other
// end of the socket.
func peerUID(net.Conn) (int, error) {
	return 0, errors.New("bridge: peer credentials are not supported on this system")
}

// dirOwnedBy cannot tell on this system, so it does not stand in the way
// of ListenLocal; PeerChecked refuses every peer here anyway.
func dirOwnedBy(os.FileInfo, int) bool { return true }
