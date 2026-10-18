package session

// EffortOverride is the session's unsaved reasoning-effort override.
type EffortOverride struct {
	Level    string
	Provider string
	Model    string
}

// EffortChange describes an effort override or its application.
type EffortChange struct {
	Effective   string
	Pending     string
	AppliesFrom string
}

// When an effort change takes effect.
const (
	EffortAppliesNextStep = "next_step"
	EffortAppliesNextTurn = "next_turn"
)

// EffortOverride returns the session's effort override.
func (s *State) EffortOverride() EffortOverride {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effortOverride
}

// SetEffortOverride replaces the session's effort override.
func (s *State) SetEffortOverride(o EffortOverride) {
	s.mu.Lock()
	s.effortOverride = o
	s.mu.Unlock()
}

// TurnRoute returns the route the running turn uses; ok is false between turns.
func (s *State) TurnRoute() (RouteInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnRoute, s.turnRouteSet
}

// SetTurnRoute records the route the running turn uses.
func (s *State) SetTurnRoute(r RouteInfo) {
	s.mu.Lock()
	s.turnRoute, s.turnRouteSet = r, true
	s.mu.Unlock()
}

// ClearTurnRoute marks that no turn is running.
func (s *State) ClearTurnRoute() {
	s.mu.Lock()
	s.turnRoute, s.turnRouteSet = RouteInfo{}, false
	s.mu.Unlock()
}

// PublishEffortChange broadcasts an effort override or application.
func (s *State) PublishEffortChange(c EffortChange) {
	s.publishEvent(EventEffortChanged, Event{Effort: &c})
}
