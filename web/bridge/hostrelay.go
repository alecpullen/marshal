package bridge

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// The relay's wire names (P8 D11–D16). They are shared with the joiner, which
// sends a join, and with the forward route, which names the host on the hop.
const (
	relayJoinPath      = "/api/relay/join"
	relayHostsPath     = "/api/relay/hosts"
	relayForwardPrefix = "/api/hosts/"
	hostTokenHeader    = "X-Marshal-Host-Token"
	relayedHeader      = "X-Marshal-Relayed"
	joinNameHeader     = "X-Marshal-Join-Name"
	joinInstanceHeader = "X-Marshal-Join-Instance"
	joinUpgrade        = "marshal-relay/1"
)

// relayNameRe is fleet.toml's host-name rule (D11). web/ is standard library
// only, so internal/fleetprofile's serverNameRe cannot be imported: the rule
// is copied here, as the bridge copies every other rule it shares with it.
var relayNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// The relay's limits, with their defaults (decision 3). They are constants
// only so that the fields which carry them have somewhere to start; a test
// sets its own on the relay it builds, never these.
const (
	// relayWait is how long a dial waits for a joined connection before it
	// reports its host as not connected (D14).
	relayWait = 5 * time.Second
	// relayCap bounds the requests the relay sends over one host's
	// connections at once, and so how many joined connections it will use
	// (D14).
	relayCap = 32
)

// HostRelay is a relay's table of the hosts that may join it (D11).
type HostRelay struct {
	// hosts is written once, by NewHostRelay, and read-only after it, so a
	// lookup needs no lock. names holds the same set in name order, which is
	// the order the hosts route lists it in (D16).
	hosts map[string]*relayHost
	names []string
	// wait and cap are the relay's limits (decision 3).
	wait time.Duration
	cap  int
}

// NewHostRelay builds the table from each name's join secret. A name that
// breaks the name rule, or an empty secret, is refused.
func NewHostRelay(secrets map[string]string) (*HostRelay, error) {
	r := &HostRelay{
		hosts: make(map[string]*relayHost, len(secrets)),
		names: make([]string, 0, len(secrets)),
		wait:  relayWait,
		cap:   relayCap,
	}
	for name := range secrets {
		r.names = append(r.names, name)
	}
	sort.Strings(r.names)
	for _, name := range r.names {
		secret := secrets[name]
		if !relayNameRe.MatchString(name) {
			return nil, fmt.Errorf("relay host name %q is not [A-Za-z0-9._-]", name)
		}
		if secret == "" {
			return nil, fmt.Errorf("relay host %q has no join secret", name)
		}
		h := &relayHost{relay: r, name: name, secret: secret, fresh: make(chan struct{}, 1)}
		h.transport = h.newTransport()
		h.proxy = h.newProxy()
		r.hosts[name] = h
	}
	return r, nil
}

// SetHostRelay makes s a relay: the join route on the network listener, and
// the forward and hosts routes behind the bearer check. Both new routes are
// /api/ routes, so the bearer check the mux is wrapped in covers them as it
// covers every other (D15, D16); a bridge that relays nothing registers
// neither, so it answers each as it answers any path it does not serve.
func (s *Server) SetHostRelay(r *HostRelay) {
	s.relay = r
	s.mux.HandleFunc("GET "+relayHostsPath, s.relayHosts)
	s.mux.Handle(relayForwardPrefix, http.HandlerFunc(s.relayForward))
}

// hostFor answers the entry for name, or nil when the table does not name it.
func (r *HostRelay) hostFor(name string) *relayHost {
	return r.hosts[name]
}

// relayHost is one name in the table: its join secret, the instance that
// currently holds it, and the joined connections no request has taken yet.
type relayHost struct {
	relay  *HostRelay
	name   string
	secret string
	// transport is the HTTP/1.1 client that sends this host's requests down
	// its joined connections (decision 6).
	transport *http.Transport
	// proxy is the forward that sends a caller's request down those
	// connections. It is built once, with the transport, and never per
	// request (D15).
	proxy *httputil.ReverseProxy

	mu sync.Mutex
	// instance is the id of the process that currently holds this name, and
	// since is when it joined. lastContact is when it last joined or answered
	// a forwarded request (D16).
	instance    string
	since       time.Time
	lastContact time.Time
	// queue holds joined connections no request has taken yet.
	queue []net.Conn
	// open counts the joined connections that are still open, which is what
	// the hosts route reports as connected (D16). It is optimistic: a joiner
	// that closes its last connection while nothing is being forwarded is only
	// noticed when a forward next takes one, so "connected" means "had a joined
	// connection the last time the relay looked".
	open int
	// fresh is signalled when a connection joins the queue, so a dial waiting
	// for one wakes without polling.
	fresh chan struct{}
}

