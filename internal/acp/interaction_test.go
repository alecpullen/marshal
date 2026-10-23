package acp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"marshal/internal/interaction"
)

// fakeInteraction is a TurnRuntime interaction seam backed by the canonical
// interaction.Apply, so a test exercises the same commit rules production uses
// without building a whole session. It records how many times the updater ran,
// which is how the atomicity assertions are made.
type fakeInteraction struct {
	mu    sync.Mutex
	state interaction.State
	calls int
	err   error
}

func newFakeInteraction(start interaction.State) *fakeInteraction {
	if start.Revision == 0 {
		start.Revision = 1
	}
	return &fakeInteraction{state: start}
}

// set is the SetInteraction seam: one validated patch, one commit.
func (f *fakeInteraction) set(p interaction.Patch) (interaction.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.state, f.err
	}
	next, err := interaction.Apply(f.state, p)
	if err != nil {
		return f.state, err
	}
	if next != f.state {
		next.Revision = f.state.Revision + 1
	}
	f.state = next
	return next, nil
}

// read is the InteractionState seam.
func (f *fakeInteraction) read() (interaction.State, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, true
}

func (f *fakeInteraction) snapshot() interaction.State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeInteraction) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// interactionManager builds a TurnManager whose single session commits patches
// through fake and records every session/update it broadcasts. The returned
// func reads the recorded updates.
func interactionManager(t *testing.T, fake *fakeInteraction) (*TurnManager, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var updates []map[string]any
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:        sessionID,
				SetInteraction:   fake.set,
				InteractionState: fake.read,
			}, true
		},
		Notify: func(method string, params any) error {
			mu.Lock()
			defer mu.Unlock()
			if p, ok := params.(SessionUpdateParams); ok {
				updates = append(updates, p.Update)
			}
			return nil
		},
	})
	return manager, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), updates...)
	}
}

// TestSetModeAtomicPatchTransitionTable pins the ingress translation and the
// single-commit semantics: every accepted request produces exactly one
// SetInteraction call and the committed tuple the table names.
func TestSetModeAtomicPatchTransitionTable(t *testing.T) {
	cases := []struct {
		name  string
		start interaction.State
		body  string
		want  InteractionState
	}{
		{
			name:  "legacy plan selects ask",
			start: interaction.State{Mode: interaction.ModeAct, Revision: 1},
			body:  `{"sessionId":"s","mode":"plan"}`,
			want:  InteractionState{Mode: "ask", Revision: 2},
		},
		{
			name:  "legacy default selects ask",
			start: interaction.State{Mode: interaction.ModeAct, Revision: 1},
			body:  `{"sessionId":"s","mode":"default"}`,
			want:  InteractionState{Mode: "ask", Revision: 2},
		},
		{
			name:  "legacy edit selects act",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","mode":"edit"}`,
			want:  InteractionState{Mode: "act", Revision: 2},
		},
		{
			name:  "legacy copilot selects act",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","mode":"copilot"}`,
			want:  InteractionState{Mode: "act", Revision: 2},
		},
		{
			name:  "legacy auto derives auto",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","mode":"auto"}`,
			want:  InteractionState{Mode: "act", Auto: true, Revision: 2},
		},
		{
			name:  "explicit auto overrides derived auto",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","mode":"auto","auto":false}`,
			want:  InteractionState{Mode: "act", Revision: 2},
		},
		{
			name:  "explicit auto enables auto on act",
			start: interaction.State{Mode: interaction.ModeAct, Revision: 1},
			body:  `{"sessionId":"s","mode":"act","auto":true}`,
			want:  InteractionState{Mode: "act", Auto: true, Revision: 2},
		},
		{
			name:  "canonical ask clears auto",
			start: interaction.State{Mode: interaction.ModeAct, Auto: true, Revision: 1},
			body:  `{"sessionId":"s","mode":"ask"}`,
			want:  InteractionState{Mode: "ask", Revision: 2},
		},
		{
			name:  "modifier only grants system",
			start: interaction.State{Mode: interaction.ModeAct, Revision: 1},
			body:  `{"sessionId":"s","system_access":true}`,
			want:  InteractionState{Mode: "act", System: true, Revision: 2},
		},
		{
			name:  "modifier only revokes system",
			start: interaction.State{Mode: interaction.ModeAct, System: true, Revision: 1},
			body:  `{"sessionId":"s","system_access":false}`,
			want:  InteractionState{Mode: "act", Revision: 2},
		},
		{
			name:  "modifier only leaves mode alone",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","system_access":true}`,
			want:  InteractionState{Mode: "ask", System: true, Revision: 2},
		},
		{
			name:  "mode and system in one patch",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","mode":"act","system_access":true}`,
			want:  InteractionState{Mode: "act", System: true, Revision: 2},
		},
		{
			name:  "case and whitespace normalize",
			start: interaction.State{Mode: interaction.ModeAsk, Revision: 1},
			body:  `{"sessionId":"s","mode":"  CoPiLoT "}`,
			want:  InteractionState{Mode: "act", Revision: 2},
		},
		{
			name:  "no-op patch keeps the revision",
			start: interaction.State{Mode: interaction.ModeAct, Revision: 1},
			body:  `{"sessionId":"s","auto":false}`,
			want:  InteractionState{Mode: "act", Revision: 1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeInteraction(c.start)
			manager, _ := interactionManager(t, fake)
			got, err := manager.SetMode(context.Background(), json.RawMessage(c.body))
			if err != nil {
				t.Fatalf("SetMode(%s) error = %v", c.body, err)
			}
			if fake.callCount() != 1 {
				t.Fatalf("SetInteraction calls = %d, want exactly 1", fake.callCount())
			}
			result, ok := got.(InteractionState)
			if !ok {
				t.Fatalf("result = %#v, want an InteractionState", got)
			}
			if result != c.want {
				t.Fatalf("result = %+v, want %+v", result, c.want)
			}
			if committed := interactionStateFrom(fake.snapshot()); committed != c.want {
				t.Fatalf("committed state = %+v, want %+v", committed, c.want)
			}
		})
	}
}

