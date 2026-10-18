package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"marshal/internal/app/config"
	"marshal/internal/pubsub"
)

// ── Fixtures and observation helpers ─────────────────────────────────────

// newSendNowTurnState is a session with an installed (no-op) interruption
// handler and one active Send now-eligible turn.
func newSendNowTurnState(t *testing.T) (*State, string) {
	t.Helper()
	s := newTestState()
	s.SetOperationInterrupt(func(context.Context, OperationRef) (OperationStop, error) {
		return OperationStop{}, nil
	})
	turn, err := s.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	return s, turn
}

// attachSteeringBroker wires a steering broker into the state and returns the
// subscriber channel, so tests can assert on published receipts.
func attachSteeringBroker(t *testing.T, s *State) <-chan pubsub.Event[SteeringEvent] {
	t.Helper()
	broker := pubsub.NewBroker[SteeringEvent]()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.SetSteeringBroker(broker)
	return broker.Subscribe(ctx)
}

// waitSteeringReceipt drains subscriber events until one matches, failing
// rather than hanging.
func waitSteeringReceipt(t *testing.T, events <-chan pubsub.Event[SteeringEvent], match func(SteeringReceipt) bool) SteeringReceipt {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if r := ev.Payload.Receipt; r != nil && match(*r) {
				return *r
			}
		case <-deadline:
			t.Fatal("timed out waiting for a matching steering receipt event")
			return SteeringReceipt{}
		}
	}
}

// assertNoSteeringReceiptEvent fails if a receipt event for requestID arrives
// within the quiet window. Snapshot-only events are ignored.
func assertNoSteeringReceiptEvent(t *testing.T, events <-chan pubsub.Event[SteeringEvent], requestID string, quiet time.Duration) {
	t.Helper()
	deadline := time.After(quiet)
	for {
		select {
		case ev := <-events:
			if r := ev.Payload.Receipt; r != nil && r.RequestID == requestID {
				t.Fatalf("unexpected steering receipt event for %s: %+v", requestID, r)
			}
		case <-deadline:
			return
		}
	}
}

// waitEventOfType returns when an event of the given type arrives, failing
// rather than hanging.
func waitEventOfType(t *testing.T, sub <-chan pubsub.Event[Event], typ string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type == typ {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q event", typ)
			return
		}
	}
}

// assertNoPendingApprovalChanged fails if a pending-approval event arrives
// within the quiet window; other events are ignored.
func assertNoPendingApprovalChanged(t *testing.T, sub <-chan pubsub.Event[Event], quiet time.Duration) {
	t.Helper()
	deadline := time.After(quiet)
	for {
		select {
		case ev := <-sub:
			if ev.Type == EventPendingApprovalChanged {
				t.Fatalf("unexpected pending-approval event: %+v", ev.Payload)
			}
		case <-deadline:
			return
		}
	}
}

// containsSteeringOutcomeID reports whether any outcome carries the given ID
// in one of its identity fields.
func containsSteeringOutcomeID(outcomes []SteeringOutcome, id string) bool {
	for _, o := range outcomes {
		if o.ToolCallID == id || o.ExecutionID == id || o.AgentID == id {
			return true
		}
	}
	return false
}

// ── Group 1: the interruption-note pipeline ──────────────────────────────

// TestBuildSteeringInterruptNoteFormat pins the exact shape of an
// untruncated note: a fixed header, then one line per identifiable outcome
// naming its kind, real ID, state and the curated summary. Exact equality is
// what rules raw command output out of the transcript: the note can carry
// nothing but these lines. An outcome with no identity at all is skipped.
func TestBuildSteeringInterruptNoteFormat(t *testing.T) {
	outcomes := []SteeringOutcome{
		{Kind: "command", State: "stopped", ToolCallID: "call_7", Summary: "the server build is still running"},
		{Kind: "agent", State: "done", AgentID: "child-1"},
		{ExecutionID: "exec-9"},
		{Kind: "command", State: "unknown", Summary: "has no identity and cannot be named"},
	}
	got := buildSteeringInterruptNote(outcomes)
	want := "User steering interrupted the previous operation before its result was used." +
		"\n- command call_7: stopped (the server build is still running)" +
		"\n- agent child-1: done" +
		"\n- work exec-9: unknown"
	if got != want {
		t.Fatalf("buildSteeringInterruptNote =\n%q\nwant\n%q", got, want)
	}
	if len(got) > steeringNoteLimit || !utf8.ValidString(got) {
		t.Fatalf("untruncated note violates the bound/UTF-8 rule: %d bytes", len(got))
	}
}

