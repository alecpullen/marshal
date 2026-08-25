package acp

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"marshal/internal/app"
	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/commands"
	"marshal/internal/llm/routing"
	"marshal/internal/pipeline"
	"marshal/internal/pubsub"
)

// frameID returns the wire-level "id" value for f as a string, or "" if
// the frame has no id. JSON numbers decode as float64, so we accept both.
func frameID(f map[string]any) string {
	v, ok := f["id"]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}

// frameMethod returns the "method" field of f (or "" for responses).
func frameMethod(f map[string]any) string {
	m, _ := f["method"].(string)
	return m
}

// frameIsResponse returns true if f has an id and no method (a response
// frame rather than a request or notification).
func frameIsResponse(f map[string]any) bool {
	return frameMethod(f) == "" && f["id"] != nil
}

// frameResult returns f["result"] if present.
func frameResult(f map[string]any) any {
	return f["result"]
}

// frameError returns f["error"] if present.
func frameError(f map[string]any) any {
	return f["error"]
}

// readResponse waits for a frame whose id matches wantID. Response
// frames (those with a non-empty id) are cached by id inside the
// harness so subsequent calls do not block waiting for the channel
// to deliver them again. Notifications (no id) are read and dropped
// because they are not addressable.
func readResponse(t *testing.T, h *wireHarness, wantID string) map[string]any {
	t.Helper()
	for {
		if f, ok := h.responses[wantID]; ok {
			delete(h.responses, wantID)
			return f
		}
		select {
		case f := <-h.frames:
			if frameID(f) == wantID {
				return f
			}
			if frameID(f) != "" {
				h.responses[frameID(f)] = f
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("readResponse(%s): timed out (cached=%v)", wantID, cachedIDs(h.responses))
		}
	}
}

func cachedIDs(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- Test 1: permission deadlock regression ---

// TestACPWirePermissionResponseDuringPrompt wires a real TurnManager
// and real PermissionBridge into a Server, sends session/prompt, reads
// the outbound session/request_permission, sends an approval response,
// and verifies both that the runner receives the approval and that the
// original prompt response is end_turn. This is the regression for the
// case where the synchronous reader deadlocks because it can't read the
// permission response while the handler runs.
func TestACPWirePermissionResponseDuringPrompt(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()

	runnerReceived := make(chan session.UserApprovalDecision, 1)
	pendingCh := make(chan session.UserApprovalDecision, 1)
	pending := &session.PendingToolCall{
		ID:           "tool-1",
		Name:         "shell.run",
		Command:      "ls",
		ResponseChan: pendingCh,
	}
	runFn := RunnerFunc(func(ctx context.Context, prompt string) error {
		// Publish the pending approval via the real broker so the
		// TurnManager's forwarding loop drives the bridge.
		broker.Publish(session.EventPendingApprovalChanged, session.Event{PendingApproval: pending})
		select {
		case dec := <-pendingCh:
			runnerReceived <- dec
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	h := newWireHarness(t, func(srv *Server) {
		turns := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{
					SessionID: sessionID,
					BeginWork: identityBeginWork,
					Run:       runFn,
					Events:    broker,
				}, true
			},
			Notify: srv.Notify,
			Perms:  &serverPermissionClient{server: srv},
		})
		srv.Handle("session/prompt", turns.PromptTurn)
	})
	defer h.close(t)

	// 1. Send session/prompt.
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_perm",
			"prompt":    []map[string]any{{"type": "text", "text": "list"}},
		},
	})

	// 2. Read outbound session/request_permission and capture its id.
	permID := ""
	for {
		f := h.next(t)
		if frameMethod(f) == "session/request_permission" {
			permID = frameID(f)
			break
		}
		// skip notifications; the runner doesn't publish any here, but be defensive.
	}
	if permID == "" {
		t.Fatal("did not see session/request_permission frame on the wire")
	}

	// 3. Send the approval response.
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": permID,
		"result": map[string]any{"approved": true},
	})

	// 4. Runner must receive the approval.
	select {
	case dec := <-runnerReceived:
		if !dec.Approved {
			t.Fatalf("runner received decision with Approved=false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not receive the approval decision")
	}

	// 5. The original prompt response must be end_turn.
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("prompt response error: %+v", errObj)
	}
	res, _ := frameResult(resp).(map[string]any)
	if res["stopReason"] != "end_turn" {
		t.Fatalf("prompt stopReason = %v, want end_turn", res["stopReason"])
	}
}

