package session

import "time"

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
	// Tokens is this role's token spend, attributed by the swarm runtime.
	Tokens int
	// StartedAt is when the role first became active, for elapsed-time
	// display. Zero until then.
	StartedAt time.Time
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
			if status == SwarmRoleActive && s.swarmProgress.Roles[i].StartedAt.IsZero() {
				s.swarmProgress.Roles[i].StartedAt = time.Now()
			}
			return
		}
	}
}

// UpdateSwarmRoleTokens attributes token spend to one role by name.
// A no-op when the role is not present.
func (s *State) UpdateSwarmRoleTokens(name string, tokens int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.swarmProgress.Roles {
		if s.swarmProgress.Roles[i].Name == name {
			s.swarmProgress.Roles[i].Tokens = tokens
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
