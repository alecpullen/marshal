package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// This file drives a configured ACP adapter (an agent speaking ACP v1 over
// newline-delimited JSON-RPC on stdio) as a fleet worker. It is the one place
// that knows the adapter's wire shapes: the handshake, the negotiated
// capabilities, the session's mode list and the access mode the planner asked
// for. Everything it observes leaves through the instance-owned sinks the
// WorkerBackend seam declares, so the planner journals it and the browser
// renders it without the backend keeping state of its own.
//
// The adapter's own answer is the source of truth throughout: capabilities
// that the handshake did not advertise are absent rather than assumed, and the
// mode list is the session's, not a guess about what the adapter supports.

// ACP v1 methods this backend drives.
const (
	acpMethodInitialize     = "initialize"
	acpMethodSessionNew     = "session/new"
	acpMethodSessionSetMode = "session/set_mode"
	acpMethodSessionPrompt  = "session/prompt"
	acpMethodSessionCancel  = "session/cancel"
	acpMethodSessionUpdate  = "session/update"
)

// Methods the adapter may send the client and this backend forwards to the
// planner's request sink: a permission request and an elicitation (a question).
const (
	acpMethodRequestPermission = "session/request_permission"
	acpMethodRequestQuestion   = "session/request_question"
	acpMethodElicit            = "session/elicit"
	acpMethodElicitationCreate = "elicitation/create"
)

// Bounds on the JSON an event may carry. A turn's text is the transcript, so
// it gets a generous cap; an unmodelled update is opaque payload, so it gets a
// small one. Both truncate with a marker rather than dropping the event.
const (
	acpTextEventLimit   = 64 << 10
	acpOpaqueEventLimit = 8 << 10
)

// Event kinds the backend emits. They are stable strings: the planner journals
// them and the browser switches on them.
const (
	workerEventSessionStarted = "session.started"
	workerEventSessionMode    = "session.mode"
	workerEventTurnText       = "turn.text"
	workerEventTurnTool       = "turn.tool"
	workerEventTurnPermission = "turn.permission"
	workerEventTurnQuestion   = "turn.question"
	workerEventTurnUnknown    = "turn.unknown"
	workerEventTurnCancelled  = "turn.cancelled"
	workerEventTurnUncertain  = "turn.uncertain"
	workerEventUnsupported    = "unsupported.request"
)

// acpWorkerBackend is one adapter process driven as a WorkerBackend. The
// transport is supplied by the caller, so the backend never spawns
// `marshal acp` itself; it owns the Child that frames the process and its own
// copy of the sinks. Nothing here is package-global.
type acpWorkerBackend struct {
	transport agentTransport
	events    workerEventSink
	requests  workerRequestSink
	child     *Child

	// startMu serializes the child's start and the initialize handshake, so
	// concurrent Start calls handshake once. It is never held while a sink
	// runs.
	startMu       sync.Mutex
	childStarted  bool
	childStartErr error
	handshook     bool
	handshakeGen  uint64
	handshakeCaps WorkerCapabilities

	mu         sync.Mutex
	closed     bool
	generation uint64
	sessions   map[string]*acpWorkerSession
	nextID     int
	nextReqID  int
}

// acpWorkerSession is one session this backend started on the adapter. Its
// sequence counter, its mode and its pending requests belong to it alone: two
// sessions on one backend never share routing or ordering.
type acpWorkerSession struct {
	spec       WorkerSessionSpec
	caps       WorkerCapabilities
	mode       string
	modeOK     bool
	generation uint64
	seq        uint64
	pending    map[string]*acpPendingRequest
}

// acpPendingRequest is one adapter request forwarded to the planner and not
// yet settled. A request settles exactly once: Close and the handler's own
// return race for it, and the loser finds nothing to do.
type acpPendingRequest struct {
	method string
	cancel context.CancelFunc
}

