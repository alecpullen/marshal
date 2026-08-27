package bridge

import (
	"context"
	"errors"
	"fmt"
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
			return SubmitResult{}, fmt.Errorf("%w: %s", ErrRepoNotAllowed, req.RepoID)
		}
		if err := f.checkCaps(client); err != nil {
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

// checkCaps enforces the client's concurrency and daily budgets.
func (f *Fleet) checkCaps(c MCPClient) error {
	if c.MaxConcurrent <= 0 && c.MaxPerDay <= 0 {
		return nil
	}
	var live, today int
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	for _, a := range f.ws.Agents() {
		if a.Origin != OriginMCP {
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
