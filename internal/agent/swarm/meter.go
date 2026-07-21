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

// EstimateMeter is the default TokenMeter: it sums whatever prompt and
// completion counts it is given. The orchestrator feeds it real
// provider-reported usage via Runner.UsageObserver when the provider
// surfaces usage, and EstimateText-derived estimates when it does not.
// It is self-contained and behaves identically across all providers.
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
