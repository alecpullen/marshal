// internal/agent/swarm/orchestrator.go
package swarm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"marshal/internal/agent"
	"marshal/internal/app/session"
)

// RegistryScope selects which tool registry view a role's runner receives.
type RegistryScope int

const (
	ScopeFull     RegistryScope = iota // implementer: full registry (sole writer)
	ScopeReadOnly                      // planner, scouts, reviewer
	ScopeTester                        // tester: read-only plus command execution
)

// RunnerFactory builds a role-specific agent.Runner. Implementations must
// return a fresh Runner per call: Runner tracks per-turn state (call
// history, loop nudge), so instances cannot be shared between concurrent
// scouts.
type RunnerFactory func(role agent.AgentRole, scope RegistryScope) (*agent.Runner, error)

// Orchestrator drives the swarm pipeline:
//
//	planner -> parallel read-only repo scouts -> [implementer -> tester]* -> reviewer
//
// sharing one TaskState blackboard. It satisfies the TUI's AgentRunner
// interface so /swarm dispatch reuses the existing agent-turn plumbing.
type Orchestrator struct {
	State        *session.State
	NewRunner    RunnerFactory
	ScoutFocuses []ScoutFocus

	MaxFixRounds   int
	MaxTotalTokens int
	NewMeter       func() TokenMeter
}

func New(state *session.State, factory RunnerFactory) *Orchestrator {
	return &Orchestrator{State: state, NewRunner: factory, ScoutFocuses: DefaultScoutFocuses}
}

// SetForceClass satisfies tui.AgentRunner. Swarm roles fix their own task
// classes, so forcing has no effect.
func (o *Orchestrator) SetForceClass(string) {}

func (o *Orchestrator) maxRounds() int {
	if o.MaxFixRounds < 1 {
		return 1
	}
	return o.MaxFixRounds
}

func (o *Orchestrator) newMeter() TokenMeter {
	if o.NewMeter != nil {
		return o.NewMeter()
	}
	return NewEstimateMeter()
}

func (o *Orchestrator) overBudget(meter TokenMeter) bool {
	return o.MaxTotalTokens > 0 && meter.Total() >= o.MaxTotalTokens
}

