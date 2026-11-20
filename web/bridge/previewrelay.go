package bridge

// previewrelay.go is the far end of a forwarded preview: a host that serves
// previews is reached with this daemon's own preview_url, so the link it issues
// comes back to this daemon's preview listener rather than to the host's. The
// forward records what the host issued, and the listener sends a request for a
// recorded link down the tunnel to the host that issued it (one-daemon open
// question 1).

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"marshal/web/bridge/contract"
)

// maxPreviewRelays bounds the relay table: a preview link is good for
// previewTokenTTL, and a daemon that only ever forwards previews must still not
// grow without bound. A full table drops its oldest entry.
const maxPreviewRelays = 1024

// maxPreviewIssueBody bounds what a preview-issue answer may carry: a host
// answers with the link it issued, and nothing larger is one this daemon
// records.
const maxPreviewIssueBody = 64 << 10

// maxPreviewRelayToken bounds the token a recorded link may carry: a preview
// token is 24 bytes of lower-case hex, and nothing longer than this is one this
// daemon could have issued.
const maxPreviewRelayToken = 128

// previewRelayDialTimeout bounds a relayed request's dial of the host's preview
// port on this machine, so a port that does not answer is given up on.
const previewRelayDialTimeout = 10 * time.Second

// previewRelayHeaderTimeout bounds a relayed request's wait for the host's
// first response header, and only that: once the headers are in, a preview's
// event stream or its websocket carries on streaming without a bound.
const previewRelayHeaderTimeout = 30 * time.Second

// previewRelayTransport carries the relays: one connection pool for every
// relayed preview, so a page's assets reuse the connection they opened, with
// the hop's wait for an answer bounded (a tunnel whose far end is wedged would
// otherwise park the relay's goroutine for as long as it stayed wedged). It is
// a variable so a test can bound the wait it watches.
var previewRelayTransport = newPreviewRelayTransport(previewRelayHeaderTimeout)

// newPreviewRelayTransport is the relay's transport, with headerTimeout as the
// wait for the host's first header.
func newPreviewRelayTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: previewRelayDialTimeout}).DialContext,
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       90 * time.Second,
		// The target is a plain HTTP connection to a loopback port.
		ForceAttemptHTTP2: false,
	}
}

// previewRelay is a preview link a forwarded host issued through this daemon's
// preview_url, and what this daemon needs to send a request for it back to that
// host.
type previewRelay struct {
	Host    string // the forwarded host's name
	AgentID string
	Port    int
	Token   string
	InHost  bool // the token is in the link's host
	Expires time.Time
}

// previewRelayKey is the table's agent key: an agent ID and port.
func previewRelayKey(agentID string, port int) string {
	return agentID + "/" + strconv.Itoa(port)
}

// previewRelayNow is the clock the relay table reads: the fleet's, so a test's
// clock reaches it, and the wall clock on a server without a fleet.
func (s *Server) previewRelayNow() time.Time {
	if s.fleet != nil {
		return s.fleet.now()
	}
	return time.Now()
}

// recordPreviewRelay records the link a forwarded host issued, so a request for
// it on this daemon's preview listener is relayed back to that host. A link it
// cannot read records nothing, and a link that does not name this daemon's own
// preview origin records nothing either: a host issues its link through the
// preview_url this daemon gave it, so an entry for any other origin would be a
// host planting a relay for a link that is not its own.
func (s *Server) recordPreviewRelay(host, issued string) {
	u, err := url.Parse(issued)
	if err != nil {
		return
	}
	agentID, port, _, ok := parsePreviewPath(u.Path)
	if !ok {
		return
	}
	tok, inHost, ok := s.recordablePreviewOrigin(u)
	if !ok || !usableRelayToken(tok) {
		return
	}
	now := s.previewRelayNow()
	rel := previewRelay{
		Host:    host,
		AgentID: agentID,
		Port:    port,
		Token:   tok,
		InHost:  inHost,
		Expires: now.Add(previewTokenTTL),
	}
	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	s.dropExpiredRelays(now)
	if _, exists := s.relaysByToken[tok]; !exists {
		for len(s.relaysByToken) >= maxPreviewRelays {
			s.dropOldestRelay()
		}
	}
	s.relaysByToken[tok] = rel
	s.relaysByAgent[previewRelayKey(agentID, port)] = rel
}