// --- Test 2: in-flight cancellation regression ---

// TestACPWireCancelDuringPrompt starts a prompt whose runner blocks on
// the context, sends session/cancel (a notification, no id), and
// requires that no cancel response frame appears and that the original
// prompt response carries stopReason: cancelled once the runner
// completes.
func TestACPWireCancelDuringPrompt(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	runnerStarted := make(chan struct{}, 1)
	cleanupDone := make(chan struct{})

	runFn := RunnerFunc(func(ctx context.Context, prompt string) error {
		runnerStarted <- struct{}{}
		<-ctx.Done()
		// "cleanup marker" — after this the runner has finished its
		// in-flight work; the turn manager will now drain the broker
		// and return a result. Close cleanupDone to let the test
		// observe ordering.
		close(cleanupDone)
		return ctx.Err()
	})

	h := newWireHarness(t, func(srv *Server) {
		turns := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{
					SessionID: sessionID,
					BeginWork: identityBeginWork,
					Run:       runFn,
					Events:    broker,
				}, true
			},
			Notify: srv.Notify,
		})
		srv.Handle("session/prompt", turns.PromptTurn)
		srv.Handle("session/cancel", turns.Cancel)
	})
	defer h.close(t)

	// 1. Send prompt.
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_cancel",
			"prompt":    []map[string]any{{"type": "text", "text": "go"}},
		},
	})

	// Wait for the runner to start.
	select {
	case <-runnerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}

	// 2. Send session/cancel (notification, no id).
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "method": "session/cancel",
		"params": map[string]any{"sessionId": "sess_cancel"},
	})

	// 3. Wait for the runner's cleanup marker so we know any frame
	// seen after this point is the prompt's terminal response.
	<-cleanupDone

	// 4. The next frame with id "1" must be the prompt response. It
	// must NOT be a cancel response (session/cancel is a notification
	// and never produces a response), and it must carry
	// stopReason: cancelled.
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("prompt response error: %+v", errObj)
	}
	// No cancel response frame should have been produced. session/cancel
	// is a notification (no id), so it must never yield a response; any
	// response frame still cached here would indicate a stray cancel
	// response that would mask the prompt's terminal frame.
	if n := len(h.responses); n != 0 {
		t.Fatalf("unexpected cached response frames after prompt response: %v", cachedIDs(h.responses))
	}
	res, _ := frameResult(resp).(map[string]any)
	if res["stopReason"] != "cancelled" {
		t.Fatalf("prompt stopReason = %v, want cancelled", res["stopReason"])
	}
}

// --- Test 3: concurrent prompts for different sessions ---

// TestACPWireDifferentSessionsPromptConcurrently runs two prompts on
// different sessions. Both runners must start before either is
// released; the responses must correlate to their request ids.
func TestACPWireDifferentSessionsPromptConcurrently(t *testing.T) {
	brokerA := pubsub.NewBroker[session.Event]()
	brokerB := pubsub.NewBroker[session.Event]()
	var startedCount atomic.Int32
	releaseGate := make(chan struct{})

	makeRun := func(broker *pubsub.Broker[session.Event], started chan<- struct{}) RunnerFunc {
		return RunnerFunc(func(ctx context.Context, prompt string) error {
			startedCount.Add(1)
			started <- struct{}{}
			<-releaseGate
			broker.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
				Role:    session.RoleAssistant,
				Content: "done-" + prompt,
			}})
			return nil
		})
	}

	startedA := make(chan struct{}, 1)
	startedB := make(chan struct{}, 1)

	h := newWireHarness(t, func(srv *Server) {
		turns := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				switch sessionID {
				case "sess_a":
					return &TurnRuntime{SessionID: sessionID, BeginWork: identityBeginWork, Run: makeRun(brokerA, startedA), Events: brokerA}, true
				case "sess_b":
					return &TurnRuntime{SessionID: sessionID, BeginWork: identityBeginWork, Run: makeRun(brokerB, startedB), Events: brokerB}, true
				}
				return nil, false
			},
			Notify: srv.Notify,
		})
		srv.Handle("session/prompt", turns.PromptTurn)
	})
	defer h.close(t)

	// Send both prompts.
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_a",
			"prompt":    []map[string]any{{"type": "text", "text": "a"}},
		},
	})
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_b",
			"prompt":    []map[string]any{{"type": "text", "text": "b"}},
		},
	})

	// Both runners must start.
	<-startedA
	<-startedB
	if n := startedCount.Load(); n != 2 {
		t.Fatalf("startedCount = %d, want 2", n)
	}

	// Release both.
	close(releaseGate)

	// Read both responses; they must carry the original ids.
	respA := readResponse(t, h, "1")
	respB := readResponse(t, h, "2")
	for _, r := range []map[string]any{respA, respB} {
		if errObj := frameError(r); errObj != nil {
			t.Fatalf("prompt error: %+v", errObj)
		}
		res, _ := frameResult(r).(map[string]any)
		if res["stopReason"] != "end_turn" {
			t.Fatalf("stopReason = %v, want end_turn", res["stopReason"])
		}
	}
}

