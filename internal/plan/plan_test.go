package plan

import (
	"testing"
	"time"

	"github.com/alecpullen/marshal/internal/session"
)

func TestPlan_CalculateProgress(t *testing.T) {
	p := &Plan{
		Goals: []*Goal{
			{ID: "1", Status: GoalCompleted},
			{ID: "2", Status: GoalCompleted},
			{ID: "3", Status: GoalPending},
			{ID: "4", Status: GoalActive},
			{ID: "5", Status: GoalFailed},
			{ID: "6", Status: GoalSkipped},
		},
	}

	prog := p.CalculateProgress()
	if prog.Total != 6 {
		t.Errorf("expected Total=6, got %d", prog.Total)
	}
	if prog.Completed != 2 {
		t.Errorf("expected Completed=2, got %d", prog.Completed)
	}
	if prog.Pending != 1 {
		t.Errorf("expected Pending=1, got %d", prog.Pending)
	}
	if prog.Active != 1 {
		t.Errorf("expected Active=1, got %d", prog.Active)
	}
	if prog.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", prog.Failed)
	}
	if prog.Skipped != 1 {
		t.Errorf("expected Skipped=1, got %d", prog.Skipped)
	}
}

func TestPlan_IsComplete(t *testing.T) {
	tests := []struct {
		name     string
		goals    []*Goal
		expected bool
	}{
		{
			name:     "all completed",
			goals:    []*Goal{{Status: GoalCompleted}, {Status: GoalCompleted}},
			expected: true,
		},
		{
			name:     "one pending",
			goals:    []*Goal{{Status: GoalCompleted}, {Status: GoalPending}},
			expected: false,
		},
		{
			name:     "one active",
			goals:    []*Goal{{Status: GoalCompleted}, {Status: GoalActive}},
			expected: false,
		},
		{
			name:     "empty plan",
			goals:    []*Goal{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Plan{Goals: tt.goals}
			if got := p.IsComplete(); got != tt.expected {
				t.Errorf("IsComplete() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestPlan_NextGoal(t *testing.T) {
	p := &Plan{
		Goals: []*Goal{
			{ID: "1", Status: GoalCompleted},
			{ID: "2", Status: GoalPending, DependsOn: []string{"1"}},
			{ID: "3", Status: GoalPending, DependsOn: []string{"2"}},
		},
	}

	// First next should be goal 2 (goal 1 is completed)
	next := p.NextGoal()
	if next == nil || next.ID != "2" {
		t.Errorf("expected next goal to be '2', got %v", next)
	}

	// Mark goal 2 as completed
	p.Goals[1].Status = GoalCompleted

	// Now next should be goal 3
	next = p.NextGoal()
	if next == nil || next.ID != "3" {
		t.Errorf("expected next goal to be '3', got %v", next)
	}
}

func TestParseAuditFindings(t *testing.T) {
	auditOutput := `
## Security Audit Findings

**CRITICAL**: No Authentication
**File**: 
The system lacks authentication.

**HIGH**: SQL Injection
**File**: 
User input is not sanitized.

### 1. **Information Leakage**
Some info is leaked.

### 2. **Logging Issue**
Logs are insufficient.
`

	findings := ParseAuditFindings(auditOutput)

	// Should find at least the findings we explicitly listed
	if len(findings) == 0 {
		t.Error("expected to find findings, got none")
	}

	// Check for critical finding
	foundCritical := false
	for _, f := range findings {
		if f.Severity == PriorityCritical {
			foundCritical = true
			break
		}
	}
	if !foundCritical {
		t.Errorf("expected to find a critical severity finding, got: %v", findings)
	}
}

func TestGetGoalPrompt(t *testing.T) {
	g := &Goal{
		Title:       "Fix auth bug",
		Description: "Add authentication middleware",
		Priority:    PriorityCritical,
		Files:       []string{"auth.ts", "middleware.ts"},
		Findings: []Finding{
			{Severity: PriorityCritical, Title: "No auth", Description: "Missing auth"},
		},
	}

	prompt := GetGoalPrompt(g)

	// Check that prompt contains key elements
	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if !contains(prompt, "Fix auth bug") {
		t.Error("expected prompt to contain goal title")
	}
	if !contains(prompt, "auth.ts") {
		t.Error("expected prompt to contain related files")
	}
}

func TestFormatPlanStatus(t *testing.T) {
	p := &Plan{
		Name: "Test Plan",
		Goals: []*Goal{
			{Status: GoalCompleted},
			{Status: GoalPending},
		},
	}

	status := FormatPlanStatus(p)
	if status == "" {
		t.Error("expected non-empty status")
	}
	if !contains(status, "Test Plan") {
		t.Error("expected status to contain plan name")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstr(s, substr)))
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Mock store for testing
type mockStore struct {
	plans []*session.Plan
	goals []*session.Goal
}

func (m *mockStore) InsertPlan(p *session.Plan) error {
	m.plans = append(m.plans, p)
	return nil
}

func (m *mockStore) InsertGoal(g *session.Goal) error {
	m.goals = append(m.goals, g)
	return nil
}

func (m *mockStore) UpdateGoalStatus(goalID string, status string, taskID *string) error {
	for _, g := range m.goals {
		if g.ID == goalID {
			g.Status = status
			now := time.Now()
			if status == "active" {
				g.StartedAt = &now
			}
			if status == "completed" || status == "failed" {
				g.CompletedAt = &now
			}
			break
		}
	}
	return nil
}

func (m *mockStore) GetActivePlanForSession(sessionID string) (*session.Plan, error) {
	for _, p := range m.plans {
		if p.SessionID == sessionID {
			return p, nil
		}
	}
	return nil, nil
}

func (m *mockStore) GetGoalsForPlan(planID string) ([]*session.Goal, error) {
	var result []*session.Goal
	for _, g := range m.goals {
		if g.PlanID == planID {
			result = append(result, g)
		}
	}
	return result, nil
}

func (m *mockStore) GetPendingGoalsCount(planID string) (int, error) {
	count := 0
	for _, g := range m.goals {
		if g.PlanID == planID && (g.Status == "pending" || g.Status == "active") {
			count++
		}
	}
	return count, nil
}
