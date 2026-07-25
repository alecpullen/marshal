package sdd

import (
	"context"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
)

// ControllerAdapter wraps *Controller to satisfy the TUI's AgentRunner
// interface. The controller does not use ForceClass/PolicyRules/ApprovalMode
// (those live on the per-role runners built by the factory); the setters
// are no-ops so the adapter drops into the existing /sdd dispatch path.
type ControllerAdapter struct {
	c *Controller
}

func NewControllerAdapter(c *Controller) *ControllerAdapter {
	return &ControllerAdapter{c: c}
}

// Run sets the plan path (the TUI passes the picked plan file as the
// "goal") then runs the controller state machine. The controller returns
// ErrHumanGateRequired when it reaches a human gate; the TUI renders the
// gate, collects the user's resolution, and calls Run again — the
// controller resumes from its saved State field.
func (a *ControllerAdapter) Run(ctx context.Context, goal string) error {
	a.c.PlanPath = goal
	return a.c.Run(ctx)
}

func (a *ControllerAdapter) SetForceClass(string)        {}
func (a *ControllerAdapter) SetPolicyRules([]config.PermissionRule) {}
func (a *ControllerAdapter) SetApprovalMode(policy.ApprovalMode)    {}
