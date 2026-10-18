package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
)

// Canonical human-request settlement.
//
// Every prompt the agent raises — an approval, a question, a skill-load gate,
// a masked privileged-auth prompt — is registered here once, under the
// session's access gate, and settled through a single record. Display and
// event copies carry the record's key and its shared responder instead of a
// response channel, so a copy can never grant, answer or clear a request on
// its own, and a late copy of an old request can never settle a newer one.
//
// The same gate arbitrates a user answer against an accepted steering
// cancellation: cancellation intent that is already visible wins outright and
// no callback is applied, while a grant whose claim was accepted first runs to
// completion and is never rolled back.
var (
	// errPromptOccupied refuses a registration that would displace the prompt
	// the user is currently looking at.
	errPromptOccupied = errors.New("a pending prompt already occupies the slot")
	// errPromptRequested refuses a registration on an operation whose steering
	// cancellation was already accepted.
	errPromptRequested = errors.New("the request was already interrupted")
	// errPromptUnknown reports a key that names no live prompt.
	errPromptUnknown = errors.New("the key does not name a pending request")
	// errPromptSuperseded reports a registered prompt whose slot a generic
	// compatibility setter cleared or replaced before any answer arrived.
	errPromptSuperseded = errors.New("the pending request was superseded")
)

// promptRef is the canonical responder shared by every display or event copy
// of a registered prompt. It owns no channel and no sync.Once: settlement is
// routed back to the owning session by key, which is what makes an answer
// compare against the canonical request instead of against a copied channel.
type promptRef struct {
	key   PromptKey
	owner *State
}

// promptRecord is the session's private settlement record for one registered
// prompt. It is retained after settlement (so PromptCause and PromptContext
// still answer truthfully) until ClearPrompt or shutdown releases it.
type promptRecord struct {
	key PromptKey
	ref *promptRef
	// ctx is the original request context. SteeringRequested on it is the
	// cancellation-intent test this record arbitrates against.
	ctx  context.Context
	done chan struct{}

	// access marks a typed access request: its settlement belongs to
	// finishAccessLocked, so this record only mirrors it and never delivers.
	access bool

	// exactly one of these is the canonical prompt.
	approval *PendingToolCall
	question *PendingQuestion
	skill    *PendingSkillGate
	// sudo is deliberately not retained beyond its pointer: a canceled sudo
	// prompt must leave no password bytes behind, and the record never holds
	// a decision value.
	sudo *PendingSudoAuth

	// claimed/settled/cause and claimDone are the arbitration state. claimed
	// means a settlement claim owns this record; claimDone closes when that
	// claim finishes, so a cancellation that lost can wait for the grant it
	// must not roll back.
	claimed     bool
	settled     bool
	cause       error
	claimDone   chan struct{}
	claimClosed bool
}

// ── Registration ───────────────────────────────────────────────────────

// newPromptRecordLocked builds a fresh record for one prompt. It refuses a
// context whose steering cancellation was already accepted, so an interrupted
// operation cannot publish a new prompt. Caller holds accessMu.
func (s *State) newPromptRecordLocked(ctx context.Context, kind PromptKind) (*promptRecord, error) {
	if ctx != nil && SteeringRequested(ctx) {
		return nil, errPromptRequested
	}
	ref := OperationRef{}
	if op, ok := OperationFromContext(ctx); ok {
		ref = op
	}
	key := PromptKey{ID: rand.Text(), Kind: kind, Operation: ref}
	return &promptRecord{
		key:       key,
		ref:       &promptRef{key: key, owner: s},
		ctx:       ctx,
		done:      make(chan struct{}),
		claimDone: make(chan struct{}),
	}, nil
}

// addPromptLocked installs an unsettled record. Caller holds accessMu.
func (s *State) addPromptLocked(rec *promptRecord) {
	if s.prompts == nil {
		s.prompts = make(map[string]*promptRecord)
	}
	s.prompts[rec.key.ID] = rec
}

// lookupPromptLocked finds a live, unsettled record for a key. A key whose ID,
// kind or operation does not match names nothing: a stale event copy cannot
// reach the request that replaced it. Caller holds accessMu.
func (s *State) lookupPromptLocked(key PromptKey) *promptRecord {
	rec := s.prompts[key.ID]
	if rec == nil || rec.settled {
		return nil
	}
	if rec.key.Kind != key.Kind || rec.key.Operation != key.Operation {
		return nil
	}
	return rec
}

