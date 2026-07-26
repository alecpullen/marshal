package sdd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/tools/registry"
)

// SDDToolOpts carries the runtime state the sdd.* tool handlers need.
// It mirrors native.Options: one struct passed to RegisterTools so every
// tool handler closes over the same workspace/DAG/state/progress/git.
type SDDToolOpts struct {
	WS              *Workspace
	RepoRoot        string
	DAG             *DAG
	RS              *RepoState
	Progress        *Progress
	Git             GitOps
	Runner          CommandRunner
	VerifyTimeoutMS int
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
		Description: "Get the current SDD pipeline state as JSON (the state-dump file the orchestrator reads).",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			path, err := WriteStateDump(opts.WS, opts.DAG, opts.RS, 0)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.state_dump: %w", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.state_dump: read: %w", err)
			}
			return registry.ToolResult{Summary: "state-dump written", Content: string(data)}, nil
		},
	}
}

func sddEditGuardTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.edit_guard",
		Description: "Check whether editing a file is safe given the current pipeline state.",
		Schema:      []byte(`{"type":"object","properties":{"branch":{"type":"string"},"base":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				Branch string `json:"branch"`
				Base   string `json:"base"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.edit_guard: parse args: %w", err)
			}
			res := EditGuard(opts.Git, args.Branch, args.Base, opts.DAG)
			if res.Pass {
				return registry.ToolResult{Summary: "clean", Content: res.Event}, nil
			}
			return registry.ToolResult{Summary: "violation: " + strings.Join(res.Files, ", "), Content: res.Event + ": " + res.Reason}, nil
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
			loop, err := DetectLoops(opts.Progress, opts.WS, 50)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.health: detect loops: %w", err)
			}
			stagnation, err := DetectStagnation(opts.Progress, opts.WS, 50)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.health: detect stagnation: %w", err)
			}
			var alerts []string
			if loop != nil {
				alerts = append(alerts, fmt.Sprintf("LOOP: %s (evidence: %v)", loop.Detail, loop.Evidence))
			}
			if stagnation != nil {
				alerts = append(alerts, fmt.Sprintf("STAGNATION: %s (evidence: %v)", stagnation.Detail, stagnation.Evidence))
			}
			if len(alerts) == 0 {
				return registry.ToolResult{Summary: "healthy", Content: "no health alerts"}, nil
			}
			return registry.ToolResult{Summary: "alerts: " + strings.Join(alerts, "; "), Content: strings.Join(alerts, "\n")}, nil
		},
	}
}

func sddWorktreeTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.worktree",
		Description: "Create a git worktree for a task branch.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.worktree: parse args: %w", err)
			}
			wt := NewWorktree(opts.WS, opts.DAG, opts.Git)
			path, err := wt.Create(args.TaskID, opts.RS.Branch)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.worktree: %w", err)
			}
			return registry.ToolResult{Summary: "worktree at " + path, Content: path}, nil
		},
	}
}

func sddContractTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.contract",
		Description: "Extract the contract for a task from the spec.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.contract: parse args: %w", err)
			}
			contract, err := ExtractContract(opts.WS, opts.DAG, args.TaskID)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.contract: %w", err)
			}
			path := filepath.Join(opts.WS.Dir(), "contracts", args.TaskID+".md")
			var b strings.Builder
			b.WriteString("path: ")
			b.WriteString(path)
			b.WriteString("\n\n## Task\n\n")
			b.WriteString(contract.Task)
			b.WriteString("\n\n## Allowed Files\n\n")
			b.WriteString(contract.AllowedFiles)
			b.WriteString("\n\n## Acceptance Criteria\n\n")
			b.WriteString(contract.Acceptance)
			return registry.ToolResult{Summary: "contract extracted for " + args.TaskID, Content: b.String()}, nil
		},
	}
}

func sddValidateContractTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.validate_contract",
		Description: "Validate a contract against the spec.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.validate_contract: parse args: %w", err)
			}
			contract, err := ExtractContract(opts.WS, opts.DAG, args.TaskID)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.validate_contract: extract: %w", err)
			}
			// Build the allowed-files glob from the DAG task's Files field.
			task, ok := opts.DAG.TaskByID(args.TaskID)
			if !ok {
				return registry.ToolResult{}, fmt.Errorf("sdd.validate_contract: unknown task %q", args.TaskID)
			}
			allowedFilesGlob := strings.Join(task.Files, ",")
			if err := contract.Validate(allowedFilesGlob); err != nil {
				return registry.ToolResult{Summary: "invalid: " + err.Error(), Content: "contract validation failed"}, nil
			}
			return registry.ToolResult{Summary: "valid", Content: "contract passes all validation checks"}, nil
		},
	}
}

func sddNormalizeReportTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.normalize_report",
		Description: "Normalize a worker report into the standard format.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"},"kind":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
				Kind   string `json:"kind"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.normalize_report: parse args: %w", err)
			}
			path := filepath.Join(opts.WS.Dir(), "reports", args.TaskID+args.Kind+".md")
			raw, err := os.ReadFile(path)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.normalize_report: read: %w", err)
			}
			normalized := NormalizeReport(string(raw))
			if err := os.WriteFile(path, []byte(normalized), 0644); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.normalize_report: write: %w", err)
			}
			return registry.ToolResult{Summary: "normalized", Content: normalized}, nil
		},
	}
}

func sddValidateReportTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.validate_report",
		Description: "Validate a report against the contract.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"},"kind":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
				Kind   string `json:"kind"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.validate_report: parse args: %w", err)
			}
			rep, err := ReadReport(opts.WS, args.TaskID, args.Kind)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.validate_report: read: %w", err)
			}
			if err := rep.Validate(); err != nil {
				return registry.ToolResult{Summary: "invalid: " + err.Error(), Content: "report validation failed"}, nil
			}
			return registry.ToolResult{Summary: "valid", Content: "report passes all validation checks"}, nil
		},
	}
}

func sddVerifyTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.verify",
		Description: "Run verification (build, vet, test) on a task branch.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"},"timeout_ms":{"type":"integer"}}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID    string `json:"task_id"`
				TimeoutMS int    `json:"timeout_ms"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.verify: parse args: %w", err)
			}
			task, ok := opts.DAG.TaskByID(args.TaskID)
			if !ok {
				return registry.ToolResult{}, fmt.Errorf("sdd.verify: unknown task %q", args.TaskID)
			}
			worktreeDir := task.WorktreePath
			if worktreeDir == "" {
				return registry.ToolResult{}, fmt.Errorf("sdd.verify: task %q has no worktree path", args.TaskID)
			}
			timeoutMS := args.TimeoutMS
			if timeoutMS <= 0 {
				timeoutMS = opts.VerifyTimeoutMS
			}
			runner := opts.Runner
			if runner == nil {
				runner = CLICommandRunner{}
			}
			res := VerifyTask(ctx, runner, worktreeDir, timeoutMS)
			if res.Pass {
				return registry.ToolResult{Summary: "VERIFY_PASS", Content: res.Output}, nil
			}
			return registry.ToolResult{Summary: "VERIFY_FAIL", Content: res.Output}, nil
		},
	}
}

func sddAuditGateTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.audit_gate",
		Description: "Check audit gate conditions for a task.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.audit_gate: parse args: %w", err)
			}
			res := AuditGate(opts.WS, args.TaskID)
			return registry.ToolResult{Summary: res.Event, Content: res.Reason}, nil
		},
	}
}

func sddReviewStateTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.review_state",
		Description: "Get the review state for a task.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.review_state: parse args: %w", err)
			}
			res := ReviewState(opts.Git, opts.DAG, args.TaskID)
			return registry.ToolResult{Summary: res.Event, Content: res.Reason}, nil
		},
	}
}

func sddReviewGuardTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.review_guard",
		Description: "Check review guard conditions for a task.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.review_guard: parse args: %w", err)
			}
			res := ReviewGuard(opts.WS, opts.Progress, args.TaskID)
			return registry.ToolResult{Summary: res.Event, Content: res.Reason}, nil
		},
	}
}

func sddMergeTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.merge",
		Description: "Merge a task branch into the pipeline branch.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.merge: parse args: %w", err)
			}
			res := Merge(opts.Git, opts.WS, opts.Progress, opts.DAG, opts.RS, args.TaskID)
			if res.Merged {
				return registry.ToolResult{Summary: "MERGED: " + args.TaskID, Content: "merged " + args.TaskID}, nil
			}
			return registry.ToolResult{Summary: "BLOCKED: " + res.Reason, Content: res.Reason}, nil
		},
	}
}

func sddCommitTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.commit",
		Description: "Commit changes to the pipeline branch.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"},"message":{"type":"string"}}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID  string `json:"task_id"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.commit: parse args: %w", err)
			}
			if args.Message == "" {
				return registry.ToolResult{}, fmt.Errorf("sdd.commit: message is required")
			}
			if err := opts.Git.Commit(args.Message); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.commit: %w", err)
			}
			sha, err := opts.Git.RevParse("HEAD")
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.commit: rev-parse HEAD: %w", err)
			}
			return registry.ToolResult{Summary: "committed " + sha, Content: sha}, nil
		},
	}
}

func sddPrepareRetryTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.prepare_retry",
		Description: "Prepare a retry for a failed task.",
		Schema:      []byte(`{"type":"object","properties":{"task_id":{"type":"string"}}}`),
		Risk:        registry.RiskWorkspaceWrite,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.prepare_retry: parse args: %w", err)
			}
			path, err := PrepareRetry(opts.WS, opts.Progress, args.TaskID)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.prepare_retry: %w", err)
			}
			return registry.ToolResult{Summary: "retry prepared at " + path, Content: path}, nil
		},
	}
}

