package sdd

import (
	"context"
	"fmt"
	"strconv"

	"marshal/internal/agent/swarm"
	"marshal/internal/llm/routing"
)

// Orchestrator dispatches the SDD orchestrator LLM for one drain iteration
// per call. It holds the RunnerFactory seam (swarm.RunnerFactory) and the
// workspace/progress. Each Dispatch call builds a fresh Runner (no replayed
// history), writes the state-dump, runs RunTask, and parses the structured
// report from task.Summary.
type Orchestrator struct {
	Factory  swarm.RunnerFactory
	WS       *Workspace
	Progress *Progress
}

// NewOrchestrator constructs an Orchestrator bound to the given factory,
// workspace, and progress logger.
func NewOrchestrator(factory swarm.RunnerFactory, ws *Workspace, progress *Progress) *Orchestrator {
	return &Orchestrator{Factory: factory, WS: ws, Progress: progress}
}

// Dispatch runs one drain iteration. It writes the state-dump, builds a
// fresh Runner for RoleSDDOrchestrator with ScopeFull, calls RunTask with
// the dispatch prompt, parses the structured report from task.Summary, logs
// the dispatch to progress, and returns the report.
func (o *Orchestrator) Dispatch(ctx context.Context, dag *DAG, rs *RepoState, pipelineBranch string, lastBatchID int) (*OrchestratorReport, error) {
	stateDumpPath, err := WriteStateDump(o.WS, dag, rs, lastBatchID)
	if err != nil {
		return nil, fmt.Errorf("sdd orchestrator: write state-dump: %w", err)
	}
	runner, err := o.Factory(routing.RoleSDDOrchestrator, swarm.ScopeFull)
	if err != nil {
		return nil, fmt.Errorf("sdd orchestrator: build runner: %w", err)
	}
	dispatchPrompt := BuildDispatchPrompt(o.WS, stateDumpPath)
	task, err := runner.RunTask(ctx, dispatchPrompt)
	if err != nil {
		return nil, fmt.Errorf("sdd orchestrator: RunTask: %w", err)
	}
	report, err := ParseOrchestratorReport(task.Summary)
	if err != nil {
		return nil, fmt.Errorf("sdd orchestrator: parse report: %w", err)
	}
	_ = o.Progress.Append(o.WS, "ORCHESTRATOR", "DISPATCH_BATCH",
		"batch_id", strconv.Itoa(report.BatchID),
		"status", string(report.Status))
	return report, nil
}
