package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"marshal/internal/acpclient/inproc"
	"marshal/internal/acpwire"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/llm/routing"
	"marshal/internal/pipeline"
	"marshal/internal/sddauthor"
	"marshal/internal/sddplans"
)

// PipelineRunner is a swarm or plan-execution runner. *swarm.Orchestrator and
// *pipeline.ControllerAdapter satisfy it, and ACP's AgentRunner has the same
// methods.
type PipelineRunner interface {
	Run(ctx context.Context, goal string) error
	SetForceClass(class string)
	AnswerGate(answer string)
}

// SwarmOverrideFactory builds a per-run swarm.RunnerFactory with role->preset
// overrides applied.
type SwarmOverrideFactory func(overrides map[routing.AgentRole]string) swarm.RunnerFactory

// RunSwarm runs the runtime's swarm over params.Goal, with params' role
// overrides installed for this run only.
func (rt *Runtime) RunSwarm(ctx context.Context, params acpwire.SwarmParams) error {
	rt.mu.Lock()
	orchestrator := rt.SwarmRunner
	override := rt.SwarmOverrideFactory
	rt.mu.Unlock()
	if orchestrator == nil {
		return &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "run swarm: no swarm runner"}
	}
	// The overrides are installed on the shared orchestrator for this run
	// only: RestoreRunner puts the original factory back when the run ends,
	// so a later run never inherits them.
	if len(params.Overrides) > 0 && override != nil {
		orchestrator.SetRunnerFactory(override(roleOverrides(params.Overrides)))
		defer orchestrator.RestoreRunner()
	}
	return orchestrator.Run(ctx, params.Goal)
}

// NewPlanRun builds the plan run params names over the runtime's current
// pipeline factory. It does not run it.
func (rt *Runtime) NewPlanRun(params acpwire.SDDParams) (inproc.PlanRun, error) {
	rt.mu.Lock()
	factory := rt.PipelineFactory
	rt.mu.Unlock()
	if factory == nil {
		return nil, &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "run plan: no plan runner"}
	}
	runner := factory(params.PlanPath, roleOverrides(params.Overrides))
	if runner == nil {
		return nil, fmt.Errorf("run plan: cannot build a runner for %s", params.PlanPath)
	}
	// The strategy and verify commands the preflight settled on reach the
	// controller here, agent side: a plan a client asked to run strictly, or
	// with commands it proposed, must not fall back to the configured ones.
	if adapter, ok := runner.(*pipeline.ControllerAdapter); ok {
		controller := adapter.Controller()
		if params.Strategy != "" {
			controller.Strategy = pipeline.Strategy(params.Strategy)
		}
		if params.Verify != nil && (params.Verify.Build != "" || params.Verify.Test != "") {
			controller.Verifier.Build = params.Verify.Build
			controller.Verifier.Test = params.Verify.Test
		}
	}
	return &planRun{runner: runner, planPath: params.PlanPath}, nil
}

// PlanPreflight inspects the plan params names against the runtime's working
// directory and resolves the verify gate a run would use. It writes nothing.
func (rt *Runtime) PlanPreflight(ctx context.Context, params acpwire.PlanPreflightParams) (acpwire.PlanPreflight, error) {
	rt.mu.Lock()
	state := rt.State
	rt.mu.Unlock()
	strategy := pipeline.Strategy(params.Strategy)
	if strategy == "" {
		strategy = pipeline.StrategyAuto
	}
	inspection, err := pipeline.InspectPlan(params.PlanPath, state.WorkingDir, strategy)
	if err != nil {
		return acpwire.PlanPreflight{}, err
	}
	// Agent-side code reads the config through ConfigSnapshot: a reload
	// replaces State.Config from another goroutine.
	out := acpwire.PlanPreflight{
		Verify: verifyGateOf(state.WorkingDir, state.ConfigSnapshot().SDD.Verify),
	}
	if wire := planInspectionOf(inspection); wire != nil {
		out.Inspection = *wire
	}
	return out, nil
}

