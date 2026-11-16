package acpwire

// The restart notification a bridge sends a viewer (P6).

// MethodRestarting is what the bridge tells a viewer before it closes the
// viewer's connection for a restart (B2, B4).
const MethodRestarting = "marshal/restarting"

// RestartingParams says why, and what comes next.
type RestartingParams struct {
	Reason  string `json:"reason"`
	Version string `json:"version,omitempty"` // the build the restart brings
}
