package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// previewTokenTTL is how long a preview token stays valid.
const previewTokenTTL = 12 * time.Hour

// previewPrefix is the path prefix of preview traffic. It sits outside
// /api, so bearerAuth does not apply: the handler checks a token or cookie.
const previewPrefix = "/preview/"

// previewAuthHeader carries the target agent's own proxy token from the
// bridge to the sidecar's preview listener. Other agents can reach that
// listener on the internal network but cannot know this agent's token.
const previewAuthHeader = "X-Marshal-Preview-Auth"

// ErrPreviewPortNotDeclared is returned for a port the agent's workspace
// does not declare under [preview]. It maps to 403.
var ErrPreviewPortNotDeclared = errors.New("bridge: port is not declared for preview")

// previewToken binds a token to one agent and port.
type previewToken struct {
	agentID string
	port    int
	expires time.Time
}

// previewStore holds live preview tokens in memory. A bridge restart
// invalidates them, which only costs the user a new link.
type previewStore struct {
	mu     sync.Mutex
	tokens map[string]previewToken
}

func (f *Fleet) previewTokens() *previewStore {
	f.previewOnce.Do(func() { f.previews = &previewStore{tokens: make(map[string]previewToken)} })
	return f.previews
}

func newPreviewToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// IssuePreview returns a preview URL for a declared port.
func (f *Fleet) IssuePreview(ctx context.Context, agentID string, port int) (string, error) {
	a, ok := f.ws.Agent(agentID)
	if !ok {
		return "", fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
	_, doc, ok, err := f.AgentWorkspaceDoc(ctx, a)
	if err != nil {
		return "", err
	}
	declared := false
	if ok {
		for _, p := range doc.previewPorts() {
			if p == port {
				declared = true
			}
		}
	}
	if !declared {
		return "", fmt.Errorf("%w: %d", ErrPreviewPortNotDeclared, port)
	}
	tok := newPreviewToken()
	st := f.previewTokens()
	now := f.now()
	st.mu.Lock()
	for k, v := range st.tokens {
		if now.After(v.expires) {
			delete(st.tokens, k)
		}
	}
	st.tokens[tok] = previewToken{agentID: agentID, port: port, expires: now.Add(previewTokenTTL)}
	st.mu.Unlock()
	return fmt.Sprintf("%s%s/%d/?t=%s", previewPrefix, url.PathEscape(agentID), port, tok), nil
}

// previewTokenValid reports whether tok is live for the agent and port.
func (f *Fleet) previewTokenValid(tok, agentID string, port int) bool {
	if tok == "" {
		return false
	}
	st := f.previewTokens()
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.tokens[tok]
	if !ok {
		return false
	}
	if f.now().After(v.expires) {
		delete(st.tokens, tok)
		return false
	}
	return v.agentID == agentID && v.port == port &&
		subtle.ConstantTimeCompare([]byte(v.agentID), []byte(agentID)) == 1
}

// dropPreviewTokens revokes every token of an agent that is going away.
func (f *Fleet) dropPreviewTokens(agentID string) {
	st := f.previewTokens()
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, v := range st.tokens {
		if v.agentID == agentID {
			delete(st.tokens, k)
		}
	}
}

// previewPortsFor is the agent's declared preview ports, empty when it has
// no workspace or the workspace cannot be resolved.
func (f *Fleet) previewPortsFor(ctx context.Context, a Agent) []int {
	_, doc, ok, err := f.AgentWorkspaceDoc(ctx, a)
	if err != nil || !ok {
		return nil
	}
	return doc.previewPorts()
}

// previewCookieName is mp_<agent>_<port>.
func previewCookieName(agentID string, port int) string {
	return fmt.Sprintf("mp_%s_%d", agentID, port)
}

// parsePreviewPath splits /preview/<id>/<port>/<rest>.
func parsePreviewPath(p string) (agentID string, port int, rest string, ok bool) {
	s := strings.TrimPrefix(p, previewPrefix)
	if s == p {
		return "", 0, "", false
	}
	id, after, found := strings.Cut(s, "/")
	if !found || id == "" {
		return "", 0, "", false
	}
	portStr, rest, _ := strings.Cut(after, "/")
	n, err := strconv.Atoi(portStr)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, "", false
	}
	return id, n, "/" + rest, true
}