// RegisterApproval publishes an ordinary tool approval as a canonical request.
// It rejects an occupied slot and an already-interrupted operation before
// touching the slot, so a refused registration changes nothing.
func (s *State) RegisterApproval(ctx context.Context, pending *PendingToolCall) (PromptKey, error) {
	if pending == nil {
		return PromptKey{}, fmt.Errorf("session: register approval: no pending call")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	s.mu.Lock()
	occupied := s.pendingApproval != nil
	s.mu.Unlock()
	if occupied {
		return PromptKey{}, fmt.Errorf("session: register approval: %w", errPromptOccupied)
	}
	rec, err := s.newPromptRecordLocked(ctx, PromptApproval)
	if err != nil {
		return PromptKey{}, fmt.Errorf("session: register approval: %w", err)
	}
	rec.approval = pending
	pending.Key = rec.key
	pending.canonical = rec.ref
	s.addPromptLocked(rec)

	s.mu.Lock()
	s.pendingApproval = pending
	s.mu.Unlock()
	s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: approvalSnapshot(pending)})
	return rec.key, nil
}

// RegisterQuestion publishes a structured question round-trip.
func (s *State) RegisterQuestion(ctx context.Context, pending *PendingQuestion) (PromptKey, error) {
	if pending == nil {
		return PromptKey{}, fmt.Errorf("session: register question: no pending question")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	s.mu.Lock()
	occupied := s.pendingQuestion != nil
	s.mu.Unlock()
	if occupied {
		return PromptKey{}, fmt.Errorf("session: register question: %w", errPromptOccupied)
	}
	rec, err := s.newPromptRecordLocked(ctx, PromptQuestion)
	if err != nil {
		return PromptKey{}, fmt.Errorf("session: register question: %w", err)
	}
	rec.question = pending
	pending.Key = rec.key
	pending.canonical = rec.ref
	s.addPromptLocked(rec)

	s.mu.Lock()
	s.pendingQuestion = pending
	s.mu.Unlock()
	s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: questionSnapshot(pending)})
	return rec.key, nil
}

