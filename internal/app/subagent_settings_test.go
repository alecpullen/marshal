package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/trust"
)

// startSubagentSettingsRuntime starts a real runtime over the native-tool test
// config, optionally binding a resolved child-settings snapshot the way a
// client's session/new does.
func startSubagentSettingsRuntime(t *testing.T, settings *session.SubagentSettings) *Runtime {
	t.Helper()
	opts := []Option{
		WithWorkingDir(t.TempDir()),
		WithHomeDir(t.TempDir()),
		WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
		WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
			return nativeToolAgentConfig("subagent-settings"), nil
		}),
	}
	if settings != nil {
		opts = append(opts, WithSubagentSettings(*settings))
	}
	rt, err := StartRuntime(context.Background(), opts...)
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

// TestSubagentDefaultsDerivedFromConfigAndRoute pins the ordinary defaults a
// session reports: the enabled bit, an empty model (normal child routing, never
// the worker model) and the configured cap normalized the way the session
// normalizes it — plus a digest of the inputs, which follows the cap.
func TestSubagentDefaultsDerivedFromConfigAndRoute(t *testing.T) {
	ctx := context.Background()
	cfg := nativeToolAgentConfig("subagent-defaults")
	cfg.Agent.MaxConcurrentSubagents = 5

	state := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	if _, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, state, nil, 0, nil, "", nil, nil, nil, ""); err != nil {
		t.Fatalf("buildAgentRunner: %v", err)
	}

	defaults, ok := state.SubagentDefaults()
	if !ok {
		t.Fatal("SubagentDefaults() = false after the runner build, want the derived defaults")
	}
	if !defaults.Settings.Enabled {
		t.Error("defaults.Settings.Enabled = false, want true (ordinary delegation is offered)")
	}
	if defaults.Settings.Model != "" {
		t.Errorf("defaults.Settings.Model = %q, want empty (normal child routing)", defaults.Settings.Model)
	}
	if defaults.Settings.Concurrency != 5 {
		t.Errorf("defaults.Settings.Concurrency = %d, want the configured 5", defaults.Settings.Concurrency)
	}
	if defaults.Digest == "" {
		t.Fatal("defaults.Digest is empty, want the digest of the config and route inputs")
	}
	firstDigest := defaults.Digest

	// Legacy behaviour: deriving the defaults binds no snapshot.
	if _, bound := state.SubagentSettings(); bound {
		t.Error("the runner build bound a child-settings snapshot; ordinary sessions must stay unbound")
	}

	// The digest follows the inputs: a different configured cap re-derives it.
	cfg.Agent.MaxConcurrentSubagents = 2
	other := session.New(cfg, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	if _, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, cfg, other, nil, 0, nil, "", nil, nil, nil, ""); err != nil {
		t.Fatalf("buildAgentRunner (changed cap): %v", err)
	}
	changed, ok := other.SubagentDefaults()
	if !ok {
		t.Fatal("SubagentDefaults() = false after the second runner build")
	}
	if changed.Settings.Concurrency != 2 {
		t.Errorf("changed defaults concurrency = %d, want 2", changed.Settings.Concurrency)
	}
	if changed.Digest == firstDigest {
		t.Error("digest did not change with the configured cap; it must fingerprint its inputs")
	}

	// An unset cap keeps the session's built-in default, derived by the same
	// normalization the session applies to a configured one.
	unset := nativeToolAgentConfig("subagent-defaults")
	unset.Agent.MaxConcurrentSubagents = 0
	zero := session.New(unset, t.TempDir(), time.Unix(100, 0), session.Persistence{})
	if _, _, _, _, _, _, _, _, _, _, _, _, err := buildAgentRunner(ctx, unset, zero, nil, 0, nil, "", nil, nil, nil, ""); err != nil {
		t.Fatalf("buildAgentRunner (unset cap): %v", err)
	}
	builtin, ok := zero.SubagentDefaults()
	if !ok {
		t.Fatal("SubagentDefaults() = false after the unset-cap runner build")
	}
	if !builtin.Settings.Enabled {
		t.Error("unset-cap defaults are disabled, want the ordinary enabled default")
	}
	if want := session.EffectiveSubagentConcurrency(0); builtin.Settings.Concurrency != want {
		t.Errorf("unset-cap concurrency = %d, want the built-in %d", builtin.Settings.Concurrency, want)
	}
}

// TestStartRuntimeBindsSubagentSettingsSnapshot pins that a snapshot supplied
// at creation reaches its own session only, and that an unusable snapshot is
// rejected before the runtime exists rather than silently clamped.
func TestStartRuntimeBindsSubagentSettingsSnapshot(t *testing.T) {
	rt := startSubagentSettingsRuntime(t, &session.SubagentSettings{Enabled: false, Model: "mock/big", Concurrency: 2})

	settings, ok := rt.State.SubagentSettings()
	if !ok {
		t.Fatal("the runtime's session reports no child settings, want the snapshot it was created with")
	}
	if settings.Enabled || settings.Model != "mock/big" || settings.Concurrency != 2 {
		t.Fatalf("bound settings = %+v, want the resolved snapshot", settings)
	}
	if cap := rt.State.SubagentMaxConcurrency(); cap != 2 {
		t.Fatalf("session cap = %d, want the snapshot's 2", cap)
	}
}

