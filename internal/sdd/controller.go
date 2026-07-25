package sdd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/agent/swarm"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
)

// ControllerState is the closed set of states the controller's state machine
// transitions through (spec §3).
type ControllerState string

const (
	StateIdle            ControllerState = "idle"
	StateWorkspaceReset  ControllerState = "workspace_reset"
	StateSpecGate        ControllerState = "spec_gate"
	StateDecompose       ControllerState = "decompose"
	StateDrainIteration  ControllerState = "drain_iteration"
	StateDispatchWorkers ControllerState = "dispatch_workers"
	StateVerifyAudit     ControllerState = "verify_audit"
	StateReviewMerge     ControllerState = "review_merge"
	StateBranchReview    ControllerState = "branch_review"
	StateFinalMergeGate  ControllerState = "final_merge_gate"
	StateFinalize        ControllerState = "finalize"
	StateBlocked         ControllerState = "blocked"
)

// ErrHumanGateRequired is returned by Run when the controller reaches a
// human gate (spec approval, final merge, escalation) and must wait for the
// human to resolve. The TUI (P5) reads the gate state from SessionState and
// re-dispatches the controller after resolution.
var ErrHumanGateRequired = errors.New("sdd controller: human gate required")

// Controller is the deterministic Go state machine that owns all SDD
// strategy. It dispatches the orchestrator LLM for each drain iteration,
// runs the gates/guards, performs git operations, monitors health, and
// presents human gates. It does NOT edit source or make subjective quality
// judgments.
type Controller struct {
	WS             *Workspace
	Git            GitOps
	DAG            *DAG
	RepoState      *RepoState
	Progress       *Progress
	Orchestrator   *Orchestrator
	Factory        swarm.RunnerFactory
	RoutingConfig  routing.Config
	SessionState   *session.State
	PipelineBranch string
	TargetBranch   string
	PlanPath       string

	// RebuildFactory, when non-nil, is called by swapOrchestratorModel
	// to construct a new RunnerFactory from an overridden routing.Config
	// (model escalation). P5 wires this; P4 left it nil.
	RebuildFactory func(routing.Config) swarm.RunnerFactory

	// UsageSink, when non-nil, receives token counts from the factory's
	// UsageObserver. recordUsage flushes the accumulated count to the
	// session. P5 wires this.
	UsageSink func(tokens int)

	usageTokens int
	usageMax    int

	State         ControllerState
	LastBatchID   int
	MaxIterations int
	BlockedTasks  []BlockedTask
}

// NewController constructs a Controller with the given dependencies. The
// Orchestrator is built from the factory + ws + progress.
func NewController(ws *Workspace, git GitOps, dag *DAG, rs *RepoState, progress *Progress, factory swarm.RunnerFactory, routingConfig routing.Config, ss *session.State, pipelineBranch, targetBranch string) *Controller {
	return &Controller{
		WS:             ws,
		Git:            git,
		DAG:            dag,
		RepoState:      rs,
		Progress:       progress,
		Factory:        factory,
		RoutingConfig:  routingConfig,
		SessionState:   ss,
		PipelineBranch: pipelineBranch,
		TargetBranch:   targetBranch,
		State:          StateIdle,
		MaxIterations:  50,
		Orchestrator:   NewOrchestrator(factory, ws, progress),
	}
}

