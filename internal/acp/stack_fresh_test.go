package acp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"marshal/internal/acp/service"
	"marshal/internal/acpclient/rpc"
	"marshal/internal/acpwire"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
)

// freshPipeSession opens a state on the harness's pipe arm and returns the
// client's own session: the one the TUI will hold from plan 8 on.
func freshPipeSession(t *testing.T, st *session.State) *rpc.Session {
	t.Helper()
	_, sess := openPipeArmsTB(t, service.Deps{State: st})
	rs, ok := sess.(*rpc.Session)
	if !ok {
		t.Fatalf("the pipe arm's session = %T, want *rpc.Session", sess)
	}
	return rs
}

// stackCarries reports whether the snapshot's encoding carries text.
func stackCarries(snap acpwire.StackSnapshot, text string) bool {
	encoded, err := json.Marshal(snap)
	if err != nil {
		return false
	}
	return strings.Contains(string(encoded), text)
}

// awaitStackCarries polls reads until one carries text, and fails at the
// deadline: a read cannot block on a change the agent never flushed.
func awaitStackCarries(t *testing.T, rs *rpc.Session, busy bool, text string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if stackCarries(rs.Stack(0, busy), text) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no read carried %q within %v", text, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitStackDoesNotCarry polls reads for within and fails if one carries text.
func awaitStackDoesNotCarry(t *testing.T, rs *rpc.Session, busy bool, text string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if stackCarries(rs.Stack(0, busy), text) {
			t.Fatalf("a read carried %q, wanted no flush within %v", text, within)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startQuietCall starts one running shell call and waits until the projection
// carries it, so the silent appends below are the only thing left to flush.
func startQuietCall(t *testing.T, rs *rpc.Session, st *session.State, busy bool) {
	t.Helper()
	st.SetActiveToolCall(session.ActiveToolCall{
		ToolCallID: "c1",
		Name:       "shell.run",
		Args:       `{"command":"make test"}`,
	})
	awaitStackCarries(t, rs, busy, "make test", 2*time.Second)
}

// TestABusyClientSeesARunningCallsOutput pins R4 for what decision 1 does not
// cover: a running call's streaming output, which publishes nothing, reaches a
// client whose busy flag is set because the tick re-projects every time.
func TestABusyClientSeesARunningCallsOutput(t *testing.T) {
	st := seamState(t, "s1")
	rs := freshPipeSession(t, st)

	// The busy flag is what makes the tick flush unconditionally.
	rs.Stack(0, true)
	startQuietCall(t, rs, st, true)

	st.AppendActiveToolCallOutput("c1", "hello")
	awaitStackCarries(t, rs, true, "hello", 500*time.Millisecond)
}

// TestARunningSubagentKeepsTheMainStackFlushing pins the other half of
// flushesEveryTick: with the busy flag clear, a registered subagent still
// running re-projects the main stack every tick.
func TestARunningSubagentKeepsTheMainStackFlushing(t *testing.T) {
	st := seamState(t, "s1")
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: "child"})
	st.RegisterSubagent("scout", child)

	rs := freshPipeSession(t, st)
	rs.Stack(0, false)
	startQuietCall(t, rs, st, false)

	st.AppendActiveToolCallOutput("c1", "hello")
	awaitStackCarries(t, rs, false, "hello", 500*time.Millisecond)
}

// TestAnIdleClientFlushesOnlyWhatIsDirty pins that the every-tick flush is for
// a busy client alone: idle, the silent append waits for an event.
func TestAnIdleClientFlushesOnlyWhatIsDirty(t *testing.T) {
	st := seamState(t, "s1")
	rs := freshPipeSession(t, st)
	rs.Stack(0, false)
	startQuietCall(t, rs, st, false)

	st.AppendActiveToolCallOutput("c1", "quiet")
	awaitStackDoesNotCarry(t, rs, false, "quiet", 300*time.Millisecond)

	// A publishing call is what makes it show.
	st.SetActivity(session.Activity{Kind: session.ActivityIdle, Label: "idle"})
	awaitStackCarries(t, rs, false, "quiet", 500*time.Millisecond)
}

// TestSyncDeliversASilentChange pins session/sync: its response follows every
// update the state calls for, so the very next read shows the change.
func TestSyncDeliversASilentChange(t *testing.T) {
	st := seamState(t, "s1")
	rs := freshPipeSession(t, st)
	rs.Stack(0, false)
	startQuietCall(t, rs, st, false)

	st.AppendActiveToolCallOutput("c1", "quiet")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rs.Sync(ctx); err != nil {
		t.Fatalf("Sync = %v, want nil", err)
	}
	if snap := rs.Stack(0, false); !stackCarries(snap, "quiet") {
		t.Fatal("the read after Sync did not carry the silent change")
	}
}

// TestSyncRefusesBadParams pins the two rejections: no session ID is invalid
// params, an unknown session is the unknown-session error.
func TestSyncRefusesBadParams(t *testing.T) {
	m := NewTurnManager(TurnManagerConfig{
		Lookup: func(string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(string, any) error { return nil },
	})
	for _, tc := range []struct {
		name   string
		params string
		code   int
	}{
		{name: "no session id", params: `{}`, code: invalidParams},
		{name: "unknown session", params: `{"sessionId":"nope"}`, code: serverError},
	} {
		_, err := m.Sync(context.Background(), json.RawMessage(tc.params))
		rpcErr, ok := err.(*jsonRPCError)
		if !ok {
			t.Errorf("%s: Sync error = %v, want an RPC error", tc.name, err)
			continue
		}
		if rpcErr.Code != tc.code {
			t.Errorf("%s: Sync code = %d, want %d", tc.name, rpcErr.Code, tc.code)
		}
	}
}

// TestOnlyAMarshalClientFlushesEveryTick pins V6: a client without the
// capability gets no new cadence, whatever it set and however many subagents
// run.
func TestOnlyAMarshalClientFlushesEveryTick(t *testing.T) {
	child := session.New(config.Default(), t.TempDir(), time.Unix(100, 0), session.Persistence{SessionID: "child"})
	withSubagent := seamState(t, "s1")
	withSubagent.RegisterSubagent("scout", child)
	quiet := seamState(t, "s2")

	newManager := func(marshal bool) *TurnManager {
		return NewTurnManager(TurnManagerConfig{
			Lookup:  func(string) (*TurnRuntime, bool) { return nil, false },
			Notify:  func(string, any) error { return nil },
			Marshal: func() bool { return marshal },
		})
	}

	plain := newManager(false)
	plain.setClientBusy("s1", true)
	plain.setClientBusy("s2", true)
	if plain.flushesEveryTick("s1", withSubagent) {
		t.Error("a client without the capability flushed every tick with a running subagent")
	}
	if plain.flushesEveryTick("s2", quiet) {
		t.Error("a client without the capability flushed every tick with its busy flag set")
	}

	marshaler := newManager(true)
	if !marshaler.flushesEveryTick("s1", withSubagent) {
		t.Error("a marshal client with a running subagent must flush every tick")
	}
	if marshaler.flushesEveryTick("s2", quiet) {
		t.Error("a marshal client that is idle and has no subagent flushed every tick")
	}
	marshaler.setClientBusy("s2", true)
	if !marshaler.flushesEveryTick("s2", quiet) {
		t.Error("a busy marshal client must flush every tick")
	}
}
