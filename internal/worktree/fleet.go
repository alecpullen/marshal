package worktree

import (
	"time"
)

// FleetRow is one agent-owned worktree's status, as the /fleet panel and
// ACP fleet listing render it.
type FleetRow struct {
	Path    string
	Branch  string
	Ahead   int
	Behind  int
	Dirty   bool
	Age     time.Duration
	Session string // resolved by callers, not ListFleet
}

// ListFleet reports status for agent-owned worktrees under
// .marshal/worktrees/, relative to base. Rows outside the agent dir (the
// project checkout itself, pipeline run worktrees) are filtered out, as
// are detached worktrees with no branch to report on. Per-row git
// failures degrade the row — zeros for ahead/behind, unknown age — rather
// than failing the whole listing: a fleet view must render even when one
// worktree is mid-operation.
func ListFleet(git GitOps, repoRoot, base string) ([]FleetRow, error) {
	infos, err := git.ListWorktrees(repoRoot)
	if err != nil {
		return nil, err
	}
	var rows []FleetRow
	for _, info := range infos {
		if info.Branch == "" {
			continue
		}
		if !OwnedByAgentDir(repoRoot, info.Path) {
			continue
		}
		row := FleetRow{Path: info.Path, Branch: info.Branch}
		if ahead, behind, aerr := git.AheadBehind(info.Path, base, info.Branch); aerr == nil {
			row.Ahead, row.Behind = ahead, behind
		}
		if dirty, derr := git.IsDirty(info.Path); derr == nil {
			row.Dirty = dirty
		}
		if tip, terr := git.BranchAge(info.Path, info.Branch); terr == nil && !tip.IsZero() {
			row.Age = time.Since(tip)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