// --- Test 4: duplicate prompt rejected with -32000 ---

// TestACPWireDuplicatePromptRejectedWithoutCancellingFirst sends a
// blocking prompt then a second prompt for the same session. The
// second must be rejected with -32000; the first must remain running
// and return end_turn once the runner finishes.
func TestACPWireDuplicatePromptRejectedWithoutCancellingFirst(t *testing.T) {
	broker := pubsub.NewBroker[session.Event]()
	runnerStarted := make(chan struct{}, 1)
	blocker := make(chan struct{})

	runFn := RunnerFunc(func(ctx context.Context, prompt string) error {
		runnerStarted <- struct{}{}
		<-blocker
		return nil
	})

	h := newWireHarness(t, func(srv *Server) {
		turns := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{
					SessionID: sessionID,
					BeginWork: identityBeginWork,
					Run:       runFn,
					Events:    broker,
				}, true
			},
			Notify: srv.Notify,
		})
		srv.Handle("session/prompt", turns.PromptTurn)
	})
	defer h.close(t)

	// First prompt (id "1"): blocks on the runner.
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_dup",
			"prompt":    []map[string]any{{"type": "text", "text": "first"}},
		},
	})

	// Wait deterministically for the first runner to be in-flight
	// before sending the duplicate. This guarantees the active turn
	// slot is registered and the duplicate is rejected deterministically
	// without a fixed sleep.
	select {
	case <-runnerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first runner did not start")
	}

	// Second prompt (id "2") for the same session: must be rejected.
	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_dup",
			"prompt":    []map[string]any{{"type": "text", "text": "second"}},
		},
	})

	resp2 := readResponse(t, h, "2")
	errObj, _ := frameError(resp2).(map[string]any)
	if errObj == nil {
		t.Fatalf("duplicate prompt response missing error: %+v", resp2)
	}
	if code, _ := errObj["code"].(float64); int(code) != -32000 {
		t.Fatalf("duplicate prompt code = %v, want -32000 (err=%+v)", code, errObj)
	}

	// First prompt must still be running; release it and verify end_turn.
	close(blocker)
	resp1 := readResponse(t, h, "1")
	if errObj := frameError(resp1); errObj != nil {
		t.Fatalf("first prompt error: %+v", errObj)
	}
	res, _ := frameResult(resp1).(map[string]any)
	if res["stopReason"] != "end_turn" {
		t.Fatalf("first stopReason = %v, want end_turn", res["stopReason"])
	}
}

// --- Test 5: load replays before null result ---