// newACPWorkerBackend returns a WorkerBackend that drives transport as an ACP
// v1 agent. The child is built around the transport the caller supplies, which
// is what keeps the implicit `marshal acp` argument out of the picture: the
// transport is a configured adapter, not this binary.
func newACPWorkerBackend(transport agentTransport, events workerEventSink, requests workerRequestSink) WorkerBackend {
	b := &acpWorkerBackend{
		transport: transport,
		events:    events,
		requests:  requests,
		sessions:  map[string]*acpWorkerSession{},
	}
	b.child = &Child{
		Transport:      transport,
		OnNotification: b.onNotification,
		OnRequest:      b.onRequest,
		OnRestart:      b.onRestart,
	}
	return b
}

// Start initializes the adapter, opens an ACP session in the requested working
// directory, negotiates the requested mode and reports what that session can
// do. The mode is verified before Start returns, so a session the planner
// holds is never one whose access mode was silently refused.
func (b *acpWorkerBackend) Start(ctx context.Context, spec WorkerSessionSpec) (string, WorkerCapabilities, error) {
	caps, err := b.handshake(ctx)
	if err != nil {
		return "", WorkerCapabilities{}, err
	}
	cwd, err := acpSessionCWD(spec)
	if err != nil {
		return "", WorkerCapabilities{}, err
	}

	res, err := b.child.Request(ctx, acpMethodSessionNew, map[string]any{
		"cwd": cwd,
		// Always sent, never omitted: an adapter that defaults to its own
		// MCP servers would otherwise attach tools the planner did not
		// configure.
		"mcpServers": []any{},
	})
	if err != nil {
		return "", WorkerCapabilities{}, fmt.Errorf("acp worker: session/new: %w", err)
	}
	var created acpSessionNewResult
	if len(bytes.TrimSpace(res)) > 0 && !bytes.Equal(bytes.TrimSpace(res), []byte("null")) {
		if err := json.Unmarshal(res, &created); err != nil {
			return "", WorkerCapabilities{}, fmt.Errorf("acp worker: session/new result: %w", err)
		}
	}

	sessionID := created.SessionID
	if sessionID == "" {
		b.mu.Lock()
		b.nextID++
		sessionID = fmt.Sprintf("acp-session-%d", b.nextID)
		b.mu.Unlock()
	}

	var available []string
	mode := ""
	if created.Modes != nil {
		mode = created.Modes.CurrentModeID
		for _, m := range created.Modes.AvailableModes {
			if m.ID != "" {
				available = append(available, m.ID)
			}
		}
	}

	if spec.Mode != "" {
		if !containsMode(available, spec.Mode) {
			return "", WorkerCapabilities{}, fmt.Errorf(
				"acp worker: mode %q is not available (available modes: %s)",
				spec.Mode, describeModes(available))
		}
		if err := b.selectMode(ctx, sessionID, spec.Mode); err != nil {
			return "", WorkerCapabilities{}, err
		}
		mode = spec.Mode
	}

	sessionCaps := caps
	sessionCaps.Modes = append([]string(nil), available...)
	reportedCaps := caps
	reportedCaps.Modes = append([]string(nil), available...)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return "", WorkerCapabilities{}, errors.New("acp worker: backend is closed")
	}
	if _, dup := b.sessions[sessionID]; dup {
		b.mu.Unlock()
		return "", WorkerCapabilities{}, fmt.Errorf("acp worker: the adapter reused session id %q", sessionID)
	}
	b.sessions[sessionID] = &acpWorkerSession{
		spec:       spec,
		caps:       sessionCaps,
		mode:       mode,
		modeOK:     true,
		generation: b.generation,
		pending:    map[string]*acpPendingRequest{},
	}
	b.mu.Unlock()

	b.emit(sessionID, workerEventSessionStarted, map[string]any{
		"workerId":    spec.WorkerID,
		"workspaceId": spec.WorkspaceID,
		"mode":        mode,
	})
	if mode != "" {
		b.emit(sessionID, workerEventSessionMode, map[string]any{"modeId": mode})
	}
	return sessionID, reportedCaps, nil
}

