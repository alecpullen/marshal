package session

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"marshal/internal/pubsub"
)

// SteeringEvent is the broker payload published when the steering queue or
// the Send now controller changes. QueueLen/Message are the original queued
// marker fields; Snapshot and Receipt are the additive Send now channels a
// client may ignore. Every published value is a deep copy, so a subscriber
// cannot mutate session state through it.
type SteeringEvent struct {
	QueueLen int
	Message  string
	Snapshot *SteeringSnapshot
	Receipt  *SteeringReceipt
}

// Send now rejection causes. They are private sentinels: the transports only
// need to know that a request was refused and why, in words. A refusal never
// mutates the queue, the turn or the running operation.
var (
	errSteeringInactive    = errors.New("send now: no active turn to target")
	errSteeringClosing     = errors.New("send now: the turn is closing and does not accept new work")
	errSteeringUnsupported = errors.New("send now: this runner has no operation interruption handler")
	errSteeringTarget      = errors.New("send now: request targets a different session or turn")
	errSteeringRequestID   = errors.New("send now: a nonempty request ID is required")
	errSteeringConflict    = errors.New("send now: that request ID was already used with a different target or text")
	errSteeringEmptyFlush  = errors.New("send now: nothing queued to flush and no message text")
	errSteeringTurnActive  = errors.New("send now: a turn is already active")
	errSteeringOperation   = errors.New("send now: an operation is already open for this turn")
)

// steeringNoteLimit bounds the interruption note appended to the transcript:
// one short human-readable paragraph of IDs and outcomes, never a copy of raw
// command output.
const steeringNoteLimit = 2048

// steeringControl is the session-owned Send now controller: it accepts
// requests, captures the operation each one interrupts, records receipts, and
// keeps the in-flight steering ledger between acceptance and model submission.
//
// Guarded by State.steeringMu. Lock order is accessMu → steeringMu →
// State.mu with no inverse acquisition; every callback, wait and event
// publication happens after those locks are released.
type steeringControl struct {
	// handler is the installed operation-interruption handler. A turn is
	// "supported" only while one is installed; tests inject a no-op handler.
	handler OperationInterrupt

	// Turn identity and lifecycle. active is true between BeginSteeringTurn
	// and EndSteeringTurn; closing is set by TryFinishSteeringTurn so a new
	// request rejects before final publication.
	turnID  string
	turnCtx context.Context
	active  bool
	closing bool

	// version is the snapshot revision.
	version uint64

	// current is the open operation token, or nil. It is compare-cleared by
	// FinishSteeringOperation so a late old completion cannot clear a
	// replacement.
	current *steeringOperation

	// interrupts holds the one interruption worker started for a captured
	// operation, keyed by operation ID, so FinishSteeringOperation joins the
	// worker that belongs to the operation it is finishing.
	interrupts map[string]*steeringInterrupt

	// requests is the accepted-request ledger keyed by request ID; order
	// preserves acceptance order.
	requests map[string]*steeringRequest
	order    []string

	// stagedMsgs and stagedNotes are the ordered in-flight ledgers: entries
	// moved off the ordinary queue (and notes produced by the interruption
	// worker) that no model request has submitted yet.
	stagedMsgs   []steeringStaged
	stagedNotes  []steeringStaged
	pendingNotes []SteeringNote

	// submitted and archived are the ID sets that must never be delivered or
	// requeued twice.
	submitted map[string]bool
	archived  map[string]bool

	// lastPublished suppresses a re-publication that would tell subscribers
	// nothing new.
	lastPublished SteeringSnapshot
	hasPublished  bool

	// units counts the admitted steering mutation units (the file-commit
	// boundary). A unit admitted before the interruption finishes; a later
	// unit is rejected while the operation is interrupted.
	units int
}

// steeringRequest is one accepted Send now submission.
type steeringRequest struct {
	id        string
	turnID    string
	text      string
	covered   []string // steering message IDs this request is responsible for
	ref       OperationRef
	delivered bool
	receipt   SteeringReceipt
}

// steeringStaged is one entry of the in-flight ledger.
type steeringStaged struct {
	msg       SteeringMessage
	note      SteeringNote
	isNote    bool
	returned  bool
	submitted bool
	archived  bool
}

// steeringInterrupt is the single interruption worker started for one
// captured operation. done closes once the worker has settled every stop it
// requested, so FinishSteeringOperation joins exactly that operation's worker.
type steeringInterrupt struct {
	ref OperationRef
	// op is the operation token this worker interrupts. The worker cancels
	// exactly this token, so a worker that belongs to a replaced operation can
	// never cancel its replacement.
	op   *steeringOperation
	done chan struct{}

	mu       sync.Mutex
	outcomes []SteeringOutcome
}

func (i *steeringInterrupt) setOutcomes(outcomes []SteeringOutcome) {
	i.mu.Lock()
	i.outcomes = cloneSteeringOutcomes(outcomes)
	i.mu.Unlock()
}

