package native

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// testAuthority is a test-OWNED interaction authority. It is deliberately
// explicit: nothing about this fixture lets an unadmitted command through, so a
// test that forgets to wire admission cannot pass by accident.
type testAuthority struct {
	mu       sync.Mutex
	snap     interaction.Snapshot
	leases   int
	releases int
}

func newTestAuthority() *testAuthority {
	return &testAuthority{snap: interaction.Snapshot{
		Access: interaction.State{Mode: interaction.ModeAct, Revision: 1},
	}}
}

func (a *testAuthority) Snapshot() interaction.Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snap
}

func (a *testAuthority) BeginAdmission(ctx context.Context, expected interaction.Snapshot) (interaction.AdmissionLease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if expected != a.snap {
		return nil, policy.ErrStaleAdmission
	}
	a.leases++
	return &testLease{authority: a}, nil
}

func (a *testAuthority) setMode(mode interaction.Mode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snap.Access.Mode = mode
	a.snap.Access.Revision++
}

func (a *testAuthority) counts() (leases, releases int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.leases, a.releases
}

type testLease struct {
	authority *testAuthority
}

func (l *testLease) Release() {
	l.authority.mu.Lock()
	defer l.authority.mu.Unlock()
	l.authority.releases++
}

// testAdmission admits attempts through the REAL authorizer over a test
// authority. approve decides whether a Confirm is pre-approved; afterAdmit runs
// after the lease is taken, which is how a test reproduces an authority or
// workspace change arriving between admission and insertion.
type testAdmission struct {
	authorizer *policy.Authorizer
	authority  *testAuthority
	approve    bool
	afterAdmit func()

	mu         sync.Mutex
	admissions int
}

func newTestAdmission(t *testing.T, approve bool) *testAdmission {
	t.Helper()
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	authority := newTestAuthority()
	return &testAdmission{
		authorizer: policy.NewAuthorizer(policy.NewEngine(&cfg, nil), authority),
		authority:  authority,
		approve:    approve,
	}
}

func (f *testAdmission) admit(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error) {
	call := policy.Call{InvocationID: attempt.InvocationID, ToolName: attempt.ToolName, Shell: &attempt.Input}
	proof, evaluation, err := f.authorizer.Evaluate(ctx, call)
	if err != nil {
		return nil, err
	}
	switch evaluation.Decision {
	case policy.DecisionDeny:
		return nil, policy.ErrApprovalRequired
	case policy.DecisionConfirm:
		if !f.approve {
			return nil, policy.ErrApprovalRequired
		}
		if _, err := f.authorizer.ApproveExact(proof, "approval-1"); err != nil {
			return nil, err
		}
	}
	admission, err := f.authorizer.BeginCommand(policy.WithAuthorization(ctx, proof), attempt)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.admissions++
	f.mu.Unlock()
	if f.afterAdmit != nil {
		f.afterAdmit()
	}
	return admission, nil
}

func (f *testAdmission) admissionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.admissions
}

// registerManagedCommands builds a registry whose shell.run/test.run dispatch
// through jm with the given admission seam.
func registerManagedCommands(t *testing.T, root string, runner CommandRunner, jm *JobManager, state *session.State, admit func(context.Context, policy.CommandAttempt) (*policy.Admission, error)) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := RegisterAll(reg, Options{
		WorkspaceRoot:    root,
		CommandRunner:    runner,
		JobManager:       jm,
		SessionState:     state,
		Guardrail:        func(string, bool) error { return nil },
		CommandAdmission: admit,
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	return reg
}

// TestManagedCommandApprovedDestructionLaunchesOnce pins that an approved
// destructive command is admitted once and launches exactly one process: the
// authorizer sees the attempt at the accepted launch, not a second classified
// string later.
func TestManagedCommandApprovedDestructionLaunchesOnce(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
	jm := newTestCommandService(t, runner, root, nil, nil)
	admission := newTestAdmission(t, true)
	reg := registerManagedCommands(t, root, runner, jm, nil, admission.admit)

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"git reset --hard"}`); err != nil {
		t.Fatalf("shell.run: %v", err)
	}
	if got := admission.admissionCount(); got != 1 {
		t.Fatalf("admissions = %d, want exactly 1", got)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1 launch", got)
	}
	if _, releases := admission.authority.counts(); releases != 1 {
		t.Fatalf("lease releases = %d, want 1: the lease must not outlive the launch", releases)
	}
}

// TestManagedCommandAskBlocksUnadmittedLaunch pins that a command whose
// evaluation requires approval cannot launch when no approval was granted, and
// leaves no execution, no slot and no lease behind.
func TestManagedCommandAskBlocksUnadmittedLaunch(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
	jm := newTestCommandService(t, runner, root, nil, nil)
	admission := newTestAdmission(t, false)
	reg := registerManagedCommands(t, root, runner, jm, nil, admission.admit)

	_, err := invokeTool(t, reg, "shell.run", `{"command":"git reset --hard"}`)
	if !errors.Is(err, policy.ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired", err)
	}
	if got := admission.admissionCount(); got != 0 {
		t.Fatalf("admissions = %d, want 0", got)
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d, want 0 (nothing may be launched)", got)
	}
	if got := jm.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount = %d, want 0", got)
	}
}

// TestManagedCommandAdmittedBeforeAskKeepsCapturedIdentity pins that an
// authority switch arriving between admission and insertion cannot retarget or
// abort the command that was already approved: it launches with the exact
// command and directory the admission bound.
func TestManagedCommandAdmittedBeforeAskKeepsCapturedIdentity(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
	jm := newTestCommandService(t, runner, root, nil, nil)
	admission := newTestAdmission(t, true)
	admission.afterAdmit = func() { admission.authority.setMode(interaction.ModeAsk) }
	reg := registerManagedCommands(t, root, runner, jm, nil, admission.admit)

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"git reset --hard"}`); err != nil {
		t.Fatalf("shell.run: %v", err)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want exactly 1 launch", got)
	}
	req := runner.lastRequest(t)
	if req.Command != "git reset --hard" {
		t.Fatalf("command = %q, want the admitted command unchanged", req.Command)
	}
	if req.Dir != root {
		t.Fatalf("dir = %q, want the admitted directory %q", req.Dir, root)
	}
}

