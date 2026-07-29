package pipeline

import (
	"fmt"
	"os"
	"strings"

	"marshal/internal/worktree"
)

// WriteReviewPackage writes the commit list, stat summary, and full diff
// for a commit range into one file. The reviewer reads this file instead of
// running git itself, and the diff never enters the controller's own
// context.
func WriteReviewPackage(git worktree.GitOps, dir, rng, path string) error {
	log, err := git.LogOneline(dir, rng)
	if err != nil {
		return fmt.Errorf("pipeline review package: log %s: %w", rng, err)
	}
	stat, err := git.DiffStat(dir, rng)
	if err != nil {
		return fmt.Errorf("pipeline review package: diff --stat %s: %w", rng, err)
	}
	diff, err := git.Diff(dir, rng, 10)
	if err != nil {
		return fmt.Errorf("pipeline review package: diff %s: %w", rng, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Review package: %s\n\n## Commits\n\n```\n%s\n```\n\n", rng, log)
	fmt.Fprintf(&b, "## Files changed\n\n```\n%s\n```\n\n", stat)
	fmt.Fprintf(&b, "## Diff (-U10)\n\n```diff\n%s\n```\n", diff)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("pipeline review package: write %s: %w", path, err)
	}
	return nil
}