// Run executes the state machine loop. Returns nil on reaching StateFinalize,
// or ErrHumanGateRequired when a human gate is reached (the TUI re-dispatches
// after resolution), or another error on failure.
func (c *Controller) Run(ctx context.Context) error {
	if c.State == StateIdle || c.State == "" {
		c.State = StateWorkspaceReset
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		switch c.State {
		case StateWorkspaceReset:
			if err := c.WS.Reset(); err != nil {
				return fmt.Errorf("controller: workspace reset: %w", err)
			}
			c.State = StateSpecGate
		case StateSpecGate:
			// Check if spec.md exists and is approved.
			specPath := filepath.Join(c.WS.Dir(), "spec.md")
			data, err := os.ReadFile(specPath)
			if err == nil {
				fm, _ := ParseSpecFrontmatter(string(data))
				if fm.Status == string(SpecStatusApproved) {
					c.State = StateDecompose
					continue
				}
			}
			// No approved spec — surface human gate.
			c.SessionState.SetSDDGate(session.SDDGate{
				Kind:   "spec_approval",
				Reason: "spec.md needs human approval before decomposition",
			})
			c.State = StateSpecGate
			return ErrHumanGateRequired
		case StateDecompose:
			specPath := filepath.Join(c.WS.Dir(), "spec.md")
			// If no spec.md exists, parse the plan and emit a draft spec.
			if _, err := os.Stat(specPath); err != nil {
				if c.PlanPath == "" {
					return fmt.Errorf("controller: no spec.md and no plan path for decomposition")
				}
				draftDAG, err := ParsePlan(c.PlanPath)
				if err != nil {
					return fmt.Errorf("controller: parse plan: %w", err)
				}
				if err := c.emitDraftSpec(draftDAG); err != nil {
					return fmt.Errorf("controller: emit draft spec: %w", err)
				}
				c.State = StateSpecGate
				continue
			}
			// spec.md exists — parse it into the DAG.
			dag, err := ParseSpec(specPath)
			if err != nil {
				return fmt.Errorf("controller: parse spec: %w", err)
			}
			c.DAG = dag
			if err := c.DAG.Save(c.WS); err != nil {
				return fmt.Errorf("controller: save dag: %w", err)
			}
			c.RepoState.Branch = c.PipelineBranch
			c.RepoState.TargetBranch = c.TargetBranch
			if err := c.RepoState.Save(c.WS); err != nil {
				return fmt.Errorf("controller: save state: %w", err)
			}
			c.State = StateDrainIteration
		case StateDrainIteration:
			c.BlockedTasks = nil
			if c.LastBatchID >= c.MaxIterations {
				c.State = StateBranchReview
				continue
			}
			report, err := c.Orchestrator.Dispatch(ctx, c.DAG, c.RepoState, c.PipelineBranch, c.LastBatchID)
			if err != nil {
				return fmt.Errorf("controller: orchestrator dispatch: %w", err)
			}
			c.recordUsage()
			c.LastBatchID = report.BatchID
			_ = c.Progress.Append(c.WS, "ORCHESTRATOR", "BATCH_RETURNED", "batch_id", fmt.Sprintf("%d", report.BatchID), "status", string(report.Status))
			switch report.Status {
			case ReportDone:
				if report.NextAction == "branch_review" || len(c.DAG.ReadyTasks()) == 0 {
					c.State = StateBranchReview
				} else {
					c.State = StateDrainIteration
				}
			case ReportBlocked:
				c.BlockedTasks = report.BlockedTasks
				c.State = StateBlocked
			case ReportNeedsHuman:
				c.SessionState.SetSDDGate(session.SDDGate{Kind: "escalation", Reason: "orchestrator returned NEEDS_HUMAN"})
				return ErrHumanGateRequired
			case ReportHealthAlert:
				c.State = StateBlocked
			default:
				c.State = StateBlocked
			}
		case StateBlocked:
			if len(c.BlockedTasks) == 0 {
				// No blocked tasks — check health.
				if alert, _ := DetectLoops(c.Progress, c.WS, 50); alert != nil {
					rep, err := DispatchRescue(ctx, c.Factory, c.WS, c.Progress)
					if err != nil {
						return fmt.Errorf("controller: rescue dispatch: %w", err)
					}
					if rep.Status == ReportNeedsHuman {
						c.SessionState.SetSDDGate(session.SDDGate{Kind: "escalation", Reason: "rescue recommends human intervention"})
						return ErrHumanGateRequired
					}
					c.State = StateDrainIteration
					continue
				}
				c.SessionState.SetSDDGate(session.SDDGate{Kind: "escalation", Reason: "blocked with no tasks"})
				return ErrHumanGateRequired
			}
			// Attempt deterministic fixes for each blocked task.
			allFixed := true
			for _, bt := range c.BlockedTasks {
				err := DeterministicFix(bt.Reason, c.WS, c.DAG, c.RepoState, c.Git, c.Progress, "")
				if err != nil {
					allFixed = false
					break
				}
			}
			if allFixed {
				c.BlockedTasks = nil
				c.State = StateDrainIteration
				continue
			}
			// Deterministic fixes failed — dispatch rescue.
			rep, err := DispatchRescue(ctx, c.Factory, c.WS, c.Progress)
			if err != nil {
				return fmt.Errorf("controller: rescue dispatch: %w", err)
			}
			if rep.Status == ReportNeedsHuman {
				c.SessionState.SetSDDGate(session.SDDGate{Kind: "escalation", Reason: "rescue recommends human intervention"})
				return ErrHumanGateRequired
			}
			c.State = StateDrainIteration
		case StateBranchReview:
			// Dispatch the branch reviewer.
			runner, err := c.Factory(routing.RoleSDDBranchReviewer, swarm.ScopeReadOnly)
			if err != nil {
				return fmt.Errorf("controller: build branch reviewer: %w", err)
			}
			prompt := fmt.Sprintf("Review the whole SDD branch. Spec: %s. DAG: %s. Write your report to reports/branch.md with status: PASS | FAIL on the first line.", filepath.Join(c.WS.Dir(), "spec.md"), filepath.Join(c.WS.Dir(), "dag.json"))
			if _, err := runner.RunTask(ctx, prompt); err != nil {
				return fmt.Errorf("controller: branch review RunTask: %w", err)
			}
			rep, err := ReadReport(c.WS, "branch", "")
			if err != nil {
				return fmt.Errorf("controller: read branch report: %w", err)
			}
			if rep.Status == ReportPass {
				_ = c.Progress.Append(c.WS, "ORCHESTRATOR", "BRANCH_REVIEW", "status", "PASS")
				c.State = StateFinalMergeGate
			} else {
				_ = c.Progress.Append(c.WS, "ORCHESTRATOR", "BRANCH_REVIEW", "status", "FAIL")
				c.State = StateBlocked
			}
		case StateFinalMergeGate:
			// Surface the final merge gate.
			c.SessionState.SetSDDGate(session.SDDGate{Kind: "final_merge", Reason: "branch review passed; confirm final merge"})
			return ErrHumanGateRequired
		case StateFinalize:
			guard := FinalizeConfirm(c.Git, c.RepoState)
			if !guard.Pass {
				return fmt.Errorf("controller: finalize: %s", guard.Reason)
			}
			_ = c.Progress.Append(c.WS, "ORCHESTRATOR", "INTEGRATED", "target", c.TargetBranch)
			return nil
		default:
			return fmt.Errorf("controller: unknown state %q", c.State)
		}
	}
}

