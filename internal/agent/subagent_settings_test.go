package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/tools/registry"
)

// settingsState builds a parent that bound a child-settings snapshot at
// creation, the way a session created with a resolved snapshot is.
func settingsState(t *testing.T, settings session.SubagentSettings) *session.State {
	t.Helper()
	return session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{},
		session.WithSubagentSettings(settings))
}

// TestSubagentSettingsDisabledDelegation pins that a session whose snapshot
// disables delegation refuses agent.run with a clear tool error, without
// spending a concurrency slot and without building a child.
func TestSubagentSettingsDisabledDelegation(t *testing.T) {
	state := settingsState(t, session.SubagentSettings{Enabled: false, Concurrency: 3})
	factoryCalls := 0
	factory := func(_ SubagentRequest) (*Runner, *session.State, error) {
		factoryCalls++
		return &Runner{}, state, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)

	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("agent.run on a session with delegation disabled returned nil error")
	}
	if !strings.Contains(err.Error(), "delegation is disabled") {
		t.Fatalf("error = %v, want a delegation-disabled refusal", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory invocations = %d, want 0", factoryCalls)
	}
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency = %d, want 0 (the refusal must not take a slot)", got)
	}
}

// TestSubagentSettingsInvalidLimitsRefused pins that settings the session
// cannot honour are rejected before an approved worker starts: the handler
// validates the snapshot and refuses without admitting anything.
func TestSubagentSettingsInvalidLimitsRefused(t *testing.T) {
	// WithSubagentSettings applies the cap as given; the handler is what
	// refuses a snapshot outside 1..MaxSubagentConcurrency.
	state := settingsState(t, session.SubagentSettings{Enabled: true, Concurrency: 99})
	factoryCalls := 0
	factory := func(_ SubagentRequest) (*Runner, *session.State, error) {
		factoryCalls++
		return &Runner{}, state, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)

	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if err == nil {
		t.Fatal("agent.run under an out-of-range cap returned nil error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("error = %v, want an out-of-range refusal", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory invocations = %d, want 0", factoryCalls)
	}
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency = %d, want 0", got)
	}
}

// TestSubagentSettingsConcurrencyLimit pins that the snapshot's cap is the one
// enforced: the session keeps its configured concurrency, the snapshot
// replaces it, and a fourth dispatch under a cap of 1 is refused.
func TestSubagentSettingsConcurrencyLimit(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{},
		session.WithSubagentMaxConcurrency(4),
		session.WithSubagentSettings(session.SubagentSettings{Enabled: true, Concurrency: 1}))
	if got := state.SubagentMaxConcurrency(); got != 1 {
		t.Fatalf("effective cap = %d, want the snapshot's 1", got)
	}

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		if err := state.EnterSubagent(); err != nil {
			t.Errorf("EnterSubagent under the snapshot cap: %v", err)
			entered <- struct{}{}
			return
		}
		defer state.ExitSubagent()
		entered <- struct{}{}
		<-release
	}()
	<-entered

	factoryCalls := 0
	factory := func(_ SubagentRequest) (*Runner, *session.State, error) {
		factoryCalls++
		return &Runner{}, state, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)
	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y"}`)})
	if !errors.Is(err, session.ErrSubagentConcurrencyLimit) {
		t.Fatalf("error = %v, want session.ErrSubagentConcurrencyLimit", err)
	}
	if !strings.Contains(err.Error(), "(max 1)") {
		t.Fatalf("error = %q, want the snapshot's cap in the message", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory invocations = %d, want 0", factoryCalls)
	}

	close(release)
	<-exited
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency after release = %d, want 0", got)
	}
}

// TestSubagentSettingsModelRouting pins the two model cases a snapshot
// creates: an empty model keeps normal child routing (the request carries no
// model, exactly as with no snapshot at all), and a configured model reaches
// the child.
func TestSubagentSettingsModelRouting(t *testing.T) {
	cases := []struct {
		name     string
		settings session.SubagentSettings
		want     string
	}{
		{
			name:     "empty model keeps normal child routing",
			settings: session.SubagentSettings{Enabled: true, Concurrency: 2},
			want:     "",
		},
		{
			name:     "configured model reaches the child",
			settings: session.SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 2},
			want:     "mock/big",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := settingsState(t, tc.settings)
			var got SubagentRequest
			factory := func(req SubagentRequest) (*Runner, *session.State, error) {
				got = req
				return &Runner{}, state, nil
			}
			tool := NewSubagentTool(factory, nil, registry.New(), state,
				WithSubagentExec(func(_ context.Context, _ *Runner, _ string) (string, string, error) {
					return "done", "", nil
				}),
			)
			runAsyncSubagent(t, tool, state, `{"prompt":"x","description":"y"}`)
			if got.Model != tc.want {
				t.Fatalf("child model = %q, want %q", got.Model, tc.want)
			}
		})
	}
}

