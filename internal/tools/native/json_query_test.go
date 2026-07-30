package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func newJSONQueryToolForTest(root string) registry.Tool {
	return (&toolSet{root: root, maxOutputBytes: defaultMaxOutputBytes}).jsonQueryTool()
}

func callJSONQuery(t *testing.T, root, argsJSON string) (registry.ToolResult, error) {
	t.Helper()
	tool := newJSONQueryToolForTest(root)
	return tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(argsJSON)})
}

const usersFixture = `{"users":[{"name":"alice","active":true},{"name":"bob","active":false}],"meta":{"count":2}}`

func TestJSONQueryArrayIteration(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data.json"), usersFixture)

	res, err := callJSONQuery(t, root, `{"path":"data.json","query":".users[] | .name"}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(res.Content, `"alice"`) || !strings.Contains(res.Content, `"bob"`) {
		t.Fatalf("Content = %q, want both names", res.Content)
	}
}

func TestJSONQuerySelectFilter(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data.json"), usersFixture)

	res, err := callJSONQuery(t, root, `{"path":"data.json","query":".users[] | select(.active) | .name"}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(res.Content, `"alice"`) {
		t.Fatalf("Content = %q, want alice", res.Content)
	}
	if strings.Contains(res.Content, `"bob"`) {
		t.Fatalf("Content = %q, inactive bob should be filtered out", res.Content)
	}
}

func TestJSONQueryLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data.json"), usersFixture)

	res, err := callJSONQuery(t, root, `{"path":"data.json","query":".users[]","limit":1}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(res.Summary, "limited") {
		t.Fatalf("Summary = %q, want a limited note", res.Summary)
	}
	if strings.Count(res.Content, "\n") > 1 {
		t.Fatalf("Content = %q, want exactly 1 result line", res.Content)
	}
}

func TestJSONQueryInvalidExpression(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "data.json"), usersFixture)

	_, err := callJSONQuery(t, root, `{"path":"data.json","query":".[|"}`)
	if err == nil {
		t.Fatal("expected error for invalid jq expression")
	}
	if !strings.Contains(err.Error(), "invalid jq") {
		t.Fatalf("error = %v, want it to say %q so the model can correct itself", err, "invalid jq")
	}
}

func TestJSONQueryMalformedFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bad.json"), `{not json`)

	_, err := callJSONQuery(t, root, `{"path":"bad.json","query":"."}`)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("error = %v, want it to say %q", err, "not valid JSON")
	}
}

func TestJSONQueryRejectsTraversalAndMissing(t *testing.T) {
	root := t.TempDir()

	if _, err := callJSONQuery(t, root, `{"path":"../outside.json","query":"."}`); err == nil {
		t.Fatal("expected error for path traversal")
	}
	if _, err := callJSONQuery(t, root, `{"path":"missing.json","query":"."}`); err == nil {
		t.Fatal("expected error for missing file")
	}
}
