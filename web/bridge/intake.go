package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrCapExceeded    = errors.New("bridge: client cap exceeded")
	ErrRepoNotAllowed = errors.New("bridge: repo not allowed for this client")
)

// pendingTTL is how long an unconfirmed submission survives.
const pendingTTL = 24 * time.Hour

// SpawnRequest is one submission, whatever adapter produced it.
type SpawnRequest struct {
	Origin   string
	ClientID string
	RepoID   string
	Ref      string
	// Title is what the operator reads when confirming. Required, and
	// untrusted: it originates from the submitting client.
	Title  string
	Prompt string
	// Plan is markdown in pipeline.ParsePlan's format. Mutually
	// exclusive with Prompt.
	Plan string
	Mode string
	// Auto is the optional Auto modifier of the requested starting tuple.
	// Nil means the submission named none; an explicit false is preserved.
	Auto *bool
	// IssueNumber and IssueURL are set when the submission came from an
	// issue, so the exit path can link the pull request back to it.
	IssueNumber int
	IssueURL    string
	// Project, TaskID and AssignmentID carry a coordinated assignment's target
	// and identity through intake (agent-first Studio, task 6). They are set
	// only by AssignmentDispatch, which is reached from a coordinator dispatch
	// after its own access check — never from a request body or a tool
	// argument — so an adapter cannot claim an assignment identity.
	Project      string
	TaskID       string
	AssignmentID string
}

// AssignmentDispatch is one coordinated assignment as the intake and worker
// paths need it: the dispatch identity that must follow the submission into its
// worker, and the project and instruction it targets.
//
// The identity is the assignment's own ID (agent-first Studio, task 6). It is
// written by the coordinator store before this is called, and everything below
// resolves it: the pending submission's ID, the spawn claim's request ID and the
// worker's agent record all carry it, so a retry, a confirmation, a reconnect
// and a restart meet at one outcome.
type AssignmentDispatch struct {
	TaskID       string
	AssignmentID string
	// Project is the target project's root, resolved from its canonical ID by
	// the caller. A dispatch never names a display name or a path from a model.
	Project string
	Title   string
	Prompt  string
	Mode    string
	Auto    *bool
}

// validate refuses a dispatch that could not be resolved to one worker.
func (d AssignmentDispatch) validate() error {
	if strings.TrimSpace(d.TaskID) == "" || strings.TrimSpace(d.AssignmentID) == "" {
		return fmt.Errorf("bridge: an assignment dispatch needs its task and assignment identity")
	}
	if strings.TrimSpace(d.Project) == "" {
		return fmt.Errorf("bridge: an assignment dispatch needs a target project")
	}
	return nil
}

// StartAssignment starts the worker one authorized assignment resolves to, at
// most once, and reports whether its instruction still has to be delivered.
//
// The assignment's identity is the spawn claim's request ID, so the worker's
// identity is settled durably before the spawn has any side effect: a dispatch
// interrupted before or after the worker existed resumes under the same
// identity, and a replay of a request that already delivered its instruction
// answers with the same worker and delivers nothing.
//
// It is reached only by a coordinator dispatch that authorized the target under
// the access policy, and by the confirmation of the submission such a dispatch
// stored. Nothing here presents an operator identity: the origin is the
// coordinator's own.
func (f *Fleet) StartAssignment(ctx context.Context, d AssignmentDispatch) (string, bool, error) {
	if err := d.validate(); err != nil {
		return "", false, err
	}
	opts := SpawnOptions{
		Name: d.Title, Mode: d.Mode, Auto: d.Auto, Prompt: d.Prompt,
		Origin:                  OriginCoordinator,
		CoordinatorTaskID:       d.TaskID,
		CoordinatorAssignmentID: d.AssignmentID,
	}
	return f.SpawnClaimed(ctx, d.Project, opts, d.AssignmentID)
}

// QueueAssignment stores one assignment's instruction as a submission awaiting
// confirmation, carrying the assignment identity into the queue.
//
// The pending submission's ID is the assignment's own identity, so the same
// dispatch can never queue two submissions for one target, and confirming it
// starts the worker that identity settled.
func (f *Fleet) QueueAssignment(d AssignmentDispatch) (PendingSpawn, error) {
	if err := d.validate(); err != nil {
		return PendingSpawn{}, err
	}
	if strings.TrimSpace(d.Title) == "" {
		return PendingSpawn{}, fmt.Errorf("bridge: a submission requires a title")
	}
	if d.Prompt == "" {
		return PendingSpawn{}, fmt.Errorf("bridge: a submission requires a prompt or a plan")
	}
	now := f.now()
	p := PendingSpawn{
		ID: d.AssignmentID, Origin: OriginCoordinator,
		Title: d.Title, Prompt: d.Prompt, Mode: d.Mode, Auto: d.Auto,
		CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
		Project: d.Project,
		TaskID:  d.TaskID, AssignmentID: d.AssignmentID,
	}
	if err := f.ws.PutPending(p); err != nil {
		return PendingSpawn{}, fmt.Errorf("persist pending submission: %w", err)
	}
	return p, nil
}