// TestSetModeRejectsInvalidPatchWithoutMutation pins that every malformed
// request is refused before the runtime is consulted: no updater call, no
// state change, no broadcast.
func TestSetModeRejectsInvalidPatchWithoutMutation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "empty patch", body: `{"sessionId":"s"}`, want: "empty patch"},
		{name: "unknown mode", body: `{"sessionId":"s","mode":"yolo"}`, want: "invalid mode"},
		{name: "empty mode", body: `{"sessionId":"s","mode":""}`, want: "invalid mode"},
		{name: "canonical ask with auto", body: `{"sessionId":"s","mode":"ask","auto":true}`, want: "Ask cannot enable Auto"},
		{name: "legacy plan with auto", body: `{"sessionId":"s","mode":"plan","auto":true}`, want: "Ask cannot enable Auto"},
		{name: "null mode", body: `{"sessionId":"s","mode":null}`, want: "must not be null"},
		{name: "null auto", body: `{"sessionId":"s","auto":null}`, want: "must not be null"},
		{name: "null system", body: `{"sessionId":"s","system_access":null}`, want: "must not be null"},
		{name: "wrong type mode", body: `{"sessionId":"s","mode":5}`, want: "must be a string"},
		{name: "wrong type auto", body: `{"sessionId":"s","auto":"yes"}`, want: "must be a boolean"},
		{name: "wrong type system", body: `{"sessionId":"s","system_access":1}`, want: "must be a boolean"},
		{name: "unknown field", body: `{"sessionId":"s","mode":"act","bogus":true}`, want: "unknown field"},
		{name: "params not an object", body: `[]`, want: "params must be an object"},
		{name: "missing session", body: `{"mode":"act"}`, want: "requires sessionId"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := interaction.State{Mode: interaction.ModeAct, Revision: 3}
			fake := newFakeInteraction(start)
			manager, updates := interactionManager(t, fake)
			_, err := manager.SetMode(context.Background(), json.RawMessage(c.body))
			if err == nil {
				t.Fatalf("SetMode(%s) = nil error, want a rejection", c.body)
			}
			if !contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			if code := codeFor(err); code != invalidParams {
				t.Fatalf("error code = %d, want invalidParams (%d)", code, invalidParams)
			}
			if fake.callCount() != 0 {
				t.Fatalf("SetInteraction calls = %d, want 0 for a rejected patch", fake.callCount())
			}
			if got := fake.snapshot(); got != start {
				t.Fatalf("state = %+v, want it untouched at %+v", got, start)
			}
			if got := updates(); len(got) != 0 {
				t.Fatalf("broadcasts = %#v, want none for a rejected patch", got)
			}
		})
	}
}