// TestBuildSteeringInterruptNoteTruncationBounded pins the byte bound: a
// large outcome set is clipped to steeringNoteLimit bytes on a rune boundary,
// the note stays valid UTF-8 and it names the surviving work before the cut.
func TestBuildSteeringInterruptNoteTruncationBounded(t *testing.T) {
	outcomes := make([]SteeringOutcome, 0, 64)
	for i := 0; i < 64; i++ {
		outcomes = append(outcomes, SteeringOutcome{
			Kind:       "command",
			State:      "continues",
			ToolCallID: fmt.Sprintf("call_%02d", i),
			Summary:    strings.Repeat("héllo wörld — ", 60) + "the unit test suite is still running",
		})
	}
	note := buildSteeringInterruptNote(outcomes)
	if note == "" {
		t.Fatal("buildSteeringInterruptNote = empty, want a note")
	}
	if len(note) > steeringNoteLimit {
		t.Fatalf("note is %d bytes, want at most %d", len(note), steeringNoteLimit)
	}
	if !utf8.ValidString(note) {
		t.Fatal("truncated note is not valid UTF-8 (a rune was split)")
	}
	if !strings.Contains(note, "call_00") {
		t.Fatalf("truncated note lost the first surviving outcome ID: %.200q", note)
	}
	if !strings.HasSuffix(note, "(truncated)") {
		t.Fatalf("truncated note does not disclose the truncation: %.40s", note[len(note)-40:])
	}
}

// TestTruncateUTF8NeverSplitsRune pins rune-safe truncation: for limits that
// land inside a multibyte rune the cut is backed up to the previous boundary,
// the result stays valid UTF-8, the visible part is an exact prefix of the
// input, and the disclosure is appended.
func TestTruncateUTF8NeverSplitsRune(t *testing.T) {
	for _, tc := range []struct {
		text  string
		limit int
	}{{strings.Repeat("日", 1000), 1500}, {strings.Repeat("é", 1000), 900}, {strings.Repeat("日", 10), 16}} {
		got := truncateUTF8(tc.text, tc.limit)
		if !utf8.ValidString(got) {
			t.Fatalf("truncateUTF8(limit=%d) produced invalid UTF-8: %q", tc.limit, got)
		}
		if len(tc.text) > tc.limit && !strings.HasSuffix(got, "(truncated)") {
			t.Fatalf("truncateUTF8(limit=%d) lost the truncation disclosure: %q", tc.limit, got)
		}
		if len(got) > tc.limit {
			t.Fatalf("truncateUTF8(limit=%d) returned %d bytes, want at most the limit", tc.limit, len(got))
		}
		// The visible part must be an exact byte prefix of the input.
		prefix := strings.TrimSuffix(got, "\n…(truncated)")
		if !strings.HasPrefix(tc.text, prefix) {
			t.Fatalf("truncateUTF8(limit=%d) visible part is not a prefix of the input: %q", tc.limit, prefix)
		}
		if len(prefix) >= len(tc.text) {
			t.Fatalf("truncateUTF8(limit=%d) never shortened the text: %q", tc.limit, got)
		}
	}
}

// TestTruncateUTF8WithinLimitUnchanged: text already inside the bound passes
// through untouched.
func TestTruncateUTF8WithinLimitUnchanged(t *testing.T) {
	text := "short, in-bounds note"
	if got := truncateUTF8(text, steeringNoteLimit); got != text {
		t.Fatalf("truncateUTF8 rewrote in-bounds text: got %q, want %q", got, text)
	}
}

