package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
)

func TestRunReviewSubagentRegistersCardAndAppendsSummary(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})

	factoryCalled := false
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		factoryCalled = true
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			if prompt == "" {
				t.Fatalf("review prompt empty")
			}
			return &agent.Task{Summary: "Looks good."}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "main.go", "", ""); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	if !factoryCalled {
		t.Fatal("factory was not called")
	}

	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("registered subagents = %d, want 1", len(views))
	}
	v := views[0]
	if v.Status != session.SubagentDone {
		t.Fatalf("subagent status = %v, want Done", v.Status)
	}
	if v.Provider != "" {
		t.Fatalf("provider = %q, want empty when runner.Provider is nil", v.Provider)
	}
	if v.Model != "test-model" {
		t.Fatalf("model = %q, want test-model", v.Model)
	}
	if v.Role != routing.RoleReviewer {
		t.Fatalf("role = %v, want RoleReviewer", v.Role)
	}

	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected assistant summary appended to transcript")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleAssistant || last.Content != "Looks good." {
		t.Fatalf("last message = %+v, want assistant summary", last)
	}
	if !last.Final {
		t.Fatal("review summary must be a final assistant message, or history replay drops it")
	}
	if last.ContentType != session.ContentTypeMarkdown {
		t.Fatalf("ContentType = %v, want markdown", last.ContentType)
	}
}

func TestRunReviewSubagentSalvagedAppendsSalvagedMessage(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: "Partial findings.", SalvagedReason: "exhausted"}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "", "", ""); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected salvaged summary appended to transcript")
	}
	last := msgs[len(msgs)-1]
	if last.Role != session.RoleAssistant || last.Content != "Partial findings." {
		t.Fatalf("last message = %+v, want salvaged assistant summary", last)
	}
	if !last.Final {
		t.Fatal("salvaged review summary must still be Final=true so the TUI renders it as an answer")
	}
	if !last.Salvaged {
		t.Fatalf("Salvaged = %v, want true", last.Salvaged)
	}
	if last.SalvageReason != "exhausted" {
		t.Fatalf("SalvageReason = %q, want exhausted", last.SalvageReason)
	}

	views := state.Subagents()
	if len(views) != 1 {
		t.Fatalf("expected one subagent view, got %d", len(views))
	}
	if views[0].SalvagedReason != "exhausted" {
		t.Fatalf("subagent SalvagedReason = %q, want exhausted", views[0].SalvagedReason)
	}
}

func TestRunReviewSubagentFactoryError(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		return nil, nil, errors.New("factory exploded")
	}

	err := runReviewSubagent(context.Background(), state, factory, "", "", "")
	if err == nil {
		t.Fatal("expected error from factory")
	}
	if len(state.Subagents()) != 0 {
		t.Fatal("no subagent should be registered on factory error")
	}
}

func TestRunReviewSubagentRunErrorMarksFailed(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return nil, errors.New("child failed")
		}
		return runner, childState, nil
	}

	err := runReviewSubagent(context.Background(), state, factory, "", "", "")
	if err == nil {
		t.Fatal("expected error from child run")
	}
	views := state.Subagents()
	if len(views) != 1 || views[0].Status != session.SubagentFailed {
		t.Fatalf("expected one failed subagent, got %+v", views)
	}
	if len(state.Messages()) != 0 {
		t.Fatal("no assistant message expected on failure")
	}
}

func TestRunReviewSubagentDispatchesReviewerRole(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var gotReq agent.SubagentRequest
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		gotReq = req
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: "Looks good."}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "", "", ""); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	if gotReq.Role != routing.RoleReviewer {
		t.Fatalf("dispatched role = %v, want RoleReviewer", gotReq.Role)
	}
}

