package contract

// DaemonInfo is what GET /api/daemon answers about the local daemon.
type DaemonInfo struct {
	Version   string         `json:"version"`
	Profile   string         `json:"profile"`
	PID       int            `json:"pid"`
	StartedAt int64          `json:"startedAt"` // Unix milliseconds
	Socket    string         `json:"socket"`
	Projects  []string       `json:"projects"`          // filled per request
	Pinned    []string       `json:"pinned"`            // filled per request
	Children  []DaemonChild  `json:"children"`          // filled per request
	Refresh   *DaemonRefresh `json:"refresh,omitempty"` // filled per request
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
