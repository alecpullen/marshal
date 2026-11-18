package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"

	"marshal/web/bridge/contract"
)

// readFrame reads one newline-terminated frame, newline included, up to cap. A
// frame over the cap is an error rather than a truncation: the relay ends
// instead of forwarding half a message.
func readFrame(src *bufio.Reader, cap int) ([]byte, error) {
	var frame []byte
	for {
		chunk, err := src.ReadSlice('\n')
		if len(frame)+len(chunk) > cap {
			return nil, errFrameTooLarge
		}
		frame = append(frame, chunk...)
		if err == nil {
			return frame, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

// errFrameTooLarge ends a relay whose frame exceeds the cap.
var errFrameTooLarge = errors.New("bridge: relay frame over the cap")

// frameCap is the largest frame a relay forwards: the fleet's own cap when a
// test set one, else maxFrameBytes.
func (f *Fleet) frameCap() int {
	if f.relayFrameCap > 0 {
		return f.relayFrameCap
	}
	return maxFrameBytes
}

// restartingMethod is acpwire.MethodRestarting; web/ may not import it.
const restartingMethod = "marshal/restarting"

// relay kinds a watched request can be.
const (
	watchReplacement = iota
	watchClose
)

// relayWatch is one outbound request the relay follows to its answer.
type relayWatch struct {
	kind int
	// sessionID is the session a close names.
	sessionID string
}

// relayEnd is what a relay needs from the child its attach reached (decision
// 2): the registry that counts the viewer, the hook that rearms a project
// child's linger — nil for a child of the agent's own — and what names the
// child on the attach's log lines: a project child's root directory, or an
// agent's own child's display name.
type relayEnd struct {
	reg    *Registry       // counts the viewer
	linger func()          // rearms a project child's linger; nil for an own child
	root   string          // names the child on the attach's log lines
	allow  map[string]bool // a container's methods; nil forwards every frame
}

// relay copies frames between an attached client and its viewer connection
// until either side ends (P4 D3). Every frame passes through unchanged, in
// order, in both directions; the only frames it interprets are the answers
// to a session/new that replaces a session and to a session/close.
//
// agentID is the agent whose session an agent attach opened, "" for a project
// attach.
func (f *Fleet) relay(end relayEnd, sessionID, agentID string, client net.Conn, buffered *bufio.Reader, agent net.Conn) {
	r := &relay{
		fleet:   f,
		end:     end,
		client:  client,
		agent:   agent,
		session: sessionID,
		agentID: agentID,
		watches: make(map[string]*relayWatch),
		cap:     f.frameCap(),
	}
	logAttach("attached", end.root, sessionID, "requested")
	// Tracked before either pump starts, so a restart that is announced while
	// this viewer is live reaches it.
	f.trackRelay(r)

	done := make(chan string, 2)
	go func() { done <- r.pumpClient(buffered) }()
	go func() { done <- r.pumpAgent(bufio.NewReader(agent)) }()

	reason := <-done
	r.stop()
	<-done // the other pump returns once its connection is closed
	r.finish(reason)
}

// relay is one attach's state: the two connections, the session it follows,
// and the answers it is waiting for.
type relay struct {
	fleet  *Fleet
	end    relayEnd
	client net.Conn
	agent  net.Conn

	// agentID is the agent whose session this attach opened, "" for a project
	// attach.
	agentID string

	sessMu  sync.Mutex
	session string

	// clientMu serialises writes to the client: the agent's frames and the
	// relay's refusals.
	clientMu sync.Mutex

	// cap is the largest frame this relay forwards, read once so no pump
	// consults shared state per frame.
	cap int

	watchMu sync.Mutex
	watches map[string]*relayWatch

	// dropMu guards dropped, the methods this attach has already logged a
	// drop for: decision 2 asks for the first one of each method, not one per
	// frame.
	dropMu  sync.Mutex
	dropped map[string]bool

	stopOnce sync.Once
}

// trackRelay records a running relay under the registry it counts its viewer
// on, so that a child's restart announcement reaches exactly that child's
// viewers.
func (f *Fleet) trackRelay(r *relay) {
	if r.end.reg == nil {
		return
	}
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	if f.relays == nil {
		f.relays = make(map[*Registry]map[*relay]struct{})
	}
	set := f.relays[r.end.reg]
	if set == nil {
		set = make(map[*relay]struct{})
		f.relays[r.end.reg] = set
	}
	set[r] = struct{}{}
}

// untrackRelay removes a finished relay. A registry whose set empties is
// deleted from the map: a child with no viewer has nothing to announce to.
func (f *Fleet) untrackRelay(r *relay) {
	if r.end.reg == nil {
		return
	}
	f.relayMu.Lock()
	defer f.relayMu.Unlock()
	set := f.relays[r.end.reg]
	delete(set, r)
	if len(set) == 0 {
		delete(f.relays, r.end.reg)
	}
}

// announceRestart writes marshal/restarting to every live relay counted on
// reg, and answers how many it reached.
//
// The set is copied under relayMu and the lock released before any client is
// written to: one slow client must not hold up another child's viewers. A
// relay whose write fails is skipped rather than waited on, because its pumps
// will end it.
func (f *Fleet) announceRestart(reg *Registry, reason, version string) int {
	f.relayMu.Lock()
	live := make([]*relay, 0, len(f.relays[reg]))
	for r := range f.relays[reg] {
		live = append(live, r)
	}
	f.relayMu.Unlock()

	params := map[string]any{"reason": reason}
	// The agent's own frame omits an empty version, so this one does too.
	if version != "" {
		params["version"] = version
	}
	// Marshalled from maps, so the reason is quoted; encoding/json sorts a
	// map's keys, so the frame is the same bytes every time.
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  restartingMethod,
		"params":  params,
	})
	if err != nil {
		return 0
	}
	frame = append(frame, '\n')

	reached := 0
	for _, r := range live {
		r.clientMu.Lock()
		_, err := r.client.Write(frame)
		r.clientMu.Unlock()
		if err == nil {
			reached++
		}
	}
	return reached
}

// currentSession is the session the relay follows right now: a replacement
// moves it.
func (r *relay) currentSession() string {
	r.sessMu.Lock()
	defer r.sessMu.Unlock()
	return r.session
}

func (r *relay) setSession(id string) {
	r.sessMu.Lock()
	r.session = id
	r.sessMu.Unlock()
}

// stop closes both connections, which ends the pump that has not returned.
func (r *relay) stop() {
	r.stopOnce.Do(func() {
		_ = r.client.Close()
		_ = r.agent.Close()
	})
}

// finish uncounts the viewer, touches the session, and arms the linger when
// the close that ended the relay left the project child with no session. An
// own child has no linger to arm: end.linger is nil.
func (r *relay) finish(reason string) {
	r.fleet.untrackRelay(r)
	id := r.currentSession()
	r.end.reg.detachViewer(id)
	r.end.reg.touch(id)
	// Neither pump is running now, so an answer that has not arrived cannot
	// arrive: drop the watches rather than keep decoding every frame for them.
	r.clearWatches()
	if r.end.linger != nil {
		r.end.linger()
	}
	logAttach("detached", r.end.root, id, reason)
}

// pumpClient forwards the client's frames to the child. It reads the
// hijacked reader, so whatever the HTTP server had already buffered behind
// the request goes first.
func (r *relay) pumpClient(src *bufio.Reader) string {
	for {
		frame, err := readFrame(src, r.cap)
		if err != nil {
			return "client: " + frameErrReason(err)
		}
		// A restricted relay comes first: a container's viewer may send only
		// the methods its session screen uses, and everything else is answered
		// or dropped here rather than forwarded (decisions 2 and 7).
		if r.end.allow != nil {
			if answer, refused := r.restricted(frame); refused {
				if answer != nil {
					r.clientMu.Lock()
					_, err := r.client.Write(answer)
					r.clientMu.Unlock()
					if err != nil {
						return "client write: " + err.Error()
					}
				}
				continue
			}
		}
		// A frame the relay refuses is answered here instead of being
		// forwarded: no viewer may finish an agent's session, so neither a
		// close nor a replacement naming one ever reaches the child (decision
		// 1).
		if answer, refused := r.refusal(frame); refused {
			r.clientMu.Lock()
			_, err := r.client.Write(answer)
			r.clientMu.Unlock()
			if err != nil {
				return "client write: " + err.Error()
			}
			continue
		}
		// The watch is registered before the frame is written: the answer can
		// arrive as soon as the child reads it.
		if key, watch, ok := watchedRequest(frame); ok {
			r.addWatch(key, watch)
		}
		if _, err := r.agent.Write(frame); err != nil {
			return "child write: " + err.Error()
		}
		r.end.reg.touch(r.currentSession())
	}
}

// pumpAgent forwards the child's frames to the client, following the answers
// to the requests the client's pump watched for.
func (r *relay) pumpAgent(src *bufio.Reader) string {
	for {
		frame, err := readFrame(src, r.cap)
		if err != nil {
			return "child: " + frameErrReason(err)
		}
		// Nothing is decoded unless something is waiting on an answer: the
		// usual case is a frame on its way to a client that will read every
		// byte of it.
		var after func()
		if r.hasWatches() {
			after = r.answer(frame)
		}
		r.clientMu.Lock()
		_, err = r.client.Write(frame)
		r.clientMu.Unlock()
		if err != nil {
			return "client write: " + err.Error()
		}
		r.end.reg.touch(r.currentSession())
		if after != nil {
			after()
		}
	}
}

// clientRequest is the part of a client frame the relay interprets: the
// request's id, its method, and the session a close or a replacement names.
// watchedRequest and refusal both read a frame through decodeClientRequest, so
// a client frame's shape is decoded in one place rather than in two.
type clientRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Replaces  string `json:"replaces"`
		SessionID string `json:"sessionId"`
	} `json:"params"`
}

