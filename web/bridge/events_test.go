package bridge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAttachDoesNotDrainOnUnsubscribe(t *testing.T) {
	l := NewEventLog()
	child := &Child{}
	reg := NewRegistry(child)
	Attach(l, child, reg)
	reg.track("s1", AgentPath("/home/u/repo"))
	ch := make(chan Decision, 1)
	reg.permMu.Lock()
	reg.permissions["tc1"] = ch
	reg.permSession["tc1"] = "s1"
	reg.permMu.Unlock()
	_, unsub := l.Subscribe("s1")
	unsub()
	select {
	case d := <-ch:
		t.Fatalf("pending permission was drained (approved=%v)", d.Approved)
	default:
	}
	if got := reg.Pending("s1"); got != "approval" {
		t.Fatalf("Pending after unsubscribe = %q", got)
	}
	if err := reg.ResolvePermission("tc1", Decision{Approved: true}); err != nil {
		t.Fatalf("ResolvePermission: %v", err)
	}
	select {
	case d := <-ch:
		if !d.Approved {
			t.Error("resolved decision should be approved")
		}
	case <-time.After(time.Second):
		t.Fatal("resolve did not reach waiter")
	}
}

func TestEventLogAppendAndReplay(t *testing.T) {
	l := NewEventLog()
	for i := 1; i <= 3; i++ {
		id, err := l.Append("s1", map[string]any{"n": i})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if id != int64(i) {
			t.Fatalf("append %d returned id %d", i, id)
		}
	}

	evs, overflow := l.Replay("s1", 1)
	if overflow {
		t.Fatal("unexpected overflow")
	}
	if len(evs) != 2 || evs[0].ID != 2 || evs[1].ID != 3 {
		t.Fatalf("replay after 1 = %+v, want events 2,3", evs)
	}
	var decoded struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(evs[1].Data, &decoded); err != nil || decoded.N != 3 {
		t.Fatalf("event 3 data = %s, decoded %+v, err %v", evs[1].Data, decoded, err)
	}

	if evs, overflow := l.Replay("s1", 3); len(evs) != 0 || overflow {
		t.Fatalf("replay after newest = %v events, overflow %v", len(evs), overflow)
	}
	if evs, overflow := l.Replay("s1", 0); len(evs) != 0 || overflow {
		t.Fatalf("replay after 0 (fresh connect) = %v events, overflow %v", len(evs), overflow)
	}
	// A stale position against a session with no retained history is
	// overflow: the client must refetch state rather than sit stuck.
	if evs, overflow := l.Replay("unknown", 1); len(evs) != 0 || !overflow {
		t.Fatalf("replay unknown session = %v events, overflow %v, want overflow", len(evs), overflow)
	}
}

