package bridge

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
)

// PreviewHandler is the sidecar's preview listener. It serves
// /<agentId>/<port>/<rest> by forwarding to the agent's address on the
// internal network, and only for ports in the agent's current policy.
// WebSocket upgrades pass through httputil.ReverseProxy.
//
// The bridge authenticates the request with the agent's own proxy token in
// previewAuthHeader. Other agents can reach this listener too, but only
// the bridge knows a given agent's token.
func (p *EgressProxy) PreviewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, portStr, rest, ok := splitPreviewTarget(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		pol, found := p.policy.Load().Agents[id]
		if !found || subtle.ConstantTimeCompare([]byte(r.Header.Get(previewAuthHeader)), []byte(pol.Token)) != 1 || pol.Token == "" {
			http.NotFound(w, r)
			return
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || !portDeclared(pol.PreviewPorts, port) {
			http.Error(w, "port is not declared for preview", http.StatusForbidden)
			return
		}
		if pol.IP == "" {
			http.Error(w, "agent address unknown", http.StatusBadGateway)
			return
		}
		target := net.JoinHostPort(pol.IP, portStr)
		rp := &httputil.ReverseProxy{
			Director: func(out *http.Request) {
				out.URL.Scheme, out.URL.Host = "http", target
				out.URL.Path, out.URL.RawPath = rest, ""
				out.Host = target
				out.Header.Del(previewAuthHeader)
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, fmt.Sprintf("preview: %v", err), http.StatusBadGateway)
			},
		}
		rp.ServeHTTP(w, r)
	})
}

// splitPreviewTarget parses /<agentId>/<port>/<rest>, with rest keeping
// its leading slash.
func splitPreviewTarget(p string) (id, port, rest string, ok bool) {
	s := strings.TrimPrefix(p, "/")
	id, after, found := strings.Cut(s, "/")
	if !found || id == "" {
		return "", "", "", false
	}
	port, tail, _ := strings.Cut(after, "/")
	if port == "" {
		return "", "", "", false
	}
	return id, port, "/" + tail, true
}

func portDeclared(ports []int, port int) bool {
	for _, p := range ports {
		if p == port {
			return true
		}
	}
	return false
}