// TestSendNowInterruptNoteReachesStagedBatch drives the real interruption
// worker: an accepted Send now request captures the open operation, the
// worker's outcomes become a note, StageSteering returns it in batch.Notes,
// and SteeringToSubmit keeps returning it until MarkSteeringSubmitted.
func TestSendNowInterruptNoteReachesStagedBatch(t *testing.T) {
	f := newSendNowFixture(t)
	outcomes := []SteeringOutcome{
		{Kind: "command", State: "stopped", ToolCallID: "call_31", Summary: "the interrupted build stopped"},
		{Kind: "agent", State: "done", AgentID: "child-2", Summary: "the review child finished"},
	}
	f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return outcomes, nil }}

	if _, err := f.state.SubmitSendNow(f.request("r1", "hold on and pivot")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	merged := f.state.FinishSteeringOperation(f.ref, nil) // joins the worker
	if !containsSteeringOutcomeID(merged, "call_31") || !containsSteeringOutcomeID(merged, "child-2") {
		t.Fatalf("FinishSteeringOperation outcomes = %+v, want the worker's real results", merged)
	}

	batch, err := f.state.StageSteering(f.turn)
	if err != nil {
		t.Fatalf("StageSteering = %v, want nil", err)
	}
	if len(batch.Notes) != 1 {
		t.Fatalf("batch.Notes = %+v, want exactly one note", batch.Notes)
	}
	note := batch.Notes[0]
	if note.ID == "" {
		t.Fatal("the staged note has no ID")
	}
	for _, want := range []string{"call_31", "child-2", "the interrupted build stopped", "the review child finished"} {
		if !strings.Contains(note.Text, want) {
			t.Fatalf("note %q does not name %q", note.Text, want)
		}
	}
	if len(note.Text) > steeringNoteLimit || !utf8.ValidString(note.Text) {
		t.Fatalf("staged note violates the bound/UTF-8 rule: %d bytes", len(note.Text))
	}
	if len(batch.Messages) != 1 || batch.Messages[0].Text != "hold on and pivot" {
		t.Fatalf("batch.Messages = %+v, want the queued steering text", batch.Messages)
	}

	// SteeringToSubmit keeps returning the note until it is marked submitted.
	if again := f.state.SteeringToSubmit(f.turn); len(again.Notes) != 1 || again.Notes[0].ID != note.ID {
		t.Fatalf("SteeringToSubmit = %+v, want the same note again", again.Notes)
	}

	f.state.MarkSteeringSubmitted(f.turn, []string{note.ID})
	if final := f.state.SteeringToSubmit(f.turn); len(final.Notes) != 0 {
		t.Fatalf("SteeringToSubmit after MarkSteeringSubmitted = %+v, want no notes", final.Notes)
	}
}

// TestSendNowInterruptionWithNoOutcomesProducesNoNote: a worker that learned
// nothing queues nothing, and an ordinary finish with no steering stages no
// note at all.
func TestSendNowInterruptionWithNoOutcomesProducesNoNote(t *testing.T) {
	t.Run("interruption with no outcomes", func(t *testing.T) {
		f := newSendNowFixture(t)
		f.stop = OperationStop{Wait: func(context.Context) ([]SteeringOutcome, error) { return nil, nil }}
		if _, err := f.state.SubmitSendNow(f.request("r1", "stop and change course")); err != nil {
			t.Fatalf("SubmitSendNow = %v, want nil", err)
		}
		f.state.FinishSteeringOperation(f.ref, nil) // joins the worker
		batch, err := f.state.StageSteering(f.turn)
		if err != nil {
			t.Fatalf("StageSteering = %v, want nil", err)
		}
		if len(batch.Notes) != 0 {
			t.Fatalf("batch.Notes = %+v, want none", batch.Notes)
		}
		if len(batch.Messages) != 1 || batch.Messages[0].Text != "stop and change course" {
			t.Fatalf("batch.Messages = %+v, want the queued text", batch.Messages)
		}
	})

	t.Run("ordinary finish with no steering", func(t *testing.T) {
		s, turn := newSendNowTurnState(t)
		_, ref, err := s.BeginSteeringOperation(context.Background(), turn, OperationSetup)
		if err != nil {
			t.Fatalf("BeginSteeringOperation = %v, want nil", err)
		}
		if out := s.FinishSteeringOperation(ref, nil); len(out) != 0 {
			t.Fatalf("FinishSteeringOperation = %+v, want no outcomes", out)
		}
		batch, err := s.StageSteering(turn)
		if err != nil {
			t.Fatalf("StageSteering = %v, want nil", err)
		}
		if len(batch.Notes) != 0 || len(batch.Messages) != 0 {
			t.Fatalf("StageSteering = %+v, want an empty batch", batch)
		}
		if got := s.SteeringToSubmit(turn); len(got.Notes) != 0 || len(got.Messages) != 0 {
			t.Fatalf("SteeringToSubmit = %+v, want empty", got)
		}
	})
}

