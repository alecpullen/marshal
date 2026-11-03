package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/acpwire"
	"marshal/internal/agent"
	"marshal/internal/agent/swarm"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/pipeline"
	"marshal/internal/sddauthor"
	"marshal/internal/worktree"
)

// The three plan bodies the TUI's preflight tests use: executable
// (deterministic only), prose-only, and mixed (one agent fallback).
const (
	executablePlanBody = "# Plan\n\n## Task 1: Add file\n\n" +
		"```marshal.file path=\"created.txt\"\nhello\n```\n"
	prosePlanBody = "# Plan\n\n## Task 1: Explain\n\nProse only.\n"
	mixedPlanBody = "# Plan\n\n## Task 1: Resolve\n\n" +
		"```marshal.agent\nscope = [\"internal/app\"]\nreason = \"needs design judgment\"\n```\n"
)

func requireCoded(t *testing.T, err error, code acpwire.ErrorCode) {
	t.Helper()
	var coded *acpwire.Error
	if !errors.As(err, &coded) {
		t.Fatalf("error = %v, want a coded *acpwire.Error", err)
	}
	if coded.Code != code {
		t.Fatalf("code = %q, want %q", coded.Code, code)
	}
}

// writePlanFile writes body as a plan directly inside dir.
func writePlanFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSwarmInstallsTheOverridesForOneRun(t *testing.T) {
	state := turnState(t)
	base := func(agent.AgentRole, swarm.RegistryScope) (*agent.Runner, error) {
		return nil, errors.New("base factory")
	}
	orchestrator := swarm.New(state, base)

	overridesCalled := 0
	var got map[routing.AgentRole]string
	override := func(overrides map[routing.AgentRole]string) swarm.RunnerFactory {
		overridesCalled++
		got = overrides
		return func(agent.AgentRole, swarm.RegistryScope) (*agent.Runner, error) {
			return nil, errors.New("override factory")
		}
	}
	rt := &Runtime{State: state, SwarmRunner: orchestrator, SwarmOverrideFactory: override}

	err := rt.RunSwarm(context.Background(), acpwire.SwarmParams{
		Goal:      "ship it",
		Overrides: map[string]string{"planner": "local/qwen"},
	})
	if err == nil || !strings.Contains(err.Error(), "override factory") {
		t.Fatalf("RunSwarm() error = %v, want it to come from the override factory", err)
	}
	if overridesCalled != 1 {
		t.Fatalf("override factory called %d times, want 1", overridesCalled)
	}
	if got[routing.RolePlanner] != "local/qwen" {
		t.Fatalf("override for planner = %q, want local/qwen", got[routing.RolePlanner])
	}

	// The override was for that run only.
	if _, err := orchestrator.NewRunner(agent.RolePlanner, swarm.ScopeReadOnly); err == nil || !strings.Contains(err.Error(), "base factory") {
		t.Fatalf("factory after the run = %v, want the base factory back", err)
	}

	// A run without overrides leaves the base factory alone.
	err = rt.RunSwarm(context.Background(), acpwire.SwarmParams{Goal: "again"})
	if err == nil || !strings.Contains(err.Error(), "base factory") {
		t.Fatalf("RunSwarm() without overrides error = %v, want the base factory's", err)
	}
	if overridesCalled != 1 {
		t.Fatalf("override factory called %d times, want 1", overridesCalled)
	}
}

func TestRunSwarmWithoutAnOrchestratorIsRefused(t *testing.T) {
	rt := &Runtime{State: turnState(t)}
	err := rt.RunSwarm(context.Background(), acpwire.SwarmParams{Goal: "ship it"})
	requireCoded(t, err, acpwire.CodeNoRunner)
}