// decodeClientRequest decodes one client frame as watchedRequest and refusal
// both read it. ok is false for a frame that is not a request: a malformed
// one, a notification, or an answer.
func decodeClientRequest(frame []byte) (clientRequest, bool) {
	var req clientRequest
	if err := json.Unmarshal(frame, &req); err != nil || len(req.ID) == 0 {
		return clientRequest{}, false
	}
	return req, true
}

// clientFrame is what a restricted relay reads of every client frame: its id,
// when it has one, and its method, when it has one.
type clientFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

// restricted answers a frame a container's relay does not forward
// (decisions 2 and 7): answer is nil for a frame that is only dropped.
//
// A container's viewer may call only the methods its session screen uses
// (contract.ContainerViewerMethods): every other request is answered with
// decision 2's sentence and every other notification is dropped. A frame that
// does not decode as one JSON-RPC message — a batch, or malformed JSON — is
// dropped, and so is one with neither a method nor an id: the relay fails
// closed (decision 7). An answer to one of the agent's own requests carries no
// method and passes unchanged, so approvals and questions still reach the
// viewer and its answers reach the agent.
func (r *relay) restricted(frame []byte) (answer []byte, refused bool) {
	var cf clientFrame
	if err := json.Unmarshal(frame, &cf); err != nil {
		r.dropFrame("", "not one JSON-RPC message")
		return nil, true
	}
	if cf.Method == "" {
		if !hasJSONValue(cf.ID) {
			r.dropFrame("", "neither a method nor an id")
			return nil, true
		}
		return nil, false
	}
	if r.end.allow[cf.Method] {
		return nil, false
	}
	// The close rule is asked before the list is enforced: decision 1 is a rule
	// about sessions rather than about a method's surface, so a session/close or
	// a replacing session/new the close rule refuses is answered with the close
	// rule's own sentence. The answer is returned here rather than asked for
	// again by pumpClient, so the fleet is asked once per frame.
	if answer, closes := r.refusal(frame); closes {
		return answer, true
	}
	if !hasJSONValue(cf.ID) {
		r.dropFrame(cf.Method, "not available for a containerized agent")
		return nil, true
	}
	return refusalAnswer(cf.ID, cf.Method+" is not available for a containerized agent"), true
}

