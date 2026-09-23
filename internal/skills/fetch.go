package skills

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSkillFetchTimeout = 30 * time.Second
	skillFetchResolveTimeout = 5 * time.Second
	maxSkillFetchBytes       = 1 << 20 // 1 MiB
)

// ipResolver matches net.DefaultResolver so tests can inject fake DNS.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// defaultSkillResolver is the production resolver used by SSRF checks.
var defaultSkillResolver ipResolver = net.DefaultResolver

// isRawSkillURL reports whether s is a raw single-file skill URL: an
// http(s) URL ending in ".md" that is not a git URL. It must be checked
// before looksLikeGitURL, which treats every http(s) string as a git URL
// and would mis-route a raw skill file down a failing git clone path.
func isRawSkillURL(s string) bool {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return false
	}
	if strings.HasSuffix(s, ".git") {
		return false
	}
	return strings.HasSuffix(s, ".md")
}

// skillFetcher downloads raw skill URLs with the same SSRF posture as
// web.fetch: http(s) only, loopback/private/link-local literal hosts and
// redirect targets blocked, bounded size and timeout. It is a struct so
// tests can inject a client, a resolver, and a permissive SSRF check.
type skillFetcher struct {
	client    *http.Client
	ssrfCheck func(*url.URL) bool
	maxBytes  int64
}

// defaultSkillFetcher is the production fetcher used by fetchRawSkillURL.
var defaultSkillFetcher = &skillFetcher{
	ssrfCheck: isPrivateSkillURL,
	maxBytes:  maxSkillFetchBytes,
}

// httpClient returns a client with the SSRF redirect guard installed. A
// caller-supplied client is copied so its transport (e.g. an httptest
// server's) is preserved while the redirect guard is still applied.
func (f *skillFetcher) httpClient() *http.Client {
	base := f.client
	if base == nil {
		base = &http.Client{Timeout: defaultSkillFetchTimeout}
	}
	c := *base
	if c.Transport == nil {
		c.Transport = &http.Transport{DialContext: makeSafeSkillDialContext(defaultSkillResolver)}
	}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		u, err := url.Parse(req.URL.String())
		if err != nil {
			return fmt.Errorf("invalid redirect target: %w", err)
		}
		if f.ssrfCheck(u) {
			return fmt.Errorf("redirect to private or link-local address blocked: %s", req.URL.String())
		}
		return nil
	}
	return &c
}

// fetch downloads rawURL and returns its body, rejecting non-200 responses
// and bodies larger than maxBytes.
func (f *skillFetcher) fetch(ctx context.Context, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("only http(s) URLs are allowed")
	}
	if f.ssrfCheck(parsed) {
		return nil, fmt.Errorf("URL resolves to a private or link-local address")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "marshal/0.1")
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", rawURL, resp.Status)
	}

	max := f.maxBytes
	if max <= 0 {
		max = maxSkillFetchBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("fetch %s: response exceeds %d bytes", rawURL, max)
	}
	return body, nil
}

// fetchRawSkillBytes downloads rawURL and returns its body.
func fetchRawSkillBytes(ctx context.Context, rawURL string) ([]byte, error) {
	return defaultSkillFetcher.fetch(ctx, rawURL)
}

// fetchRawSkillURL downloads rawURL and writes it to a temp .md file,
// returning the file path and a cleanup func the caller must invoke. The
// temp file is named after the URL's last path segment so that Install
// derives a sensible skill name when the caller passes none.
func fetchRawSkillURL(ctx context.Context, rawURL string) (string, func(), error) {
	body, err := fetchRawSkillBytes(ctx, rawURL)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "marshal-skill-fetch-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dest := filepath.Join(dir, skillFileNameFromURL(rawURL))
	if err := os.WriteFile(dest, body, 0644); err != nil {
		cleanup()
		return "", nil, err
	}
	return dest, cleanup, nil
}

// skillFileNameFromURL derives a safe temp file name from a raw skill URL,
// falling back to "skill.md" when the URL's last segment is unusable.
func skillFileNameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		base := path.Base(u.Path)
		if strings.HasSuffix(base, ".md") {
			if name := strings.TrimSuffix(base, ".md"); ValidName(name) {
				return base
			}
		}
	}
	return "skill.md"
}

func isPrivateSkillURL(u *url.URL) bool {
	return isPrivateSkillURLWithResolver(u, defaultSkillResolver)
}

func isPrivateSkillURLWithResolver(u *url.URL, resolver ipResolver) bool {
	host := u.Hostname()
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateSkillIP(ip)
	}
	if resolver == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), skillFetchResolveTimeout)
	defer cancel()
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return true
	}
	for _, ip := range ips {
		if isPrivateSkillIP(ip.IP) {
			return true
		}
	}
	return false
}

func isPrivateSkillIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate()
}

func makeSafeSkillDialContext(resolver ipResolver) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		if strings.EqualFold(host, "localhost") {
			return nil, fmt.Errorf("localhost is not allowed")
		}
		if ip := net.ParseIP(host); ip != nil {
			if isPrivateSkillIP(ip) {
				return nil, fmt.Errorf("private address %s is not allowed", addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
		if resolver == nil {
			return nil, fmt.Errorf("no resolver available")
		}
		ips, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("could not resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IPs returned for %q", host)
		}
		for _, ip := range ips {
			if isPrivateSkillIP(ip.IP) {
				return nil, fmt.Errorf("resolved private address for %q", host)
			}
		}
		// Pin to the first resolved IP to prevent DNS rebinding during the connection.
		dialAddr := net.JoinHostPort(ips[0].IP.String(), port)
		return (&net.Dialer{}).DialContext(ctx, network, dialAddr)
	}
}
