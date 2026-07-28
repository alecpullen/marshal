package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"log/slog"
)

// defaultWalkTimeout bounds the tree walk in largeFileExcludes. The walk is
// the one phase of Track with no external timeout of its own — every other
// step is a git subprocess started with exec.CommandContext. A tree large
// enough (or a bug pathological enough) to blow this budget turns into a
// logged error instead of a turn that never starts.
const defaultWalkTimeout = 30 * time.Second

// Service maintains a bare git repo per project under the user data dir and
// snapshots the full working tree on demand. It never touches the project's
// own .git state (uses --git-dir <shadow> --work-tree <project>).
type Service struct {
	shadowDir string
	workTree  string
	logger    *slog.Logger
	enabled   bool
	maxFile   int64
	ignore    []string

	// walkTimeout bounds the largeFileExcludes tree walk; tests shrink it.
	walkTimeout time.Duration

	// sem serialises Track. It is a channel rather than a sync.Mutex so
	// callers can give up when their context ends instead of queueing
	// behind an in-flight snapshot indefinitely.
	sem chan struct{}
}

// lock acquires the Track semaphore, returning the context error if ctx ends
// first. On a non-nil error the semaphore was NOT acquired and unlock must not
// be called.
func (s *Service) lock(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) unlock() { <-s.sem }

func New(dataDir, projectRoot string, maxFileBytes int64, ignore []string, logger *slog.Logger) *Service {
	hash := projectHash(projectRoot)
	s := &Service{
		shadowDir:   filepath.Join(dataDir, "snapshots", hash),
		workTree:    projectRoot,
		logger:      logger,
		enabled:     true,
		maxFile:     maxFileBytes,
		ignore:      ignore,
		walkTimeout: defaultWalkTimeout,
		sem:         make(chan struct{}, 1),
	}
	if !gitAvailable() {
		s.enabled = false
		logger.Warn("git not found; snapshots disabled")
	}
	return s
}

func (s *Service) Enabled() bool { return s.enabled }

// Track stages the full tree (respecting ignores + size cap) and commits,
// returning the snapshot hash. Called automatically before write-class tools.
func (s *Service) Track(ctx context.Context) (string, error) {
	if !s.enabled {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.lock(ctx); err != nil {
		return "", err
	}
	defer s.unlock()
	if err := s.ensureRepo(ctx); err != nil {
		return "", err
	}
	if err := s.refreshExclude(ctx); err != nil {
		return "", err
	}

	largeExcludes, err := s.largeFileExcludes(ctx)
	if err != nil {
		// A cancelled or over-budget walk means the snapshot cannot be
		// trusted to exclude what it should, and the caller is waiting.
		// Surface it rather than committing a partial exclude list.
		if ctxErr := walkContextErr(err); ctxErr != nil {
			return "", ctxErr
		}
		s.logger.Warn("failed to list large files for snapshot", "error", err)
	}

	addArgs := []string{"add", "-A", "--", "."}
	for _, ex := range largeExcludes {
		addArgs = append(addArgs, ":(exclude)"+ex)
	}
	if err := s.runGit(ctx, addArgs...); err != nil {
		return "", err
	}

	if _, err := s.runGitOut(ctx, "commit", "-m", "marshal snapshot", "--allow-empty"); err != nil {
		return "", err
	}

	hash, err := s.runGitOut(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}

	ref := "refs/snapshots/" + hash
	if err := s.runGit(ctx, "update-ref", ref, hash); err != nil {
		return hash, err
	}

	return hash, nil
}

// Diff returns a unified diff between the snapshot at hash and the current tree.
func (s *Service) Diff(ctx context.Context, hash string) (string, error) {
	if !s.enabled {
		return "", nil
	}
	out, err := s.runGitOut(ctx, "diff", hash)
	if err != nil {
		return "", err
	}
	return out, nil
}

// Restore checks out the full tree at hash into the work-tree.
func (s *Service) Restore(ctx context.Context, hash string) error {
	if !s.enabled {
		return nil
	}
	if err := s.runGit(ctx, "checkout", hash, "--", "."); err != nil {
		return fmt.Errorf("restore snapshot %s: %w", hash, err)
	}
	return nil
}

// Prune removes snapshot refs older than retentionDays.
func (s *Service) Prune(ctx context.Context, retentionDays int) error {
	if !s.enabled || retentionDays < 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()

	out, err := s.runGitOut(ctx, "for-each-ref", "--format=%(refname) %(committerdate:unix)", "refs/snapshots/")
	if err != nil {
		return fmt.Errorf("list snapshot refs: %w", err)
	}

	var deleted int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := fields[0]
		ts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		if ts < cutoff {
			if err := s.runGit(ctx, "update-ref", "-d", ref); err != nil {
				s.logger.Warn("failed to prune snapshot ref", "ref", ref, "error", err)
				continue
			}
			deleted++
		}
	}
	if deleted > 0 {
		s.runGit(ctx, "reflog", "expire", "--expire=now", "--all")
		s.runGit(ctx, "gc", "--prune=now")
	}
	return nil
}