// ── Group 2: regressions for the five Send now fixes ─────────────────────

// TestResolvePendingForShutdownKeepsAcceptedSteering pins the shutdown fix:
// accepted-but-undelivered steering survives — the queue keeps every entry an
// accepted request covers, the covered request's receipt moves to undelivered
// instead of being erased, and entries no request covers are dropped.
func TestResolvePendingForShutdownKeepsAcceptedSteering(t *testing.T) {
	s, turn := newSendNowTurnState(t)
	events := attachSteeringBroker(t, s)
	s.PushSteering("deliver me please")
	accepted, err := s.SubmitSendNow(SendNowRequest{SessionID: s.SessionID(), TurnID: turn, RequestID: "r1"})
	if err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	if accepted.Status != SteeringAccepted {
		t.Fatalf("accepted receipt = %+v, want status accepted", accepted)
	}
	// Queued after acceptance: no request covers it, so shutdown drops it.
	s.PushSteering("not covered by anyone")

	s.ResolvePendingForShutdown()

	if got := s.SteeringQueue(); !slices.Equal(got, []string{"deliver me please"}) {
		t.Fatalf("SteeringQueue after shutdown = %v, want only the accepted entry to survive", got)
	}
	final := waitSteeringReceipt(t, events, func(r SteeringReceipt) bool {
		return r.RequestID == "r1" && r.Status == SteeringUndelivered
	})
	if final.Version == 0 || final.Version <= accepted.Version {
		t.Fatalf("undelivered receipt version = %d, want a bump past the accepted version %d", final.Version, accepted.Version)
	}
}

// TestMarkSteeringInterruptStateNoopAfterTurnClosed pins the turn-closed
// guard: EndSteeringTurn gave the receipts their final word (undelivered); a
// late interruption worker reporting under the old ref must not move them.
func TestMarkSteeringInterruptStateNoopAfterTurnClosed(t *testing.T) {
	f := newSendNowFixture(t)
	events := attachSteeringBroker(t, f.state)
	if _, err := f.state.SubmitSendNow(f.request("r1", "please stop")); err != nil {
		t.Fatalf("SubmitSendNow = %v, want nil", err)
	}
	f.state.FinishSteeringOperation(f.ref, nil) // joins the worker
	f.state.EndSteeringTurn(f.turn, "test turn finished")
	waitSteeringReceipt(t, events, func(r SteeringReceipt) bool {
		return r.RequestID == "r1" && r.Status == SteeringUndelivered
	})

	// Late worker calls with the old ref: one without outcomes, one with.
	f.state.markSteeringInterruptState(f.ref, SteeringInterrupting, nil)
	f.state.markSteeringInterruptState(f.ref, SteeringWaitingSafe, []SteeringOutcome{
		{Kind: "command", State: "unknown", ToolCallID: "call_late", Summary: "a late worker guess"},
	})
	assertNoSteeringReceiptEvent(t, events, "r1", 150*time.Millisecond)
	if snap := f.state.SteeringSnapshot(); snap.Active {
		t.Fatal("the turn should be closed after EndSteeringTurn")
	}
}

