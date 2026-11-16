package bridge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The attach wire contract (P4 D3). The TUI's client and the daemon are two
// halves of one protocol, so these strings are the binding rather than a
// shared Go type.
const (
	attachPath     = "/api/projects/attach"
	attachProtocol = "marshal-acp"
	sessionHeader  = "Marshal-Session"
	agentHeader    = "Marshal-Agent"
)

// agentAttachPrefix and agentAttachSuffix bracket the agent id in an agent
// attach's path: /api/agents/{id}/attach.
const (
	agentAttachPrefix = "/api/agents/"
	agentAttachSuffix = "/attach"
)

// agentAttachID returns the agent ID a /api/agents/{id}/attach path names.
func agentAttachID(path string) (string, bool) {
	if !strings.HasPrefix(path, agentAttachPrefix) || !strings.HasSuffix(path, agentAttachSuffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, agentAttachPrefix), agentAttachSuffix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// attachRequest is the attach handshake's body.
type attachRequest struct {
	Root      string `json:"root"`
	SessionID string `json:"sessionId,omitempty"`
}

// attachProject serves POST /api/projects/attach on the socket (P4 D3). A
// successful handshake leaves a hijacked connection relaying frames to the
// project child's viewer socket; every refusal is an ordinary JSON answer.
func (s *Server) attachProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "attach is a POST"})
		return
	}
	if !hasToken(r.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), attachProtocol) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attach needs an " + attachProtocol + " upgrade"})
		return
	}
	var req attachRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	root := req.Root
	if !filepath.IsAbs(root) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root must be an absolute path"})
		return
	}
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root must be an existing directory"})
		return
	}
	if s.fleet == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this bridge has no fleet"})
		return
	}

	// The child and the session are resolved within one handshake budget, as
	// the control agent's lazy start is: a stuck child must not hold the
	// request open indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), controlHandshakeTimeout)
	defer cancel()

	pc, err := s.fleet.projectChildFor(ctx, root)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	sessionID := req.SessionID
	created := false
	switch {
	case sessionID == "":
		id, err := pc.reg.New(ctx, AgentPath(root), "")
		if err != nil {
			// Resolving the child cancelled its linger; an attach that leaves
			// nothing behind must arm it again or the child is never reaped.
			s.fleet.rearmLinger(pc)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "open a session: " + err.Error()})
			return
		}
		sessionID, created = id, true
	default:
		// A live fleet agent's session belongs to that agent: an attach would
		// take its relay and answer its client as if it were the TUI (P4 D10
		// decision 6). Refused before any viewer connection opens; the child
		// keeps its session, so no linger is rearmed.
		//
		// Three sources are consulted. The registry's flag is the fast path,
		// but it is set after session/new returns: a Spawn in that window is
		// live on the child and not yet tracked, so the classifier's map —
		// which Spawn fills first — closes it. agentOwning then covers what
		// neither sees: a record naming the session, live or paused (decision
		// 2), which is the one source that knows a paused agent's session is
		// still the agent's.
		_, classified := s.fleet.agentForSession(sessionID)
		_, owned := s.fleet.agentOwning(sessionID)
		if pc.reg.IsFleet(sessionID) || classified || owned {
			logAttach("refused", root, sessionID, "fleet session")
			writeJSON(w, http.StatusConflict, map[string]string{"error": "session " + sessionID + " belongs to a fleet agent"})
			return
		}
		if _, tracked := pc.reg.lookup(sessionID); !tracked {
			if err := pc.reg.Resume(ctx, AgentPath(root), sessionID); err != nil {
				// The agent does not know the session either, so this is a
				// stale ID rather than a bridge fault.
				s.fleet.rearmLinger(pc)
				writeErr(w, ErrUnknownSession)
				return
			}
		}
	}

	conn, err := net.Dial("unix", pc.socket)
	if err != nil {
		// A session opened for an attach that cannot reach its child has no
		// viewer and would otherwise linger as a live runtime nothing can
		// reach.
		if created {
			_ = pc.reg.Close(ctx, sessionID)
		}
		s.fleet.rearmLinger(pc)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "dial the project child: " + err.Error()})
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = conn.Close()
		if created {
			_ = pc.reg.Close(ctx, sessionID)
		}
		s.fleet.rearmLinger(pc)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "attach needs a hijackable connection"})
		return
	}
	client, buffered, err := hj.Hijack()
	if err != nil {
		_ = conn.Close()
		if created {
			_ = pc.reg.Close(ctx, sessionID)
		}
		s.fleet.rearmLinger(pc)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hijack the connection: " + err.Error()})
		return
	}

	// From here the connection is ours: the server's deadlines are dropped so
	// a long-lived stream is never cut off by the handshake's.
	_ = client.SetDeadline(time.Time{})
	head := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: " + attachProtocol + "\r\n" +
		sessionHeader + ": " + sessionID + "\r\n\r\n"
	if _, err := io.WriteString(client, head); err != nil {
		// The client is gone before the stream began, so a session opened for
		// it has no viewer and must not stay open.
		_ = client.Close()
		_ = conn.Close()
		if created {
			_ = pc.reg.Close(ctx, sessionID)
		}
		s.fleet.rearmLinger(pc)
		return
	}

	// A viewer that arrives while the session is being closed attaches to
	// nothing: the relay still runs for it, and detach/touch become no-ops.
	pc.reg.attachViewer(sessionID)
	// The relay rearms the child's linger when the viewer goes: an empty
	// registry is what reaps a project child (P4 D2).
	go s.fleet.relay(relayEnd{
		reg:    pc.reg,
		linger: func() { s.fleet.rearmLinger(pc) },
		root:   pc.root,
	}, sessionID, "", client, buffered.Reader, conn)
}

