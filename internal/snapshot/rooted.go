package snapshot

import (
	"context"
	"log/slog"
)

// Rooted is a snapshotter whose target root is resolved on every call, so
// it follows a session's worktree rebinds without any subscription or
// swap. Each root gets an independent shadow repo (the shadow path derives
// from the absolute root hash), so project-root and worktree histories
// never mix.
// (Pipeline run worktrees live under <projectRoot>/.marshal/pipeline/<slug>/worktrees;
// this package neither knows nor cares.)
type Rooted struct {
	dataDir     string
	projectRoot string
	activeRoot  func() string
	maxFile     int64
	ignore      []string
	logger      *slog.Logger
}

// NewRooted builds a Rooted. activeRoot must return the session's current
// active root (or "" to mean the project root). projectRoot is used by
// Prune, which only ever prunes the main checkout's shadow repo.
func NewRooted(dataDir, projectRoot string, activeRoot func() string, maxFile int64, ignore []string, logger *slog.Logger) *Rooted {
	return &Rooted{dataDir: dataDir, projectRoot: projectRoot, activeRoot: activeRoot, maxFile: maxFile, ignore: ignore, logger: logger}
}

func (r *Rooted) svc() *Service {
	root := r.activeRoot()
	if root == "" {
		root = r.projectRoot
	}
	return New(r.dataDir, root, r.maxFile, r.ignore, r.logger)
}

func (r *Rooted) Track(ctx context.Context) (string, error) { return r.svc().Track(ctx) }
func (r *Rooted) Diff(ctx context.Context, hash string) (string, error) {
	return r.svc().Diff(ctx, hash)
}
func (r *Rooted) Restore(ctx context.Context, hash string) error { return r.svc().Restore(ctx, hash) }
func (r *Rooted) Enabled() bool                                  { return r.svc().Enabled() }

// Prune prunes only the project root's shadow repo. Worktree shadow repos
// are left alone; their disk is reclaimed when the user removes the
// worktree by hand (spec §7).
func (r *Rooted) Prune(ctx context.Context, retentionDays int) error {
	return New(r.dataDir, r.projectRoot, r.maxFile, r.ignore, r.logger).Prune(ctx, retentionDays)
}