func TestNewPlanRunSetsTheStrategyAndVerify(t *testing.T) {
	dir := t.TempDir()
	path := writePlanFile(t, dir, "p.md", executablePlanBody)
	state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})

	var got map[routing.AgentRole]string
	factory := func(planPath string, overrides map[routing.AgentRole]string) PipelineRunner {
		got = overrides
		c, err := pipeline.NewController(pipeline.ControllerOpts{
			PlanPath: planPath,
			RepoRoot: dir,
			Git:      worktree.NewFakeGitOps(),
		})
		if err != nil {
			t.Fatalf("NewController: %v", err)
		}
		return pipeline.NewControllerAdapter(c, state)
	}
	rt := &Runtime{State: state, PipelineFactory: factory}

	run, err := rt.NewPlanRun(acpwire.SDDParams{
		PlanPath:  path,
		Strategy:  "strict",
		Overrides: map[string]string{"sdd_implementer": "kimi/k3"},
		Verify:    &acpwire.VerifyCommands{Build: "make build", Test: "make test"},
	})
	if err != nil {
		t.Fatalf("NewPlanRun: %v", err)
	}
	pr, ok := run.(*planRun)
	if !ok {
		t.Fatalf("NewPlanRun returned %T, want *planRun", run)
	}
	adapter, ok := pr.runner.(*pipeline.ControllerAdapter)
	if !ok {
		t.Fatalf("runner = %T, want *pipeline.ControllerAdapter", pr.runner)
	}
	controller := adapter.Controller()
	if controller.Strategy != pipeline.StrategyStrict {
		t.Fatalf("strategy = %q, want %q", controller.Strategy, pipeline.StrategyStrict)
	}
	if controller.Verifier.Build != "make build" || controller.Verifier.Test != "make test" {
		t.Fatalf("verifier = %q / %q, want the run's commands", controller.Verifier.Build, controller.Verifier.Test)
	}
	if got[routing.RoleSDDImplementer] != "kimi/k3" {
		t.Fatalf("override for sdd_implementer = %q, want kimi/k3", got[routing.RoleSDDImplementer])
	}

	// Without Verify, and with empty Verify commands, the configured
	// commands are left alone.
	for _, verify := range []*acpwire.VerifyCommands{nil, {}} {
		run, err := rt.NewPlanRun(acpwire.SDDParams{PlanPath: path, Verify: verify})
		if err != nil {
			t.Fatalf("NewPlanRun: %v", err)
		}
		pr := run.(*planRun)
		controller := pr.runner.(*pipeline.ControllerAdapter).Controller()
		if controller.Verifier.Build != "" || controller.Verifier.Test != "" {
			t.Fatalf("verifier = %q / %q, want the configured commands untouched", controller.Verifier.Build, controller.Verifier.Test)
		}
	}
}

func TestNewPlanRunReportsAPlanItCannotBuild(t *testing.T) {
	state := turnState(t)
	rt := &Runtime{
		State: state,
		PipelineFactory: func(string, map[routing.AgentRole]string) PipelineRunner {
			return nil
		},
	}
	_, err := rt.NewPlanRun(acpwire.SDDParams{PlanPath: "/nonexistent/plan.md"})
	if err == nil || !strings.Contains(err.Error(), "cannot build a runner") {
		t.Fatalf("NewPlanRun() error = %v, want a cannot-build report", err)
	}

	rt = &Runtime{State: state}
	_, err = rt.NewPlanRun(acpwire.SDDParams{PlanPath: "/nonexistent/plan.md"})
	requireCoded(t, err, acpwire.CodeNoRunner)
}

func TestThePipelineFactoryReturnsNilForAPlanItCannotBuild(t *testing.T) {
	ctx := context.Background()
	cfg := reloadableAgentConfig("test-provider")
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	_, _, _, _, _, _, _, _, _, pipelineFactory, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, "")
	if err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}
	if pipelineFactory == nil {
		t.Fatal("pipeline factory should not be nil")
	}
	if runner := pipelineFactory("/nonexistent/plan.md", nil); runner != nil {
		t.Fatalf("pipeline factory built a runner for a plan it cannot build: %v", runner)
	}
	msgs := state.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected the failure to be reported in the transcript")
	}
	if last := msgs[len(msgs)-1]; !strings.HasPrefix(last.Content, "Cannot start plan run:") {
		t.Fatalf("last message = %q, want it to start with %q", last.Content, "Cannot start plan run:")
	}
}

// fakePipelineRunner is a PipelineRunner whose run result the test sets.
type fakePipelineRunner struct {
	runErr  error
	answers []string
}

func (f *fakePipelineRunner) Run(context.Context, string) error { return f.runErr }
func (f *fakePipelineRunner) SetForceClass(string)              {}
func (f *fakePipelineRunner) AnswerGate(answer string)          { f.answers = append(f.answers, answer) }