// TestACPWireLoadReplaysBeforeNullResult sends session/load against a
// manager backed by a hydrated state. All preceding frames (those
// emitted during replay) must use method "session/update" with kinds
// matching the underlying role; the final response must have the
// original id and a present null result; and no frame may use a
// method that starts with session/message_, session/activity_,
// session/audit_, or session/pending_.
func TestACPWireLoadReplaysBeforeNullResult(t *testing.T) {
	// Hydrate a state with one message per role.
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	state.AddMessage(session.RoleUser, "user-1", session.ContentTypePlain)
	state.AddMessage(session.RoleAssistant, "assistant-1", session.ContentTypePlain)
	state.AddMessage(session.RoleSystem, "system-1", session.ContentTypePlain)

	rt := &app.Runtime{SessionID: "sess_replay_wire", State: state}
	starter := func(ctx context.Context, opts ...app.Option) (*app.Runtime, error) {
		return rt, nil
	}
	closer := func(ctx context.Context, r *app.Runtime) error { return nil }

	tmp := t.TempDir()
	absCwd, _ := filepath.Abs(tmp)
	loadParams := map[string]any{
		"cwd":        absCwd,
		"sessionId":  "sess_replay_wire",
		"mcpServers": []any{},
	}

	h := newWireHarness(t, func(srv *Server) {
		manager := NewSessionManager(SessionManagerConfig{
			StartRuntime: starter,
			CloseRuntime: closer,
			Notify:       srv.Notify,
		})
		manager.SetTurnCanceller(func(ctx context.Context, id string) error { return nil })
		srv.Handle("session/load", manager.Load)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(7), "method": "session/load",
		"params": loadParams,
	})

	// Replay emits 3 session/update notifications before the final
	// response. Read them in order, validating each, then read the
	// final response.
	var roleKinds []string
	for i := 0; i < 3; i++ {
		f := h.next(t)
		method := frameMethod(f)
		if method != "session/update" {
			if shouldFailLoadMethod(method) {
				t.Fatalf("forbidden load-replay method %q (frame=%v)", method, f)
			}
			t.Fatalf("replay frame %d method = %q, want session/update", i, method)
		}
		params, _ := f["params"].(map[string]any)
		if upd, ok := params["update"].(map[string]any); ok {
			roleKinds = append(roleKinds, kindForRole(upd))
		}
	}
	final := readResponse(t, h, "7")
	if errObj := frameError(final); errObj != nil {
		t.Fatalf("load response error: %+v", errObj)
	}
	// The result key must be present and explicitly null. A missing
	// "result" key would not satisfy ACP v1; a present-but-non-null
	// value would also be wrong.
	if _, present := final["result"]; !present {
		t.Fatalf("final load response missing 'result' key: %+v", final)
	}
	if frameResult(final) != nil {
		t.Fatalf("final load response result = %v, want null", frameResult(final))
	}

	// Drain any remaining frames on the wire briefly and fail if any
	// use a forbidden load-replay method. readResponse silently drops
	// notifications, so a stray frame emitted between the replay
	// notifications and the final response would otherwise be invisible
	// to the assertions above.
draining:
	for {
		select {
		case f := <-h.frames:
			if method := frameMethod(f); shouldFailLoadMethod(method) {
				t.Fatalf("forbidden load-replay method %q (frame=%v)", method, f)
			}
		case <-time.After(200 * time.Millisecond):
			break draining
		}
	}

	// All three messages must have been replayed with the right kinds.
	wantKinds := []string{"user_message_chunk", "agent_message_chunk", "agent_message_chunk"}
	if len(roleKinds) != len(wantKinds) {
		t.Fatalf("replayed kinds = %v, want %v", roleKinds, wantKinds)
	}
	for i, k := range wantKinds {
		if roleKinds[i] != k {
			t.Fatalf("replay kind[%d] = %q, want %q", i, roleKinds[i], k)
		}
	}
}

// shouldFailLoadMethod returns true for load-replay method names that
// must never appear on the wire.
func shouldFailLoadMethod(method string) bool {
	switch {
	case len(method) < len("session/"):
		return false
	case method == "session/update":
		return false
	}
	prefixes := []string{"session/message_", "session/activity_", "session/audit_", "session/pending_"}
	for _, p := range prefixes {
		if len(method) >= len(p) && method[:len(p)] == p {
			return true
		}
	}
	return false
}

// kindForRole mirrors messageUpdate in turn.go. We re-derive it here
// rather than exporting that helper to keep the turn.go API stable.
func kindForRole(u map[string]any) string {
	return u["kind"].(string)
}

// --- Test 6: concurrent frames remain valid JSON ---

