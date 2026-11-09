//go:build darwin

package bridge

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// peerCredsSupported says LOCAL_PEERCRED answers here, which is what
// lets the local profile serve at all (decision 7).
const peerCredsSupported = true

// darwin's socket level, its peer-credential option, and the only
// defined version of xucred. syscall does not declare them.
const (
	solLocal      = 0 // SOL_LOCAL
	localPeerCred = 1 // LOCAL_PEERCRED
	xucredVersion = 0 // XUCRED_VERSION
)

// xucred is darwin's struct xucred, which LOCAL_PEERCRED fills.
type xucred struct {
	Version uint32
	UID     uint32
	NGroups int16
	_       int16
	Groups  [16]uint32
}

// peerUID is the user ID of the process on the other end of conn, a unix
// socket connection. LOCAL_PEERCRED is recorded by the kernel when the
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
	var cred xucred
	// getsockopt writes the struct and its length back through the
	// pointers, so both are live for the duration of Control only.
	size := uintptr(unsafe.Sizeof(cred))
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		_, _, errno := syscall.Syscall6(
			syscall.SYS_GETSOCKOPT,
			fd,
			uintptr(solLocal),
			uintptr(localPeerCred),
			uintptr(unsafe.Pointer(&cred)),
			uintptr(unsafe.Pointer(&size)),
			0,
		)
		if errno != 0 {
			credErr = errno
		}
	}); err != nil {
		return 0, fmt.Errorf("bridge: peer credentials: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("bridge: peer credentials: %w", credErr)
	}
	// The kernel writes the struct's length back through size. A short
	// answer would leave cred zeroed — version 0, uid 0, the peer a
	// root daemon accepts — so the length is checked, not trusted.
	if want := uintptr(unsafe.Sizeof(cred)); size != want {
		return 0, fmt.Errorf("bridge: peer credentials came back as %d bytes, want %d", size, want)
	}
	// The only defined version of xucred is the one getsockopt returned
	// here; anything else is a struct we do not understand.
	if cred.Version != xucredVersion {
		return 0, fmt.Errorf("bridge: unexpected peer credential version %d", cred.Version)
	}
	return int(cred.UID), nil
}
