package bridge

// hostforward.go is the forward to the tunnels: the daemon keeps an SSH tunnel
// to each host its tunnel helper reports (one-daemon D7), and a request under
// /api/hosts/<name>/ is carried down that host's tunnel socket to the host's
// own daemon, which decides what to do with it (D10).

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"marshal/web/bridge/contract"
)

// The forward's own wire names (D10). The prefix is also what splitForward
// refuses inside a forward, so a forward never chains.
const (
	hostsPath         = "/api/hosts"
	hostForwardPrefix = "/api/hosts/"
)

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

// listHosts answers GET /api/hosts: the hosts this daemon keeps a tunnel to, as
// its tunnel helper last reported them (D10).
func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, contract.HostList{Hosts: s.reportedHosts()})
}

// hostForward sends /api/hosts/<name>/<rest> to that host's tunnel socket as
// /<rest>, refusing a socket-only route (D10). The host's own daemon decides
// everything else: the forward is the caller's request, not the daemon's
// authority.
func (s *Server) hostForward(w http.ResponseWriter, r *http.Request) {
	name, rest, ok := splitForward(r.URL)
	if !ok {
		// Not a path this route forwards: a remainder outside the host's API,
		// or the hosts route itself, which would chain forwards. It is not a
		// route at all, and reads as one.
		http.NotFound(w, r)
		return
	}
	host, ok := reportedHost(s.reportedHosts(), name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no host " + name})
		return
	}
	if host.State != contract.HostLive {
		// A host the helper has not reached is a tunnel that cannot carry this
		// request now, which is the caller's gateway, not a missing route: the
		// answer names the state and why the helper last failed (D10).
		reason := name + " is " + string(host.State)
		if host.LastError != "" {
			reason += ": " + host.LastError
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": reason})
		return
	}
	if socketOnly("/" + rest.path) {
		// A route only the socket serves stays the socket's: the host's owner
		// is the caller there, and a forwarded request is not (D10). The host
		// lookup comes first, so an unknown host answers as an unknown host.
		http.NotFound(w, r)
		return
	}
	// A preview link the host issues names the origin the caller reaches this
	// daemon's preview listener through, in place of the host's own port. A
	// caller on this machine names the host's preview port, which the tunnel
	// carries; a caller behind the proxy names this daemon's preview_url, and
	// the link it gets back comes here to be relayed (one-daemon open
	// question 1). The caller cannot choose that origin through the forward.
	fromThisMachine := isLoopbackName(r.Host)
	namesPreviewURL := !fromThisMachine && s.previewURL != ""
	proxy := &httputil.ReverseProxy{
		Transport: s.hostTransport(host.Socket),
		Rewrite: func(pr *httputil.ProxyRequest) {
			hop, _ := pr.In.Context().Value(hopPathKey{}).(hopPath)
			out := pr.Out
			out.URL = &url.URL{
				Scheme: "http",
				Host:   "daemon",
				// The forward's remainder is the host's path: /api/hosts/box/
				// api/agents reaches the host as /api/agents, and the caller's
				// query goes with it (D10).
				Path:     "/" + hop.path,
				RawPath:  "/" + hop.raw,
				RawQuery: pr.In.URL.RawQuery,
			}
			out.Host = ""
			// The caller's credentials are the caller's: the tunnel socket is
			// the owner's own, and the host's daemon needs no token there. No
			// X-Forwarded-* is added (D10).
			out.Header.Del("Authorization")
			out.Header.Del("Cookie")
			out.Header.Del(contract.PreviewOriginHeader)
			switch {
			case namesPreviewURL:
				out.Header.Set(contract.PreviewOriginHeader, s.previewURL)
			case host.PreviewPort > 0:
				out.Header.Set(contract.PreviewOriginHeader, fmt.Sprintf("http://localhost:%d", host.PreviewPort))
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		},
		// An event stream is flushed as it is written, so a caller sees each
		// event when the host writes it rather than when a buffer fills (D10).
		FlushInterval: -1,
	}
	if namesPreviewURL && r.Method == http.MethodPost && isPreviewIssuePath(rest.path) {
		// The host named this daemon's preview_url in the link it issued, so
		// the request that follows the link comes here: record which host
		// issued it, for the relay to send it back there (previewrelay.go).
		// The forward sets no other ModifyResponse; one it had would still
		// run, chained here.
		proxy.ModifyResponse = s.recordForwardedPreview(name)
	}
	proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), hopPathKey{}, rest)))
}

// hostTransport is the HTTP transport that dials socket, built once per path:
// one connection is reused across the requests to one host, so a stream keeps
// the connection it opened (D10).
func (s *Server) hostTransport(socket string) *http.Transport {
	if t, ok := s.hostTransports.Load(socket); ok {
		return t.(*http.Transport)
	}
	dialer := &net.Dialer{}
	t := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// The address names the host, not a socket: the tunnel socket is
			// this machine's end, and it is what the dial takes (D10).
			return dialer.DialContext(ctx, "unix", socket)
		},
		// HTTP/2 is off: the tunnel carries an HTTP/1.1 connection to a unix
		// socket, and the host's daemon serves it as any other.
		ForceAttemptHTTP2: false,
	}
	actual, _ := s.hostTransports.LoadOrStore(socket, t)
	return actual.(*http.Transport)
}

// reportedHost finds name in a hosts report. A name the helper does not report
// is no host this daemon keeps a tunnel to.
func reportedHost(hosts []DaemonHost, name string) (DaemonHost, bool) {
	for _, h := range hosts {
		if h.Name == name {
			return h, true
		}
	}
	return DaemonHost{}, false
}

// splitForward reads a forward path: the name it addresses, unescaped, and the
// path to send the host.
func splitForward(u *url.URL) (string, hopPath, bool) {
	escaped := u.EscapedPath()
	if !strings.HasPrefix(escaped, hostForwardPrefix) {
		return "", hopPath{}, false
	}
	escapedName, escapedPath, ok := strings.Cut(escaped[len(hostForwardPrefix):], "/")
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
	// A forward carries the host's API and nothing else, and never the hosts
	// route or the retired relay's: a forward does not chain (D10). The
	// prefixes are written here rather than taken from the route constants
	// because they are relative to the host, which is exactly what this path no
	// longer holds.
	if !strings.HasPrefix(path, "api/") ||
		strings.HasPrefix(path, "api/hosts/") ||
		strings.HasPrefix(path, "api/relay/") {
		return "", hopPath{}, false
	}
	return name, hopPath{path: path, raw: escapedPath}, true
}
