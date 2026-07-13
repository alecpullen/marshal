package sdd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// scriptedFactory returns canned summaries per role via the Runner's
// RunTaskFunc override. The implementer returns "DONE" with a commit;
// the reviewer returns a clean verdict.
func scriptedFactory(summaries map[agent.AgentRole]string) RunnerFactory {
	return func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		summary := summaries[role]
		r.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: summary}, nil
		}
		return r, nil
	}
}

func writePlan(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

const twoTaskPlan = `# Test Plan Implementation Plan

## Global Constraints

- Go 1.24+
- TDD

---

### Task 1: First task

Implement the first thing.

### Task 2: Second task

Implement the second thing.
`

func TestOrchestratorRunsAllTasks(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	planPath := writePlan(t, t.TempDir(), "plan.md", twoTaskPlan)
	factory := scriptedFactory(map[agent.AgentRole]string{
		agent.RoleSDDImplementer:    "DONE\ncommits: abc1234\ntests: 5/5 passing",
		agent.RoleSDDReviewer:       "### Spec Compliance\n- ✅ Spec compliant\n\n### Assessment\n**Task quality:** Approved",
		agent.RoleSDDBranchReviewer: "### Branch Verdict\n- ✅ Ready to merge",
	})
	o := New(state, factory, config.SDDConfig{MaxFixRounds: 3})
	if err := o.Run(context.Background(), planPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p := state.SDDProgress()
	// After completion, progress should be cleared (via defer).
	if p.Active {
		t.Error("SDDProgress should be cleared after completion")
	}
}

func TestOrchestratorResumesFromLedger(t *testing.T) {
	workDir := t.TempDir()
	state := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	planPath := writePlan(t, workDir, "plan.md", twoTaskPlan)

	// Pre-populate the ledger with Task 1 complete.
	ws, _ := NewWorkspace(workDir)
	ws.Ensure()
	ledger := NewLedger(ws)
	ledger.Append(LedgerEntry{TaskNumber: 1, BaseSHA: "aaa", HeadSHA: "bbb"})

	factory := scriptedFactory(map[agent.AgentRole]string{
		agent.RoleSDDImplementer:    "DONE\ncommits: ccc1234\ntests: 3/3 passing",
		agent.RoleSDDReviewer:       "### Spec Compliance\n- ✅ Spec compliant\n\n### Assessment\n**Task quality:** Approved",
		agent.RoleSDDBranchReviewer: "### Branch Verdict\n- ✅ Ready to merge",
	})
	o := New(state, factory, config.SDDConfig{MaxFixRounds: 3})
	if err := o.Run(context.Background(), planPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Task 1 should have been skipped — only Task 2 dispatched.
	// The ledger should now have 2 entries (1 pre-existing + 1 new).
	entries := ledger.Read()
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2 (1 pre-existing + 1 new)", len(entries))
	}
}

func TestOrchestratorSetForceClassAndPolicyRulesAreNoOps(t *testing.T) {
	o := New(session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}),
		scriptedFactory(nil), config.SDDConfig{})
	o.SetForceClass("question")
	o.SetPolicyRules(nil)
}