// dropFrame logs the first frame of each method this attach drops, so a client
// retrying a refused notification cannot flood the log (decision 2). An
// undecodable frame has no method and is logged once per attach.
func (r *relay) dropFrame(method, why string) {
	r.dropMu.Lock()
	if r.dropped == nil {
		r.dropped = make(map[string]bool)
	}
	first := !r.dropped[method]
	r.dropped[method] = true
	r.dropMu.Unlock()
	if !first {
		return
	}
	named := method
	if named == "" {
		named = "(no method)"
	}
	slog.Default().Warn("webbridge: restricted relay dropped a frame",
		"root", r.end.root, "session", r.currentSession(), "method", named, "why", why)
}

// containerViewerSet is ContainerViewerMethods as the relay looks them up.
func containerViewerSet() map[string]bool {
	set := make(map[string]bool, len(contract.ContainerViewerMethods))
	for _, method := range contract.ContainerViewerMethods {
		set[method] = true
	}
	return set
}

// refusal answers a client frame the relay does not forward: a session/close
// or a replacing session/new that names an agent's session. ok is false for
// every other frame.
//
// Only a request is refused. A frame without an id is not decoded at all,
// exactly as watchedRequest treats it: ACP sends both methods as requests, so
// an id-less notification naming an agent's session would be forwarded like any
// other frame. Widening this means widening decodeClientRequest, which the
// watch side shares.
//
// No viewer may finish an agent's session, on either attach (decision 1), so
// the frame is answered to the client instead of being forwarded. The answer
// echoes the request's id and says what the fleet screen says: data carries an
// acpwire.Error-shaped object whose code is empty, which is how a marshal
// client reads an error's own sentence. web/ is standard library only, so that
// shape is written out here rather than imported.
func (r *relay) refusal(frame []byte) (answer []byte, ok bool) {
	req, isRequest := decodeClientRequest(frame)
	if !isRequest {
		return nil, false
	}
	var sessionID string
	switch req.Method {
	case "session/new":
		sessionID = req.Params.Replaces
	case "session/close":
		sessionID = req.Params.SessionID
	default:
		return nil, false
	}
	agent, owned := r.fleet.agentOwning(sessionID)
	if !owned {
		return nil, false
	}
	// The answer is assembled rather than marshalled whole: the request's id
	// is already valid JSON and quoting the sentence cannot fail, so a refused
	// frame is never lost to an encoding error and never falls through to the
	// child.
	return refusalAnswer(req.ID, finishFromFleet(agent)), true
}

