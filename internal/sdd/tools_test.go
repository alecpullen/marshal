package sdd_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/sdd"
	"marshal/internal/tools/registry"
)

func TestRegisterToolsRegistersAllSDDTools(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := sdd.SDDToolOpts{
		WS:       ws,
		RepoRoot: t.TempDir(),
		DAG:      &sdd.DAG{},
		RS:       &sdd.RepoState{},
		Progress: &sdd.Progress{},
		Git:      sdd.NewFakeGitOps(),
	}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	want := []string{
		"sdd.state_dump", "sdd.edit_guard", "sdd.health", "sdd.worktree",
		"sdd.contract", "sdd.validate_contract", "sdd.normalize_report",
		"sdd.validate_report", "sdd.verify", "sdd.audit_gate",
		"sdd.review_state", "sdd.review_guard", "sdd.merge", "sdd.commit",
		"sdd.prepare_retry", "sdd.branch_package", "sdd.rescue_bundle",
		"sdd.todo",
	}
	for _, name := range want {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("missing tool %q", name)
		}
	}
}

func TestSDDStateDumpToolReturnsJSON(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{Merged: []string{}}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.state_dump")
	if !ok {
		t.Fatal("sdd.state_dump not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.state_dump"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "T1") {
		t.Errorf("state_dump content missing T1: %q", res.Content)
	}
}

func TestSDDEditGuardToolClean(t *testing.T) {
	reg := registry.New()
	git := sdd.NewFakeGitOps()
	git.SetRef("sdd/feature", "base123")
	git.SetRef("sdd/T1", "base123")
	opts := sdd.SDDToolOpts{WS: &sdd.Workspace{}, DAG: &sdd.DAG{}, RS: &sdd.RepoState{Branch: "sdd/feature"}, Progress: &sdd.Progress{}, Git: git}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.edit_guard")
	if !ok {
		t.Fatal("sdd.edit_guard not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.edit_guard", Args: []byte(`{"branch":"sdd/feature","base":"base123"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "clean") {
		t.Errorf("edit_guard clean summary: %q", res.Summary)
	}
}

func TestSDDHealthTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	opts := sdd.SDDToolOpts{WS: ws, DAG: &sdd.DAG{}, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.health")
	if !ok {
		t.Fatal("sdd.health not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.health"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "healthy") {
		t.Errorf("health summary should be healthy: %q", res.Summary)
	}
}

func TestSDDContractTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write a contract file
	contractContent := "## Task\n\nDo the thing\n\n## Allowed Files\n\n- main.go\n\n## Acceptance Criteria\n\n- it works\n\n## Constraints\n\n- none\n\n## Code Examples\n\n- example\n\n## Knowledge Bundle\n\n- knowledge\n\n## Allowed Symbols\n\n- Foo\n"
	if err := os.WriteFile(filepath.Join(ws.Dir(), "contracts", "T1.md"), []byte(contractContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.contract")
	if !ok {
		t.Fatal("sdd.contract not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.contract", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Content, "Do the thing") {
		t.Errorf("contract content missing task body: %q", res.Content)
	}
}

func TestSDDValidateContractTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write a valid contract
	contractContent := "## Task\n\nDo the thing\n\n## Allowed Files\n\n- main.go\n\n## Acceptance Criteria\n\n- it works\n\n## Constraints\n\n- none\n\n## Code Examples\n\n- example\n\n## Knowledge Bundle\n\n- knowledge\n\n## Allowed Symbols\n\n- Foo\n"
	if err := os.WriteFile(filepath.Join(ws.Dir(), "contracts", "T1.md"), []byte(contractContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending, Files: []string{"main.go"}}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.validate_contract")
	if !ok {
		t.Fatal("sdd.validate_contract not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.validate_contract", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "valid") {
		t.Errorf("validate_contract should be valid: %q", res.Summary)
	}
}

func TestSDDNormalizeReportTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write a report with status not on first line
	reportContent := "Some text\nstatus: DONE\n\nbody content"
	if err := os.WriteFile(filepath.Join(ws.Dir(), "reports", "T1.md"), []byte(reportContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.normalize_report")
	if !ok {
		t.Fatal("sdd.normalize_report not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.normalize_report", Args: []byte(`{"task_id":"T1","kind":""}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "normalized") {
		t.Errorf("normalize_report summary: %q", res.Summary)
	}
	if !strings.HasPrefix(res.Content, "status: DONE") {
		t.Errorf("normalized content should start with status: DONE, got: %q", res.Content)
	}
}

func TestSDDValidateReportTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write a valid report
	reportContent := "status: DONE\n\nbody content here"
	if err := os.WriteFile(filepath.Join(ws.Dir(), "reports", "T1.md"), []byte(reportContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.validate_report")
	if !ok {
		t.Fatal("sdd.validate_report not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.validate_report", Args: []byte(`{"task_id":"T1","kind":""}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "valid") {
		t.Errorf("validate_report should be valid: %q", res.Summary)
	}
}

func TestSDDAuditGateTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write an audit report
	reportContent := "status: PASS\n\neverything looks good"
	if err := os.WriteFile(filepath.Join(ws.Dir(), "reports", "T1-audit.md"), []byte(reportContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.audit_gate")
	if !ok {
		t.Fatal("sdd.audit_gate not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.audit_gate", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "AUDIT_PASS") {
		t.Errorf("audit_gate should show AUDIT_PASS: %q", res.Summary)
	}
}

func TestSDDReviewStateTool(t *testing.T) {
	reg := registry.New()
	git := sdd.NewFakeGitOps()
	git.SetRef("sdd/T1", "abc123")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending, Branch: "sdd/T1"}}}
	opts := sdd.SDDToolOpts{WS: &sdd.Workspace{}, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: git}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.review_state")
	if !ok {
		t.Fatal("sdd.review_state not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.review_state", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "REVIEW_REQUIRED") {
		t.Errorf("review_state should show REVIEW_REQUIRED: %q", res.Summary)
	}
}

func TestSDDReviewGuardTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write a review report
	reportContent := "status: PASS\n\nlooks good"
	if err := os.WriteFile(filepath.Join(ws.Dir(), "reports", "T1-review.md"), []byte(reportContent), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.review_guard")
	if !ok {
		t.Fatal("sdd.review_guard not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.review_guard", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "REVIEW_PASS") {
		t.Errorf("review_guard should show REVIEW_PASS: %q", res.Summary)
	}
}

func TestSDDBranchPackageTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.branch_package")
	if !ok {
		t.Fatal("sdd.branch_package not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.branch_package", Args: []byte(`{"branch":"feature-branch"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "packaged") {
		t.Errorf("branch_package summary: %q", res.Summary)
	}
}

func TestSDDRescueBundleTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.rescue_bundle")
	if !ok {
		t.Fatal("sdd.rescue_bundle not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.rescue_bundle"})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "rescue") {
		t.Errorf("rescue_bundle summary: %q", res.Summary)
	}
}

func TestSDDWorktreeToolCreatesWorktree(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	git := sdd.NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{Branch: "sdd/feature"}, Progress: &sdd.Progress{}, Git: git}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.worktree")
	if !ok {
		t.Fatal("sdd.worktree not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.worktree", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "worktree") {
		t.Errorf("worktree summary: %q", res.Summary)
	}
}

func TestSDDVerifyToolRunsVerify(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	runner := sdd.NewFakeCommandRunner()
	runner.SetResult("go build ./...", "build ok")
	runner.SetResult("go vet ./...", "vet ok")
	runner.SetResult("go test ./...", "tests ok")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending, WorktreePath: ws.Dir()}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps(), Runner: runner, VerifyTimeoutMS: 30000, RepoRoot: t.TempDir()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.verify")
	if !ok {
		t.Fatal("sdd.verify not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.verify", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "VERIFY_PASS") {
		t.Errorf("verify summary should show VERIFY_PASS: %q", res.Summary)
	}
}

func TestSDDMergeToolFastForwards(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	git := sdd.NewFakeGitOps()
	git.SetRef("sdd/feature", "pipe123")
	git.SetRef("sdd/T1", "task123")
	git.SetRef("main", "main123")
	git.SetBranch("sdd/T1")
	git.SetAncestor("main123", "pipe123")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Status: sdd.TaskInProgress, Branch: "sdd/T1"}}}
	rs := &sdd.RepoState{Branch: "sdd/feature", TargetBranch: "main", Merged: []string{}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: rs, Progress: &sdd.Progress{}, Git: git}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.merge")
	if !ok {
		t.Fatal("sdd.merge not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.merge", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "MERGED") {
		t.Errorf("merge summary should show MERGED: %q", res.Summary)
	}
}

func TestSDDCommitToolCommits(t *testing.T) {
	reg := registry.New()
	git := sdd.NewFakeGitOps()
	git.SetRef("HEAD", "abc123")
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskInProgress}}}
	opts := sdd.SDDToolOpts{WS: &sdd.Workspace{}, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: git}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.commit")
	if !ok {
		t.Fatal("sdd.commit not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.commit", Args: []byte(`{"task_id":"T1","message":"implement feature"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "abc123") {
		t.Errorf("commit summary should contain SHA: %q", res.Summary)
	}
}

func TestSDDPrepareRetryTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	if _, err := ws.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Write a contract file so PrepareRetry can find it.
	contractDir := filepath.Join(ws.Dir(), "contracts")
	if err := os.MkdirAll(contractDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contractDir, "T1.md"), []byte("## Task\n\nretry\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.prepare_retry")
	if !ok {
		t.Fatal("sdd.prepare_retry not registered")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.prepare_retry", Args: []byte(`{"task_id":"T1"}`)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "retry") {
		t.Errorf("prepare_retry summary: %q", res.Summary)
	}
}

func TestSDDTodoTool(t *testing.T) {
	reg := registry.New()
	ws, err := sdd.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	dag := &sdd.DAG{Tasks: []sdd.DAGTask{{ID: "T1", Title: "X", Status: sdd.TaskPending}}}
	opts := sdd.SDDToolOpts{WS: ws, DAG: dag, RS: &sdd.RepoState{}, Progress: &sdd.Progress{}, Git: sdd.NewFakeGitOps()}
	if err := sdd.RegisterTools(reg, opts); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	tool, ok := reg.Lookup("sdd.todo")
	if !ok {
		t.Fatal("sdd.todo not registered")
	}
	tasks := []map[string]string{{"id": "T1", "title": "X"}}
	args, _ := json.Marshal(map[string]any{"tasks": tasks})
	res, err := tool.Handler(context.Background(), registry.ToolCall{ID: "1", Name: "sdd.todo", Args: args})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !strings.Contains(res.Summary, "todo") {
		t.Errorf("todo summary: %q", res.Summary)
	}
}
