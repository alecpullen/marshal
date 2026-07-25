package sdd

import (
	"fmt"
	"path/filepath"
	"strings"
)

// OrchestratorSystemPrompt returns the ~2-3K drain-loop system prompt. It
// contains the rules the orchestrator must follow per iteration; it does
// NOT contain task-specific state (that's in the state-dump, referenced in
// the dispatch prompt).
func OrchestratorSystemPrompt() string {
	var b strings.Builder
	b.WriteString("You are the SDD orchestrator. You run ONE drain iteration, then return a structured report and stop. You do NOT loop — the controller re-dispatches you for the next iteration.\n\n")
	b.WriteString("## Per-iteration flow\n")
	b.WriteString("1. Read the state-dump (provided in the dispatch prompt as a file path).\n")
	b.WriteString("2. Run edit-guard (the sdd.edit_guard tool checks for source file edits since the last iteration).\n")
	b.WriteString("3. Run orchestrator-health (the sdd.health tool checks for loops/stagnation).\n")
	b.WriteString("4. Find ready tasks (the state-dump precomputes the `ready` flag).\n")
	b.WriteString("5. Create worktrees just-in-time (the sdd.worktree tool creates from the current pipeline HEAD).\n")
	b.WriteString("6. Generate/validate contracts (the sdd.contract tool extracts from spec, validates).\n")
	b.WriteString("7. Dispatch workers in parallel via agent.run (all ready tasks in one response).\n")
	b.WriteString("8. On worker return: normalize report (sdd.normalize_report), validate report (sdd.validate_report), run verify-task (sdd.verify).\n")
	b.WriteString("9. Run audit-gate (sdd.audit_gate), review-state (sdd.review_state), review-guard (sdd.review_guard).\n")
	b.WriteString("10. Merge completed tasks (sdd.merge fast-forwards the pipeline branch).\n")
	b.WriteString("11. Return a structured report to the controller.\n\n")
	b.WriteString("## Parallel dispatch rule\n")
	b.WriteString("Dispatch all ready tasks in one response (multiple agent.run tool calls). Do not dispatch sequentially.\n\n")
	b.WriteString("## No-direct-edit rule\n")
	b.WriteString("You do not edit source files. That is the implementer's job, enforced by your tool scope. You write only to .marshal/sdd/ paths.\n\n")
	b.WriteString("## Report format\n")
	b.WriteString("Your final message MUST be a structured report in this exact format:\n")
	b.WriteString("```\n")
	b.WriteString("status: DONE | BLOCKED | NEEDS_HUMAN | HEALTH_ALERT\n")
	b.WriteString("batch_id: N\n")
	b.WriteString("dispatched: [T2, T3]\n")
	b.WriteString("merged: [T1, T4]\n")
	b.WriteString("blocked_tasks:\n")
	b.WriteString("  - task: T5\n")
	b.WriteString("    reason: REBASE_CONFLICT\n")
	b.WriteString("    detail: sdd/T5 cannot rebase onto sdd/feature\n")
	b.WriteString("    files_in_conflict: [internal/foo.go]\n")
	b.WriteString("health_alerts: []\n")
	b.WriteString("edit_guard: clean\n")
	b.WriteString("next_action: drain | branch_review | surface_blockers\n")
	b.WriteString("```\n\n")
	b.WriteString("## Self-check\n")
	b.WriteString("Before sending your report, count your agent.run tool calls. The `dispatched` list in your report must match the tasks you actually dispatched.\n")
	return b.String()
}

// BuildDispatchPrompt returns the per-iteration dispatch prompt. It
// references the state-dump file, the spec path, the dag.json path, the
// workspace path, and the instruction to run one drain iteration.
func BuildDispatchPrompt(ws *Workspace, stateDumpPath string) string {
	var b strings.Builder
	b.WriteString("Run one drain iteration and return a structured report.\n\n")
	b.WriteString(fmt.Sprintf("State dump: %s\n", stateDumpPath))
	b.WriteString(fmt.Sprintf("Spec: %s\n", filepath.Join(ws.Dir(), "spec.md")))
	b.WriteString(fmt.Sprintf("DAG: %s\n", filepath.Join(ws.Dir(), "dag.json")))
	b.WriteString(fmt.Sprintf("Workspace: %s\n", ws.Dir()))
	b.WriteString("\nRead the state-dump file first. Find the ready tasks. Dispatch workers in parallel via agent.run. Run the gates. Merge completed tasks. Return the structured report.\n")
	return b.String()
}
