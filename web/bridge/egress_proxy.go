package bridge

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const egressDialTimeout = 10 * time.Second

// EgressProxy is a forward proxy that enforces per-agent policy. It
// tunnels CONNECT, terminates TLS for hosts with an injected credential,
// and reports every connection. It uses only the standard library.
type EgressProxy struct {
	policy atomic.Pointer[EgressPolicy]
	// leaf returns a certificate for host signed by the workspace CA
	// generation ca (empty means the current one).
	leaf func(workspace, ca, host string) (*tls.Certificate, error)
	// records receives one record per connection; nil disables logging.
	// A full channel drops the record rather than stall traffic.
	records chan<- EgressRecord
	// blocked is told about each refused request. It must not block.
	blocked func(agentID, host string)
	// upstream is the TLS config for connections to real hosts (injected
	// hosts only). Nil uses the system roots.
	upstream *tls.Config
	// dial opens upstream connections; tests replace it.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)

	dropped atomic.Int64

	plainOnce sync.Once
	plain     *http.Transport
}

// NewEgressProxy builds a proxy with an empty policy (everything is
// refused until SetPolicy is called).
func NewEgressProxy(leaf func(workspace, ca, host string) (*tls.Certificate, error), records chan<- EgressRecord, blocked func(agentID, host string), upstream *tls.Config) *EgressProxy {
	p := &EgressProxy{leaf: leaf, records: records, blocked: blocked, upstream: upstream}
	d := &net.Dialer{Timeout: egressDialTimeout}
	p.dial = d.DialContext
	p.policy.Store(&EgressPolicy{})
	return p
}

// SetPolicy swaps the policy; connections already open keep going but
// injected requests re-check it on every request.
func (p *EgressProxy) SetPolicy(pol EgressPolicy) {
	cp := pol
	p.policy.Store(&cp)
}

// Dropped is how many records were discarded because the channel was full.
func (p *EgressProxy) Dropped() int64 { return p.dropped.Load() }

func (p *EgressProxy) emit(r EgressRecord) {
	if p.records == nil {
		return
	}
	select {
	case p.records <- r:
	default:
		p.dropped.Add(1)
	}
}

// authenticate checks Proxy-Authorization (and the source IP when the
// policy pins one). Unknown agent and wrong token are indistinguishable.
func (p *EgressProxy) authenticate(r *http.Request) (string, EgressAgentPolicy, bool) {
	user, pass, ok := parseProxyAuth(r.Header.Get("Proxy-Authorization"))
	if !ok {
		return "", EgressAgentPolicy{}, false
	}
	pol, found := p.policy.Load().Agents[user]
	want := pol.Token
	if !found || want == "" {
		want = "\x00no-such-agent"
	}
	match := subtle.ConstantTimeCompare([]byte(pass), []byte(want)) == 1
	if !found || pol.Token == "" || !match {
		return "", EgressAgentPolicy{}, false
	}
	if pol.IP != "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || host != pol.IP {
			return "", EgressAgentPolicy{}, false
		}
	}
	return user, pol, true
}

func parseProxyAuth(h string) (user, pass string, ok bool) {
	const prefix = "Basic "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(prefix):]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok = strings.Cut(string(raw), ":")
	return user, pass, ok
}

