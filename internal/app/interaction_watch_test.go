package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/interaction"
	"marshal/internal/tools/policy"
)

// watchTestOwner builds a fresh root conversation actor and its authorizer.
// The authority reads the owner's live tuple, so switching the owner to Ask
// affects the next sample without rebuilding the authorizer.
func watchTestOwner(t *testing.T) (*session.State, *policy.Authorizer) {
	t.Helper()
	cfg := config.Default()
	cfg.Tools.Shell.Confirm.Commands = []string{"git reset --hard"}
	state := session.New(cfg, t.TempDir(), time.Unix(1, 0), session.Persistence{})
	pol := policy.NewEngine(&cfg, nil)
	return state, policy.NewAuthorizer(pol, state.InteractionAuthority())
}

// admitWatchSample commits and releases one authorized sample. Tests that only
// need the authorization result call authorizeWatchSample directly.
func admitWatchSample(t *testing.T, admission *policy.Admission) {
	t.Helper()
	if err := admission.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	admission.Release()
}

func TestWatchNextMutationSampleStopsAfterAsk(t *testing.T) {
	owner, az := watchTestOwner(t)
	ctx := context.Background()

	// Act admits a plain filesystem write.
	admission, err := authorizeWatchSample(ctx, az, watchSampleAttempt("echo hi > out.txt", owner.WorkingDir))
	if err != nil {
		t.Fatalf("Act write sample: %v", err)
	}
	admitWatchSample(t, admission)

	// After the owner selects Ask, the next mutation sample is denied without
	// opening a background approval prompt or inventing Auto.
	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner Ask: %v", err)
	}
	if _, err := authorizeWatchSample(ctx, az, watchSampleAttempt("echo bye > out2.txt", owner.WorkingDir)); !errors.Is(err, errWatchSampleNotApproved) {
		t.Fatalf("mutation sample after Ask = %v, want errWatchSampleNotApproved", err)
	}
}

func TestWatchReadSampleContinuesInAsk(t *testing.T) {
	owner, az := watchTestOwner(t)
	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner Ask: %v", err)
	}

	admission, err := authorizeWatchSample(context.Background(), az, watchSampleAttempt("cat README.md", owner.WorkingDir))
	if err != nil {
		t.Fatalf("read sample in Ask: %v", err)
	}
	admitWatchSample(t, admission)
}

func TestWatchAdmittedSampleKeepsCapturedSettings(t *testing.T) {
	owner, az := watchTestOwner(t)
	captured := owner.WorkingDir + "/captured-root"
	attempt := watchSampleAttempt("echo hi > out.txt", captured)

	admission, err := authorizeWatchSample(context.Background(), az, attempt)
	if err != nil {
		t.Fatalf("admitted sample: %v", err)
	}
	// The admitted sample is bound to the exact facts it was evaluated with,
	// even if the owner changes settings afterwards.
	admitWatchSample(t, admission)

	ask := interaction.ModeAsk
	if _, err := owner.ApplyInteraction(interaction.Patch{Mode: &ask}); err != nil {
		t.Fatalf("owner Ask: %v", err)
	}
	if attempt.Input.Dir != captured || attempt.Input.WorkspaceRoot != captured {
		t.Fatalf("captured attempt = %+v, want dir/workspace %q", attempt.Input, captured)
	}
}

func TestWatchConfirmNeverBecomesBackgroundApproval(t *testing.T) {
	owner, az := watchTestOwner(t)
	ctx := context.Background()
	attempt := watchSampleAttempt("git reset --hard", owner.WorkingDir)

	// Confirm the classification first: a destructive operation in Act needs
	// exact approval, which a background watch must never solicit.
	call := policy.Call{InvocationID: attempt.InvocationID, ToolName: attempt.ToolName, Shell: &attempt.Input}
	_, evaluation, err := az.Evaluate(ctx, call)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if evaluation.Decision != policy.DecisionConfirm {
		t.Fatalf("git reset --hard = %s, want Confirm in Act", evaluation.Decision)
	}

	if _, err := authorizeWatchSample(ctx, az, attempt); !errors.Is(err, errWatchSampleNotApproved) {
		t.Fatalf("Confirm sample = %v, want errWatchSampleNotApproved", err)
	}
}
