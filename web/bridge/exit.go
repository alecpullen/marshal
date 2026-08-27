package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

	// Prefer an API-created pull request: it carries a title, a body,
	// draft state and an issue link, none of which a pushed URL can.
	//
	// Every failure here falls through to extraction rather than
	// returning. The push has already succeeded by this point, and
	// failing the exit would strand work that is safely on the remote.
	if pr, err := f.createPR(ctx, a, branch, verify); err == nil {
		a.PRUrl = pr.URL
	} else {
		if !errors.Is(err, errNoForge) {
			slog.Default().Warn("webbridge: create pull request failed; falling back to the pushed URL",
				"agent", a.ID, "err", err)
		}
		if url := extractPRURL(out, f.remoteURLFor(a)); url != "" {
			a.PRUrl = url
		}
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

// forgeFor resolves the forge and HTTP-capable credential for a repo.
// It returns errNoForge when the repo has no forge declared or its
// credential cannot make HTTP API calls — the documented degradation
// path, not an error worth logging.
func (f *Fleet) forgeFor(repo Repo) (Forge, Credential, error) {
	if repo.Forge == "" {
		return nil, Credential{}, errNoForge
	}
	cred, err := f.creds.Resolve(repo.OwnerID, repo.CredRef)
	if err != nil {
		return nil, Credential{}, err
	}
	if cred.Kind != "pat" {
		return nil, Credential{}, errNoForge
	}
	forge, err := ForgeFor(repo, forgeHTTPClient)
	if err != nil {
		return nil, Credential{}, err
	}
	return forge, cred, nil
}

// createPR creates a pull request through the forge API when possible.
// Returns errNoForge when the repo has no forge or a non-pat credential.
func (f *Fleet) createPR(ctx context.Context, a Agent, branch string, verify *gateResult) (PR, error) {
	repo, ok := f.ws.Repo(a.SourceRef)
	if !ok {
		return PR{}, errNoForge
	}
	forge, cred, err := f.forgeFor(repo)
	if err != nil {
		return PR{}, err
	}
	return forge.CreatePR(ctx, repo, PRRequest{
		Title: a.Name,
		Body:  prBody(a, verify),
		Head:  branch,
		Base:  a.TargetBranch,
	}, cred)
}

// prBody assembles the pull request body: what the agent did, the
// verify outcome, "Closes #N" when the agent was spawned from an issue,
// and the override reason when a gate override was applied.
func prBody(a Agent, verify *gateResult) string {
	var b strings.Builder
	if a.Prompt != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Prompt)
	}
	if verify != nil {
		if verify.OK {
			b.WriteString("Verification: passed\n")
		} else if verify.Skipped {
			b.WriteString("Verification: skipped (no commands found)\n")
		} else {
			fmt.Fprintf(&b, "Verification: failed (%s)\n", verify.FailedCommand)
		}
	}
	if a.IssueNumber != 0 {
		fmt.Fprintf(&b, "\nCloses #%d\n", a.IssueNumber)
	}
	if a.GateOverride != nil {
		fmt.Fprintf(&b, "\nGate override: %s\n", a.GateOverride.Reason)
		if a.GateOverride.FailedCommand != "" {
			fmt.Fprintf(&b, "Failed command: %s\n", a.GateOverride.FailedCommand)
		}
	}
	return b.String()
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