func (o *Orchestrator) Run(ctx context.Context, goal string) error {
	ts := NewTaskState(goal)
	meter := o.newMeter()

	o.State.SetSwarmProgress(session.SwarmProgress{
		Goal:   goal,
		Active: true,
		Roles: []session.SwarmRole{
			{Name: "planner", Status: session.SwarmRolePending},
			{Name: "scouts", Status: session.SwarmRolePending},
			{Name: "implementer", Status: session.SwarmRolePending},
			{Name: "tester", Status: session.SwarmRolePending},
			{Name: "reviewer", Status: session.SwarmRolePending},
		},
	})
	defer o.State.ClearSwarmProgress()

	o.announce("Swarm run started.")

	// 1. Planner (read-only): produces the shared plan.
	o.State.UpdateSwarmRole("planner", session.SwarmRoleActive, "")
	planPrompt := plannerPrompt(ts)
	plannerTask, err := o.runRole(ctx, agent.RolePlanner, ScopeReadOnly, planPrompt)
	if err != nil {
		o.State.UpdateSwarmRole("planner", session.SwarmRoleFailed, "")
		o.announce("Swarm aborted: planner failed.")
		return err
	}
	ts.SetPlan(planLines(plannerTask.Summary))
	o.observe(meter, agent.RolePlanner, planPrompt, plannerTask.Summary)
	o.State.UpdateSwarmRole("planner", session.SwarmRoleDone, "")

	// 2. Repo scouts (read-only, parallel). Runners are constructed before
	// the goroutines start so the factory is never called concurrently.
	if !o.overBudget(meter) {
		focuses := o.focuses()
		o.State.UpdateSwarmRole("scouts", session.SwarmRoleActive, fmt.Sprintf("0/%d", len(focuses)))
		type scoutJob struct {
			focus  ScoutFocus
			runner *agent.Runner
			prompt string
		}
		jobs := make([]scoutJob, 0, len(focuses))
		for _, focus := range focuses {
			runner, err := o.NewRunner(agent.RoleRepoScout, ScopeReadOnly)
			if err != nil {
				o.State.UpdateSwarmRole("scouts", session.SwarmRoleFailed, "")
				o.announce("Swarm aborted: could not build repo scout.")
				return err
			}
			jobs = append(jobs, scoutJob{focus: focus, runner: runner, prompt: scoutPrompt(ts, focus)})
		}
		var wg sync.WaitGroup
		var done int32
		for _, job := range jobs {
			wg.Add(1)
			go func(j scoutJob) {
				defer wg.Done()
				task, err := j.runner.RunTask(ctx, j.prompt)
				if err != nil {
					ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: "scout failed: " + err.Error()})
				} else {
					ts.AddFinding(Finding{Agent: "repo_scout", Area: j.focus.Area, Content: task.Summary})
					o.observe(meter, agent.RoleRepoScout, j.prompt, task.Summary)
				}
				n := atomic.AddInt32(&done, 1)
				o.State.UpdateSwarmRole("scouts", session.SwarmRoleActive, fmt.Sprintf("%d/%d", n, len(jobs)))
			}(job)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o.State.UpdateSwarmRole("scouts", session.SwarmRoleDone, fmt.Sprintf("%d/%d", len(jobs), len(jobs)))
	} else {
		o.State.UpdateSwarmRole("scouts", session.SwarmRoleDone, "skipped (budget)")
	}

	// 3. Implementer/tester loop. The implementer is the only writer; the
	// tester gets the read + command registry scope.
	rounds := o.maxRounds()
	for round := 1; round <= rounds; round++ {
		if o.overBudget(meter) {
			break
		}
		o.State.UpdateSwarmRole("implementer", session.SwarmRoleActive, fmt.Sprintf("round %d/%d", round, rounds))
		implPrompt := implementerPrompt(ts)
		implTask, err := o.runRole(ctx, agent.RoleImplementer, ScopeFull, implPrompt)
		if err != nil {
			o.State.UpdateSwarmRole("implementer", session.SwarmRoleFailed, "")
			o.announce("Swarm aborted: implementer failed.")
			return err
		}
		ts.AddPatchNote(implTask.Summary)
		o.observe(meter, agent.RoleImplementer, implPrompt, implTask.Summary)
		o.State.UpdateSwarmRole("implementer", session.SwarmRoleDone, fmt.Sprintf("round %d/%d", round, rounds))

		if o.overBudget(meter) {
			break
		}
		o.State.UpdateSwarmRole("tester", session.SwarmRoleActive, fmt.Sprintf("round %d/%d", round, rounds))
		testPrompt := testerPrompt(ts)
		testTask, err := o.runRole(ctx, agent.RoleTester, ScopeTester, testPrompt)
		if err != nil {
			ts.AddFinding(Finding{Agent: "tester", Area: "tests", Content: "tester failed: " + err.Error()})
			o.State.UpdateSwarmRole("tester", session.SwarmRoleFailed, "")
			break
		}
		ts.AddFinding(Finding{Agent: "tester", Area: "tests", Content: testTask.Summary})
		o.observe(meter, agent.RoleTester, testPrompt, testTask.Summary)

		pass, ok := ParseVerdict(testTask.Summary)
		if pass || !ok {
			o.State.UpdateSwarmRole("tester", session.SwarmRoleDone, fmt.Sprintf("round %d/%d", round, rounds))
			break
		}
		o.State.UpdateSwarmRole("tester", session.SwarmRoleDone, fmt.Sprintf("round %d/%d FAIL", round, rounds))
	}

	// 4. Reviewer (read-only). A reviewer failure is reported, not fatal:
	// the implementer's work is already in the working tree.
	o.State.UpdateSwarmRole("reviewer", session.SwarmRoleActive, "")
	reviewPrompt := reviewerPrompt(ts)
	reviewTask, err := o.runRole(ctx, agent.RoleReviewer, ScopeReadOnly, reviewPrompt)
	if err != nil {
		ts.SetFinalSummary("Reviewer failed: " + err.Error())
		o.State.UpdateSwarmRole("reviewer", session.SwarmRoleFailed, "")
	} else {
		ts.SetFinalSummary(reviewTask.Summary)
		o.observe(meter, agent.RoleReviewer, reviewPrompt, reviewTask.Summary)
		o.State.UpdateSwarmRole("reviewer", session.SwarmRoleDone, "")
	}

	summary := ts.Render()
	if o.MaxTotalTokens > 0 {
		summary += fmt.Sprintf("\n\n_Token budget: ~%d / %d (estimated)._", meter.Total(), o.MaxTotalTokens)
	}
	o.State.AddMessage(session.RoleSystem, "Swarm complete.\n\n"+summary, session.ContentTypeMarkdown)
	o.announce("Swarm run complete.")
	return nil
}

func (o *Orchestrator) runRole(ctx context.Context, role agent.AgentRole, scope RegistryScope, prompt string) (*agent.Task, error) {
	runner, err := o.NewRunner(role, scope)
	if err != nil {
		return nil, err
	}
	return runner.RunTask(ctx, prompt)
}

func (o *Orchestrator) observe(meter TokenMeter, role agent.AgentRole, prompt, answer string) {
	meter.Observe(role, EstimateText(prompt), EstimateText(answer))
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
