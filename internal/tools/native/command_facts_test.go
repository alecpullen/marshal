package native

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// CaptureCommandEnvironment gives the shared command-service fixture the launch
// capture a production runner has, so these tests exercise the real preparation
// path instead of a runner that cannot say what environment it would use. The
// values are deliberate fixtures, never the ambient environment.
func (f *fakeCommandRunner) CaptureCommandEnvironment(dir string) ([]string, map[string]string, error) {
	return []string{"PATH=/fixture/bin", "PWD=" + dir}, map[string]string{"PATH": "/fixture/bin", "PWD": dir}, nil
}

// factsAuthorizer builds a real authorizer over a permissive test authority, so
// these tests evaluate through the production policy engine rather than a stub
// that would agree with any facts it was handed.
func factsAuthorizer(t *testing.T, cfg config.Config) *policy.Authorizer {
	t.Helper()
	return policy.NewAuthorizer(policy.NewEngine(&cfg, nil), newTestAuthority())
}

// commandFactsFixture is one registered shell toolset with a real conversation
// command service, a real policy authorizer and a fixture runner that records
// exactly what it was asked to launch.
type commandFactsFixture struct {
	root       string
	state      *session.State
	runner     *fakeCommandRunner
	jobs       *JobManager
	reg        *registry.Registry
	tool       registry.Tool
	authorizer *policy.Authorizer
	attempts   []policy.CommandAttempt
}

func newCommandFactsFixture(t *testing.T, cfg config.Config) *commandFactsFixture {
	t.Helper()
	root := t.TempDir()
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	runner := newFakeRunner(withResult(CommandResult{
		ExitCode: 0,
		Meta:     registry.SandboxMeta{Backend: "fixture", Enabled: true},
	}))
	jobs := newTestCommandService(t, runner, root, state, nil)
	authorizer := factsAuthorizer(t, cfg)
	f := &commandFactsFixture{root: root, state: state, runner: runner, jobs: jobs, authorizer: authorizer}
	f.reg = registry.New()
	if err := RegisterAll(f.reg, Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		JobManager:    jobs,
		SessionState:  state,
		Config:        cfg,
		CommandAdmission: func(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error) {
			f.attempts = append(f.attempts, attempt)
			return authorizer.BeginCommand(ctx, attempt)
		},
		SetupAuthorizer: authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := f.reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run is not registered")
	}
	f.tool = tool
	return f
}

// prepared returns the facts this toolset prepares for one command, which is
// the description launch must use.
func (f *commandFactsFixture) prepared(t *testing.T, command string) registry.CommandFacts {
	t.Helper()
	if f.tool.PrepareCommand == nil {
		t.Fatal("shell.run lost its preparation")
	}
	facts, err := f.tool.PrepareCommand(context.Background(), registry.ToolCall{
		ID:   "inv-1",
		Name: "shell.run",
		Args: []byte(`{"command":"` + command + `"}`),
	})
	if err != nil {
		t.Fatalf("PrepareCommand: %v", err)
	}
	return facts
}

// launch runs one command the way dispatch does: evaluate, (optionally) approve,
// and hand the proof to the handler at the accepted launch.
func (f *commandFactsFixture) launch(t *testing.T, authorizer *policy.Authorizer, facts registry.CommandFacts, approve bool) (registry.ToolResult, error) {
	t.Helper()
	call := policy.Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &facts}
	proof, evaluation, err := authorizer.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if approve {
		if evaluation.Decision != policy.DecisionConfirm {
			t.Fatalf("decision = %q, want confirm for an approved launch", evaluation.Decision)
		}
		proof, err = authorizer.ApproveExact(proof, "approval-1")
		if err != nil {
			t.Fatalf("ApproveExact: %v", err)
		}
	} else if evaluation.Decision != policy.DecisionAllow {
		t.Fatalf("decision = %q, want allow", evaluation.Decision)
	}
	ctx := policy.WithAuthorization(context.Background(), proof)
	return f.tool.Handler(ctx, registry.ToolCall{
		ID:   "inv-1",
		Name: "shell.run",
		Args: []byte(`{"command":"` + facts.Command + `"}`),
	})
}

