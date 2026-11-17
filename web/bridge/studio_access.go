package bridge

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The Studio's sign-in handover.
//
// The daemon's bearer token is long-lived: it outlives a browser tab, a
// restart keeps it so an open Studio stays signed in, and it must never be
// written where a process list, a shell history, a browser history or a
// server access log can read it back. So `marshal studio` never puts the
// token in a URL.
//
// Instead the launcher mints a single-use code over the daemon's unix socket,
// which is peer-checked by user ID and needs no secret. It opens the page with
// `#code=<code>`: a fragment, so the browser never sends it to the server and
// it reaches no access log. The page posts the code once to
// /api/studio/session, on the loopback listener, and gets the token back in a
// response body. A code that is guessed, replayed or found in scrollback later
// is worth nothing: it expires in StudioCodeTTL and is deleted the moment it
// is used.
const (
	// studioCodePath mints a code. It exists on the socket only: the socket
	// is the caller's own, and minting is the privileged half of the pair.
	studioCodePath = "/api/studio/code"
	// studioSessionPath exchanges a code for the bearer token. It is on the
	// loopback listener only, and is the one /api route that needs no bearer
	// token: the code is the credential.
	studioSessionPath = "/api/studio/session"
	// studioHelloPath answers what this daemon is, without a token, so the
	// launcher can confirm the port it is about to hand a browser belongs to
	// the daemon whose socket it just read, before any secret is sent.
	studioHelloPath = "/api/studio/hello"
)

const (
	// studioCodeTTL bounds a code's life. Long enough to open a browser and
	// load one page, short enough that a code left in scrollback is dead by
	// the time anyone reads it.
	studioCodeTTL = 2 * time.Minute
	// studioCodeBytes is the entropy in one code: 256 bits, so guessing is
	// not a threat model.
	studioCodeBytes = 32
	// studioCodeBodyLimit bounds the exchange body: a code and nothing else.
	studioCodeBodyLimit = 4 << 10
)

// studioCodeAnswer is POST /api/studio/code's answer.
type studioCodeAnswer struct {
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"` // Unix milliseconds
}

// studioSessionAnswer is POST /api/studio/session's answer.
type studioSessionAnswer struct {
	Token string `json:"token"`
}

// studioHelloAnswer is GET /api/studio/hello's answer: identity, and nothing
// that is not already visible to the user who can reach the loopback port.
type studioHelloAnswer struct {
	Version   string `json:"version"`
	Profile   string `json:"profile"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"startedAt"` // Unix milliseconds
	Socket    string `json:"socket"`
}

// studioCodes is the daemon's live sign-in codes, in memory only: a restart
// ends every one of them, which is the right lifetime for a handover that
// lasts a page load.
type studioCodes struct {
	now  func() time.Time
	mu   sync.Mutex
	live map[string]time.Time
}

func newStudioCodes() *studioCodes {
	return &studioCodes{now: time.Now, live: map[string]time.Time{}}
}

// mint records a fresh code and answers it with its expiry.
func (c *studioCodes) mint() (string, time.Time, error) {
	raw := make([]byte, studioCodeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.prune(now)
	expiresAt := now.Add(studioCodeTTL)
	c.live[code] = expiresAt
	return code, expiresAt, nil
}

// redeem consumes code, answering whether it was live. The deletion is the
// point: a second exchange of the same code fails even inside its TTL.
func (c *studioCodes) redeem(code string) bool {
	if code == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.prune(now)
	expiresAt, ok := c.live[code]
	if !ok || now.After(expiresAt) {
		return false
	}
	delete(c.live, code)
	return true
}

// prune drops expired codes. The caller holds c.mu.
func (c *studioCodes) prune(now time.Time) {
	for code, expiresAt := range c.live {
		if now.After(expiresAt) {
			delete(c.live, code)
		}
	}
}

// studioRoute serves the sign-in handover, answering whether it handled r.
//
// onSocket selects which half this listener owns: minting is the socket's,
// because the socket is peer-checked; the exchange and the identity probe are
// the loopback listener's, because the page reaches the daemon there. Each
// listener answers the other's route 404, so neither half is reachable from
// where it should not be.
func (s *Server) studioRoute(w http.ResponseWriter, r *http.Request, onSocket bool) bool {
	switch r.URL.Path {
	case studioCodePath:
		if !onSocket {
			http.NotFound(w, r)
			return true
		}
		s.mintStudioCode(w, r)
		return true
	case studioSessionPath:
		if onSocket {
			http.NotFound(w, r)
			return true
		}
		s.exchangeStudioCode(w, r)
		return true
	case studioHelloPath:
		if onSocket {
			http.NotFound(w, r)
			return true
		}
		s.studioHello(w, r)
		return true
	}
	return false
}

// mintStudioCode answers a single-use code. It is reached over the socket, so
// the caller is already the account owner; it still refuses when this daemon
// serves no Studio, because there is then nothing to sign in to.
func (s *Server) mintStudioCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.token == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this daemon serves no Studio"})
		return
	}
	code, expiresAt, err := s.codes.mint()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not mint a sign-in code"})
		return
	}
	writeJSON(w, http.StatusCreated, studioCodeAnswer{Code: code, ExpiresAt: expiresAt.UnixMilli()})
}

// exchangeStudioCode trades a live code for the bearer token. This is the one
// /api route without a bearer token, so the checks in front of it are the
// whole gate: a live code, a same-origin caller when the browser says what it
// is, and one use only.
func (s *Server) exchangeStudioCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.token == "" {
		http.NotFound(w, r)
		return
	}
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the sign-in exchange is same-origin only"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, studioCodeBodyLimit)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a sign-in code is required"})
		return
	}
	if !s.codes.redeem(strings.TrimSpace(body.Code)) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unknown or expired sign-in code"})
		return
	}
	writeJSON(w, http.StatusOK, studioSessionAnswer{Token: s.token})
}

// sameOrigin refuses a caller that names a different origin from this
// listener. A browser always sends Origin on a cross-origin POST, and one that
// matches the page the daemon itself served is the only caller that should be
// swapping a code. A non-browser client (the launcher, a test) sends none and
// is allowed: the code is the credential.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// studioHello answers who this daemon is. It carries no secret: the listener
// is loopback-only, and the launcher compares the answer with what it read
// over the socket before it lets a browser near the port.
func (s *Server) studioHello(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.daemonInfo == nil {
		http.NotFound(w, r)
		return
	}
	info := *s.daemonInfo
	writeJSON(w, http.StatusOK, studioHelloAnswer{
		Version:   info.Version,
		Profile:   info.Profile,
		PID:       info.PID,
		StartedAt: info.StartedAt,
		Socket:    info.Socket,
	})
}