func (i *steeringInterrupt) snapshot() []SteeringOutcome {
	i.mu.Lock()
	defer i.mu.Unlock()
	return cloneSteeringOutcomes(i.outcomes)
}

// PushSteering appends a message typed by the user mid-turn. It is a
// steering message (consumed at the next loop-top) if a turn is running,
// or the next follow-up turn if the turn has already ended. Publishing
// to the broker lets the TUI re-render the queued marker without polling.
//
// Every entry gets a stable internal ID so the Send now controller can
// name exactly which queued messages one accepted request covers; two
// identical texts must stay distinguishable.
func (s *State) PushSteering(msg string) {
	s.mu.Lock()
	s.steeringQueue = append(s.steeringQueue, SteeringMessage{ID: rand.Text(), Text: msg})
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen, Message: msg})
	}
}

// SteeringQueue returns a copy of the current queue (does not drain).
func (s *State) SteeringQueue() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return steeringTexts(s.steeringQueue)
}

// DrainSteering returns and clears the queue atomically. The runner
// calls this at every loop-top to inject queued messages into the live
// model context.
func (s *State) DrainSteering() []string {
	s.mu.Lock()
	out := steeringTexts(s.steeringQueue)
	s.steeringQueue = nil
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
	return out
}

// PopSteering returns and removes the oldest queued steering message.
// Used by the TUI's follow-up turn path so each Enter pops one item
// at a time; DrainSteering handles the rest when the next turn starts.
func (s *State) PopSteering() (string, bool) {
	s.mu.Lock()
	if len(s.steeringQueue) == 0 {
		s.mu.Unlock()
		return "", false
	}
	oldest := s.steeringQueue[0]
	s.steeringQueue = s.steeringQueue[1:]
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
	return oldest.Text, true
}

// ClearSteering drops queued messages without returning them. Used by the
// TUI's Ctrl+X clear and the interrupt path (cancelTurn). It only clears
// human steering; machine-generated subagent reports live in a separate
// queue (see subagent_reports.go) so a turn-cancel cannot drop a background
// child's completion report.
//
// An entry an accepted Send now request is still responsible for survives the
// clear: the user asked explicitly for it to be delivered, and a UI cleanup
// must not silently discard it. Only full-turn cleanup (EndSteeringTurn or
// DrainSteeringForInterrupt) settles those entries.
func (s *State) ClearSteering() {
	s.steeringMu.Lock()
	protected := s.protectedSteeringIDsLocked()
	s.mu.Lock()
	if len(protected) == 0 {
		s.steeringQueue = nil
	} else {
		kept := s.steeringQueue[:0]
		for _, e := range s.steeringQueue {
			if protected[e.ID] {
				kept = append(kept, e)
			}
		}
		s.steeringQueue = kept
	}
	broker := s.steeringBroker
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()
	s.steeringMu.Unlock()
	if broker != nil {
		broker.Publish("steering", SteeringEvent{QueueLen: queueLen})
	}
}

// SetSteeringBroker wires the pub/sub broker so PushSteering publishes.
func (s *State) SetSteeringBroker(b *pubsub.Broker[SteeringEvent]) {
	s.mu.Lock()
	s.steeringBroker = b
	s.mu.Unlock()
}

// SetOperationInterrupt installs the handler the interruption worker calls
// with a captured operation. A turn is "supported" only while a handler is
// installed, so a nil handler makes Send now unavailable rather than
// half-working.
func (s *State) SetOperationInterrupt(handler OperationInterrupt) {
	s.steeringMu.Lock()
	s.steering.handler = handler
	s.steering.version++
	s.steeringMu.Unlock()
	s.publishSteeringState(nil)
}

// BeginSteeringTurn opens a fresh eligible turn for Send now. It rejects a
// second active turn and a quiescing session, and initializes fresh request
// and in-flight ledgers without touching text the user already queued.
func (s *State) BeginSteeringTurn(ctx context.Context) (string, error) {
	s.accessMu.Lock()
	if s.quiescingLocked() {
		s.accessMu.Unlock()
		return "", errSteeringClosing
	}
	s.steeringMu.Lock()
	ctl := &s.steering
	if ctl.active {
		s.steeringMu.Unlock()
		s.accessMu.Unlock()
		return "", errSteeringTurnActive
	}
	turnID := rand.Text()
	ctl.turnID = turnID
	ctl.turnCtx = ctx
	ctl.active = true
	ctl.closing = false
	ctl.current = nil
	ctl.interrupts = make(map[string]*steeringInterrupt)
	ctl.requests = make(map[string]*steeringRequest)
	ctl.order = nil
	ctl.stagedMsgs = nil
	ctl.stagedNotes = nil
	ctl.pendingNotes = nil
	ctl.submitted = make(map[string]bool)
	ctl.archived = make(map[string]bool)
	ctl.units = 0
	ctl.version++
	ctl.hasPublished = false
	s.steeringMu.Unlock()
	s.accessMu.Unlock()

	s.publishSteeringState(nil)
	return turnID, nil
}