// PrepareRetry logs a RETRY event to progress and re-extracts the contract
// for the given task, returning the contract path. This clears the
// orchestrator-override state in ReviewGuard (which checks for RETRY events
// between REVIEW_FAIL and a subsequent PASS).
func PrepareRetry(ws *Workspace, progress *Progress, taskID string) (string, error) {
	if _, err := ws.Ensure(); err != nil {
		return "", fmt.Errorf("sdd prepare_retry: workspace: %w", err)
	}
	if err := progress.Append(ws, taskID, "RETRY", "task", taskID); err != nil {
		return "", fmt.Errorf("sdd prepare_retry: progress: %w", err)
	}
	// Re-extract the contract so the worker gets a fresh copy.
	contractPath := filepath.Join(ws.Dir(), "contracts", taskID+".md")
	// If the contract already exists, just return its path.
	if _, err := os.Stat(contractPath); err == nil {
		return contractPath, nil
	}
	// Otherwise, the caller should have extracted the contract first.
	return "", fmt.Errorf("sdd prepare_retry: no contract for task %q; extract contract first", taskID)
}

func sddBranchPackageTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.branch_package",
		Description: "Package a task's branch state for review.",
		Schema:      []byte(`{"type":"object","properties":{"branch":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				Branch string `json:"branch"`
			}
			if err := json.Unmarshal(call.Args, &args); err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.branch_package: parse args: %w", err)
			}
			path, err := assembleBranchPackage(opts.WS, args.Branch)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.branch_package: %w", err)
			}
			return registry.ToolResult{Summary: "packaged at " + path, Content: "branch package written to " + path}, nil
		},
	}
}

// assembleBranchPackage writes <ws.Dir()>/reports/branch-package-<branch>.md
// containing the DAG, RepoState, and progress log for the given branch.
// Returns the package path.
func assembleBranchPackage(ws *Workspace, branch string) (string, error) {
	if _, err := ws.Ensure(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("# Branch Package: ")
	b.WriteString(branch)
	b.WriteString("\n\n")

	b.WriteString("## DAG (dag.json)\n\n```\n")
	dag, err := LoadDAG(ws)
	if err == nil && dag != nil {
		b.WriteString(formatDAG(dag))
	} else if err != nil {
		b.WriteString("error: " + err.Error())
	}
	b.WriteString("\n```\n\n")

	b.WriteString("## RepoState (state/repo.json)\n\n```\n")
	rs, err := LoadRepoState(ws)
	if err == nil && rs != nil {
		b.WriteString(fmt.Sprintf("branch=%s target=%s head=%s merged=%v\n", rs.Branch, rs.TargetBranch, rs.Head, rs.Merged))
	} else if err != nil {
		b.WriteString("error: " + err.Error())
	}
	b.WriteString("\n```\n\n")

	b.WriteString("## Progress (last 50 lines)\n\n```\n")
	var p Progress
	lines, _ := p.Tail(ws, 50)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```\n")

	path := filepath.Join(ws.Dir(), "reports", "branch-package-"+branch+".md")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("sdd branch package: write: %w", err)
	}
	return path, nil
}

func sddRescueBundleTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.rescue_bundle",
		Description: "Assemble a rescue bundle for a failed pipeline.",
		Schema:      []byte(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			path, err := AssembleRescueBundle(opts.WS)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("sdd.rescue_bundle: %w", err)
			}
			return registry.ToolResult{Summary: "rescue bundle at " + path, Content: "rescue bundle written to " + path}, nil
		},
	}
}

func sddTodoTool(opts SDDToolOpts) registry.Tool {
	return registry.Tool{
		Name:        "sdd.todo",
		Description: "List pending tasks from the SDD pipeline.",
		Schema:      []byte(`{"type":"object","properties":{"tasks":{"type":"array","items":{"type":"object"}}}}`),
		Risk:        registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			var args struct {
				Tasks []map[string]any `json:"tasks"`
			}
			if call.Args != nil && len(call.Args) > 0 {
				if err := json.Unmarshal(call.Args, &args); err != nil {
					return registry.ToolResult{}, fmt.Errorf("sdd.todo: parse args: %w", err)
				}
			}
			// Build the todo list from the DAG: pending tasks that are ready.
			ready := opts.DAG.ReadyTasks()
			var todoList []string
			for _, t := range ready {
				todoList = append(todoList, fmt.Sprintf("%s: %s", t.ID, t.Title))
			}
			if len(todoList) == 0 {
				return registry.ToolResult{Summary: "todo: no pending tasks", Content: "all tasks are completed or blocked"}, nil
			}
			return registry.ToolResult{Summary: "todo: " + strings.Join(todoList, ", "), Content: strings.Join(todoList, "\n")}, nil
		},
	}
}
