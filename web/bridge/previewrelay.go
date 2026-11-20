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
// cannot read records nothing.
func (s *Server) recordPreviewRelay(host, issued string) {
	u, err := url.Parse(issued)
	if err != nil {
		return
	}
	agentID, port, _, ok := parsePreviewPath(u.Path)
	if !ok {
		return
	}
	tok, inHost := s.previewHostToken(u.Host)
	if !inHost {
		tok = u.Query().Get("t")
	}
	if tok == "" {
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

// previewRelayFor is the relay a request on this daemon's preview listener is
// for, if any. This daemon's own preview always wins: a token it issued itself
// is served here, and an agent it has is its own.
func (s *Server) previewRelayFor(r *http.Request) (previewRelay, bool) {
	agentID, port, _, ok := parsePreviewPath(r.URL.Path)
	if !ok {
		return previewRelay{}, false
	}
	now := s.previewRelayNow()
	if tok, inHost := s.previewHostToken(r.Host); inHost {
		if s.previewTokenValid(tok, agentID, port) {
			return previewRelay{}, false
		}
		return s.relayByToken(tok, now)
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
			// No X-Forwarded-*: the host is the only place this request goes,
			// and it decides everything else.
		},
		// The host's answer is passed back as it is, and a stream is flushed
		// as it is written.
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}