// ResolveGate clears the human gate and advances the state machine. Called by
// the TUI (P5) after the human resolves a gate. For spec_approval, it
// transitions to Decompose; for final_merge, to Finalize; for escalation,
// back to DrainIteration.
func (c *Controller) ResolveGate() {
	gate := c.SessionState.SDDGate()
	c.SessionState.ClearSDDGate()
	switch gate.Kind {
	case "spec_approval":
		c.State = StateDecompose
	case "final_merge":
		c.State = StateFinalize
	case "escalation":
		c.State = StateDrainIteration
	}
}

// swapOrchestratorModel rebuilds the Orchestrator with a new RunnerFactory
// whose routing config has RoleSDDOrchestrator overridden to the given
// preset. Used for model escalation (spec unknown 6).
func (c *Controller) swapOrchestratorModel(preset string) {
	if c.RebuildFactory == nil {
		return
	}
	newCfg := c.RoutingConfig.WithRoleOverride(routing.RoleSDDOrchestrator, preset)
	c.RoutingConfig = newCfg
	c.Factory = c.RebuildFactory(newCfg)
	c.Orchestrator = NewOrchestrator(c.Factory, c.WS, c.Progress)
}

// recordUsage flushes accumulated token usage to the session state.
// The factory's UsageObserver (wired in Task 7) increments c.usageTokens;
// this method writes the accumulated count to SessionState.
func (c *Controller) recordUsage() {
	if c.SessionState != nil {
		c.SessionState.UpdateSDDTokens(c.usageTokens, c.usageMax)
	}
}

// emitDraftSpec writes a draft spec.md with frontmatter status: draft and
// a yaml tasks: block listing the DAG's tasks (id, title, deps, files,
// acceptance). The human approves it at the spec gate.
func (c *Controller) emitDraftSpec(dag *DAG) error {
	var b strings.Builder
	b.WriteString("---\nstatus: draft\nsource_plan: " + c.PlanPath + "\ntarget_branch: " + c.TargetBranch + "\n---\n\n# SDD Spec\n\n```yaml\ntasks:\n")
	for _, t := range dag.Tasks {
		b.WriteString("  - id: " + t.ID + "\n")
		b.WriteString("    title: " + t.Title + "\n")
		b.WriteString("    deps: [" + strings.Join(t.Deps, ", ") + "]\n")
		b.WriteString("    files: [" + strings.Join(t.Files, ", ") + "]\n")
		b.WriteString("    acceptance: [" + strings.Join(t.Acceptance, ", ") + "]\n")
	}
	b.WriteString("```\n")
	return os.WriteFile(filepath.Join(c.WS.Dir(), "spec.md"), []byte(b.String()), 0644)
}
