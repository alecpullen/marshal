// Package oauth implements an OAuth 2.1 authorization-code flow with
// PKCE (RFC 7636) for remote Streamable-HTTP MCP servers.
//
// Engine is the entry point. Wire an Engine with a TokenStore (the same
// shape used by internal/credentials), an HTTP client, and a
// browser-opening function; then call TokenSource to obtain a valid
// access token. TokenSource transparently refreshes stored tokens and
// returns ErrAuthRequired when re-authentication is needed; callers
// invoke Authorize to drive the browser flow.
//
// Authorize runs metadata discovery (RFC 8414 with an OIDC fallback),
// dynamic client registration (RFC 7591), binds a loopback receiver,
// hands the authorization URL to the supplied Display, and persists the
// resulting token set. The package never persists secrets to disk
// outside the supplied TokenStore; the on-disk metadata cache under the
// project database directory stores only URLs and endpoint names.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"marshal/internal/credentials"
)

// TokenStore is the persistence interface this package requires. The
// internal/credentials package (or any other keychain wrapper) is
// structurally compatible: Get/Set/Delete taking and returning []byte
// and a single error.
//
// Implementations are responsible for secrecy; this package does not
// encrypt the bytes handed to Set.
type TokenStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// Display abstracts how the authorization URL reaches the user. The TUI
// implementation typically opens the URL in a browser and shows a
// notice in the panel; a headless implementation just prints it.
//
// ShowURL is called once. Wait blocks until the user has had a chance
// to complete the flow (e.g. close the browser tab); it is also the
// point at which the caller can detect cancellation through ctx.
type Display interface {
	ShowURL(url string)
	Wait(ctx context.Context) error
}

// Engine drives the OAuth flow for a single MCP server. One Engine per
// server URL; it is safe to call TokenSource concurrently — the mutex
// serializes refresh races.
type Engine struct {
	ServerURL  string
	ClientName string
	Store      TokenStore
	HTTPClient *http.Client
	Open       func(string)
	Logger     *slog.Logger

	// Scopes is the optional set of scopes requested at the
	// authorization endpoint. An empty slice omits the parameter,
	// deferring to the server's default scope set.
	Scopes []string

	// LoopbackTimeout caps a single Authorize call. Zero means the
	// default five-minute timeout. Tests override this to drive
	// timeout scenarios.
	LoopbackTimeout time.Duration

	// CacheDir overrides the on-disk metadata cache directory. Empty
	// means use DefaultCacheDir derived from the current working
	// directory.
	CacheDir string

	// clock is replaced in tests to keep expiry math deterministic.
	clock func() time.Time

	mu        sync.Mutex
	metadata  *MetadataCache
	cacheOnce sync.Once
}

// ErrAuthRequired is the error type returned when the caller needs to
// invoke Authorize. The struct carries ServerName and Reason so
// callers can introspect without unwrapping.
//
// To detect this error with errors.Is, pass a *ErrAuthRequired value
// (any concrete value works; the Is method checks the dynamic type).
// The package also exposes a process-wide sentinel (ErrAuthSentinel).
type ErrAuthRequired struct {
	ServerName string
	Reason     string
}

// ErrAuthSentinel is a package-level *ErrAuthRequired that callers
// can pass to errors.Is. Any concrete *ErrAuthRequired matches.
var ErrAuthSentinel = &ErrAuthRequired{}

// Error implements the error interface.
func (e *ErrAuthRequired) Error() string {
	if e == nil {
		return "oauth: authentication required"
	}
	name := e.ServerName
	if name == "" {
		name = "<unnamed>"
	}
	return fmt.Sprintf("oauth: authentication required for %s (%s)", name, e.Reason)
}

// Is satisfies errors.Is. Any pointer-to-ErrAuthRequired target
// matches, so callers can check err against either a freshly built
// literal or the package-level ErrAuthSentinel.
func (e *ErrAuthRequired) Is(target error) bool {
	if target == nil {
		return false
	}
	_, ok := target.(*ErrAuthRequired)
	return ok
}

// log returns the engine's logger, defaulting to slog.Default() when
// the Logger field is nil. Mirrors the convention used by other
// packages in the repo.
func (e *Engine) log() *slog.Logger {
	if e.Logger == nil {
		return slog.Default()
	}
	return e.Logger
}

// http returns the engine's HTTP client, falling back to a 30s-timeout
// default when none is set. The returned client refuses cross-host
// redirects: a 307/308 replays the request body, which on the token and
// registration endpoints carries the PKCE verifier or the refresh token,
// so following one would hand those to a host the user never authorized.
func (e *Engine) http() *http.Client {
	base := e.HTTPClient
	if base == nil {
		base = &http.Client{Timeout: 30 * time.Second}
	}
	clone := *base
	clone.CheckRedirect = sameHostRedirect
	return &clone
}

