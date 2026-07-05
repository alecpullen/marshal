// internal/agent/swarm/orchestrator.go
package swarm

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"marshal/internal/agent"
	"marshal/internal/app/session"
)

// RunnerFactory builds a role-specific agent.Runner. readOnly selects the
// read-only registry view. Implementations must return a fresh Runner per
// call: Runner tracks per-turn state (call history, loop nudge), so
// instances cannot be shared between concurrent scouts.
type RunnerFactory func(role agent.AgentRole, readOnly bool) (*agent.Runner, error)

// Orchestrator drives the Milestone O sequential swarm
// (docs/07, "First swarm milestone"):
//
//	planner -> parallel read-only repo scouts -> implementer -> reviewer
//
// sharing one TaskState blackboard. It satisfies the TUI's AgentRunner
// interface so /swarm dispatch reuses the existing agent-turn plumbing.
type Orchestrator struct {
	State        *session.State
	NewRunner    RunnerFactory
	ScoutFocuses []ScoutFocus
}

func New(state *session.State, factory RunnerFactory) *Orchestrator {
	return &Orchestrator{State: state, NewRunner: factory, ScoutFocuses: DefaultScoutFocuses}
}

// SetForceClass satisfies tui.AgentRunner. Swarm roles fix their own task
// classes, so forcing has no effect.
func (o *Orchestrator) SetForceClass(string) {}

func (o *Orchestrator) Run(ctx context.Context, goal string) error {
	ts := NewTaskState(goal)
	o.announce("Swarm run started: planner -> repo scouts -> implementer -> reviewer.")

	// 1. Planner (read-only): produces the shared plan.
	o.announce("Swarm: planner")
	plannerTask, err := o.runRole(ctx, agent.RolePlanner, true, plannerPrompt(ts))
	if err != nil {
		o.announce("Swarm aborted: planner failed.")
		return err
	}
	ts.SetPlan(planLines(plannerTask.Summary))

	// 2. Repo scouts (read-only, parallel). Runners are constructed before
	// the goroutines start so the factory is never called concurrently.
	focuses := o.focuses()
	o.announce(fmt.Sprintf("Swarm: %d repo scouts (parallel, read-only)", len(focuses)))
	type scoutJob struct {
		focus  ScoutFocus
		runner *agent.Runner
	}
	jobs := make([]scoutJob, 0, len(focuses))
	for _, focus := range focuses {
		runner, err := o.NewRunner(agent.RoleRepoScout, true)
		if err != nil {
			o.announce("Swarm aborted: could not build repo scout.")
			return err
		}
		jobs = append(jobs, scoutJob{focus: focus, runner: runner})
	}
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(j scoutJob) {
			defer wg.Done()
			task, err := j.runner.RunTask(ctx, scoutPrompt(ts, j.focus))
			if err != nil {
				ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: "scout failed: " + err.Error()})
				return
			}
			ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: task.Summary})
		}(job)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 3. Implementer: the only writer. Its runner holds the full registry
	// and the shared WriteGate (set by the factory).
	o.announce("Swarm: implementer")
	implTask, err := o.runRole(ctx, agent.RoleImplementer, false, implementerPrompt(ts))
	if err != nil {
		o.announce("Swarm aborted: implementer failed.")
		return err
	}
	ts.AddPatchNote(implTask.Summary)

	// 4. Reviewer (read-only). A reviewer failure is reported, not fatal:
	// the implementer's work is already in the working tree.
	o.announce("Swarm: reviewer")
	reviewTask, err := o.runRole(ctx, agent.RoleReviewer, true, reviewerPrompt(ts))
	if err != nil {
		ts.SetFinalSummary("Reviewer failed: " + err.Error())
	} else {
		ts.SetFinalSummary(reviewTask.Summary)
	}

	o.State.AddMessage(session.RoleSystem, "Swarm complete.\n\n"+ts.Render(), session.ContentTypeMarkdown)
	return nil
}

func (o *Orchestrator) runRole(ctx context.Context, role agent.AgentRole, readOnly bool, prompt string) (*agent.Task, error) {
	runner, err := o.NewRunner(role, readOnly)
	if err != nil {
		return nil, err
	}
	return runner.RunTask(ctx, prompt)
}

func (o *Orchestrator) focuses() []ScoutFocus {
	if len(o.ScoutFocuses) > 0 {
		return o.ScoutFocuses
	}
	return DefaultScoutFocuses
}

func (o *Orchestrator) announce(text string) {
	o.State.AddMessage(session.RoleSystem, text, session.ContentTypePlain)
}

// planLines splits the planner's final answer into trimmed non-empty lines.
func planLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