// AuthorPlan resolves the draft path for params.Goal, runs the runtime's plan
// author over it, and returns the candidate.
func (rt *Runtime) AuthorPlan(ctx context.Context, params acpwire.AuthorPlanParams) (acpwire.AuthoredPlan, error) {
	rt.mu.Lock()
	state := rt.State
	factory := rt.PlanAuthorFactory
	rt.mu.Unlock()
	if factory == nil {
		return acpwire.AuthoredPlan{}, &acpwire.Error{Code: acpwire.CodeNoRunner, Message: "author plan: no plan author"}
	}
	design := params.Design
	if strings.TrimSpace(design) == "" {
		design = params.Goal
	}
	planPath, err := sddplans.DraftPath(state.WorkingDir, state.ConfigSnapshot().SDD.PlansDir, params.Goal, state.Now())
	if err != nil {
		return acpwire.AuthoredPlan{}, fmt.Errorf("resolve plan path: %w", err)
	}
	req := sddauthor.Request{
		Goal:     params.Goal,
		Design:   design,
		RepoRoot: state.WorkingDir,
		PlanPath: planPath,
	}
	runner, err := factory(req)
	if err != nil {
		return acpwire.AuthoredPlan{}, fmt.Errorf("start plan authoring: %w", err)
	}
	result, err := runner.Run(ctx, req)
	if err != nil {
		return acpwire.AuthoredPlan{}, err
	}
	return authoredPlanOf(result), nil
}

// planRun is one plan-execution run (inproc.PlanRun).
type planRun struct {
	runner   PipelineRunner
	planPath string
}

// Run executes the plan. A run that stops at a human gate reports the gate as
// a code, so the client can answer it instead of treating the run as failed.
func (r *planRun) Run(ctx context.Context) error {
	err := r.runner.Run(ctx, r.planPath)
	if errors.Is(err, pipeline.ErrHumanGateRequired) {
		return &acpwire.Error{Code: acpwire.CodeHumanGateRequired, Message: err.Error()}
	}
	return err
}

// Answer delivers the human's answer to the run's open gate.
func (r *planRun) Answer(answer string) { r.runner.AnswerGate(answer) }

// roleOverrides converts the wire's role-name overrides; nil when empty.
func roleOverrides(names map[string]string) map[routing.AgentRole]string {
	if len(names) == 0 {
		return nil
	}
	out := make(map[routing.AgentRole]string, len(names))
	for name, preset := range names {
		out[routing.AgentRole(name)] = preset
	}
	return out
}

// planInspectionOf converts an inspection to the wire; nil for nil.
func planInspectionOf(insp *pipeline.Inspection) *acpwire.PlanInspection {
	if insp == nil {
		return nil
	}
	out := &acpwire.PlanInspection{
		Strategy:      string(insp.EffectiveStrategy),
		HasBlocks:     insp.HasMarshalBlocks,
		StrictBlocked: insp.StrictBlocked,
		DetOps:        insp.Report.Total.DetOps,
		AgentOps:      insp.Report.Total.AgentOps,
		EstCalls:      insp.Report.Total.EstCalls,
	}
	for _, task := range insp.Report.Tasks {
		out.Tasks = append(out.Tasks, acpwire.PlanTask{
			TaskN:    task.TaskN,
			Title:    task.Title,
			DetOps:   task.DetOps,
			AgentOps: task.AgentOps,
			EstCalls: task.EstCalls,
		})
	}
	for _, diagnostic := range insp.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, acpwire.PlanDiagnostic{
			TaskN:    diagnostic.TaskN,
			Severity: string(diagnostic.Severity),
			Message:  diagnostic.Message,
			Line:     diagnostic.Line,
		})
	}
	return out
}

// verifyGateOf resolves the verify gate for repoRoot from the configured
// commands.
func verifyGateOf(repoRoot string, configured config.SDDVerifyConfig) acpwire.VerifyGate {
	res := pipeline.ResolveVerifyCommands(repoRoot, configured.Build, configured.Test)
	gate := acpwire.VerifyGate{Build: res.Build, Test: res.Test, Known: res.Known}
	// "(detected)": the commands came from the repository's manifest, not
	// from config. Same rule the TUI's preflight row renders.
	if res.Known && (res.Build != configured.Build || res.Test != configured.Test) {
		gate.Detected = true
	}
	return gate
}

// authoredPlanOf converts an authoring result to the wire, without a stop
// reason.
func authoredPlanOf(result sddauthor.Result) acpwire.AuthoredPlan {
	out := acpwire.AuthoredPlan{
		AutoAccepted: result.AutoAccepted,
		Inspection:   planInspectionOf(result.Inspection),
	}
	if path := result.Request.PlanPath; path != "" {
		out.PlanPath = path
	} else if result.Inspection != nil && result.Inspection.Plan != nil {
		// A result built without a request still names its candidate through
		// the inspection.
		out.PlanPath = result.Inspection.Plan.Path
	}
	return out
}