// TestStartRuntimeRejectsInvalidSubagentSettings pins that an explicit snapshot
// is validated, not clamped: a cap outside the supported range fails startup.
func TestStartRuntimeRejectsInvalidSubagentSettings(t *testing.T) {
	for _, cap := range []int{0, -1, 9} {
		_, err := StartRuntime(context.Background(),
			WithWorkingDir(t.TempDir()),
			WithHomeDir(t.TempDir()),
			WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}),
			WithConfigLoader(func(config.LoadOptions) (config.Config, error) {
				return nativeToolAgentConfig("subagent-settings"), nil
			}),
			WithSubagentSettings(session.SubagentSettings{Enabled: true, Concurrency: cap}),
		)
		if !errors.Is(err, ErrInvalidSubagentSettings) {
			t.Fatalf("StartRuntime with cap %d = %v, want ErrInvalidSubagentSettings", cap, err)
		}
	}
}

// TestSubagentSettingsSurviveReload pins that a config reload re-derives the
// ordinary defaults but leaves the session's own snapshot exactly as creation
// bound it.
func TestSubagentSettingsSurviveReload(t *testing.T) {
	rt := startSubagentSettingsRuntime(t, &session.SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 1})

	before, ok := rt.State.SubagentSettings()
	if !ok || before.Concurrency != 1 {
		t.Fatalf("bound settings before reload = %+v (ok=%v), want the snapshot", before, ok)
	}

	// Reload with a config whose configured cap differs: the ordinary defaults
	// follow it, the bound snapshot must not.
	next := nativeToolAgentConfig("subagent-settings")
	next.Agent.MaxConcurrentSubagents = 6
	if err := rt.ConfigReloader(next); err != nil {
		t.Fatalf("ConfigReloader: %v", err)
	}

	after, ok := rt.State.SubagentSettings()
	if !ok {
		t.Fatal("the session lost its snapshot across a reload")
	}
	if after != before {
		t.Fatalf("bound settings after reload = %+v, want the unchanged %+v", after, before)
	}
	if cap := rt.State.SubagentMaxConcurrency(); cap != 1 {
		t.Fatalf("session cap after reload = %d, want the snapshot's 1", cap)
	}
	defaults, ok := rt.State.SubagentDefaults()
	if !ok {
		t.Fatal("SubagentDefaults() = false after a reload")
	}
	if defaults.Settings.Concurrency != 6 {
		t.Fatalf("reported defaults concurrency = %d, want the reloaded 6", defaults.Settings.Concurrency)
	}
}

// TestTwoSessionsKeepTheirOwnSubagentSettings pins session ownership in one
// process: two sessions created with different snapshots enforce their own and
// neither sees the other's, and a session created with none stays ordinary.
func TestTwoSessionsKeepTheirOwnSubagentSettings(t *testing.T) {
	disabled := startSubagentSettingsRuntime(t, &session.SubagentSettings{Enabled: false, Concurrency: 2})
	worker := startSubagentSettingsRuntime(t, &session.SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 4})
	ordinary := startSubagentSettingsRuntime(t, nil)

	got, ok := disabled.State.SubagentSettings()
	if !ok || got.Enabled || got.Concurrency != 2 {
		t.Fatalf("first session settings = %+v (ok=%v), want the disabled snapshot", got, ok)
	}
	if cap := disabled.State.SubagentMaxConcurrency(); cap != 2 {
		t.Fatalf("first session cap = %d, want 2", cap)
	}

	got, ok = worker.State.SubagentSettings()
	if !ok || !got.Enabled || got.Model != "mock/big" || got.Concurrency != 4 {
		t.Fatalf("second session settings = %+v (ok=%v), want the worker snapshot", got, ok)
	}
	if cap := worker.State.SubagentMaxConcurrency(); cap != 4 {
		t.Fatalf("second session cap = %d, want 4", cap)
	}

	if _, ok := ordinary.State.SubagentSettings(); ok {
		t.Fatal("a session created without a snapshot reports one; ordinary behaviour must stay unbound")
	}
	if cap := ordinary.State.SubagentMaxConcurrency(); cap != config.Default().Agent.MaxConcurrentSubagents {
		t.Fatalf("ordinary session cap = %d, want the configured %d", cap, config.Default().Agent.MaxConcurrentSubagents)
	}

	// The first session's own settings are still exactly what it bound.
	if got, _ := disabled.State.SubagentSettings(); got.Concurrency != 2 || got.Enabled {
		t.Fatalf("first session settings after the second was created = %+v, want them unchanged", got)
	}
}
