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
