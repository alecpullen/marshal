package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrGone is returned by ResolvePermission and ResolveQuestion when the
// pending entry no longer exists (late or duplicate response). The HTTP
// layer maps it to 410 Gone.
var ErrGone = errors.New("bridge: pending request already resolved or expired")

// ErrUnknownSession is returned by session-scoped methods for ids the
// Registry does not track. The HTTP layer maps it to 404.
var ErrUnknownSession = errors.New("bridge: unknown session")

// Decision is the HTTP-facing shape of a permission decision. It maps
// 1:1 onto the ACP session/request_permission result.
type Decision struct {
	Approved bool   `json:"approved"`
	Edited   string `json:"edited,omitempty"`
}

// Answer is one question-and-response pair on the wire.
//
// This deliberately duplicates the agent-side shape rather than
// importing it: web/bridge is an external ACP client, and the JSON
// contract — not a shared Go type — is what binds the two halves. The
// duplication is what lets web/ ship as its own module.
type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Answers is the HTTP-facing shape of a question response. Declined
// maps to the ACP {declined:true} result; otherwise Answers carries one
// entry per question, in order.
type Answers struct {
	Answers  []Answer `json:"answers,omitempty"`
	Declined bool     `json:"declined,omitempty"`
}

// sessionInfo tracks one active session's bridge-side state.
type sessionInfo struct {
	// Cwd is the agent's view, because it is re-sent on delete and
	// resume rather than only recorded.
	Cwd AgentPath `json:"cwd"`
	// Busy is true while a turn is in flight (prompt sent, no turn end
	// observed). A child restart marks it interrupted (Busy=false).
	Busy bool `json:"busy"`
	// gen is bumped on each cancel so a permission/question request that
	// registers after a cancel can detect that it belongs to the
	// cancelled turn (see awaitPermission/awaitQuestion). Not serialized.
	gen uint64
	// accepting is true only while a prompt turn is active. Requests that
	// arrive after cancellation and before the next prompt are stale.
	accepting bool
	// ctx is cancelled (via cancel) when the session is cancelled or
	// deleted, draining any pending permission/question waits for it.
	// cancelSession replaces it with a fresh context afterwards so a
	// cancel does not poison later turns' permission/question requests.
	// Not serialized.
	ctx    context.Context
	cancel context.CancelFunc
}

// Registry tracks active sessions and brokers the child-initiated
// permission and question requests to the HTTP layer.
type Registry struct {
	child *Child

	// OnEvent, if set, is invoked with bridge-originated events for a
	// session (e.g. "bridge_restarted"). Task 3's event bus consumes it.
	OnEvent func(sessionId string, payload any)

	// testHookBeforeRegister, when non-nil, is invoked between a pending
	// request's generation capture and its registration. Tests use it to
	// force a cancel in the late-registration race window.
	testHookBeforeRegister func()

	// RootCwd is the default working directory for the HTTP load
	// endpoint when the request body omits cwd. The binary wiring
	// (cmd/webbridge --cwd-root) sets it; empty means load requires an
	// explicit cwd in the request body.
	RootCwd string

	mu       sync.Mutex
	sessions map[string]*sessionInfo

	permMu      sync.Mutex
	permissions map[string]chan Decision
	permSession map[string]string // toolCallId -> issuing sessionId

	quesMu      sync.Mutex
	questions   map[string]chan Answers
	quesSession map[string]string // questionId -> issuing sessionId
}

// NewRegistry wires a Registry onto a started Child, installing the
// OnRequest and OnRestart hooks.
func NewRegistry(child *Child) *Registry {
	r := &Registry{
		child:       child,
		sessions:    make(map[string]*sessionInfo),
		permissions: make(map[string]chan Decision),
		permSession: make(map[string]string),
		questions:   make(map[string]chan Answers),
		quesSession: make(map[string]string),
	}
	child.OnRequest = r.handleChildRequest
	prevOnRestart := child.OnRestart
	child.OnRestart = func() {
		if prevOnRestart != nil {
			prevOnRestart()
		}
		// resumeAll blocks on child.Request; run it off the
		// supervision goroutine so a stalled resume cannot wedge
		// restarts or Stop.
		go r.resumeAll()
	}
	return r
}

