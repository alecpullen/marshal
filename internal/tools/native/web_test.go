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