// TestACPWireConcurrentFramesRemainValidJSON launches 20 different
// session handlers that notify (via session/update) and complete in
// varied order while five outbound requests receive responses. The
// output decoder must read only valid single-line JSON and every
// expected id must appear exactly once.
func TestACPWireConcurrentFramesRemainValidJSON(t *testing.T) {
	const sessions = 20
	const outbound = 5

	brokers := make([]*pubsub.Broker[session.Event], sessions)
	for i := range brokers {
		brokers[i] = pubsub.NewBroker[session.Event]()
	}

	// releaseOrder shuffles the order in which the 20 runners are
	// allowed to publish their single notification. The 20 sessions
	// race against each other; a single shared gate feeds one token
	// at a time, so the wire ordering is varied.
	releaseOrder := randPerm(sessions)
	publishGate := make(chan int, sessions)
	for _, idx := range releaseOrder {
		publishGate <- idx
	}

	var srvRef *Server
	h := newWireHarness(t, func(srv *Server) {
		srvRef = srv
		turns := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				idx, err := strconv.Atoi(sessionID[len("sess_"):])
				if err != nil || idx < 0 || idx >= sessions {
					return nil, false
				}
				b := brokers[idx]
				idxCapture := idx
				return &TurnRuntime{
					SessionID: sessionID,
					BeginWork: identityBeginWork,
					Run: RunnerFunc(func(ctx context.Context, prompt string) error {
						<-publishGate
						b.Publish(session.EventMessageAdded, session.Event{Message: &session.Message{
							Role:    session.RoleAssistant,
							Content: fmt.Sprintf("s%d", idxCapture),
						}})
						return nil
					}),
					Events: b,
				}, true
			},
			Notify: srv.Notify,
		})
		srv.Handle("session/prompt", turns.PromptTurn)
	})
	defer h.close(t)

	// Fire five background goroutines that issue outbound requests
	// against the harness's server. The test reads the outbound
	// request ids from the wire and feeds matching responses below.
	requestDone := make(chan struct{}, outbound)
	for i := 0; i < outbound; i++ {
		go func() {
			defer func() { requestDone <- struct{}{} }()
			var got map[string]any
			_ = srvRef.Request(context.Background(), "session/echo", map[string]any{"ping": true}, &got)
		}()
	}

	// Send all 20 prompts in a burst. Each carries id (i+1) and a
	// unique session id.
	for i := 0; i < sessions; i++ {
		h.send(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      float64(i + 1),
			"method":  "session/prompt",
			"params": map[string]any{
				"sessionId": fmt.Sprintf("sess_%d", i),
				"prompt":    []map[string]any{{"type": "text", "text": fmt.Sprintf("p%d", i)}},
			},
		})
	}

	// Read frames until we have seen 20 prompt responses, 20
	// session/update notifications, and 5 outbound request frames.
	seenIDs := map[string]int{}
	seenOutbound := map[string]int{}
	seenNotifs := 0
	totalExpected := sessions + sessions + outbound
	deadline := time.Now().Add(3 * time.Second)
	for seen := 0; seen < totalExpected; seen++ {
		select {
		case f := <-h.frames:
			method := frameMethod(f)
			switch {
			case method == "session/update":
				seenNotifs++
			case method == "session/echo":
				id := frameID(f)
				if id == "" {
					t.Fatalf("session/echo frame missing id: %v", f)
				}
				seenOutbound[id]++
				// Feed a response so the goroutine can complete.
				h.send(t, map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result":  map[string]any{"ok": true},
				})
			case frameIsResponse(f):
				seenIDs[frameID(f)]++
			default:
				t.Fatalf("unexpected frame method %q (frame=%v)", method, f)
			}
		case <-time.After(time.Until(deadline)):
			t.Fatalf("timed out after seeing %d/%d frames (notifs=%d promptIds=%d outboundIds=%d)",
				seen, totalExpected, seenNotifs, len(seenIDs), len(seenOutbound))
		}
	}

	if seenNotifs != sessions {
		t.Fatalf("seen %d session/update notifications, want %d", seenNotifs, sessions)
	}
	for i := 1; i <= sessions; i++ {
		if seenIDs[strconv.Itoa(i)] != 1 {
			t.Fatalf("prompt response id %d seen %d times, want 1", i, seenIDs[strconv.Itoa(i)])
		}
	}
	if len(seenOutbound) != outbound {
		t.Fatalf("seen %d unique outbound ids, want %d (%v)", len(seenOutbound), outbound, seenOutbound)
	}
	for id, n := range seenOutbound {
		if n != 1 {
			t.Fatalf("outbound id %s seen %d times, want 1", id, n)
		}
	}

	// Drain the 5 outbound goroutines so none of them leak past the
	// test. They all return nil because we feed a matching response
	// for each.
	for i := 0; i < outbound; i++ {
		select {
		case <-requestDone:
		case <-time.After(2 * time.Second):
			t.Fatal("outbound request goroutine did not return")
		}
	}
}

// --- Test 7: command dispatch wire-level integration ---