// Prompt dispatches one turn. Once the frame is on the wire its outcome is
// either observed or uncertain: a transport that dies, or a generation that is
// replaced, leaves the turn possibly applied to the workspace, so it is
// reported as ErrWorkerUncertain and never reissued.
func (b *acpWorkerBackend) Prompt(ctx context.Context, sessionID, text string) (WorkerTurnResult, error) {
	b.mu.Lock()
	sess := b.sessions[sessionID]
	generation := b.generation
	if sess == nil {
		b.mu.Unlock()
		return WorkerTurnResult{}, fmt.Errorf("acp worker: unknown session %q", sessionID)
	}
	caps, mode, modeOK, sessionGen := sess.caps, sess.mode, sess.modeOK, sess.generation
	b.mu.Unlock()

	if sessionGen != generation {
		// The adapter process this session lived in is gone; its
		// replacement has never heard of it. Nothing was dispatched here,
		// but the session's continuity is unknown, not successful.
		return WorkerTurnResult{}, b.uncertain(sessionID, fmt.Errorf("the adapter generation %d holding session %q was replaced", sessionGen, sessionID))
	}
	if !caps.Prompt {
		return WorkerTurnResult{}, ErrWorkerUnsupported
	}
	if !modeOK {
		return WorkerTurnResult{}, fmt.Errorf("acp worker: session %q has no verified mode (requested %q)", sessionID, mode)
	}

	res, err := b.child.Request(ctx, acpMethodSessionPrompt, map[string]any{
		"sessionId": sessionID,
		"prompt": []map[string]any{
			{"type": "text", "text": text},
		},
	})
	if err != nil {
		var rpcErr *rpcError
		if errors.As(err, &rpcErr) {
			// The adapter answered the turn: it ended, just not well.
			return WorkerTurnResult{}, fmt.Errorf("acp worker: session/prompt: %w", err)
		}
		return WorkerTurnResult{}, b.uncertain(sessionID, err)
	}

	var out struct {
		StopReason string `json:"stopReason"`
	}
	if len(bytes.TrimSpace(res)) > 0 {
		if err := json.Unmarshal(res, &out); err != nil {
			return WorkerTurnResult{}, b.uncertain(sessionID, fmt.Errorf("session/prompt result: %w", err))
		}
	}
	return WorkerTurnResult{StopReason: out.StopReason}, nil
}

// Cancel asks the adapter to end the turn in flight. A session that is unknown
// or already closed has nothing to cancel, and that is not an error.
func (b *acpWorkerBackend) Cancel(ctx context.Context, sessionID string) error {
	b.mu.Lock()
	sess := b.sessions[sessionID]
	b.mu.Unlock()
	if sess == nil {
		return nil
	}
	if err := b.child.Notify(acpMethodSessionCancel, map[string]any{"sessionId": sessionID}); err != nil {
		return fmt.Errorf("acp worker: session/cancel: %w", err)
	}
	b.emit(sessionID, workerEventTurnCancelled, map[string]any{})
	return nil
}

// Close ends one session. Requests the planner never answered are settled as
// cancelled exactly once, so a browser that disconnects mid-question does not
// leave the adapter waiting. When the last session closes, the adapter process
// goes with it: a backend never outlives the planner that started it.
func (b *acpWorkerBackend) Close(ctx context.Context, sessionID string) error {
	b.mu.Lock()
	sess := b.sessions[sessionID]
	if sess == nil {
		b.mu.Unlock()
		return nil
	}
	delete(b.sessions, sessionID)
	cancels := make([]context.CancelFunc, 0, len(sess.pending))
	for id, req := range sess.pending {
		cancels = append(cancels, req.cancel)
		delete(sess.pending, id)
	}
	last := len(b.sessions) == 0
	if last {
		b.closed = true
	}
	b.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	if last {
		b.startMu.Lock()
		started := b.childStarted
		b.startMu.Unlock()
		if started {
			b.child.Stop()
		}
	}
	return nil
}