// attachAgent serves POST /api/agents/{id}/attach on the socket (P5 F5). It is
// the fleet's view of an agent's session: the same handshake as the project
// attach, naming an agent the fleet already started instead of opening one.
// Every refusal is an ordinary JSON answer, and none of them resumes the
// agent: the fleet's resume is the way back.
func (s *Server) attachAgent(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "attach is a POST"})
		return
	}
	if !hasToken(r.Header.Get("Connection"), "upgrade") ||
		!strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), attachProtocol) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "attach needs an " + attachProtocol + " upgrade"})
		return
	}
	// The handshake carries no body: the path names the agent.
	if s.fleet == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "this bridge has no fleet"})
		return
	}
	a, ok := s.fleet.ws.Agent(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent " + id})
		return
	}
	name := agentDisplayName(a)
	pc, sessionID, err := s.fleet.attachTargetFor(id)
	if err != nil {
		if errors.Is(err, ErrUnknownAgent) {
			// A paused agent keeps its record and loses its runtime. Resuming
			// it here would start an agent behind the fleet's back.
			writeJSON(w, http.StatusConflict, map[string]string{"error": "agent " + name + " is paused: resume it first"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	// What the attach dials and what its relay needs: an agent on a project
	// child uses that child's socket, registry, linger rearm and root; an
	// agent whose runtime records a viewer socket of its own is dialled there
	// and counted on its own registry, with no linger to rearm (decision 2).
	// A container's agent is dialled on its host-side socket like an own
	// child, and its relay carries the methods a container's viewer may call
	// (decision 6).
	var socket string
	end := relayEnd{}
	containerized := false
	versionWarning := false
	if pc != nil {
		socket = pc.socket
		end = relayEnd{reg: pc.reg, linger: func() { s.fleet.rearmLinger(pc) }, root: pc.root}
	} else {
		s.fleet.mu.Lock()
		rt := s.fleet.runtimes[id]
		if rt != nil {
			socket, end = rt.viewerSocket, relayEnd{reg: rt.reg, root: name}
			containerized, versionWarning = rt.containerized, rt.versionWarning
			if containerized {
				end.allow = containerViewerSet()
			}
		}
		s.fleet.mu.Unlock()
		// A container's agent is not refused here: it serves viewers on the
		// host side of its socket, and an absent one is a dial that fails
		// (decision 6). Anything else without a socket is an agent whose
		// runtime went away before the attach reached it, or a fleet with no
		// state root to put one in.
		if socket == "" && !containerized {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "agent " + name + " has no viewer socket yet"})
			return
		}
	}
	if sessionID == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "agent " + name + " has no session yet"})
		return
	}

	// rearm is what a refusal that leaves nothing behind owes a project child:
	// resolving the child cancelled its linger, so it must be armed again. An
	// own child has none, and there end.linger is nil.
	rearm := func() {
		if end.linger != nil {
			end.linger()
		}
	}
	// A failed dial names what it could not reach. A container's socket is on
	// the host side of the boundary, and an image that predates the viewer
	// socket serves none: the warning says so (decision 6).
	dialled := "the agent's own child"
	if pc != nil {
		dialled = "the project child"
	}
	if containerized {
		dialled = "the agent's container"
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		rearm()
		sentence := "dial " + dialled + ": " + err.Error()
		if containerized && versionWarning {
			sentence += "; its image runs another marshal version, which may serve no viewer socket: rebuild it"
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": sentence})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = conn.Close()
		rearm()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "attach needs a hijackable connection"})
		return
	}
	client, buffered, err := hj.Hijack()
	if err != nil {
		_ = conn.Close()
		rearm()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "hijack the connection: " + err.Error()})
		return
	}

	// From here the connection is ours: the server's deadlines are dropped so
	// a long-lived stream is never cut off by the handshake's.
	_ = client.SetDeadline(time.Time{})
	head := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: " + attachProtocol + "\r\n" +
		sessionHeader + ": " + sessionID + "\r\n" +
		agentHeader + ": " + a.ID + "\r\n\r\n"
	if _, err := io.WriteString(client, head); err != nil {
		// The client is gone before the stream began. The session is the
		// agent's, so it stays open: only what the attach opened is closed.
		_ = client.Close()
		_ = conn.Close()
		rearm()
		return
	}

	// A viewer that arrives while the session is being closed attaches to
	// nothing: the relay still runs for it, and detach/touch become no-ops.
	end.reg.attachViewer(sessionID)
	go s.fleet.relay(end, sessionID, a.ID, client, buffered.Reader, conn)
}

// hasToken reports whether a comma-separated header value carries want, case
// insensitively, as "Connection: keep-alive, Upgrade" does.
func hasToken(header, want string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), want) {
			return true
		}
	}
	return false
}

// logAttach records an attach or a detach. It never logs a frame's params: a
// prompt or a tool result is the user's data (P3 R8).
func logAttach(event, root, sessionID, reason string) {
	slog.Default().Info("webbridge: attach "+event, "root", root, "session", sessionID, "reason", reason)
}