// BeginSteeringOperation registers the operation beneath turnID — a logical
// model call, a tool batch or a setup step — and returns the context every
// call in it derives from. It rejects the wrong or closing turn, a turn with
// pending immediate steering, and an operation that has not been joined yet.
func (s *State) BeginSteeringOperation(ctx context.Context, turnID string, kind OperationKind) (context.Context, OperationRef, error) {
	s.accessMu.Lock()
	s.steeringMu.Lock()
	ctl := &s.steering
	switch {
	case !ctl.active || ctl.closing:
		s.steeringMu.Unlock()
		s.accessMu.Unlock()
		return nil, OperationRef{}, errSteeringInactive
	case ctl.turnID != turnID:
		s.steeringMu.Unlock()
		s.accessMu.Unlock()
		return nil, OperationRef{}, errSteeringTarget
	case ctl.hasPendingImmediateLocked():
		// Accepted steering outranks a new operation: the current work must
		// stop and deliver before anything else is admitted.
		s.steeringMu.Unlock()
		s.accessMu.Unlock()
		return nil, OperationRef{}, ErrSendNow
	case ctl.current != nil:
		s.steeringMu.Unlock()
		s.accessMu.Unlock()
		return nil, OperationRef{}, errSteeringOperation
	}

	parent := ctx
	if parent == nil {
		parent = s.Context()
	}
	opCtx, cancel := context.WithCancelCause(parent)
	ref := OperationRef{SessionID: s.sessionID, TurnID: turnID, ID: rand.Text()}
	token := &steeringOperation{
		owner:   s,
		ref:     ref,
		kind:    kind,
		parent:  parent,
		cancel:  cancel,
		settled: make(chan struct{}),
	}
	ctl.current = token
	ctl.version++
	s.steeringMu.Unlock()
	s.accessMu.Unlock()

	s.publishSteeringState(nil)
	return context.WithValue(opCtx, steeringContextKey{}, token), ref, nil
}

// FinishSteeringOperation joins the operation's interruption worker when one
// exists, merges callback and tool outcomes with the worker's real results,
// and clears only that exact token so a late old completion cannot clear a
// replacement. The returned slice is the complete outcome set for the batch.
func (s *State) FinishSteeringOperation(ref OperationRef, outcomes []SteeringOutcome) []SteeringOutcome {
	s.steeringMu.Lock()
	ctl := &s.steering
	intr := ctl.interrupts[ref.ID]
	token := ctl.current
	s.steeringMu.Unlock()

	if intr != nil {
		select {
		case <-intr.done:
		case <-s.Context().Done():
		}
		outcomes = mergeSteeringOutcomes(outcomes, intr.snapshot())
	}

	s.steeringMu.Lock()
	if s.steering.current != nil && s.steering.current.ref == ref {
		s.steering.current = nil
		s.steering.version++
	}
	s.steeringMu.Unlock()

	// The operation is joined: release its context so anything still deriving
	// from it stops. The first cause wins, so a steering cancellation already
	// recorded by the worker is preserved.
	if token != nil && token.ref == ref {
		token.cancel(context.Canceled)
	}

	s.publishSteeringState(nil)
	return outcomes
}

// SubmitSendNow accepts one Send now request atomically. It validates the
// target, appends new text once, records the queued IDs the request covers,
// marks the captured operation as requested (so admission already sees the
// intent), publishes the accepted receipt, and starts the single interruption
// worker for that operation outside its locks.
func (s *State) SubmitSendNow(request SendNowRequest) (SteeringReceipt, error) {
	s.accessMu.Lock()
	s.steeringMu.Lock()
	ctl := &s.steering

	switch {
	case !ctl.active:
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringInactive)
	case ctl.closing:
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringClosing)
	case ctl.handler == nil:
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringUnsupported)
	case request.SessionID != "" && request.SessionID != s.sessionID:
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringTarget)
	case request.TurnID == "" || request.TurnID != ctl.turnID:
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringTarget)
	case request.RequestID == "":
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringRequestID)
	}

	// A retried request ID returns the receipt it already has, before any
	// queue check, so a flush that already consumed its queue stays
	// idempotent. Reusing the ID for a different target or text is refused.
	if rec, ok := ctl.requests[request.RequestID]; ok {
		if rec.turnID != ctl.turnID || rec.text != request.Text {
			return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringConflict)
		}
		receipt := cloneSteeringReceipt(rec.receipt)
		s.steeringMu.Unlock()
		s.accessMu.Unlock()
		return receipt, nil
	}

	flush := strings.TrimSpace(request.Text) == ""
	s.mu.Lock()
	queued := append([]SteeringMessage(nil), s.steeringQueue...)
	s.mu.Unlock()
	staged := ctl.deliverableStagedLocked()

	if flush && len(queued) == 0 && len(staged) == 0 {
		// Whitespace-only text is a flush, never a new empty entry.
		return SteeringReceipt{}, s.rejectSendNowLocked(errSteeringEmptyFlush)
	}

	rec := &steeringRequest{id: request.RequestID, turnID: ctl.turnID, text: request.Text}
	for _, e := range queued {
		rec.covered = append(rec.covered, e.ID)
	}
	for _, st := range staged {
		rec.covered = append(rec.covered, st.msg.ID)
	}
	if !flush {
		entry := SteeringMessage{ID: rand.Text(), Text: request.Text}
		s.mu.Lock()
		s.steeringQueue = append(s.steeringQueue, entry)
		s.mu.Unlock()
		rec.covered = append(rec.covered, entry.ID)
	}

	// Capturing the current token marks the operation this request
	// interrupts. With no open operation the request simply waits for the
	// next checkpoint: no operation is invented.
	startWorker := false
	if token := ctl.current; token != nil {
		rec.ref = token.ref
		token.requested.Store(true)
		startWorker = intrPending(ctl, token.ref.ID)
	}

	ctl.requests[rec.id] = rec
	ctl.order = append(ctl.order, rec.id)
	rec.receipt = SteeringReceipt{
		Version:     1,
		RequestID:   rec.id,
		TurnID:      ctl.turnID,
		Status:      SteeringAccepted,
		OperationID: rec.ref.ID,
	}
	ctl.version++
	receipt := cloneSteeringReceipt(rec.receipt)
	token := ctl.current
	s.steeringMu.Unlock()
	s.accessMu.Unlock()

	s.publishSteeringState(&receipt)
	if startWorker && token != nil {
		s.startSteeringInterrupt(token)
	}
	return receipt, nil
}