// handshake starts the child and runs initialize once per child generation.
// The adapter's capability answer is the only source of the capabilities: a
// field it did not send is false, never a hopeful true.
func (b *acpWorkerBackend) handshake(ctx context.Context) (WorkerCapabilities, error) {
	b.startMu.Lock()
	defer b.startMu.Unlock()

	if err := b.startChild(); err != nil {
		return WorkerCapabilities{}, err
	}
	b.mu.Lock()
	generation, closed := b.generation, b.closed
	b.mu.Unlock()
	if closed {
		return WorkerCapabilities{}, errors.New("acp worker: backend is closed")
	}
	if b.handshook && b.handshakeGen == generation {
		return b.handshakeCaps, nil
	}

	res, err := b.child.Request(ctx, acpMethodInitialize, map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "marshal-bridge", "version": "1"},
	})
	if err != nil {
		return WorkerCapabilities{}, fmt.Errorf("acp worker: initialize: %w", err)
	}
	var init acpInitializeResult
	if len(bytes.TrimSpace(res)) > 0 {
		if err := json.Unmarshal(res, &init); err != nil {
			return WorkerCapabilities{}, fmt.Errorf("acp worker: initialize result: %w", err)
		}
	}
	caps := WorkerCapabilities{
		// session/cancel is core ACP: every adapter that accepts a prompt
		// takes a cancel for it.
		Cancel:      true,
		SessionLoad: init.AgentCapabilities.LoadSession,
		Prompt:      init.advertisesPrompt(),
		// A session that takes prompts is also the one that may ask the
		// planner for permission to run a tool.
		PermissionRequests: init.advertisesPrompt(),
	}
	b.handshook, b.handshakeGen, b.handshakeCaps = true, generation, caps
	return caps, nil
}

// startChild starts the child once. It runs under startMu.
func (b *acpWorkerBackend) startChild() error {
	if b.childStarted {
		return b.childStartErr
	}
	b.childStarted = true
	if b.transport == nil {
		b.childStartErr = errors.New("acp worker: a transport is required")
		return b.childStartErr
	}
	b.childStartErr = b.child.Start()
	return b.childStartErr
}

// selectMode applies the requested mode and requires the adapter's answer to
// agree with it before a prompt is ever dispatched.
func (b *acpWorkerBackend) selectMode(ctx context.Context, sessionID, mode string) error {
	res, err := b.child.Request(ctx, acpMethodSessionSetMode, map[string]any{
		"sessionId": sessionID,
		"modeId":    mode,
	})
	if err != nil {
		return fmt.Errorf("acp worker: session/set_mode %q: %w", mode, err)
	}
	current := ""
	if len(bytes.TrimSpace(res)) > 0 {
		var out struct {
			ModeID        string `json:"modeId"`
			CurrentModeID string `json:"currentModeId"`
		}
		if err := json.Unmarshal(res, &out); err == nil {
			current = out.ModeID
			if current == "" {
				current = out.CurrentModeID
			}
		}
	}
	// An adapter that answers with no mode at all accepted the request; one
	// that names a different mode refused it.
	if current != "" && current != mode {
		return fmt.Errorf("acp worker: session/set_mode returned mode %q, want %q", current, mode)
	}
	return nil
}

// onRestart records that the adapter process was replaced. Every session this
// backend holds lived in the generation that died.
func (b *acpWorkerBackend) onRestart() {
	b.mu.Lock()
	b.generation++
	b.mu.Unlock()
}

// currentGeneration is the adapter generation a new session would join.
func (b *acpWorkerBackend) currentGeneration() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.generation
}

