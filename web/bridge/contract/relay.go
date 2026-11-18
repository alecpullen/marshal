package contract

// The relay's host list (GET /api/relay/hosts) and the join a bridge makes
// into another bridge's relay, as GET /api/daemon reports it.

// RelayHosts is GET /api/relay/hosts (P8 D16): every host the relay names.
type RelayHosts struct {
	Hosts []RelayHost `json:"hosts"`
}

// RelayHost is one host a relay names, joined or not.
type RelayHost struct {
	Name        string `json:"name"`
	Connected   bool   `json:"connected"`
	Since       int64  `json:"since,omitempty"`       // Unix ms the current instance joined
	LastContact int64  `json:"lastContact,omitempty"` // Unix ms of the last join or forwarded response
}

// DaemonJoin is a joining bridge's join, as GET /api/daemon reports it (D17).
type DaemonJoin struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	LastError string `json:"lastError,omitempty"`
}
