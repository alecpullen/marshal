package daemon

// StatusSchema is the version of the daemon status document. A consumer
// refuses a version it does not know.
const StatusSchema = 1

// Status is the `marshal daemon status --json` document. Linger is always
// present, with Supported false on macOS, so a consumer has one code path.
type Status struct {
	SchemaVersion int          `json:"schemaVersion"`
	Answering     bool         `json:"answering"`
	Service       ServiceState `json:"service"`
	Linger        LingerState  `json:"linger"`
	Daemon        *Info        `json:"daemon,omitempty"`
}