// sameHostRedirect refuses a redirect whose host differs from the host of
// the original request.
func sameHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("oauth: stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}
	if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return fmt.Errorf("oauth: refusing cross-host redirect from %s to %s", via[0].URL.Host, req.URL.Host)
	}
	return nil
}

// now returns the engine's clock. Tests inject a fixed clock to make
// expiry assertions deterministic; production uses time.Now.
func (e *Engine) now() time.Time {
	if e.clock != nil {
		return e.clock()
	}
	return time.Now()
}

// storageKey is the TokenStore key used for this engine's tokens. We
// key on ServerURL since that is the per-server identity in the Engine
// struct. ClientName is only used in DCR and Display messaging.
func (e *Engine) storageKey() string {
	return "marshal:mcp:" + e.ServerURL
}

// metadataCache returns (creating if necessary) the metadata cache for
// this engine. Uses its own sync.Once so callers that already hold
// e.mu (TokenSource, Authorize) can still call this without
// self-deadlocking.
func (e *Engine) metadataCache() *MetadataCache {
	e.cacheOnce.Do(func() {
		dir := e.CacheDir
		if dir == "" {
			dir = DefaultCacheDir(".")
		}
		mc := NewMetadataCache(dir, e.http())
		mc.now = e.now
		e.metadata = mc
	})
	return e.metadata
}

// TokenSource returns a valid access token for the configured server.
// It is the package's main call-site: every authenticated MCP request
// reaches TokenSource first.
//
//  1. Reads the stored blob under the engine's key.
//  2. If absent, returns ErrAuthRequired ("no stored tokens").
//  3. If the access token is still valid (beyond the 30s skew),
//     returns it.
//  4. Otherwise POSTs grant_type=refresh_token and re-stores.
//  5. If the refresh fails with invalid_grant, deletes the entry and
//     returns ErrAuthRequired ("refresh rejected").
//
// The engine mutex serializes concurrent TokenSource calls so only one
// refresh runs at a time; the loser of a race re-reads the refreshed
// entry rather than refreshing again.
func (e *Engine) TokenSource(ctx context.Context) (string, error) {
	if e.Store == nil {
		return "", errors.New("oauth: Engine.Store is nil")
	}
	key := e.storageKey()

	e.mu.Lock()
	defer e.mu.Unlock()

	stored, err := e.loadLocked()
	if err != nil {
		return "", err
	}
	if stored == nil {
		return "", &ErrAuthRequired{ServerName: e.ServerURL, Reason: "no stored tokens"}
	}

	// Re-register secrets on every load.
	registerSecretsWithRedact(stored.AccessToken, stored.RefreshToken)

	if !stored.IsExpired() {
		return stored.AccessToken, nil
	}

	if stored.RefreshToken == "" {
		if delErr := e.Store.Delete(key); delErr != nil {
			e.log().Warn("oauth: failed to delete stale token entry", "server", e.ServerURL, "err", delErr)
		}
		return "", &ErrAuthRequired{ServerName: e.ServerURL, Reason: "no refresh token"}
	}

	meta, err := e.metadataCache().Lookup(ctx, e.ServerURL)
	if err != nil {
		return "", fmt.Errorf("oauth: %w", err)
	}
	// Pin the refresh to the endpoint that issued the token. A server
	// that later advertises a different token endpoint must not receive
	// a refresh token it never issued.
	if stored.TokenEndpoint != "" && stored.TokenEndpoint != meta.TokenEndpoint {
		return "", &ErrAuthRequired{ServerName: e.ServerURL, Reason: "authorization server changed its token endpoint; re-run /mcp auth"}
	}

	next, err := Refresh(ctx, e.http(), meta.TokenEndpoint, stored.RefreshToken, stored.ClientID)
	if err != nil {
		var te *TokenError
		if errors.As(err, &te) && te.IsInvalidGrant() {
			if delErr := e.Store.Delete(key); delErr != nil {
				e.log().Warn("oauth: failed to delete entry after invalid_grant", "server", e.ServerURL, "err", delErr)
			}
			return "", &ErrAuthRequired{ServerName: e.ServerURL, Reason: "refresh rejected"}
		}
		return "", err
	}

	// Refresh may omit a new refresh_token — preserve the existing
	// one in that case so subsequent refreshes still work.
	if next.RefreshToken == "" {
		next.RefreshToken = stored.RefreshToken
	}
	next.ClientID = stored.ClientID
	next.ClientSecret = stored.ClientSecret

	registerSecretsWithRedact(next.AccessToken, next.RefreshToken)
	if err := e.persistLocked(next); err != nil {
		return "", err
	}
	return next.AccessToken, nil
}