// SteeringSnapshot returns an immutable copy of the active-turn steering
// state. It never blocks on a running operation.
func (s *State) SteeringSnapshot() SteeringSnapshot {
	s.steeringMu.Lock()
	defer s.steeringMu.Unlock()
	snap, _ := s.steeringSnapshotLocked()
	return snap
}

// StageSteering moves newly queued entries and freshly produced interruption
// notes into the ordered in-flight ledger and returns the batch the caller
// has not seen yet. It marks nothing delivered and persists nothing: an entry
// exists in exactly one of replay history and the follow-up queue.
func (s *State) StageSteering(turnID string) (SteeringBatch, error) {
	s.steeringMu.Lock()
	ctl := &s.steering
	if !ctl.active || ctl.turnID != turnID {
		s.steeringMu.Unlock()
		return SteeringBatch{}, errSteeringInactive
	}
	s.mu.Lock()
	queued := s.steeringQueue
	s.steeringQueue = nil
	s.mu.Unlock()
	for _, e := range queued {
		ctl.stagedMsgs = append(ctl.stagedMsgs, steeringStaged{msg: e})
	}
	for _, n := range ctl.pendingNotes {
		ctl.stagedNotes = append(ctl.stagedNotes, steeringStaged{note: n, isNote: true})
	}
	ctl.pendingNotes = nil

	batch := SteeringBatch{}
	for i := range ctl.stagedNotes {
		st := &ctl.stagedNotes[i]
		if st.returned || st.submitted || st.archived {
			continue
		}
		st.returned = true
		batch.Notes = append(batch.Notes, st.note)
	}
	for i := range ctl.stagedMsgs {
		st := &ctl.stagedMsgs[i]
		if st.returned || st.submitted || st.archived {
			continue
		}
		st.returned = true
		batch.Messages = append(batch.Messages, st.msg)
	}
	ctl.version++
	s.steeringMu.Unlock()

	s.publishSteeringState(nil)
	return batch, nil
}

// SteeringToSubmit returns every staged entry no model request has submitted
// yet, in order. The caller persists them and then acknowledges their IDs.
func (s *State) SteeringToSubmit(turnID string) SteeringBatch {
	s.steeringMu.Lock()
	defer s.steeringMu.Unlock()
	ctl := &s.steering
	if ctl.turnID != turnID {
		return SteeringBatch{}
	}
	batch := SteeringBatch{}
	for i := range ctl.stagedNotes {
		st := &ctl.stagedNotes[i]
		if st.submitted || st.archived {
			continue
		}
		batch.Notes = append(batch.Notes, st.note)
	}
	for i := range ctl.stagedMsgs {
		st := &ctl.stagedMsgs[i]
		if st.submitted || st.archived {
			continue
		}
		batch.Messages = append(batch.Messages, st.msg)
	}
	return batch
}

