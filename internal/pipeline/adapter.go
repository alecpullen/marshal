package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

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
		p.Finished = false
		p.Succeeded = false
		p.PlanName = a.c.Plan.Slug
		p.PlanPath = a.c.Plan.Path
		p.TotalTasks = len(a.c.Plan.Tasks)
		p.MaxFixRounds = a.c.MaxFixRounds
		p.StartedAt = time.Now()
		titles := make([]string, len(a.c.Plan.Tasks))
		for i, t := range a.c.Plan.Tasks {
			titles[i] = t.Title
		}
		p.Tasks = titles
	})
	if completed, err := a.c.CompletedCount(); err == nil && completed > 0 {
		a.state.AddMessage(session.RoleSystem, fmt.Sprintf("Resuming %s at task %d/%d (ledger from previous run)", a.c.Plan.Slug, completed+1, len(a.c.Plan.Tasks)), session.ContentTypePlain)
	}
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
		// Terminal success: the run's worktree is scratch space. Remove it
		// so stale checkouts stop accumulating inside the repo. The branch
		// carries the work; failures are resumable and keep their worktree.
		// Cleanup is best-effort — a dirty-worktree refusal is logged, never
		// forced, and never fails the run.
		if cerr := a.c.CleanupWorktrees(); cerr != nil {
			a.state.Logger().Warn("pipeline worktree cleanup failed; worktree kept", "error", cerr, "path", a.c.Worktree.Path)
		} else {
			a.state.AddMessage(session.RoleSystem, fmt.Sprintf("Pipeline worktree removed; branch %s retained for review and merge.", a.c.Worktree.Branch), session.ContentTypePlain)
		}
	}
	// The run is over — success or failure — so the live chrome (run-panel
	// spinner, input dimming) comes down and the panel collapses to its
	// one-line summary. The gate path above returns early and intentionally
	// keeps progress live for the resume.
	a.state.FinishSDDRun(err == nil, time.Now(), err)
	return err
}

// Event mirrors one controller event into session state: the scalars drive
// the run panel, the payload becomes a durable entry in the run event log.
func (a *ControllerAdapter) Event(ev Event) {
	a.state.UpdateSDDProgress(func(p *session.SDDProgress) {
		p.CurrentTask = ev.TaskN
		p.TotalTasks = ev.TotalTasks
		p.Phase = ev.Phase
		p.Detail = ev.Detail
		p.FixRound = ev.FixRound
		p.MaxFixRounds = ev.MaxFixRounds
		if ev.Phase != p.Phase {
			p.PhaseStartedAt = time.Now()
		}
		if ev.Phase == PhaseDone {
			p.DoneTasks++
		}
	})
	a.recordRunEvents(ev)
}

// recordRunEvents flattens an event's payload into the session run log.
// A nil or unrecognised payload records nothing — the log is best-effort
// detail, never a source of errors.
func (a *ControllerAdapter) recordRunEvents(ev Event) {
	switch p := ev.Payload.(type) {
	case VerifyFailedPayload:
		a.state.AddRunEvent(session.RunEvent{
			Kind:   session.RunEventVerifyFailed,
			TaskN:  ev.TaskN,
			Title:  p.Command,
			Detail: fmt.Sprintf("fix round %d/%d", p.Round, p.MaxRounds),
			Body:   p.Output,
		})
	case GateSkippedPayload:
		a.state.AddRunEvent(session.RunEvent{
			Kind:  session.RunEventGateSkipped,
			TaskN: ev.TaskN,
			Title: p.Reason,
		})
	case ReviewPayload:
		if p.Clean {
			a.state.AddRunEvent(session.RunEvent{
				Kind: session.RunEventReview, TaskN: ev.TaskN, Detail: "clean",
			})
			return
		}
		for _, f := range p.Findings {
			a.state.AddRunEvent(session.RunEvent{
				Kind:     session.RunEventReview,
				TaskN:    ev.TaskN,
				Title:    f.Text,
				Severity: string(f.Severity),
			})
		}
	case CommitPayload:
		a.state.AddRunEvent(session.RunEvent{
			Kind: session.RunEventCommit, TaskN: ev.TaskN, Title: p.SHA, Detail: p.Subject,
		})
	case RetryPayload:
		a.state.AddRunEvent(session.RunEvent{
			Kind:   session.RunEventRetry,
			TaskN:  ev.TaskN,
			Title:  fmt.Sprintf("retry %d/%d", p.Attempt, p.MaxAttempts),
			Detail: p.Err,
		})
	case ConcernPayload:
		a.state.AddRunEvent(session.RunEvent{
			Kind: session.RunEventConcern, TaskN: ev.TaskN, Title: p.Text,
		})
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