// TestApprovalAndLaunchFactsMatch pins that one command has one description.
// The authorizer's evaluated facts, the attempt accepted at the launch, the
// CommandRequest the runner recorded and the managed execution's own evidence
// all name the same command, directory and environment — and that environment
// is the one preparation captured, not one re-derived at launch.
func TestApprovalAndLaunchFactsMatch(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Confirm.Commands = []string{"echo facts"}
	f := newCommandFactsFixture(t, cfg)
	authorizer := factsAuthorizer(t, cfg)
	// The fixture's own admission seam uses its own authorizer over a separate
	// but equivalent authority; evaluating through `authorizer` keeps the
	// proof and the recorded attempt on one value.
	facts := f.prepared(t, "echo facts")

	if _, err := f.launch(t, authorizer, facts, true); err != nil {
		t.Fatalf("launch: %v", err)
	}

	if len(f.attempts) != 1 {
		t.Fatalf("accepted attempts = %d, want exactly 1", len(f.attempts))
	}
	accepted := f.attempts[0].Input
	if accepted.Command != facts.Command || accepted.Dir != facts.Dir {
		t.Fatalf("accepted attempt = %+v, want the prepared command %q in %q", accepted, facts.Command, facts.Dir)
	}
	if !reflect.DeepEqual(accepted.Env, facts.Env) {
		t.Fatalf("accepted Env = %v, want the analyzed %v", accepted.Env, facts.Env)
	}

	req := f.runner.lastRequest(t)
	if strings.TrimSpace(req.Command) != strings.TrimSpace(facts.Command) {
		t.Fatalf("runner command = %q, want the prepared %q", req.Command, facts.Command)
	}
	if req.Dir != facts.Dir {
		t.Fatalf("runner dir = %q, want the prepared %q", req.Dir, facts.Dir)
	}
	if !reflect.DeepEqual(req.Environment, facts.Environment) {
		t.Fatalf("runner environment = %v, want the prepared %v", req.Environment, facts.Environment)
	}

	evidence := f.jobs.List()
	if len(evidence) != 1 {
		t.Fatalf("managed executions = %d, want exactly 1", len(evidence))
	}
	if evidence[0].Command != facts.Command || evidence[0].Dir != facts.Dir {
		t.Fatalf("execution evidence = %q in %q, want %q in %q", evidence[0].Command, evidence[0].Dir, facts.Command, facts.Dir)
	}
}

