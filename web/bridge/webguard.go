package bridge

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// The web listener sits behind the operator's proxy on an address this daemon
// publishes as its own, and the proxy authenticates the browser (D13). The
// daemon checks the little it can check for itself: what arrives must be
// addressed to this daemon, and must not be a request another site's page made
// on the browser's behalf. The local socket carries no guard — its callers are
// peer-checked as the owner by the kernel, so a request there is the owner's
// own.

const (
	// crossSiteRefused is the reason a state-changing request a browser made
	// for another site is answered with.
	crossSiteRefused = "cross-site request refused"
	// apiTakesJSON is the reason a body on an /api/ route is refused. A form
	// on another site can only send text/plain, multipart/form-data or
	// application/x-www-form-urlencoded, so requiring JSON is what keeps a
	// cross-site form from reaching the API at all.
	apiTakesJSON = "the API takes application/json"
)

// webGuard checks what the web listener is sent on another site's behalf
// (D13). It answers the status and reason of a refusal, or ok.
//
// A request with neither Sec-Fetch-Site nor Origin is not a browser acting for
// another site — curl, a CLI and the e2e suite send neither — so it passes.
func (s *Server) webGuard(r *http.Request) (status int, reason string, ok bool) {
	path := r.URL.Path
	// /hooks/, a status link and /mcp each carry their own credential and
	// check their own caller: a forge's delivery, a token in the path, and
	// /mcp's own Origin check (mcp.go). The guard would only get in their way.
	if strings.HasPrefix(path, hooksPrefix) || strings.HasPrefix(path, statusPrefix) || path == "/mcp" {
		return 0, "", true
	}
	if !s.allowedHost(r.Host) {
		return http.StatusForbidden, "this daemon does not answer to host " + strings.TrimSpace(r.Host) +
			"; set [daemon] web_url to the address the proxy publishes (docs/one-daemon/design.md D13)", false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		// A read changes nothing: a cross-site read is a leak of what the
		// browser already holds, not a state change another site made here.
		return 0, "", true
	}
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site != "" {
		switch strings.ToLower(site) {
		case "same-origin", "none":
			// The daemon's own page, or a navigation the user started.
		default:
			return http.StatusForbidden, crossSiteRefused, false
		}
	} else if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		// No Sec-Fetch-Site (an older browser): the Origin has to say the
		// same thing the Host does.
		if !s.sameOrigin(origin, r.Host) {
			return http.StatusForbidden, crossSiteRefused, false
		}
	}
	if strings.HasPrefix(path, "/api/") && requestHasBody(r) {
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			return http.StatusUnsupportedMediaType, apiTakesJSON, false
		}
	}
	return 0, "", true
}

// sameOrigin reports whether a request that carries origin was sent for this
// daemon: the origin names an address this daemon answers to, and it is the
// address the request was addressed to or the published one (a proxy may
// forward with the loopback Host while the browser's origin is the public URL).
// "null" — a sandboxed frame, a file: page — is no origin at all, and fails.
func (s *Server) sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	name := normalizeHost(hostName(u.Host))
	if !s.allowedHost(name) {
		return false
	}
	if name == normalizeHost(hostName(host)) {
		return true
	}
	published, ok := publicHostName(s.publicURLBase)
	return ok && name == published
}

// allowedHost reports whether host (a Host header, or an Origin's host) names
// this daemon: localhost, 127.0.0.1 or ::1 on any port, or the public URL's
// host name.
//
// No name is resolved. A name is this daemon's or it is not, whatever DNS would
// say about it, and a proxy publishes one name (D13).
func (s *Server) allowedHost(host string) bool {
	name := normalizeHost(hostName(host))
	if name == "" {
		return false
	}
	switch name {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	published, ok := publicHostName(s.publicURLBase)
	return ok && name == published
}

// hostName is host without its port. A bracketed IPv6 literal keeps its
// address without the brackets, and a bare one has no port to drop.
func hostName(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") {
		if end := strings.IndexByte(h, ']'); end > 0 {
			return h[1:end]
		}
		return h
	}
	if strings.Count(h, ":") == 1 {
		if i := strings.IndexByte(h, ':'); i >= 0 {
			return h[:i]
		}
	}
	return h
}

// publicHostName is the host name of base when base is an absolute URL. An
// empty or malformed base names no host.
func publicHostName(base string) (string, bool) {
	if strings.TrimSpace(base) == "" {
		return "", false
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", false
	}
	return normalizeHost(hostName(u.Host)), true
}

// requestHasBody reports whether the request carries a body. A length of -1 is
// an unknown length, which is a body only when the transfer encoding says it is
// chunked.
func requestHasBody(r *http.Request) bool {
	if r.ContentLength > 0 {
		return true
	}
	if r.ContentLength != -1 {
		return false
	}
	for _, te := range r.TransferEncoding {
		if strings.EqualFold(strings.TrimSpace(te), "chunked") {
			return true
		}
	}
	return false
}

// isJSONContentType reports whether ct declares JSON. Parameters such as
// charset are allowed: they do not change what the body is.
func isJSONContentType(ct string) bool {
	mediaType, _, err := mime.ParseMediaType(ct)
	return err == nil && mediaType == "application/json"
}