func TestEventLogRingWraparound(t *testing.T) {
	l := NewEventLog()
	const total = ringCap + 20
	for i := 1; i <= total; i++ {
		if _, err := l.Append("s1", map[string]any{"n": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Replay from inside the retained window: exactly the tail.
	evs, overflow := l.Replay("s1", total-10)
	if overflow {
		t.Fatal("unexpected overflow inside retained window")
	}
	if len(evs) != 10 || evs[0].ID != int64(total-9) || evs[9].ID != int64(total) {
		t.Fatalf("window replay = %d events [%d..%d], want 10 [%d..%d]",
			len(evs), evs[0].ID, evs[len(evs)-1].ID, total-9, total)
	}

	// Replay from an evicted position: overflow flagged, retained tail returned.
	evs, overflow = l.Replay("s1", 5)
	if !overflow {
		t.Fatal("want overflow for evicted position")
	}
	if len(evs) != ringCap || evs[0].ID != 21 || evs[ringCap-1].ID != int64(total) {
		t.Fatalf("overflow replay = %d events [%d..%d], want %d [21..%d]",
			len(evs), evs[0].ID, evs[len(evs)-1].ID, ringCap, total)
	}
}

func TestEventLogFanOut(t *testing.T) {
	l := NewEventLog()
	const n = 3
	chans := make([]<-chan Event, 0, n)
	unsubs := make([]func(), 0, n)
	for i := 0; i < n; i++ {
		ch, unsub := l.Subscribe("s1")
		chans = append(chans, ch)
		unsubs = append(unsubs, unsub)
	}
	defer func() {
		for _, u := range unsubs {
			u()
		}
	}()

	if _, err := l.Append("s1", json.RawMessage(`{"k":"v"}`)); err != nil {
		t.Fatalf("append: %v", err)
	}
	for i, ch := range chans {
		select {
		case ev := <-ch:
			if ev.ID != 1 || ev.SessionID != "s1" || string(ev.Data) != `{"k":"v"}` {
				t.Fatalf("subscriber %d got %+v", i, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d did not receive event", i)
		}
	}

	// Other sessions' subscribers are not notified.
	other, otherUnsub := l.Subscribe("s2")
	defer otherUnsub()
	if _, err := l.Append("s1", map[string]any{"n": 2}); err != nil {
		t.Fatalf("append: %v", err)
	}
	select {
	case ev := <-other:
		t.Fatalf("s2 subscriber received s1 event %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	// Unsubscribe closes the channel and stops delivery. The channel
	// may still hold buffered events, which must be drained before the
	// close is observable.
	unsubs[0]()
	for range chans[0] {
	}
	unsubs[0]() // idempotent
	if _, err := l.Append("s1", map[string]any{"n": 3}); err != nil {
		t.Fatalf("append after unsub: %v", err)
	}
}

func TestEventLogEmptySessionBroadcasts(t *testing.T) {
	l := NewEventLog()
	ch1, unsub1 := l.Subscribe("s1")
	defer unsub1()
	ch2, unsub2 := l.Subscribe("s2")
	defer unsub2()

	if _, err := l.Append("", map[string]any{"type": "permission_request"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.SessionID != "" {
				t.Fatalf("subscriber %d got session %q, want broadcast", i, ev.SessionID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber %d did not receive broadcast", i)
		}
	}
}

func TestEventLogOnUnsubscribeLastSubscriber(t *testing.T) {
	l := NewEventLog()
	var fired []string
	l.OnUnsubscribe = func(sessionID string) { fired = append(fired, sessionID) }

	_, unsub1 := l.Subscribe("s1")
	_, unsub2 := l.Subscribe("s1")
	_, unsubOther := l.Subscribe("s2")

	// Disconnecting one of two subscribers for s1 must not fire.
	unsub1()
	if len(fired) != 0 {
		t.Fatalf("OnUnsubscribe fired on non-last subscriber: %v", fired)
	}
	// Disconnecting the other session's only subscriber fires for s2.
	unsubOther()
	if len(fired) != 1 || fired[0] != "s2" {
		t.Fatalf("OnUnsubscribe = %v, want [s2]", fired)
	}
	// Disconnecting the last s1 subscriber fires for s1.
	unsub2()
	if len(fired) != 2 || fired[1] != "s1" {
		t.Fatalf("OnUnsubscribe = %v, want [s2 s1]", fired)
	}
	// Idempotent unsub does not fire again.
	unsub2()
	if len(fired) != 2 {
		t.Fatalf("idempotent unsub fired again: %v", fired)
	}
}

func TestEventLogSlowSubscriber(t *testing.T) {
	l := NewEventLog()
	ch, unsub := l.Subscribe("s1")
	defer unsub()

	// Fill the subscriber buffer without draining; appends must not block.
	for i := 0; i < subBuf+10; i++ {
		if _, err := l.Append("s1", map[string]any{"n": i}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Drain the buffer: it holds the first subBuf events. The
	// subscriber is marked lagged; the nudge is attempted on the next
	// Append, so post one more event.
	for i := 0; i < subBuf; i++ {
		ev := <-ch
		if ev.ID != int64(i+1) {
			t.Fatalf("drained event %d, want id %d", ev.ID, i+1)
		}
	}
	if _, err := l.Append("s1", map[string]any{"n": subBuf + 10}); err != nil {
		t.Fatalf("trigger append: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.ID != 0 || string(ev.Data) != `{"type":"replay_overflow"}` {
			t.Fatalf("nudge = %+v, want id 0 replay_overflow", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no replay_overflow nudge for lagging subscriber")
	}

	// After the nudge, the triggering event itself is delivered live.
	select {
	case ev := <-ch:
		if ev.ID != int64(subBuf+11) {
			t.Fatalf("post-nudge event = %+v, want id %d", ev, subBuf+11)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live delivery did not resume after nudge")
	}

	// Dropped events remain replayable from the ring.
	evs, overflow := l.Replay("s1", int64(subBuf))
	if overflow {
		t.Fatal("unexpected overflow: events still in ring")
	}
	if len(evs) != 11 {
		t.Fatalf("replay after drop = %d events, want 11", len(evs))
	}
}

func TestEventLogAttach(t *testing.T) {
	c := newTestChild(t, "registry")
	if err := c.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() { c.Stop() })

	r := NewRegistry(c)
	l := NewEventLog()

	// Pre-existing hooks must be chained, not replaced.
	prevNotify := make(chan string, 1)
	c.OnNotification = func(method string, _ json.RawMessage) { prevNotify <- method }
	prevEvents := make(chan any, 4)
	r.OnEvent = func(_ string, payload any) { prevEvents <- payload }

	Attach(l, c, r)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	id, err := r.New(ctx, AgentPath(t.TempDir()), "")
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	ch, unsub := l.Subscribe(id)
	defer unsub()

	// The helper emits a session/update notification on test/emit_update.
	if _, err := c.Request(ctx, "test/emit_update", nil); err != nil {
		t.Fatalf("test/emit_update: %v", err)
	}
	select {
	case ev := <-ch:
		var env struct {
			Method string `json:"method"`
			Params struct {
				SessionID string         `json:"sessionId"`
				Update    map[string]any `json:"update"`
			} `json:"params"`
		}
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			t.Fatalf("decode event envelope: %v (data %s)", err, ev.Data)
		}
		if env.Method != "session/update" || env.Params.SessionID != id || env.Params.Update["kind"] != "agent_message_chunk" {
			t.Fatalf("event envelope = %+v", env)
		}
		if ev.ID != 1 {
			t.Fatalf("first event id = %d, want 1", ev.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session/update not appended to event log")
	}
	select {
	case m := <-prevNotify:
		if m != "session/update" {
			t.Fatalf("chained OnNotification got %q", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("previous OnNotification hook not chained")
	}

	// A permission request is emitted with its issuing session id, so it
	// reaches that session's subscribers, and the previous OnEvent hook
	// chained.
	if _, err := c.Request(ctx, "test/ask_permission", nil); err != nil {
		t.Fatalf("test/ask_permission: %v", err)
	}
	select {
	case ev := <-ch:
		var payload struct {
			Type       string `json:"type"`
			ToolCallID string `json:"toolCallId"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("decode permission event: %v (data %s)", err, ev.Data)
		}
		if payload.Type != "permission_request" || payload.ToolCallID != "tc-1" {
			t.Fatalf("permission event = %+v", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("permission_request not broadcast to subscriber")
	}
	select {
	case <-prevEvents:
	case <-time.After(2 * time.Second):
		t.Fatal("previous OnEvent hook not chained")
	}

	// Resolve so the child's pending request does not outlive the test.
	if err := r.ResolvePermission("tc-1", Decision{Approved: true}); err != nil {
		t.Fatalf("resolve permission: %v", err)
	}
}