// sessionParams mirrors internal/acp sessionParams: cwd plus optional
// sessionId, used by session/new, session/load, session/resume and
// session/delete. mcpServers is required by the agent's lifecycle
// validation as an explicit empty array on session/new, session/load
// and session/resume; it is omitted on session/delete, whose validation
// does not require it.
type sessionParams struct {
	Cwd        AgentPath          `json:"cwd"`
	SessionID  string             `json:"sessionId,omitempty"`
	MCPServers *[]json.RawMessage `json:"mcpServers,omitempty"`
}

// New creates a session (session/new), or resumes an existing one when
// sessionId is non-empty. Returns the active session id.
func (r *Registry) New(ctx context.Context, cwd AgentPath, sessionId string) (string, error) {
	res, err := r.child.Request(ctx, "session/new", sessionParams{Cwd: cwd, SessionID: sessionId, MCPServers: &[]json.RawMessage{}})
	if err != nil {
		return "", err
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("bridge: decode session/new result: %w", err)
	}
	if out.SessionID == "" {
		return "", errors.New("bridge: session/new returned empty sessionId")
	}
	r.track(out.SessionID, cwd)
	return out.SessionID, nil
}

// Load attaches to an existing session (session/load) and starts
// tracking it.
func (r *Registry) Load(ctx context.Context, cwd AgentPath, id string) error {
	if _, err := r.child.Request(ctx, "session/load", sessionParams{Cwd: cwd, SessionID: id, MCPServers: &[]json.RawMessage{}}); err != nil {
		return err
	}
	r.track(id, cwd)
	return nil
}

// track registers a session with a fresh cancel context so pending
// permission/question waits can be drained on cancel/delete/disconnect.
func (r *Registry) track(id string, cwd AgentPath) {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.sessions[id] = &sessionInfo{Cwd: cwd, ctx: ctx, cancel: cancel, accepting: true}
	r.mu.Unlock()
}

// Delete removes the session record (session/delete) and stops tracking
// it, draining any pending waits for it.
func (r *Registry) Delete(ctx context.Context, id string) error {
	info, ok := r.lookup(id)
	if !ok {
		return ErrUnknownSession
	}
	if _, err := r.child.Request(ctx, "session/delete", sessionParams{Cwd: info.Cwd, SessionID: id}); err != nil {
		return err
	}
	r.cancelSession(id)
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
	return nil
}

// Prompt starts a turn (session/prompt) with a single text block. The
// ACP session/prompt request blocks until the turn completes, so the
// caller (the HTTP layer) invokes it on its own goroutine. Busy is set
// before dispatch and cleared when the turn ends, however it ends; a
// bridge-side turn_end event carries the stop reason, since ACP
// signals turn completion only via this request's response.
func (r *Registry) Prompt(ctx context.Context, id, text string) error {
	if _, ok := r.lookup(id); !ok {
		return ErrUnknownSession
	}
	params := map[string]any{
		"sessionId": id,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	}
	r.beginTurn(id)
	res, err := r.child.Request(ctx, "session/prompt", params)
	r.endTurn(id)
	if err != nil {
		r.emitEvent(id, map[string]any{"type": "turn_end", "error": err.Error()})
		return err
	}
	var out struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(res, &out)
	r.emitEvent(id, map[string]any{"type": "turn_end", "stopReason": out.StopReason})
	return nil
}

// Cancel interrupts the in-flight turn (session/cancel notification)
// and drains any pending permission/question waits for the session.
func (r *Registry) Cancel(ctx context.Context, id string) error {
	if _, ok := r.lookup(id); !ok {
		return ErrUnknownSession
	}
	r.cancelSession(id)
	return r.child.Notify("session/cancel", map[string]string{"sessionId": id})
}

// Steer redirects the in-flight turn (session/steer).
func (r *Registry) Steer(ctx context.Context, id, text string) error {
	if _, ok := r.lookup(id); !ok {
		return ErrUnknownSession
	}
	_, err := r.child.Request(ctx, "session/steer", map[string]string{"sessionId": id, "text": text})
	return err
}

// SetMode switches the session's permission mode (session/set_mode).
func (r *Registry) SetMode(ctx context.Context, id, mode string) error {
	if _, ok := r.lookup(id); !ok {
		return ErrUnknownSession
	}
	_, err := r.child.Request(ctx, "session/set_mode", map[string]string{"sessionId": id, "mode": mode})
	return err
}