// RegisterSkillGate publishes a skill-load gate prompt.
func (s *State) RegisterSkillGate(ctx context.Context, pending *PendingSkillGate) (PromptKey, error) {
	if pending == nil {
		return PromptKey{}, fmt.Errorf("session: register skill gate: no pending prompt")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	s.mu.Lock()
	occupied := s.pendingSkillGate != nil
	s.mu.Unlock()
	if occupied {
		return PromptKey{}, fmt.Errorf("session: register skill gate: %w", errPromptOccupied)
	}
	rec, err := s.newPromptRecordLocked(ctx, PromptSkill)
	if err != nil {
		return PromptKey{}, fmt.Errorf("session: register skill gate: %w", err)
	}
	rec.skill = pending
	pending.Key = rec.key
	pending.canonical = rec.ref
	s.addPromptLocked(rec)

	s.mu.Lock()
	s.pendingSkillGate = pending
	s.mu.Unlock()
	s.publishEvent(EventPendingSkillGateChanged, Event{PendingSkillGate: skillGateSnapshot(pending)})
	return rec.key, nil
}

// RegisterSudoAuth publishes a masked privileged-auth prompt.
func (s *State) RegisterSudoAuth(ctx context.Context, pending *PendingSudoAuth) (PromptKey, error) {
	if pending == nil {
		return PromptKey{}, fmt.Errorf("session: register sudo auth: no pending prompt")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()

	s.mu.Lock()
	occupied := s.pendingSudoAuth != nil
	s.mu.Unlock()
	if occupied {
		return PromptKey{}, fmt.Errorf("session: register sudo auth: %w", errPromptOccupied)
	}
	rec, err := s.newPromptRecordLocked(ctx, PromptSudo)
	if err != nil {
		return PromptKey{}, fmt.Errorf("session: register sudo auth: %w", err)
	}
	rec.sudo = pending
	pending.Key = rec.key
	pending.canonical = rec.ref
	s.addPromptLocked(rec)

	s.mu.Lock()
	s.pendingSudoAuth = pending
	s.mu.Unlock()
	s.publishEvent(EventPendingSudoAuthChanged, Event{PendingSudoAuth: sudoAuthSnapshot(pending)})
	return rec.key, nil
}

// ── Event copies ───────────────────────────────────────────────────────

// The three helpers below build the event/display copy of a registered prompt:
// it carries the canonical key and responder but never a response channel, so
// a subscriber cannot answer a request it does not own.

func approvalSnapshot(tc *PendingToolCall) *PendingToolCall {
	return &PendingToolCall{
		ID:        tc.ID,
		Name:      tc.Name,
		Args:      tc.Args,
		Command:   tc.Command,
		Risk:      tc.Risk,
		Reason:    tc.Reason,
		Diff:      tc.Diff,
		Schema:    tc.Schema,
		StepID:    tc.StepID,
		Access:    copyAccessRequest(tc.Access),
		Key:       tc.Key,
		canonical: tc.canonical,
	}
}

func questionSnapshot(q *PendingQuestion) *PendingQuestion {
	return &PendingQuestion{
		Questions: append([]Question(nil), q.Questions...),
		Key:       q.Key,
		canonical: q.canonical,
	}
}

func skillGateSnapshot(sg *PendingSkillGate) *PendingSkillGate {
	return &PendingSkillGate{
		Skill:       sg.Skill,
		Description: sg.Description,
		Reason:      sg.Reason,
		Key:         sg.Key,
		canonical:   sg.canonical,
	}
}

func sudoAuthSnapshot(sa *PendingSudoAuth) *PendingSudoAuth {
	return &PendingSudoAuth{
		Command:     sa.Command,
		Reason:      sa.Reason,
		StepID:      sa.StepID,
		AllowUnlock: sa.AllowUnlock,
		Attempt:     sa.Attempt,
		Error:       sa.Error,
		Key:         sa.Key,
		canonical:   sa.canonical,
	}
}

// ── Claim arbitration ──────────────────────────────────────────────────

// claimForAnswer tries to settle a registered prompt with the user's answer.
// It returns won=false without claiming when a steering cancellation is
// already visible on the request's operation, or when another claim owns the
// record; wait is then non-nil for the in-flight claim to join.
func (s *State) claimForAnswer(key PromptKey) (rec *promptRecord, won bool, wait chan struct{}) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	rec = s.lookupPromptLocked(key)
	if rec == nil {
		return nil, false, nil
	}
	if rec.claimed {
		return rec, false, rec.claimDone
	}
	// Cancellation intent accepted before the answer blocks the grant: the
	// steering path owns this settlement, and no callback may run.
	if rec.ctx != nil && SteeringRequested(rec.ctx) {
		return rec, false, nil
	}
	rec.claimed = true
	return rec, true, nil
}

// claimForCancel tries to settle a registered prompt as canceled. It never
// displaces a claim that already owns the record.
func (s *State) claimForCancel(key PromptKey, cause error) (rec *promptRecord, won bool, wait chan struct{}) {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	rec = s.lookupPromptLocked(key)
	if rec == nil {
		return nil, false, nil
	}
	if rec.claimed {
		return rec, false, rec.claimDone
	}
	rec.claimed = true
	rec.cause = cause
	return rec, true, nil
}

// settlePrompt records a settled record exactly once and releases any waiter
// that was blocked on the claim. It never delivers anything.
func (s *State) settlePrompt(rec *promptRecord, cause error) {
	s.accessMu.Lock()
	s.settlePromptLocked(rec, cause)
	s.accessMu.Unlock()
}

// settlePromptLocked is settlePrompt for a caller that already holds accessMu
// — the access settlement path, which must not re-enter the gate.
func (s *State) settlePromptLocked(rec *promptRecord, cause error) {
	if rec == nil {
		return
	}
	if !rec.settled {
		rec.settled = true
		rec.cause = cause
		close(rec.done)
	}
	if rec.claimed && !rec.claimClosed {
		rec.claimClosed = true
		close(rec.claimDone)
	}
}

// joinClaim waits outside every state lock for an in-flight claim to finish,
// so a cancellation that lost can return only after the grant it must not
// roll back has completed.
func (s *State) joinClaim(wait chan struct{}) {
	if wait == nil {
		return
	}
	select {
	case <-wait:
	case <-s.Context().Done():
	}
}

// ── Settlement ─────────────────────────────────────────────────────────

// ResolveApproval settles a registered approval with the user's decision.
// The optional apply callback runs outside the state locks after the claim is
// won; a failure delivers a nonapproved decision carrying its cause. The
// returned bool reports whether this decision won: false means the key named
// no live prompt, another claim owned it, or steering had already won.
func (s *State) ResolveApproval(key PromptKey, decision UserApprovalDecision, apply func() error) (bool, error) {
	rec, won, wait := s.claimForAnswer(key)
	if rec == nil {
		return false, nil
	}
	if !won {
		s.joinClaim(wait)
		return false, nil
	}
	if apply != nil {
		if err := apply(); err != nil {
			deliverApproval(rec, UserApprovalDecision{Approved: false})
			s.settlePrompt(rec, err)
			s.clearPromptSlot(rec)
			return false, err
		}
	}
	deliverApproval(rec, decision)
	s.settlePrompt(rec, nil)
	s.clearPromptSlot(rec)
	return true, nil
}

// ResolveQuestion settles a registered question with the user's answers. A
// canceled question is settled by the cancellation path instead, so a
// steering interruption never produces a fabricated human answer here.
func (s *State) ResolveQuestion(key PromptKey, answers []Answer) bool {
	rec, won, wait := s.claimForAnswer(key)
	if rec == nil {
		return false
	}
	if !won {
		s.joinClaim(wait)
		return false
	}
	deliverQuestion(rec, answers)
	s.settlePrompt(rec, nil)
	s.clearPromptSlot(rec)
	return true
}

// ResolveSkillGate settles a registered skill-load gate. The apply callback
// carries the session-level side effects (persisting a rule) and runs only
// after the claim is won, outside the state locks.
func (s *State) ResolveSkillGate(key PromptKey, choice SkillGateChoice, apply func() error) (bool, error) {
	rec, won, wait := s.claimForAnswer(key)
	if rec == nil {
		return false, nil
	}
	if !won {
		s.joinClaim(wait)
		return false, nil
	}
	if apply != nil {
		if err := apply(); err != nil {
			// A failed effect denies the load rather than granting it: the
			// skill gate must fail closed.
			deliverSkill(rec, SkillGateDeny)
			s.settlePrompt(rec, err)
			s.clearPromptSlot(rec)
			return false, err
		}
	}
	deliverSkill(rec, choice)
	s.settlePrompt(rec, nil)
	s.clearPromptSlot(rec)
	return true, nil
}

// ResolveSudoAuth settles a registered privileged-auth prompt. A canceled
// prompt delivers an empty decision, so no password bytes are retained.
func (s *State) ResolveSudoAuth(key PromptKey, decision SudoAuthDecision) bool {
	rec, won, wait := s.claimForAnswer(key)
	if rec == nil {
		return false
	}
	if !won {
		s.joinClaim(wait)
		return false
	}
	deliverSudo(rec, decision)
	s.settlePrompt(rec, nil)
	s.clearPromptSlot(rec)
	return true
}

// CancelPrompt settles a registered prompt as interrupted, with a distinct
// cause. It claims only an unclaimed record: an answer that already won
// completes first and is never rolled back, and a late cancellation cannot
// clear a newer request. It returns whether the cancellation won.
func (s *State) CancelPrompt(key PromptKey, cause error) bool {
	if cause == nil {
		cause = ErrSendNow
	}
	rec, won, wait := s.claimForCancel(key, cause)
	if rec == nil {
		return false
	}
	if !won {
		s.joinClaim(wait)
		return false
	}
	// A typed access request settles through its own canonical path, which is
	// the one place allowed to acknowledge it. The cancellation claim above
	// already marked this record owned, so the mirror only tracks the existing
	// resolver's outcome; that resolver takes the same gate and reports the
	// cancellation itself.
	if rec.access {
		s.CancelAccess(rec.key.ID, AccessCanceled, cause.Error())
		s.settlePrompt(rec, cause)
		return true
	}
	deliverCanceledPrompt(rec, cause)
	s.settlePrompt(rec, cause)
	s.clearPromptSlot(rec)
	return true
}

// ── Access mirror ───────────────────────────────────────────────────────

// mirrorAccessPromptLocked records a typed access request in the prompt
// registry so Send now's operation-scoped cancellation can reach it through the
// same gate ResolveAccess uses. The mirror never owns settlement: `access` is
// set, so CancelPrompt routes it to CancelAccess/finishAccessLocked, which is
// the one acknowledger of a grant. Caller holds accessMu.
func (s *State) mirrorAccessPromptLocked(ctx context.Context, pending *PendingToolCall, requestID string) {
	key := PromptKey{ID: requestID, Kind: PromptApproval}
	if op, ok := OperationFromContext(ctx); ok {
		key.Operation = op
	}
	ref := &promptRef{key: key, owner: s}
	if s.prompts == nil {
		s.prompts = make(map[string]*promptRecord)
	}
	// A previous access request may still be recorded under its own ID; a new
	// one gets its own entry rather than displacing it.
	s.prompts[requestID] = &promptRecord{
		key:       key,
		ref:       ref,
		ctx:       ctx,
		done:      make(chan struct{}),
		claimDone: make(chan struct{}),
		access:    true,
		approval:  pending,
	}
}

// finishPromptMirrorLocked settles the registry mirror for an access request
// that finishAccessLocked just acknowledged. It never delivers: the canonical
// access channel was written by that helper. Caller holds accessMu.
func (s *State) finishPromptMirrorLocked(requestID string, cause error) {
	rec := s.prompts[requestID]
	if rec == nil || !rec.access {
		return
	}
	s.settlePromptLocked(rec, cause)
}

// causeForAccessStatus maps an access outcome to the cause a cancellation
// waiter should observe, so a grant reads as no cause and a cancellation as
// the steering interruption that produced it.
func causeForAccessStatus(status AccessStatus) error {
	if status == AccessCanceled || status == AccessExpired {
		return ErrSendNow
	}
	return nil
}

// settleSupersededPromptLocked settles the registry record of a prompt that a
// generic compatibility setter cleared or replaced before any answer arrived.
// It releases that prompt's waiter with the kind's cancellation sentinel so it
// cannot hang on an abandoned channel. A zero key (a legacy fixture that owns
// its own channel), an already-settled record and an in-flight claim are all
// left alone. Caller holds accessMu.
func (s *State) settleSupersededPromptLocked(key PromptKey, cause error) {
	if key.ID == "" {
		return
	}
	rec := s.prompts[key.ID]
	if rec == nil || rec.settled || rec.claimed || rec.access {
		return
	}
	rec.claimed = true
	rec.cause = cause
	deliverCanceledPrompt(rec, cause)
	s.settlePromptLocked(rec, cause)
}

// settleSupersededPrompt settles the registry record of a prompt that a generic
// compatibility setter cleared or replaced, taking the access gate itself. It
// must be called with no session lock held: it releases the prompt's waiter,
// whose wake-up may re-enter State.
func (s *State) settleSupersededPrompt(previous any, cause error) {
	if key, ok := promptKeyOf(previous); ok {
		s.accessMu.Lock()
		s.settleSupersededPromptLocked(key, cause)
		s.accessMu.Unlock()
	}
}

// promptKeyOf returns the registry key of a pending prompt, reporting false for
// nil and for an unregistered legacy fixture (the zero key).
func promptKeyOf(prompt any) (PromptKey, bool) {
	var key PromptKey
	switch p := prompt.(type) {
	case *PendingToolCall:
		if p == nil {
			return PromptKey{}, false
		}
		key = p.Key
	case *PendingQuestion:
		if p == nil {
			return PromptKey{}, false
		}
		key = p.Key
	case *PendingSkillGate:
		if p == nil {
			return PromptKey{}, false
		}
		key = p.Key
	case *PendingSudoAuth:
		if p == nil {
			return PromptKey{}, false
		}
		key = p.Key
	default:
		return PromptKey{}, false
	}
	return key, key.ID != ""
}

// PromptCause returns the actual terminal cause recorded for a key, so a
// waiter can tell a steering interruption from a denial or an unanswered
// question. It retains the cause after settlement and reports errPromptUnknown
// for a key that names no record.
func (s *State) PromptCause(key PromptKey) error {
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	rec := s.prompts[key.ID]
	if rec == nil {
		return errPromptUnknown
	}
	return rec.cause
}

// PromptContext returns the original request context recorded for a key. An
// expired key returns an already-canceled context, so a transport cannot start
// a late request against a prompt that no longer exists.
func (s *State) PromptContext(key PromptKey) context.Context {
	s.accessMu.Lock()
	rec := s.prompts[key.ID]
	s.accessMu.Unlock()
	if rec == nil || rec.ctx == nil {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(errPromptUnknown)
		return ctx
	}
	return rec.ctx
}

// ClearPrompt compare-clears both the record and the prompt slot for a key. It
// releases nothing — a caller that settles a prompt has already done so — and it
// never touches whichever prompt is now pending: an old copy's cleanup cannot
// release a newer request.
func (s *State) ClearPrompt(key PromptKey) {
	s.accessMu.Lock()
	rec := s.prompts[key.ID]
	if rec == nil || rec.key.Kind != key.Kind || rec.key.Operation != key.Operation {
		s.accessMu.Unlock()
		return
	}
	delete(s.prompts, key.ID)
	// Compare the slot against this record's own prompt while holding the gate,
	// so the comparison and the clear cannot interleave with a replacement.
	var clearedApproval, clearedQuestion, clearedSkill, clearedSudo bool
	s.mu.Lock()
	if rec.approval != nil && s.pendingApproval == rec.approval {
		s.pendingApproval = nil
		s.pendingAccessCtx = nil
		s.pendingAccessIntent = AccessIntent{}
		clearedApproval = true
	}
	if rec.question != nil && s.pendingQuestion == rec.question {
		s.pendingQuestion = nil
		clearedQuestion = true
	}
	if rec.skill != nil && s.pendingSkillGate == rec.skill {
		s.pendingSkillGate = nil
		clearedSkill = true
	}
	if rec.sudo != nil && s.pendingSudoAuth == rec.sudo {
		s.pendingSudoAuth = nil
		clearedSudo = true
	}
	s.mu.Unlock()
	s.accessMu.Unlock()

	// Publish outside the locks: a subscriber may re-enter State.
	if clearedApproval {
		s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: nil})
	}
	if clearedQuestion {
		s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: nil})
	}
	if clearedSkill {
		s.publishEvent(EventPendingSkillGateChanged, Event{PendingSkillGate: nil})
	}
	if clearedSudo {
		s.publishEvent(EventPendingSudoAuthChanged, Event{PendingSudoAuth: nil})
	}
}

