// Package daemon installs and drives the local daemon (P4 D1): its paths,
// its service unit, the commands that manage it, and the client for its
// socket.
package daemon

import (
	"path/filepath"
	"time"

	"marshal/internal/app/config"
)

const (
	// Label is the launchd agent's label; Unit is the systemd unit's name.
	Label = "dev.marshal.daemon"
	Unit  = "marshal-daemon.service"
)

// Paths are the files the daemon uses, all under config.DaemonDir.
type Paths struct {
	Dir, Socket, StateDir, Registry, Log string
	// Token holds the Studio's bearer token: created 0600 by the bridge
	// on first start and read by `marshal studio` to open the browser on
	// the same credential, so nobody types a secret.
	Token string
}

// PathsFor resolves Paths for home.
func PathsFor(home string) Paths {
	dir := config.DaemonDir(home)
	return Paths{
		Dir:      dir,
		Socket:   config.DaemonSocketPath(home),
		StateDir: filepath.Join(dir, "state"),
		Registry: filepath.Join(dir, "fleet.json"),
		Log:      filepath.Join(dir, "webbridge.log"),
		Token:    filepath.Join(dir, "studio.token"),
	}
}

// UnitSpec is what a service unit runs.
type UnitSpec struct {
	Webbridge          string // absolute path of the webbridge binary
	Marshal            string // absolute path of the marshal binary
	Paths              Paths
	Env                map[string]string // PATH and the location variables that were set
	IdleSessionTimeout time.Duration     // written as --idle-session-timeout
	// StudioAddr is the loopback address the Studio is served on, and
	// empty means it is not served at all. Set, the daemon answers the
	// Studio's HTTP API itself, so the browser drives this daemon's own
	// host-child agents rather than a separate container fleet: no image
	// and no workspace build stands between the page and a spawn.
	StudioAddr string
}

// Args are the daemon's arguments, in the order decision 10 gives.
func (u UnitSpec) Args() []string {
	args := []string{
		"--profile", "local",
		"--socket", u.Paths.Socket,
		"--state-dir", u.Paths.StateDir,
		"--workspace", u.Paths.Registry,
	}
	if u.StudioAddr == "" {
		args = append(args, "--addr", "off")
	} else {
		args = append(args, "--addr", u.StudioAddr, "--token-file", u.Paths.Token)
	}
	return append(args,
		"--marshal-bin", u.Marshal,
		"--idle-session-timeout", u.IdleSessionTimeout.String(),
		// The installed daemon always supervises a refresh helper, so the
		// flag is written rather than configured (decision 5). It comes
		// last: it is the one argument that is not a value the user set.
		"--oauth-refresh",
	)
}