// deliverInstruction hands one assignment's instruction to the worker that
// acknowledged its session.
//
// The durable claim recorded the delivery before this is called, so a retry
// never sends the same instruction twice; a delivery lost to a crash is
// recovered from the assignment's thread, where a duplicate turn would be a
// second instruction nobody gave.
func (f *Fleet) deliverInstruction(agentID, prompt string) {
	if strings.TrimSpace(agentID) == "" || strings.TrimSpace(prompt) == "" {
		return
	}
	go func() {
		rt, err := f.RuntimeForSession(agentID)
		if err != nil {
			slog.Default().Warn("webbridge: assignment instruction not delivered", "agent", agentID, "err", err)
			return
		}
		if err := rt.reg.Prompt(context.Background(), f.sessionIDFor(rt), prompt); err != nil {
			slog.Default().Warn("webbridge: assignment instruction failed", "agent", agentID, "err", err)
		}
	}()
}

// SubmitResult reports what happened. Status is "pending" or "running".
type SubmitResult struct {
	AgentID   string `json:"agentId,omitempty"`
	PendingID string `json:"pendingId,omitempty"`
	Status    string `json:"status"`
}

// Submit is the single origin-tagged intake entry point. Every adapter
// goes through it so policy is enforced once rather than per-adapter.
func (f *Fleet) Submit(ctx context.Context, req SpawnRequest) (SubmitResult, error) {
	if strings.TrimSpace(req.Title) == "" {
		return SubmitResult{}, fmt.Errorf("bridge: a submission requires a title")
	}
	if req.Prompt == "" && req.Plan == "" {
		return SubmitResult{}, fmt.Errorf("bridge: a submission requires a prompt or a plan")
	}

	// A non-UI origin may only target a registered repo: the registry is
	// the allowlist that stops a compromised client aiming an agent at
	// an unvetted remote.
	if req.Origin != OriginUI {
		if _, ok := f.ws.Repo(req.RepoID); !ok {
			f.auditf(AuditEvent{Event: AuditSpawnDenied, OwnerID: DefaultOwnerID,
				ClientID: req.ClientID, RepoID: req.RepoID, Origin: req.Origin,
				Reason: "unregistered repo"})
			return SubmitResult{}, fmt.Errorf("%w: %s", ErrUnregisteredRepo, req.RepoID)
		}
	}

	var client MCPClient
	if req.ClientID != "" {
		c, ok := f.clientByID(req.ClientID)
		if !ok {
			return SubmitResult{}, fmt.Errorf("bridge: unknown client %s", req.ClientID)
		}
		client = c
		if !repoAllowed(client, req.RepoID) {
			f.auditf(AuditEvent{Event: AuditSpawnDenied, OwnerID: DefaultOwnerID,
				ClientID: req.ClientID, RepoID: req.RepoID, Origin: req.Origin,
				Reason: "repo not allowed for client"})
			return SubmitResult{}, fmt.Errorf("%w: %s", ErrRepoNotAllowed, req.RepoID)
		}
		if err := f.checkCaps(client); err != nil {
			f.auditf(AuditEvent{Event: AuditSpawnDenied, OwnerID: DefaultOwnerID,
				ClientID: req.ClientID, RepoID: req.RepoID, Origin: req.Origin,
				Reason: err.Error()})
			return SubmitResult{}, err
		}
	}

	// Sweeping here as well as on load keeps the queue bounded without a
	// background timer.
	f.ws.SweepExpired(time.Now().UTC())

	// Validate the requested starting tuple before anything is queued or
	// spawned, and canonicalize it in place: a spelling the bridge cannot read
	// must fail the submission where the client sends it, not surface later
	// when a pending entry is approved.
	mode, auto, _, err := canonicalModeTuple(req.Mode, req.Auto)
	if err != nil {
		return SubmitResult{}, err
	}
	req.Mode, req.Auto = mode, auto

	if req.Origin != OriginUI && !client.Autonomous {
		now := time.Now().UTC()
		p := PendingSpawn{
			ID: newAgentID(), Origin: req.Origin, ClientID: req.ClientID,
			Title: req.Title, RepoID: req.RepoID, Ref: req.Ref,
			Prompt: req.Prompt, Plan: req.Plan, Mode: req.Mode, Auto: req.Auto,
			CreatedAt: now, ExpiresAt: now.Add(pendingTTL),
			IssueNumber: req.IssueNumber, IssueURL: req.IssueURL,
		}
		if err := f.ws.PutPending(p); err != nil {
			return SubmitResult{}, fmt.Errorf("persist pending submission: %w", err)
		}
		return SubmitResult{PendingID: p.ID, Status: "pending"}, nil
	}

	id, err := f.spawnFromRequest(ctx, req)
	if err != nil {
		return SubmitResult{}, err
	}
	return SubmitResult{AgentID: id, Status: "running"}, nil
}

// repoAllowed reports whether a client may target a repo. An empty
// AllowedRepos means every registered repo.
func repoAllowed(c MCPClient, repoID string) bool {
	if len(c.AllowedRepos) == 0 {
		return true
	}
	for _, r := range c.AllowedRepos {
		if r == repoID {
			return true
		}
	}
	return false
}

