package bridge

// hostjoin.go is the joiner: the half of P8's relay that dials out (D13, D14).
// A bridge behind NAT joins a relay by reversing a connection onto itself; from
// then on the relay is the HTTP client on it and this bridge the server, so a
// request that reaches the relay reaches this host like any other.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"marshal/web/bridge/contract"
)

// The joiner's limits, with their defaults (decision 3). They are constants only
// so that the fields which carry them have somewhere to start; a test sets its
// own on the joiner it builds, never these.
const (
	// joinSpare is how many joined connections the joiner keeps fresh: ones the
	// relay has not sent a request down yet (D14).
	joinSpare = 2
	// joinCap bounds the joined connections the joiner holds at once (D14).
	joinCap = 32
	// joinBackoffMin and joinBackoffMax bound the wait after a failed dial or
	// join, which doubles from the first to the second and resets on a 101
	// (D14).
	joinBackoffMin = time.Second
	joinBackoffMax = 30 * time.Second
	// joinKeepAlive is each joined connection's TCP keep-alive (D14).
	joinKeepAlive = 15 * time.Second
	// relayedStudioPrefix is the Studio's own surface. A relayed request must
	// not reach it: minting a sign-in code and handing over a token are what a
	// connection to this machine alone may ask for (D15).
	relayedStudioPrefix = "/api/studio/"
)

// JoinConfig is the relay a bridge joins (D12).
type JoinConfig struct {
	URL    string // an https origin, or http on loopback
	Name   string
	Secret string
	CAFile string // optional
}

// Joiner keeps a bridge joined to its relay (D13, D14). It dials the relay,
// reverses each connection onto itself, and serves a handler on the ones the
// relay sends requests down.
type Joiner struct {
	// url is the relay's origin as it was configured, target is the join route
	// on it, and addr is the host:port a dial reaches. tls is what a dial offers
	// over https, and nil for loopback http.
	url    string
	target string
	addr   string
	tls    *tls.Config
	// name, secret and instance are what a join presents (D13). The instance is
	// this process's own, so the relay can tell a restarted bridge from the one
	// that holds a name.
	name     string
	secret   string
	instance string
	// handler serves a joined connection, listener is where the joined
	// connections wait for it, and srv serves them.
	//
	// srv carries no ReadHeaderTimeout, unlike cmd/webbridge's listeners: a
	// spare connection is idle by design and can wait minutes for its first
	// request, so any header deadline closes it. The pool then redials for
	// ever, and the relay's queue fills with connections it has already been
	// sent a FIN on (the plan 3 record's run by hand). Only the relay writes
	// to a joined connection, and it is trusted as much as the daemons that
	// join it (D15), so the deadline buys nothing.
	handler  http.Handler
	listener *joinListener
	srv      *http.Server

	// spare, cap, backoffMin, backoffMax and keepAlive are the joiner's limits
	// (decision 3).
	spare      int
	cap        int
	backoffMin time.Duration
	backoffMax time.Duration
	keepAlive  time.Duration

	// wake is signalled when a connection joins, is consumed or ends, so the
	// pool looks at its counts without polling.
	wake chan struct{}

	mu sync.Mutex
	// conns is every joined connection this bridge holds, fresh (no request has
	// come down it yet) and in use alike; its length is the open count the cap
	// bounds. fresh counts the ones no request has taken yet.
	conns   map[*joinedConn]struct{}
	fresh   int
	lastErr string
}

// NewJoiner checks cfg and builds the joiner that will serve h on the
// connections its relay reverses onto it.
func NewJoiner(cfg JoinConfig, h http.Handler) (*Joiner, error) {
	u, err := joinURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	if !relayNameRe.MatchString(cfg.Name) {
		return nil, fmt.Errorf("join name %q is not [A-Za-z0-9._-]", cfg.Name)
	}
	if cfg.Secret == "" {
		return nil, fmt.Errorf("join name %q has no secret", cfg.Name)
	}
	if h == nil {
		return nil, fmt.Errorf("join name %q has no handler to serve", cfg.Name)
	}
	instance, err := newJoinInstance()
	if err != nil {
		return nil, err
	}
	origin := strings.TrimRight(cfg.URL, "/")
	j := &Joiner{
		url:        origin,
		target:     origin + relayJoinPath,
		addr:       joinDialAddr(u),
		name:       cfg.Name,
		secret:     cfg.Secret,
		instance:   instance,
		handler:    h,
		spare:      joinSpare,
		cap:        joinCap,
		backoffMin: joinBackoffMin,
		backoffMax: joinBackoffMax,
		keepAlive:  joinKeepAlive,
		wake:       make(chan struct{}, 1),
		conns:      make(map[*joinedConn]struct{}, joinCap),
	}
	if u.Scheme == "https" {
		tlsConfig, err := joinTLSConfig(u, cfg.CAFile)
		if err != nil {
			return nil, err
		}
		j.tls = tlsConfig
	}
	j.listener = newJoinListener(j)
	// No ReadHeaderTimeout: see the srv field's comment. A spare connection's
	// first request may be minutes away, and only the trusted relay writes to
	// one.
	j.srv = &http.Server{Handler: h}
	return j, nil
}

