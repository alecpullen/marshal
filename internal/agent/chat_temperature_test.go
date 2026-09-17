package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"marshal/internal/agent/agenttest"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/llm/routing"
	"marshal/internal/llm/schema"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
)

// TestChatOnceDropsTemperatureOnLockedProvider pins the wire contract: a
// provider marked temperature-locked never receives the routed preset
// temperature, and the suppression is logged with provider and value.
func TestChatOnceDropsTemperatureOnLockedProvider(t *testing.T) {
	var logBuf bytes.Buffer
	temp := 0.2
	p := &agenttest.ScriptedProvider{
		Responses:    []string{"{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}"},
		ProviderCaps: schema.ProviderCapabilities{TemperatureLocked: true},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: logger})
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "locked", Temperature: &temp},
	}}

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("provider was never called")
	}
	if p.Requests[0].Temperature != nil {
		t.Fatalf("request Temperature = %v, want nil (provider is temperature-locked)", p.Requests[0].Temperature)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "temperature dropped") {
		t.Fatalf("expected a 'temperature dropped' log line, got:\n%s", logs)
	}
	if !strings.Contains(logs, "provider=scripted") || !strings.Contains(logs, "temperature=0.2") {
		t.Fatalf("drop log must name the provider and the dropped value, got:\n%s", logs)
	}
}

// TestChatOnceDropsTemperatureOverrideOnLockedProvider covers the runner's
// TemperatureOverride path: it must be suppressed on a locked provider too.
func TestChatOnceDropsTemperatureOverrideOnLockedProvider(t *testing.T) {
	temp := 0.2
	p := &agenttest.ScriptedProvider{
		Responses:    []string{"{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}"},
		ProviderCaps: schema.ProviderCapabilities{TemperatureLocked: true},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	runner := NewRunner(p, reg, pol, newTestState(t), "test-model")
	runner.TemperatureOverride = &temp
	runner.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "locked"},
	}}

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(p.Requests) == 0 {
		t.Fatal("provider was never called")
	}
	if p.Requests[0].Temperature != nil {
		t.Fatalf("request Temperature = %v, want nil (override must be suppressed too)", p.Requests[0].Temperature)
	}
}

// TestChatOnceKeepsTemperatureOnUnlockedProvider: byte-identical passthrough
// and no drop log when the capability is absent.
func TestChatOnceKeepsTemperatureOnUnlockedProvider(t *testing.T) {
	var logBuf bytes.Buffer
	temp := 0.2
	p := &agenttest.ScriptedProvider{
		Responses: []string{"{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}"},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: logger})
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "m", Temperature: &temp},
	}}

	if err := runner.Run(context.Background(), "do the thing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if p.Requests[0].Temperature == nil || *p.Requests[0].Temperature != 0.2 {
		t.Fatalf("request Temperature = %v, want 0.2 (provider is not locked)", p.Requests[0].Temperature)
	}
	if strings.Contains(logBuf.String(), "temperature dropped") {
		t.Fatalf("unlocked provider must not log a drop:\n%s", logBuf.String())
	}
}

// TestChatOnceWarnsTemperatureLockedOncePerProvider pins the warn-once latch:
// two turns against the same locked provider log exactly one drop line.
func TestChatOnceWarnsTemperatureLockedOncePerProvider(t *testing.T) {
	var logBuf bytes.Buffer
	temp := 0.2
	p := &agenttest.ScriptedProvider{
		Responses: []string{
			"{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}",
			"{\"rationale\":\"r\",\"action\":{\"type\":\"answer\",\"content\":\"done\"}}",
		},
		ProviderCaps: schema.ProviderCapabilities{TemperatureLocked: true},
	}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	state := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{Logger: logger})
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.RouteResolver = &staticResolver{route: routing.Route{
		Preset: routing.ModelPreset{Name: "test", Model: "locked", Temperature: &temp},
	}}

	for i := 0; i < 2; i++ {
		if err := runner.Run(context.Background(), "do the thing"); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	if got := strings.Count(logBuf.String(), "temperature dropped"); got != 1 {
		t.Fatalf("'temperature dropped' logged %d times, want exactly 1:\n%s", got, logBuf.String())
	}
}