// CancelOperationPrompts settles every prompt owned by exactly ref. It selects
// only exact operation matches — never a blanket shutdown cleanup, never a
// child's independent scope — grants no authority, records no sticky skill
// denial, and zeroes canceled privileged material by delivering an empty
// decision. Each cancellation waits for an accepted response claim outside the
// state locks.
func (s *State) CancelOperationPrompts(ref OperationRef) {
	if ref.ID == "" {
		return
	}
	s.accessMu.Lock()
	var keys []PromptKey
	for _, rec := range s.prompts {
		if rec.key.Operation != ref || rec.settled {
			continue
		}
		keys = append(keys, rec.key)
	}
	s.accessMu.Unlock()

	for _, key := range keys {
		s.CancelPrompt(key, ErrSendNow)
	}
}

// ── Delivery ───────────────────────────────────────────────────────────

// The delivery helpers below are the only writers of a prompt's canonical
// channel. Each sends at most one value and then closes the channel, so a
// late decision on a retained copy cannot produce a second settlement.

// sendApproval delivers one approval decision on a canonical channel and closes
// it. It is safe on a nil channel and never blocks.
func sendApproval(ch chan UserApprovalDecision, d UserApprovalDecision) {
	if ch == nil {
		return
	}
	select {
	case ch <- d:
	default:
	}
	close(ch)
}