// hostPort splits a CONNECT target or URL host, applying defPort.
func hostPort(hostport string, defPort int) (string, int) {
	h, ps, err := net.SplitHostPort(hostport)
	if err != nil {
		return normalizeHost(strings.Trim(hostport, "[]")), defPort
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port <= 0 || port > 65535 {
		port = defPort
	}
	return normalizeHost(h), port
}

// ServeHTTP handles CONNECT and absolute-URI requests.
func (p *EgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	agentID, pol, ok := p.authenticate(r)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="marshal-egress"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	switch {
	case r.Method == http.MethodConnect:
		host, port := hostPort(r.Host, 443)
		p.connect(w, r, agentID, pol, host, port)
	case r.URL.IsAbs() && strings.EqualFold(r.URL.Scheme, "http"):
		host, port := hostPort(r.URL.Host, 80)
		p.forward(w, r, agentID, pol, host, port)
	default:
		http.Error(w, "this is a forward proxy: use CONNECT or an absolute http URL", http.StatusBadRequest)
	}
}

func (p *EgressProxy) deny(w http.ResponseWriter, agentID string, pol EgressAgentPolicy, host string, port int) {
	p.emit(EgressRecord{
		At: time.Now().UnixMilli(), AgentID: agentID, Workspace: pol.Workspace,
		Host: host, Port: port, Decision: "block",
	})
	if p.blocked != nil {
		p.blocked(agentID, host)
	}
	http.Error(w, "egress to "+host+" is not allowed for this agent", http.StatusForbidden)
}

// connect handles CONNECT: refuse, tunnel, or terminate TLS and inject.
func (p *EgressProxy) connect(w http.ResponseWriter, r *http.Request, agentID string, pol EgressAgentPolicy, host string, port int) {
	if !allowed(pol, host) {
		p.deny(w, agentID, pol, host, port)
		return
	}
	inj, injected := pol.injectionFor(host)
	if injected && p.leaf != nil {
		p.connectInjected(w, r, agentID, pol, host, port, inj)
		return
	}
	start := time.Now()
	target := net.JoinHostPort(host, strconv.Itoa(port))
	up, err := p.dial(r.Context(), "tcp", target)
	if err != nil {
		p.emit(EgressRecord{At: start.UnixMilli(), AgentID: agentID, Workspace: pol.Workspace, Host: host, Port: port, Decision: "allow"})
		http.Error(w, "cannot reach "+host, http.StatusBadGateway)
		return
	}
	defer up.Close()
	client, rd, ok := hijack(w)
	if !ok {
		return
	}
	defer client.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	var bytesUp, bytesDown int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		bytesUp, _ = io.Copy(up, rd)
		closeWrite(up)
	}()
	go func() {
		defer wg.Done()
		bytesDown, _ = io.Copy(client, up)
		closeWrite(client)
	}()
	wg.Wait()
	p.emit(EgressRecord{
		At: start.UnixMilli(), AgentID: agentID, Workspace: pol.Workspace, Host: host, Port: port,
		Decision: "allow", BytesUp: bytesUp, BytesDown: bytesDown, DurationMs: time.Since(start).Milliseconds(),
	})
}

// hijack takes over the client connection. The returned reader includes
// anything the HTTP server already buffered.
func hijack(w http.ResponseWriter) (net.Conn, io.Reader, bool) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy cannot hijack this connection", http.StatusInternalServerError)
		return nil, nil, false
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, nil, false
	}
	return conn, brw.Reader, true
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// plainTransport is the transport for plain-HTTP forwarding. It never
// consults the environment's proxy settings.
func (p *EgressProxy) plainTransport() *http.Transport {
	p.plainOnce.Do(func() {
		p.plain = &http.Transport{
			Proxy:                 nil,
			DialContext:           p.dial,
			MaxIdleConns:          32,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		}
	})
	return p.plain
}

// countingWriter counts bytes written to a ResponseWriter.
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type countingBody struct {
	io.ReadCloser
	n *int64
}

func (c countingBody) Read(b []byte) (int, error) {
	n, err := c.ReadCloser.Read(b)
	*c.n += int64(n)
	return n, err
}

// forward proxies an absolute-URI plain HTTP request. Injection applies
// here too, but it puts a credential on the wire in the clear; use
// https hosts for injected credentials.
func (p *EgressProxy) forward(w http.ResponseWriter, r *http.Request, agentID string, pol EgressAgentPolicy, host string, port int) {
	if !allowed(pol, host) {
		p.deny(w, agentID, pol, host, port)
		return
	}
	start := time.Now()
	inj, injected := pol.injectionFor(host)
	var up int64
	rp := &httputil.ReverseProxy{
		Transport: p.plainTransport(),
		Director: func(req *http.Request) {
			req.Host = req.URL.Host
			stripProxyHeaders(req.Header)
			if injected {
				req.Header.Del(inj.Header)
				req.Header.Set(inj.Header, inj.Value)
			}
			if req.Body != nil && req.Body != http.NoBody {
				req.Body = countingBody{ReadCloser: req.Body, n: &up}
			}
		},
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, err error) {
			http.Error(rw, "cannot reach "+host, http.StatusBadGateway)
		},
	}
	cw := &countingWriter{ResponseWriter: w}
	rp.ServeHTTP(cw, r)
	p.emit(EgressRecord{
		At: start.UnixMilli(), AgentID: agentID, Workspace: pol.Workspace, Host: host, Port: port,
		Decision: "allow", Injected: injected, BytesUp: up, BytesDown: cw.n, DurationMs: time.Since(start).Milliseconds(),
	})
}

func stripProxyHeaders(h http.Header) {
	h.Del("Proxy-Authorization")
	h.Del("Proxy-Connection")
	h.Del("Proxy-Authenticate")
}
