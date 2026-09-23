package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validSkillBody(t *testing.T, name string) []byte {
	t.Helper()
	return []byte("+++\nname = \"" + name + "\"\ndescription = \"test skill\"\n+++\n\nBody text.\n")
}

func permissiveSkillFetcher(srv *httptest.Server) *skillFetcher {
	return &skillFetcher{
		client:    srv.Client(),
		ssrfCheck: func(*url.URL) bool { return false },
		maxBytes:  maxSkillFetchBytes,
	}
}

// swapDefaultSkillFetcher temporarily replaces the package-level fetcher.
func swapDefaultSkillFetcher(t *testing.T, f *skillFetcher) {
	t.Helper()
	old := defaultSkillFetcher
	defaultSkillFetcher = f
	t.Cleanup(func() { defaultSkillFetcher = old })
}

func TestIsRawSkillURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: "https://example.com/a/SKILL.md", want: true},
		{in: "http://example.com/x.md", want: true},
		{in: "https://github.com/o/r.git", want: false},
		{in: "https://github.com/o/r", want: false},
		{in: "github:o/r", want: false},
		{in: "git@github.com:o/r.git", want: false},
		{in: "/local/path/skill.md", want: false},
		{in: "./rel/skill.md", want: false},
		{in: "https://example.com/x.md.git", want: false},
		{in: "https://example.com/x.txt", want: false},
		{in: "", want: false},
		{in: "https://example.com/", want: false},
		// Query strings and fragments must not defeat the suffix test.
		{in: "https://example.com/SKILL.md?raw=1", want: true},
		{in: "https://example.com/SKILL.md#section", want: true},
		{in: "https://example.com/x.md.git?ref=main", want: false},
		// Scheme comparison is case-insensitive.
		{in: "HTTPS://example.com/x.md", want: true},
		{in: "HTTP://example.com/x.md", want: true},
		// A scheme with no host is not a fetchable URL.
		{in: "https:///x.md", want: false},
	}
	for _, tt := range tests {
		if got := isRawSkillURL(tt.in); got != tt.want {
			t.Errorf("isRawSkillURL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestFetchRawSkillURLWritesTempFile(t *testing.T) {
	ctx := context.Background()
	body := validSkillBody(t, "fetched-skill")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	url := srv.URL + "/SKILL.md"

	// Direct fetch: bytes must match.
	f := permissiveSkillFetcher(srv)
	got, err := f.fetch(ctx, url)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("fetched body mismatch: got %q want %q", got, body)
	}

	// End-to-end through fetchRawSkillURL with the default fetcher swapped.
	swapDefaultSkillFetcher(t, permissiveSkillFetcher(srv))
	path, cleanup, err := fetchRawSkillURL(ctx, url)
	if err != nil {
		t.Fatalf("fetchRawSkillURL: %v", err)
	}
	if !strings.HasSuffix(path, ".md") {
		t.Errorf("temp file %q does not end in .md", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("temp file contents mismatch: got %q want %q", data, body)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove temp file %q (stat err: %v)", path, err)
	}
}

func TestFetchRawSkillURLRejectsSSRFHost(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("irrelevant"))
	}))
	defer srv.Close()

	f := &skillFetcher{
		client:    srv.Client(),
		ssrfCheck: func(*url.URL) bool { return true },
		maxBytes:  maxSkillFetchBytes,
	}
	_, err := f.fetch(ctx, srv.URL+"/x.md")
	if err == nil {
		t.Fatal("expected error for blocked host, got nil")
	}
	if !strings.Contains(err.Error(), "private or link-local") {
		t.Errorf("error %q does not mention 'private or link-local'", err)
	}

	privateURLs := []string{
		"http://127.0.0.1/x.md",
		"http://localhost/x.md",
		"http://10.0.0.1/x.md",
		"http://169.254.169.254/x.md",
		"http://[::1]/x.md",
	}
	for _, u := range privateURLs {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("parse %q: %v", u, err)
		}
		if !isPrivateSkillURL(parsed) {
			t.Errorf("isPrivateSkillURL(%q) = false, want true", u)
		}
	}
	public, _ := url.Parse("http://93.184.216.34/x.md")
	if isPrivateSkillURL(public) {
		t.Errorf("isPrivateSkillURL(http://93.184.216.34/x.md) = true, want false")
	}
}