// onNotification routes one adapter notification. A supported update becomes
// the matching event; anything this backend does not model is preserved as a
// bounded opaque event rather than dropped.
func (b *acpWorkerBackend) onNotification(method string, params json.RawMessage) {
	if method != acpMethodSessionUpdate {
		sessionID := acpParamSessionID(params)
		if sessionID == "" {
			return
		}
		b.emit(sessionID, workerEventTurnUnknown, map[string]any{
			"method": method,
			"update": opaquePayload(params, acpOpaqueEventLimit),
		})
		return
	}

	var envelope struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return
	}
	sessionID := envelope.SessionID
	if sessionID == "" {
		sessionID = b.soleSession()
	}
	if sessionID == "" {
		return
	}

	var update acpSessionUpdatePayload
	_ = json.Unmarshal(envelope.Update, &update)
	kind := update.Kind
	if kind == "" {
		kind = update.SessionUpdate
	}

	switch {
	case acpIsTextUpdate(kind):
		text := update.Content.Text
		if text == "" {
			text = update.Text
		}
		bounded, truncated := truncateWorkerText(text, acpTextEventLimit)
		b.emit(sessionID, workerEventTurnText, map[string]any{
			"kind":      kind,
			"text":      bounded,
			"truncated": truncated,
		})
	case acpIsToolUpdate(kind):
		b.emit(sessionID, workerEventTurnTool, map[string]any{
			"kind":   kind,
			"update": opaquePayload(envelope.Update, acpTextEventLimit),
		})
	case acpIsModeUpdate(kind):
		mode := update.CurrentModeID
		if mode == "" {
			mode = update.ModeID
		}
		b.noteSessionMode(sessionID, mode)
		b.emit(sessionID, workerEventSessionMode, map[string]any{"modeId": mode})
	case acpIsPermissionUpdate(kind):
		b.emit(sessionID, workerEventTurnPermission, map[string]any{
			"kind":   kind,
			"update": opaquePayload(envelope.Update, acpOpaqueEventLimit),
		})
	case acpIsQuestionUpdate(kind):
		b.emit(sessionID, workerEventTurnQuestion, map[string]any{
			"kind":   kind,
			"update": opaquePayload(envelope.Update, acpOpaqueEventLimit),
		})
	default:
		b.emit(sessionID, workerEventTurnUnknown, map[string]any{
			"kind":   kind,
			"update": opaquePayload(envelope.Update, acpOpaqueEventLimit),
		})
	}
}

// onRequest answers one adapter-initiated request. A permission request and a
// supported elicitation go to the planner's request sink and its raw answer is
// returned to the adapter verbatim. Any other method is refused with JSON-RPC's
// method-not-found, and journaled as an unsupported request rather than
// silently ignored.
func (b *acpWorkerBackend) onRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	requestKind := classifyACPClientRequest(method)
	sessionID := acpParamSessionID(params)
	if sessionID == "" {
		sessionID = b.soleSession()
	}
	if requestKind == acpRequestUnsupported {
		b.emit(sessionID, workerEventUnsupported, map[string]any{
			"method": method,
			"params": opaquePayload(params, acpOpaqueEventLimit),
		})
		return nil, &handlerError{Code: -32601, Message: "method not found: " + method}
	}

	b.mu.Lock()
	sess := b.sessions[sessionID]
	if sess == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("acp worker: %q names unknown session %q", method, sessionID)
	}
	requestID := acpRequestID(params)
	if requestID == "" {
		b.nextReqID++
		requestID = fmt.Sprintf("acp-request-%d", b.nextReqID)
	}
	if _, dup := sess.pending[requestID]; dup {
		b.mu.Unlock()
		return nil, fmt.Errorf("acp worker: duplicate request id %q", requestID)
	}
	reqCtx, cancel := context.WithCancel(ctx)
	sess.pending[requestID] = &acpPendingRequest{method: method, cancel: cancel}
	sink := b.requests
	b.mu.Unlock()

	eventKind := workerEventTurnPermission
	if requestKind == acpRequestQuestion {
		eventKind = workerEventTurnQuestion
	}
	b.emit(sessionID, eventKind, map[string]any{
		"requestId": requestID,
		"method":    method,
		"params":    opaquePayload(params, acpOpaqueEventLimit),
	})

	if sink == nil {
		b.settlePending(sessionID, requestID)
		return nil, ErrWorkerUnsupported
	}
	if reqCtx.Err() != nil {
		b.settlePending(sessionID, requestID)
		return nil, errRequestCancelled
	}

	answer, err := sink(reqCtx, sessionID, requestID, method, params)
	b.settlePending(sessionID, requestID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(reqCtx.Err(), context.Canceled) {
			// Close settled this request: the adapter is told the request
			// was cancelled, exactly as the protocol spells it.
			return nil, errRequestCancelled
		}
		return nil, err
	}
	if len(bytes.TrimSpace(answer)) == 0 {
		return map[string]any{}, nil
	}
	return json.RawMessage(answer), nil
}

