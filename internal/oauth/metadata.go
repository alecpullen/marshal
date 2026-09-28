package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// metadataCacheTTL is the on-disk freshness window. The in-memory cache
// lives for the process lifetime; the on-disk cache lets a second process
// skip the network round-trip on startup within 24 hours.
const metadataCacheTTL = 24 * time.Hour

// discoveryTimeout bounds a single metadata fetch. Discovery is a
// best-effort warm-up; we don't want a stuck server to block Authorize
// for the full 5-minute loopback timeout.
const discoveryTimeout = 15 * time.Second

// metadataResponse is the subset of RFC 8414 / OIDC metadata we actually
// use. We intentionally only decode the fields the MCP authorization spec
// requires; unknown fields are ignored so future additions to the spec do
// not break us.
type metadataResponse struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
}

// AuthorizeServerMetadata is the resolved authorization-server metadata.
// It is what the rest of the engine consumes.
type AuthorizeServerMetadata struct {
	Issuer                            string
	AuthorizationEndpoint             string
	TokenEndpoint                     string
	RegistrationEndpoint              string
	ScopesSupported                   []string
	CodeChallengeMethodsSupported     []string
	TokenEndpointAuthMethodsSupported []string
}

// fetchFunc is the discovery network call. It is injected so tests can
// supply an httptest.Server-driven implementation.
type fetchFunc func(ctx context.Context, u string) (*http.Response, error)

// MetadataCache caches authorization-server metadata both in memory and
// on disk. The on-disk layer is keyed by URL origin and stored as one JSON
// file per origin under cacheDir.
type MetadataCache struct {
	cacheDir string
	fetch    fetchFunc
	now      func() time.Time

	mu  sync.Mutex
	mem map[string]metadataCacheEntry
}

type metadataCacheEntry struct {
	meta     AuthorizeServerMetadata
	cachedAt time.Time
}

// NewMetadataCache constructs a MetadataCache. cacheDir is the directory
// used for the on-disk layer; if empty, DefaultCacheDir is consulted. The
// fetch function is used for the actual GET; if nil, an internal HTTP
// client derived from the supplied http.Client (or http.DefaultClient) is
// used.
func NewMetadataCache(cacheDir string, hc *http.Client) *MetadataCache {
	if hc == nil {
		hc = http.DefaultClient
	}
	f := func(ctx context.Context, u string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		return hc.Do(req)
	}
	return &MetadataCache{
		cacheDir: cacheDir,
		fetch:    f,
		now:      time.Now,
		mem:      make(map[string]metadataCacheEntry),
	}
}

// DefaultCacheDir returns the on-disk cache directory for a working
// directory. It mirrors internal/db.Path: when the working directory is
// inside a git repo, the cache anchors at the repo root, otherwise at the
// working directory itself. The directory is created lazily by Lookup.
func DefaultCacheDir(workingDir string) string {
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		abs = workingDir
	}
	root := abs
	for {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return filepath.Join(root, ".marshal", "oauth-metadata")
}

// Lookup returns the metadata for the given MCP server URL, consulting the
// in-memory cache, then the on-disk cache, then falling back to discovery.
// The on-disk layer uses cacheDir (or DefaultCacheDir(".") if empty) for
// storage.
func (c *MetadataCache) Lookup(ctx context.Context, serverURL string) (AuthorizeServerMetadata, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return AuthorizeServerMetadata{}, fmt.Errorf("oauth: invalid server URL %q", serverURL)
	}
	origin := originOf(u)

	if meta, ok := c.fromMemory(origin); ok {
		return meta, nil
	}

	if meta, ok, err := c.fromDisk(origin); err != nil {
		// A corrupt cache file is non-fatal: log via the returned
		// error and fall through to the network. We swallow the
		// error so the caller still gets a metadata result.
		_ = err
	} else if ok {
		c.toMemory(origin, meta)
		return meta, nil
	}

	meta, err := c.discover(ctx, u)
	if err != nil {
		return AuthorizeServerMetadata{}, err
	}
	c.toMemory(origin, meta)
	if dirErr := c.ensureDir(); dirErr == nil {
		_ = c.toDisk(origin, meta)
	}
	return meta, nil
}