func TestFetchRawSkillURLRejectsNon200(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	f := permissiveSkillFetcher(srv)
	_, err := f.fetch(ctx, srv.URL+"/missing.md")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q does not mention the status", err)
	}
}

func TestFetchRawSkillURLRejectsOversize(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 32))
	}))
	defer srv.Close()
	f := &skillFetcher{
		client:    srv.Client(),
		ssrfCheck: func(*url.URL) bool { return false },
		maxBytes:  16,
	}
	_, err := f.fetch(ctx, srv.URL+"/big.md")
	if err == nil {
		t.Fatal("expected error for oversize body, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q does not mention 'exceeds'", err)
	}
}

func TestInstallRawSkillURL(t *testing.T) {
	ctx := context.Background()
	body := validSkillBody(t, "remote-skill")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	swapDefaultSkillFetcher(t, permissiveSkillFetcher(srv))

	targetDir := t.TempDir()
	got, err := Install(ctx, srv.URL+"/remote-skill.md", targetDir, "")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := filepath.Join(targetDir, "remote-skill.md")
	if got != want {
		t.Errorf("Install returned %q, want %q", got, want)
	}
	skill, err := parseSkillFile(got)
	if err != nil {
		t.Fatalf("parseSkillFile: %v", err)
	}
	if skill.Name != "remote-skill" {
		t.Errorf("skill.Name = %q, want %q", skill.Name, "remote-skill")
	}
}

// TestDefaultSkillFetcherBlocksPrivateHost exercises the production fetcher
// (real SSRF check, safe dialer) rather than a permissive fake, so the
// shipped posture is actually covered.
func TestDefaultSkillFetcherBlocksPrivateHost(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://127.0.0.1/x.md",
		"http://localhost/x.md",
		"http://169.254.169.254/x.md",
		"http://[::1]/x.md",
	} {
		if _, err := defaultSkillFetcher.fetch(ctx, u); err == nil {
			t.Errorf("defaultSkillFetcher.fetch(%q) = nil error, want SSRF block", u)
		} else if !strings.Contains(err.Error(), "private or link-local") {
			t.Errorf("defaultSkillFetcher.fetch(%q) error = %q, want 'private or link-local'", u, err)
		}
	}
}

// TestDefaultSkillFetcherInstallsSafeTransport pins the invariant that the
// production fetcher never dials without the SSRF guard: it has no
// caller-supplied client, and the client it builds carries both a safe
// dialer and a redirect check.
func TestDefaultSkillFetcherInstallsSafeTransport(t *testing.T) {
	if defaultSkillFetcher.client != nil {
		t.Fatal("defaultSkillFetcher must not carry a caller-supplied client")
	}
	client := defaultSkillFetcher.httpClient()
	if client.Transport == nil {
		t.Fatal("httpClient() returned a client with no transport")
	}
	if client.CheckRedirect == nil {
		t.Fatal("httpClient() returned a client with no redirect guard")
	}
}

// TestFetchRawSkillURLRejectsSSRFRedirect covers the redirect guard: a
// public-looking host that redirects to a private one must be blocked.
func TestFetchRawSkillURLRejectsSSRFRedirect(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://blocked.example/x.md", http.StatusFound)
	}))
	defer srv.Close()

	// Allow the initial request (the httptest server) but block the
	// redirect target, mirroring the web.fetch redirect test.
	f := &skillFetcher{
		client: srv.Client(),
		ssrfCheck: func(u *url.URL) bool {
			return u.Hostname() == "blocked.example"
		},
		maxBytes: maxSkillFetchBytes,
	}
	_, err := f.fetch(ctx, srv.URL+"/x.md")
	if err == nil {
		t.Fatal("expected SSRF redirect to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "redirect to private or link-local") {
		t.Errorf("error %q does not mention the blocked redirect", err)
	}
}

func TestSkillFileNameFromURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "https://example.com/a/b/my-skill.md", want: "my-skill.md"},
		{in: "https://example.com/a/b/../evil.md", want: "evil.md"},
		{in: "https://example.com/a/..md", want: "skill.md"},
	}
	for _, tt := range tests {
		if got := skillFileNameFromURL(tt.in); got != tt.want {
			t.Errorf("skillFileNameFromURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
