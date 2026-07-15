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

// TestEstimateMeterIsDefault verifies the active default meter is
// EstimateMeter (ProviderUsageMeter is removed).
func TestEstimateMeterIsDefault(t *testing.T) {
	o := &Orchestrator{}
	m := o.newMeter()
	if _, ok := m.(*EstimateMeter); !ok {
		t.Errorf("default meter = %T, want *EstimateMeter", m)
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