func TestRunReviewSubagentExplicitModelBeatsRole(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	var gotReq agent.SubagentRequest
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		gotReq = req
		childState := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
		runner := agent.NewRunner(nil, nil, nil, childState, "test-model")
		runner.RunTaskFunc = func(ctx context.Context, prompt string) (*agent.Task, error) {
			return &agent.Task{Summary: "Looks good."}, nil
		}
		return runner, childState, nil
	}

	if err := runReviewSubagent(context.Background(), state, factory, "main.go", "ollama/explicit", ""); err != nil {
		t.Fatalf("runReviewSubagent() error = %v", err)
	}
	if gotReq.Role != routing.RoleReviewer {
		t.Fatalf("dispatched role = %v, want RoleReviewer", gotReq.Role)
	}
	if gotReq.Model != "ollama/explicit" {
		t.Fatalf("dispatched model = %q, want ollama/explicit", gotReq.Model)
	}
}

func TestRunReviewSubagentInvalidRangeRegistersNoCard(t *testing.T) {
	cfg := config.Default()
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	factory := func(req agent.SubagentRequest) (*agent.Runner, *session.State, error) {
		t.Fatal("factory should not be called for invalid range")
		return nil, nil, nil
	}

	err := runReviewSubagent(context.Background(), state, factory, "", "", "nope..HEAD")
	if err == nil {
		t.Fatal("expected error for invalid range")
	}
	if len(state.Subagents()) != 0 {
		t.Fatal("no subagent should be registered when range validation fails")
	}
}

func TestBuildReviewPromptRangeMode(t *testing.T) {
	dir := initReviewGitRepo(t)

	prompt := buildReviewPrompt(dir, "", "main..HEAD")
	if !strings.Contains(prompt, "Review the changes in range main..HEAD") {
		t.Fatalf("prompt missing range header:\n%s", prompt)
	}
	if !strings.Contains(prompt, "feature commit") {
		t.Fatalf("prompt missing commit log:\n%s", prompt)
	}
	if !strings.Contains(prompt, "+feature") {
		t.Fatalf("prompt missing diff:\n%s", prompt)
	}

	// Dirty the tree and assert working-tree section appears.
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\nextra"), 0o644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	prompt = buildReviewPrompt(dir, "", "main..HEAD")
	if !strings.Contains(prompt, "Working tree also has uncommitted changes") {
		t.Fatalf("prompt missing dirty-tree note:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Working-tree diff") {
		t.Fatalf("prompt missing working-tree diff:\n%s", prompt)
	}
}

func TestResolveReviewRangeBase(t *testing.T) {
	dir := initReviewGitRepo(t)
	r, err := resolveReviewRange(dir, "main")
	if err != nil {
		t.Fatalf("resolveReviewRange error = %v", err)
	}
	if !strings.HasSuffix(r, "..HEAD") {
		t.Fatalf("resolved range = %q, want merge-base..HEAD", r)
	}
}

func TestResolveReviewRangeExplicit(t *testing.T) {
	dir := initReviewGitRepo(t)
	r, err := resolveReviewRange(dir, "main..HEAD")
	if err != nil {
		t.Fatalf("resolveReviewRange error = %v", err)
	}
	if r != "main..HEAD" {
		t.Fatalf("resolved range = %q, want main..HEAD", r)
	}
}

func TestResolveReviewRangeInvalid(t *testing.T) {
	dir := initReviewGitRepo(t)
	_, err := resolveReviewRange(dir, "nope..HEAD")
	if err == nil {
		t.Fatal("expected error for invalid range")
	}
}

func initReviewGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	runGit("add", "base.txt")
	runGit("commit", "-m", "base commit")
	// Ensure a stable base branch then create a feature branch for the
	// commit that HEAD will be ahead of main. Some git versions already
	// default to main, so create the branch only when absent.
	if err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "main").Run(); err != nil {
		runGit("branch", "main")
	}
	runGit("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature.txt: %v", err)
	}
	runGit("add", "feature.txt")
	runGit("commit", "-m", "feature commit")
	return dir
}
