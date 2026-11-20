package bridge

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// webSocketName is the socket the web listener uses when --web-listen is
// "unix": it sits beside the daemon's own socket in the daemon directory.
const webSocketName = "web.sock"

// WebListen is a parsed --web-listen.
type WebListen struct {
	Network string // "unix" or "tcp"
	Address string // the socket path, or host:port
	Group   string // unix only: the group the socket is shared with; "" none
}

// String is "unix:<path>" or host:port, as DaemonInfo.WebListen reports it.
func (w WebListen) String() string {
	if w.Network == "unix" {
		return "unix:" + w.Address
	}
	return w.Address
}

// ParseWebListen reads value: "unix" (web.sock in daemonSocket's directory),
// an absolute socket path, or a loopback host:port. group needs an absolute
// path. Anything else is refused, naming --web-listen (D3).
func ParseWebListen(value, group, daemonSocket string) (WebListen, error) {
	if value == "" {
		return WebListen{}, errors.New("--web-listen is empty: give unix, an absolute socket path, or a loopback host:port (docs/one-daemon/design.md D3)")
	}
	if value == "unix" {
		if group != "" {
			return WebListen{}, errors.New("--web-group needs --web-listen to be an absolute socket path: the daemon directory is private, so the group could not reach web.sock there")
		}
		return WebListen{Network: "unix", Address: filepath.Join(filepath.Dir(daemonSocket), webSocketName)}, nil
	}
	if strings.HasPrefix(value, "/") {
		return WebListen{Network: "unix", Address: value, Group: group}, nil
	}
	if err := CheckLoopback("--web-listen", value); err != nil {
		return WebListen{}, err
	}
	if group != "" {
		return WebListen{}, errors.New("--web-group is for a unix --web-listen")
	}
	return WebListen{Network: "tcp", Address: value}, nil
}

// CheckLoopback refuses an addr whose host is not a loopback address or
// localhost, naming setting.
func CheckLoopback(setting, addr string) error {
	refusal := func() error {
		return fmt.Errorf("%s %s: the daemon listens on a unix socket or a loopback address only; put a proxy in front of it (docs/one-daemon/design.md D3)", setting, addr)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return refusal()
	}
	// An empty host binds every interface, which is the opposite of a
	// loopback listener.
	if host == "" {
		return refusal()
	}
	if host == "localhost" {
		return nil
	}
	// A name is not resolved here: only an address literal can be shown
	// to be loopback without asking a resolver.
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return refusal()
}

// ListenWeb opens w. A unix socket replaces a stale socket but refuses a live
// one, as ListenLocal does. It is made 0600, or 0660 in Group.
func ListenWeb(w WebListen) (net.Listener, error) {
	switch w.Network {
	case "unix":
		return listenWebUnix(w)
	case "tcp":
		return net.Listen("tcp", w.Address)
	default:
		return nil, fmt.Errorf("bridge: web listener network %q is neither unix nor tcp", w.Network)
	}
}

// listenWebUnix binds w's socket 0600, or 0660 in w.Group after chowning it to
// that group. Closing the listener removes the socket.
func listenWebUnix(w WebListen) (net.Listener, error) {
	// The daemon's own socket has made this directory by now, so this
	// checks it rather than creating one: a directory shared with a group
	// is the daemon's decision to make, not the web listener's.
	if err := checkWebSocketDir(filepath.Dir(w.Address)); err != nil {
		return nil, err
	}
	unix, err := openUnixSocket(w.Address)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o600)
	if w.Group != "" {
		gid, err := webGroupGID(w.Group)
		if err != nil {
			_ = unix.Close()
			return nil, err
		}
		// Only the owner may drive the daemon; the group may connect.
		if err := os.Lchown(w.Address, -1, gid); err != nil {
			_ = unix.Close()
			return nil, fmt.Errorf("bridge: share socket %s with group %q: %w", w.Address, w.Group, err)
		}
		mode = 0o660
	}
	if err := os.Chmod(w.Address, mode); err != nil {
		_ = unix.Close()
		return nil, fmt.Errorf("bridge: restrict socket %s: %w", w.Address, err)
	}
	return unix, nil
}

// webGroupGID resolves group's gid, naming it in the refusal.
func webGroupGID(group string) (int, error) {
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, fmt.Errorf("bridge: web group %q: %w", group, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("bridge: web group %q: gid %q is not a number: %w", group, g.Gid, err)
	}
	return gid, nil
}

// checkWebSocketDir checks that dir exists, belongs to the user, and that
// neither group nor other may write it. It creates nothing and changes no
// mode: a directory it refuses is left exactly as it was.
func checkWebSocketDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("bridge: inspect web socket directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("bridge: web socket directory %s is not a directory", dir)
	}
	if !dirOwnedBy(fi, os.Getuid()) {
		return fmt.Errorf("bridge: web socket directory %s belongs to another user", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("bridge: web socket directory %s is writable by group or other (mode %04o)", dir, perm)
	}
	return nil
}