// recordablePreviewOrigin reads the token an issued link carries in its own
// origin, and reports whether that origin is this daemon's preview origin:
// this daemon's preview_url template with the token in its host, or, for a
// plain preview_url, the same scheme, host and port with the token in ?t=.
// inHost reports that the token is in the link's host.
func (s *Server) recordablePreviewOrigin(u *url.URL) (tok string, inHost bool, ok bool) {
	if s.previewURL == "" {
		return "", false, false
	}
	if t, templated, err := parsePreviewTemplate(s.previewURL); templated && err == nil {
		tok, ok := t.Token(u.Host)
		return tok, ok, ok
	}
	base, err := url.Parse(s.previewURL)
	if err != nil || !samePreviewOrigin(u, base) {
		return "", false, false
	}
	tok = u.Query().Get("t")
	return tok, false, tok != ""
}

// samePreviewOrigin reports whether u names the origin base does: the scheme,
// the host and the port, a scheme's own default port standing for no port.
func samePreviewOrigin(u, base *url.URL) bool {
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Hostname(), base.Hostname()) {
		return false
	}
	return previewOriginPort(u) == previewOriginPort(base)
}

// previewOriginPort is the port u names, its scheme's default when it names
// none, and -1 for a port that is not a number.
func previewOriginPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return -1
		}
		return n
	}
	return defaultPort(strings.ToLower(u.Scheme))
}

// usableRelayToken reports whether tok is a token this daemon could have
// issued: lower-case hex, and no longer than a client sends. A token that is
// anything else is not recorded.
func usableRelayToken(tok string) bool {
	return len(tok) <= maxPreviewRelayToken && isLowerHex(tok)
}

// previewRelayFor is the relay a request on this daemon's preview listener is
// for, if any. This daemon's own preview always wins: a token it issued itself
// is served here, and an agent it runs is its own.
//
// A token is the credential a link carries, whether it rides in the host, in
// ?t= or in the preview cookie, and all three are the same token, so a request
// that carries one is resolved through the table keyed by that token and then
// held to the path it names: an entry recorded for one agent's link is never
// served for another agent's path. Only a request with no credential at all
// falls back to the table keyed by agent and port.
func (s *Server) previewRelayFor(r *http.Request) (previewRelay, bool) {
	agentID, port, _, ok := parsePreviewPath(r.URL.Path)
	if !ok {
		return previewRelay{}, false
	}
	now := s.previewRelayNow()
	if tok, inHost := s.previewHostToken(r.Host); inHost {
		// The host is where the credential is, and on a localhost name it is
		// the only place one is accepted.
		return s.relayForCredential(tok, agentID, port, now)
	}
	if tok := r.URL.Query().Get("t"); tok != "" {
		return s.relayForCredential(tok, agentID, port, now)
	}
	if c, err := r.Cookie(previewCookieName(agentID, port)); err == nil && c.Value != "" {
		return s.relayForCredential(c.Value, agentID, port, now)
	}
	if s.fleet != nil {
		if _, exists := s.fleet.ws.Agent(agentID); exists {
			// The daemon runs this agent, so its preview is served here,
			// whatever a relay recorded for the same agent and port.
			return previewRelay{}, false
		}
	}
	return s.relayByAgent(agentID, port, now)
}

// relayForCredential is the recorded relay whose token is tok, for the agent
// and port the request's path names. A token this daemon issued is its own
// preview, served here rather than relayed, and a token recorded for another
// agent or port is not this path's to serve.
func (s *Server) relayForCredential(tok, agentID string, port int, now time.Time) (previewRelay, bool) {
	if tok == "" || s.previewTokenValid(tok, agentID, port) {
		return previewRelay{}, false
	}
	rel, ok := s.relayByToken(tok, now)
	if !ok || rel.AgentID != agentID || rel.Port != port {
		return previewRelay{}, false
	}
	return rel, true
}

// previewTokenValid reports whether tok is a token this daemon issued for the
// agent and port.
func (s *Server) previewTokenValid(tok, agentID string, port int) bool {
	return s.fleet != nil && s.fleet.previewTokenValid(tok, agentID, port)
}

// relayByToken is the unexpired relay recorded under tok.
func (s *Server) relayByToken(tok string, now time.Time) (previewRelay, bool) {
	if tok == "" {
		return previewRelay{}, false
	}
	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	rel, ok := s.relaysByToken[tok]
	if !ok {
		return previewRelay{}, false
	}
	if now.After(rel.Expires) {
		s.forgetRelay(rel)
		return previewRelay{}, false
	}
	return rel, true
}

// relayByAgent is the unexpired relay recorded for the agent and port.
func (s *Server) relayByAgent(agentID string, port int, now time.Time) (previewRelay, bool) {
	key := previewRelayKey(agentID, port)
	s.relaysMu.Lock()
	defer s.relaysMu.Unlock()
	rel, ok := s.relaysByAgent[key]
	if !ok {
		return previewRelay{}, false
	}
	if now.After(rel.Expires) {
		s.forgetRelay(rel)
		return previewRelay{}, false
	}
	return rel, true
}