// intakeDir is where an approved submission's plan is written, relative
// to the agent's workspace.
const intakeDir = ".marshal/intake"

// planPathFor is the destination for a submitted plan.
//
// The path is derived entirely from the workspace and the pending id,
// and the id is sanitised to a bare base name: a client-influenced path
// component here would be an arbitrary write into the host filesystem.
func planPathFor(workDir, pendingID string) string {
	safe := filepath.Base(filepath.Clean("/" + pendingID))
	if safe == "." || safe == string(filepath.Separator) || safe == ".." {
		safe = "plan"
	}
	return filepath.Join(workDir, intakeDir, safe+".md")
}

// Approve turns a confirmed submission into a running agent.
//
// A coordinated assignment's submission (agent-first Studio, task 6) carries the
// dispatch identity its coordinator stored before this submission existed, so
// confirming it starts the worker that identity settled and hands it the
// assignment's instruction once — never a second worker, and never an operator
// identity the model did not have.
func (f *Fleet) Approve(ctx context.Context, pendingID string) (string, error) {
	p, ok := f.ws.PendingByID(pendingID)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownPending, pendingID)
	}
	// The fleet's clock, not the wall clock: QueueAssignment stamps ExpiresAt
	// from it, so checking against a different source makes a submission that
	// was just queued unapprovable whenever the clock is pinned ahead.
	if !p.ExpiresAt.IsZero() && f.now().After(p.ExpiresAt) {
		_ = f.ws.DeletePending(pendingID)
		return "", fmt.Errorf("%w: %s expired at %s", ErrUnknownPending, pendingID, p.ExpiresAt)
	}

	var (
		agentID string
		deliver bool
		err     error
	)
	if p.TaskID != "" {
		agentID, deliver, err = f.StartAssignment(ctx, AssignmentDispatch{
			TaskID: p.TaskID, AssignmentID: p.AssignmentID, Project: p.Project,
			Title: p.Title, Prompt: p.Prompt, Mode: p.Mode, Auto: p.Auto,
		})
	} else {
		agentID, err = f.spawnFromRequest(ctx, SpawnRequest{
			Origin: p.Origin, ClientID: p.ClientID, RepoID: p.RepoID, Ref: p.Ref,
			Title: p.Title, Prompt: p.Prompt, Plan: p.Plan, Mode: p.Mode, Auto: p.Auto,
			IssueNumber: p.IssueNumber, IssueURL: p.IssueURL,
		})
	}
	if err != nil {
		return "", err
	}
	if err := f.ws.DeletePending(pendingID); err != nil {
		return "", fmt.Errorf("clear pending submission: %w", err)
	}
	if p.Plan != "" {
		if err := f.startPlan(ctx, agentID, pendingID, p.Plan); err != nil {
			return "", err
		}
	}
	if deliver {
		f.deliverInstruction(agentID, p.Prompt)
	}
	f.auditf(AuditEvent{Event: AuditPendingApproved, OwnerID: DefaultOwnerID,
		AgentID: agentID, ClientID: p.ClientID, RepoID: p.RepoID, Origin: p.Origin})
	return agentID, nil
}

// Deny discards a submission without starting anything.
func (f *Fleet) Deny(pendingID string) error {
	p, ok := f.ws.PendingByID(pendingID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownPending, pendingID)
	}
	if err := f.ws.DeletePending(pendingID); err != nil {
		return err
	}
	f.auditf(AuditEvent{Event: AuditPendingDenied, OwnerID: DefaultOwnerID,
		ClientID: p.ClientID, RepoID: p.RepoID, Origin: p.Origin})
	return nil
}

// startPlan writes a submitted plan into the agent's workspace and asks
// the agent to execute it. It is StartRun for an intake submission: the
// pending id names the plan file.
func (f *Fleet) startPlan(ctx context.Context, agentID, pendingID, plan string) error {
	return f.startRun(ctx, agentID, RunRequest{Kind: RunSDD, Plan: plan}, pendingID)
}

// checkCaps enforces the client's concurrency and daily budgets.
//
// Counts are scoped to the calling client: one client's agents must not
// count against another's cap.
func (f *Fleet) checkCaps(c MCPClient) error {
	if c.MaxConcurrent <= 0 && c.MaxPerDay <= 0 {
		return nil
	}
	var live, today int
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	for _, a := range f.ws.Agents() {
		if a.Origin != OriginMCP || a.ClientID != c.ID {
			continue
		}
		if a.CreatedAt.After(cutoff) {
			today++
		}
		if _, err := f.runtimeForAgent(a.ID); err == nil {
			live++
		}
	}
	if c.MaxConcurrent > 0 && live >= c.MaxConcurrent {
		return fmt.Errorf("%w: %d concurrent agents", ErrCapExceeded, live)
	}
	if c.MaxPerDay > 0 && today >= c.MaxPerDay {
		return fmt.Errorf("%w: %d agents in the last day", ErrCapExceeded, today)
	}
	return nil
}
