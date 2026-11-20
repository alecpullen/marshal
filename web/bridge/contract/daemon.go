package contract

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string `json:"version"`
	Profile   string `json:"profile"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"` // Unix milliseconds
	Socket    string `json:"socket"`
	// WebListen is the web listener's bound address ("unix:<path>" or
	// host:port), and WebError is why it bound nothing (D10).
	WebListen string `json:"webListen,omitempty"`
	WebError  string `json:"webError,omitempty"`
	// PreviewAddr is the preview listener's bound address (host:port), or ""
	// when previews are off.
	PreviewAddr string         `json:"previewAddr,omitempty"`
	Projects    []string       `json:"projects"`          // filled per request
	Pinned      []string       `json:"pinned"`            // filled per request
	Children    []DaemonChild  `json:"children"`          // filled per request
	Refresh     *DaemonRefresh `json:"refresh,omitempty"` // filled per request
	Hosts       []DaemonHost   `json:"hosts"`             // filled per request: the tunnel helper's last report
	Restart     *DaemonRestart `json:"restart,omitempty"` // the daemon's own; filled per request
	// StudioDir is the directory the Studio is served from, or "" when the
	// daemon found none (one-daemon D12).
	StudioDir string `json:"studioDir,omitempty"`
}

// PreviewOriginHeader carries the origin a caller reaches this daemon's
// preview listener through. A preview link is built from it, in place of the
// daemon's own preview port (one-daemon open question 1).
const PreviewOriginHeader = "X-Marshal-Preview-Origin"

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

// HostState is a remote host's tunnel state (one-daemon D7).
type HostState string

const (
	HostConnecting HostState = "connecting" // before the first attempt has an outcome
	HostLive       HostState = "live"       // the tunnel answers GET /api/daemon
	HostDown       HostState = "down"       // the last attempt failed; LastError says why
)

// DaemonHost is one remote host's tunnel as GET /api/daemon reports it
// (one-daemon D7, D9).
type DaemonHost struct {
	Name   string    `json:"name"`
	SSH    string    `json:"ssh"` // the destination, as fleet.toml names it
	State  HostState `json:"state"`
	Socket string    `json:"socket"` // this machine's end of the tunnel
	// PreviewPort is this machine's loopback port forwarded to the host's
	// preview listener, or 0 when the host serves no previews.
	PreviewPort int    `json:"previewPort,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	Since       int64  `json:"since"` // Unix milliseconds: when State last changed
}

// DaemonHostsReport is PUT /api/daemon/hosts's body: every host the tunnel
// helper keeps, replacing its last report.
type DaemonHostsReport struct {
	Hosts []DaemonHost `json:"hosts"`
}

// HostList is GET /api/hosts's answer: the hosts this daemon keeps a tunnel
// to, as its tunnel helper last reported them (one-daemon D10).
type HostList struct {
	Hosts []DaemonHost `json:"hosts"`
}

// HostAdd is POST /api/hosts's body: a host to add to fleet.toml on this
// daemon's machine (one-daemon D15, plan 7).
type HostAdd struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh"`
	Marshal string `json:"marshal,omitempty"`
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