// forgetRelay drops a relay from both tables. The caller holds relaysMu.
func (s *Server) forgetRelay(rel previewRelay) {
	delete(s.relaysByToken, rel.Token)
	key := previewRelayKey(rel.AgentID, rel.Port)
	if cur, ok := s.relaysByAgent[key]; ok && cur.Token == rel.Token {
		delete(s.relaysByAgent, key)
	}
}

// dropExpiredRelays drops every relay that is past its expiry. The caller holds
// relaysMu.
func (s *Server) dropExpiredRelays(now time.Time) {
	for _, rel := range s.relaysByToken {
		if now.After(rel.Expires) {
			s.forgetRelay(rel)
		}
	}
	for key, rel := range s.relaysByAgent {
		if now.After(rel.Expires) {
			delete(s.relaysByAgent, key)
		}
	}
}

// dropOldestRelay drops the relay that expires first, from both tables. The
// caller holds relaysMu and the table is not empty.
func (s *Server) dropOldestRelay() {
	var oldest previewRelay
	found := false
	for _, rel := range s.relaysByToken {
		if !found || rel.Expires.Before(oldest.Expires) {
			oldest, found = rel, true
		}
	}
	if found {
		s.forgetRelay(oldest)
	}
}

// recordForwardedPreview is the forward's ModifyResponse: it notes the link a
// host issued for a preview, so a browser that follows it back to this daemon's
// preview_url reaches the host's preview listener through the relay. The bytes
// the host answered reach the caller unchanged.
func (s *Server) recordForwardedPreview(host string) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		rest := resp.Body
		body, err := io.ReadAll(io.LimitReader(rest, maxPreviewIssueBody+1))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), rest), rest}
		if err != nil || len(body) > maxPreviewIssueBody {
			// The answer is not one this daemon reads: the caller still gets
			// every byte the host wrote, and nothing is recorded.
			return nil
		}
		var issued struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(body, &issued) != nil || issued.URL == "" {
			return nil
		}
		s.recordPreviewRelay(host, issued.URL)
		return nil
	}
}

// isPreviewIssuePath reports whether a forward's remainder is this daemon's
// preview-issue route, api/agents/<id>/preview/<port>.
func isPreviewIssuePath(p string) bool {
	rest, ok := strings.CutPrefix(p, "api/agents/")
	if !ok {
		return false
	}
	id, after, ok := strings.Cut(rest, "/preview/")
	if !ok || !agentIDRe.MatchString(id) {
		return false
	}
	n, err := strconv.Atoi(after)
	return err == nil && n >= 1 && n <= 65535
}

// relayPreview serves a recorded forwarded preview from the host's own preview
// listener, through the tunnel to it. Every failure is a plain 404, as every
// other preview failure is.
func (s *Server) relayPreview(w http.ResponseWriter, r *http.Request, rel previewRelay) {
	host, ok := reportedHost(s.reportedHosts(), rel.Host)
	if !ok || host.State != contract.HostLive || host.PreviewPort <= 0 {
		http.NotFound(w, r)
		return
	}
	target := "127.0.0.1:" + strconv.Itoa(host.PreviewPort)
	proxy := &httputil.ReverseProxy{
		// The hop's own transport: the wait for the host to answer at all is
		// bounded, so a wedged tunnel's far end cannot park this goroutine.
		Transport: previewRelayTransport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host = "http", target
			// The path, the query and the cookies are the browser's: the
			// host's listener authenticates the request the way it issued it.
			if rel.InHost {
				// The token in the host is the credential the host reads.
				out.Host = "p" + rel.Token + ".localhost"
			} else {
				// A shared-origin link keeps its ?t= and its cookie path, and
				// those are scoped to the host the browser asked for.
				out.Host = pr.In.Host
			}
			// The caller's credentials are the caller's: a proxy's Basic or
			// bearer token is not the preview's, and the host authenticates
			// the link's own credential instead. Cookies stay — they are the
			// preview's own auth.
			out.Header.Del("Authorization")
			// No X-Forwarded-*: the host is the only place this request goes,
			// and it decides everything else.
		},
		// Every preview failure is a plain 404, a relayed one included.
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			http.NotFound(w, r)
		},
		// The proxied error carries the request's URL, its ?t= token included,
		// so the hop logs nothing rather than the token to the daemon's log.
		ErrorLog: log.New(io.Discard, "", 0),
		// The host's answer is passed back as it is, and a stream is flushed
		// as it is written.
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}