// MarkSteeringSubmitted marks exactly the supplied IDs submitted and advances
// a request to delivered once every message ID it covers has been submitted.
// Retrying it is idempotent.
func (s *State) MarkSteeringSubmitted(turnID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}

	s.steeringMu.Lock()
	ctl := &s.steering
	if ctl.turnID != turnID {
		s.steeringMu.Unlock()
		return
	}
	changed := false
	for i := range ctl.stagedMsgs {
		st := &ctl.stagedMsgs[i]
		if set[st.msg.ID] && !st.submitted {
			st.submitted = true
			ctl.submitted[st.msg.ID] = true
			changed = true
		}
	}
	for i := range ctl.stagedNotes {
		st := &ctl.stagedNotes[i]
		if set[st.note.ID] && !st.submitted {
			st.submitted = true
			ctl.submitted[st.note.ID] = true
			changed = true
		}
	}
	var receipts []SteeringReceipt
	if ctl.active {
		for _, id := range ctl.order {
			rec := ctl.requests[id]
			if rec == nil || rec.delivered || !ctl.requestSatisfiedLocked(rec) {
				continue
			}
			rec.delivered = true
			if rec.receipt.Status != SteeringDelivered {
				rec.receipt.Status = SteeringDelivered
				rec.receipt.Version++
			}
			receipts = append(receipts, cloneSteeringReceipt(rec.receipt))
			changed = true
		}
	}
	if changed {
		ctl.version++
	}
	s.steeringMu.Unlock()

	for i := range receipts {
		r := receipts[i]
		s.publishSteeringState(&r)
	}
}

// HasImmediateSteering reports whether the turn still owes the model an
// accepted, undelivered steering message.
func (s *State) HasImmediateSteering(turnID string) bool {
	s.steeringMu.Lock()
	defer s.steeringMu.Unlock()
	ctl := &s.steering
	if ctl.turnID != turnID {
		return false
	}
	return ctl.hasAcceptedDeliverableLocked()
}

// TryFinishSteeringTurn is the atomic final-admission check: it succeeds only
// when no accepted deliverable steering is outstanding, and marks the turn
// closing so a new request rejects before final publication.
func (s *State) TryFinishSteeringTurn(turnID string) bool {
	s.steeringMu.Lock()
	ctl := &s.steering
	if !ctl.active || ctl.closing || ctl.turnID != turnID || ctl.hasAcceptedDeliverableLocked() {
		s.steeringMu.Unlock()
		return false
	}
	ctl.closing = true
	ctl.version++
	s.steeringMu.Unlock()

	s.publishSteeringState(nil)
	return true
}

// EndSteeringTurn closes an eligible turn. It disarms the turn's interruption
// callbacks, marks every remaining receipt undelivered so the outcome stays
// truthful, and retains unsubmitted steering text as ordinary follow-up queue
// content. An entry the interruption marker already reported is not requeued.
func (s *State) EndSteeringTurn(turnID, reason string) {
	s.steeringMu.Lock()
	ctl := &s.steering
	if !ctl.active || ctl.turnID != turnID {
		s.steeringMu.Unlock()
		return
	}
	_ = reason

	ctl.active = false
	ctl.closing = false
	if ctl.current != nil {
		ctl.current.cancel(context.Canceled)
	}
	ctl.current = nil

	var receipts []SteeringReceipt
	for _, id := range ctl.order {
		rec := ctl.requests[id]
		if rec == nil || rec.delivered {
			continue
		}
		rec.receipt.Status = SteeringUndelivered
		rec.receipt.Version++
		receipts = append(receipts, cloneSteeringReceipt(rec.receipt))
	}

	var restore []SteeringMessage
	for i := range ctl.stagedMsgs {
		st := &ctl.stagedMsgs[i]
		if st.submitted || st.archived {
			continue
		}
		restore = append(restore, st.msg)
	}
	ctl.stagedMsgs = nil
	ctl.stagedNotes = nil
	ctl.pendingNotes = nil
	ctl.units = 0
	ctl.version++

	if len(restore) > 0 {
		s.mu.Lock()
		s.steeringQueue = append(restore, s.steeringQueue...)
		s.mu.Unlock()
	}
	s.steeringMu.Unlock()

	s.publishSteeringState(nil)
	for i := range receipts {
		r := receipts[i]
		s.publishSteeringState(&r)
	}
}

// DrainSteeringForInterrupt returns the queued and staged unsubmitted text
// once, marking it archived, so the interruption marker and the follow-up
// queue cannot both carry the same feedback.
func (s *State) DrainSteeringForInterrupt() []string {
	s.steeringMu.Lock()
	ctl := &s.steering

	s.mu.Lock()
	var out []string
	for _, e := range s.steeringQueue {
		out = append(out, e.Text)
		if ctl.archived != nil {
			ctl.archived[e.ID] = true
		}
	}
	s.steeringQueue = nil
	s.mu.Unlock()

	for i := range ctl.stagedMsgs {
		st := &ctl.stagedMsgs[i]
		if st.submitted || st.archived {
			continue
		}
		out = append(out, st.msg.Text)
		st.archived = true
		if ctl.archived != nil {
			ctl.archived[st.msg.ID] = true
		}
	}
	if len(out) > 0 {
		ctl.version++
	}
	s.steeringMu.Unlock()

	if len(out) > 0 {
		s.publishSteeringState(nil)
	}
	return out
}

