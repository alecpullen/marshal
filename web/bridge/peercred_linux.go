//go:build linux

package bridge

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

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

// dirOwnedBy reports whether fi belongs to uid. The stat layout behind
// os.FileInfo is per system, so this sits beside the other per-system
// credential check: the same systems that can vouch for a peer can vouch
// for a directory.
func dirOwnedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == uid
}
