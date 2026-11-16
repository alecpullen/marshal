package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file owns the delivery half of plan revisions: handing an authorized
// candidate to the worker that is running, asking the affected work to pause,
// and recording only the evidence the runtime actually supplied.
//
// Three rules shape everything here. An intent is durable before anything
// external runs, so a lost response leaves an intent rather than a mystery. A
// state records what was observed: a receipt the runtime emitted, or the
// absence of one, never an inference from an idle-looking session. And nothing
// is ever resent automatically — an outcome this bridge cannot prove stays
// `uncertain` until a user reconciles it, because a second delivery into a turn
// that already took the first is a silent duplicate.

// revisionSteerDeliveryNow is session/steer's "Send now" delivery. Queued
// steering is the absent delivery, which is why an ordinary Registry.Steer call
// stays what it was and produces no receipt this file would record.
const revisionSteerDeliveryNow = "now"

// revisionSteeringReceiptLimit bounds how many requests one session's receipt
// store keeps. A receipt is only ever evidence for the request it names, and a
// request whose entry was evicted is unproven rather than resent.
const revisionSteeringReceiptLimit = 8

// revisionSteeringUpdateLimit bounds the steering_receipt notification this
// bridge will parse, so an oversized payload is dropped at the boundary rather
// than held.
const revisionSteeringUpdateLimit = 64 << 10

// RevisionSteeringState is session/steering_status's answer: the Send now
// snapshot of a session's running turn, reduced to what a delivery needs.
//
// It mirrors acpwire.SteeringState field for field, as a bridge-local copy:
// web/ is an external ACP client and must not import internal/....
type RevisionSteeringState struct {
	TurnID    string `json:"turnId"`
	Active    bool   `json:"active"`
	Supported bool   `json:"supported"`
}

// RevisionSteeringReceipt is one observed Send now receipt. The runtime's
// receipt does not carry the candidate hash: the journal's request-to-hash
// binding is what makes it evidence for a proposal.
type RevisionSteeringReceipt struct {
	Version   uint64 `json:"version"`
	RequestID string `json:"requestId"`
	TurnID    string `json:"turnId"`
	Status    string `json:"status"`
}

// revisionSteeringReceiptStatuses are the statuses a receipt may contribute as
// delivery evidence. A value this bridge does not know is dropped rather than
// stored: an unknown status is not a state.
var revisionSteeringReceiptStatuses = map[string]bool{
	"accepted": true, "interrupting": true, "waiting_safe_boundary": true,
	"waiting_for_resume": true, "delivered": true, "undelivered": true,
}

// RevisionSteeringStatus reads a session's live Send now snapshot
// (session/steering_status). It is safe on an idle session: an inactive
// snapshot is the honest answer, not an error.
func (r *Registry) RevisionSteeringStatus(ctx context.Context, sessionID string) (RevisionSteeringState, error) {
	res, err := r.call(ctx, sessionID, "session/steering_status", "steering_status", nil)
	if err != nil {
		return RevisionSteeringState{}, err
	}
	var out RevisionSteeringState
	if err := json.Unmarshal(res, &out); err != nil {
		return RevisionSteeringState{}, fmt.Errorf("bridge: decode session/steering_status result: %w", err)
	}
	return out, nil
}

// RevisionSendNow submits one Send now request (session/steer with
// delivery:"now") against an explicit turn and request identity.
//
// The reply is a receipt, so it is recorded like any other observation: the
// runtime answered, and that answer is evidence for this request and no other.
// An error means the request may or may not have been taken — the caller
// records that as uncertain rather than retrying.
func (r *Registry) RevisionSendNow(ctx context.Context, sessionID, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	if _, ok := r.lookup(sessionID); !ok {
		return RevisionSteeringReceipt{}, ErrUnknownSession
	}
	if strings.TrimSpace(turnID) == "" || strings.TrimSpace(requestID) == "" {
		return RevisionSteeringReceipt{}, errors.New("bridge: send now requires a turn and a request identity")
	}
	params := map[string]any{
		"sessionId": sessionID,
		"turnId":    turnID,
		"requestId": requestID,
		"delivery":  revisionSteerDeliveryNow,
	}
	if text != "" {
		params["text"] = text
	}
	res, err := r.child.Request(ctx, "session/steer", params)
	if err != nil {
		return RevisionSteeringReceipt{}, err
	}
	var out RevisionSteeringReceipt
	if err := json.Unmarshal(res, &out); err != nil {
		return RevisionSteeringReceipt{}, fmt.Errorf("bridge: decode session/steer result: %w", err)
	}
	r.observeRevisionReceipt(sessionID, out)
	return out, nil
}

