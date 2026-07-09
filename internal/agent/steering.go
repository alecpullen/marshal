package agent

// SteeringProvider supplies mid-turn steering messages drained at the
// runner's loop-top (F16 R4). The session.State implements this via
// DrainSteering; a nil provider means steering is disabled.
type SteeringProvider interface {
	DrainSteering() []string
}
