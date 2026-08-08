// internal/pipeline/compile.go
package pipeline

import (
	"fmt"
	"strings"
)

// DiagSeverity is the severity of a compiler diagnostic.
type DiagSeverity string

const (
	DiagError   DiagSeverity = "error"
	DiagWarning DiagSeverity = "warning"
)

// Diagnostic is one compiler message. TaskN is 0 for plan-level diagnostics.
type Diagnostic struct {
	TaskN    int
	Severity DiagSeverity
	Message  string
	Line     int
}

// CompilePlan parses a Plan's task bodies for marshal.* blocks, compiles
// them into typed operations, validates the IR, and returns it with any
// diagnostics. A hard error means the compiler itself could not run;
// diagnostics carry plan-authoring errors and warnings.
func CompilePlan(plan *Plan) (*PlanIR, []Diagnostic, error) {
	ir := &PlanIR{
		Version:           1,
		Name:              plan.Slug,
		Slug:              plan.Slug,
		SourcePath:        plan.Path,
		GlobalConstraints: plan.GlobalConstraints,
	}
	var diags []Diagnostic

	for _, task := range plan.Tasks {
		taskIR := TaskIR{
			ID:        fmt.Sprintf("task-%d", task.N),
			TaskN:     task.N,
			Title:     task.Title,
			ProseBody: task.Body,
		}

		blocks, err := parseMarshalBlocks(task.Body, 0)
		if err != nil {
			diags = append(diags, Diagnostic{
				TaskN:    task.N,
				Severity: DiagError,
				Message:  err.Error(),
			})
			ir.Tasks = append(ir.Tasks, taskIR)
			continue
		}

		for _, blk := range blocks {
			op, err := compileBlock(blk)
			if err != nil {
				diags = append(diags, Diagnostic{
					TaskN:    task.N,
					Severity: DiagError,
					Message:  err.Error(),
					Line:     blk.lineStart,
				})
				continue
			}
			taskIR.Operations = append(taskIR.Operations, op)

			// Extract allowed files from agent blocks.
			if agent, ok := op.(*AgentOp); ok {
				taskIR.FallbackAllowed = true
				taskIR.AllowedFiles = append(taskIR.AllowedFiles, agent.AllowedFiles...)
			}
		}

		// Deduplicate allowed files.
		taskIR.AllowedFiles = dedupStrings(taskIR.AllowedFiles)

		ir.Tasks = append(ir.Tasks, taskIR)
	}

	// Validate.
	if vDiags := validatePlan(ir); len(vDiags) > 0 {
		diags = append(diags, vDiags...)
	}

	return ir, diags, nil
}

// ValidateIR checks the IR for structural problems like dependency cycles.
// It returns an error for hard failures that make the plan unexecutable.
func ValidateIR(ir *PlanIR) error {
	// Check for dependency cycles using a simple DFS.
	visited := map[string]int{} // 0 = unvisited, 1 = in progress, 2 = done
	var visit func(id string) error
	visit = func(id string) error {
		if visited[id] == 1 {
			return fmt.Errorf("compile: dependency cycle involving %s", id)
		}
		if visited[id] == 2 {
			return nil
		}
		visited[id] = 1
		for _, task := range ir.Tasks {
			if task.ID == id {
				for _, dep := range task.Dependencies {
					if err := visit(dep); err != nil {
						return err
					}
				}
				break
			}
		}
		visited[id] = 2
		return nil
	}
	for _, task := range ir.Tasks {
		if err := visit(task.ID); err != nil {
			return err
		}
	}
	return nil
}

// validatePlan runs validation checks and returns diagnostics (not errors).
func validatePlan(ir *PlanIR) []Diagnostic {
	var diags []Diagnostic

	// Dependency cycle check.
	if err := ValidateIR(ir); err != nil {
		diags = append(diags, Diagnostic{
			TaskN:    0,
			Severity: DiagError,
			Message:  err.Error(),
		})
	}

	// Allowed-path conflict check: two tasks patching the same file without
	// a declared dependency.
	for i, a := range ir.Tasks {
		for _, b := range ir.Tasks[i+1:] {
			for _, af := range a.AllowedFiles {
				for _, bf := range b.AllowedFiles {
					if af == bf && !hasDependency(a, b.ID) && !hasDependency(b, a.ID) {
						diags = append(diags, Diagnostic{
							TaskN:    b.TaskN,
							Severity: DiagWarning,
							Message:  fmt.Sprintf("task %d and task %d both modify %s without a declared dependency", a.TaskN, b.TaskN, af),
						})
					}
				}
			}
		}
	}

	return diags
}

func hasDependency(task TaskIR, depID string) bool {
	for _, d := range task.Dependencies {
		if d == depID {
			return true
		}
	}
	return false
}

func dedupStrings(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// CompileReport is a human-readable summary of the compiled plan, shown in
// the preflight panel.
type CompileReport struct {
	Tasks []TaskReport
	Total struct {
		DetOps   int
		AgentOps int
		EstCalls int
	}
}

type TaskReport struct {
	TaskN    int
	Title    string
	DetOps   int
	AgentOps int
	EstCalls int
	Status   TaskStatus
	Warnings []string
}

// Report generates a CompileReport from the IR.
func (ir *PlanIR) Report() CompileReport {
	var r CompileReport
	for _, task := range ir.Tasks {
		tr := TaskReport{
			TaskN:    task.TaskN,
			Title:    task.Title,
			EstCalls: task.EstimatedModelCalls(),
			Status:   task.DerivedStatus(),
		}
		for _, op := range task.Operations {
			if op.OpKind() == "agent" || op.OpStatus() == OpAgent || op.OpStatus() == OpBlocked {
				tr.AgentOps++
			} else {
				tr.DetOps++
			}
		}
		r.Total.DetOps += tr.DetOps
		r.Total.AgentOps += tr.AgentOps
		r.Total.EstCalls += tr.EstCalls
		r.Tasks = append(r.Tasks, tr)
	}
	return r
}

func (r CompileReport) String() string {
	var b strings.Builder
	for _, tr := range r.Tasks {
		fmt.Fprintf(&b, "Task %d: %s   %d det ops - %d agent - %d call(s)\n",
			tr.TaskN, tr.Title, tr.DetOps, tr.AgentOps, tr.EstCalls)
	}
	fmt.Fprintf(&b, "plan: %d det ops - %d agent ops - est. %d model calls\n",
		r.Total.DetOps, r.Total.AgentOps, r.Total.EstCalls)
	return b.String()
}