// beginSteeringMutation admits one atomic commit unit under the steering
// gate and returns an idempotent release. A requested or closed scope is
// refused before any file I/O starts, so an interruption between units is
// respected and an admitted unit finishes.
func (s *State) beginSteeringMutation(ctx context.Context) (func(), error) {
	op, _ := ctx.Value(steeringContextKey{}).(*steeringOperation)
	if op == nil || op.owner != s {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	s.steeringMu.Lock()
	ctl := &s.steering
	switch {
	case !ctl.active || ctl.closing || ctl.turnID != op.ref.TurnID:
		s.steeringMu.Unlock()
		return nil, errSteeringInactive
	case op.requested.Load():
		s.steeringMu.Unlock()
		return nil, ErrSendNow
	}
	ctl.units++
	turnID := ctl.turnID
	s.steeringMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.steeringMu.Lock()
			if s.steering.turnID == turnID && s.steering.units > 0 {
				s.steering.units--
			}
			s.steeringMu.Unlock()
		})
	}, nil
}

// startSteeringInterrupt starts the single interruption worker for a captured
// operation. The worker's Begin closes old-operation admission and requests
// its stops; later requests for the same operation share this worker.
func (s *State) startSteeringInterrupt(token *steeringOperation) {
	token.interruptOnce.Do(func() {
		intr := &steeringInterrupt{ref: token.ref, op: token, done: make(chan struct{})}
		s.steeringMu.Lock()
		if s.steering.interrupts == nil {
			s.steering.interrupts = make(map[string]*steeringInterrupt)
		}
		s.steering.interrupts[token.ref.ID] = intr
		handler := s.steering.handler
		turnCtx := s.steering.turnCtx
		s.steeringMu.Unlock()

		if handler == nil {
			close(intr.done)
			return
		}
		go s.runSteeringInterrupt(turnCtx, intr, handler)
	})
}

// runSteeringInterrupt is the interruption worker. It never holds a session
// lock while the handler, the prompt cancellation or the join runs.
func (s *State) runSteeringInterrupt(turnCtx context.Context, intr *steeringInterrupt, handler OperationInterrupt) {
	ctx := turnCtx
	if ctx == nil {
		ctx = s.Context()
	}
	ref := intr.ref

	// Begin has closed the old operation's admission and requested its
	// stops by the time it returns; it never waits for the process itself.
	// The status moves to interrupting before the call so a slow Begin still
	// renders a truthful pending state.
	s.markSteeringInterruptState(ref, SteeringInterrupting, nil)
	stop, err := handler(ctx, ref)

	s.cancelOperationPromptsForInterrupt(ref)
	if intr.token() != nil {
		intr.token().cancel(ErrSendNow)
	}

	status := SteeringInterrupting
	var outcomes []SteeringOutcome
	switch {
	case err != nil:
		// A handler that failed has told us nothing. Do not claim a stop and
		// do not release conflicting work: report unknown and keep waiting.
		status = SteeringWaitingSafe
		outcomes = append(outcomes, SteeringOutcome{
			Kind:    "command",
			State:   "unknown",
			Summary: "interruption handler failed; remote side effects are unknown",
		})
	case stop.Wait != nil:
		got, werr := stop.Wait(s.Context())
		outcomes = append(outcomes, got...)
		if werr != nil {
			status = SteeringWaitingSafe
			outcomes = append(outcomes, SteeringOutcome{
				Kind:    "command",
				State:   "unknown",
				Summary: "the interrupted operation did not confirm settlement",
			})
		}
	}

	if s.steeringUnitsOutstanding() {
		status = SteeringWaitingSafe
	} else if s.heldNow() && status != SteeringWaitingSafe {
		status = SteeringWaitingResume
	}

	intr.setOutcomes(outcomes)
	s.markSteeringInterruptState(ref, status, outcomes)
	s.queueSteeringNote(outcomes)
	close(intr.done)
}

// markSteeringInterruptState advances every request that captured ref to the
// given status. A delivered or already-settled receipt is left alone.
func (s *State) markSteeringInterruptState(ref OperationRef, status SteeringStatus, outcomes []SteeringOutcome) {
	s.steeringMu.Lock()
	ctl := &s.steering
	var receipts []SteeringReceipt
	for _, id := range ctl.order {
		rec := ctl.requests[id]
		if rec == nil || rec.ref != ref || rec.delivered {
			continue
		}
		if rec.receipt.Status == status && len(outcomes) == 0 {
			continue
		}
		rec.receipt.Status = status
		rec.receipt.Version++
		if len(outcomes) > 0 {
			rec.receipt.Outcomes = cloneSteeringOutcomes(outcomes)
		}
		receipts = append(receipts, cloneSteeringReceipt(rec.receipt))
	}
	if len(receipts) > 0 {
		ctl.version++
	}
	s.steeringMu.Unlock()

	for i := range receipts {
		r := receipts[i]
		s.publishSteeringState(&r)
	}
}

// queueSteeringNote queues one bounded interruption note for the next
// checkpoint. It names real outcomes only: never raw output, never a claim
// that unconfirmed remote work stopped.
func (s *State) queueSteeringNote(outcomes []SteeringOutcome) {
	text := buildSteeringInterruptNote(outcomes)
	if text == "" {
		return
	}
	s.steeringMu.Lock()
	ctl := &s.steering
	if !ctl.active {
		s.steeringMu.Unlock()
		return
	}
	ctl.pendingNotes = append(ctl.pendingNotes, SteeringNote{ID: rand.Text(), Text: text})
	ctl.version++
	s.steeringMu.Unlock()

	s.publishSteeringState(nil)
}