// loadLocked reads and decodes the stored token blob. The caller
// must hold e.mu. A nil result with a nil error means "no entry yet".
func (e *Engine) loadLocked() (*StoredTokens, error) {
	raw, err := e.Store.Get(e.storageKey())
	if err != nil {
		// A missing entry is the normal "not authorized yet" state, not
		// a failure: the store reports it with ErrNotFound.
		if errors.Is(err, credentials.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("oauth: read stored tokens: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var stored StoredTokens
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("oauth: decode stored tokens: %w", err)
	}
	return &stored, nil
}

// persistLocked serializes and stores the token blob. The caller must
// hold e.mu.
func (e *Engine) persistLocked(s StoredTokens) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("oauth: marshal tokens: %w", err)
	}
	if err := e.Store.Set(e.storageKey(), raw); err != nil {
		return fmt.Errorf("oauth: write tokens: %w", err)
	}
	return nil
}

// Authorize drives the full browser flow: metadata discovery → optional
// dynamic client registration → loopback listener → browser open via
// Display → callback verification → token exchange → storage.
//
// It blocks until the callback arrives, the context is cancelled, the
// loopback timeout elapses, or any other failure surfaces. The
// TokenStore is written only after a successful token exchange;
// cancelled or failed flows leave zero partial state.
func (e *Engine) Authorize(ctx context.Context, d Display) error {
	if e.Store == nil {
		return errors.New("oauth: Engine.Store is nil")
	}
	if d == nil {
		return errors.New("oauth: Display is nil")
	}

	meta, err := e.metadataCache().Lookup(ctx, e.ServerURL)
	if err != nil {
		return fmt.Errorf("oauth: discover: %w", err)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return errors.New("oauth: server metadata missing required endpoints")
	}

	timeout := e.LoopbackTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	// Prefer a stable port so the redirect_uri is the same on every run;
	// fall back to an ephemeral one when it is already in use.
	loop, err := StartLoopbackOnPort(ctx, PreferredLoopbackPort, timeout)
	if err != nil {
		loop, err = StartLoopbackOnPort(ctx, 0, timeout)
		if err != nil {
			return err
		}
	}
	defer loop.Close()

	// Register a fresh client for every flow. The redirect_uri carries a
	// per-flow loopback port, so a client registered for an earlier port
	// would be rejected by a strict authorization server. A cached
	// client_id is used only when the server advertises no registration
	// endpoint at all.
	var clientID, clientSecret string
	if regEndpoint := meta.RegistrationEndpoint; regEndpoint != "" {
		reg, err := RegisterClient(ctx, e.http(), regEndpoint, loop.RedirectURI, e.ClientName)
		if err != nil {
			return fmt.Errorf("oauth: dynamic client registration: %w", err)
		}
		clientID = reg.ClientID
		clientSecret = reg.ClientSecret
	} else {
		e.mu.Lock()
		stored, _ := e.loadLocked()
		e.mu.Unlock()
		if stored == nil || stored.ClientID == "" {
			return errors.New("oauth: server does not support dynamic client registration and no client_id is cached")
		}
		clientID = stored.ClientID
		clientSecret = stored.ClientSecret
	}

	pkce, err := NewPKCE()
	if err != nil {
		return err
	}
	state, err := NewState()
	if err != nil {
		return err
	}

	authURL, err := buildAuthorizeURL(meta.AuthorizationEndpoint, loop.RedirectURI, clientID, e.Scopes, pkce, state)
	if err != nil {
		return err
	}

	// Open the URL via the injected opener if present; the Display
	// is always invoked so the URL is visible to the user in the
	// TUI regardless of browser-launch success.
	if e.Open != nil {
		e.Open(authURL)
	}
	d.ShowURL(authURL)

	// The loopback callback is the canonical signal that the user
	// finished. We wait for it directly; the Display's own Wait is
	// a hint and is not on the critical path. If ctx fires before
	// the callback does, surface that error so the caller can
	// re-drive the flow.
	var result LoopbackResult
	select {
	case result = <-loop.Done:
	case <-ctx.Done():
		return ctx.Err()
	}

	if result.Err != nil {
		return result.Err
	}
	if result.Code == "" {
		return errors.New("oauth: callback did not include an authorization code")
	}
	if !VerifyState(state, result.State) {
		return errors.New("oauth: callback state did not match — possible CSRF, aborting")
	}

	tokens, err := Exchange(ctx, e.http(), meta.TokenEndpoint, result.Code, loop.RedirectURI, clientID, pkce.Verifier)
	if err != nil {
		return fmt.Errorf("oauth: exchange: %w", err)
	}
	tokens.ClientID = clientID
	tokens.ClientSecret = clientSecret
	tokens.TokenEndpoint = meta.TokenEndpoint

	registerSecretsWithRedact(tokens.AccessToken, tokens.RefreshToken)

	e.mu.Lock()
	defer e.mu.Unlock()
	return e.persistLocked(tokens)
}
