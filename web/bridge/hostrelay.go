package bridge

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
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
		r.hosts[name] = h
	}
	return r, nil
}

// SetHostRelay makes s a relay: the join route on the network listener, and
// the forward and hosts routes behind the bearer check.
func (s *Server) SetHostRelay(r *HostRelay) {
	s.relay = r
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

	mu sync.Mutex
	// instance is the id of the process that currently holds this name, and
	// since is when it joined. lastContact is when it last joined or answered
	// a forwarded request (D16).
	instance    string
	since       time.Time
	lastContact time.Time
	// queue holds joined connections no request has taken yet.
	queue []net.Conn
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
	var took bool
	if h.instance != instance {
		h.instance = instance
		h.since = time.Now()
		superseded, h.queue = h.queue, nil
		took = true
	}
	h.lastContact = time.Now()
	h.queue = append(h.queue, conn)
	h.mu.Unlock()

	if took {
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
			return nil, fmt.Errorf("%s is not connected", h.name)
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
}

// newRelayConn wraps a hijacked connection and the reader the hijack left.
func newRelayConn(conn net.Conn, r *bufio.Reader) *relayConn {
	return &relayConn{Conn: conn, r: r}
}

// Read reads the buffered bytes first, then the socket.
func (c *relayConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
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