// joinURL applies fleet.toml's url rule to raw: an origin, https, or http on a
// loopback host (D12). web/ is standard library only, so internal/fleetprofile's
// checkURL cannot be imported: the rule is copied here, as the bridge copies
// every other rule it shares with it.
func joinURL(raw string) (*url.URL, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("join url %q: %s", raw, fmt.Sprintf(format, args...))
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, bad("it is not a URL: %v", err)
	}
	switch {
	case u.Scheme == "":
		return nil, bad("it has no scheme: want https://host")
	case u.Host == "":
		return nil, bad("it has no host")
	case u.User != nil:
		return nil, bad("it carries user information; a join secret is not kept in the url")
	case u.RawQuery != "" || u.ForceQuery:
		return nil, bad("it carries a query")
	case u.Fragment != "":
		return nil, bad("it carries a fragment")
	case u.Path != "" && u.Path != "/":
		return nil, bad("it carries a path; a relay's url is an origin")
	case u.Scheme == "https":
		return u, nil
	case u.Scheme == "http":
		if !joinLoopbackHost(u.Hostname()) {
			return nil, bad("it is http on %s, which is not loopback: use https", u.Hostname())
		}
		return u, nil
	default:
		return nil, bad("it is %s, not https or http", u.Scheme)
	}
}

// joinLoopbackHost reports whether host names this machine.
func joinLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// joinDialAddr is the host:port a join dials: the url's port, or the scheme's
// own when it names none.
func joinDialAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// joinTLSConfig is the client TLS configuration a join dials with, built as
// fleetprofile.TLSConfig builds a profile's: the system roots kept and every
// certificate in caFile added to them, TLS 1.2 the minimum, and http/1.1 the
// only protocol offered, so the relay can take the connection over (D13).
func joinTLSConfig(u *url.URL, caFile string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		ServerName: u.Hostname(),
	}
	if caFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("join ca file %s: %w", caFile, err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("join ca file %s holds no certificate", caFile)
	}
	cfg.RootCAs = roots
	return cfg, nil
}