func TestAPlanRunReportsItsGateAsACode(t *testing.T) {
	runner := &fakePipelineRunner{runErr: fmt.Errorf("task 2: %w", pipeline.ErrHumanGateRequired)}
	run := &planRun{runner: runner, planPath: "p.md"}

	err := run.Run(context.Background())
	requireCoded(t, err, acpwire.CodeHumanGateRequired)

	other := errors.New("boom")
	runner.runErr = other
	if err := run.Run(context.Background()); err != other {
		t.Fatalf("Run() error = %v, want the runner's error unchanged", err)
	}

	run.Answer("use 30s")
	if len(runner.answers) != 1 || runner.answers[0] != "use 30s" {
		t.Fatalf("answers = %v, want [use 30s]", runner.answers)
	}
}

func TestPlanPreflightInspectsThePlan(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		strategy   string
		effective  string
		hasBlocks  bool
		blocked    bool
		detOps     int
		agentOps   int
		estCalls   int
		wantTasks  bool
		taskNumber int
		taskTitle  string
		taskDetOps int
	}{
		{
			name:       "executable",
			body:       executablePlanBody,
			effective:  acpwire.StrategyAdaptive,
			hasBlocks:  true,
			detOps:     1,
			wantTasks:  true,
			taskNumber: 1,
			taskTitle:  "Add file",
			taskDetOps: 1,
		},
		{
			name:      "prose",
			body:      prosePlanBody,
			effective: acpwire.StrategyAgent,
		},
		{
			name:      "mixed",
			body:      mixedPlanBody,
			strategy:  acpwire.StrategyStrict,
			effective: acpwire.StrategyStrict,
			hasBlocks: true,
			blocked:   true,
			agentOps:  1,
			estCalls:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writePlanFile(t, dir, "p.md", tc.body)
			state := session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})
			rt := &Runtime{State: state}

			got, err := rt.PlanPreflight(context.Background(), acpwire.PlanPreflightParams{
				PlanPath: path,
				Strategy: tc.strategy,
			})
			if err != nil {
				t.Fatalf("PlanPreflight: %v", err)
			}
			insp := got.Inspection
			if insp.Strategy != tc.effective {
				t.Errorf("strategy = %q, want %q", insp.Strategy, tc.effective)
			}
			if insp.HasBlocks != tc.hasBlocks {
				t.Errorf("hasBlocks = %v, want %v", insp.HasBlocks, tc.hasBlocks)
			}
			if insp.StrictBlocked != tc.blocked {
				t.Errorf("strictBlocked = %v, want %v", insp.StrictBlocked, tc.blocked)
			}
			if insp.DetOps != tc.detOps || insp.AgentOps != tc.agentOps || insp.EstCalls != tc.estCalls {
				t.Errorf("totals = %d/%d/%d, want %d/%d/%d",
					insp.DetOps, insp.AgentOps, insp.EstCalls, tc.detOps, tc.agentOps, tc.estCalls)
			}
			if tc.wantTasks {
				if len(insp.Tasks) != 1 {
					t.Fatalf("tasks = %+v, want exactly one", insp.Tasks)
				}
				task := insp.Tasks[0]
				if task.TaskN != tc.taskNumber || task.Title != tc.taskTitle || task.DetOps != tc.taskDetOps {
					t.Errorf("task = %+v, want {%d %q detOps %d}", task, tc.taskNumber, tc.taskTitle, tc.taskDetOps)
				}
			}
		})
	}

	t.Run("missing plan", func(t *testing.T) {
		dir := t.TempDir()
		rt := &Runtime{State: session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})}
		_, err := rt.PlanPreflight(context.Background(), acpwire.PlanPreflightParams{
			PlanPath: filepath.Join(dir, "nope.md"),
		})
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("PlanPreflight() error = %v, want a no-such-file error", err)
		}
	})
}