// steeringUnitsOutstanding reports whether an admitted file-commit unit has
// not been released yet. An interruption cannot claim a safe boundary while
// one is still committing.
func (s *State) steeringUnitsOutstanding() bool {
	s.steeringMu.Lock()
	defer s.steeringMu.Unlock()
	return s.steering.units > 0
}

// heldNow reads the authoritative hold flag. Send now interrupts work but
// never silently unholds the session.
func (s *State) heldNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hold
}

// cancelOperationPromptsForInterrupt is the interruption worker's hook into
// the canonical prompt registry: it settles every pending human/gate prompt
// owned by ref, granting no authority and fabricating no answer.
//
// Task 3 owns the registry (State.CancelOperationPrompts). Until it lands this
// seam is intentionally inert, so the worker's ordering — handler Begin,
// prompt cancellation, context cancel, join — is already fixed.
func (s *State) cancelOperationPromptsForInterrupt(ref OperationRef) {
	if ref.ID == "" {
		return
	}
}

// rejectSendNowLocked publishes the refusal reason and releases both locks.
// A rejected request changes nothing: no queue entry, no intent, no stop.
func (s *State) rejectSendNowLocked(err error) error {
	s.steeringMu.Unlock()
	s.accessMu.Unlock()
	return err
}

// publishSteeringState captures an immutable snapshot under the steering
// locks and publishes it to both brokers afterwards. An identical snapshot
// with no receipt transition tells subscribers nothing new and is suppressed.
func (s *State) publishSteeringState(receipt *SteeringReceipt) {
	s.steeringMu.Lock()
	snap, queueLen := s.steeringSnapshotLocked()
	last := s.steering.lastPublished
	published := s.steering.hasPublished
	if receipt == nil && published && last == snap {
		s.steeringMu.Unlock()
		return
	}
	s.steering.lastPublished = snap
	s.steering.hasPublished = true
	s.steeringMu.Unlock()

	s.mu.Lock()
	broker := s.steeringBroker
	s.mu.Unlock()

	var receiptCopy *SteeringReceipt
	if receipt != nil {
		r := cloneSteeringReceipt(*receipt)
		receiptCopy = &r
	}
	if broker != nil {
		snapCopy := snap
		broker.Publish("steering", SteeringEvent{
			QueueLen: queueLen,
			Snapshot: &snapCopy,
			Receipt:  receiptCopy,
		})
	}
	if receiptCopy != nil {
		s.publishEvent(EventSteeringReceipt, Event{SteeringReceipt: receiptCopy})
	}
	snapCopy := snap
	s.publishEvent(EventSteeringState, Event{Steering: &snapCopy})
}

// steeringSnapshotLocked builds the current snapshot. Callers must hold
// steeringMu; it briefly takes State.mu for the hold flag and queue depth.
func (s *State) steeringSnapshotLocked() (SteeringSnapshot, int) {
	ctl := &s.steering
	s.mu.Lock()
	held := s.hold
	queueLen := len(s.steeringQueue)
	s.mu.Unlock()

	snap := SteeringSnapshot{
		Version:      ctl.version,
		TurnID:       ctl.turnID,
		Active:       ctl.active,
		Supported:    ctl.handler != nil,
		Held:         held,
		PendingCount: ctl.pendingCountLocked(),
	}
	if ctl.current != nil {
		snap.OperationID = ctl.current.ref.ID
		snap.OperationKind = ctl.current.kind
	}
	return snap, queueLen
}

// pendingCountLocked counts the accepted, still-undelivered messages every
// live request is responsible for — queued or staged alike — so the snapshot
// reflects acceptance before staging moves an entry.
func (ctl *steeringControl) pendingCountLocked() int {
	n := 0
	for _, id := range ctl.order {
		rec := ctl.requests[id]
		if rec == nil || rec.delivered {
			continue
		}
		for _, mid := range rec.covered {
			if ctl.submitted[mid] || ctl.archived[mid] {
				continue
			}
			n++
		}
	}
	return n
}

// deliverableStagedLocked returns staged message entries no model request has
// submitted or archived, in order.
func (ctl *steeringControl) deliverableStagedLocked() []steeringStaged {
	var out []steeringStaged
	for i := range ctl.stagedMsgs {
		st := ctl.stagedMsgs[i]
		if st.submitted || st.archived {
			continue
		}
		out = append(out, st)
	}
	return out
}

// hasPendingImmediateLocked reports whether an accepted request still owes the
// transcript something: acceptance outranks new work until every covered entry
// is submitted to a model request or has already been reported by the
// interruption marker.
func (ctl *steeringControl) hasPendingImmediateLocked() bool {
	for _, id := range ctl.order {
		rec := ctl.requests[id]
		if rec == nil || rec.delivered {
			continue
		}
		if !ctl.requestSatisfiedLocked(rec) {
			return true
		}
	}
	return false
}

