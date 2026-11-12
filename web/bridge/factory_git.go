package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type factoryGit struct{ fleet *Fleet }

func newFactoryGit(fleet *Fleet) *factoryGit { return &factoryGit{fleet: fleet} }

var factorySafeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,79}$`)

func (g *factoryGit) checkout(item FactoryItem, worker FactoryWorker) (Agent, *agentRuntime, error) {
	if !factorySafeID.MatchString(item.ID) || item.Branch != "marshal/factory/"+item.ID {
		return Agent{}, nil, fmt.Errorf("factory: unsafe item ID or branch")
	}
	a, ok := g.fleet.ws.Agent(worker.AgentID)
	if !ok || a.FactoryItemID != item.ID || a.FactoryRole != worker.Role || a.FactorySpawnEffectID != worker.SpawnEffectID || a.SessionID != worker.SessionID || a.FactoryParked || a.SourceKind != "git" || a.SourceRef != item.RepoID {
		return Agent{}, nil, fmt.Errorf("factory: checkout ownership mismatch")
	}
	rt, err := g.fleet.runtimeForAgent(worker.AgentID)
	if err != nil {
		return Agent{}, nil, err
	}
	if g.fleet.sessionIDFor(rt) != worker.SessionID || g.fleet.git == nil {
		return Agent{}, nil, fmt.Errorf("factory: checkout runtime mismatch")
	}
	return a, rt, nil
}

func (g *factoryGit) git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := g.fleet.git.runCtx(ctx, dir, Credential{}, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *factoryGit) state(ctx context.Context, dir string) (head, tree, branch string, dirty bool, err error) {
	if head, err = g.git(ctx, dir, "rev-parse", "HEAD"); err != nil {
		return
	}
	if tree, err = g.git(ctx, dir, "rev-parse", "HEAD^{tree}"); err != nil {
		return
	}
	if branch, err = g.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err != nil {
		return
	}
	var status string
	if status, err = g.git(ctx, dir, "status", "--porcelain", "--untracked-files=all"); err != nil {
		return
	}
	dirty = status != ""
	return
}

// Seed creates the one owned branch and commits the exact approved inputs via ACP.
func (g *factoryGit) Seed(ctx context.Context, item FactoryItem, worker FactoryWorker) (head, tree string, err error) {
	a, rt, err := g.checkout(item, worker)
	if err != nil {
		return "", "", err
	}
	if worker.Role != "implementer" || item.BaseSHA == "" || len(item.Approvals) == 0 {
		return "", "", fmt.Errorf("factory: seed requires an approved implementer and base")
	}
	old, _, _, dirty, err := g.state(ctx, a.Project)
	if err != nil { // A prepared checkout starts detached; read HEAD without a branch.
		old, err = g.git(ctx, a.Project, "rev-parse", "HEAD")
		if err != nil {
			return "", "", err
		}
		status, e := g.git(ctx, a.Project, "status", "--porcelain", "--untracked-files=all")
		if e != nil {
			return "", "", e
		}
		dirty = status != ""
	}
	if dirty || old != item.BaseSHA {
		return "", "", fmt.Errorf("factory: seed checkout is dirty or not at approved base")
	}
	if _, err = g.git(ctx, a.Project, "show-ref", "--verify", "refs/heads/"+item.Branch); err == nil {
		return "", "", fmt.Errorf("factory: branch already exists")
	}
	if _, err = g.git(ctx, a.Project, "switch", "-c", item.Branch); err != nil {
		return "", "", err
	}
	base := filepath.Join(a.Project, "docs", "marshal-factory")
	dir := filepath.Join(base, item.ID)
	for _, p := range []string{filepath.Join(a.Project, "docs"), base, dir} {
		info, e := os.Lstat(p)
		if e == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return "", "", fmt.Errorf("factory: unsafe docs path %s", p)
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", "", e
		}
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	ap := item.Approvals[len(item.Approvals)-1]
	for name, data := range map[string]string{"plan.md": ap.Plan, "spec.md": ap.Spec} {
		p := filepath.Join(dir, name)
		if _, e := os.Lstat(p); e == nil {
			return "", "", fmt.Errorf("factory: seed file already exists")
		} else if !errors.Is(e, os.ErrNotExist) {
			return "", "", e
		}
		if err = os.WriteFile(p, []byte(data), 0o644); err != nil {
			return "", "", err
		}
	}
	if _, err = g.fleet.commitSession(ctx, rt, "Seed factory plan and spec for "+item.ID); err != nil {
		return "", "", err
	}
	head, tree, branch, dirty, err := g.state(ctx, a.Project)
	if err != nil {
		return "", "", err
	}
	if branch != item.Branch || dirty || head == old {
		return "", "", fmt.Errorf("factory: seed commit did not produce a clean owned branch")
	}
	return head, tree, nil
}

func (g *factoryGit) remoteHead(ctx context.Context, dir, branch string, cred Credential) (string, error) {
	out, err := g.fleet.git.runCtx(ctx, dir, cred, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", nil
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch {
		return "", fmt.Errorf("factory: unexpected remote ref response")
	}
	return fields[0], nil
}

// Publish only sends the observed local HEAD when the remote still equals the
// controller's expected SHA. A lost push response is resolved by rereading it.
func (g *factoryGit) Publish(ctx context.Context, item FactoryItem, worker FactoryWorker, expectedRemote string) (head, tree string, err error) {
	a, _, err := g.checkout(item, worker)
	if err != nil {
		return "", "", err
	}
	if worker.Role != "implementer" {
		return "", "", fmt.Errorf("factory: only implementer can publish")
	}
	head, tree, branch, dirty, err := g.state(ctx, a.Project)
	if err != nil {
		return "", "", err
	}
	if branch != item.Branch || dirty {
		return "", "", fmt.Errorf("factory: publication requires clean owned branch")
	}
	repo, ok := g.fleet.ws.Repo(item.RepoID)
	if !ok {
		return "", "", ErrUnknownRepo
	}
	cred, err := g.fleet.creds.Resolve(ctx, DefaultOwnerID, repo.CredRef)
	if err != nil {
		return "", "", err
	}
	remote, err := g.remoteHead(ctx, a.Project, item.Branch, cred)
	if err != nil {
		return "", "", err
	}
	if remote == head {
		return head, tree, nil
	}
	if remote != expectedRemote {
		return "", "", fmt.Errorf("factory: remote branch changed from expected SHA")
	}
	_, pushErr := g.fleet.git.Push(a.Project, item.Branch, cred)
	remote, readErr := g.remoteHead(ctx, a.Project, item.Branch, cred)
	if readErr == nil && remote == head {
		return head, tree, nil
	}
	if pushErr != nil {
		return "", "", fmt.Errorf("factory: push outcome uncertain: %w", pushErr)
	}
	if readErr != nil {
		return "", "", fmt.Errorf("factory: pushed branch but remote verification failed: %w", readErr)
	}
	return "", "", fmt.Errorf("factory: remote did not reach expected head")
}
