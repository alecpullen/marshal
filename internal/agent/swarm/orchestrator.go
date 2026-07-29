// internal/agent/swarm/orchestrator.go
package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/schema"
	"marshal/internal/strutil"
	"marshal/internal/tools/policy"
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
}

func New(state *session.State, factory RunnerFactory) *Orchestrator {
	return &Orchestrator{State: state, NewRunner: factory, ScoutFocuses: DefaultScoutFocuses}
}

// SetForceClass satisfies tui.AgentRunner. Swarm roles fix their own task
// classes, so forcing has no effect.
func (o *Orchestrator) SetForceClass(string)                   {}
func (o *Orchestrator) SetPolicyRules([]config.PermissionRule) {}
func (o *Orchestrator) SetApprovalMode(policy.ApprovalMode)    {}
func (o *Orchestrator) AnswerGate(string)                           {}

func (o *Orchestrator) maxRounds() int {
	if o.MaxFixRounds < 1 {
		return 1
	}
	return o.MaxFixRounds
}

func (o *Orchestrator) newMeter() TokenMeter {
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
		TokensMax: o.MaxTotalTokens,
	})
	defer o.State.ClearSwarmProgress()

	o.announce("Swarm run started.")

	// 1. Planner (read-only): produces the shared plan.
	o.setRole(meter, "planner", session.SwarmRoleActive, "")
	planPrompt := plannerPrompt(ts)
	plannerTask, err := o.runRole(ctx, meter, agent.RolePlanner, ScopeReadOnly, planPrompt)
	if err != nil {
		o.setRole(meter, "planner", session.SwarmRoleFailed, "")
		o.announce("Swarm aborted: planner failed.")
		return err
	}
	ts.SetPlan(planLines(plannerTask.Summary))
	o.setRole(meter, "planner", session.SwarmRoleDone, "")

	// 2. Repo scouts (read-only, parallel). Runners are constructed before
	// the goroutines start so the factory is never called concurrently.
	if !o.overBudget(meter) {
		focuses := o.focuses()
		o.setRole(meter, "scouts", session.SwarmRoleActive, fmt.Sprintf("0/%d", len(focuses)))
		type scoutJob struct {
			focus        ScoutFocus
			runner       *agent.Runner
			prompt       string
			hasRealUsage *bool
		}
		jobs := make([]scoutJob, 0, len(focuses))
		for _, focus := range focuses {
			runner, err := o.NewRunner(agent.RoleRepoScout, ScopeReadOnly)
			if err != nil {
				o.setRole(meter, "scouts", session.SwarmRoleFailed, "")
				o.announce("Swarm aborted: could not build repo scout.")
				return err
			}
			var hasRealUsage bool
			runner.UsageObserver = func(usage schema.TokenUsage) {
				hasRealUsage = true
				meter.Observe(agent.RoleRepoScout, usage)
			}
			jobs = append(jobs, scoutJob{
				focus:        focus,
				runner:       runner,
				prompt:       scoutPrompt(ts, focus),
				hasRealUsage: &hasRealUsage,
			})
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
					if !*j.hasRealUsage {
						o.observe(meter, agent.RoleRepoScout, j.prompt, task.Summary)
					}
				}
				n := atomic.AddInt32(&done, 1)
				o.setRole(meter, "scouts", session.SwarmRoleActive, fmt.Sprintf("%d/%d", n, len(jobs)))
			}(job)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o.setRole(meter, "scouts", session.SwarmRoleDone, fmt.Sprintf("%d/%d", len(jobs), len(jobs)))
	} else {
		o.setRole(meter, "scouts", session.SwarmRoleDone, "skipped (budget)")
	}

	// 3. Implementer/tester loop. The implementer is the only writer; the
	// tester gets the read + command registry scope.
	rounds := o.maxRounds()
	for round := 1; round <= rounds; round++ {
		if o.overBudget(meter) {
			break
		}
		o.setRole(meter, "implementer", session.SwarmRoleActive, fmt.Sprintf("round %d/%d", round, rounds))
		implPrompt := implementerPrompt(ts)
		implTask, err := o.runRole(ctx, meter, agent.RoleImplementer, ScopeFull, implPrompt)
		if err != nil {
			o.setRole(meter, "implementer", session.SwarmRoleFailed, "")
			o.announce("Swarm aborted: implementer failed.")
			return err
		}
		if o.overBudget(meter) {
			break
		}
		ts.AddPatchNote(implTask.Summary)
		o.setRole(meter, "implementer", session.SwarmRoleDone, fmt.Sprintf("round %d/%d", round, rounds))

		o.setRole(meter, "tester", session.SwarmRoleActive, fmt.Sprintf("round %d/%d", round, rounds))
		testPrompt := testerPrompt(ts)
		testTask, err := o.runRole(ctx, meter, agent.RoleTester, ScopeTester, testPrompt)
		if err != nil {
			ts.AddFinding(Finding{Agent: "tester", Area: "tests", Content: "tester failed: " + err.Error()})
			o.setRole(meter, "tester", session.SwarmRoleFailed, "")
			break
		}
		if o.overBudget(meter) {
			break
		}
		ts.AddFinding(Finding{Agent: "tester", Area: "tests", Content: testTask.Summary})
		for _, tf := range parseTestFailures(testTask.Summary) {
			ts.AddTestFailure(tf)
		}

		pass, ok := ParseVerdict(testTask.Summary)
		if pass || !ok {
			o.setRole(meter, "tester", session.SwarmRoleDone, fmt.Sprintf("round %d/%d", round, rounds))
			break
		}
		o.setRole(meter, "tester", session.SwarmRoleDone, fmt.Sprintf("round %d/%d FAIL", round, rounds))
	}

	// 4. Reviewer (read-only). A reviewer failure is reported, not fatal:
	// the implementer's work is already in the working tree.
	o.setRole(meter, "reviewer", session.SwarmRoleActive, "")
	reviewPrompt := reviewerPrompt(ts)
	reviewTask, err := o.runRole(ctx, meter, agent.RoleReviewer, ScopeReadOnly, reviewPrompt)
	if err != nil {
		ts.SetFinalSummary("Reviewer failed: " + err.Error())
		o.setRole(meter, "reviewer", session.SwarmRoleFailed, "")
	} else {
		ts.SetFinalSummary(reviewTask.Summary)
		o.setRole(meter, "reviewer", session.SwarmRoleDone, "")
	}

	summary := ts.Render()
	if o.MaxTotalTokens > 0 {
		summary += fmt.Sprintf("\n\n_Token budget: ~%d / %d (estimated)._", meter.Total(), o.MaxTotalTokens)
	}
	o.State.AddMessage(session.RoleSystem, "Swarm complete.\n\n"+summary, session.ContentTypeMarkdown)
	o.announce("Swarm run complete.")
	return nil
}