// Invalidate removes any cached metadata for the given server URL's
// origin. Tests use this to force a fresh discovery.
func (c *MetadataCache) Invalidate(serverURL string) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Host == "" {
		return
	}
	origin := originOf(u)
	c.mu.Lock()
	delete(c.mem, origin)
	c.mu.Unlock()
	if c.cacheDir == "" {
		return
	}
	_ = os.Remove(filepath.Join(c.cacheDir, originFilename(origin)))
}

func (c *MetadataCache) fromMemory(origin string) (AuthorizeServerMetadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.mem[origin]
	if !ok {
		return AuthorizeServerMetadata{}, false
	}
	if c.now().Sub(e.cachedAt) > metadataCacheTTL {
		delete(c.mem, origin)
		return AuthorizeServerMetadata{}, false
	}
	return e.meta, true
}

func (c *MetadataCache) toMemory(origin string, meta AuthorizeServerMetadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mem[origin] = metadataCacheEntry{meta: meta, cachedAt: c.now()}
}

func (c *MetadataCache) fromDisk(origin string) (AuthorizeServerMetadata, bool, error) {
	if c.cacheDir == "" {
		return AuthorizeServerMetadata{}, false, nil
	}
	path := filepath.Join(c.cacheDir, originFilename(origin))
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AuthorizeServerMetadata{}, false, nil
		}
		return AuthorizeServerMetadata{}, false, err
	}
	var onDisk onDiskMetadata
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		return AuthorizeServerMetadata{}, false, err
	}
	if c.now().Sub(onDisk.CachedAt) > metadataCacheTTL {
		return AuthorizeServerMetadata{}, false, nil
	}
	return onDisk.Metadata, true, nil
}

func (c *MetadataCache) toDisk(origin string, meta AuthorizeServerMetadata) error {
	if c.cacheDir == "" {
		return nil
	}
	if err := c.ensureDir(); err != nil {
		return err
	}
	onDisk := onDiskMetadata{Metadata: meta, CachedAt: c.now()}
	raw, err := json.Marshal(onDisk)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.cacheDir, originFilename(origin)), raw, 0o600)
}

func (c *MetadataCache) ensureDir() error {
	if c.cacheDir == "" {
		return errors.New("oauth: metadata cache directory not configured")
	}
	return os.MkdirAll(c.cacheDir, 0o700)
}

// onDiskMetadata is the on-disk wire format. CachedAt lets us expire the
// file independently of any in-memory state.
type onDiskMetadata struct {
	Metadata AuthorizeServerMetadata `json:"metadata"`
	CachedAt time.Time               `json:"cached_at"`
}