// Sessions returns a snapshot of tracked sessions for the event bus and
// HTTP layer.
func (r *Registry) Sessions() map[string]sessionInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]sessionInfo, len(r.sessions))
	for id, info := range r.sessions {
		out[id] = *info
	}
	return out
}

// Pending reports whether a session is parked on an approval or question.
// Approval takes precedence if both are outstanding.
func (r *Registry) Pending(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	r.permMu.Lock()
	for _, id := range r.permSession {
		if id == sessionID {
			r.permMu.Unlock()
			return "approval"
		}
	}
	r.permMu.Unlock()

	r.quesMu.Lock()
	defer r.quesMu.Unlock()
	for _, id := range r.quesSession {
		if id == sessionID {
			return "question"
		}
	}
	return ""
}

// ResolvePermission delivers the HTTP layer's decision to a pending
// session/request_permission. Late or duplicate resolves return ErrGone.
func (r *Registry) ResolvePermission(toolCallId string, d Decision) error {
	r.permMu.Lock()
	ch, ok := r.permissions[toolCallId]
	if ok {
		delete(r.permissions, toolCallId)
		delete(r.permSession, toolCallId)
	}
	r.permMu.Unlock()
	if !ok {
		return ErrGone
	}
	ch <- d
	return nil
}

// ResolveQuestion delivers the HTTP layer's answers to a pending
// session/request_question. Late or duplicate resolves return ErrGone.
func (r *Registry) ResolveQuestion(questionId string, a Answers) error {
	r.quesMu.Lock()
	ch, ok := r.questions[questionId]
	if ok {
		delete(r.questions, questionId)
		delete(r.quesSession, questionId)
	}
	r.quesMu.Unlock()
	if !ok {
		return ErrGone
	}
	ch <- a
	return nil
}

// handleChildRequest is installed as the Child's OnRequest. It brokers
// permission and question requests to the HTTP layer; unknown methods
// get a JSON-RPC method-not-found error.
func (r *Registry) handleChildRequest(id int, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/request_permission":
		return r.awaitPermission(params)
	case "session/request_question":
		return r.awaitQuestion(params)
	default:
		return nil, fmt.Errorf("bridge: unsupported child request %q", method)
	}
}

func (r *Registry) awaitPermission(params json.RawMessage) (any, error) {
	var req struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
	}
	if err := json.Unmarshal(params, &req); err != nil || req.ToolCallID == "" {
		return map[string]any{"approved": false}, nil
	}
	// A permission must be keyed to a tracked session with a non-empty
	// id; otherwise it could never be drained by session cancellation
	// and would wait forever. Reject malformed requests immediately.
	if !r.sessionAccepting(req.SessionID) {
		return map[string]any{"approved": false}, nil
	}
	// Capture the session's generation before registering so a request
	// that arrives from a turn already being cancelled can be detected
	// as stale after registration (see the re-check below).
	gen := r.sessionGen(req.SessionID)
	if r.testHookBeforeRegister != nil {
		r.testHookBeforeRegister()
	}
	ch := make(chan Decision, 1)
	r.permMu.Lock()
	r.permissions[req.ToolCallID] = ch
	r.permSession[req.ToolCallID] = req.SessionID
	r.permMu.Unlock()
	// Clean up the pending entry on every exit path (resolve, session
	// cancel/delete, subscriber disconnect, child restart) so a waiter
	// that returns via the session context cannot leak its map entry.
	defer func() {
		r.permMu.Lock()
		delete(r.permissions, req.ToolCallID)
		delete(r.permSession, req.ToolCallID)
		r.permMu.Unlock()
	}()
	// Late-registration guard: if the session was cancelled between the
	// generation capture above and this registration, the drain already
	// ran and this request would otherwise wait forever on the fresh
	// context. Detect the bumped generation and deny immediately.
	if currentGen, accepting := r.sessionState(req.SessionID); currentGen != gen || !accepting {
		return map[string]any{"approved": false}, nil
	}
	r.emitEvent(req.SessionID, map[string]any{"type": "permission_request", "toolCallId": req.ToolCallID, "params": params})

	// Wait ends when the HTTP layer resolves the permission, when the
	// issuing session is cancelled/deleted or its SSE subscriber
	// disconnects (drainSession), or when clearPending drains it on child
	// restart — all with a deny. Browsers can still explicitly deny.
	//
	// No wall-clock timeout: a user reading and deciding is not a hung
	// request, and denying after N seconds punishes them for thinking.
	// This matches the TUI runner and ACP, which also wait forever.
	select {
	case d := <-ch:
		return map[string]any{"approved": d.Approved, "edited": d.Edited}, nil
	case <-r.sessionCtx(req.SessionID).Done():
		return map[string]any{"approved": false}, nil
	}
}