// TestSetModeReturnsAndBroadcastsSameFullState pins that the result and the
// mode_changed broadcast carry the identical committed tuple, field for field.
func TestSetModeReturnsAndBroadcastsSameFullState(t *testing.T) {
	fake := newFakeInteraction(interaction.State{Mode: interaction.ModeAsk, Revision: 1})
	manager, updates := interactionManager(t, fake)

	got, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"s","mode":"auto","system_access":true}`))
	if err != nil {
		t.Fatalf("SetMode: %v", err)
	}
	result, ok := got.(InteractionState)
	if !ok {
		t.Fatalf("result = %#v, want an InteractionState", got)
	}
	want := InteractionState{Mode: "act", Auto: true, System: true, Revision: 2}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}

	all := updates()
	if len(all) != 1 {
		t.Fatalf("broadcasts = %#v, want exactly one", all)
	}
	update := all[0]
	if update["kind"] != "mode_changed" {
		t.Fatalf("update kind = %v, want mode_changed", update["kind"])
	}
	broadcast := InteractionState{
		Mode:     update["mode"].(string),
		Auto:     update["auto"].(bool),
		System:   update["system_access"].(bool),
		Revision: update["access_revision"].(uint64),
	}
	if broadcast != result {
		t.Fatalf("broadcast = %+v, want the same state as the result %+v", broadcast, result)
	}

	// The two JSON projections carry the same fields and values, so a client
	// cannot observe a difference between the reply and the event.
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var resultFields map[string]any
	if err := json.Unmarshal(resultJSON, &resultFields); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	broadcastFields := map[string]any{
		"mode":            update["mode"],
		"auto":            update["auto"],
		"system_access":   update["system_access"],
		"access_revision": update["access_revision"],
	}
	broadcastJSON, err := json.Marshal(broadcastFields)
	if err != nil {
		t.Fatalf("marshal broadcast: %v", err)
	}
	var normalizedBroadcast map[string]any
	if err := json.Unmarshal(broadcastJSON, &normalizedBroadcast); err != nil {
		t.Fatalf("unmarshal broadcast: %v", err)
	}
	if !reflect.DeepEqual(resultFields, normalizedBroadcast) {
		t.Fatalf("result fields %#v != broadcast fields %#v", resultFields, normalizedBroadcast)
	}
}

// TestSetModeLegacyIngressCanonicalReports pins that a retired mode spelling is
// translated once at ingress and never appears on the wire: both the result and
// the broadcast report the canonical pair.
func TestSetModeLegacyIngressCanonicalReports(t *testing.T) {
	cases := []struct {
		legacy string
		mode   string
		auto   bool
	}{
		{legacy: "plan", mode: "ask"},
		{legacy: "default", mode: "ask"},
		{legacy: "edit", mode: "act"},
		{legacy: "copilot", mode: "act"},
		{legacy: "auto", mode: "act", auto: true},
	}
	for _, c := range cases {
		t.Run(c.legacy, func(t *testing.T) {
			fake := newFakeInteraction(interaction.State{Mode: interaction.ModeAsk, Revision: 1})
			manager, updates := interactionManager(t, fake)
			got, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"s","mode":"`+c.legacy+`"}`))
			if err != nil {
				t.Fatalf("SetMode(%q): %v", c.legacy, err)
			}
			result := got.(InteractionState)
			if result.Mode != c.mode || result.Auto != c.auto {
				t.Fatalf("result = %+v, want mode %q auto %v", result, c.mode, c.auto)
			}
			all := updates()
			if len(all) != 1 {
				t.Fatalf("broadcasts = %#v, want exactly one", all)
			}
			if all[0]["mode"] != c.mode || all[0]["auto"] != c.auto {
				t.Fatalf("broadcast = %#v, want mode %q auto %v", all[0], c.mode, c.auto)
			}
			if all[0]["mode"] == c.legacy {
				t.Fatalf("broadcast leaked the legacy spelling %q", c.legacy)
			}
		})
	}
}

