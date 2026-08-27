package native

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
	"marshal/internal/worktree"
)

type workspaceWorktreeArgs struct {
	Branch string `json:"branch"`
}

// workspaceWorktreeTool isolates the session in a git worktree (or returns
// it to the project root). The tool covers mechanics; the using-worktrees
// skill covers judgment. There is deliberately no removal path (spec §7).
func (t *toolSet) workspaceWorktreeTool() registry.Tool {
	tool := registry.Tool{
		Name:        "workspace.worktree",
		Description: "Isolate the session in a git worktree for a branch, or return to the project root.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"branch":{"type":"string","minLength":1}},"additionalProperties":false}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(_ context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[workspaceWorktreeArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		st := t.wsState()
		if st == nil {
			return registry.ToolResult{}, fmt.Errorf("workspace.worktree requires a session")
		}
		ws := st.Workspace()

		// {} — return to the project root. Uncommitted worktree changes are
		// left behind (the skill asks for a commit first; the tool does not
		// enforce it, spec §7).
		if args.Branch == "" {
			if ws.ActiveRoot == ws.ProjectRoot {
				return registry.ToolResult{
					Summary: "already at project root",
					Content: fmt.Sprintf("The session is already operating at the project root %s.", ws.ProjectRoot),
				}, nil
			}
			st.SetWorkspace(session.Workspace{ProjectRoot: ws.ProjectRoot, ActiveRoot: ws.ProjectRoot})
			return registry.ToolResult{
				Summary: "returned to project root",
				Content: fmt.Sprintf("Session root moved from %s back to the project root %s. The worktree and its branch %q still exist; merging and cleanup are manual.",
					ws.ActiveRoot, ws.ProjectRoot, ws.Branch),
			}, nil
		}

		dir, err := worktree.AgentDir(ws.ProjectRoot)
		if err != nil {
			return registry.ToolResult{}, err
		}
		wt, err := worktree.EnsureWorktree(worktree.CLIGitOps{}, ws.ProjectRoot, dir, args.Branch, "HEAD")
		if err != nil {
			return registry.ToolResult{}, err
		}
		st.SetWorkspace(session.Workspace{ProjectRoot: ws.ProjectRoot, ActiveRoot: wt.Path, Branch: wt.Branch})
		return registry.ToolResult{
			Summary: fmt.Sprintf("worktree %s", wt.Branch),
			Content: fmt.Sprintf("Worktree for branch %q (base %s) at %s. The session root moved there: file and shell tools now operate inside the worktree. Commit before returning to the project root; returning does not carry changes.",
				wt.Branch, wt.Base, wt.Path),
		}, nil
	}
	return tool
}
