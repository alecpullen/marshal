package sdd

import (
	"context"
	"fmt"

	"marshal/internal/agent/swarm"
	"marshal/internal/llm/routing"
)

// DeterministicFix applies the deterministic fix for the given error kind
// per the escalation matrix (spec §15). Returns nil if the fix is a no-op
// (the controller handles it inline), or an error if the kind is unknown or
// has no deterministic fix (human escalation).
func DeterministicFix(kind string, ws *Workspace, dag *DAG, rs *RepoState, git GitOps, progress *Progress, newBranch string) error {
	switch kind {
	case "WRONG_BASE":
		if newBranch == "" {
			return fmt.Errorf("sdd escalation: WRONG_BASE requires a new branch name")
		}
		return BranchRebranch(git, rs, newBranch)
	case "STALE_HEAD":
		return rs.Repair(ws, git)
	case "DIRTY_MAIN_REPO":
		return fmt.Errorf("sdd escalation: DIRTY_MAIN_REPO has no deterministic fix; human escalation required")
	case "WORKTREE_HAS_COMMITS":
		return nil // don't recreate worktree; controller uses the existing one
	case "REVIEW_MALFORMED", "AUDIT_MALFORMED":
		return nil // controller re-runs NormalizeReport + retries; no-op here
	case "ALLOWED_FILES_VIOLATION":
		return nil // controller expands contract Allowed Files; no-op here
	default:
		return fmt.Errorf("sdd escalation: unknown kind %q", kind)
	}
}

// DispatchRescue assembles the rescue bundle (P3's AssembleRescueBundle),
// builds a fresh Runner for RoleSDDRescue with ScopeReadOnly, calls RunTask
// with a rescue dispatch prompt, reads the rescue report from
// <ws.Dir()>/reports/orchestrator-rescue.md, and returns it.
func DispatchRescue(ctx context.Context, factory swarm.RunnerFactory, ws *Workspace, progress *Progress) (*Report, error) {
	bundlePath, err := AssembleRescueBundle(ws)
	if err != nil {
		return nil, fmt.Errorf("sdd rescue: assemble bundle: %w", err)
	}
	runner, err := factory(routing.RoleSDDRescue, swarm.ScopeReadOnly)
	if err != nil {
		return nil, fmt.Errorf("sdd rescue: build runner: %w", err)
	}
	prompt := fmt.Sprintf("Diagnose why the SDD orchestrator is stuck. Read the rescue bundle at %s. Recommend exactly ONE corrective action. Write your report to reports/orchestrator-rescue.md with status: NEEDS_HUMAN | DONE on the first line.", bundlePath)
	if _, err := runner.RunTask(ctx, prompt); err != nil {
		return nil, fmt.Errorf("sdd rescue: RunTask: %w", err)
	}
	rep, err := ReadReport(ws, "orchestrator-rescue", "")
	if err != nil {
		return nil, fmt.Errorf("sdd rescue: read report: %w", err)
	}
	_ = progress.Append(ws, "ORCHESTRATOR", "RESCUE", "status", string(rep.Status))
	return rep, nil
}
