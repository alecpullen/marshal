package pipeline

import "testing"

func TestOpStatusString(t *testing.T) {
	tests := []struct {
		status OpStatus
		want   string
	}{
		{OpExecutable, "executable"},
		{OpBlocked, "blocked"},
		{OpAgent, "agent"},
	}
	for _, tt := range tests {
		if got := string(tt.status); got != tt.want {
			t.Errorf("OpStatus(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStrategyConstants(t *testing.T) {
	if StrategyAgent != "agent" {
		t.Errorf("StrategyAgent = %q, want %q", StrategyAgent, "agent")
	}
	if StrategyAdaptive != "adaptive" {
		t.Errorf("StrategyAdaptive = %q, want %q", StrategyAdaptive, "adaptive")
	}
	if StrategyStrict != "strict" {
		t.Errorf("StrategyStrict = %q, want %q", StrategyStrict, "strict")
	}
}

func TestTaskIRDerivedStatusAllExecutable(t *testing.T) {
	task := TaskIR{
		Operations: []Operation{
			&PatchOp{Status: OpExecutable},
			&RunOp{Status: OpExecutable},
		},
	}
	if got := task.DerivedStatus(); got != TaskExecutable {
		t.Errorf("DerivedStatus = %q, want %q", got, TaskExecutable)
	}
}

func TestTaskIRDerivedStatusPartial(t *testing.T) {
	task := TaskIR{
		Operations: []Operation{
			&PatchOp{Status: OpExecutable},
			&AgentOp{Status: OpAgent},
		},
	}
	if got := task.DerivedStatus(); got != TaskPartial {
		t.Errorf("DerivedStatus = %q, want %q", got, TaskPartial)
	}
}

func TestTaskIRDerivedStatusAgentOnly(t *testing.T) {
	task := TaskIR{
		Operations: nil,
	}
	if got := task.DerivedStatus(); got != TaskAgentOnly {
		t.Errorf("DerivedStatus = %q, want %q", got, TaskAgentOnly)
	}
}

func TestTaskIRDerivedStatusBlocked(t *testing.T) {
	task := TaskIR{
		Operations: []Operation{
			&PatchOp{Status: OpBlocked},
		},
		FallbackAllowed: false,
	}
	if got := task.DerivedStatus(); got != TaskBlocked {
		t.Errorf("DerivedStatus = %q, want %q", got, TaskBlocked)
	}
}

func TestTaskIRDerivedStatusBlockedFallsToAgentWhenAllowed(t *testing.T) {
	task := TaskIR{
		Operations: []Operation{
			&PatchOp{Status: OpBlocked},
		},
		FallbackAllowed: true,
	}
	if got := task.DerivedStatus(); got != TaskPartial {
		t.Errorf("DerivedStatus with fallback = %q, want %q (partial, not blocked)", got, TaskPartial)
	}
}

func TestTaskIREstimatedModelCalls(t *testing.T) {
	task := TaskIR{
		Operations: []Operation{
			&PatchOp{Status: OpExecutable},
			&AgentOp{Status: OpAgent},
			&AgentOp{Status: OpAgent},
		},
	}
	if got := task.EstimatedModelCalls(); got != 1 {
		t.Errorf("EstimatedModelCalls = %d, want 1 (one dispatch for all agent ops)", got)
	}
}

func TestTaskIREstimatedModelCallsNone(t *testing.T) {
	task := TaskIR{
		Operations: []Operation{
			&PatchOp{Status: OpExecutable},
			&RunOp{Status: OpExecutable},
		},
	}
	if got := task.EstimatedModelCalls(); got != 0 {
		t.Errorf("EstimatedModelCalls = %d, want 0", got)
	}
}
