package agent

import (
	"context"
	"testing"
)

// TestCopyFromRefreshesHooks guards the reload path: app.reloadAgentRuntime
// mutates a live runner in place via CopyFrom, so the rebuilt runner's
// session-scoped hooks must be adopted or a config reload silently keeps the
// stale TitleGenerator/Classifier closures bound to the old route.
func TestCopyFromRefreshesHooks(t *testing.T) {
	classifierCalled := false
	classifier := func(ctx context.Context, goal string) (TaskClass, error) {
		classifierCalled = true
		return ClassEdit, nil
	}

	source := &Runner{
		HookRunner:     fakeHookRunner{},
		TitleGenerator: fakeTitleGenerator{},
		Classifier:     classifier,
	}
	target := &Runner{}
	target.CopyFrom(source)

	if target.Classifier == nil {
		t.Fatal("CopyFrom did not transfer Classifier")
	}
	if _, err := target.Classifier(context.Background(), "goal"); err != nil {
		t.Fatalf("transferred Classifier errored: %v", err)
	}
	if !classifierCalled {
		t.Fatal("transferred Classifier closure was not the source's")
	}
	if target.TitleGenerator == nil {
		t.Fatal("CopyFrom did not transfer TitleGenerator")
	}
	if target.HookRunner == nil {
		t.Fatal("CopyFrom did not transfer HookRunner")
	}

	// A target that had prior hooks must have them replaced, not preserved.
	source2 := &Runner{}
	target2 := &Runner{Classifier: classifier}
	target2.CopyFrom(source2)
	if target2.Classifier != nil {
		t.Fatal("CopyFrom preserved a stale Classifier when source had nil")
	}
}

// TestCopyFromTransfersVerificationGate guards the reload path for the
// verification-gate fallback: app.reloadAgentRuntime rebuilds the runner and
// mutates the live one in place via CopyFrom, so the global gate flag must
// transfer, and a gate-off source must clear a stale on-value.
func TestCopyFromTransfersVerificationGate(t *testing.T) {
	target := &Runner{}
	target.CopyFrom(&Runner{VerificationGate: true})
	if !target.VerificationGate {
		t.Fatal("CopyFrom did not transfer VerificationGate")
	}

	target2 := &Runner{VerificationGate: true}
	target2.CopyFrom(&Runner{})
	if target2.VerificationGate {
		t.Fatal("CopyFrom preserved a stale VerificationGate when source had false")
	}
}

type fakeTitleGenerator struct{}

func (fakeTitleGenerator) Generate(context.Context, string) {}