func sendQuestion(ch chan []Answer, a []Answer) {
	if ch == nil {
		return
	}
	select {
	case ch <- a:
	default:
	}
	close(ch)
}

func sendSkill(ch chan SkillGateChoice, c SkillGateChoice) {
	if ch == nil {
		return
	}
	select {
	case ch <- c:
	default:
	}
	close(ch)
}

func sendSudo(ch chan SudoAuthDecision, d SudoAuthDecision) {
	if ch == nil {
		return
	}
	select {
	case ch <- d:
	default:
	}
	close(ch)
}

func deliverApproval(rec *promptRecord, d UserApprovalDecision) {
	if rec.approval == nil {
		return
	}
	sendApproval(rec.approval.ResponseChan, d)
}

func deliverQuestion(rec *promptRecord, answers []Answer) {
	if rec.question == nil {
		return
	}
	sendQuestion(rec.question.ResponseChan, answers)
}

func deliverSkill(rec *promptRecord, c SkillGateChoice) {
	if rec.skill == nil {
		return
	}
	sendSkill(rec.skill.ResponseChan, c)
}

func deliverSudo(rec *promptRecord, d SudoAuthDecision) {
	if rec.sudo == nil {
		return
	}
	sendSudo(rec.sudo.ResponseChan, d)
}

