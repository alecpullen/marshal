package native

import (
	"context"
	"encoding/json"
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

func TestWebFetchRejectsSSRFRedirect(t *testing.T) {
	// Server returns a 302 redirect to a private IP (AWS metadata).
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer target.Close()

	// ssrfCheck returns false for the test server (so the initial request goes through)
	// but true for the redirect target (169.254.169.254).
	tools := &toolSet{
		webEnabled:     true,
		maxOutputBytes: 200000,
		ssrfCheck: func(u *url.URL) bool {
			return u.Hostname() == "169.254.169.254"
		},
		// webHTTPClient left nil so the handler constructs a client with CheckRedirect.
	}
	tool := tools.webFetchTool()
	args, _ := json.Marshal(map[string]any{"url": target.URL})
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
