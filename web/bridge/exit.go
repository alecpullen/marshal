package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// exitDestination reports how an agent's work leaves the fleet. It is
// determined by the agent's source, never chosen by the operator:
// SourceKind and ReadOnly already carry the answer.
func exitDestination(a Agent) string {
	if a.SourceKind != "git" {
		return "merge"
	}
	if a.ReadOnly {
		return "patch"
	}
	return "push"
}

// ExitOptions carries the operator's decisions for one exit attempt.
type ExitOptions struct {
	CommitMessage string `json:"commitMessage"`
	// Override, when non-nil, pushes despite a failed or skipped gate.
	Override *GateOverride `json:"override,omitempty"`
}

// ExitResult reports the outcome. Blocked means the gate refused and no
// override was supplied — nothing was pushed.
type ExitResult struct {
	Destination string      `json:"destination"`
	Branch      string      `json:"branch,omitempty"`
	PRUrl       string      `json:"prUrl,omitempty"`
	Verify      *gateResult `json:"verify,omitempty"`
	Blocked     bool        `json:"blocked,omitempty"`
}

// gateResult is the bridge-side mirror of acp.VerifyReply. It lives
// here rather than importing internal/acp because web/ must stay
// stdlib-only.
type gateResult struct {
	OK            bool   `json:"ok"`
	Skipped       bool   `json:"skipped"`
	FailedCommand string `json:"failedCommand,omitempty"`
	Output        string `json:"output,omitempty"`
}

// Exit runs the exit path for one agent: commit, verify, then push.
func (f *Fleet) Exit(ctx context.Context, agentID string, opts ExitOptions) (ExitResult, error) {
	a, ok := f.ws.Agent(agentID)
	if !ok {
		return ExitResult{}, fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	dest := exitDestination(a)
	if dest != "push" {
		return ExitResult{Destination: dest}, nil
	}
	if opts.Override != nil && strings.TrimSpace(opts.Override.Reason) == "" {
		return ExitResult{}, fmt.Errorf("bridge: a gate override requires a reason")
	}

	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		return ExitResult{}, err
	}

	// 1. Commit. A clean tree means the agent already committed.
	if _, err := f.commitSession(ctx, rt, opts.CommitMessage); err != nil {
		return ExitResult{}, err
	}

	// 2. Verify, agent-side: the toolchain is in the container.
	verify, err := f.verifySession(ctx, rt)
	if err != nil {
		return ExitResult{}, err
	}

	// 3. Gate. Skipped blocks as firmly as a failure.
	passed := verify.OK && !verify.Skipped
	if !passed && opts.Override == nil {
		return ExitResult{Destination: dest, Verify: verify, Blocked: true}, nil
	}

	// 4. Push — the only bridge-side git operation at exit.
	branch := branchNameFor(a)
	cred, err := f.credentialForAgent(a)
	if err != nil {
		return ExitResult{}, err
	}
	out, err := f.git.Push(a.Project, branch, cred)
	if err != nil {
		return ExitResult{}, err
	}

	a.Branch = branch
	a.PushedAt = time.Now().UTC()
	if url := extractPRURL(out, f.remoteURLFor(a)); url != "" {
		a.PRUrl = url
	}
	if !passed {
		rec := *opts.Override
		rec.At = time.Now().UTC()
		rec.By = a.OwnerID
		rec.FailedCommand = verify.FailedCommand
		rec.Skipped = verify.Skipped
		a.GateOverride = &rec
	}
	if err := f.ws.PutAgent(a); err != nil {
		return ExitResult{}, fmt.Errorf("persist exit state for %s: %w", a.ID, err)
	}

	return ExitResult{Destination: dest, Branch: branch, PRUrl: a.PRUrl, Verify: verify}, nil
}

// commitSession issues session/commit through the agent's child.
func (f *Fleet) commitSession(ctx context.Context, rt *agentRuntime, message string) (json.RawMessage, error) {
	params := map[string]any{
		"sessionId": f.sessionIDFor(rt),
	}
	if message != "" {
		params["message"] = message
	}
	return rt.child.Request(ctx, "session/commit", params)
}

// verifySession issues session/verify through the agent's child and
// decodes the gate result.
func (f *Fleet) verifySession(ctx context.Context, rt *agentRuntime) (*gateResult, error) {
	raw, err := rt.child.Request(ctx, "session/verify", map[string]any{
		"sessionId": f.sessionIDFor(rt),
	})
	if err != nil {
		return nil, err
	}
	var res gateResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode verify result: %w", err)
	}
	return &res, nil
}

// branchNameFor returns the push target branch for an agent.
func branchNameFor(a Agent) string {
	name := a.Name
	if name == "" {
		name = a.ID
	}
	return "marshal/" + name
}

// credentialForAgent resolves the credential for a git-sourced agent
// through the credential store.
func (f *Fleet) credentialForAgent(a Agent) (Credential, error) {
	// For a registered repo, resolve via its CredRef.
	// For a raw URL (read-only), no credential is needed.
	if a.ReadOnly {
		return Credential{Kind: "none"}, nil
	}
	// Look up the repo to find the CredRef.
	// The SourceRef for a registered repo is the repo ID.
	r, ok := f.ws.Repo(a.SourceRef)
	if !ok {
		return Credential{Kind: "none"}, nil
	}
	if r.CredRef == "" {
		return Credential{Kind: "none"}, nil
	}
	return f.creds.Resolve(a.OwnerID, r.CredRef)
}

// remoteURLFor returns the remote URL for PR extraction.
func (f *Fleet) remoteURLFor(a Agent) string {
	if a.SourceRef != "" {
		// For a registered repo, look up the URL.
		if r, ok := f.ws.Repo(a.SourceRef); ok {
			return r.URL
		}
		// For a raw URL, SourceRef is the URL itself.
		return a.SourceRef
	}
	return ""
}