// TestParentRebindDuringApprovalRejectsStaleProof pins that a proof bound to the
// old parent root cannot launch after the root moved: the stale launch starts no
// process, and a fresh evaluation prepares the rebound root.
func TestParentRebindDuringApprovalRejectsStaleProof(t *testing.T) {
	root := t.TempDir()
	rebound := t.TempDir()
	cfg := config.Default()
	cfg.Tools.Shell.Confirm.Commands = []string{"echo before", "echo after"}
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	runner := newFakeRunner(withResult(CommandResult{
		ExitCode: 0,
		Meta:     registry.SandboxMeta{Backend: "fixture", Enabled: true},
	}))
	jobs := newTestCommandService(t, runner, root, state, nil)
	// The session's own authority: a workspace rebind bumps its revision, which
	// is exactly what must make the pending proof stale.
	authorizer := policy.NewAuthorizer(policy.NewEngine(&cfg, nil), state.InteractionAuthority())
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:    root,
		CommandRunner:    runner,
		JobManager:       jobs,
		SessionState:     state,
		Config:           cfg,
		CommandAdmission: authorizer.BeginCommand,
		SetupAuthorizer:  authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run is not registered")
	}
	prepare := func(command string) registry.CommandFacts {
		t.Helper()
		facts, err := tool.PrepareCommand(context.Background(), registry.ToolCall{
			ID: "inv-" + command, Name: "shell.run",
			Args: []byte(`{"command":"` + command + `"}`),
		})
		if err != nil {
			t.Fatalf("PrepareCommand(%q): %v", command, err)
		}
		return facts
	}

	// The parent rebinds its root while this command's approval is pending.
	before := prepare("echo before")
	proof, evaluation, err := authorizer.Evaluate(context.Background(), policy.Call{
		InvocationID: "inv-before", ToolName: "shell.run", Shell: &before,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if evaluation.Decision != policy.DecisionConfirm {
		t.Fatalf("decision = %q, want confirm", evaluation.Decision)
	}
	state.SetWorkspace(session.Workspace{ProjectRoot: rebound, ActiveRoot: rebound})
	proof, err = authorizer.ApproveExact(proof, "approval-stale")
	if err != nil {
		t.Fatalf("ApproveExact: %v", err)
	}
	staleCtx := policy.WithAuthorization(context.Background(), proof)
	if _, err := tool.Handler(staleCtx, registry.ToolCall{
		ID: "inv-before", Name: "shell.run", Args: []byte(`{"command":"echo before"}`),
	}); err == nil {
		t.Fatal("a proof bound to the old parent root launched after the rebind")
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d, want 0: a stale proof must start no process", got)
	}

	// Reevaluation prepares the rebound root, and the retry launches there.
	after := prepare("echo after")
	if after.Dir != rebound {
		t.Fatalf("prepared dir = %q, want the rebound root %q", after.Dir, rebound)
	}
	afterProof, afterEval, err := authorizer.Evaluate(context.Background(), policy.Call{
		InvocationID: "inv-after", ToolName: "shell.run", Shell: &after,
	})
	if err != nil {
		t.Fatalf("Evaluate(after): %v", err)
	}
	if afterEval.Decision != policy.DecisionConfirm {
		t.Fatalf("after decision = %q, want confirm", afterEval.Decision)
	}
	afterProof, err = authorizer.ApproveExact(afterProof, "approval-after")
	if err != nil {
		t.Fatalf("ApproveExact(after): %v", err)
	}
	if _, err := tool.Handler(policy.WithAuthorization(context.Background(), afterProof), registry.ToolCall{
		ID: "inv-after", Name: "shell.run", Args: []byte(`{"command":"echo after"}`),
	}); err != nil {
		t.Fatalf("retry launch: %v", err)
	}
	req := runner.lastRequest(t)
	if req.Command != "echo after" || req.Dir != rebound {
		t.Fatalf("retry ran %q in %q, want %q in %q", req.Command, req.Dir, "echo after", rebound)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly the retry", got)
	}
}

// TestAcceptedFactsCannotBeRetargeted pins that an accepted launch keeps the
// command, cwd and environment it was admitted with: changing the workspace, the
// session config or the service's default directory after admission must not
// move it.
func TestAcceptedFactsCannotBeRetargeted(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	cfg := config.Default()
	state := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	runner := newFakeRunner(withResult(CommandResult{
		ExitCode: 0,
		Meta:     registry.SandboxMeta{Backend: "fixture", Enabled: true},
	}))
	jobs := newTestCommandService(t, runner, root, state, nil)
	authorizer := factsAuthorizer(t, cfg)
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		JobManager:    jobs,
		SessionState:  state,
		Config:        cfg,
		// The changes below all arrive AFTER the attempt was accepted, which is
		// the window an accepted launch must be immune to.
		CommandAdmission: func(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error) {
			admission, err := authorizer.BeginCommand(ctx, attempt)
			if err != nil {
				return nil, err
			}
			state.SetWorkspace(session.Workspace{ProjectRoot: other, ActiveRoot: other})
			jobs.SetDirFunc(func() string { return other })
			rebased := cfg
			rebased.Commands.Test = "go test ./retargeted"
			if _, rebaseErr := state.RebaseInteractionConfig(rebased); rebaseErr != nil {
				t.Errorf("RebaseInteractionConfig: %v", rebaseErr)
			}
			return admission, nil
		},
		SetupAuthorizer: authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run is not registered")
	}
	facts, err := tool.PrepareCommand(context.Background(), registry.ToolCall{
		ID: "inv-1", Name: "shell.run", Args: []byte(`{"command":"echo accepted"}`),
	})
	if err != nil {
		t.Fatalf("PrepareCommand: %v", err)
	}
	proof, evaluation, err := authorizer.Evaluate(context.Background(), policy.Call{
		InvocationID: "inv-1", ToolName: "shell.run", Shell: &facts,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if evaluation.Decision != policy.DecisionAllow {
		t.Fatalf("decision = %q, want allow", evaluation.Decision)
	}
	if _, err := tool.Handler(policy.WithAuthorization(context.Background(), proof), registry.ToolCall{
		ID: "inv-1", Name: "shell.run", Args: []byte(`{"command":"echo accepted"}`),
	}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	req := runner.lastRequest(t)
	if req.Command != facts.Command {
		t.Fatalf("command = %q, want the accepted %q", req.Command, facts.Command)
	}
	if req.Dir != root || req.Dir == other {
		t.Fatalf("dir = %q, want the accepted %q, not the retargeted %q", req.Dir, root, other)
	}
	if !reflect.DeepEqual(req.Environment, facts.Environment) {
		t.Fatalf("environment = %v, want the accepted %v", req.Environment, facts.Environment)
	}
}

// TestChildRevocationPreventsLaunch pins that a child's grant is re-read at the
// launch boundary: revoking the owner's Auto/System access between evaluation
// and admission leaves no process started, even though the evaluation allowed
// the command.
func TestChildRevocationPreventsLaunch(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	on, off := true, false

	owner := session.New(cfg, root, time.Unix(100, 0), session.Persistence{})
	if _, err := owner.ApplyInteraction(interaction.Patch{Auto: &on, System: &on}); err != nil {
		t.Fatalf("owner ApplyInteraction: %v", err)
	}
	child := session.New(cfg, root, time.Unix(100, 0), session.Persistence{},
		session.WithInteractionOwner(owner, interaction.Ceiling{Act: true, Auto: true, System: true}))

	runner := newFakeRunner(withResult(CommandResult{
		ExitCode: 0,
		Meta:     registry.SandboxMeta{Backend: "fixture", Enabled: true},
	}))
	jobs := newTestCommandService(t, runner, root, child, nil)
	authorizer := policy.NewAuthorizer(policy.NewEngine(&cfg, nil), child.InteractionAuthority())

	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: root,
		CommandRunner: runner,
		JobManager:    jobs,
		SessionState:  child,
		Config:        cfg,
		CommandAdmission: func(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error) {
			// The owner revokes the grant while the attempt sits between
			// evaluation and admission.
			if _, err := owner.ApplyInteraction(interaction.Patch{Auto: &off, System: &off}); err != nil {
				t.Errorf("owner revocation: %v", err)
			}
			return authorizer.BeginCommand(ctx, attempt)
		},
		SetupAuthorizer: authorizer,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	tool, ok := reg.Lookup("shell.run")
	if !ok {
		t.Fatal("shell.run is not registered")
	}

	const command = "rm -rf ../outside"
	facts, err := tool.PrepareCommand(context.Background(), registry.ToolCall{
		ID: "inv-1", Name: "shell.run", Args: []byte(`{"command":"` + command + `"}`),
	})
	if err != nil {
		t.Fatalf("PrepareCommand: %v", err)
	}
	proof, evaluation, err := authorizer.Evaluate(context.Background(), policy.Call{
		InvocationID: "inv-1", ToolName: "shell.run", Shell: &facts,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if evaluation.Decision != policy.DecisionAllow {
		t.Fatalf("decision = %q, want the owner's grant to allow it", evaluation.Decision)
	}

	if _, err := tool.Handler(policy.WithAuthorization(context.Background(), proof), registry.ToolCall{
		ID: "inv-1", Name: "shell.run", Args: []byte(`{"command":"` + command + `"}`),
	}); err == nil {
		t.Fatal("a revoked grant still launched the command")
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d, want 0 after the grant was revoked", got)
	}
}
