package agent

import (
	"context"
	"testing"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
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

// TestCopyFromCopiesDecodingDegraded guards the reload path:
// DecodingDegraded is copied so a rebuilt runner keeps advertising the
// degraded provider state. The notice lifecycle has no per-instance state
// left to reset — the startup notice re-emits every turn and the escalation
// re-fires on every tally — so the reload path needs no guard re-arming.
func TestCopyFromCopiesDecodingDegraded(t *testing.T) {
	source := &Runner{DecodingDegraded: true}
	target := &Runner{}
	target.CopyFrom(source)

	if !target.DecodingDegraded {
		t.Fatal("CopyFrom did not transfer DecodingDegraded")
	}

	// Behavioral pin on the reload path: a rebuilt degraded runner re-emits
	// the startup notice on its own first turn. Field-transfer assertions
	// alone cannot catch a regression where the rebuilt runner stays silent.
	live := NewRunner(&agenttest.ScriptedProvider{Responses: []string{finalActionJSON}},
		registry.New(), policy.NewEngine(&config.Config{}, nil), newTestState(t), "test-model")
	live.DecodingDegraded = true
	target2 := NewRunner(nil, nil, nil, nil, "")
	target2.CopyFrom(live)
	if err := target2.Run(context.Background(), "reload turn"); err != nil {
		t.Fatalf("rebuilt runner Run: %v", err)
	}
	if _, ok := live.State.Notice(); !ok {
		t.Fatal("rebuilt degraded runner emitted no startup notice on its first turn")
	}
}