// relayJoin answers the join route: a host asks for its connection to be
// reversed onto it (D13). It answers before the bearer check, as the Studio's
// sign-in exchange does, and only on the network listener, because a join over
// the owner's own socket is meaningless (decision 8).
func (s *Server) relayJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "a join is a POST"})
		return
	}
	// A reversed connection is an HTTP/1.1 upgrade: HTTP/2 has no hijack, so an
	// answer here could only be a malformed 101. The joiner offers only
	// http/1.1, but this route answers before the bearer check, so say no
	// plainly to anyone else.
	if r.ProtoMajor != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a join is an HTTP/1.1 upgrade"})
		return
	}
	// The upgrade is what makes the connection reversible, so a request
	// without it is not a join at all rather than one unauthenticated.
	if r.Header.Get("Connection") != "Upgrade" || r.Header.Get("Upgrade") != joinUpgrade {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a join is an upgrade to " + joinUpgrade})
		return
	}
	host := s.relay.hostFor(r.Header.Get(joinNameHeader))
	if host == nil {
		// An unknown name and a wrong secret answer alike, so the route is
		// not an oracle for which names the relay holds (D13).
		unauthorized(w)
		return
	}
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) ||
		subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(host.secret)) != 1 {
		unauthorized(w)
		return
	}
	instance := r.Header.Get(joinInstanceHeader)
	if instance == "" {
		// A join with no instance cannot be told from the one already here,
		// so it could never take the name back after a restart.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a join names its instance"})
		return
	}

	// The answer is written and flushed before the connection is taken over,
	// so the joiner reads a whole response before the stream reverses.
	w.Header().Set("Connection", "Upgrade")
	w.Header().Set("Upgrade", joinUpgrade)
	w.WriteHeader(http.StatusSwitchingProtocols)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}
	conn, buffered, err := rc.Hijack()
	if err != nil {
		return
	}
	// The connection is the relay's now: the listener's deadlines are dropped
	// so a joined connection is never cut off by the handshake's.
	_ = conn.SetDeadline(time.Time{})
	host.join(instance, newRelayConn(conn, buffered.Reader))
}

// join records a join by instance and queues its connection. A join from a new
// instance takes the name: the older instance's queued connections are closed
// and its idle keep-alive connections dropped before the new one can be taken,
// while a request already in flight on an older connection finishes (D14).
func (h *relayHost) join(instance string, conn net.Conn) {
	h.mu.Lock()
	var superseded []net.Conn
	var previous string
	var took bool
	if h.instance != instance {
		previous = h.instance
		h.instance = instance
		h.since = time.Now()
		superseded, h.queue = h.queue, nil
		took = true
	}
	h.lastContact = time.Now()
	if rc, ok := conn.(*relayConn); ok {
		// The name counts the connection while it is open, which is what the
		// hosts route reports as connected (D16).
		rc.host = h
	}
	h.open++
	h.queue = append(h.queue, conn)
	h.mu.Unlock()

	if took {
		// A name changing hands is the one relay event a reader cannot see
		// from the outside: two live daemons with one name flap it between
		// them, and every flip looks like a fresh join. Say so, once per
		// takeover (D14).
		slog.Default().Warn("bridge: a relayed host was taken over",
			"host", h.name, "instance", instance, "previous", previous)
		for _, c := range superseded {
			_ = c.Close()
		}
		// An idle connection carries no request, so dropping it takes nothing
		// from anyone; one in flight is not idle and stays until it finishes.
		h.transport.CloseIdleConnections()
	}
	h.signal()
}

// signal wakes a dial waiting for a connection.
func (h *relayHost) signal() {
	select {
	case h.fresh <- struct{}{}:
	default:
	}
}

// errHostNotConnected is a dial's report that the name had no joined
// connection within the relay's wait. It is the forward's 503, and it carries
// D14's words.
type errHostNotConnected struct{ name string }

func (e errHostNotConnected) Error() string { return e.name + " is not connected" }