// TestDrainAndPopProtectAcceptedRequestsDuringActiveTurn pins the drain fix:
// while the turn is ACTIVE, entries an accepted request still owns are left
// in the queue (so its ledger is not stranded) while ordinary entries drain
// normally; once the turn has closed, those entries are consumed like any
// other follow-up text.
func TestDrainAndPopProtectAcceptedRequestsDuringActiveTurn(t *testing.T) {
	t.Run("DrainSteering", func(t *testing.T) {
		s, turn := newSendNowTurnState(t)
		s.PushSteering("accepted: deliver me")
		if _, err := s.SubmitSendNow(SendNowRequest{SessionID: s.SessionID(), TurnID: turn, RequestID: "r1"}); err != nil {
			t.Fatalf("SubmitSendNow = %v, want nil", err)
		}
		s.PushSteering("loose text")

		if got := s.DrainSteering(); !slices.Equal(got, []string{"loose text"}) {
			t.Fatalf("DrainSteering during an active turn = %v, want only the unprotected entry", got)
		}
		if got := s.SteeringQueue(); !slices.Equal(got, []string{"accepted: deliver me"}) {
			t.Fatalf("SteeringQueue during an active turn = %v, want the accepted entry intact", got)
		}
		if _, ok := s.PopSteering(); ok {
			t.Fatal("PopSteering removed an entry an accepted request still owns")
		}

		s.EndSteeringTurn(turn, "test turn done")
		if got := s.DrainSteering(); !slices.Equal(got, []string{"accepted: deliver me"}) {
			t.Fatalf("DrainSteering after the turn closed = %v, want the accepted entry consumed", got)
		}
		if got := s.SteeringQueue(); len(got) != 0 {
			t.Fatalf("SteeringQueue after the closed-turn drain = %v, want empty", got)
		}
	})

	t.Run("PopSteering", func(t *testing.T) {
		s, turn := newSendNowTurnState(t)
		s.PushSteering("accepted: deliver me")
		if _, err := s.SubmitSendNow(SendNowRequest{SessionID: s.SessionID(), TurnID: turn, RequestID: "r1"}); err != nil {
			t.Fatalf("SubmitSendNow = %v, want nil", err)
		}
		if text, ok := s.PopSteering(); ok || text != "" {
			t.Fatalf("PopSteering during an active turn = (%q, %v), want no entry", text, ok)
		}
		if got := s.SteeringQueue(); !slices.Equal(got, []string{"accepted: deliver me"}) {
			t.Fatalf("SteeringQueue during an active turn = %v, want the accepted entry intact", got)
		}

		s.EndSteeringTurn(turn, "test turn done")
		if text, ok := s.PopSteering(); !ok || text != "accepted: deliver me" {
			t.Fatalf("PopSteering after the turn closed = (%q, %v), want the entry", text, ok)
		}
		if _, ok := s.PopSteering(); ok {
			t.Fatal("PopSteering returned a second entry from an empty queue")
		}
	})
}