func TestPlanPreflightResolvesTheVerifyGate(t *testing.T) {
	executable := func(t *testing.T) (string, string) {
		t.Helper()
		dir := t.TempDir()
		return dir, writePlanFile(t, dir, "p.md", executablePlanBody)
	}

	t.Run("configured", func(t *testing.T) {
		dir, path := executable(t)
		cfg := config.Default()
		cfg.SDD.Verify.Build = "make build"
		cfg.SDD.Verify.Test = "make test"
		rt := &Runtime{State: session.New(cfg, dir, time.Unix(100, 0), session.Persistence{})}

		got, err := rt.PlanPreflight(context.Background(), acpwire.PlanPreflightParams{PlanPath: path})
		if err != nil {
			t.Fatalf("PlanPreflight: %v", err)
		}
		if !got.Verify.Known || got.Verify.Detected {
			t.Fatalf("gate = %+v, want known and not detected", got.Verify)
		}
		if got.Verify.Build != "make build" || got.Verify.Test != "make test" {
			t.Fatalf("gate = %q / %q, want the configured commands", got.Verify.Build, got.Verify.Test)
		}
	})

	t.Run("detected from the manifest", func(t *testing.T) {
		dir, path := executable(t)
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rt := &Runtime{State: session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})}

		got, err := rt.PlanPreflight(context.Background(), acpwire.PlanPreflightParams{PlanPath: path})
		if err != nil {
			t.Fatalf("PlanPreflight: %v", err)
		}
		if !got.Verify.Known || !got.Verify.Detected {
			t.Fatalf("gate = %+v, want known and detected", got.Verify)
		}
		if got.Verify.Build != "go build ./..." || got.Verify.Test != "go test ./..." {
			t.Fatalf("gate = %q / %q, want the detected Go commands", got.Verify.Build, got.Verify.Test)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		dir, path := executable(t)
		rt := &Runtime{State: session.New(config.Default(), dir, time.Unix(100, 0), session.Persistence{})}

		got, err := rt.PlanPreflight(context.Background(), acpwire.PlanPreflightParams{PlanPath: path})
		if err != nil {
			t.Fatalf("PlanPreflight: %v", err)
		}
		if got.Verify.Known {
			t.Fatalf("gate = %+v, want unknown with no manifest and no commands", got.Verify)
		}
	})
}

// planWritingModel writes one plan artifact at path, the way the authoring
// child does.
type planWritingModel struct {
	path string
	body string
}

func (m planWritingModel) Run(context.Context, string) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.path, []byte(m.body), 0o644)
}

func TestAuthorPlanReturnsTheCandidate(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	state := session.New(cfg, dir, time.Unix(100, 0), session.Persistence{})

	var got sddauthor.Request
	factory := func(req sddauthor.Request) (*sddauthor.Runner, error) {
		got = req
		return sddauthor.NewRunner(planWritingModel{path: req.PlanPath, body: executablePlanBody}), nil
	}
	rt := &Runtime{State: state, PlanAuthorFactory: factory}

	result, err := rt.AuthorPlan(context.Background(), acpwire.AuthorPlanParams{Goal: "add marker file"})
	if err != nil {
		t.Fatalf("AuthorPlan: %v", err)
	}
	plansDir := filepath.Join(dir, cfg.SDD.PlansDir)
	if !strings.HasPrefix(result.PlanPath, plansDir) {
		t.Fatalf("plan path = %q, want it inside %q", result.PlanPath, plansDir)
	}
	if _, err := os.Stat(result.PlanPath); err != nil {
		t.Fatalf("candidate was not written: %v", err)
	}
	if got.Design != "add marker file" {
		t.Fatalf("request design = %q, want the goal", got.Design)
	}
	if result.Inspection == nil || result.Inspection.Strategy != acpwire.StrategyAdaptive {
		t.Fatalf("inspection = %+v, want the adaptive strategy", result.Inspection)
	}

	rtWithout := &Runtime{State: state}
	_, err = rtWithout.AuthorPlan(context.Background(), acpwire.AuthorPlanParams{Goal: "add marker file"})
	requireCoded(t, err, acpwire.CodeNoRunner)

	failing := &Runtime{
		State: state,
		PlanAuthorFactory: func(sddauthor.Request) (*sddauthor.Runner, error) {
			return nil, errors.New("no route")
		},
	}
	_, err = failing.AuthorPlan(context.Background(), acpwire.AuthorPlanParams{Goal: "add marker file"})
	if err == nil || !strings.Contains(err.Error(), "start plan authoring: no route") {
		t.Fatalf("AuthorPlan() error = %v, want it to name the failed start", err)
	}
}
