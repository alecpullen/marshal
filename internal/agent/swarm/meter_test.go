package swarm

import (
	"marshal/internal/agent"
	"testing"
)

func TestEstimateMeterAccumulates(t *testing.T) {
	m := NewEstimateMeter()
	if m.Total() != 0 {
		t.Fatalf("new meter Total = %d, want 0", m.Total())
	}
	m.Observe(agent.RolePlanner, 100, 50)
	m.Observe(agent.RoleImplementer, 200, 80)
	if got := m.Total(); got != 430 {
		t.Fatalf("Total = %d, want 430", got)
	}
}

func TestProviderUsageMeterIsDormantButUsable(t *testing.T) {
	var m TokenMeter = NewProviderUsageMeter()
	m.Observe(agent.RoleTester, 10, 5)
	if m.Total() != 15 {
		t.Fatalf("stub meter Total = %d, want 15 (delegates to estimate)", m.Total())
	}
}

func TestEstimateTextIsNonNegative(t *testing.T) {
	if EstimateText("") != 0 {
		t.Errorf("EstimateText(\"\") should be 0")
	}
	if EstimateText("some tokens here") <= 0 {
		t.Errorf("EstimateText of non-empty string should be > 0")
	}
}