// TestACPWireCommandDispatchListAndRun wires a CommandManager with a
// real commands.Registry into a Server and exercises session/command_list
// and session/command over the actual JSON encode/decode wire transport.
func TestACPWireCommandDispatchListAndRun(t *testing.T) {
	reg := commands.New()
	if err := reg.Register(commands.Command{
		Name: "diff",
		Handler: func(state *session.State, args []string) commands.Result {
			return commands.Text("no changes")
		},
	}); err != nil {
		t.Fatalf("register diff: %v", err)
	}
	if err := reg.Register(commands.Command{
		Name:    "settings",
		TUIOnly: true,
	}); err != nil {
		t.Fatalf("register settings: %v", err)
	}

	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) { return nil, false },
		Notify: func(method string, params any) error { return nil },
	})
	cmds := NewCommandManager(CommandManagerConfig{
		Lookup: func(sessionID string) (*CommandRuntime, bool) {
			if sessionID != "sess_cmd" {
				return nil, false
			}
			return &CommandRuntime{State: &session.State{}, Registry: reg}, true
		},
		HasActive: turns.HasActiveTurn,
	})

	h := newWireHarness(t, func(srv *Server) {
		srv.Handle("session/command", cmds.Command)
		srv.Handle("session/command_list", cmds.CommandList)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/command_list",
		"params": map[string]any{"sessionId": "sess_cmd"},
	})
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/command_list error: %+v", errObj)
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/command",
		"params": map[string]any{"sessionId": "sess_cmd", "name": "diff"},
	})
	resp = readResponse(t, h, "2")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/command(diff) error: %+v", errObj)
	}
	res, _ := frameResult(resp).(map[string]any)
	if res["text"] != "no changes" {
		t.Fatalf("session/command(diff) text = %v, want %q", res["text"], "no changes")
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(3), "method": "session/command",
		"params": map[string]any{"sessionId": "sess_cmd", "name": "settings"},
	})
	resp = readResponse(t, h, "3")
	if frameError(resp) == nil {
		t.Fatal("session/command(settings): got no error, want an error (TUI-only)")
	}
}

// --- Test 8: SDD start/gate/answer wire-level integration ---

// TestACPWireSDDStartGateAndAnswer wires a TurnManager with a
// PipelineFactory that returns a fakeAgentRunner, registers
// session/sdd_start and session/sdd_answer, and exercises the
// full gate-and-answer flow over the wire.
func TestACPWireSDDStartGateAndAnswer(t *testing.T) {
	state := &session.State{}
	state.SetSDDGate(session.SDDGate{}) // ensure a clean start; SDDStart populates it via the fake runner below

	callCount := 0
	factory := func(planPath string, overrides map[routing.AgentRole]string) AgentRunner {
		return &fakeAgentRunner{
			run: func(ctx context.Context, goal string) error {
				callCount++
				if callCount == 1 {
					state.SetSDDGate(session.SDDGate{TaskN: 1, Question: "pick one"})
					return pipeline.ErrHumanGateRequired
				}
				state.ClearSDDGate()
				return nil
			},
		}
	}

	turns := NewTurnManager(TurnManagerConfig{
		Lookup: func(sessionID string) (*TurnRuntime, bool) {
			return &TurnRuntime{
				SessionID:       sessionID,
				BeginWork:       identityBeginWork,
				Events:          pubsub.NewBroker[session.Event](),
				State:           state,
				PipelineFactory: factory,
			}, true
		},
		Notify: func(method string, params any) error { return nil },
	})

	h := newWireHarness(t, func(srv *Server) {
		srv.Handle("session/sdd_start", turns.SDDStart)
		srv.Handle("session/sdd_answer", turns.SDDAnswer)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/sdd_start",
		"params": map[string]any{"sessionId": "sess_gate", "planPath": "docs/plan.md"},
	})
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/sdd_start error: %+v", errObj)
	}
	res, _ := frameResult(resp).(map[string]any)
	if res["stopReason"] != "gate" {
		t.Fatalf("session/sdd_start stopReason = %v, want %q", res["stopReason"], "gate")
	}
	gate, _ := res["gate"].(map[string]any)
	if gate == nil || gate["question"] != "pick one" {
		t.Fatalf("session/sdd_start gate = %v, want question %q", gate, "pick one")
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/sdd_answer",
		"params": map[string]any{"sessionId": "sess_gate", "answer": "option b"},
	})
	resp = readResponse(t, h, "2")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/sdd_answer error: %+v", errObj)
	}
	res, _ = frameResult(resp).(map[string]any)
	if res["stopReason"] != "end_turn" {
		t.Fatalf("session/sdd_answer stopReason = %v, want %q", res["stopReason"], "end_turn")
	}
}

