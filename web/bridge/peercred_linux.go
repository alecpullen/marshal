//go:build linux

package bridge

import (
	"fmt"
	"net"
	"syscall"
)

// peerCredsSupported says SO_PEERCRED answers here, which is what lets
// the local profile serve at all (decision 7).
const peerCredsSupported = true

// peerUID is the user ID of the process on the other end of conn, a unix
// socket connection. SO_PEERCRED is recorded by the kernel when the
// connection is established, so the answer is the kernel's, not the
// peer's claim.
//
// The descriptor is reached through SyscallConn and its Control, never
// through File: File would duplicate the descriptor, and a dup is not
// what the credentials were recorded against.
func peerUID(conn net.Conn) (int, error) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, fmt.Errorf("bridge: %T has no syscall descriptor", conn)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("bridge: peer credentials: %w", err)
	}
	uid := 0
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if err != nil {
			credErr = err
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return 0, fmt.Errorf("bridge: peer credentials: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("bridge: peer credentials: %w", credErr)
	}
	return uid, nil
}