// take hands the transport the next joined connection, waiting up to the
// relay's wait for one to arrive. A connection the joiner closed while it
// waited is skipped: its first write would fail, and taking it would spend a
// request to discover that (decision 5).
func (h *relayHost) take(ctx context.Context) (net.Conn, error) {
	timer := time.NewTimer(h.relay.wait)
	defer timer.Stop()
	for {
		h.mu.Lock()
		for len(h.queue) > 0 {
			conn := h.queue[0]
			h.queue = h.queue[1:]
			h.mu.Unlock()
			if relayConnOpen(conn) {
				return conn, nil
			}
			_ = conn.Close()
			h.mu.Lock()
		}
		h.mu.Unlock()

		select {
		case <-h.fresh:
		case <-timer.C:
			return nil, errHostNotConnected{name: h.name}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// newTransport builds the name's HTTP/1.1 client. Every connection it dials is
// one the host joined with, and the host in a request's URL names the relay,
// not a socket: the dial ignores it (decision 6).
func (h *relayHost) newTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return h.take(ctx)
		},
		// Keep-alives stay on, so a joined connection is reused after its
		// first request.
		DisableKeepAlives: false,
		MaxConnsPerHost:   h.relay.cap,
		// HTTP/2 is off: the connection is one reversed HTTP/1.1 stream, and
		// the joiner serves it with net/http's HTTP/1.1 server.
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// relayConn is a joined connection whose reader starts where the hijack's
// buffered reader does: the upgrade was read through that reader, so it may
// already hold bytes the request side must not skip (decision 4).
type relayConn struct {
	net.Conn
	r *bufio.Reader
	// host, when set, is the name this connection was joined for: it counts
	// the connection as open until it closes, which is what the hosts route
	// reports as connected (D16).
	host *relayHost
	// once closes over that count, so a connection both the transport and the
	// join route close leaves it once.
	once sync.Once
}

// newRelayConn wraps a hijacked connection and the reader the hijack left.
func newRelayConn(conn net.Conn, r *bufio.Reader) *relayConn {
	return &relayConn{Conn: conn, r: r}
}

// Read reads the buffered bytes first, then the socket.
func (c *relayConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

// Close closes the connection and drops it from the name's count of open
// connections. A connection the host closed reads as closed here too: the
// transport closes what it reads EOF on, and the queue closes what it skips.
func (c *relayConn) Close() error {
	if c.host != nil {
		c.once.Do(c.host.left)
	}
	return c.Conn.Close()
}

// left drops one closed connection from the name's count of open connections.
func (h *relayHost) left() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.open > 0 {
		h.open--
	}
}

// relayInvalidHost suffixes the URL host a forward is addressed to. Nothing
// resolves it: the transport's dial takes a joined connection and ignores the
// address it was given (decision 6).
const relayInvalidHost = ".relay.invalid"

// hopPath is the path a forward puts on the hop: the decoded path the host
// routes on, and the escaped form, so a caller's own escaping reaches the host
// as it was written.
type hopPath struct {
	path string
	raw  string
}

// hopPathKey carries a forward's resolved path from its route to the proxy's
// Rewrite.
type hopPathKey struct{}

// relayForward answers a forward: the caller's request goes down the name's
// joined connections and the host decides what to do with it (D15).
func (s *Server) relayForward(w http.ResponseWriter, r *http.Request) {
	name, rest, ok := splitForward(r.URL)
	if !ok {
		// Not a path this route forwards: a remainder outside the host's API,
		// or another relay's route, which would chain relays. It is not a
		// route at all, and reads as one.
		http.NotFound(w, r)
		return
	}
	host := s.relay.hostFor(name)
	if host == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no relayed host " + name})
		return
	}
	if r.Header.Get(hostTokenHeader) == "" {
		// The relay forwards nothing without the host's own token. Its own
		// token opens no daemon and a join secret only lets one offer itself,
		// so this 401 is the relay's, and carries no X-Marshal-Relayed (D15).
		unauthorized(w)
		return
	}
	host.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), hopPathKey{}, rest)))
}

