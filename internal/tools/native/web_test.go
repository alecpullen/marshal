package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestWebFetchBlocksPrivateIP(t *testing.T) {
	tools := &toolSet{webEnabled: true, ssrfCheck: isPrivateURL}
	tool := tools.webFetchTool()
	args, _ := json.Marshal(map[string]any{"url": "http://169.254.169.254/latest/meta-data/"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected SSRF block")
	}
}

func TestWebFetchBlocksZeroAddr(t *testing.T) {
	u, err := url.Parse("http://0.0.0.0/")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !isPrivateURL(u) {
		t.Fatal("isPrivateURL should block 0.0.0.0")
	}

	tools := &toolSet{webEnabled: true, ssrfCheck: isPrivateURL}
	tool := tools.webFetchTool()
	args, _ := json.Marshal(map[string]any{"url": "http://0.0.0.0:80/admin"})
	_, err = tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected SSRF block for 0.0.0.0")
	}
}

func TestHtmlToTextDecodesNumericAndNamedEntities(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Tom &amp; Jerry", "Tom & Jerry"},
		{"it&#39;s fine", "it's fine"},
		{"it&#x27;s fine", "it's fine"},
		{"&copy; 2026", "© 2026"},
		{"&nbsp;hi", "hi"},
	}
	for _, c := range cases {
		if got := htmlToText(c.in); got != c.want {
			t.Errorf("htmlToText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWebFetchRejectsSSRFRedirect(t *testing.T) {
	// Mock a server that returns a 302 redirect to a private IP (AWS metadata).
	mockClient := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"http://169.254.169.254/latest/meta-data/"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}

	// ssrfCheck returns false for the initial request (example.com) but true
	// for the redirect target (169.254.169.254).
	tools := &toolSet{
		webEnabled:     true,
		maxOutputBytes: 200000,
		webHTTPClient:  mockClient,
		ssrfCheck: func(u *url.URL) bool {
			return u.Hostname() == "169.254.169.254"
		},
	}
	tool := tools.webFetchTool()
	args, _ := json.Marshal(map[string]any{"url": "http://example.com/redirect"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected SSRF redirect to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "redirect") && !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected SSRF/redirect error, got %v", err)
	}
}

func TestWebFetchReturnsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>hello</body></html>"))
	}))
	defer srv.Close()

	tools := &toolSet{
		webEnabled:     true,
		maxOutputBytes: 200000,
		webHTTPClient:  srv.Client(),
		ssrfCheck:      func(*url.URL) bool { return false },
	}
	tool := tools.webFetchTool()
	args, _ := json.Marshal(map[string]any{"url": srv.URL})
	res, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("content = %q", res.Content)
	}
}

// fakeResolver is a test double for net.Resolver.
type fakeResolver struct {
	ips []net.IPAddr
	err error
}

func (f *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.ips, nil
}

func TestIsPrivateURLResolvesHostname(t *testing.T) {
	mustParse := func(s string) *url.URL {
		t.Helper()
		u, err := url.Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	if !isPrivateURLWithResolver(mustParse("http://localhost/admin"), nil) {
		t.Error("localhost should be blocked without resolution")
	}
	if !isPrivateURLWithResolver(mustParse("http://127.0.0.1/admin"), nil) {
		t.Error("127.0.0.1 should be blocked")
	}
	if !isPrivateURLWithResolver(mustParse("http://0.0.0.0/admin"), nil) {
		t.Error("0.0.0.0 should be blocked")
	}

	public := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	if isPrivateURLWithResolver(mustParse("http://example.com/admin"), public) {
		t.Error("hostname resolving to public IP should be allowed")
	}

	private := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	if !isPrivateURLWithResolver(mustParse("http://metadata.example/admin"), private) {
		t.Error("hostname resolving to private IP should be blocked")
	}

	mixed := &fakeResolver{ips: []net.IPAddr{
		{IP: net.ParseIP("8.8.8.8")},
		{IP: net.ParseIP("10.0.0.1")},
	}}
	if !isPrivateURLWithResolver(mustParse("http://mixed.example/admin"), mixed) {
		t.Error("hostname resolving to any private IP should be blocked")
	}

	failed := &fakeResolver{err: errors.New("NXDOMAIN")}
	if !isPrivateURLWithResolver(mustParse("http://missing.example/admin"), failed) {
		t.Error("hostname that fails resolution should be blocked")
	}
}

func TestSafeDialContextBlocksPrivateAddr(t *testing.T) {
	// Literal private IP blocked before any network attempt.
	dial := makeSafeDialContext(&fakeResolver{})
	if _, err := dial(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("expected dial to 127.0.0.1 to be blocked")
	}

	// Literal public IP is not rejected as private (connection may fail, but for a different reason).
	_, err := dial(context.Background(), "tcp", "8.8.8.8:53")
	if err != nil && strings.Contains(err.Error(), "private") {
		t.Fatalf("8.8.8.8 should not be rejected as private: %v", err)
	}

	// Hostname resolving to private IP blocked.
	privateResolver := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	dial = makeSafeDialContext(privateResolver)
	_, err = dial(context.Background(), "tcp", "metadata.example:80")
	if err == nil {
		t.Fatal("expected hostname resolving to private IP to be blocked")
	}
	if !strings.Contains(err.Error(), "private") {
		t.Fatalf("expected private-address error, got %v", err)
	}

	// Hostname resolving to public IP is not rejected as private.
	publicResolver := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}}
	dial = makeSafeDialContext(publicResolver)
	_, err = dial(context.Background(), "tcp", "example.com:53")
	if err != nil && strings.Contains(err.Error(), "private") {
		t.Fatalf("example.com should not be rejected as private: %v", err)
	}
}

func TestWebFetchBlocksHostnameSSRF(t *testing.T) {
	resolver := &fakeResolver{ips: []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}}
	tools := &toolSet{
		webEnabled: true,
		ssrfCheck: func(u *url.URL) bool {
			return isPrivateURLWithResolver(u, resolver)
		},
	}
	tool := tools.webFetchTool()
	args, _ := json.Marshal(map[string]any{"url": "http://metadata.example/latest/meta-data/"})
	_, err := tool.Handler(context.Background(), registry.ToolCall{Args: args})
	if err == nil {
		t.Fatal("expected hostname SSRF to be blocked")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("expected private/link-local error, got %v", err)
	}
}
