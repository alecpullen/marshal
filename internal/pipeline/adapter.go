package pipeline

import (
	"context"
	"errors"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/policy"
)

// ControllerAdapter adapts *Controller to the TUI's AgentRunner interface
// and mirrors controller events into session state for the panel. The
// pipeline package does not import the TUI; the TUI reads session state.
type ControllerAdapter struct {
	c     *Controller
	state *session.State
}

func NewControllerAdapter(c *Controller, state *session.State) *ControllerAdapter {
	a := &ControllerAdapter{c: c, state: state}
	if c != nil {
		c.Observer = a
	}
	return a
}

// Run executes (or resumes) the run. goal is the plan path the TUI picked;
// it must match the controller's plan.
func (a *ControllerAdapter) Run(ctx context.Context, goal string) error {
	if a.c == nil {
		return errors.New("pipeline: no controller")
	}
	a.state.UpdateSDDProgress(func(p *session.SDDProgress) {
		p.Active = true
		p.PlanName = a.c.Plan.Slug
		p.PlanPath = a.c.Plan.Path
		p.TotalTasks = len(a.c.Plan.Tasks)
		p.MaxFixRounds = a.c.MaxFixRounds
	})
	err := a.c.Run(ctx)
	a.state.UpdateSDDProgress(func(p *session.SDDProgress) {
		p.Branch = a.c.Worktree.Branch
	})
	if errors.Is(err, ErrHumanGateRequired) {
		a.state.SetSDDGate(session.SDDGate{TaskN: a.c.nextTask, Question: a.c.Question()})
		return err
	}
	if err == nil {
		a.state.AddMessage(session.RoleSystem, a.c.Summary(), session.ContentTypePlain)
		a.state.UpdateSDDProgress(func(p *session.SDDProgress) { p.Phase = PhaseDone })
	}
	return err
}

// Event mirrors one controller event into session state.
func (a *ControllerAdapter) Event(ev Event) {
	a.state.UpdateSDDProgress(func(p *session.SDDProgress) {
		p.CurrentTask = ev.TaskN
		p.TotalTasks = ev.TotalTasks
		p.Phase = ev.Phase
		p.Detail = ev.Detail
		p.FixRound = ev.FixRound
		p.MaxFixRounds = ev.MaxFixRounds
		if ev.Phase == PhaseDone {
			p.DoneTasks++
		}
	})
	if lines, err := a.c.Ledger.Tail(1); err == nil && len(lines) == 1 {
		a.state.UpdateSDDProgress(func(p *session.SDDProgress) { p.LastLedger = lines[0] })
	}
}

// AnswerGate hands the human's answer to the controller and clears the
// gate. The TUI then calls Run again to resume.
func (a *ControllerAdapter) AnswerGate(answer string) {
	if a.c == nil {
		return
	}
	a.c.Answer(answer)
	a.state.ClearSDDGate()
}

// Controller exposes the underlying controller for the TUI's plan-path
// lookup and for tests.
func (a *ControllerAdapter) Controller() *Controller { return a.c }

// The pipeline runs its own per-role runners; these knobs belong to the
// interactive runner and are no-ops here.
func (a *ControllerAdapter) SetForceClass(string)                   {}
func (a *ControllerAdapter) SetPolicyRules([]config.PermissionRule) {}
func (a *ControllerAdapter) SetApprovalMode(policy.ApprovalMode)    {}