func (r *Registry) awaitQuestion(params json.RawMessage) (any, error) {
	var req struct {
		QuestionID string `json:"questionId"`
		SessionID  string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &req); err != nil || req.QuestionID == "" {
		return map[string]any{"declined": true}, nil
	}
	// A question must be keyed to a tracked session with a non-empty id;
	// otherwise it could never be drained by session cancellation and
	// would wait forever. Reject malformed requests immediately.
	if !r.sessionAccepting(req.SessionID) {
		return map[string]any{"declined": true}, nil
	}
	// Capture the session's generation before registering so a request
	// that arrives from a turn already being cancelled can be detected
	// as stale after registration (see the re-check below).
	gen := r.sessionGen(req.SessionID)
	if r.testHookBeforeRegister != nil {
		r.testHookBeforeRegister()
	}
	ch := make(chan Answers, 1)
	r.quesMu.Lock()
	r.questions[req.QuestionID] = ch
	r.quesSession[req.QuestionID] = req.SessionID
	r.quesMu.Unlock()
	// Clean up the pending entry on every exit path (resolve, session
	// cancel/delete, subscriber disconnect, child restart) so a waiter
	// that returns via the session context cannot leak its map entry.
	defer func() {
		r.quesMu.Lock()
		delete(r.questions, req.QuestionID)
		delete(r.quesSession, req.QuestionID)
		r.quesMu.Unlock()
	}()
	// Late-registration guard: if the session was cancelled between the
	// generation capture above and this registration, the drain already
	// ran and this request would otherwise wait forever on the fresh
	// context. Detect the bumped generation and decline immediately.
	if currentGen, accepting := r.sessionState(req.SessionID); currentGen != gen || !accepting {
		return map[string]any{"declined": true}, nil
	}
	r.emitEvent(req.SessionID, map[string]any{"type": "question_request", "questionId": req.QuestionID, "params": params})

	// Wait ends when the HTTP layer answers, when the issuing session is
	// cancelled/deleted or its SSE subscriber disconnects (drainSession),
	// or when clearPending drains it on child restart — all with a
	// decline. Browsers can still explicitly decline.
	//
	// No wall-clock timeout: a user reading and deciding is not a hung
	// request, and declining after N seconds punishes them for thinking.
	// This matches the TUI runner and ACP, which also wait forever.
	select {
	case a := <-ch:
		if a.Declined {
			return map[string]any{"declined": true}, nil
		}
		return map[string]any{"answers": a.Answers}, nil
	case <-r.sessionCtx(req.SessionID).Done():
		return map[string]any{"declined": true}, nil
	}
}

// sessionTracked reports whether id is a non-empty, tracked session.
func (r *Registry) sessionTracked(id string) bool {
	if id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.sessions[id]
	return ok
}

// sessionGen returns the session's current generation, or 0 when the
// session is not tracked. cancelSession bumps the generation so a
// request registering after a cancel can detect that it belongs to a
// cancelled turn.
func (r *Registry) sessionGen(id string) uint64 {
	gen, _ := r.sessionState(id)
	return gen
}

func (r *Registry) sessionState(id string) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.sessions[id]; ok {
		return info.gen, info.accepting
	}
	return 0, false
}

func (r *Registry) sessionAccepting(id string) bool {
	_, accepting := r.sessionState(id)
	return accepting
}

func (r *Registry) beginTurn(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.sessions[id]; ok {
		info.accepting = true
		info.Busy = true
	}
}

func (r *Registry) endTurn(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.sessions[id]; ok {
		info.accepting = false
		info.Busy = false
	}
}