// TestRegisterAccessRejectsAcceptedSteeringCancellation pins the access fix:
// a context whose steering cancellation was already accepted must fail
// RegisterAccess with the prompt-requested sentinel and publish no
// pending-approval event.
func TestRegisterAccessRejectsAcceptedSteeringCancellation(t *testing.T) {
	t.Run("rejected on an accepted cancellation", func(t *testing.T) {
		f := newSendNowFixture(t)
		// Keep the interruption worker inside its handler so the operation
		// context stays alive (the worker would cancel it once it returns).
		release := make(chan struct{})
		defer close(release)
		f.begin = func(OperationRef) { <-release }

		if _, err := f.state.SubmitSendNow(f.request("r1", "stop here")); err != nil {
			t.Fatalf("SubmitSendNow = %v, want nil", err)
		}
		if !SteeringRequested(f.opCtx) {
			t.Fatal("precondition failed: the operation context carries no accepted steering cancellation")
		}

		broker := pubsub.NewBroker[Event]()
		subCtx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		f.state.SetEventBroker(broker)
		sub := broker.Subscribe(subCtx)

		call := &PendingToolCall{ID: "late-approval", Name: "shell", ResponseChan: make(chan UserApprovalDecision, 1)}
		if _, waiting, err := f.state.RegisterAccess(f.opCtx, call, AccessIntent{System: true}); !errors.Is(err, errPromptRequested) || waiting {
			t.Fatalf("RegisterAccess on an accepted-cancellation context = (waiting %v, err %v), want errPromptRequested", waiting, err)
		}
		if f.state.PendingApproval() != nil || call.Access != nil {
			t.Fatal("the refused registration left the slot or the pending call touched")
		}
		assertNoPendingApprovalChanged(t, sub, 150*time.Millisecond)
	})

	t.Run("control: an uninterrupted context still registers", func(t *testing.T) {
		f := newSendNowFixture(t)
		broker := pubsub.NewBroker[Event]()
		subCtx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		f.state.SetEventBroker(broker)
		sub := broker.Subscribe(subCtx)

		call := &PendingToolCall{ID: "steady", Name: "shell", ResponseChan: make(chan UserApprovalDecision, 1)}
		if _, waiting, err := f.state.RegisterAccess(context.Background(), call, AccessIntent{System: true}); err != nil || !waiting {
			t.Fatalf("RegisterAccess = (waiting %v, err %v), want a published request", waiting, err)
		}
		waitEventOfType(t, sub, EventPendingApprovalChanged)
	})
}

// TestSubmitSendNowRejectsForeignSessionID pins the session-targeting fix:
// only the session's own SessionID is accepted — an empty SessionID is no
// longer a wildcard — and a rejected request changes nothing.
func TestSubmitSendNowRejectsForeignSessionID(t *testing.T) {
	s := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{SessionID: "sess-main"})
	s.SetOperationInterrupt(func(context.Context, OperationRef) (OperationStop, error) {
		return OperationStop{}, nil
	})
	turn, err := s.BeginSteeringTurn(context.Background())
	if err != nil {
		t.Fatalf("BeginSteeringTurn = %v, want nil", err)
	}
	s.PushSteering("originally queued")

	before := s.SteeringSnapshot()
	if _, err := s.SubmitSendNow(SendNowRequest{SessionID: "sess-other", TurnID: turn, RequestID: "r-foreign"}); !errors.Is(err, errSteeringTarget) {
		t.Fatalf("SubmitSendNow with a foreign SessionID = %v, want errSteeringTarget", err)
	}
	if _, err := s.SubmitSendNow(SendNowRequest{SessionID: "", TurnID: turn, RequestID: "r-empty"}); !errors.Is(err, errSteeringTarget) {
		t.Fatalf("SubmitSendNow with an empty SessionID = %v, want errSteeringTarget (empty is no wildcard)", err)
	}
	after := s.SteeringSnapshot()
	if after.Version != before.Version {
		t.Fatalf("a rejected request changed the snapshot version: %d -> %d", before.Version, after.Version)
	}
	if got := s.SteeringQueue(); !slices.Equal(got, []string{"originally queued"}) {
		t.Fatalf("SteeringQueue after the rejections = %v, want the original queue untouched", got)
	}
	if after.PendingCount != 0 {
		t.Fatalf("PendingCount after the rejections = %d, want 0", after.PendingCount)
	}

	// Control: the session's own ID is accepted.
	receipt, err := s.SubmitSendNow(SendNowRequest{SessionID: s.SessionID(), TurnID: turn, RequestID: "r-own", Text: "do this instead"})
	if err != nil {
		t.Fatalf("SubmitSendNow with the session's own ID = %v, want nil", err)
	}
	if receipt.Status != SteeringAccepted {
		t.Fatalf("receipt = %+v, want status accepted", receipt)
	}
}
