package session

// SwarmRoleStatus is the lifecycle state of one role in a swarm run.
type SwarmRoleStatus string

const (
	SwarmRolePending SwarmRoleStatus = "pending"
	SwarmRoleActive  SwarmRoleStatus = "active"
	SwarmRoleDone    SwarmRoleStatus = "done"
	SwarmRoleFailed  SwarmRoleStatus = "failed"
)

// SwarmRole is one row in the swarm roster panel.
type SwarmRole struct {
	Name   string
	Status SwarmRoleStatus
	Detail string
}

// SwarmProgress is the live state of a swarm run.
type SwarmProgress struct {
	Goal       string
	Active     bool
	Roles      []SwarmRole
	TokensUsed int
	TokensMax  int
}

func (p SwarmProgress) clone() SwarmProgress {
	out := p
	out.Roles = append([]SwarmRole(nil), p.Roles...)
	return out
}

func (s *State) SetSwarmProgress(p SwarmProgress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmProgress = p.clone()
}

func (s *State) SwarmProgress() SwarmProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.swarmProgress.clone()
}

func (s *State) UpdateSwarmRole(name string, status SwarmRoleStatus, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.swarmProgress.Roles {
		if s.swarmProgress.Roles[i].Name == name {
			s.swarmProgress.Roles[i].Status = status
			s.swarmProgress.Roles[i].Detail = detail
			return
		}
	}
}

func (s *State) UpdateSwarmTokens(used, max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmProgress.TokensUsed = used
	s.swarmProgress.TokensMax = max
}

func (s *State) ClearSwarmProgress() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.swarmProgress = SwarmProgress{}
}