// hasAcceptedDeliverableLocked is the final-admission predicate: accepted
// immediate work exists that no model request has delivered yet.
func (ctl *steeringControl) hasAcceptedDeliverableLocked() bool {
	return ctl.hasPendingImmediateLocked()
}

// requestSatisfiedLocked reports whether every message a request covers has
// been submitted to a model request or already reported by the interruption
// marker, so its receipt may advance to delivered.
func (ctl *steeringControl) requestSatisfiedLocked(rec *steeringRequest) bool {
	for _, id := range rec.covered {
		if ctl.submitted[id] || ctl.archived[id] {
			continue
		}
		return false
	}
	return true
}

// intrPending reports whether the operation still needs its interruption
// worker started.
func intrPending(ctl *steeringControl, opID string) bool {
	if opID == "" {
		return false
	}
	if ctl.interrupts == nil {
		return true
	}
	_, ok := ctl.interrupts[opID]
	return !ok
}

// protectedSteeringIDsLocked returns the queued IDs an accepted, undelivered
// request is still responsible for; a UI clear must preserve them.
func (s *State) protectedSteeringIDsLocked() map[string]bool {
	protected := make(map[string]bool)
	for _, id := range s.steering.order {
		rec := s.steering.requests[id]
		if rec == nil || rec.delivered {
			continue
		}
		for _, mid := range rec.covered {
			protected[mid] = true
		}
	}
	return protected
}

// token returns the operation token this worker belongs to, or nil.
func (i *steeringInterrupt) token() *steeringOperation {
	if i == nil {
		return nil
	}
	return i.op
}

// mergeSteeringOutcomes merges two outcome lists by kind plus stable
// identity, keeping a real terminal result over an earlier "continues"
// snapshot of the same target.
func mergeSteeringOutcomes(base, extra []SteeringOutcome) []SteeringOutcome {
	out := make([]SteeringOutcome, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	add := func(o SteeringOutcome) {
		key := o.Kind + "\x00" + o.ToolCallID + "\x00" + o.ExecutionID + "\x00" + o.AgentID
		if i, ok := index[key]; ok {
			if out[i].State == "continues" && o.State != "continues" {
				out[i] = o
			}
			return
		}
		index[key] = len(out)
		out = append(out, o)
	}
	for _, o := range base {
		add(o)
	}
	for _, o := range extra {
		add(o)
	}
	return out
}

// buildSteeringInterruptNote renders the bounded interruption context: IDs,
// real outcomes and surviving work. It carries no raw command output and
// discloses truncation.
func buildSteeringInterruptNote(outcomes []SteeringOutcome) string {
	var b strings.Builder
	b.WriteString("User steering interrupted the previous operation before its result was used.")
	wrote := false
	for _, o := range outcomes {
		label := o.Kind
		if label == "" {
			label = "work"
		}
		id := o.ToolCallID
		if id == "" {
			id = o.ExecutionID
		}
		if id == "" {
			id = o.AgentID
		}
		if id == "" {
			continue
		}
		state := o.State
		if state == "" {
			state = "unknown"
		}
		b.WriteString("\n- ")
		b.WriteString(label)
		b.WriteString(" ")
		b.WriteString(id)
		b.WriteString(": ")
		b.WriteString(state)
		if o.Summary != "" {
			b.WriteString(" (")
			b.WriteString(o.Summary)
			b.WriteString(")")
		}
		wrote = true
	}
	if !wrote {
		return ""
	}
	return truncateUTF8(b.String(), steeringNoteLimit)
}

// truncateUTF8 bounds text to limit bytes on a rune boundary and discloses
// the truncation.
func truncateUTF8(text string, limit int) string {
	text = strings.ToValidUTF8(text, "")
	if len(text) <= limit {
		return text
	}
	const suffix = "\n…(truncated)"
	budget := limit - len(suffix)
	if budget < 0 {
		budget = 0
	}
	cut := text[:budget]
	for len(cut) > 0 && !utf8.RuneStart(cut[len(cut)-1]) {
		cut = cut[:len(cut)-1]
	}
	return cut + suffix
}

// cloneSteeringOutcomes returns a deep copy so a subscriber mutating the
// received slice cannot reach session state.
func cloneSteeringOutcomes(in []SteeringOutcome) []SteeringOutcome {
	if len(in) == 0 {
		return nil
	}
	out := make([]SteeringOutcome, len(in))
	copy(out, in)
	return out
}

// cloneSteeringReceipt returns a deep copy of a receipt, including its
// outcome list.
func cloneSteeringReceipt(in SteeringReceipt) SteeringReceipt {
	out := in
	out.Outcomes = cloneSteeringOutcomes(in.Outcomes)
	return out
}

// steeringTexts projects typed queue entries onto the plain-text view the
// existing string APIs expose. It always returns a fresh slice, so a caller
// mutating the result cannot reach the queue.
func steeringTexts(entries []SteeringMessage) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Text
	}
	return out
}
