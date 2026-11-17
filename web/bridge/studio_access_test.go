package bridge

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestTheStudioSignInHandsOverACodeNotTheToken is the point of the flow: the
// token is never in a URL, and the page gets it by redeeming a code once.
func TestTheStudioSignInHandsOverACodeNotTheToken(t *testing.T) {
	s := daemonTestServer(t)

	minted := viaHandler(t, s.SocketHandler(), http.MethodPost, studioCodePath)
	if minted.Code != http.StatusCreated {
		t.Fatalf("socket %s status = %d, body = %s", studioCodePath, minted.Code, minted.Body.String())
	}
	var code studioCodeAnswer
	decodeBody(t, minted, &code)
	if code.Code == "" {
		t.Fatal("minted code is empty")
	}
	if code.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatalf("code expires at %d, want a future time", code.ExpiresAt)
	}

	// The exchange needs no bearer token: the code is the credential.
	exchanged := doReq(t, s, http.MethodPost, studioSessionPath, map[string]string{"code": code.Code}, nil)
	if exchanged.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body = %s", studioSessionPath, exchanged.Code, exchanged.Body.String())
	}
	var session studioSessionAnswer
	decodeBody(t, exchanged, &session)
	if session.Token != "secret" {
		t.Fatalf("exchanged token = %q, want the daemon's", session.Token)
	}

	// One use only: the code is gone the moment it worked.
	again := doReq(t, s, http.MethodPost, studioSessionPath, map[string]string{"code": code.Code}, nil)
	if again.Code != http.StatusUnauthorized {
		t.Fatalf("second exchange status = %d, want 401: %s", again.Code, again.Body.String())
	}
}

// TestTheStudioCodeRouteBelongsToItsListener: minting is the socket's half and
// the exchange is the loopback listener's, so neither is reachable from the
// other. A page reaching the socket is not a thing; a network client minting
// its own code would be the whole hole back again.
func TestTheStudioCodeRouteBelongsToItsListener(t *testing.T) {
	s := daemonTestServer(t)

	if rec := doReq(t, s, http.MethodPost, studioCodePath, nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("network %s = %d, want 404", studioCodePath, rec.Code)
	}
	if rec := viaHandler(t, s.SocketHandler(), http.MethodPost, studioSessionPath); rec.Code != http.StatusNotFound {
		t.Fatalf("socket %s = %d, want 404", studioSessionPath, rec.Code)
	}
	if rec := viaHandler(t, s.SocketHandler(), http.MethodGet, studioHelloPath); rec.Code != http.StatusNotFound {
		t.Fatalf("socket %s = %d, want 404", studioHelloPath, rec.Code)
	}
}

// TestTheStudioHelloNamesTheDaemonWithoutASecret: the launcher confirms the
// port is this daemon's before sending it anything, and the confirmation
// carries no credential.
func TestTheStudioHelloNamesTheDaemonWithoutASecret(t *testing.T) {
	s := daemonTestServer(t)

	rec := doReq(t, s, http.MethodGet, studioHelloPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, body = %s", studioHelloPath, rec.Code, rec.Body.String())
	}
	var hello studioHelloAnswer
	decodeBody(t, rec, &hello)
	if hello.PID != 4242 || hello.StartedAt != 1700000000000 ||
		hello.Socket != "/run/user/501/marshal/daemon.sock" || hello.Version != "v1.2.3" {
		t.Fatalf("hello = %+v, want the daemon's identity", hello)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("hello body carries the token: %s", rec.Body.String())
	}
	// The token-checked route stays shut without a token.
	if rec := doReq(t, s, http.MethodGet, "/api/daemon", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("network /api/daemon without a token = %d, want 401", rec.Code)
	}
}

// TestTheStudioExchangeRefusesAForeignOrigin: a browser always names its
// origin, and one that is not the page this daemon served has no business
// redeeming a code.
func TestTheStudioExchangeRefusesAForeignOrigin(t *testing.T) {
	s := daemonTestServer(t)
	code := mintStudioCodeForTest(t, s)

	foreign := doReq(t, s, http.MethodPost, studioSessionPath, map[string]string{"code": code},
		map[string]string{"Origin": "https://evil.example"})
	if foreign.Code != http.StatusForbidden {
		t.Fatalf("foreign origin = %d, want 403: %s", foreign.Code, foreign.Body.String())
	}

	// The same code still works from the page's own origin: a refused
	// exchange does not spend it.
	own := doReq(t, s, http.MethodPost, studioSessionPath, map[string]string{"code": code},
		map[string]string{"Origin": "http://example.com"})
	if own.Code != http.StatusOK {
		t.Fatalf("own origin = %d, want 200: %s", own.Code, own.Body.String())
	}
}

// TestAServerWithoutATokenHasNoStudioSignIn: the server profile's bridge serves
// no Studio, so there is nothing to mint and nothing to exchange.
func TestAServerWithoutATokenHasNoStudioSignIn(t *testing.T) {
	s := NewServer(testFleet(t))

	if rec := viaHandler(t, s.SocketHandler(), http.MethodPost, studioCodePath); rec.Code != http.StatusNotFound {
		t.Fatalf("socket %s without a token = %d, want 404", studioCodePath, rec.Code)
	}
	if rec := doReq(t, s, http.MethodPost, studioSessionPath, map[string]string{"code": "x"}, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("%s without a token = %d, want 404", studioSessionPath, rec.Code)
	}
}

// TestAStudioCodeExpires: a code's life is bounded, so one recovered from
// scrollback later is worth nothing.
func TestAStudioCodeExpires(t *testing.T) {
	codes := newStudioCodes()
	now := time.Unix(1_700_000_000, 0)
	codes.now = func() time.Time { return now }

	code, expiresAt, err := codes.mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !expiresAt.After(now) {
		t.Fatalf("expiry %s is not after %s", expiresAt, now)
	}
	if !codes.redeem(code) {
		t.Fatal("a live code did not redeem")
	}

	code, _, err = codes.mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	now = now.Add(studioCodeTTL + time.Second)
	if codes.redeem(code) {
		t.Fatal("an expired code redeemed")
	}
}

// TestAStudioCodeIsSingleUse: redeeming consumes it, so a replay in the same
// window fails too.
func TestAStudioCodeIsSingleUse(t *testing.T) {
	codes := newStudioCodes()
	code, _, err := codes.mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !codes.redeem(code) {
		t.Fatal("first redeem failed")
	}
	if codes.redeem(code) {
		t.Fatal("second redeem succeeded")
	}
}

// mintStudioCodeForTest mints a code the way `marshal studio` does.
func mintStudioCodeForTest(t *testing.T, s *Server) string {
	t.Helper()
	minted := viaHandler(t, s.SocketHandler(), http.MethodPost, studioCodePath)
	if minted.Code != http.StatusCreated {
		t.Fatalf("socket %s status = %d, body = %s", studioCodePath, minted.Code, minted.Body.String())
	}
	var code studioCodeAnswer
	decodeBody(t, minted, &code)
	return code.Code
}