// refusalAnswer writes an error answer to one client request: the request's own
// id, -32000, and a sentence a marshal client reads as that sentence. data
// carries an acpwire.Error-shaped object whose code is empty, which is how a
// marshal client reads an error's own sentence; web/ is standard library only,
// so that shape is written out here rather than imported.
//
// The answer is assembled rather than marshalled whole: the id is already valid
// JSON and quoting the sentence cannot fail, so a refused frame is never lost
// to an encoding error.
func refusalAnswer(id json.RawMessage, sentence string) []byte {
	// json.Marshal of a string cannot fail: the sentence is quoted, never
	// rejected.
	quoted, _ := json.Marshal(sentence)
	var out bytes.Buffer
	out.WriteString(`{"jsonrpc":"2.0","id":`)
	out.Write(id)
	out.WriteString(`,"error":{"code":-32000,"message":`)
	out.Write(quoted)
	out.WriteString(`,"data":{"code":"","message":`)
	out.Write(quoted)
	out.WriteString(`}}}`)
	out.WriteByte('\n')
	return out.Bytes()
}

// watchedRequest reports the request a client frame asks the relay to follow.
func watchedRequest(frame []byte) (string, *relayWatch, bool) {
	req, ok := decodeClientRequest(frame)
	if !ok {
		return "", nil, false
	}
	switch req.Method {
	case "session/new":
		if req.Params.Replaces == "" {
			return "", nil, false
		}
		return compactID(req.ID), &relayWatch{kind: watchReplacement}, true
	case "session/close":
		if req.Params.SessionID == "" {
			return "", nil, false
		}
		return compactID(req.ID), &relayWatch{kind: watchClose, sessionID: req.Params.SessionID}, true
	}
	return "", nil, false
}

