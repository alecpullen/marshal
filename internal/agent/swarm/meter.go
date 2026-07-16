package swarm

import (
	"sync"

	"marshal/internal/agent"
	"marshal/internal/contextpack"
)

// TokenMeter accumulates token consumption across a swarm run so the
// orchestrator can enforce a whole-run token ceiling. Observe is called
// once per role turn. Implementations must be safe for concurrent use:
// parallel scouts observe from separate goroutines.
type TokenMeter interface {
	Observe(role agent.AgentRole, promptTokens, completionTokens int)
	Total() int
}

// EstimateText estimates the token count of s using the same heuristic the
// context-pack builder uses, so budget accounting is consistent with the
// rest of Marshal.
func EstimateText(s string) int {
	return contextpack.EstimateTokens(s)
}

// Provider-usage-backed meters are deferred to a later milestone:
// the provider layer does not yet surface real token-usage
// reporting. Until it does, EstimateMeter is the only default and
// all callers can be migrated without churn.
//
// EstimateMeter is the active default: it sums the prompt and completion
// token counts it is given (themselves derived from EstimateText). It is
// approximate but self-contained and identical across all local providers.
type EstimateMeter struct {
	mu    sync.Mutex
	total int
}

func NewEstimateMeter() *EstimateMeter { return &EstimateMeter{} }

func (m *EstimateMeter) Observe(_ agent.AgentRole, promptTokens, completionTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total += promptTokens + completionTokens
}

func (m *EstimateMeter) Total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}
