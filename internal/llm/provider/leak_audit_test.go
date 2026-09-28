package provider

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/credentials"
	"marshal/internal/llm/schema"
	"marshal/internal/oauth"
	"marshal/internal/redact"
)

// This file is the leak-audit gate for OAuth token material. It asserts that
// a token which reaches the provider never escapes through any of the
// channels a user could later read: the redactor, the wire captures, the
// structured log, or an error surfaced to the UI.
//
// The tokens here are synthetic and structurally valid only. No real token
// material appears in this file, and none should ever be added to it.

// leakAuditToken is a synthetic access token long enough to clear redact's
// minimum literal-secret length.
const leakAuditToken = "eyJhbGciOiJSUzI1NiJ9.leak-audit-synthetic-payload.signature"

// leakAuditRefresh is a synthetic refresh token in the codex `rt.1.` shape.
const leakAuditRefresh = "rt.1.AAAleak-audit-synthetic-refresh-value"

// leakAuditAccount is a synthetic ChatGPT account id.
const leakAuditAccount = "acct-leak-audit-0000-1111-2222"

// leakAuditEngine builds an engine holding the synthetic token set.
func leakAuditEngine(t *testing.T) *oauth.Engine {
	t.Helper()
	store := credentials.NewMemStore()
	raw, err := json.Marshal(oauth.StoredTokens{
		AccessToken:  leakAuditToken,
		RefreshToken: leakAuditRefresh,
		ExpiresAt:    time.Now().Add(time.Hour),
		IssuedAt:     time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal tokens: %v", err)
	}
	if err := store.Set("marshal:provider:codex", raw); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	return &oauth.Engine{
		ServerURL:  codexIssuer,
		StorageKey: "marshal:provider:codex",
		Store:      store,
		Flow: oauth.FlowConfig{
			Issuer:       codexIssuer,
			AuthorizeURL: codexAuthorizeURL,
			TokenURL:     codexTokenURL,
			ClientID:     codexClientID,
		},
	}
}

// TestLeakAuditRedactorMasksTokenMaterial: once a token has been loaded
// through the engine, the redactor must mask it in arbitrary text.
func TestLeakAuditRedactorMasksTokenMaterial(t *testing.T) {
	engine := leakAuditEngine(t)
	if _, err := engine.TokenSource(t.Context()); err != nil {
		t.Fatalf("TokenSource: %v", err)
	}

	text := "Authorization: Bearer " + leakAuditToken + " and refresh " + leakAuditRefresh
	got := redact.Secrets(text)
	if strings.Contains(got, leakAuditToken) {
		t.Errorf("access token survived redaction: %q", got)
	}
	if strings.Contains(got, leakAuditRefresh) {
		t.Errorf("refresh token survived redaction: %q", got)
	}
}

// TestLeakAuditRedactorMasksAccountID: the account id is registered when the
// provider extracts it, so it must be masked too.
func TestLeakAuditRedactorMasksAccountID(t *testing.T) {
	tok := codexTestToken(leakAuditAccount)
	if got := accountIDFromAccessToken(tok); got != leakAuditAccount {
		t.Fatalf("accountIDFromAccessToken = %q, want %q", got, leakAuditAccount)
	}
	got := redact.Secrets("account " + leakAuditAccount + " in use")
	if strings.Contains(got, leakAuditAccount) {
		t.Errorf("account id survived redaction: %q", got)
	}
}

// TestLeakAuditWireCaptureRequestHasNoToken: the request-body capture writes
// the body verbatim, so the body must never contain token material. The
// token travels in a header, which the capture does not write.
func TestLeakAuditWireCaptureRequestHasNoToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(wireCaptureRequestsEnvVar, dir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine := leakAuditEngine(t)
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("request capture wrote no file")
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read capture %s: %v", e.Name(), err)
		}
		assertNoTokenMaterial(t, "request capture "+e.Name(), string(raw))
	}
}

// TestLeakAuditWireCaptureResponseHasNoToken: the response capture tees the
// SSE body. The endpoint does not echo the token, and the capture must not
// introduce it.
func TestLeakAuditWireCaptureResponseHasNoToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(wireCaptureEnvVar, dir)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine := leakAuditEngine(t)
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range events {
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read capture dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("response capture wrote no file")
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read capture %s: %v", e.Name(), err)
		}
		assertNoTokenMaterial(t, "response capture "+e.Name(), string(raw))
	}
}

