package bridge

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// localDialTimeout bounds the probe that decides whether a daemon
// already answers on a socket path. A live daemon accepts immediately;
// the timeout only exists so a wedged one cannot hang startup.
const localDialTimeout = time.Second

// ErrDaemonRunning is ListenLocal's refusal when a daemon already
// answers on the path.
var ErrDaemonRunning = errors.New("a daemon is already listening")

// DefaultDaemonDir is <data>/daemon, where <data> follows
// config.DataDir's rule: MARSHAL_DATA_DIR, else $XDG_DATA_HOME/marshal,
// else ~/.local/share/marshal (P4 D1).
//
// The rule is restated here rather than imported: web/ is a standalone
// ACP client that may not depend on marshal/internal
// (TestWebIsStdlibOnly).
func DefaultDaemonDir() (string, error) {
	if dir := os.Getenv("MARSHAL_DATA_DIR"); dir != "" {
		return filepath.Join(dir, "daemon"), nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "marshal", "daemon"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("bridge: find home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "marshal", "daemon"), nil
}

// ListenLocal listens on the unix socket at path for the local profile:
// a 0700 directory of the user's own, no live daemon on the path, a
// stale socket removed, the socket 0600 (decision 5). Closing the
// listener removes the socket.
func ListenLocal(path string) (net.Listener, error) {
	if err := prepareLocalDir(filepath.Dir(path)); err != nil {
		return nil, err
	}

	// A daemon that still answers owns the path. Refuse and touch
	// nothing: taking over a live daemon would strand its sessions.
	if conn, err := net.DialTimeout("unix", path, localDialTimeout); err == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%w on %s", ErrDaemonRunning, path)
	}

	// Nothing answers. A socket file left by a crashed generation would
	// make Listen fail with EADDRINUSE even though it is dead, so remove
	// it. Anything that is not a socket is somebody's file, not ours to
	// delete.
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("bridge: %s exists and is not a socket; refusing to remove it", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("bridge: remove stale socket %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("bridge: inspect %s: %w", path, err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		// A path over the platform's limit fails here with the system's
		// own error; the path is named, the reason is not rewritten.
		return nil, fmt.Errorf("bridge: listen on %s: %w", path, err)
	}
	// Only the owner may drive the daemon.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("bridge: restrict socket %s: %w", path, err)
	}
	unix, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		return nil, fmt.Errorf("bridge: %s did not yield a unix socket listener", path)
	}
	unix.SetUnlinkOnClose(true)
	return unix, nil
}

// prepareLocalDir creates the socket's directory 0700, or checks that an
// existing one is a directory of the user's own that no group or other
// user may reach. It never changes the mode of a directory it did not
// create: a directory it refuses is left exactly as it was.
func prepareLocalDir(dir string) error {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("bridge: create socket directory %s: %w", dir, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("bridge: inspect socket directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("bridge: socket directory %s is not a directory", dir)
	}
	if !dirOwnedBy(fi, os.Getuid()) {
		return fmt.Errorf("bridge: socket directory %s belongs to another user", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("bridge: socket directory %s is open to other users (mode %04o, want 0700)", dir, perm)
	}
	return nil
}

// PeerChecked wraps ln so Accept returns only connections whose peer
// runs as uid; any other, or one whose peer cannot be read, is closed
// and logged (decision 6).
func PeerChecked(ln net.Listener, uid int, log *slog.Logger) net.Listener {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &peerCheckedListener{Listener: ln, uid: uid, log: log}
}

// peerCheckedListener is the wrapped listener itself in every other
// respect: Close and Addr are the wrapped listener's, and a failed
// Accept is returned unchanged.
type peerCheckedListener struct {
	net.Listener
	uid int
	log *slog.Logger
}

func (l *peerCheckedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peer, err := peerUID(conn)
		if err != nil {
			// Refusing is the safe answer: an unreadable peer is not a
			// peer we can vouch for.
			_ = conn.Close()
			l.log.Warn("local connection refused: cannot read its peer credentials", "err", err)
			continue
		}
		if peer != l.uid {
			_ = conn.Close()
			l.log.Warn("local connection refused: it belongs to another user", "uid", peer)
			continue
		}
		return conn, nil
	}
}
