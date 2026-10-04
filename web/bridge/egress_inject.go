package bridge

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// countConn counts bytes crossing a connection.
type countConn struct {
	net.Conn
	// src is where reads come from: the hijacked connection's buffered
	// reader, which may already hold client bytes.
	src      io.Reader
	up, down atomic.Int64 // up: read from client; down: written to client
}

func (c *countConn) Read(b []byte) (int, error) {
	n, err := c.src.Read(b)
	c.up.Add(int64(n))
	return n, err
}

func (c *countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.down.Add(int64(n))
	return n, err
}

// injectTransport sends decrypted requests to the real host. HTTP/2 is
// off and the system roots (or the configured upstream roots) verify it.
func (p *EgressProxy) injectTransport() *http.Transport {
	t := &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dial,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	if p.upstream != nil {
		t.TLSClientConfig = p.upstream.Clone()
	}
	// A non-nil TLSClientConfig with no NextProtos keeps HTTP/1.1.
	return t
}

// connectInjected terminates the client's TLS with a workspace-CA leaf
// for host, then forwards each request upstream with the injected header
// set. The client never sees the credential, and any copy of that header
// it sent is discarded.
func (p *EgressProxy) connectInjected(w http.ResponseWriter, r *http.Request, agentID string, pol EgressAgentPolicy, host string, port int, _ EgressInjection) {
	start := time.Now()
	cert, err := p.leaf(pol.Workspace, host)
	if err != nil {
		http.Error(w, "cannot mint a certificate for "+host, http.StatusBadGateway)
		return
	}
	raw, rd, ok := hijack(w)
	if !ok {
		return
	}
	defer raw.Close()
	if _, err := io.WriteString(raw, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	cc := &countConn{Conn: raw, src: rd}
	tlsConn := tls.Server(cc, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	defer tlsConn.Close()
	_ = tlsConn.SetDeadline(time.Now().Add(15 * time.Second))
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	_ = tlsConn.SetDeadline(time.Time{})

	tr := p.injectTransport()
	defer tr.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := net.JoinHostPort(host, strconv.Itoa(port))
	br := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			break
		}
		// Re-read the policy: a revoked host or changed credential
		// applies to the next request on a long-lived connection.
		cur, live := p.policy.Load().Agents[agentID]
		inj, stillInjected := cur.injectionFor(host)
		if !live || !allowed(cur, host) || !stillInjected {
			writeSimpleResponse(tlsConn, http.StatusForbidden, "egress policy changed")
			break
		}
		if req.Header.Get("Upgrade") != "" {
			writeSimpleResponse(tlsConn, http.StatusNotImplemented, "upgrades are not supported on injected hosts")
			break
		}
		// The credential is for the CONNECT target: never for whatever
		// Host header the client wrote.
		req.URL.Scheme = "https"
		req.URL.Host = target
		req.Host = host
		if port != 443 {
			req.Host = target
		}
		req.RequestURI = ""
		stripProxyHeaders(req.Header)
		req.Header.Del(inj.Header)
		req.Header.Set(inj.Header, inj.Value)
		resp, err := tr.RoundTrip(req.WithContext(ctx))
		if err != nil {
			writeSimpleResponse(tlsConn, http.StatusBadGateway, "cannot reach "+host)
			break
		}
		closeAfter := req.Close || resp.Close
		resp.Header.Del("Proxy-Connection")
		werr := resp.Write(tlsConn)
		resp.Body.Close()
		if werr != nil || closeAfter {
			break
		}
	}
	p.emit(EgressRecord{
		At: start.UnixMilli(), AgentID: agentID, Workspace: pol.Workspace, Host: host, Port: port,
		Decision: "allow", Injected: true, BytesUp: cc.up.Load(), BytesDown: cc.down.Load(),
		DurationMs: time.Since(start).Milliseconds(),
	})
}

func writeSimpleResponse(w io.Writer, status int, msg string) {
	resp := &http.Response{
		StatusCode:    status,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(msg + "\n")),
		ContentLength: int64(len(msg) + 1),
		Close:         true,
	}
	_ = resp.Write(w)
}
