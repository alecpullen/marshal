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

type fakeTitleGenerator struct{}

func (fakeTitleGenerator) Generate(context.Context, string) {}

// TestCopyFromCopiesDecodingDegradedNotGuards guards the reload path:
// DecodingDegraded is copied so a rebuilt runner keeps advertising the
// degraded provider state, but the per-instance decoding notice guards are
// NOT copied, so a rebuilt runner re-arms them and re-emits the notice once
// on its own first turn (intentionally — the reload produces a fresh runner
// instance whose notices should fire again).
func TestCopyFromCopiesDecodingDegradedNotGuards(t *testing.T) {
	source := &Runner{
		DecodingDegraded:             true,
		decodingStartupNoticeSent:    true,
		decodingEscalationNoticeSent: true,
	}
	target := &Runner{}
	target.CopyFrom(source)

	if !target.DecodingDegraded {
		t.Fatal("CopyFrom did not transfer DecodingDegraded")
	}
	if target.decodingStartupNoticeSent {
		t.Fatal("CopyFrom transferred the per-instance startup notice guard")
	}
	if target.decodingEscalationNoticeSent {
		t.Fatal("CopyFrom transferred the per-instance escalation notice guard")
	}
}