// deliverCanceledPrompt unblocks the waiter of a canceled prompt with the
// compatibility sentinel for its kind. The distinct cause lives on the record
// (see PromptCause), so no fabricated human answer is delivered: a canceled
// question carries the Unanswered sentinel, a canceled skill gate a
// non-sticky deny, and a canceled privileged prompt an empty decision with no
// password bytes.
func deliverCanceledPrompt(rec *promptRecord, cause error) {
	_ = cause
	switch {
	case rec.approval != nil:
		deliverApproval(rec, UserApprovalDecision{Approved: false, Canceled: true})
	case rec.question != nil:
		deliverQuestion(rec, UnansweredAnswers(rec.question.Questions))
	case rec.skill != nil:
		deliverSkill(rec, SkillGateDeny)
	case rec.sudo != nil:
		deliverSudo(rec, SudoAuthDecision{OK: false})
	}
}

// clearPromptSlot clears the prompt slot this record occupies, and only that
// one, and announces the clear. A prompt that replaced it is left alone.
func (s *State) clearPromptSlot(rec *promptRecord) {
	s.accessMu.Lock()
	switch {
	case rec.approval != nil:
		s.mu.Lock()
		match := s.pendingApproval == rec.approval
		if match {
			s.pendingApproval = nil
			s.pendingAccessCtx = nil
			s.pendingAccessIntent = AccessIntent{}
		}
		s.mu.Unlock()
		s.accessMu.Unlock()
		if match {
			s.publishEvent(EventPendingApprovalChanged, Event{PendingApproval: nil})
		}
		return
	case rec.question != nil:
		s.mu.Lock()
		match := s.pendingQuestion == rec.question
		if match {
			s.pendingQuestion = nil
		}
		s.mu.Unlock()
		s.accessMu.Unlock()
		if match {
			s.publishEvent(EventPendingQuestionChanged, Event{PendingQuestion: nil})
		}
		return
	case rec.skill != nil:
		s.mu.Lock()
		match := s.pendingSkillGate == rec.skill
		if match {
			s.pendingSkillGate = nil
		}
		s.mu.Unlock()
		s.accessMu.Unlock()
		if match {
			s.publishEvent(EventPendingSkillGateChanged, Event{PendingSkillGate: nil})
		}
		return
	case rec.sudo != nil:
		s.mu.Lock()
		match := s.pendingSudoAuth == rec.sudo
		if match {
			s.pendingSudoAuth = nil
		}
		s.mu.Unlock()
		s.accessMu.Unlock()
		if match {
			s.publishEvent(EventPendingSudoAuthChanged, Event{PendingSudoAuth: nil})
		}
		return
	}
	s.accessMu.Unlock()
}

// ── Registered-copy routing ─────────────────────────────────────────────

// resolveApprovalCopy routes a decision made on an event/display copy through
// the canonical record. A copy whose key no longer names a live request can
// neither grant nor clear anything.
func (r *promptRef) resolveApprovalCopy(d UserApprovalDecision) {
	if r == nil || r.owner == nil {
		return
	}
	r.owner.ResolveApproval(r.key, d, nil)
}

func (r *promptRef) resolveQuestionCopy(answers []Answer) {
	if r == nil || r.owner == nil {
		return
	}
	r.owner.ResolveQuestion(r.key, answers)
}

func (r *promptRef) resolveSkillCopy(choice SkillGateChoice) {
	if r == nil || r.owner == nil {
		return
	}
	r.owner.ResolveSkillGate(r.key, choice, nil)
}

func (r *promptRef) resolveSudoCopy(d SudoAuthDecision) {
	if r == nil || r.owner == nil {
		return
	}
	r.owner.ResolveSudoAuth(r.key, d)
}
