package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"marshal/internal/acpwire"
	"marshal/internal/app/session"
)

// TestSessionNewBindsSubagentSettingsToThatSession pins the creation-time
// snapshot: session/new carries a resolved snapshot, and only the session it
// created enforces it. Two sessions created with different snapshots keep
// their own, and a session created without one stays on the ordinary
// defaults — which is what a legacy client sends.
func TestSessionNewBindsSubagentSettingsToThatSession(t *testing.T) {
	m, work := newRoutingSessionManager(t)

	disabledID, err := newRoutedSession(t, m, work,
		`,"subagentSettings":{"enabled":false,"concurrency":2}`)
	if err != nil {
		t.Fatalf("session/new (disabled snapshot): %v", err)
	}
	workerID, err := newRoutedSession(t, m, work,
		`,"subagentSettings":{"enabled":true,"model":"mock/big","concurrency":4}`)
	if err != nil {
		t.Fatalf("session/new (worker snapshot): %v", err)
	}
	// Legacy: no subagentSettings at all keeps Marshal's normal behaviour.
	plainID, err := newRoutedSession(t, m, work, "")
	if err != nil {
		t.Fatalf("session/new (no snapshot): %v", err)
	}

	disabledRT, ok := m.Get(disabledID)
	if !ok || disabledRT.State == nil {
		t.Fatalf("session %q is not registered", disabledID)
	}
	got, ok := disabledRT.State.SubagentSettings()
	if !ok {
		t.Fatalf("session %q bound no snapshot", disabledID)
	}
	if got.Enabled || got.Concurrency != 2 {
		t.Fatalf("disabled session settings = %+v, want enabled=false concurrency=2", got)
	}
	if cap := disabledRT.State.SubagentMaxConcurrency(); cap != 2 {
		t.Fatalf("disabled session cap = %d, want 2", cap)
	}

	workerRT, ok := m.Get(workerID)
	if !ok || workerRT.State == nil {
		t.Fatalf("session %q is not registered", workerID)
	}
	got, ok = workerRT.State.SubagentSettings()
	if !ok {
		t.Fatalf("session %q bound no snapshot", workerID)
	}
	if !got.Enabled || got.Model != "mock/big" || got.Concurrency != 4 {
		t.Fatalf("worker session settings = %+v, want the resolved worker snapshot", got)
	}
	if cap := workerRT.State.SubagentMaxConcurrency(); cap != 4 {
		t.Fatalf("worker session cap = %d, want 4", cap)
	}

	plainRT, ok := m.Get(plainID)
	if !ok || plainRT.State == nil {
		t.Fatalf("session %q is not registered", plainID)
	}
	if _, ok := plainRT.State.SubagentSettings(); ok {
		t.Fatalf("a session created without subagentSettings bound a snapshot")
	}

	// Session ownership: the first session's settings are exactly what it
	// bound, untouched by the sessions created after it.
	if again, _ := disabledRT.State.SubagentSettings(); again.Enabled || again.Concurrency != 2 {
		t.Fatalf("first session settings = %+v, want the disabled snapshot it bound", again)
	}
}

// TestSessionNewRejectsInvalidSubagentSettings pins that an unusable snapshot
// fails with invalid params before any runtime starts, rather than being
// clamped or accepted and then ignored.
func TestSessionNewRejectsInvalidSubagentSettings(t *testing.T) {
	m, work := newRoutingSessionManager(t)

	for _, concurrency := range []int{0, -1, 9} {
		params := `{"cwd":` + jsonString(work) + `,"mcpServers":[],"subagentSettings":` +
			`{"enabled":true,"concurrency":` + jsonInt(concurrency) + `}}`
		res, err := m.Create(context.Background(), json.RawMessage(params))
		if err == nil {
			t.Fatalf("session/new with concurrency %d returned %v, want invalid params", concurrency, res)
		}
		var rpcErr *jsonRPCError
		if !errors.As(err, &rpcErr) {
			t.Fatalf("error is not a jsonRPCError: %v", err)
		}
		if rpcErr.Code != invalidParams {
			t.Fatalf("code = %d, want invalidParams (%v)", rpcErr.Code, err)
		}
	}
}

// TestSessionStatusReportsSubagentDefaultsAndSettingsCapability pins what a
// client reads before it decides anything: the settings capability bit is set,
// and the session reports the ordinary defaults with a digest of their inputs.
func TestSessionStatusReportsSubagentDefaultsAndSettingsCapability(t *testing.T) {
	m, work := newRoutingSessionManager(t)
	id, err := newRoutedSession(t, m, work, "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	sess, ok := m.Service(id)
	if !ok {
		t.Fatalf("no service for session %q", id)
	}
	status := sess.Status()

	if status.SubagentControls == nil {
		t.Fatal("Status().SubagentControls = nil, want the advertised capabilities")
	}
	if !status.SubagentControls.Settings {
		t.Error("Status().SubagentControls.Settings = false, want true: the session accepts and enforces a snapshot")
	}

	defaults := status.SubagentDefaults
	if defaults == nil {
		t.Fatal("Status().SubagentDefaults = nil, want the ordinary child defaults")
	}
	rt, _ := m.Get(id)
	want := session.EffectiveSubagentConcurrency(rt.State.Config.Agent.MaxConcurrentSubagents)
	if !defaults.Settings.Enabled {
		t.Error("defaults.Settings.Enabled = false, want true (ordinary delegation is offered)")
	}
	if defaults.Settings.Model != "" {
		t.Errorf("defaults.Settings.Model = %q, want empty (normal child routing)", defaults.Settings.Model)
	}
	if defaults.Settings.Concurrency != want {
		t.Errorf("defaults.Settings.Concurrency = %d, want %d", defaults.Settings.Concurrency, want)
	}
	if defaults.Digest == "" {
		t.Error("defaults.Digest is empty, want the digest of the config and route inputs")
	}
}

// TestSessionSettingsWireNames pins the creation parameter's JSON shape and
// that its omission stays absent, so a legacy client is unaffected.
func TestSessionSettingsWireNames(t *testing.T) {
	raw, err := json.Marshal(acpwire.SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 2})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"enabled":true,"model":"mock/big","concurrency":2}`
	if string(raw) != want {
		t.Fatalf("SubagentSettings JSON = %s, want %s", raw, want)
	}

	// An empty model is omitted rather than sent as "", so "normal child
	// routing" is the absence of the field.
	raw, err = json.Marshal(acpwire.SubagentSettings{Enabled: true, Concurrency: 3})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if want := `{"enabled":true,"concurrency":3}`; string(raw) != want {
		t.Fatalf("SubagentSettings JSON = %s, want %s", raw, want)
	}

	// A session/new params blob without the field stays valid and decodes to
	// nil, which is what the omitted-settings path is.
	var p sessionParams
	if err := json.Unmarshal([]byte(`{"cwd":"/tmp","mcpServers":[]}`), &p); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if p.SubagentSettings != nil {
		t.Fatalf("omitted subagentSettings decoded to %+v, want nil", p.SubagentSettings)
	}
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