// TestManagedCommandWorkspaceCannotRetargetAfterApproval pins that changing the
// session's workspace after admission does not move the approved command: it
// runs in the directory the admission captured.
func TestManagedCommandWorkspaceCannotRetargetAfterApproval(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	state := session.New(config.Default(), root, time.Unix(100, 0), session.Persistence{})
	runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
	jm := newTestCommandService(t, runner, root, state, nil)
	admission := newTestAdmission(t, true)
	admission.afterAdmit = func() {
		state.SetWorkspace(session.Workspace{ProjectRoot: other, ActiveRoot: other})
	}
	reg := registerManagedCommands(t, root, runner, jm, state, admission.admit)

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"git reset --hard"}`); err != nil {
		t.Fatalf("shell.run: %v", err)
	}
	req := runner.lastRequest(t)
	if req.Dir != root {
		t.Fatalf("dir = %q, want the admitted directory %q, not the retargeted workspace", req.Dir, root)
	}
}

// TestManagedCommandRejectedCapacityDoesNotExecute pins that a launch rejected
// for capacity never reaches the runner and never leaks the admission lease it
// took before the capacity check.
func TestManagedCommandRejectedCapacityDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	release := make(chan struct{})
	runner := newFakeRunner(withRelease(release), withPID(9001))
	jm := NewCommandService(context.Background(), LaunchPolicy{
		Runner:         runner,
		Dir:            root,
		MaxJobs:        1,
		MaxOutputBytes: 1 << 20,
	})
	t.Cleanup(func() {
		close(release)
		_ = jm.Shutdown(context.Background())
	})

	// Occupy the only slot with a command that stays running.
	if _, err := jm.Launch(context.Background(), LaunchSpec{Command: "sleep 1000", Detached: true}); err != nil {
		t.Fatalf("occupying launch: %v", err)
	}

	admission := newTestAdmission(t, true)
	reg := registerManagedCommands(t, root, runner, jm, nil, admission.admit)
	_, err := invokeTool(t, reg, "shell.run", `{"command":"git reset --hard"}`)
	if err == nil || !strings.Contains(err.Error(), "too many running commands") {
		t.Fatalf("err = %v, want a capacity rejection", err)
	}
	if got := admission.admissionCount(); got != 1 {
		t.Fatalf("admissions = %d, want 1 (the attempt reached the authorizer)", got)
	}
	if _, releases := admission.authority.counts(); releases != 1 {
		t.Fatalf("lease releases = %d, want 1: a rejected launch must not hold the admission gate", releases)
	}
	if got := runner.countRuns(); got != 1 {
		t.Fatalf("runner runs = %d, want only the occupying command", got)
	}
	if got := jm.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount = %d, want 1", got)
	}
}

// TestManagedCommandMissingAuthorityFailsClosed pins that a missing authority is
// a refusal, not a trusted bypass: no process is launched.
func TestManagedCommandMissingAuthorityFailsClosed(t *testing.T) {
	root := t.TempDir()
	runner := newFakeRunner(withResult(CommandResult{ExitCode: 0}))
	jm := newTestCommandService(t, runner, root, nil, nil)
	cfg := config.Default()
	noAuthority := policy.NewAuthorizer(policy.NewEngine(&cfg, nil), nil)
	admit := func(ctx context.Context, attempt policy.CommandAttempt) (*policy.Admission, error) {
		return noAuthority.BeginCommand(ctx, attempt)
	}
	reg := registerManagedCommands(t, root, runner, jm, nil, admit)

	_, err := invokeTool(t, reg, "shell.run", `{"command":"cat README.md"}`)
	if !errors.Is(err, policy.ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired", err)
	}
	if got := runner.countRuns(); got != 0 {
		t.Fatalf("runner runs = %d, want 0 (nothing may be launched)", got)
	}
	if got := jm.ActiveCount(); got != 0 {
		t.Fatalf("ActiveCount = %d, want 0", got)
	}
}