// TestSubagentSettingsPerCallModelWins pins that the existing per-call override
// still wins over the session's configured model, so a snapshot sets the
// default child model rather than taking the call's own choice away.
func TestSubagentSettingsPerCallModelWins(t *testing.T) {
	state := settingsState(t, session.SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 2})
	var got SubagentRequest
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		got = req
		return &Runner{}, state, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state,
		WithSubagentExec(func(_ context.Context, _ *Runner, _ string) (string, string, error) {
			return "done", "", nil
		}),
	)
	runAsyncSubagent(t, tool, state, `{"prompt":"x","description":"y","model":"mock/small"}`)
	if got.Model != "mock/small" {
		t.Fatalf("child model = %q, want the per-call override mock/small", got.Model)
	}
}

// TestSubagentSettingsPreserveAuthority pins that a snapshot widens nothing:
// the interaction ceiling and the system-access rule still decide what the
// child may do, so a session without system access cannot grant it through its
// child settings.
func TestSubagentSettingsPreserveAuthority(t *testing.T) {
	state := settingsState(t, session.SubagentSettings{Enabled: true, Model: "mock/big", Concurrency: 2})
	if state.SystemAccess() {
		t.Fatal("precondition: the parent must not have system access")
	}
	factoryCalls := 0
	factory := func(_ SubagentRequest) (*Runner, *session.State, error) {
		factoryCalls++
		return &Runner{}, state, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state)
	_, err := tool.Handler(t.Context(), registry.ToolCall{Args: []byte(`{"prompt":"x","description":"y","system":true}`)})
	if err == nil {
		t.Fatal("expected the system-access refusal to survive a child-settings snapshot")
	}
	if !strings.Contains(err.Error(), "system access requested but this session does not have it") {
		t.Fatalf("error = %v, want the system-access refusal", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("factory invocations = %d, want 0", factoryCalls)
	}
	if got := state.SubagentConcurrency(); got != 0 {
		t.Fatalf("concurrency = %d, want 0 (the refusal must not take a slot)", got)
	}
	if tool.Risk != registry.RiskWorkspaceWrite {
		t.Fatalf("agent.run risk = %v, want the unchanged RiskWorkspaceWrite", tool.Risk)
	}
}

// TestSubagentToolDescriptionStatesSnapshotCap pins that the description states
// the cap the session actually enforces, not the configured one a snapshot
// replaced.
func TestSubagentToolDescriptionStatesSnapshotCap(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{},
		session.WithSubagentMaxConcurrency(6),
		session.WithSubagentSettings(session.SubagentSettings{Enabled: true, Concurrency: 2}))
	tool := NewSubagentTool(func(_ SubagentRequest) (*Runner, *session.State, error) {
		return &Runner{}, nil, nil
	}, nil, registry.New(), state)
	if !strings.Contains(tool.Description, "Up to 2 run at once") {
		t.Fatalf("description does not state the snapshot's cap:\n%s", tool.Description)
	}
	if strings.Contains(tool.Description, "Up to 6 run at once") {
		t.Fatal("description states the configured cap a snapshot replaced")
	}
}

// TestSubagentSettingsUnboundIsOrdinaryBehaviour pins the legacy case: a
// session that bound no snapshot keeps Marshal's ordinary behaviour, including
// the model reaching the child as the call asked.
func TestSubagentSettingsUnboundIsOrdinaryBehaviour(t *testing.T) {
	state := session.New(config.Config{}, t.TempDir(), time.Now(), session.Persistence{},
		session.WithSubagentMaxConcurrency(5))
	if _, ok := state.SubagentSettings(); ok {
		t.Fatal("precondition: the state must have bound no snapshot")
	}
	var got SubagentRequest
	factory := func(req SubagentRequest) (*Runner, *session.State, error) {
		got = req
		return &Runner{}, state, nil
	}
	tool := NewSubagentTool(factory, nil, registry.New(), state,
		WithSubagentExec(func(_ context.Context, _ *Runner, _ string) (string, string, error) {
			return "done", "", nil
		}),
	)
	runAsyncSubagent(t, tool, state, `{"prompt":"x","description":"y"}`)
	if got.Model != "" {
		t.Fatalf("child model = %q, want normal child routing with no snapshot", got.Model)
	}
	if cap := state.SubagentMaxConcurrency(); cap != 5 {
		t.Fatalf("cap = %d, want the configured 5", cap)
	}
}