// splitForward reads a forward path: the name it addresses, unescaped, and the
// path to send the host.
func splitForward(u *url.URL) (string, hopPath, bool) {
	escaped := u.EscapedPath()
	if !strings.HasPrefix(escaped, relayForwardPrefix) {
		return "", hopPath{}, false
	}
	escapedName, escapedPath, ok := strings.Cut(escaped[len(relayForwardPrefix):], "/")
	if !ok {
		return "", hopPath{}, false
	}
	name, err := url.PathUnescape(escapedName)
	if err != nil {
		return "", hopPath{}, false
	}
	path, err := url.PathUnescape(escapedPath)
	if err != nil {
		return "", hopPath{}, false
	}
	// A relay forwards the host's API and nothing else, and never another
	// relay's routes: relays do not chain (D15). The prefixes are written
	// here rather than taken from the route constants because they are
	// relative to the host, which is exactly what this path no longer holds.
	if !strings.HasPrefix(path, "api/") ||
		strings.HasPrefix(path, "api/hosts/") ||
		strings.HasPrefix(path, "api/relay/") {
		return "", hopPath{}, false
	}
	return name, hopPath{path: path, raw: escapedPath}, true
}

// newProxy builds the name's forward once: every request it carries goes down
// the name's joined connections (decision 6).
func (h *relayHost) newProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:      h.transport,
		Rewrite:        h.rewrite,
		ModifyResponse: h.relayed,
		ErrorHandler:   h.forwardFailed,
		// An event stream is flushed as it is written, so a caller sees each
		// event when the host writes it rather than when a buffer fills (D15).
		FlushInterval: -1,
	}
}

// rewrite puts the forward's path on the hop and gives the request the host's
// own credentials (D15). The URL's host names the relay, not a socket: the
// dial takes a joined connection and ignores the address (decision 6).
func (h *relayHost) rewrite(pr *httputil.ProxyRequest) {
	rest, _ := pr.In.Context().Value(hopPathKey{}).(hopPath)
	token := pr.In.Header.Get(hostTokenHeader)
	out := pr.Out
	out.URL = &url.URL{
		Scheme: "http",
		Host:   h.name + relayInvalidHost,
		// The forward's remainder is the host's path: /api/hosts/alpha/api/
		// agents reaches the host as /api/agents (D15).
		Path:     "/" + rest.path,
		RawPath:  "/" + rest.raw,
		RawQuery: pr.In.URL.RawQuery,
	}
	out.Host = ""
	// The host decides on its own token, not the caller's: the relay's bearer
	// value never crosses the hop.
	out.Header.Set("Authorization", "Bearer "+token)
	// The host token is the caller's credential and the cookie is the page's;
	// neither belongs to the host. No X-Forwarded-* is added (D15).
	out.Header.Del(hostTokenHeader)
	out.Header.Del("Cookie")
}

// relayed marks a forwarded response with the host that answered it, so a
// caller can tell the host's 401 from the relay's, and records the contact
// (D15, D16).
func (h *relayHost) relayed(res *http.Response) error {
	res.Header.Set(relayedHeader, h.name)
	h.mu.Lock()
	h.lastContact = time.Now()
	h.mu.Unlock()
	return nil
}

// forwardFailed answers a forwarded request the proxy could not complete. A
// dial that found no joined connection within the relay's wait is D14's 503;
// anything else is the relay's own gateway failure (decision 7).
func (h *relayHost) forwardFailed(w http.ResponseWriter, r *http.Request, err error) {
	var notConnected errHostNotConnected
	if errors.As(err, &notConnected) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": notConnected.Error()})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

// relayHosts answers GET /api/relay/hosts: every name the relay names, in name
// order, joined or not (D16).
func (s *Server) relayHosts(w http.ResponseWriter, r *http.Request) {
	out := contract.RelayHosts{Hosts: make([]contract.RelayHost, 0, len(s.relay.names))}
	for _, name := range s.relay.names {
		h := s.relay.hostFor(name)
		h.mu.Lock()
		entry := contract.RelayHost{Name: name, Connected: h.open > 0}
		if !h.since.IsZero() {
			entry.Since = h.since.UnixMilli()
		}
		if !h.lastContact.IsZero() {
			entry.LastContact = h.lastContact.UnixMilli()
		}
		h.mu.Unlock()
		out.Hosts = append(out.Hosts, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

// relayConnOpen reports whether a joined connection is still open, which is
// what its first write would discover: the joiner may have closed it while it
// waited in the queue (decision 5).
func relayConnOpen(conn net.Conn) bool {
	c, ok := conn.(*relayConn)
	if !ok {
		return true
	}
	_ = c.SetReadDeadline(time.Now())
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	if _, err := c.r.Peek(1); err != nil {
		// No data yet, and none coming: a timeout is a live connection with
		// nothing to say, and anything else is a closed one.
		var netErr net.Error
		return errors.As(err, &netErr) && netErr.Timeout()
	}
	return true
}
