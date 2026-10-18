package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/pubsub"
)

// effortRuntime builds a Runtime with an active route over a preset that
// advertises the given thinking levels, mirroring how other runtime tests
// assemble state.
func effortRuntime(t *testing.T, levels []string) (*Runtime, *session.State) {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{
		"p": {Type: "openai_compatible", BaseURL: "http://localhost:11434/v1"},
	}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"m": {Provider: "p", Model: "m", Thinking: "high",
			ThinkingOptions: &schema.ThinkingOptions{Levels: levels}},
	}
	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "m", Provider: "p", Model: "m",
		Thinking: "high", ReasoningCapable: true})
	rt := &Runtime{State: state, DataDir: t.TempDir()}
	return rt, state
}

func TestSetSessionEffortRejectsUnsupportedLevel(t *testing.T) {
	rt, state := effortRuntime(t, []string{"low", "medium", "high"})
	_, err := rt.SetSessionEffort("max")
	if err == nil {
		t.Fatal("an unsupported level must be rejected")
	}
	if !strings.Contains(err.Error(), "low, medium, high") {
		t.Fatalf("error %q does not list the valid levels", err)
	}
	if got := state.EffortOverride().Level; got != "" {
		t.Fatalf("override = %q, want no override recorded after a rejection", got)
	}
}

func TestSetSessionEffortIdleAppliesNextTurn(t *testing.T) {
	rt, state := effortRuntime(t, []string{"low", "medium", "high"})
	applies, err := rt.SetSessionEffort("low")
	if err != nil {
		t.Fatalf("SetSessionEffort: %v", err)
	}
	if applies != session.EffortAppliesNextTurn {
		t.Fatalf("applies = %q, want next_turn when no turn runs", applies)
	}
	if got := state.EffortOverride().Level; got != "low" {
		t.Fatalf("override = %q, want low", got)
	}
}

func TestSetSessionEffortRunningLevelChangeAppliesNextStep(t *testing.T) {
	rt, state := effortRuntime(t, []string{"low", "medium", "high"})
	state.SetTurnRoute(session.RouteInfo{Provider: "p", Model: "m", Thinking: "high"})
	applies, err := rt.SetSessionEffort("low")
	if err != nil {
		t.Fatalf("SetSessionEffort: %v", err)
	}
	if applies != session.EffortAppliesNextStep {
		t.Fatalf("applies = %q, want next_step for a running turn staying on", applies)
	}
}

func TestSetSessionEffortRunningOffAppliesNextTurn(t *testing.T) {
	rt, state := effortRuntime(t, []string{"low", "medium", "high", "off"})
	state.SetTurnRoute(session.RouteInfo{Provider: "p", Model: "m", Thinking: "high"})
	applies, err := rt.SetSessionEffort("off")
	if err != nil {
		t.Fatalf("SetSessionEffort: %v", err)
	}
	if applies != session.EffortAppliesNextTurn {
		t.Fatalf("applies = %q, want next_turn when the change crosses the off boundary", applies)
	}
}

func TestSetSessionEffortRefusesIncapableRoute(t *testing.T) {
	rt, state := effortRuntime(t, []string{"low", "medium", "high"})
	state.SetActiveRoute(session.RouteInfo{Active: true, Preset: "m", Provider: "p", Model: "m",
		Thinking: "high", ReasoningCapable: false})
	_, err := rt.SetSessionEffort("low")
	if err == nil || !strings.Contains(err.Error(), "no effort control") {
		t.Fatalf("err = %v, want an error refusing an incapable route", err)
	}
}

func TestSetSessionEffortPublishesEvent(t *testing.T) {
	rt, state := effortRuntime(t, []string{"low", "medium", "high"})
	state.SetTurnRoute(session.RouteInfo{Provider: "p", Model: "m", Thinking: "high"})
	broker := pubsub.NewBroker[session.Event]()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := broker.Subscribe(ctx)
	state.SetEventBroker(broker)
	if _, err := rt.SetSessionEffort("low"); err != nil {
		t.Fatalf("SetSessionEffort: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Type != session.EventEffortChanged || ev.Payload.Effort == nil {
			t.Fatalf("event = %+v", ev)
		}
		if ev.Payload.Effort.Effective != "high" || ev.Payload.Effort.Pending != "low" ||
			ev.Payload.Effort.AppliesFrom != session.EffortAppliesNextStep {
			t.Fatalf("event payload = %+v", ev.Payload.Effort)
		}
	case <-time.After(time.Second):
		t.Fatal("no effort_changed event published")
	}
}