// newJoinInstance is a random id per process: 16 random bytes, hex-encoded
// (D13).
func newJoinInstance() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("join instance: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Run keeps the bridge joined. It dials the spare connections, replaces them as
// the relay takes them, and serves h on every connection the relay reverses onto
// this bridge (D13, D14). It answers when ctx ends or the joiner's listener is
// closed.
func (j *Joiner) Run(ctx context.Context) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	srv := j.srv
	served := make(chan error, 1)
	go func() { served <- srv.Serve(j.listener) }()
	pooled := make(chan struct{})
	go func() { defer close(pooled); j.pool(ctx) }()

	var err error
	select {
	case <-ctx.Done():
	case err = <-served:
	}
	// A joiner has no accept backlog to drain: closing the listener ends the
	// server's loop, and every joined connection is this bridge's to close.
	stop()
	_ = j.listener.Close()
	_ = srv.Close()
	<-pooled
	j.closeAll()
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// pool keeps the joined connections the relay expects (D14). At start it dials
// the spare count; a connection the relay takes is one fresh fewer, so the same
// count dials its replacement while the open count is under the cap; and a
// connection that closes leaves the count short the same way.
func (j *Joiner) pool(ctx context.Context) {
	var backoff time.Duration
	for {
		if ctx.Err() != nil {
			return
		}
		if !j.needsConn() {
			select {
			case <-ctx.Done():
				return
			case <-j.wake:
			}
			continue
		}
		conn, err := j.join(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff = j.failed(err, backoff)
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		backoff = 0
		if !j.add(conn) {
			return
		}
	}
}

// needsConn reports whether the pool owes a connection: fewer fresh ones than
// the spare count, and fewer open ones than the cap (D14).
func (j *Joiner) needsConn() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.fresh < j.spare && len(j.conns) < j.cap
}

// add takes a joined connection into the pool and hands it to the listener,
// which is what makes it one the server serves. It answers false once the
// listener is closed, which ends the pool.
func (j *Joiner) add(c *joinedConn) bool {
	j.mu.Lock()
	j.conns[c] = struct{}{}
	j.fresh++
	j.lastErr = ""
	j.mu.Unlock()
	return j.listener.put(c)
}

// consumed records that the relay has sent its first request down a connection,
// which is when the pool dials that connection's replacement (decision 5).
func (j *Joiner) consumed() {
	j.mu.Lock()
	if j.fresh > 0 {
		j.fresh--
	}
	j.mu.Unlock()
	j.signal()
}

// closed records a connection that ended: one no request ever took leaves the
// spares short, which is a connection to dial as well.
func (j *Joiner) closed(c *joinedConn, fresh bool) {
	j.mu.Lock()
	delete(j.conns, c)
	if fresh && j.fresh > 0 {
		j.fresh--
	}
	j.mu.Unlock()
	j.signal()
}

// signal wakes the pool to look at its counts.
func (j *Joiner) signal() {
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

// failed records err as the join's last error and answers how long the pool
// waits before dialling again: the maximum for a refusal, and otherwise from the
// minimum, doubling to the maximum (D14). A new error is logged once, so a relay
// that keeps refusing is one line rather than one per attempt.
func (j *Joiner) failed(err error, backoff time.Duration) time.Duration {
	text := err.Error()
	j.mu.Lock()
	changed := j.lastErr != text
	j.lastErr = text
	j.mu.Unlock()
	if changed {
		slog.Default().Warn("bridge: join failed", "relay", j.url, "name", j.name, "err", err)
	}
	var refused *errJoinRefused
	if errors.As(err, &refused) {
		return j.backoffMax
	}
	if backoff <= 0 {
		return j.backoffMin
	}
	if doubled := backoff * 2; doubled < j.backoffMax {
		return doubled
	}
	return j.backoffMax
}

// join dials the relay and asks it to reverse the connection onto this bridge
// (D13). The connection it answers is ready for the server to serve requests on.
func (j *Joiner) join(ctx context.Context) (*joinedConn, error) {
	dialer := &net.Dialer{KeepAlive: j.keepAlive}
	var (
		conn net.Conn
		err  error
	)
	if j.tls != nil {
		td := &tls.Dialer{NetDialer: dialer, Config: j.tls}
		conn, err = td.DialContext(ctx, "tcp", j.addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", j.addr)
	}
	if err != nil {
		return nil, err
	}
	joined, err := j.upgrade(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return joined, nil
}

// upgrade sends the join down conn and reads the relay's answer (D13). A 101
// leaves the stream reversed, so the connection is wrapped where the answer's
// reader stopped: the relay may already have sent the first request down it
// (decision 4).
func (j *Joiner) upgrade(conn net.Conn) (*joinedConn, error) {
	req, err := http.NewRequest(http.MethodPost, j.target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", joinUpgrade)
	req.Header.Set(joinNameHeader, j.name)
	req.Header.Set(joinInstanceHeader, j.instance)
	req.Header.Set("Authorization", "Bearer "+j.secret)
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("join %s: write the join: %w", j.url, err)
	}
	r := bufio.NewReader(conn)
	res, err := http.ReadResponse(r, req)
	if err != nil {
		return nil, fmt.Errorf("join %s: read the answer: %w", j.url, err)
	}
	if res.StatusCode == http.StatusSwitchingProtocols {
		return newJoinedConn(j, conn, r), nil
	}
	body := joinFirstLine(res.Body)
	_ = res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, &errJoinRefused{name: j.name}
	}
	return nil, fmt.Errorf("join %s: the relay answered %s: %s", j.url, res.Status, body)
}

// joinFirstLine is a refused answer's first line: the status is recorded
// beside it, and a whole page is not an error message (D14).
func joinFirstLine(body io.Reader) string {
	line, err := bufio.NewReader(io.LimitReader(body, 4096)).ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err.Error()
	}
	return "(no body)"
}

// Report is the join as GET /api/daemon answers it (D17): where this bridge
// joins, under what name, whether it holds a connection now, and why it last
// could not.
func (j *Joiner) Report() contract.DaemonJoin {
	j.mu.Lock()
	defer j.mu.Unlock()
	return contract.DaemonJoin{
		URL:       j.url,
		Name:      j.name,
		Connected: len(j.conns) > 0,
		LastError: j.lastErr,
	}
}

// openCount is how many joined connections the joiner holds: the open count the
// cap bounds (D14).
func (j *Joiner) openCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.conns)
}

