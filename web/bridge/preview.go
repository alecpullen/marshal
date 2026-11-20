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

	"marshal/web/bridge/contract"
)

// previewTokenTTL is how long a preview token stays valid.
const previewTokenTTL = 12 * time.Hour

// previewPrefix is the path prefix of preview traffic. It sits outside
// /api, so nothing in front of the mux guards it: the handler checks a token
// or cookie.
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

// previewHostLabelRe is a DNS label as a name may carry it: letters, digits
// and hyphens. url.Parse is laxer (a `*`, `<` or `>` is a host byte to it), and
// a wildcard belongs in the deployment's DNS, not in a template.
var previewHostLabelRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

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
	if h, ok := previewOriginFrom(r); ok {
		// The caller knows its own way in to the preview listener, so the
		// origin it names — scheme included — is the one the link takes, in
		// place of the preview URL and the request's host (one-daemon open
		// question 1). The request's own scheme says how it reached this
		// daemon, not how it will dial the origin it named.
		if h.templated {
			// A templated origin subdivides the caller's name per preview,
			// as a localhost one does: the token is the host's.
			origin, tokenInHost = h.template.Origin(tokenOf(u)), true
		} else {
			origin, tokenInHost = previewOriginOn(h.scheme, h.host, h.port, tokenOf(u))
		}
	}
	if tokenInHost {
		// The origin carries the credential, so the URL needs no ?t=.
		u = u[:strings.Index(u, "?t=")]
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": origin + u})
}

// SetPreviewPort records the port of the preview listener, so issued
// URLs point at that origin rather than the API's.
func (s *Server) SetPreviewPort(port int) { s.previewPort.Store(int32(port)) }

// SetPreviewAddr records the preview listener's bound address, which
// GET /api/daemon reports. It also sets the preview port, as SetPreviewPort
// did.
func (s *Server) SetPreviewAddr(addr *net.TCPAddr) {
	if addr == nil {
		return
	}
	bound := addr.String()
	s.previewAddr.Store(&bound)
	s.SetPreviewPort(addr.Port)
}

// CheckPreviewURL refuses a base that is not an http or https origin: a host,
// and no userinfo, no path beyond "/", no query and no fragment. A template —
// the same origin with {token} once in its first host label — is accepted
// too, and is what gives each issued preview an origin of its own.
func CheckPreviewURL(base string) error {
	if _, ok, err := parsePreviewTemplate(base); ok || err != nil {
		return err
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("preview URL %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("preview URL %q: scheme is not http or https", base)
	}
	if u.Host == "" {
		return fmt.Errorf("preview URL %q: no host", base)
	}
	if u.User != nil {
		// An origin's host is all a browser sends: userinfo is discarded, so
		// a link that carried it would not be the origin it names.
		return fmt.Errorf("preview URL %q: an origin carries no userinfo", base)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("preview URL %q: an origin carries no path", base)
	}
	if u.RawQuery != "" {
		return fmt.Errorf("preview URL %q: an origin carries no query", base)
	}
	if u.Fragment != "" {
		return fmt.Errorf("preview URL %q: an origin carries no fragment", base)
	}
	return nil
}

// SetPreviewURL makes issued preview links name base, after CheckPreviewURL,
// in place of the request's host and the preview port.
func (s *Server) SetPreviewURL(base string) error {
	base = strings.TrimSuffix(base, "/")
	if err := CheckPreviewURL(base); err != nil {
		return err
	}
	s.previewURL = base
	return nil
}

// previewTokenMarker is the placeholder a preview_url template carries where
// each issued preview's token goes.
const previewTokenMarker = "{token}"

// previewHostTemplate is a preview_url with {token} in its host:
// Scheme://Prefix<token>Suffix[:Port].
type previewHostTemplate struct {
	Scheme, Prefix, Suffix, Port string
}

// parsePreviewTemplate reads base as a template, and ok is false when base
// has no {token}. A {token} anywhere but once in the first host label is an
// error.
//
// Only the leftmost label may carry it: the token has to survive DNS, and
// the leftmost label is the one every deployment can point at this daemon
// (a wildcard name such as *.preview.example.com). Preview tokens are
// lower-case hex, so they need no escaping in a name.
func parsePreviewTemplate(base string) (t previewHostTemplate, ok bool, err error) {
	if !strings.Contains(base, previewTokenMarker) {
		return previewHostTemplate{}, false, nil
	}
	refuse := func(why string) (previewHostTemplate, bool, error) {
		return previewHostTemplate{}, false, fmt.Errorf("preview URL %q: %s", base, why)
	}
	if strings.Count(base, previewTokenMarker) > 1 {
		return refuse("{token} appears more than once")
	}
	_, after, hasScheme := strings.Cut(base, "://")
	if !hasScheme {
		return refuse("{token} is not in a host")
	}
	// The host is the text after :// and before the next /, : or the end.
	host := after
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	label, afterFirst, _ := strings.Cut(host, ".")
	if !strings.Contains(label, previewTokenMarker) {
		return refuse("{token} is not in the first host label")
	}
	prefix, tail, _ := strings.Cut(label, previewTokenMarker)
	// The suffix is the rest of the name: what follows {token} in its own
	// label, and every label after it.
	suffix := tail
	if afterFirst != "" {
		suffix += "." + afterFirst
	}
	// Every label has to be one a name can carry: CheckPreviewURL's own parse
	// is laxer than that (`*`, `<` and `>` are host bytes to url.Parse), and
	// the port, when there is one, has to be in range — which url.Parse does
	// not check either.
	for _, l := range strings.Split(strings.Replace(host, previewTokenMarker, "0", 1), ".") {
		if !previewHostLabelRe.MatchString(l) {
			return refuse(fmt.Sprintf("host label %q is not letters, digits and hyphens", l))
		}
	}
	// Everything else about the base is CheckPreviewURL's business: it has
	// to be an origin once the placeholder is filled in. Its answer names the
	// filled-in URL, so restate it against the base the operator wrote.
	checked := strings.Replace(base, previewTokenMarker, "0", 1)
	if err := CheckPreviewURL(checked); err != nil {
		why := strings.TrimPrefix(err.Error(), fmt.Sprintf("preview URL %q: ", checked))
		return refuse(why)
	}
	u, err := url.Parse(checked)
	if err != nil {
		return previewHostTemplate{}, false, fmt.Errorf("preview URL %q: %w", base, err)
	}
	if ps := u.Port(); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n < 1 || n > 65535 {
			return refuse(fmt.Sprintf("port %s is not in 1-65535", ps))
		}
	}
	return previewHostTemplate{
		Scheme: u.Scheme,
		Prefix: strings.ToLower(prefix),
		Suffix: strings.ToLower(suffix),
		Port:   u.Port(),
	}, true, nil
}

