// internal/pipeline/inspect.go
package pipeline

// Inspection is the shared result of parsing, compiling, and preflighting a
// plan. It is produced by InspectPlan and consumed by authoring review, plan
// discovery, the TUI preflight, and the controller's own execution path so
// every caller sees the same PlanIR, diagnostics, and strategy.
type Inspection struct {
	Plan              *Plan
	IR                *PlanIR
	Diagnostics       []Diagnostic
	Report            CompileReport
	RequestedStrategy Strategy
	EffectiveStrategy Strategy
	HasMarshalBlocks  bool
	StrictBlocked     bool
}

// Errors returns the error-severity diagnostics.
func (i *Inspection) Errors() []Diagnostic {
	var out []Diagnostic
	for _, d := range i.Diagnostics {
		if d.Severity == DiagError {
			out = append(out, d)
		}
	}
	return out
}

// HasErrors reports whether any error-severity diagnostic exists.
func (i *Inspection) HasErrors() bool { return len(i.Errors()) > 0 }

// InspectPlan parses, compiles, and preflights a plan against the repository
// at repoRoot, then resolves the effective strategy. requested may be
// StrategyAuto to select adaptive when executable blocks exist and agent
// otherwise. Strict mode fails closed: any compile error, blocked operation,
// explicit agent operation, or prose-only task sets StrictBlocked.
func InspectPlan(path, repoRoot string, requested Strategy) (*Inspection, error) {
	plan, err := ParsePlan(path)
	if err != nil {
		return nil, err
	}
	ir, diagnostics, err := CompilePlan(plan)
	if err != nil {
		return nil, err
	}
	for index := range ir.Tasks {
		taskDiagnostics := preflightOps(repoRoot, ir.Tasks[index].Operations)
		for _, diagnostic := range taskDiagnostics {
			diagnostic.TaskN = ir.Tasks[index].TaskN
			diagnostics = append(diagnostics, diagnostic)
		}
		ir.Tasks[index].Status = ir.Tasks[index].DerivedStatus()
	}
	hasBlocks := false
	for _, task := range ir.Tasks {
		if len(task.Operations) > 0 {
			hasBlocks = true
			break
		}
	}
	effective := requested
	if effective == StrategyAuto {
		effective = StrategyAgent
		if hasBlocks {
			effective = StrategyAdaptive
		}
	}
	strictBlocked := false
	if effective == StrategyStrict {
		for _, diagnostic := range diagnostics {
			if diagnostic.Severity == DiagError {
				strictBlocked = true
				break
			}
		}
		for _, task := range ir.Tasks {
			if task.Status == TaskBlocked || task.Status == TaskPartial || task.Status == TaskAgentOnly {
				strictBlocked = true
				break
			}
		}
	}
	return &Inspection{
		Plan: plan, IR: ir, Diagnostics: diagnostics, Report: ir.Report(),
		RequestedStrategy: requested, EffectiveStrategy: effective,
		HasMarshalBlocks: hasBlocks, StrictBlocked: strictBlocked,
	}, nil
}