// settlePending settles one forwarded request exactly once.
func (b *acpWorkerBackend) settlePending(sessionID, requestID string) {
	b.mu.Lock()
	var cancel context.CancelFunc
	if sess := b.sessions[sessionID]; sess != nil {
		if req := sess.pending[requestID]; req != nil {
			cancel = req.cancel
			delete(sess.pending, requestID)
		}
	}
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// emit delivers one event under its session's own sequence. A session that no
// longer exists has nowhere to deliver to.
func (b *acpWorkerBackend) emit(sessionID, kind string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		raw = json.RawMessage(`{}`)
	}
	b.mu.Lock()
	sess := b.sessions[sessionID]
	sink := b.events
	if sess == nil || sink == nil {
		b.mu.Unlock()
		return
	}
	sess.seq++
	seq := sess.seq
	b.mu.Unlock()
	sink(sessionID, seq, kind, raw)
}

// uncertain reports a dispatched turn whose outcome this backend could not
// observe.
func (b *acpWorkerBackend) uncertain(sessionID string, cause error) error {
	b.emit(sessionID, workerEventTurnUncertain, map[string]any{"reason": cause.Error()})
	return fmt.Errorf("%w: %v", ErrWorkerUncertain, cause)
}

// soleSession returns the only session this backend holds, or "" when there
// are none or several: a frame with no session id is only routable when there
// is exactly one candidate.
func (b *acpWorkerBackend) soleSession() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sessions) != 1 {
		return ""
	}
	for id := range b.sessions {
		return id
	}
	return ""
}

// noteSessionMode records the mode an update reported as current.
func (b *acpWorkerBackend) noteSessionMode(sessionID, mode string) {
	if mode == "" {
		return
	}
	b.mu.Lock()
	if sess := b.sessions[sessionID]; sess != nil {
		sess.mode = mode
		sess.modeOK = true
	}
	b.mu.Unlock()
}

// acpInitializeResult is what initialize's result must carry for this backend
// to know what the adapter can do.
type acpInitializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession bool `json:"loadSession"`
		// PromptCapabilities is kept raw so "sent" is distinguishable from
		// "sent as null": only a real object advertises prompting.
		PromptCapabilities json.RawMessage `json:"promptCapabilities"`
	} `json:"agentCapabilities"`
}

// advertisesPrompt reports whether the adapter sent prompt capabilities.
func (r acpInitializeResult) advertisesPrompt() bool {
	pc := bytes.TrimSpace(r.AgentCapabilities.PromptCapabilities)
	return len(pc) > 0 && !bytes.Equal(pc, []byte("null"))
}

// acpSessionNewResult is what session/new's result may carry.
type acpSessionNewResult struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID string `json:"id"`
		} `json:"availableModes"`
	} `json:"modes"`
}

// acpSessionUpdatePayload is the part of a session update this backend models.
type acpSessionUpdatePayload struct {
	Kind          string `json:"kind"`
	SessionUpdate string `json:"sessionUpdate"`
	Content       struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Text          string `json:"text"`
	CurrentModeID string `json:"currentModeId"`
	ModeID        string `json:"modeId"`
}