// sessionCtx returns the cancel-scoped context for a session, or a
// background context when the session is not tracked. Pending waits
// select on it so a session cancel/delete or subscriber disconnect
// drains them. The await paths guarantee the session is tracked before
// reaching the select, so the background fallback is defensive only.
func (r *Registry) sessionCtx(id string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.sessions[id]; ok && info.ctx != nil {
		return info.ctx
	}
	return context.Background()
}

// cancelSession cancels the session's pending-wait context, bumps its
// generation, and drains its pending permission/question waits with
// deny/decline.
func (r *Registry) cancelSession(id string) {
	r.mu.Lock()
	if info, ok := r.sessions[id]; ok && info.cancel != nil {
		info.accepting = false
		info.cancel()
		// Bump the generation so a request from the cancelled turn that
		// registers after the drain below can detect it is stale (see the
		// late-registration guard in awaitPermission/awaitQuestion).
		info.gen++
		// Replace the cancelled context so a later turn's permission or
		// question requests for this session are not immediately denied.
		// Pending waits registered before the cancel already hold the old
		// (cancelled) ctx and are drained via drainSession below.
		info.ctx, info.cancel = context.WithCancel(context.Background())
	}
	r.mu.Unlock()
	r.drainSession(id)
}

// DrainSession drains the pending permission/question waits belonging to
// a session with deny/decline. It is invoked on session cancel/delete and
// wired to the event log's OnUnsubscribe so an SSE client disconnect for
// a session also unblocks its pending waits instead of leaking them. The
// event log only fires OnUnsubscribe when the last subscriber for the
// session disconnects, so a duplicate tab or reconnect does not deny
// pending requests for still-connected clients.
func (r *Registry) DrainSession(id string) {
	r.drainSession(id)
}

// drainSession unblocks every pending permission/question wait whose
// issuing session is id, with deny/decline, and removes the entries.
func (r *Registry) drainSession(id string) {
	r.permMu.Lock()
	for toolCallID, sid := range r.permSession {
		if sid == id {
			if ch, ok := r.permissions[toolCallID]; ok {
				ch <- Decision{Approved: false}
				delete(r.permissions, toolCallID)
			}
			delete(r.permSession, toolCallID)
		}
	}
	r.permMu.Unlock()

	r.quesMu.Lock()
	for questionID, sid := range r.quesSession {
		if sid == id {
			if ch, ok := r.questions[questionID]; ok {
				ch <- Answers{Declined: true}
				delete(r.questions, questionID)
			}
			delete(r.quesSession, questionID)
		}
	}
	r.quesMu.Unlock()
}

// resumeAll runs on child restart: fail any pending permission/question
// waits (their request ids died with the old generation), resume every
// tracked session, mark in-flight turns interrupted, and emit
// bridge_restarted.
func (r *Registry) resumeAll() {
	r.clearPending()

	r.mu.Lock()
	ids := make([]string, 0, len(r.sessions))
	cwds := make(map[string]AgentPath, len(r.sessions))
	for id, info := range r.sessions {
		ids = append(ids, id)
		cwds[id] = info.Cwd
		info.Busy = false // in-flight turn was interrupted by the restart
	}
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range ids {
		_, _ = r.child.Request(ctx, "session/resume", sessionParams{Cwd: cwds[id], SessionID: id, MCPServers: &[]json.RawMessage{}})
		r.emitEvent(id, map[string]any{"type": "bridge_restarted"})
	}
}

// clearPending drains both pending maps, unblocking every waiter with
// deny/decline. Used on child restart: the old generation's request ids
// are gone, so waiting (or resolving) against them is meaningless.
func (r *Registry) clearPending() {
	r.permMu.Lock()
	for id, ch := range r.permissions {
		ch <- Decision{Approved: false}
		delete(r.permissions, id)
	}
	r.permSession = make(map[string]string)
	r.permMu.Unlock()

	r.quesMu.Lock()
	for id, ch := range r.questions {
		ch <- Answers{Declined: true}
		delete(r.questions, id)
	}
	r.quesSession = make(map[string]string)
	r.quesMu.Unlock()
}

func (r *Registry) lookup(id string) (*sessionInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.sessions[id]
	return info, ok
}

func (r *Registry) setBusy(id string, busy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, ok := r.sessions[id]; ok {
		info.Busy = busy
	}
}

func (r *Registry) emitEvent(sessionId string, payload any) {
	if r.OnEvent != nil {
		r.OnEvent(sessionId, payload)
	}
}
