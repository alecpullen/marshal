package sdd

import (
	"context"

	"marshal/internal/tools/registry"
)

// SDDToolOpts carries the runtime state the sdd.* tool handlers need.
// It mirrors native.Options: one struct passed to RegisterTools so every
// tool handler closes over the same workspace/DAG/state/progress/git.
type SDDToolOpts struct {
	WS       *Workspace
	RepoRoot string
	DAG      *DAG
	RS       *RepoState
	Progress *Progress
	Git      GitOps
}

// RegisterTools registers the 18 sdd.* tools on reg, wrapping the P2/P3
// Go functions. It mirrors native.RegisterAll. Individual tool builders
// are added in Tasks 2-3; this skeleton registers the set and verifies
// every name resolves.
func RegisterTools(reg *registry.Registry, opts SDDToolOpts) error {
	tools := []registry.Tool{
		sddStateDumpTool(opts),
		sddEditGuardTool(opts),
		sddHealthTool(opts),
		sddWorktreeTool(opts),
		sddContractTool(opts),
		sddValidateContractTool(opts),
		sddNormalizeReportTool(opts),
		sddValidateReportTool(opts),
		sddVerifyTool(opts),
		sddAuditGateTool(opts),
		sddReviewStateTool(opts),
		sddReviewGuardTool(opts),
		sddMergeTool(opts),
		sddCommitTool(opts),
		sddPrepareRetryTool(opts),
		sddBranchPackageTool(opts),
		sddRescueBundleTool(opts),
		sddTodoTool(opts),
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func sddStateDumpTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.state_dump",
		Description: "Get the current SDD pipeline state as JSON.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddEditGuardTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.edit_guard",
		Description: "Check whether editing a file is safe given the current pipeline state.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddHealthTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.health",
		Description: "Check the health of the SDD pipeline (loop/stagnation detection).",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddWorktreeTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.worktree",
		Description: "Manage SDD worktrees (create, list, clean).",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddContractTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.contract",
		Description: "Extract the contract for a task from the spec.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddValidateContractTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.validate_contract",
		Description: "Validate a contract against the spec.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddNormalizeReportTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.normalize_report",
		Description: "Normalize a worker report into the standard format.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddValidateReportTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.validate_report",
		Description: "Validate a report against the contract.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddVerifyTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.verify",
		Description: "Run verification (build, vet, test) on a task branch.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddAuditGateTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.audit_gate",
		Description: "Check audit gate conditions for a task.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddReviewStateTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.review_state",
		Description: "Get the review state for a task.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddReviewGuardTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.review_guard",
		Description: "Check review guard conditions for a task.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddMergeTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.merge",
		Description: "Merge a task branch into the pipeline branch.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddCommitTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.commit",
		Description: "Commit changes to the pipeline branch.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddPrepareRetryTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.prepare_retry",
		Description: "Prepare a retry for a failed task.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddBranchPackageTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.branch_package",
		Description: "Package a task's branch state for review.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddRescueBundleTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.rescue_bundle",
		Description: "Assemble a rescue bundle for a failed pipeline.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}

func sddTodoTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.todo",
		Description: "List pending tasks from the SDD pipeline.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "not yet implemented"}, nil
		},
	}
}
