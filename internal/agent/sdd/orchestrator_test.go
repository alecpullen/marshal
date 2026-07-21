package sdd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// scriptedFactory returns canned summaries per role via the Runner's
// RunTaskFunc override. The implementer returns "DONE" with a commit;
// the reviewer returns a clean verdict.
func scriptedFactory(summaries map[agent.AgentRole]string) RunnerFactory {
	return func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		r := &agent.Runner{}
		summary := summaries[role]
		r.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: summary}, nil
		}
		return r, nil
	}
}

func writePlan(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// initGitRepo makes dir a git repo on branch main with one commit. The
// orchestrator shells out to git for diffs, and now that git failures are
// surfaced instead of swallowed, tests need a real repo.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
}

// trackingFactory records which roles were dispatched.
func trackingFactory(summaries map[agent.AgentRole]string, calls *[]agent.AgentRole) RunnerFactory {
	return func(role agent.AgentRole, scope swarm.RegistryScope) (*agent.Runner, error) {
		*calls = append(*calls, role)
		r := &agent.Runner{}
		summary := summaries[role]
		r.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: summary}, nil
		}
		return r, nil
	}
}

func systemMessages(state *session.State) string {
	var b strings.Builder
	for _, m := range state.Messages() {
		if m.Role == session.RoleSystem {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func TestParseImplementerStatusReadsStatusLine(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{"blocked with 'not done' in body", "Status: BLOCKED\nTried but not done: missing API key", "BLOCKED"},
		{"needs_context", "Status: NEEDS_CONTEXT\nWhich schema applies?", "NEEDS_CONTEXT"},
		{"done with concerns", "Status: DONE_WITH_CONCERNS\nworks but flaky", "DONE_WITH_CONCERNS"},
		{"plain done", "Status: DONE\nall good", "DONE"},
		{"no status line, blocked word wins", "blocked: could not finish", "BLOCKED"},
		{"no status line, default done", "implemented the thing", "DONE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseImplementerStatus(tc.summary); got != tc.want {
				t.Fatalf("parseImplementerStatus(%q) = %q, want %q", tc.summary, got, tc.want)
			}
		})
	}
}