// --- Test 9: session_telemetry wire-level integration ---

// TestACPWirePromptTurnIncludesSessionTelemetry sends a session/prompt
// over the real JSON transport and confirms a session_telemetry
// session/update notification arrives before the prompt's terminal
// response.
func TestACPWirePromptTurnIncludesSessionTelemetry(t *testing.T) {
	state := session.New(config.Default(), t.TempDir(), time.Now(), session.Persistence{})
	broker := pubsub.NewBroker[session.Event]()
	runFn := RunnerFunc(func(ctx context.Context, prompt string) error {
		state.AddMessageFinal(session.RoleAssistant, "done", session.ContentTypePlain)
		return nil
	})

	h := newWireHarness(t, func(srv *Server) {
		turns := NewTurnManager(TurnManagerConfig{
			Lookup: func(sessionID string) (*TurnRuntime, bool) {
				return &TurnRuntime{
					SessionID: sessionID, BeginWork: identityBeginWork,
					Run: runFn, Events: broker, State: state,
				}, true
			},
			Notify: srv.Notify,
		})
		srv.Handle("session/prompt", turns.PromptTurn)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/prompt",
		"params": map[string]any{
			"sessionId": "sess_telemetry",
			"prompt":    []map[string]any{{"type": "text", "text": "go"}},
		},
	})

	foundTelemetry := false
	for !foundTelemetry {
		f := h.next(t)
		if frameMethod(f) != "session/update" {
			continue
		}
		params, _ := f["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["kind"] == "session_telemetry" {
			foundTelemetry = true
			if _, ok := update["sessionFooter"]; !ok {
				t.Fatal("session_telemetry update missing sessionFooter field")
			}
		}
		if frameID(f) == "1" {
			// Reached the prompt's own terminal response without seeing
			// telemetry first — fail with a clear message rather than
			// looping forever.
			t.Fatal("saw the session/prompt response before any session_telemetry update")
		}
	}
}

// --- Test 10: memory list/delete wire-level integration ---

// TestACPWireMemoryListAndDelete wires a MemoryManager with a real
// in-memory DB into a Server and exercises session/memory_list and
// session/memory_delete over the actual JSON encode/decode wire transport.
func TestACPWireMemoryListAndDelete(t *testing.T) {
	d, projectID := newMemoryTestDB(t)
	if err := d.SaveMemory(projectID, "fact", "wire test entry", "sess_a", time.Now()); err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}

	mem := NewMemoryManager(MemoryManagerConfig{
		Lookup: func(sessionID string) (*MemoryRuntime, bool) {
			return &MemoryRuntime{DB: d, ProjectID: projectID}, true
		},
	})

	h := newWireHarness(t, func(srv *Server) {
		srv.Handle("session/memory_list", mem.MemoryList)
		srv.Handle("session/memory_delete", mem.MemoryDelete)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/memory_list",
		"params": map[string]any{"sessionId": "sess_mem"},
	})
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/memory_list error: %+v", errObj)
	}
	res, _ := frameResult(resp).(map[string]any)
	entries, _ := res["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("session/memory_list entries = %v, want 1", res["entries"])
	}
	entry, _ := entries[0].(map[string]any)
	idFloat, _ := entry["id"].(float64)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/memory_delete",
		"params": map[string]any{"sessionId": "sess_mem", "id": idFloat},
	})
	resp = readResponse(t, h, "2")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/memory_delete error: %+v", errObj)
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(3), "method": "session/memory_list",
		"params": map[string]any{"sessionId": "sess_mem"},
	})
	resp = readResponse(t, h, "3")
	res, _ = frameResult(resp).(map[string]any)
	entries, _ = res["entries"].([]any)
	if len(entries) != 0 {
		t.Fatalf("session/memory_list after delete = %v, want empty", res["entries"])
	}
}

// --- Test 11: skills install preview/confirm/list wire-level integration ---