// acpClientRequestKind classifies an adapter-initiated request method.
type acpClientRequestKind int

const (
	acpRequestUnsupported acpClientRequestKind = iota
	acpRequestPermission
	acpRequestQuestion
)

// classifyACPClientRequest classifies one adapter-initiated request method.
// Only the methods the planner can actually answer are supported.
func classifyACPClientRequest(method string) acpClientRequestKind {
	switch method {
	case acpMethodRequestPermission:
		return acpRequestPermission
	case acpMethodRequestQuestion, acpMethodElicit, acpMethodElicitationCreate:
		return acpRequestQuestion
	default:
		return acpRequestUnsupported
	}
}

// acpParamSessionID is the session id a frame names, if any.
func acpParamSessionID(params json.RawMessage) string {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	return p.SessionID
}

// acpRequestID names a forwarded request for the planner. ACP's own request id
// never reaches the request sink, so the id the adapter sends in the params —
// the tool call or the question it is about — is the stable name, and a
// generated one covers the rest.
func acpRequestID(params json.RawMessage) string {
	var p struct {
		RequestID     string `json:"requestId"`
		ToolCallID    string `json:"toolCallId"`
		QuestionID    string `json:"questionId"`
		ElicitationID string `json:"elicitationId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ""
	}
	for _, id := range []string{p.RequestID, p.ToolCallID, p.QuestionID, p.ElicitationID} {
		if id != "" {
			return id
		}
	}
	return ""
}

// acpSessionCWD resolves the absolute working directory session/new carries.
func acpSessionCWD(spec WorkerSessionSpec) (string, error) {
	dir := spec.CWD
	if dir == "" {
		dir = spec.ProjectRoot
	}
	if dir == "" {
		return "", errors.New("acp worker: the session spec has no working directory")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("acp worker: resolve cwd: %w", err)
	}
	return abs, nil
}

func containsMode(modes []string, want string) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}

func describeModes(modes []string) string {
	if len(modes) == 0 {
		return "none"
	}
	return strings.Join(modes, ", ")
}

// acpIsTextUpdate reports an update kind that carries transcript text.
func acpIsTextUpdate(kind string) bool {
	switch kind {
	case "agent_message_chunk", "agent_thought_chunk", "agent_message", "thought":
		return true
	}
	return false
}

// acpIsToolUpdate reports an update kind that reports tool activity.
func acpIsToolUpdate(kind string) bool {
	switch kind {
	case "tool_call", "tool_call_update", "tool_result", "plan":
		return true
	}
	return false
}

// acpIsModeUpdate reports an update kind that reports the current mode.
func acpIsModeUpdate(kind string) bool {
	switch kind {
	case "current_mode_update", "mode_changed", "current_mode_changed":
		return true
	}
	return false
}

func acpIsPermissionUpdate(kind string) bool {
	switch kind {
	case "permission_request", "request_permission", "permission":
		return true
	}
	return false
}

func acpIsQuestionUpdate(kind string) bool {
	switch kind {
	case "question_request", "request_question", "question", "elicitation":
		return true
	}
	return false
}

// truncateWorkerText bounds a transcript fragment, cutting on a rune boundary
// and marking the truncation rather than dropping the text.
func truncateWorkerText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "...[truncated]", true
}

// opaquePayload preserves an unmodelled frame as bounded JSON. A frame that
// fits travels verbatim; one that does not becomes a preview, so the event
// stays valid JSON and still says how much was lost.
func opaquePayload(raw json.RawMessage, limit int) map[string]any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}
	}
	if len(trimmed) <= limit && json.Valid(trimmed) {
		return map[string]any{"payload": json.RawMessage(trimmed)}
	}
	text := string(trimmed)
	if len(text) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "...[truncated]"
	}
	return map[string]any{"truncated": true, "bytes": len(trimmed), "preview": text}
}
