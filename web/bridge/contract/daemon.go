package contract

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string `json:"version"`
	Profile   string `json:"profile"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"` // Unix milliseconds
	Socket    string `json:"socket"`
	// StudioAddr is the address the Studio listener actually bound, and
	// StudioError is why it bound nothing when it could not. `marshal
	// studio` reads both over the socket: the address is the truth (a
	// configured port 0 names its own once bound), and the error is what
	// makes a busy port a named failure instead of "nothing is serving".
	StudioAddr  string         `json:"studioAddr,omitempty"`
	StudioError string         `json:"studioError,omitempty"`
	Projects    []string       `json:"projects"`          // filled per request
	Pinned      []string       `json:"pinned"`            // filled per request
	Children    []DaemonChild  `json:"children"`          // filled per request
	Refresh     *DaemonRefresh `json:"refresh,omitempty"` // filled per request
	Restart     *DaemonRestart `json:"restart,omitempty"` // the daemon's own; filled per request
}

// DaemonRestartRequest is POST /api/daemon/restart's body (P6 B5).
type DaemonRestartRequest struct {
	Now bool `json:"now,omitempty"`
}

// DaemonRestartProgress is one line of POST /api/daemon/restart's answer:
// newline-delimited JSON, one line per sweep, ending with Restarting or Error.
type DaemonRestartProgress struct {
	WaitingFor []string `json:"waitingFor,omitempty"`
	Restarting bool     `json:"restarting,omitempty"`
	Version    string   `json:"version,omitempty"` // the build it restarts on
	Error      string   `json:"error,omitempty"`   // why it will not; the answer ends
}

// DaemonRefresh is the refresh helper as GET /api/daemon reports it.
type DaemonRefresh struct {
	PID      int `json:"pid"`
	Restarts int `json:"restarts"`
}

// DaemonChild is one project child as GET /api/daemon reports it.
type DaemonChild struct {
	Root       string          `json:"root"`
	Version    string          `json:"version"`
	EnvWarning string          `json:"envWarning,omitempty"`
	Pinned     bool            `json:"pinned,omitempty"`
	Sessions   []DaemonSession `json:"sessions"`
	Restart    *DaemonRestart  `json:"restart,omitempty"` // nil while the child is current
}

// DaemonRestart is a restart the daemon is holding, as GET /api/daemon
// reports it (P6 B4).
type DaemonRestart struct {
	Reasons    []string `json:"reasons"`
	WaitingFor string   `json:"waitingFor,omitempty"` // "" when nothing holds it
	Refused    string   `json:"refused,omitempty"`    // the new build's failure; "" when none
}

// DaemonSession is one tracked session as GET /api/daemon reports it.
type DaemonSession struct {
	ID         string `json:"id"`
	Viewers    int    `json:"viewers"`
	LastActive int64  `json:"lastActive"` // Unix milliseconds
}

// PinState is a project's pin state.
type PinState struct {
	Root    string `json:"root"`
	Pinned  bool   `json:"pinned"`
	Running bool   `json:"running"`
}