// TestOrchestratorSkipsBranchReviewOnEmptyDiff: when merge-base == HEAD
// (nothing committed, or merge-base failed and fell back to HEAD), the
// branch reviewer must NOT grade an empty diff.
func TestOrchestratorSkipsBranchReviewOnEmptyDiff(t *testing.T) {
	workDir := t.TempDir()
	initGitRepo(t, workDir) // HEAD == merge-base on main
	state := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	planPath := writePlan(t, t.TempDir(), "plan.md", twoTaskPlan)

	var calls []agent.AgentRole
	factory := trackingFactory(map[agent.AgentRole]string{
		agent.RoleSDDImplementer: "DONE\ncommits: none\ntests: 5/5 passing",
		agent.RoleSDDReviewer:    "### Spec Compliance\n- ✅ Spec compliant\n\n### Assessment\n**Task quality:** Approved",
	}, &calls)
	o := New(state, factory, config.SDDConfig{MaxFixRounds: 3})
	if err := o.Run(context.Background(), planPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, role := range calls {
		if role == agent.RoleSDDBranchReviewer {
			t.Fatal("branch reviewer graded an empty diff (merge-base == HEAD)")
		}
	}
	if got := systemMessages(state); !strings.Contains(got, "Branch review skipped") {
		t.Fatalf("expected a skip announcement, got:\n%s", got)
	}
}

// TestOrchestratorBranchReviewRunsOnBranchDiff: with a real branch diff,
// the branch reviewer runs and a ready verdict is announced.
func TestOrchestratorBranchReviewRunsOnBranchDiff(t *testing.T) {
	workDir := t.TempDir()
	initGitRepo(t, workDir)
	for _, c := range [][]string{
		{"git", "checkout", "-b", "sdd/test"},
		{"git", "commit", "--allow-empty", "-m", "task work"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c, err, out)
		}
	}
	state := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	planPath := writePlan(t, t.TempDir(), "plan.md", twoTaskPlan)

	var calls []agent.AgentRole
	factory := trackingFactory(map[agent.AgentRole]string{
		agent.RoleSDDImplementer:    "DONE\ncommits: none\ntests: 5/5 passing",
		agent.RoleSDDReviewer:       "### Spec Compliance\n- ✅ Spec compliant\n\n### Assessment\n**Task quality:** Approved",
		agent.RoleSDDBranchReviewer: "### Branch Verdict\n- ✅ Ready to merge",
	}, &calls)
	o := New(state, factory, config.SDDConfig{MaxFixRounds: 3})
	if err := o.Run(context.Background(), planPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	found := false
	for _, role := range calls {
		if role == agent.RoleSDDBranchReviewer {
			found = true
		}
	}
	if !found {
		t.Fatal("branch reviewer was not dispatched on a real branch diff")
	}
	if got := systemMessages(state); !strings.Contains(got, "ready to merge") {
		t.Fatalf("expected a ready-to-merge announcement, got:\n%s", got)
	}
}

const twoTaskPlan = `# Test Plan Implementation Plan

## Global Constraints

- Go 1.24+
- TDD

---

### Task 1: First task

Implement the first thing.

### Task 2: Second task

Implement the second thing.
`

func TestOrchestratorRunsAllTasks(t *testing.T) {
	workDir := t.TempDir()
	initGitRepo(t, workDir)
	state := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	planPath := writePlan(t, t.TempDir(), "plan.md", twoTaskPlan)
	factory := scriptedFactory(map[agent.AgentRole]string{
		agent.RoleSDDImplementer:    "DONE\ncommits: none\ntests: 5/5 passing",
		agent.RoleSDDReviewer:       "### Spec Compliance\n- ✅ Spec compliant\n\n### Assessment\n**Task quality:** Approved",
		agent.RoleSDDBranchReviewer: "### Branch Verdict\n- ✅ Ready to merge",
	})
	o := New(state, factory, config.SDDConfig{MaxFixRounds: 3})
	if err := o.Run(context.Background(), planPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	p := state.SDDProgress()
	// After completion, progress should be cleared (via defer).
	if p.Active {
		t.Error("SDDProgress should be cleared after completion")
	}
}

func TestOrchestratorResumesFromLedger(t *testing.T) {
	workDir := t.TempDir()
	initGitRepo(t, workDir)
	state := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	planPath := writePlan(t, workDir, "plan.md", twoTaskPlan)

	// Pre-populate the ledger with Task 1 complete.
	ws, _ := NewWorkspace(workDir)
	ws.Ensure()
	ledger := NewLedger(ws)
	ledger.Append(LedgerEntry{TaskNumber: 1, BaseSHA: "aaa", HeadSHA: "bbb"})

	factory := scriptedFactory(map[agent.AgentRole]string{
		agent.RoleSDDImplementer:    "DONE\ncommits: none\ntests: 3/3 passing",
		agent.RoleSDDReviewer:       "### Spec Compliance\n- ✅ Spec compliant\n\n### Assessment\n**Task quality:** Approved",
		agent.RoleSDDBranchReviewer: "### Branch Verdict\n- ✅ Ready to merge",
	})
	o := New(state, factory, config.SDDConfig{MaxFixRounds: 3})
	if err := o.Run(context.Background(), planPath); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Task 1 should have been skipped — only Task 2 dispatched.
	// The ledger should now have 2 entries (1 pre-existing + 1 new).
	entries := ledger.Read()
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2 (1 pre-existing + 1 new)", len(entries))
	}
}

func TestParseImplementerCommit(t *testing.T) {
	sha := "739801df0f5a6bde9d9819aff47385d11e2d2192"
	tests := []struct {
		name    string
		summary string
		want    string
	}{
		{
			name:    "full sha in commits line",
			summary: "Status: DONE\ncommits: " + sha + " task work\nbranch: main\ntests: 5/5 passing",
			want:    sha,
		},
		{
			name:    "short sha in commits line",
			summary: "Status: DONE\ncommits: abc1234 task work\ntests: 5/5 passing",
			want:    "abc1234",
		},
		{
			name:    "no commit reported",
			summary: "Status: DONE\ncommits: none\ntests: 5/5 passing",
			want:    "",
		},
		{
			name:    "no commits line",
			summary: "Status: DONE\ntests: 5/5 passing",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseImplementerCommit(tc.summary); got != tc.want {
				t.Fatalf("parseImplementerCommit(%q) = %q, want %q", tc.summary, got, tc.want)
			}
		})
	}
}

func TestVerifyBranchState(t *testing.T) {
	workDir := t.TempDir()
	mainDir := t.TempDir()
	initGitRepo(t, workDir)
	initGitRepo(t, mainDir)

	state := session.New(config.Default(), workDir, time.Now(), session.Persistence{})
	o := New(state, scriptedFactory(nil), config.SDDConfig{})

	// Worktree is on main; expected branch is main; no commit reported.
	if err := o.verifyBranchState(workDir, mainDir, "main", "main", ""); err != nil {
		t.Fatalf("verifyBranchState with matching branch and no commit: %v", err)
	}

	// Wrong branch should fail.
	if err := o.verifyBranchState(workDir, mainDir, "sdd/feature", "main", ""); err == nil {
		t.Fatal("verifyBranchState should fail on wrong branch")
	}

	// Create a commit and verify it is reported correctly.
	if out, err := exec.Command("git", "-C", workDir, "commit", "--allow-empty", "-m", "task work").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	out, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(out))
	if err := o.verifyBranchState(workDir, mainDir, "main", "main", sha); err != nil {
		t.Fatalf("verifyBranchState with matching commit: %v", err)
	}
	if err := o.verifyBranchState(workDir, mainDir, "main", "main", "abc1234"); err == nil {
		t.Fatal("verifyBranchState should fail when reported commit does not match HEAD")
	}
}

func TestOrchestratorSetForceClassAndPolicyRulesAreNoOps(t *testing.T) {
	o := New(session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{}),
		scriptedFactory(nil), config.SDDConfig{})
	o.SetForceClass("question")
	o.SetPolicyRules(nil)
}
