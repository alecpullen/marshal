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
	}
}

// UnitSpec is what a service unit runs.
type UnitSpec struct {
	Webbridge          string // absolute path of the webbridge binary
	Marshal            string // absolute path of the marshal binary
	Paths              Paths
	Env                map[string]string // PATH and the location variables that were set
	IdleSessionTimeout time.Duration     // written as --idle-session-timeout
}

// Args are the daemon's arguments, in the order decision 10 gives.
func (u UnitSpec) Args() []string {
	return []string{
		"--profile", "local",
		"--socket", u.Paths.Socket,
		"--state-dir", u.Paths.StateDir,
		"--workspace", u.Paths.Registry,
		"--addr", "off",
		"--marshal-bin", u.Marshal,
		"--idle-session-timeout", u.IdleSessionTimeout.String(),
	}
}
