package sidepanel

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"marshal/internal/app/tui/glyph"
	"marshal/internal/strutil"
)

// RepoSection is the ambient identity block: git branch, indexed repo
// size, language mix, sandbox posture. Always relevant and lowest
// priority, so it is the rail's filler and the first to yield.
type RepoSection struct{}

func (RepoSection) ID() string         { return "repo" }
func (RepoSection) Title() string      { return "REPO" }
func (RepoSection) Priority() int      { return 5 }
func (RepoSection) Relevant(Data) bool { return true }
func (RepoSection) Clippable() bool    { return false }

func (RepoSection) Render(d Data, width, maxRows int) []string {
	rows := make([]string, 0, 4)

	if d.Git.InRepo && d.Git.Branch != "" {
		line := " ⎇ " + d.Git.Branch
		if d.Git.Worktree != "" {
			line += "  wt:" + d.Git.Worktree
		}
		rows = append(rows, line)
	}

	rows = append(rows, fmt.Sprintf(" %s files · %s symbols",
		strutil.CompactTokens(d.Repo.Files),
		strutil.CompactTokens(d.Repo.Symbols)))

	if len(d.Repo.Languages) > 0 {
		parts := make([]string, 0, 3)
		for i, l := range d.Repo.Languages {
			if i == 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %d%%", l.Name, l.Share))
		}
		rows = append(rows, " "+strings.Join(parts, "  "))
	}

	if d.State != nil {
		sb := d.State.SandboxInfo()
		net := "✘"
		if d.State.Config.Tools.Shell.AllowNetwork {
			net = glyph.OK
		}
		rows = append(rows, fmt.Sprintf(" sandbox %s · net %s", sb.Backend, net))
		rows = append(rows, fmt.Sprintf(" %d tools loaded", len(d.State.LoadedToolNames())))
	}

	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	if maxRows > 0 && len(rows) > maxRows {
		rows = rows[:maxRows]
	}
	return rows
}

func (RepoSection) OneLine(d Data, width int) string {
	parts := make([]string, 0, 3)
	if d.Git.InRepo && d.Git.Branch != "" {
		parts = append(parts, "⎇ "+d.Git.Branch)
	}
	parts = append(parts, strutil.CompactTokens(d.Repo.Files)+" files")
	if d.Repo.Symbols > 0 {
		parts = append(parts, strutil.CompactTokens(d.Repo.Symbols)+" symbols")
	}
	return ansi.Truncate(strings.Join(parts, " · "), width, "…")
}