func (o *Orchestrator) runRole(ctx context.Context, meter TokenMeter, role agent.AgentRole, scope RegistryScope, prompt string) (*agent.Task, error) {
	runner, err := o.NewRunner(role, scope)
	if err != nil {
		return nil, err
	}
	var hasRealUsage bool
	runner.UsageObserver = func(usage schema.TokenUsage) {
		hasRealUsage = true
		meter.Observe(role, usage)
		o.State.UpdateSwarmTokens(meter.Total(), o.MaxTotalTokens)
		o.State.UpdateSwarmRoleTokens(string(role), usage.PromptTokens+usage.CompletionTokens)
	}
	task, err := runner.RunTask(ctx, prompt)
	if err != nil {
		return nil, err
	}
	if !hasRealUsage {
		o.observe(meter, role, prompt, task.Summary)
	}
	return task, nil
}

func (o *Orchestrator) observe(meter TokenMeter, role agent.AgentRole, prompt, answer string) {
	meter.Observe(role, schema.TokenUsage{
		PromptTokens:     EstimateText(prompt),
		CompletionTokens: EstimateText(answer),
	})
	o.State.UpdateSwarmTokens(meter.Total(), o.MaxTotalTokens)
	o.State.UpdateSwarmRoleTokens(string(role), EstimateText(prompt)+EstimateText(answer))
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

// setRole updates the swarm roster and, on a role's first terminal
// transition, prints a `·` transcript event. The persistent roster panel
// that used to carry this information was deleted in the hairline-gutter
// redesign; live progress is the one-row strip, history is the
// transcript. Repeated Done updates (the tester loop reports one per
// round) announce only once.
func (o *Orchestrator) setRole(meter TokenMeter, name string, status session.SwarmRoleStatus, detail string) {
	prev := roleStatus(o.State.SwarmProgress(), name)
	o.State.UpdateSwarmRole(name, status, detail)
	if !terminalRoleStatus(status) || terminalRoleStatus(prev) {
		return
	}
	line := name + " " + string(status)
	if detail != "" {
		line += " · " + detail
	}
	if meter != nil && meter.Total() > 0 {
		line += " · ~" + strutil.CompactTokens(meter.Total()) + " tokens"
	}
	o.announce(line)
}

func terminalRoleStatus(s session.SwarmRoleStatus) bool {
	return s == session.SwarmRoleDone || s == session.SwarmRoleFailed
}

func roleStatus(p session.SwarmProgress, name string) session.SwarmRoleStatus {
	for _, r := range p.Roles {
		if r.Name == name {
			return r.Status
		}
	}
	return session.SwarmRolePending
}

// parseTestFailures extracts the TEST_FAILURES_JSON: [...] block from
// the tester's output. Returns nil on parse error or empty input.
func parseTestFailures(s string) []TestFailure {
	idx := strings.Index(s, "TEST_FAILURES_JSON:")
	if idx < 0 {
		return nil
	}
	rest := s[idx+len("TEST_FAILURES_JSON:"):]
	end := strings.Index(rest, "\n")
	if end < 0 {
		end = len(rest)
	}
	payload := strings.TrimSpace(rest[:end])
	if payload == "" || payload == "[]" {
		return nil
	}
	var failures []TestFailure
	if err := json.Unmarshal([]byte(payload), &failures); err != nil {
		return nil
	}
	return failures
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