// observeSteeringReceiptUpdate applies one session/update notification. It runs
// on the child's read goroutine, before any request read after it.
func (r *Registry) observeSteeringReceiptUpdate(params json.RawMessage) {
	if len(params) == 0 || len(params) > revisionSteeringUpdateLimit {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind      string `json:"kind"`
			Version   uint64 `json:"version"`
			RequestID string `json:"requestId"`
			TurnID    string `json:"turnId"`
			Status    string `json:"status"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil || p.Update.Kind != "steering_receipt" {
		return
	}
	// An untracked session is not this bridge's to record.
	if _, ok := r.lookup(p.SessionID); !ok {
		return
	}
	r.observeRevisionReceipt(p.SessionID, RevisionSteeringReceipt{
		Version:   p.Update.Version,
		RequestID: p.Update.RequestID,
		TurnID:    p.Update.TurnID,
		Status:    p.Update.Status,
	})
}

// observeRevisionReceipt stores one observed receipt, newest version wins per
// request. A receipt is kept under its own request identity, so a newer request
// in the same session never replaces an older one's evidence.
func (r *Registry) observeRevisionReceipt(sessionID string, rec RevisionSteeringReceipt) {
	if sessionID == "" || rec.RequestID == "" || strings.TrimSpace(rec.Status) == "" {
		return
	}
	if !revisionSteeringReceiptStatuses[rec.Status] {
		return
	}
	r.steerMu.Lock()
	defer r.steerMu.Unlock()
	if r.steerReceipt == nil {
		r.steerReceipt = make(map[string]map[string]RevisionSteeringReceipt)
		r.steerOrder = make(map[string][]string)
	}
	byRequest := r.steerReceipt[sessionID]
	if byRequest == nil {
		byRequest = make(map[string]RevisionSteeringReceipt)
		r.steerReceipt[sessionID] = byRequest
	}
	if held, ok := byRequest[rec.RequestID]; ok && held.Version > rec.Version {
		// An older receipt is not newer evidence.
		return
	}
	byRequest[rec.RequestID] = rec
	order := r.steerOrder[sessionID]
	known := false
	for _, id := range order {
		if id == rec.RequestID {
			known = true
			break
		}
	}
	if !known {
		order = append(order, rec.RequestID)
		for len(order) > revisionSteeringReceiptLimit {
			delete(byRequest, order[0])
			order = order[1:]
		}
	}
	r.steerOrder[sessionID] = order
}

// revisionReceipt reads the newest observed receipt for one request. An absent
// entry means no receipt was observed, which is not the same as a receipt that
// said nothing happened.
func (r *Registry) revisionReceipt(sessionID, requestID string) (RevisionSteeringReceipt, bool) {
	if sessionID == "" || requestID == "" {
		return RevisionSteeringReceipt{}, false
	}
	r.steerMu.Lock()
	defer r.steerMu.Unlock()
	rec, ok := r.steerReceipt[sessionID][requestID]
	return rec, ok
}

// factoryRevisionRuntime is the runtime seam a delivery reaches: the session
// running one item's worker, its Send now snapshot, one Send now submission,
// the session's existing cancel path, and the receipts observed for a request.
//
// It is optional. A bridge whose effects adapter offers none records an
// uncertain outcome rather than pretending to have sent something.
type factoryRevisionRuntime interface {
	// RevisionSession names the live session running the item's worker, so the
	// journal records the session the intent was aimed at.
	RevisionSession(ctx context.Context, item FactoryItem) (string, error)
	RevisionSteeringStatus(ctx context.Context, item FactoryItem) (RevisionSteeringState, error)
	RevisionSendNow(ctx context.Context, item FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error)
	RevisionPause(ctx context.Context, item FactoryItem) error
	RevisionReceipt(item FactoryItem, requestID string) (RevisionSteeringReceipt, bool)
}

// revisionRuntime is the seam this controller's effects adapter offers, if any.
func (c *FactoryController) revisionRuntime() (factoryRevisionRuntime, bool) {
	if c.effects == nil {
		return nil, false
	}
	seam, ok := c.effects.(factoryRevisionRuntime)
	return seam, ok
}

// The effects adapter forwards the seam to the worker adapter, which owns the
// fleet lookup that resolves the item's live worker session.
func (f *factoryEffects) RevisionSession(ctx context.Context, item FactoryItem) (string, error) {
	if f == nil || f.workers == nil {
		return "", fmt.Errorf("%w: no worker runtime", ErrFactoryRefused)
	}
	return f.workers.RevisionSession(ctx, item)
}

func (f *factoryEffects) RevisionSteeringStatus(ctx context.Context, item FactoryItem) (RevisionSteeringState, error) {
	if f == nil || f.workers == nil {
		return RevisionSteeringState{}, fmt.Errorf("%w: no worker runtime", ErrFactoryRefused)
	}
	return f.workers.RevisionSteeringStatus(ctx, item)
}

func (f *factoryEffects) RevisionSendNow(ctx context.Context, item FactoryItem, turnID, requestID, text string) (RevisionSteeringReceipt, error) {
	if f == nil || f.workers == nil {
		return RevisionSteeringReceipt{}, fmt.Errorf("%w: no worker runtime", ErrFactoryRefused)
	}
	return f.workers.RevisionSendNow(ctx, item, turnID, requestID, text)
}

func (f *factoryEffects) RevisionPause(ctx context.Context, item FactoryItem) error {
	if f == nil || f.workers == nil {
		return fmt.Errorf("%w: no worker runtime", ErrFactoryRefused)
	}
	return f.workers.RevisionPause(ctx, item)
}

func (f *factoryEffects) RevisionReceipt(item FactoryItem, requestID string) (RevisionSteeringReceipt, bool) {
	if f == nil || f.workers == nil {
		return RevisionSteeringReceipt{}, false
	}
	return f.workers.RevisionReceipt(item, requestID)
}

// factoryRevisionPauseRequestID is the journal identity of one proposal's pause
// request. It is derived from the item and the proposal, so a pass that repeats
// a lost request names the same intent instead of a second one.
func factoryRevisionPauseRequestID(itemID, proposalID string) string {
	return "rev-pause-" + factoryDigest([]byte(itemID+"\x00"+proposalID))
}

// factoryRevisionSteerRequestID is the journal identity of one proposal's Send
// now request: the stable request ID a receipt is correlated against.
func factoryRevisionSteerRequestID(itemID, proposalID string) string {
	return "rev-steer-" + factoryDigest([]byte(itemID+"\x00"+proposalID))
}

// factoryRevisionAffectedEffect returns the effect a proposal's assessment named
// as affected, if this item still carries it.
func factoryRevisionAffectedEffect(i FactoryItem, p FactoryRevisionProposal) *FactoryEffect {
	if p.Assessment == nil || strings.TrimSpace(p.Assessment.AffectedEffectID) == "" {
		return nil
	}
	for n := range i.Effects {
		if i.Effects[n].ID == p.Assessment.AffectedEffectID {
			return &i.Effects[n]
		}
	}
	return nil
}

// factoryRevisionAffectedEffectID names the effect a pause is tied to.
func factoryRevisionAffectedEffectID(p FactoryRevisionProposal) string {
	if p.Assessment == nil {
		return ""
	}
	return p.Assessment.AffectedEffectID
}

// factoryRevisionDeliveryStrategy is the strategy a proposal's delivery acts on:
// the user's chosen one once a choice exists, and the assessment's
// recommendation before that.
func factoryRevisionDeliveryStrategy(p FactoryRevisionProposal) string {
	if p.Delivery != nil && p.Delivery.Strategy != "" {
		return p.Delivery.Strategy
	}
	if p.Assessment != nil {
		return p.Assessment.Strategy
	}
	return ""
}

// factoryRevisionPauseTarget finds the proposal that asks the affected work to
// stop before it is authorized. Only work the assessment named is ever paused,
// and only while that effect is actually dispatched.
func factoryRevisionPauseTarget(i FactoryItem) (int, bool) {
	for n := range i.Proposals {
		p := i.Proposals[n]
		if p.PauseState != "" || p.Status != "proposed" && p.Status != "awaiting_authorization" {
			continue
		}
		e := factoryRevisionAffectedEffect(i, p)
		if e == nil || e.Status != "dispatched" {
			continue
		}
		if factoryRevisionDeliveryStrategy(p) != "interrupt" {
			continue
		}
		return n, true
	}
	return -1, false
}

// factoryRevisionPauseStopping finds the proposal whose pause was asked for and
// waits for the runtime to prove the work stopped.
func factoryRevisionPauseStopping(i FactoryItem) (int, bool) {
	for n := range i.Proposals {
		if i.Proposals[n].PauseState == "stopping" {
			return n, true
		}
	}
	return -1, false
}

// factoryRevisionWorkPaused reports whether any proposal holds this item's work.
// A paused or requested pause is not idle work: the scheduler dispatches
// nothing for the item until a user resumes it.
func factoryRevisionWorkPaused(i FactoryItem) bool {
	for _, p := range i.Proposals {
		if p.PauseState != "" {
			return true
		}
	}
	return false
}

// revisionDelegated reports whether the project delegates plan-revision handling
// to a designated planner client right now. A planner may pause affected work on
// its own account only under that delegation; with it off the recommendation is
// recorded and the user asks.
func (c *FactoryController) revisionDelegated(projectRoot string) bool {
	if c.projectSettings == nil {
		return false
	}
	ps, ok := c.projectSettings(projectRoot)
	if !ok {
		return false
	}
	return ps.PlannerDelegationEnabled && strings.TrimSpace(ps.DesignatedMCPClientID) != ""
}

// advanceRevisionPauses journals and confirms the pauses proposals asked for.
// One pass either requests a pause or settles an outstanding one, never both, so
// the request is durably visible before anything is cancelled.
func (c *FactoryController) advanceRevisionPauses(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	for _, listed := range c.store.List() {
		if err := c.advanceRevisionPause(ctx, listed.ID); err != nil {
			if errors.Is(err, ErrFactoryConflict) {
				continue
			}
			return err
		}
	}
	return nil
}

func (c *FactoryController) advanceRevisionPause(ctx context.Context, itemID string) error {
	item, err := c.store.Get(itemID)
	if err != nil {
		return err
	}
	if n, ok := factoryRevisionPauseTarget(item); ok {
		proposal := item.Proposals[n]
		// The planner's own recommendation is acted on only while its project
		// delegation is in force; a strategy the user chose is the user asking.
		if proposal.Delivery == nil && !c.revisionDelegated(item.ProjectRoot) {
			return nil
		}
		return c.requestRevisionPause(ctx, item, n)
	}
	n, ok := factoryRevisionPauseStopping(item)
	if !ok {
		return nil
	}
	obs, err := c.effects.Observe(ctx, item)
	if err != nil || !obs.RuntimeKnown || !obs.TurnIdle || !obs.ActiveWorkKnown || !obs.WorkIdle {
		// A cancellation request, a detached command or an unobserved runtime is
		// not proof that the work stopped, so the pause stays unconfirmed.
		return nil
	}
	if e := factoryRevisionAffectedEffect(item, item.Proposals[n]); e != nil && e.Status == "dispatched" {
		return nil
	}
	return c.markRevisionPaused(item, n)
}

// requestRevisionPause journals the pause intent before calling the existing
// session/cancel path. The intent names the pause request, the affected effect
// and the turn, so the cancellation is attributable to this proposal rather
// than to the factory's terminal stop.
func (c *FactoryController) requestRevisionPause(ctx context.Context, item FactoryItem, n int) error {
	proposal := item.Proposals[n]
	seam, ok := c.revisionRuntime()
	if !ok {
		_, err := c.journalRevisionPause(item, n, "uncertain", "",
			"the affected work was asked to pause and no runtime is reachable to confirm it")
		return err
	}
	turn := ""
	if state, err := seam.RevisionSteeringStatus(ctx, item); err == nil {
		turn = state.TurnID
	}
	requested, err := c.journalRevisionPause(item, n, "requested", turn,
		"the affected work was asked to pause")
	if err != nil {
		return err
	}
	if proposal.PauseState == "" && requested.Proposals[n].PauseState != "requested" {
		return nil
	}
	if err := seam.RevisionPause(ctx, requested); err != nil {
		// A refusal or a transport loss is not a stop: the pause stays
		// uncertain and is resolved by the existing explicit recovery.
		_, jerr := c.journalRevisionPause(requested, n, "uncertain", turn,
			"the pause request could not be delivered: "+err.Error())
		return jerr
	}
	_, err = c.journalRevisionPause(requested, n, "stopping", turn,
		"the pause request was sent and the work is not yet confirmed stopped")
	return err
}

// journalRevisionPause records one pause state on the proposal, tied to the
// affected effect and the turn it was observed in.
func (c *FactoryController) journalRevisionPause(item FactoryItem, n int, state, turn, message string) (FactoryItem, error) {
	if n >= len(item.Proposals) {
		return item, ErrFactoryConflict
	}
	proposal := item.Proposals[n]
	now := c.now().UTC()
	if state == "requested" || state == "uncertain" {
		message = message + " (" + factoryRevisionPauseRequestID(item.ID, proposal.ID) + ")"
	}
	if turn != "" {
		message = message + " during turn " + turn
	}
	return c.internalUpdate(item, "revision_pause_"+state, func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		if p.PauseState == state {
			return ErrFactoryConflict
		}
		p.PauseState = state
		if p.PauseRequestID == "" {
			p.PauseRequestID = factoryRevisionPauseRequestID(dst.ID, p.ID)
		}
		p.Version++
		p.UpdatedAt = now
		dst.History = append(dst.History, FactoryEvent{
			At: now, Kind: "revision_pause_" + state,
			EffectID: factoryRevisionAffectedEffectID(*p), Message: message,
		})
		return nil
	})
}

// markRevisionPaused confirms a pause only after the independent observation
// proved the turn and the active work idle. The worker keeps its checkout: a
// pause is not a stop, and nothing is parked or discarded.
func (c *FactoryController) markRevisionPaused(item FactoryItem, n int) error {
	proposal := item.Proposals[n]
	now := c.now().UTC()
	_, err := c.internalUpdate(item, "revision_pause_paused", func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID || dst.Proposals[n].PauseState != "stopping" {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		p.PauseState = "paused"
		p.Version++
		p.UpdatedAt = now
		dst.History = append(dst.History, FactoryEvent{
			At: now, Kind: "revision_pause_paused",
			EffectID: factoryRevisionAffectedEffectID(*p),
			Message:  "the affected work is observed idle; the pause is confirmed and the checkout is retained",
		})
		return nil
	})
	if errors.Is(err, ErrFactoryConflict) {
		return nil
	}
	return err
}

// deliverAuthorizedRevisions advances the delivery evidence of every authorized
// amendment. It dispatches nothing itself: an interrupt is one Send now
// request, and the safe-boundary activation stays in its own gate.
func (c *FactoryController) deliverAuthorizedRevisions(ctx context.Context) error {
	if c.store == nil {
		return nil
	}
	for _, listed := range c.store.List() {
		if err := c.deliverAuthorizedRevision(ctx, listed.ID); err != nil {
			if errors.Is(err, ErrFactoryConflict) {
				continue
			}
			return err
		}
	}
	return nil
}

// factoryRevisionDeliveryPending finds the authorized amendment whose delivery
// evidence is still moving. A settled state (delivered, undelivered, uncertain)
// is terminal for the attempt: only a new explicit choice starts another.
func factoryRevisionDeliveryPending(i FactoryItem) (int, bool) {
	for n := range i.Proposals {
		p := i.Proposals[n]
		if p.Status != "authorized" || p.Routing != "amend" || p.Delivery == nil {
			continue
		}
		switch p.Delivery.State {
		case "delivered", "undelivered", "uncertain":
			continue
		}
		return n, true
	}
	return -1, false
}

func (c *FactoryController) deliverAuthorizedRevision(ctx context.Context, itemID string) error {
	item, err := c.store.Get(itemID)
	if err != nil {
		return err
	}
	n, ok := factoryRevisionDeliveryPending(item)
	if !ok {
		return nil
	}
	if item.Proposals[n].PauseState != "" {
		// A pause holds delivery too: a paused item's work resumes on the
		// user's explicit word, not on a steer into a stopped turn.
		return nil
	}
	switch item.Proposals[n].Delivery.Strategy {
	case "hold":
		// The user chose to leave delivery pending. Nothing is sent and no
		// fallback is implied.
		return nil
	case "wait_safe":
		if item.Proposals[n].Delivery.State != "pending" {
			return nil
		}
		// No steer is sent and no receipt is invented: the revision reaches the
		// worker in the next normal factory prompt, after activation.
		return c.journalRevisionDelivery(item, n, "waiting_safe_boundary",
			"the revision waits for the safe boundary; it is supplied by the next factory prompt")
	case "interrupt":
		return c.interruptRevisionDelivery(ctx, item, n)
	}
	return nil
}

// interruptRevisionDelivery records the full intent, reads the active turn and
// only then sends Send now — and only while the target still matches.
func (c *FactoryController) interruptRevisionDelivery(ctx context.Context, item FactoryItem, n int) error {
	d := item.Proposals[n].Delivery
	seam, ok := c.revisionRuntime()
	switch d.State {
	case "dispatching":
		// A recorded intent whose outcome was never journalled: a restart or a
		// disconnect. A receipt the runtime did emit may still be in hand — the
		// pass that sent it may have lost its journal write — so it is consulted
		// before the outcome is called unknown. Either way it is never sent
		// again.
		if ok {
			if rec, observed := seam.RevisionReceipt(item, d.RequestID); observed {
				return c.applyRevisionReceipt(item, n, rec)
			}
		}
		return c.journalRevisionDelivery(item, n, "uncertain",
			"the Send now intent was recorded and its outcome is unknown; reconcile before delivering again")
	case "accepted", "interrupting", "waiting_safe_boundary", "waiting_for_resume":
		if !ok {
			return c.journalRevisionDelivery(item, n, "uncertain",
				"the runtime is unreachable, so the recorded Send now receipt is the last evidence")
		}
		rec, observed := seam.RevisionReceipt(item, d.RequestID)
		if !observed {
			// No newer receipt was observed. That is not a failure and not a
			// delivery: the recorded state stands.
			return nil
		}
		return c.applyRevisionReceipt(item, n, rec)
	}
	if d.State != "pending" {
		return nil
	}
	if !ok {
		return c.journalRevisionDelivery(item, n, "uncertain",
			"no runtime is reachable to deliver the revision")
	}
	state, err := seam.RevisionSteeringStatus(ctx, item)
	if err != nil {
		return c.journalRevisionDelivery(item, n, "uncertain",
			"the runtime cannot report an active turn: "+err.Error())
	}
	if !state.Active {
		return c.journalRevisionDelivery(item, n, "waiting_safe_boundary",
			"no turn is running to steer; the revision waits for the next factory prompt")
	}
	if !state.Supported {
		return c.journalRevisionDelivery(item, n, "uncertain",
			"the runtime does not support Send now for this turn")
	}
	session, err := seam.RevisionSession(ctx, item)
	if err != nil {
		return c.journalRevisionDelivery(item, n, "uncertain",
			"the worker session cannot be resolved: "+err.Error())
	}
	intent, err := c.journalRevisionDeliveryIntent(item, n, session, state.TurnID)
	if err != nil {
		return err
	}
	// Read the target again: Send now is only sent while it still matches.
	fresh, err := seam.RevisionSteeringStatus(ctx, intent)
	if err != nil {
		return c.journalRevisionDelivery(intent, n, "uncertain",
			"the target turn could not be re-read before Send now: "+err.Error())
	}
	if !fresh.Active || !fresh.Supported || fresh.TurnID != state.TurnID {
		return c.journalRevisionDelivery(intent, n, "undelivered",
			"the target turn changed before Send now could be sent, so nothing was sent")
	}
	requestID := intent.Proposals[n].Delivery.RequestID
	text := factoryRevisionSteerText(intent, intent.Proposals[n])
	receipt, err := seam.RevisionSendNow(ctx, intent, state.TurnID, requestID, text)
	if err != nil {
		return c.journalRevisionDelivery(intent, n, "uncertain",
			"the Send now request was dispatched and its outcome is unknown: "+err.Error())
	}
	if receipt.RequestID == "" {
		return c.journalRevisionDelivery(intent, n, "uncertain",
			"the Send now request was accepted and no receipt was observed")
	}
	return c.applyRevisionReceipt(intent, n, receipt)
}

// journalRevisionDeliveryIntent records the full delivery intent before the
// runtime is asked to accept anything.
func (c *FactoryController) journalRevisionDeliveryIntent(item FactoryItem, n int, sessionID, turnID string) (FactoryItem, error) {
	if n >= len(item.Proposals) {
		return item, ErrFactoryConflict
	}
	proposal := item.Proposals[n]
	now := c.now().UTC()
	requestID := factoryRevisionSteerRequestID(item.ID, proposal.ID)
	return c.internalUpdate(item, "revision_delivery_intent", func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		if p.Status != "authorized" || p.Delivery == nil || p.Delivery.Strategy != "interrupt" || p.Delivery.State != "pending" {
			return ErrFactoryConflict
		}
		if p.Delivery.CandidateHash != p.CandidateHash {
			return ErrFactoryConflict
		}
		p.Delivery.State = "dispatching"
		p.Delivery.SessionID = sessionID
		p.Delivery.TurnID = turnID
		p.Delivery.RequestID = requestID
		p.Version++
		p.UpdatedAt = now
		dst.History = append(dst.History, FactoryEvent{
			At: now, Kind: "revision_delivery_intent", EffectID: p.ID,
			Message: "Send now intent recorded for turn " + turnID + " (" + requestID + ")",
		})
		return nil
	})
}

// applyRevisionReceipt records one receipt as delivery evidence, and only when
// it names the journaled intent: a receipt for another request, another turn or
// another session is not this proposal's evidence.
func (c *FactoryController) applyRevisionReceipt(item FactoryItem, n int, rec RevisionSteeringReceipt) error {
	if n >= len(item.Proposals) {
		return nil
	}
	d := item.Proposals[n].Delivery
	if d == nil {
		return nil
	}
	if rec.RequestID == "" || rec.RequestID != d.RequestID {
		return nil
	}
	// The receipt must name the turn the intent named. The wire always carries
	// one, so a receipt without it cannot be correlated and is not evidence.
	if d.TurnID != "" && rec.TurnID != d.TurnID {
		return nil
	}
	if !revisionSteeringReceiptStatuses[rec.Status] || rec.Status == d.State {
		return nil
	}
	// "delivered" means the runtime took the input. It is not proof the agent
	// followed it, and it never activates a revision on its own.
	return c.journalRevisionDelivery(item, n, rec.Status, "runtime receipt: "+rec.Status)
}

// journalRevisionDelivery records one observed delivery state.
func (c *FactoryController) journalRevisionDelivery(item FactoryItem, n int, state, message string) error {
	if n >= len(item.Proposals) {
		return ErrFactoryConflict
	}
	proposal := item.Proposals[n]
	now := c.now().UTC()
	_, err := c.internalUpdate(item, "revision_delivery_"+state, func(dst *FactoryItem) error {
		if n >= len(dst.Proposals) || dst.Proposals[n].ID != proposal.ID {
			return ErrFactoryConflict
		}
		p := &dst.Proposals[n]
		if p.Delivery == nil || !factoryRevisionDeliveryStates[state] {
			return ErrFactoryConflict
		}
		if p.Delivery.State == state {
			return ErrFactoryConflict
		}
		p.Delivery.State = state
		p.Version++
		p.UpdatedAt = now
		dst.History = append(dst.History, FactoryEvent{
			At: now, Kind: "revision_delivery_" + state, EffectID: p.ID, Message: message,
		})
		return nil
	})
	return err
}

// factoryRevisionSteerText is the direction handed to the running turn. It names
// the candidate hash so the text the runtime receives and the journal's evidence
// name the same bytes.
func factoryRevisionSteerText(i FactoryItem, p FactoryRevisionProposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Approved plan revision %d for factory item %s (candidate %s).\n", p.BaseRevision+1, i.ID, p.CandidateHash)
	fmt.Fprintf(&b, "Task: %s\nApproved plan:\n%s\nApproved specification:\n%s\n", i.Title, p.Plan, p.Spec)
	b.WriteString("The previous plan no longer applies. Continue this task under the approved revision above; do not push or merge.\n")
	return b.String()
}

// activateRevisionAtSafeBoundary is the tick's activation entry. It keeps the
// single safe-boundary gate in one place and only adds the delivery slice's two
// preconditions: no unresolved pause holds the item, and the delivery evidence
// is settled rather than still pending or unproven.
func (c *FactoryController) activateRevisionAtSafeBoundary(ctx context.Context) error {
	if c.store == nil || c.effects == nil {
		return nil
	}
	for _, listed := range c.store.List() {
		n, ok := factoryRevisionActivationPending(listed)
		if !ok {
			continue
		}
		proposal := listed.Proposals[n]
		if proposal.PauseState != "" || !factoryRevisionDeliverySettled(proposal) {
			continue
		}
		if err := c.activateAuthorizedRevision(ctx, listed.ID); err != nil {
			if errors.Is(err, ErrFactoryConflict) {
				continue
			}
			return err
		}
	}
	return nil
}

// factoryRevisionDeliverySettled reports whether a proposal's delivery evidence
// allows the approval to move at the safe boundary. Pending work, a known
// non-delivery and an unproven outcome all keep the old approval active.
func factoryRevisionDeliverySettled(p FactoryRevisionProposal) bool {
	d := p.Delivery
	if d == nil {
		return true
	}
	switch d.State {
	case "pending", "dispatching", "undelivered", "uncertain":
		return false
	}
	return true
}

// factoryRevisionDelivery is factoryApplyRevisionDeliveryChoice with the one
// addition this slice owns: a delivery choice naming a paused proposal is the
// user's explicit resume, and it is the only thing that lifts a pause.
func factoryApplyRevisionDelivery(i *FactoryItem, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	if r := c.Revision; r != nil && strings.TrimSpace(r.ProposalID) != "" {
		if n, ok := factoryRevisionProposalByID(*i, r.ProposalID); ok && i.Proposals[n].PauseState != "" {
			return factoryRevisionResume(i, n, c, p, now)
		}
	}
	return factoryApplyRevisionDeliveryChoice(i, c, p, now)
}

// factoryRevisionResume clears the recorded pause and records who resumed it.
// A superseded or rejected proposal resumes its worker the same way: neither
// action silently restarts work, and neither leaves it silently stopped.
func factoryRevisionResume(i *FactoryItem, n int, c FactoryCommand, p *factoryPrincipal, now time.Time) error {
	if p != nil {
		return fmt.Errorf("%w: a paused worker is resumed by an authenticated Studio user", errFactoryDenied)
	}
	proposal := &i.Proposals[n]
	r := c.Revision
	if r.ProposalVersion == 0 || r.ProposalVersion != proposal.Version {
		return fmt.Errorf("%w: proposal %s is at version %d", ErrFactoryConflict, proposal.ID, proposal.Version)
	}
	if r.CandidateHash != "" && r.CandidateHash != proposal.CandidateHash {
		return fmt.Errorf("%w: the resume does not name proposal %s's candidate", ErrFactoryConflict, proposal.ID)
	}
	resumed := proposal.PauseRequestID
	proposal.PauseState = ""
	proposal.PauseRequestID = ""
	proposal.Version++
	proposal.UpdatedAt = now
	i.History = append(i.History, FactoryEvent{
		At: now, Kind: "revision_resumed", EffectID: proposal.ID,
		Message: "the paused worker was resumed explicitly after pause " + resumed,
	})
	// A resume that also names a strategy is one edit: the choice applies to
	// the version the resume just wrote.
	switch proposal.Status {
	case "proposed", "awaiting_authorization", "authorized":
	default:
		return nil
	}
	next := *r
	next.ProposalVersion = proposal.Version
	cmd := c
	cmd.Revision = &next
	return factoryApplyRevisionDeliveryChoice(i, cmd, p, now)
}