// Origin is the template with token in place.
func (t previewHostTemplate) Origin(token string) string {
	host := t.Prefix + token + t.Suffix
	if t.Port != "" {
		host += ":" + t.Port
	}
	return t.Scheme + "://" + host
}

// Token reads a token from a request's host (any port), when the host
// matches the template. Preview tokens are lower-case hex, so a host whose
// middle label is anything else is not one of ours.
func (t previewHostTemplate) Token(host string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasPrefix(host, t.Prefix) || !strings.HasSuffix(host, t.Suffix) {
		return "", false
	}
	if len(host) < len(t.Prefix)+len(t.Suffix) {
		return "", false
	}
	tok := host[len(t.Prefix) : len(host)-len(t.Suffix)]
	if !isLowerHex(tok) {
		return "", false
	}
	return tok, true
}

// isLowerHex reports whether s is non-empty lower-case hexadecimal.
func isLowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return s != ""
}

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
		if rel, ok := s.previewRelayFor(r); ok {
			// A preview a forwarded host issued through this daemon's
			// preview_url is served by that host, through its tunnel
			// (previewrelay.go). Anything else is this daemon's own.
			s.relayPreview(w, r, rel)
			return
		}
		s.preview(w, r)
	})
}

// previewOrigin is scheme://host:previewPort for the host the caller used
// to reach the bridge (or the configured public URL's).
//
// When that host is localhost (or a *.localhost name, which browsers resolve
// to loopback, or a loopback IP such as the 127.0.0.1 the bridge prints), the URL's host is also specific to the token, so each issued
// preview is its own origin and a page one agent serves cannot read the
// preview of another. The token rides in the host name there
// (p<token>.localhost) and is the credential: such an origin is cross-site to
// the Studio at localhost, so a cookie could not be set or sent in an
// embedded frame. tokenInHost reports that. Any other host cannot be
// subdivided without wildcard DNS, so previews share its origin and use the
// ?t= token and cookie.
//
// A configured public origin (SetPreviewURL) is answered first: the link
// names it whatever host the caller used, and carries the ?t= token. When
// that origin is a template, each preview gets an origin of its own from it
// instead, with the token in the host.
func (s *Server) previewOrigin(r *http.Request, port int, tok string) (origin string, tokenInHost bool) {
	if s.previewURL != "" {
		if t, ok, err := parsePreviewTemplate(s.previewURL); ok && err == nil {
			return t.Origin(tok), true
		}
		return s.previewURL, false
	}
	scheme, host := requestScheme(r), r.Host
	if s.publicURLBase != "" {
		if u, err := url.Parse(s.publicURLBase); err == nil && u.Host != "" {
			scheme, host = u.Scheme, u.Host
		}
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return previewOriginOn(scheme, host, port, tok)
}

// previewOriginOn is the preview origin on host:port, with the token in the
// host name when that host is a localhost name (and tokenInHost true), and in
// the ?t= query otherwise. See previewOrigin for why.
func previewOriginOn(scheme, host string, port int, tok string) (origin string, tokenInHost bool) {
	if isLocalhostName(host) {
		return fmt.Sprintf("%s://p%s.localhost:%d", scheme, tok, port), true
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // bare IPv6
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port), false
}

// requestScheme is the scheme the caller reached this server with.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// previewOriginHeader is what a caller's PreviewOriginHeader names: either a
// template, which gives each preview an origin of its own, or one plain origin
// its previews share.
type previewOriginHeader struct {
	scheme, host string
	port         int
	template     previewHostTemplate
	templated    bool
}

// previewOriginFrom reads PreviewOriginHeader. It accepts a template (a
// preview_url with {token} once in its first host label), or an http or https
// origin with a host, with or without a port, and no path, query or fragment.
// ok is false for anything else, and the header is then ignored whole. The
// scheme is returned as url.Parse normalises it, because it decides how the
// caller will dial the origin it named — which is not necessarily how the
// request reached this daemon.
func previewOriginFrom(r *http.Request) (h previewOriginHeader, ok bool) {
	raw := r.Header.Get(contract.PreviewOriginHeader)
	if raw == "" {
		return previewOriginHeader{}, false
	}
	if t, templated, err := parsePreviewTemplate(raw); templated || err != nil {
		if err != nil {
			return previewOriginHeader{}, false
		}
		return previewOriginHeader{template: t, templated: true}, true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return previewOriginHeader{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return previewOriginHeader{}, false
	}
	if u.Path != "" && u.Path != "/" {
		return previewOriginHeader{}, false
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return previewOriginHeader{}, false
	}
	host := u.Hostname()
	if host == "" {
		return previewOriginHeader{}, false
	}
	n := defaultPort(u.Scheme)
	if rawPort := u.Port(); rawPort != "" {
		n, err = strconv.Atoi(rawPort)
		if err != nil || n < 1 || n > 65535 {
			return previewOriginHeader{}, false
		}
	}
	return previewOriginHeader{scheme: u.Scheme, host: host, port: n}, true
}

// defaultPort is the port a browser dials for scheme.
func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// tokenOf is the token in an issued preview path's ?t= query.
func tokenOf(u string) string {
	_, tok, _ := strings.Cut(u, "?t=")
	return tok
}

// isLocalhostName reports whether host (no port) is localhost, a subdomain
// of it, or a loopback address.
func isLocalhostName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() // 127.0.0.1 and ::1 are the same machine
	}
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

// previewHostToken reads a token from a request's host: this daemon's
// preview_url template, or the localhost rule hostPreviewToken has. inHost
// reports that the host is where this request's credential is, so no cookie or
// ?t= need be consulted (and for a localhost name, none is accepted).
//
// The template is read first, because it is the operator's own statement of
// what a host names. A template that ends in .localhost — the natural
// no-wildcard-DNS shape, https://{token}.preview.localhost — is a host the
// localhost rule has no token for (it knows only p<token>.localhost), so
// reading that rule first would issue links nothing can authenticate.
func (s *Server) previewHostToken(host string) (tok string, inHost bool) {
	if s.previewURL != "" {
		if t, ok, err := parsePreviewTemplate(s.previewURL); ok && err == nil {
			if tok, ok := t.Token(host); ok {
				return tok, true
			}
		}
	}
	return hostPreviewToken(host)
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

	if hostTok, inHost := s.previewHostToken(r.Host); inHost {
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
		w.Header().Set("Referrer-Policy", "no-referrer")
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
		if h == nil || h.previewPort.Load() == 0 {
			http.Error(w, "preview is unavailable: the egress sidecar has no preview listener", http.StatusBadGateway)
			return
		}
		target.Host = fmt.Sprintf("127.0.0.1:%d", h.previewPort.Load())
		path = fmt.Sprintf("/%s/%d%s", agentID, port, rest)
		extra = http.Header{previewAuthHeader: []string{h.tokenFor(agentID)}}
	} else {
		target.Host = fmt.Sprintf("127.0.0.1:%d", port)
	}
	_, tokenInHost := hostPreviewToken(r.Host)
	rp := &httputil.ReverseProxy{
		ModifyResponse: func(resp *http.Response) error {
			if tokenInHost {
				// The origin holds the credential, so keep it out of the
				// Referer the previewed page sends to other sites.
				resp.Header.Set("Referrer-Policy", "no-referrer")
			}
			return nil
		},
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
