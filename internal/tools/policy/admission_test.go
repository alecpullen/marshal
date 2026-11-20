package policy

import (
	"context"
	"errors"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/permissions"
	"marshal/internal/tools/registry"
)

type fakeLease struct{ released bool }

func (l *fakeLease) Release() { l.released = true }

type fakeAuthority struct {
	snap  interaction.Snapshot
	lease *fakeLease
	err   error
}

func (a *fakeAuthority) Snapshot() interaction.Snapshot { return a.snap }

func (a *fakeAuthority) BeginAdmission(ctx context.Context, expected interaction.Snapshot) (interaction.AdmissionLease, error) {
	if a.err != nil {
		return nil, a.err
	}
	if expected != a.snap {
		return nil, ErrStaleAdmission
	}
	return a.lease, nil
}

func admissionFixture(t *testing.T) (*Authorizer, *fakeAuthority) {
	t.Helper()
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	auth := &fakeAuthority{
		snap:  interaction.Snapshot{Access: interaction.State{Mode: interaction.ModeAct, Revision: 1}},
		lease: &fakeLease{},
	}
	return NewAuthorizer(pe, auth), auth
}

// TestValidatedTargetsDecideNonShellScope pins that a caller's validated
// targets — the ones the tool's own resolver accepted for this operation —
// decide whether a non-shell write needs System. An approved additional write
// root and a read-only reference are indistinguishable from the argument
// spelling, and policy must not demand System for work the tool will do.
func TestValidatedTargetsDecideNonShellScope(t *testing.T) {
	cfg := config.Default()
	cfg.Tools.Shell.Deny.Patterns = nil
	pe := NewEngine(&cfg, nil)
	// The engine classifies a tool by its registered risk, so the tool under
	// test must be registered as the workspace write it really is.
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "file.write",
		Risk: registry.RiskWorkspaceWrite,
		// The engine reads only the risk; the handler exists because a
		// registrable tool must be dispatchable.
		Handler: func(context.Context, registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{}, nil
		},
	}); err != nil {
		t.Fatalf("register file.write: %v", err)
	}
	pe.WithRegistry(reg)
	az := NewAuthorizer(pe, &fakeAuthority{
		snap:  interaction.Snapshot{Access: interaction.State{Mode: interaction.ModeAct, Revision: 1}},
		lease: &fakeLease{},
	})

	permitted := Call{
		InvocationID: "inv-granted",
		ToolName:     "file.write",
		Args:         map[string]any{"path": "/elsewhere/granted.txt", "content": "x"},
		Targets: []ShellTarget{{
			Path:      "/elsewhere/granted.txt",
			Effect:    ShellWrite,
			Validated: true,
		}},
	}
	if _, ev, err := az.Evaluate(context.Background(), permitted); err != nil {
		t.Fatalf("Evaluate(permitted): %v", err)
	} else if ev.Decision != DecisionAllow || ev.Required.System {
		t.Fatalf("permitted write = %q (System=%v), want allow with no System", ev.Decision, ev.Required.System)
	}

	// The same argument spelling, but nothing proved the tool may write it: an
	// unvalidated unknown target fails closed onto the System requirement.
	unplaced := Call{
		InvocationID: "inv-unknown",
		ToolName:     "file.write",
		Args:         map[string]any{"path": "/elsewhere/granted.txt", "content": "x"},
		Targets:      []ShellTarget{{Path: "/elsewhere/granted.txt", Scope: ShellScopeUnknown, Effect: ShellWrite}},
	}
	if _, ev, err := az.Evaluate(context.Background(), unplaced); err != nil {
		t.Fatalf("Evaluate(unplaced): %v", err)
	} else if ev.Decision != DecisionConfirm || !ev.Required.System {
		t.Fatalf("unplaced write = %q (System=%v), want confirm requiring System", ev.Decision, ev.Required.System)
	}
}

// TestFingerprintBindsValidatedTargets pins that the targets an evaluation
// bound cannot be retargeted between evaluation and admission.
func TestFingerprintBindsValidatedTargets(t *testing.T) {
	az, _ := admissionFixture(t)
	call := Call{
		InvocationID: "inv-1",
		ToolName:     "file.write",
		Args:         map[string]any{"path": "/elsewhere/a.txt", "content": "x"},
		Targets:      []ShellTarget{{Path: "/elsewhere/a.txt", Effect: ShellWrite, Validated: true}},
	}
	proof, _, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	retargeted := call
	retargeted.Targets = []ShellTarget{{Path: "/elsewhere/b.txt", Effect: ShellWrite, Validated: true}}
	ctx := WithAuthorization(context.Background(), proof)
	if _, err := az.BeginTool(ctx, retargeted); !errors.Is(err, ErrStaleAdmission) {
		t.Fatalf("retargeted write = %v, want ErrStaleAdmission", err)
	}
}

func TestAdmissionBindsExactInvocationCommandAndDirectory(t *testing.T) {
	az, _ := admissionFixture(t)
	call := Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &ShellInput{Command: "cat x", Dir: "/repo", WorkspaceRoot: "/repo"}}
	proof, ev, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Decision != DecisionAllow {
		t.Fatalf("decision = %q, want allow", ev.Decision)
	}
	ctx := WithAuthorization(context.Background(), proof)
	adm, err := az.BeginTool(ctx, call)
	if err != nil {
		t.Fatalf("BeginTool: %v", err)
	}
	if err := adm.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	adm.Release()

	changed := call
	changed.Shell = &ShellInput{Command: "cat y", Dir: "/repo", WorkspaceRoot: "/repo"}
	if _, err := az.BeginTool(ctx, changed); !errors.Is(err, ErrStaleAdmission) {
		t.Fatalf("changed command = %v, want ErrStaleAdmission", err)
	}
}

