package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	// IssueNumber and IssueURL are set when the submission came from an
	// issue, so the exit path can link the pull request back to it.
	IssueNumber int
	IssueURL    string
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

	if req.Origin != OriginUI && !client.Autonomous {
		now := time.Now().UTC()
		p := PendingSpawn{
			ID: newAgentID(), Origin: req.Origin, ClientID: req.ClientID,
			Title: req.Title, RepoID: req.RepoID, Ref: req.Ref,
			Prompt: req.Prompt, Plan: req.Plan, Mode: req.Mode,
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
func (f *Fleet) Approve(ctx context.Context, pendingID string) (string, error) {
	p, ok := f.ws.PendingByID(pendingID)
	if !ok {
		return "", fmt.Errorf("bridge: unknown pending submission %s", pendingID)
	}
	if !p.ExpiresAt.IsZero() && time.Now().UTC().After(p.ExpiresAt) {
		_ = f.ws.DeletePending(pendingID)
		return "", fmt.Errorf("bridge: submission %s expired at %s", pendingID, p.ExpiresAt)
	}

	agentID, err := f.spawnFromRequest(ctx, SpawnRequest{
		Origin: p.Origin, ClientID: p.ClientID, RepoID: p.RepoID, Ref: p.Ref,
		Title: p.Title, Prompt: p.Prompt, Plan: p.Plan, Mode: p.Mode,
		IssueNumber: p.IssueNumber, IssueURL: p.IssueURL,
	})
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
	f.auditf(AuditEvent{Event: AuditPendingApproved, OwnerID: DefaultOwnerID,
		AgentID: agentID, ClientID: p.ClientID, RepoID: p.RepoID, Origin: p.Origin})
	return agentID, nil
}

// Deny discards a submission without starting anything.
func (f *Fleet) Deny(pendingID string) error {
	p, ok := f.ws.PendingByID(pendingID)
	if !ok {
		return fmt.Errorf("bridge: unknown pending submission %s", pendingID)
	}
	if err := f.ws.DeletePending(pendingID); err != nil {
		return err
	}
	f.auditf(AuditEvent{Event: AuditPendingDenied, OwnerID: DefaultOwnerID,
		ClientID: p.ClientID, RepoID: p.RepoID, Origin: p.Origin})
	return nil
}

// startPlan writes the submitted markdown into the agent's workspace and
// asks the agent to execute it.
//
// The plan goes to a file rather than over the wire because the existing
// execution path takes a planPath: session/sdd_start hands it to
// pipeline.ParsePlan, which reads a markdown plan from disk. That also
// keeps the bridge out of plan semantics entirely — it writes bytes.
func (f *Fleet) startPlan(ctx context.Context, agentID, pendingID, plan string) error {
	a, ok := f.ws.Agent(agentID)
	if !ok {
		return fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	path := planPathFor(a.Project, pendingID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create intake dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(plan), 0o600); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}

	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		return err
	}
	// path is the bridge-side location just written. Translating it —
	// rather than rebuilding the agent's view by hand — keeps knowledge
	// of what /work means in exactly one place.
	inAgent, perr := rt.agentPath(path)
	if perr != nil {
		return fmt.Errorf("resolve plan path for agent %s: %w", agentID, perr)
	}
	_, err = rt.child.Request(ctx, "session/sdd_start", map[string]any{
		"sessionId": rt.sessionID,
		"planPath":  inAgent,
	})
	return err
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