// TestLeakAuditLogsHaveNoToken: a chat call must not write token material to
// the structured log, including on the error path.
func TestLeakAuditLogsHaveNoToken(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A 400 exercises the error path, which is where a careless
	// implementation would log the request (and its headers).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"bad request"}`))
	}))
	defer server.Close()

	engine := leakAuditEngine(t)
	p := newTestCodex(t, server.URL, engine)

	_, _ = p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})

	assertNoTokenMaterial(t, "structured log", buf.String())
}

// TestLeakAuditProviderErrorBodyHasNoToken: the error surfaced to the user
// must not carry the token. The endpoint's error body is echoed, so this
// pins that the provider does not append request context to it.
func TestLeakAuditProviderErrorBodyHasNoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":"model not supported"}`))
	}))
	defer server.Close()

	engine := leakAuditEngine(t)
	p := newTestCodex(t, server.URL, engine)

	_, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoTokenMaterial(t, "provider error", err.Error())
}

// TestLeakAuditAuthErrorHasNoToken: the auth-required error names the
// provider, never the token.
func TestLeakAuditAuthErrorHasNoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(codexErrorCodeHeader, "token_expired")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"token expired"}}`))
	}))
	defer server.Close()

	engine := leakAuditEngine(t)
	// Make the forced refresh fail so the auth error surfaces.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	engine.Flow.TokenURL = deadURL

	p := newTestCodex(t, server.URL, engine)
	_, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	assertNoTokenMaterial(t, "auth error", err.Error())
}

// TestLeakAuditDoneEventHasNoToken: the quota and usage values attached to
// the Done event are counters, never token material.
func TestLeakAuditDoneEventHasNoToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range codexQuotaHeaders() {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(codexSSE))
	}))
	defer server.Close()

	engine := leakAuditEngine(t)
	p := newTestCodex(t, server.URL, engine)

	events, err := p.Chat(t.Context(), schema.ChatRequest{
		Model:    "gpt-5.6-luna",
		Messages: []schema.ChatMessage{{Role: schema.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for ev := range events {
		if ev.Type != schema.ChatEventDone {
			continue
		}
		blob, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal done event: %v", err)
		}
		assertNoTokenMaterial(t, "done event", string(blob))
	}
}

// assertNoTokenMaterial fails when any synthetic token value appears in s.
func assertNoTokenMaterial(t *testing.T, what, s string) {
	t.Helper()
	for name, secret := range map[string]string{
		"access token":  leakAuditToken,
		"refresh token": leakAuditRefresh,
		"account id":    leakAuditAccount,
	} {
		if strings.Contains(s, secret) {
			t.Errorf("%s leaked the %s:\n%s", what, name, s)
		}
	}
}

// TestLeakAuditAssertionDetectsLeak is the negative control for the gate
// above. Without it, every other test in this file could pass because the
// assertion is broken rather than because nothing leaked — a vacuous gate is
// worse than no gate, since it reads as coverage.
func TestLeakAuditAssertionDetectsLeak(t *testing.T) {
	for name, secret := range map[string]string{
		"access token":  leakAuditToken,
		"refresh token": leakAuditRefresh,
		"account id":    leakAuditAccount,
	} {
		t.Run(name, func(t *testing.T) {
			// A sub-test that must FAIL: run the assertion against text
			// containing the secret and confirm it reports the leak.
			rec := &testing.T{}
			assertNoTokenMaterial(rec, "control", "prefix "+secret+" suffix")
			if !rec.Failed() {
				t.Fatalf("assertNoTokenMaterial did not detect a planted %s", name)
			}
		})
	}
}

// TestLeakAuditAssertionPassesCleanText is the positive control: the
// assertion must not fire on text that contains no token material.
func TestLeakAuditAssertionPassesCleanText(t *testing.T) {
	rec := &testing.T{}
	assertNoTokenMaterial(rec, "control", "an ordinary log line with no secrets")
	if rec.Failed() {
		t.Fatal("assertNoTokenMaterial fired on clean text")
	}
}