func TestAdmissionRechecksSymlinkFactsAndConfig(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	rev := uint64(1)
	pe.SetConfigSource(func() (config.Config, []string, []session.PermissionGrant, uint64) { return cfg, nil, nil, rev })
	auth := &fakeAuthority{
		snap:  interaction.Snapshot{Access: interaction.State{Mode: interaction.ModeAct, Revision: 1}},
		lease: &fakeLease{},
	}
	az := NewAuthorizer(pe, auth)
	call := Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &ShellInput{Command: "cat x", Dir: "/repo", WorkspaceRoot: "/repo"}}
	proof, _, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	rev = 2
	ctx := WithAuthorization(context.Background(), proof)
	if _, err := az.BeginTool(ctx, call); !errors.Is(err, ErrStalePolicy) {
		t.Fatalf("config change = %v, want ErrStalePolicy", err)
	}
}

func TestAdmissionCannotReuseApproval(t *testing.T) {
	az, _ := admissionFixture(t)
	az.engine.SetRules([]permissions.Rule{{Permission: "shell", Pattern: "*", Action: permissions.ActionAsk}})
	call := Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &ShellInput{Command: "git reset --hard", Dir: "/repo", WorkspaceRoot: "/repo"}}
	proof, ev, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Decision != DecisionConfirm {
		t.Fatalf("decision = %q, want confirm", ev.Decision)
	}
	if _, err := az.ApproveExact(proof, "req-1"); err != nil {
		t.Fatalf("ApproveExact: %v", err)
	}
	ctx := WithAuthorization(context.Background(), proof)
	adm, err := az.BeginTool(ctx, call)
	if err != nil {
		t.Fatalf("BeginTool: %v", err)
	}
	if err := adm.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	adm.Release()
	if _, err := az.BeginTool(ctx, call); !errors.Is(err, ErrStaleAdmission) {
		t.Fatalf("reused proof = %v, want ErrStaleAdmission", err)
	}
}

func TestAdmissionAskWinsBeforeCommit(t *testing.T) {
	az, auth := admissionFixture(t)
	call := Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &ShellInput{Command: "git reset --hard", Dir: "/repo", WorkspaceRoot: "/repo"}}
	proof, _, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// The owner switches to Ask before admission.
	auth.snap = interaction.Snapshot{Access: interaction.State{Mode: interaction.ModeAsk, Revision: 2}}
	ctx := WithAuthorization(context.Background(), proof)
	if _, err := az.BeginTool(ctx, call); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Ask switch = %v, want ErrApprovalRequired", err)
	}
}

func TestAdmissionMissingAuthorityFailsClosed(t *testing.T) {
	cfg := config.Default()
	pe := NewEngine(&cfg, nil)
	az := NewAuthorizer(pe, nil)
	if _, _, err := az.Evaluate(context.Background(), Call{}); err == nil {
		t.Fatal("Evaluate without an authority succeeded")
	}
}

func TestFingerprintBindsEveryAnalysisInput(t *testing.T) {
	newBase := func() Call {
		return Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &ShellInput{
			Command:       "cat x",
			Dir:           "/repo",
			WorkspaceRoot: "/repo",
			HomeDir:       "/home/u",
			ReadRoots:     []string{"/docs"},
			Env:           map[string]string{"A": "1"},
		}}
	}
	want := fingerprintCall(newBase())
	if fingerprintCall(newBase()) != want {
		t.Fatal("fingerprint is not deterministic")
	}

	mutations := map[string]func() Call{
		"home dir":  func() Call { c := newBase(); c.Shell.HomeDir = "/home/other"; return c },
		"read root": func() Call { c := newBase(); c.Shell.ReadRoots = []string{"/elsewhere"}; return c },
		"env value": func() Call { c := newBase(); c.Shell.Env = map[string]string{"A": "2"}; return c },
		"env key":   func() Call { c := newBase(); c.Shell.Env = map[string]string{"B": "1"}; return c },
		"args":      func() Call { c := newBase(); c.Args = map[string]any{"command": "cat x"}; return c },
	}
	for name, build := range mutations {
		if fingerprintCall(build()) == want {
			t.Fatalf("%s is not bound by the fingerprint", name)
		}
	}
}

func TestAdmissionBackgroundAllowNeverManufacturesApproval(t *testing.T) {
	az, _ := admissionFixture(t)
	az.engine.SetRules([]permissions.Rule{{Permission: "shell", Pattern: "*", Action: permissions.ActionAsk}})
	call := Call{InvocationID: "inv-1", ToolName: "shell.run", Shell: &ShellInput{Command: "git reset --hard", Dir: "/repo", WorkspaceRoot: "/repo"}}
	proof, ev, err := az.Evaluate(context.Background(), call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ev.Decision != DecisionConfirm {
		t.Fatalf("decision = %q, want confirm", ev.Decision)
	}
	ctx := WithAuthorization(context.Background(), proof)
	if _, err := az.BeginTool(ctx, call); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("unapproved confirm = %v, want ErrApprovalRequired", err)
	}
}
