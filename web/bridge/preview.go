package bridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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
	// ports caches each agent's declared ports for previewPortsTTL, so a
	// page's many requests do not each resolve the workspace.
	ports map[string]previewPortsEntry
}

type previewPortsEntry struct {
	ports []int
	at    time.Time
}

// previewPortsTTL bounds how long a workspace that drops a port keeps
// serving it to tokens issued earlier.
const previewPortsTTL = 10 * time.Second

// agentIDRe is the characters an agent id may have to appear in a cookie
// name and a URL path unescaped.
var agentIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (f *Fleet) previewTokens() *previewStore {
	f.previewOnce.Do(func() {
		f.previews = &previewStore{tokens: make(map[string]previewToken), ports: make(map[string]previewPortsEntry)}
	})
	return f.previews
}

func newPreviewToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// IssuePreview returns a preview URL for a declared port.
func (f *Fleet) IssuePreview(ctx context.Context, agentID string, port int) (string, error) {
	if !agentIDRe.MatchString(agentID) {
		return "", fmt.Errorf("%w: agent %s", ErrUnknownAgent, agentID)
	}
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

// previewPortDeclared reports whether the agent's workspace still declares
// the port, from a short-lived cache. Tokens are checked against the
// declaration when issued; this catches a declaration removed since.
func (f *Fleet) previewPortDeclared(agentID string, port int) bool {
	st := f.previewTokens()
	now := f.now()
	st.mu.Lock()
	e, ok := st.ports[agentID]
	st.mu.Unlock()
	if !ok || now.Sub(e.at) > previewPortsTTL {
		a, found := f.ws.Agent(agentID)
		if !found {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		e = previewPortsEntry{ports: f.previewPortsFor(ctx, a), at: now}
		st.mu.Lock()
		st.ports[agentID] = e
		st.mu.Unlock()
	}
	for _, p := range e.ports {
		if p == port {
			return true
		}
	}
	return false
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
	delete(st.ports, agentID)
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
	if !found || !agentIDRe.MatchString(id) {
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
	pp := int(s.previewPort.Load())
	if pp == 0 {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "preview_unconfigured"})
		return
	}
	u, err := s.fleet.IssuePreview(r.Context(), r.PathValue("id"), port)
	if err != nil {
		writeErr(w, err)
		return
	}
	origin, tokenInHost := s.previewOrigin(r, pp, tokenOf(u))
	if tokenInHost {
		// The origin carries the credential, so the URL needs no ?t=.
		u = u[:strings.Index(u, "?t=")]
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": origin + u})
}

// SetPreviewPort records the port of the preview listener, so issued
// URLs point at that origin rather than the API's.
func (s *Server) SetPreviewPort(port int) { s.previewPort.Store(int32(port)) }

// PreviewHandler serves only /preview/…, for a listener of its own. The
// browser treats that port as a different origin from the API and UI, so a
// page an agent serves cannot read the UI's sessionStorage (which holds the
// bearer token) or call /api with it. Nothing but previews is reachable
// here.
func (s *Server) PreviewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, previewPrefix) {
			http.NotFound(w, r)
			return
		}
		s.preview(w, r)
	})
}

// previewOrigin is scheme://host:previewPort for the host the caller used
// to reach the bridge (or the configured public URL's).
//
// When that host is localhost (or a *.localhost name, which browsers resolve
// to loopback), the URL's host is also specific to the token, so each issued
// preview is its own origin and a page one agent serves cannot read the
// preview of another. The token rides in the host name there
// (p<token>.localhost) and is the credential: such an origin is cross-site to
// the Studio at localhost, so a cookie could not be set or sent in an
// embedded frame. tokenInHost reports that. Any other host cannot be
// subdivided without wildcard DNS, so previews share its origin and use the
// ?t= token and cookie.
func (s *Server) previewOrigin(r *http.Request, port int, tok string) (origin string, tokenInHost bool) {
	scheme, host := "http", r.Host
	if r.TLS != nil {
		scheme = "https"
	}
	if s.publicURLBase != "" {
		if u, err := url.Parse(s.publicURLBase); err == nil && u.Host != "" {
			scheme, host = u.Scheme, u.Host
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if isLocalhostName(host) {
		return fmt.Sprintf("%s://p%s.localhost:%d", scheme, tok, port), true
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // bare IPv6
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port), false
}

// tokenOf is the token in an issued preview path's ?t= query.
func tokenOf(u string) string {
	_, tok, _ := strings.Cut(u, "?t=")
	return tok
}

// isLocalhostName reports whether host (no port) is localhost or a
// subdomain of it.
func isLocalhostName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

// hostPreviewToken is the token a localhost-name request carries in its
// first host label (p<token>.localhost). local is true for any localhost
// name, where only that token is accepted: a cookie or ?t= is not.
func hostPreviewToken(reqHost string) (tok string, local bool) {
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if !isLocalhostName(host) {
		return "", false
	}
	label, rest, _ := strings.Cut(host, ".")
	if rest != "localhost" || len(label) < 2 || label[0] != 'p' {
		return "", true
	}
	return label[1:], true
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

	if hostTok, local := hostPreviewToken(r.Host); local {
		// The token in the host name is the credential; no cookie.
		if !s.fleet.previewTokenValid(hostTok, id, port) {
			notFound()
			return
		}
	} else {
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
	}
	if rest == "/" && !strings.HasSuffix(r.URL.Path, "/") {
		// /preview/<id>/<port> without the trailing slash.
		http.Redirect(w, r, prefix, http.StatusFound)
		return
	}
	if !s.fleet.previewPortDeclared(id, port) {
		notFound()
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
