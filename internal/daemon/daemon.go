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
	// JoinToken holds the secret a joining daemon presents to its relay,
	// stored 0600 by `marshal daemon join-token` and written into the unit
	// as --join-token-file (P8 D12).
	JoinToken string
}

// PathsFor resolves Paths for home.
func PathsFor(home string) Paths {
	dir := config.DaemonDir(home)
	return Paths{
		Dir:       dir,
		Socket:    config.DaemonSocketPath(home),
		StateDir:  filepath.Join(dir, "state"),
		Registry:  filepath.Join(dir, "fleet.json"),
		Log:       filepath.Join(dir, "webbridge.log"),
		Token:     filepath.Join(dir, "studio.token"),
		JoinToken: filepath.Join(dir, "join.token"),
	}
}

// UnitSpec is what a service unit runs.
type UnitSpec struct {
	Webbridge          string // absolute path of the webbridge binary
	Marshal            string // absolute path of the marshal binary
	Paths              Paths
	Env                map[string]string // PATH and the location variables that were set
	IdleSessionTimeout time.Duration     // written as --idle-session-timeout
	Egress             bool              // written as --egress on or --egress off
	// StudioAddr is the loopback address the Studio is served on, and
	// empty means it is not served at all. Set, the daemon answers the
	// Studio's HTTP API itself, so the browser drives this daemon's own
	// host-child agents rather than a separate container fleet: no image
	// and no workspace build stands between the page and a spawn.
	StudioAddr string
	// The web listener, and the origins it advertises (D3). Each is
	// written only when set: WebListen is `unix`, an absolute socket path
	// or a loopback host:port, WebGroup shares a unix listener with a
	// group, PublicURL is the origin the page and its API are reached on,
	// and PreviewListen/PreviewURL are where previews are served and the
	// origin they are reached on.
	WebListen     string // written as --web-listen
	WebGroup      string // written as --web-group
	PublicURL     string // written as --public-url
	PreviewListen string // written as --preview-addr
	PreviewURL    string // written as --preview-url
	// Join is the relay this daemon joins, and nil means it joins none
	// (P8 D12).
	Join *Join
}

// Join is the relay the daemon joins (P8 D12).
type Join struct{ URL, Name, CAFile string }

// Args are the daemon's arguments, in the order decision 10 gives.
func (u UnitSpec) Args() []string {
	args := []string{
		"--socket", u.Paths.Socket,
		"--state-dir", u.Paths.StateDir,
		"--workspace", u.Paths.Registry,
	}
	if u.Join != nil {
		args = append(args,
			"--join", u.Join.URL,
			"--join-name", u.Join.Name,
			"--join-token-file", u.Paths.JoinToken,
		)
		if u.Join.CAFile != "" {
			args = append(args, "--join-ca-file", u.Join.CAFile)
		}
	}
	if u.StudioAddr == "" {
		args = append(args, "--addr", "off")
	} else {
		args = append(args, "--addr", u.StudioAddr)
	}
	// The token file is the listener's credential: the Studio's own when one
	// is served, and the join's when the daemon joins a relay with no Studio
	// of its own (P8 D12).
	if u.StudioAddr != "" || u.Join != nil {
		args = append(args, "--token-file", u.Paths.Token)
	}
	// The egress proxy is written either way, so the installed unit says
	// whether its container agents get one; it is off unless asked for.
	egress := "off"
	if u.Egress {
		egress = "on"
	}
	args = append(args,
		"--marshal-bin", u.Marshal,
		"--idle-session-timeout", u.IdleSessionTimeout.String(),
		"--egress", egress,
	)
	// The web listener and the origins it advertises are values the user
	// sets, so each is written only when the unit holds one (D3). Nothing
	// sets them yet, so an installed unit keeps the argv it had.
	for _, flag := range []struct{ name, value string }{
		{"--web-listen", u.WebListen},
		{"--web-group", u.WebGroup},
		{"--public-url", u.PublicURL},
		{"--preview-addr", u.PreviewListen},
		{"--preview-url", u.PreviewURL},
	} {
		if flag.value != "" {
			args = append(args, flag.name, flag.value)
		}
	}
	// The installed daemon always supervises a refresh helper and a
	// tunnel helper, so both flags are written rather than configured
	// (decision 5). They come last: they are the arguments that are
	// not values the user set.
	return append(args,
		"--oauth-refresh",
		"--tunnels",
	)
}