// closeAll ends every joined connection the joiner holds, which is what Run does
// when it stops.
func (j *Joiner) closeAll() {
	j.mu.Lock()
	conns := make([]*joinedConn, 0, len(j.conns))
	for c := range j.conns {
		conns = append(conns, c)
	}
	j.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// errJoinRefused is the relay's 401: the name is not one it names, or the secret
// is not that name's (D13). It waits the maximum backoff rather than doubling,
// because no attempt will change that answer (D14).
type errJoinRefused struct{ name string }

func (e *errJoinRefused) Error() string { return "the relay refused the join for " + e.name }

// joinedConn is a joined connection. Its reader starts where the join's answer
// was read, so the relay's first bytes are not skipped, and its first read is
// what makes it consumed (decisions 4 and 5).
type joinedConn struct {
	net.Conn
	j *Joiner
	r *bufio.Reader

	mu       sync.Mutex
	consumed bool
	closed   bool
}

// newJoinedConn wraps a joined connection around the reader the answer was read
// through.
func newJoinedConn(j *Joiner, conn net.Conn, r *bufio.Reader) *joinedConn {
	return &joinedConn{Conn: conn, j: j, r: r}
}

// Read reads the bytes the answer's reader already holds, then the socket.
func (c *joinedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.consume()
	}
	return n, err
}

// Close ends the connection and tells the pool it may owe a replacement.
func (c *joinedConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	fresh := !c.consumed
	c.mu.Unlock()
	err := c.Conn.Close()
	c.j.closed(c, fresh)
	return err
}

// consume marks the connection consumed once, when the relay's first request
// arrives on it (decision 5).
func (c *joinedConn) consume() {
	c.mu.Lock()
	if c.consumed || c.closed {
		c.mu.Unlock()
		return
	}
	c.consumed = true
	c.mu.Unlock()
	c.j.consumed()
}

// joinListener is a net.Listener whose Accept returns joined connections, so
// http.Server serves them as it serves any connection, and whose Close stops Run
// (D13).
type joinListener struct {
	j     *Joiner
	conns chan *joinedConn
	done  chan struct{}
	once  sync.Once
}

// newJoinListener builds the listener a joiner's server serves.
func newJoinListener(j *Joiner) *joinListener {
	return &joinListener{
		j:     j,
		conns: make(chan *joinedConn, joinCap),
		done:  make(chan struct{}),
	}
}

// Accept answers the next joined connection, or net.ErrClosed once the joiner's
// listener is closed.
func (l *joinListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// put queues a joined connection for the server. It answers false once the
// listener is closed, which ends the pool.
func (l *joinListener) put(c *joinedConn) bool {
	select {
	case l.conns <- c:
		return true
	case <-l.done:
		return false
	}
}

// Close stops Run: a server serving this listener has nothing left to accept.
func (l *joinListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// Addr names no socket: a joined connection is one this bridge dialled out.
func (l *joinListener) Addr() net.Addr { return joinAddr{name: l.j.name} }

// joinAddr is a joined listener's address, which is a name rather than a socket.
type joinAddr struct{ name string }

func (a joinAddr) Network() string { return "marshal-relay" }

func (a joinAddr) String() string { return a.name + "@relay" }

// RelayedHandler is ServeHTTP for a relayed connection: the agent attach and
// /api/studio/* answer 404 (D15). A relayed request is a client's, so it reaches
// this bridge's own network listener handler otherwise, and that listener's
// bearer check still decides.
func (s *Server) RelayedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := agentAttachID(r.URL.Path); ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, relayedStudioPrefix) {
			http.NotFound(w, r)
			return
		}
		s.ServeHTTP(w, r)
	})
}

// SetJoiner makes GET /api/daemon report j's join (D17). A joining bridge has
// one joiner for the life of its process: it is set once at start, as the
// refresh helper is, and read on each GET /api/daemon.
func (s *Server) SetJoiner(j *Joiner) {
	s.join = j
}

// joiner is the join s reports, or nil when s is not a joining bridge.
func (s *Server) joiner() *Joiner {
	return s.join
}