// discover performs the actual network round-trip: it tries the RFC 8414
// endpoint first, then the OIDC discovery endpoint as a fallback, and
// finally returns the first one that yields a usable payload. Both
// endpoints are 404-fallback-safe per RFC 8414 §3.
func (c *MetadataCache) discover(ctx context.Context, u *url.URL) (AuthorizeServerMetadata, error) {
	dctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	candidates := discoveryCandidates(u)
	var lastErr error
	for _, ep := range candidates {
		meta, err := c.fetchMetadata(dctx, ep)
		if err == nil && meta.AuthorizationEndpoint != "" && meta.TokenEndpoint != "" {
			if verr := validateMetadata(meta, ep, u.Scheme); verr != nil {
				lastErr = verr
				continue
			}
			return meta, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no authorization-server metadata endpoint returned a usable document")
	}
	return AuthorizeServerMetadata{}, fmt.Errorf("oauth: discover %s: %w", u.Host, lastErr)
}

// discoveryCandidates returns the URL(s) we should try for metadata. The
// RFC 8414 path is preferred; if the server URL has a path component (an
// MCP endpoint like /mcp) we ALSO try the OIDC fallback at the issuer
// root, which is what OIDC providers actually publish.
func discoveryCandidates(u *url.URL) []string {
	authorizationServerPath := u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server"
	if u.Path != "" && u.Path != "/" {
		authorizationServerPath = u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server" + u.Path
	}
	openIDConfiguration := u.Scheme + "://" + u.Host + "/.well-known/openid-configuration"
	// Try RFC 8414 first, then OIDC. The MCP authorization spec calls out
	// both, so this matches what other MCP clients do.
	return []string{authorizationServerPath, openIDConfiguration}
}

func (c *MetadataCache) fetchMetadata(ctx context.Context, endpoint string) (AuthorizeServerMetadata, error) {
	resp, err := c.fetch(ctx, endpoint)
	if err != nil {
		return AuthorizeServerMetadata{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Fall through to the next candidate.
		return AuthorizeServerMetadata{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return AuthorizeServerMetadata{}, fmt.Errorf("metadata endpoint %s returned HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw metadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return AuthorizeServerMetadata{}, fmt.Errorf("metadata endpoint %s returned invalid JSON: %w", endpoint, err)
	}
	return AuthorizeServerMetadata{
		Issuer:                            raw.Issuer,
		AuthorizationEndpoint:             raw.AuthorizationEndpoint,
		TokenEndpoint:                     raw.TokenEndpoint,
		RegistrationEndpoint:              raw.RegistrationEndpoint,
		ScopesSupported:                   raw.ScopesSupported,
		CodeChallengeMethodsSupported:     raw.CodeChallengeMethodsSupported,
		TokenEndpointAuthMethodsSupported: raw.TokenEndpointAuthMethodsSupported,
	}, nil
}

// validateMetadata enforces the endpoint trust rules on a discovered
// document. Every endpoint must use the same scheme as the configured
// server URL — in production that URL is https-enforced by
// mcp.ValidateRemoteServer, so this rejects a document that downgrades the
// token exchange to cleartext. A declared issuer must live on the same
// origin as the document that declared it, so a benign host cannot point
// the flow at an attacker's issuer.
func validateMetadata(meta AuthorizeServerMetadata, docURL, serverScheme string) error {
	for _, ep := range []struct{ name, value string }{
		{"authorization_endpoint", meta.AuthorizationEndpoint},
		{"token_endpoint", meta.TokenEndpoint},
		{"registration_endpoint", meta.RegistrationEndpoint},
	} {
		if ep.value == "" {
			continue
		}
		u, err := url.Parse(ep.value)
		if err != nil || u.Host == "" {
			return fmt.Errorf("metadata %s %q is not a valid absolute URL", ep.name, ep.value)
		}
		if u.Scheme != serverScheme {
			return fmt.Errorf("metadata %s %q uses scheme %q but the server URL uses %q", ep.name, ep.value, u.Scheme, serverScheme)
		}
	}
	if meta.Issuer == "" {
		return nil
	}
	iu, err := url.Parse(meta.Issuer)
	if err != nil || iu.Host == "" {
		return fmt.Errorf("metadata issuer %q is not a valid absolute URL", meta.Issuer)
	}
	du, err := url.Parse(docURL)
	if err != nil {
		return fmt.Errorf("metadata document URL %q is not a valid URL", docURL)
	}
	if iu.Scheme != du.Scheme || iu.Host != du.Host {
		return fmt.Errorf("metadata issuer %q does not match the document origin %s://%s", meta.Issuer, du.Scheme, du.Host)
	}
	return nil
}

// originOf returns scheme://host[:port] for the URL. We use it as the
// cache key because two URLs that differ only in path can still resolve
// to the same authorization server (e.g. /mcp and /api/mcp).
func originOf(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

// originFilename turns an origin into a safe on-disk filename.
// Colons are not portable on Windows; we replace them and any other
// non-alphanumeric characters with '_'.
func originFilename(origin string) string {
	var b strings.Builder
	for _, r := range origin {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String() + ".json"
}
