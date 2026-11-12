package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/repo"
	"marshal/internal/trust"
)

// projectHoldFunc holds root's project host and returns its release.
type projectHoldFunc func(ctx context.Context, root string) (release func(), err error)

// hostsHold is a --viewers child's hold: the config loaded for root by
// resolver (the headless rule), held on hosts.
func hostsHold(hosts *app.Hosts, home string, resolver trust.Resolver) projectHoldFunc {
	return func(ctx context.Context, root string) (func(), error) {
		cfg, err := config.Load(config.LoadOptions{
			HomeDir:       home,
			WorkingDir:    root,
			TrustResolver: resolver,
		})
		if err != nil {
			return nil, err
		}
		return hosts.Hold(root, cfg)
	}
}

// ProjectHoldParams is project/hold's and project/release's body.
type ProjectHoldParams struct {
	Root string `json:"root"`
}

// ProjectHoldResult answers both: the repository root and whether it is held.
type ProjectHoldResult struct {
	Root string `json:"root"`
	Held bool   `json:"held"`
}

// projectHolds is an agent host's held roots: one release per root.
type projectHolds struct {
	hold projectHoldFunc
	mu   sync.Mutex
	held map[string]func()
}

// Hold holds the project root params names (P4 D9). A root already held
// answers held without holding it again.
func (p *projectHolds) Hold(ctx context.Context, params json.RawMessage) (any, error) {
	root, err := p.root(params, "project/hold")
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, invalidParamsError("project root %q is not a directory", root)
	}
	root = repo.Root(root)

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.held[root]; ok {
		return ProjectHoldResult{Root: root, Held: true}, nil
	}
	release, err := p.hold(ctx, root)
	if err != nil {
		return nil, err
	}
	p.held[root] = release
	return ProjectHoldResult{Root: root, Held: true}, nil
}

// Release drops the hold on the project root params names. A root that is not
// held answers not held.
func (p *projectHolds) Release(ctx context.Context, params json.RawMessage) (any, error) {
	root, err := p.root(params, "project/release")
	if err != nil {
		return nil, err
	}
	root = repo.Root(root)

	p.mu.Lock()
	release, ok := p.held[root]
	delete(p.held, root)
	p.mu.Unlock()
	if ok {
		release()
	}
	return ProjectHoldResult{Root: root, Held: false}, nil
}

// releaseAll drops every hold.
func (p *projectHolds) releaseAll() {
	p.mu.Lock()
	releases := make([]func(), 0, len(p.held))
	for root, release := range p.held {
		releases = append(releases, release)
		delete(p.held, root)
	}
	p.mu.Unlock()
	for _, release := range releases {
		release()
	}
}

// root decodes params and returns the absolute root they name.
func (p *projectHolds) root(params json.RawMessage, method string) (string, error) {
	var req ProjectHoldParams
	if err := decodeParams(params, &req, method); err != nil {
		return "", err
	}
	if req.Root == "" || !filepath.IsAbs(req.Root) {
		return "", invalidParamsError("project root %q must be an absolute path", req.Root)
	}
	return req.Root, nil
}
