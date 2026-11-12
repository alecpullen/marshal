package bridge

import (
	"context"
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
)

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
		// Both sources are consulted. The registry's flag is the fast path, but
		// it is set after session/new returns: a Spawn in that window is live on
		// the child and not yet tracked, so the classifier's map — which Spawn
		// fills first — closes it.
		_, classified := s.fleet.agentForSession(sessionID)
		if pc.reg.IsFleet(sessionID) || classified {
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
	go s.fleet.relay(pc, sessionID, client, buffered.Reader, conn)
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
