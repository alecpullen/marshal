package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func newCSVInspectToolForTest(root string) registry.Tool {
	return (&toolSet{root: root, maxOutputBytes: defaultMaxOutputBytes}).csvInspectTool()
}

func callCSVInspect(t *testing.T, root, argsJSON string) (registry.ToolResult, error) {
	t.Helper()
	tool := newCSVInspectToolForTest(root)
	return tool.Handler(context.Background(), registry.ToolCall{Args: json.RawMessage(argsJSON)})
}

func TestCSVInspectBasic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "people.csv"), "name,role\nalice,eng\nbob,ops\ncarol,eng\n")

	res, err := callCSVInspect(t, root, `{"path":"people.csv"}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	for _, want := range []string{"columns: name, role", "total_rows: 3", "alice,eng", "carol,eng"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, res.Content)
		}
	}
}

func TestCSVInspectDelimiterDetection(t *testing.T) {
	cases := map[string]string{
		"comma":     "name,role\nalice,eng\nbob,ops\n",
		"semicolon": "name;role\nalice;eng\nbob;ops\n",
		"tab":       "name\trole\nalice\teng\nbob\tops\n",
		"pipe":      "name|role\nalice|eng\nbob|ops\n",
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "data.csv"), fixture)

			res, err := callCSVInspect(t, root, `{"path":"data.csv"}`)
			if err != nil {
				t.Fatalf("handler error: %v", err)
			}
			if !strings.Contains(res.Content, "columns: name, role") {
				t.Fatalf("Content missing detected columns:\n%s", res.Content)
			}
			if !strings.Contains(res.Content, "total_rows: 2") {
				t.Fatalf("Content missing row count:\n%s", res.Content)
			}
		})
	}
}

func TestCSVInspectColumnsSubset(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "people.csv"), "name,role,team\nalice,eng,core\n")

	res, err := callCSVInspect(t, root, `{"path":"people.csv","columns":["role"]}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(res.Content, "\nrole\neng\n") {
		t.Fatalf("Content should project only the role column:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "alice") {
		t.Fatalf("projected output should not contain other columns:\n%s", res.Content)
	}
}

func TestCSVInspectWhere(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "people.csv"), "name,role\nalice,eng\nbob,ops\ncarol,eng\n")

	res, err := callCSVInspect(t, root, `{"path":"people.csv","where":"role=eng"}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(res.Content, "matched 2 rows") {
		t.Fatalf("Content missing matched count:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "bob") {
		t.Fatalf("filtered output should not contain bob:\n%s", res.Content)
	}
}

func TestCSVInspectPaging(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("n\n")
	for i := 0; i < 10; i++ {
		b.WriteString(string(rune('a' + i)))
		b.WriteString("\n")
	}
	writeFile(t, filepath.Join(root, "rows.csv"), b.String())

	page1, err := callCSVInspect(t, root, `{"path":"rows.csv","offset":0,"limit":2}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	page2, err := callCSVInspect(t, root, `{"path":"rows.csv","offset":2,"limit":2}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(page1.Content, "showing 2 row(s) from offset 0") {
		t.Fatalf("page1 header wrong:\n%s", page1.Content)
	}
	if !strings.Contains(page2.Content, "showing 2 row(s) from offset 2") {
		t.Fatalf("page2 header wrong:\n%s", page2.Content)
	}
	if strings.Contains(page2.Content, "\na\n") || !strings.Contains(page2.Content, "\nc\n") {
		t.Fatalf("page2 should show rows c,d, not a:\n%s", page2.Content)
	}
}

func TestCSVInspectRaggedRows(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ragged.csv"), "a,b,c\n1,2,3\n4,5\n")

	res, err := callCSVInspect(t, root, `{"path":"ragged.csv"}`)
	if err != nil {
		t.Fatalf("handler error for ragged rows: %v", err)
	}
	if !strings.Contains(res.Content, "total_rows: 2") {
		t.Fatalf("Content:\n%s", res.Content)
	}
}

func TestCSVInspectRejectsBadArgs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "people.csv"), "name,role\nalice,eng\n")

	if _, err := callCSVInspect(t, root, `{"path":"people.csv","columns":["nope"]}`); err == nil {
		t.Fatal("expected error for unknown column")
	}
	if _, err := callCSVInspect(t, root, `{"path":"people.csv","where":"role"}`); err == nil {
		t.Fatal("expected error for where without =")
	}
	if _, err := callCSVInspect(t, root, `{"path":"../x.csv"}`); err == nil {
		t.Fatal("expected error for traversal")
	}
	if _, err := callCSVInspect(t, root, `{"path":"people.csv","delimiter":"?"}`); err == nil {
		t.Fatal("expected error for unknown delimiter")
	}
}