func (s *Server) issuePreview(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid port"})
		return
	}
	u, err := s.fleet.IssuePreview(r.Context(), r.PathValue("id"), port)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

// preview authenticates a /preview/… request by token or cookie and then
// forwards it. Every failure is a plain 404, so a probe learns nothing
// about which agents or ports exist.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	notFound := func() { http.NotFound(w, r) }
	if s.fleet == nil {
		notFound()
		return
	}
	id, port, rest, ok := parsePreviewPath(r.URL.Path)
	if !ok {
		notFound()
		return
	}
	prefix := fmt.Sprintf("%s%s/%d/", previewPrefix, url.PathEscape(id), port)
	name := previewCookieName(id, port)

	if tok := r.URL.Query().Get("t"); tok != "" {
		if !s.fleet.previewTokenValid(tok, id, port) {
			notFound()
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: tok, Path: prefix,
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
			Expires: s.fleet.now().Add(previewTokenTTL),
		})
		q := r.URL.Query()
		q.Del("t")
		dest := r.URL.EscapedPath()
		if !strings.HasSuffix(dest, "/") && rest == "/" {
			dest += "/"
		}
		if enc := q.Encode(); enc != "" {
			dest += "?" + enc
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, dest, http.StatusFound)
		return
	}
	c, err := r.Cookie(name)
	if err != nil || !s.fleet.previewTokenValid(c.Value, id, port) {
		notFound()
		return
	}
	if rest == "/" && !strings.HasSuffix(r.URL.Path, "/") {
		// /preview/<id>/<port> without the trailing slash.
		http.Redirect(w, r, prefix, http.StatusFound)
		return
	}
	if s.fleet.previewForward != nil {
		s.fleet.previewForward(w, r, id, port, rest)
		return
	}
	s.fleet.forwardPreview(w, r, id, port, rest)
}

// scrubPreviewRequest removes what must not reach the previewed app: the
// bridge's preview cookies and any Authorization header.
func scrubPreviewRequest(h http.Header) {
	h.Del("Authorization")
	cookies := h.Values("Cookie")
	if len(cookies) == 0 {
		return
	}
	h.Del("Cookie")
	var kept []string
	for _, line := range cookies {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" || strings.HasPrefix(part, "mp_") {
				continue
			}
			kept = append(kept, part)
		}
	}
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// forwardPreview reverse-proxies to the agent's port: through the egress
// sidecar for a container agent, straight to loopback for a process one.
func (f *Fleet) forwardPreview(w http.ResponseWriter, r *http.Request, agentID string, port int, rest string) {
	rt, err := f.runtimeForAgent(agentID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := &url.URL{Scheme: "http"}
	path := rest
	var extra http.Header
	if rt.child != nil && rt.child.Containerized {
		h := f.egress
		if h == nil || h.mode != egressModeContainer || h.previewPort.Load() == 0 {
			http.Error(w, "preview is unavailable: the egress sidecar has no preview listener", http.StatusBadGateway)
			return
		}
		target.Host = fmt.Sprintf("127.0.0.1:%d", h.previewPort.Load())
		path = fmt.Sprintf("/%s/%d%s", agentID, port, rest)
		extra = http.Header{previewAuthHeader: []string{h.tokenFor(agentID)}}
	} else {
		target.Host = fmt.Sprintf("127.0.0.1:%d", port)
	}
	rp := &httputil.ReverseProxy{
		Director: func(out *http.Request) {
			out.URL.Scheme, out.URL.Host = target.Scheme, target.Host
			out.URL.Path = path
			out.URL.RawPath = ""
			out.Host = target.Host
			scrubPreviewRequest(out.Header)
			for k, v := range extra {
				out.Header[k] = v
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Default().Debug("webbridge: preview forward failed", "agent", agentID, "port", port, "err", err)
			http.Error(w, "preview: nothing is answering on that port", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}