// answer follows one child frame to a watched request. It reports what must
// happen after the frame has been forwarded: nothing for a replacement, whose
// rename is local and already done, and the bridge's own close for a close
// the agent accepted.
func (r *relay) answer(frame []byte) func() {
	var resp struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(frame, &resp); err != nil || len(resp.ID) == 0 {
		return nil
	}
	watch := r.takeWatch(compactID(resp.ID))
	if watch == nil {
		return nil
	}
	// An error answer changes nothing: the request failed, so the session the
	// relay follows is the one it followed before.
	if hasJSONValue(resp.Error) || !hasJSONValue(resp.Result) {
		return nil
	}
	switch watch.kind {
	case watchReplacement:
		var res struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(resp.Result, &res); err != nil || res.SessionID == "" {
			return nil
		}
		if r.end.reg.rename(r.currentSession(), res.SessionID) {
			r.setSession(res.SessionID)
		}
		return nil
	case watchClose:
		id := watch.sessionID
		return func() { r.fleet.closeRelayedSession(r.end.reg, r.end.root, id) }
	}
	return nil
}

// closeRelayedSession ends a session's runtime through the bridge's own
// connection after a viewer closed it. It runs off the relay's goroutine so a
// slow agent never delays the client's stream (decision 8).
func (f *Fleet) closeRelayedSession(reg *Registry, root, id string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), controlHandshakeTimeout)
		defer cancel()
		if err := reg.Close(ctx, id); err != nil {
			slog.Default().Warn("webbridge: close a viewer-closed session",
				"root", root, "session", id, "err", err)
		}
	}()
}

func (r *relay) addWatch(key string, watch *relayWatch) {
	r.watchMu.Lock()
	if r.watches == nil {
		r.watches = make(map[string]*relayWatch)
	}
	r.watches[key] = watch
	r.watchMu.Unlock()
}

// clearWatches drops every pending watch. The relay calls it once both pumps
// have returned: a watched request whose answer never came would otherwise keep
// every later agent frame on the decode path for the life of the attach.
func (r *relay) clearWatches() {
	r.watchMu.Lock()
	r.watches = nil
	r.watchMu.Unlock()
}

func (r *relay) takeWatch(key string) *relayWatch {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	watch := r.watches[key]
	delete(r.watches, key)
	return watch
}

func (r *relay) hasWatches() bool {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	return len(r.watches) > 0
}

// compactID is a JSON-RPC id's canonical form, so "1" and " 1" are the same
// id and a string id keeps its escapes.
func compactID(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return buf.String()
}

// hasJSONValue reports whether a JSON field holds anything but null, the way
// an absent field and an explicit null both mean "none".
func hasJSONValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// frameErrReason turns a read error into the reason a relay ended: a plain
// EOF is the other side hanging up, which is not an error.
func frameErrReason(err error) string {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return "closed"
	}
	return err.Error()
}