// TestAccessStateReportsAuthoritativeSnapshot pins the read-only handler: it
// reports the runtime's committed tuple, mutates nothing, and fails closed when
// the runtime cannot report one.
func TestAccessStateReportsAuthoritativeSnapshot(t *testing.T) {
	t.Run("reports the committed tuple", func(t *testing.T) {
		committed := interaction.State{Mode: interaction.ModeAct, Auto: true, System: true, Revision: 7}
		fake := newFakeInteraction(committed)
		manager, updates := interactionManager(t, fake)

		got, err := manager.AccessState(context.Background(), json.RawMessage(`{"sessionId":"s"}`))
		if err != nil {
			t.Fatalf("AccessState: %v", err)
		}
		want := InteractionState{Mode: "act", Auto: true, System: true, Revision: 7}
		if got != want {
			t.Fatalf("AccessState = %#v, want %+v", got, want)
		}
		// A read is not a mutation and announces nothing.
		if fake.callCount() != 0 {
			t.Fatalf("SetInteraction calls = %d, want 0 for a read", fake.callCount())
		}
		if fake.snapshot() != committed {
			t.Fatalf("state = %+v, want it untouched at %+v", fake.snapshot(), committed)
		}
		if all := updates(); len(all) != 0 {
			t.Fatalf("broadcasts = %#v, want none for a read", all)
		}
	})

	t.Run("unknown session fails closed", func(t *testing.T) {
		manager := NewTurnManager(TurnManagerConfig{
			Lookup: func(string) (*TurnRuntime, bool) { return nil, false },
			Notify: func(string, any) error { return nil },
		})
		_, err := manager.AccessState(context.Background(), json.RawMessage(`{"sessionId":"nope"}`))
		if err == nil || !contains(err.Error(), "unknown session") {
			t.Fatalf("err = %v, want unknown session", err)
		}
	})

	t.Run("runtime without a reader fails closed", func(t *testing.T) {
		manager := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{SessionID: sessionID}, true
			},
			Notify: func(string, any) error { return nil },
		})
		_, err := manager.AccessState(context.Background(), json.RawMessage(`{"sessionId":"s"}`))
		if err == nil || !contains(err.Error(), "does not report interaction state") {
			t.Fatalf("err = %v, want a fail-closed server error", err)
		}
	})

	t.Run("unavailable read fails closed", func(t *testing.T) {
		manager := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{
					SessionID:        sessionID,
					InteractionState: func() (interaction.State, bool) { return interaction.State{}, false },
				}, true
			},
			Notify: func(string, any) error { return nil },
		})
		_, err := manager.AccessState(context.Background(), json.RawMessage(`{"sessionId":"s"}`))
		if err == nil || !contains(err.Error(), "does not report interaction state") {
			t.Fatalf("err = %v, want a fail-closed server error", err)
		}
	})
}

// TestSetModeUnavailableRuntimeFailsClosed pins that a runtime without the
// interaction seams refuses the request instead of silently accepting it.
func TestSetModeUnavailableRuntimeFailsClosed(t *testing.T) {
	manager := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{SessionID: sessionID}, true
		},
		Notify: func(string, any) error { return nil },
	})
	_, err := manager.SetMode(context.Background(), json.RawMessage(`{"sessionId":"s","mode":"act"}`))
	if err == nil || !contains(err.Error(), "does not support interaction changes") {
		t.Fatalf("err = %v, want a fail-closed server error", err)
	}
	if code := codeFor(err); code != serverError {
		t.Fatalf("error code = %d, want serverError (%d)", code, serverError)
	}
}

// contains is strings.Contains, kept local so this file's imports stay minimal.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