// TestACPWireSkillsInstallPreviewConfirmAndList wires a SkillsManager
// with a Lookup that returns a SkillsRuntime backed by temp dirs, then
// exercises session/skills_install_preview, session/skills_install_confirm,
// and session/skills_list over the actual JSON wire transport.
func TestACPWireSkillsInstallPreviewConfirmAndList(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	srcDir := t.TempDir()
	source := writeTestSkillFile(t, srcDir, "wire-skill")

	skillsMgr := NewSkillsManager(SkillsManagerConfig{
		Lookup: func(sessionID string) (*SkillsRuntime, bool) {
			return &SkillsRuntime{HomeDir: home, WorkingDir: work}, true
		},
	})

	h := newWireHarness(t, func(srv *Server) {
		srv.Handle("session/skills_install_preview", skillsMgr.SkillsInstallPreview)
		srv.Handle("session/skills_install_confirm", skillsMgr.SkillsInstallConfirm)
		srv.Handle("session/skills_list", skillsMgr.SkillsList)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/skills_install_preview",
		"params": map[string]any{"sessionId": "sess_wire", "source": source},
	})
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/skills_install_preview error: %+v", errObj)
	}
	res, _ := frameResult(resp).(map[string]any)
	token, _ := res["stagingToken"].(string)
	if token == "" {
		t.Fatalf("session/skills_install_preview result = %v, missing stagingToken", res)
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/skills_install_confirm",
		"params": map[string]any{"sessionId": "sess_wire", "stagingToken": token, "scope": "global"},
	})
	resp = readResponse(t, h, "2")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/skills_install_confirm error: %+v", errObj)
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(3), "method": "session/skills_list",
		"params": map[string]any{"sessionId": "sess_wire"},
	})
	resp = readResponse(t, h, "3")
	res, _ = frameResult(resp).(map[string]any)
	list, _ := res["skills"].([]any)
	if len(list) != 1 {
		t.Fatalf("session/skills_list after confirm = %v, want one entry", res["skills"])
	}
}

// randPerm returns a pseudo-random permutation of [0,n). The seed is
// derived from time.Now so the order varies across test runs, but the
// function is deterministic given a fixed wall clock.
func randPerm(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	s := uint64(time.Now().UnixNano())
	for i := n - 1; i > 0; i-- {
		s = s*6364136223846793005 + 1442695040888963407
		j := int(s % uint64(i+1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// --- Test 12: plugins install scan/confirm/list wire-level integration ---

// TestACPWirePluginsInstallScanConfirmAndList wires a PluginsManager
// with a Lookup that returns a PluginsRuntime backed by temp dirs, then
// exercises session/plugins_install_scan, session/plugins_install_confirm,
// and session/plugins_list over the actual JSON wire transport.
func TestACPWirePluginsInstallScanConfirmAndList(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	initPluginRepo(t, src, map[string]string{"commands/hello.md": testCommandFile})
	home := t.TempDir()
	work := t.TempDir()

	pluginsMgr := NewPluginsManager(PluginsManagerConfig{
		Lookup: func(sessionID string) (*PluginsRuntime, bool) {
			return &PluginsRuntime{HomeDir: home, WorkingDir: work, Trusted: true}, true
		},
	})

	h := newWireHarness(t, func(srv *Server) {
		srv.Handle("session/plugins_install_scan", pluginsMgr.PluginsInstallScan)
		srv.Handle("session/plugins_install_confirm", pluginsMgr.PluginsInstallConfirm)
		srv.Handle("session/plugins_list", pluginsMgr.PluginsList)
	})
	defer h.close(t)

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(1), "method": "session/plugins_install_scan",
		"params": map[string]any{"sessionId": "sess_wire", "source": src},
	})
	resp := readResponse(t, h, "1")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/plugins_install_scan error: %+v", errObj)
	}
	res, _ := frameResult(resp).(map[string]any)
	token, _ := res["scanToken"].(string)
	if token == "" {
		t.Fatalf("session/plugins_install_scan result = %v, missing scanToken", res)
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(2), "method": "session/plugins_install_confirm",
		"params": map[string]any{"sessionId": "sess_wire", "scanToken": token, "scope": "global"},
	})
	resp = readResponse(t, h, "2")
	if errObj := frameError(resp); errObj != nil {
		t.Fatalf("session/plugins_install_confirm error: %+v", errObj)
	}

	h.send(t, map[string]any{
		"jsonrpc": "2.0", "id": float64(3), "method": "session/plugins_list",
		"params": map[string]any{"sessionId": "sess_wire"},
	})
	resp = readResponse(t, h, "3")
	res, _ = frameResult(resp).(map[string]any)
	list, _ := res["plugins"].([]any)
	if len(list) != 1 {
		t.Fatalf("session/plugins_list after confirm = %v, want one entry", res["plugins"])
	}
}
