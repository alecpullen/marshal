package native

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestDiagnosticsCheckToolReturnsNoneForUnknownLanguage(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("diagnostics.check")
	if !ok {
		t.Fatal("diagnostics.check not registered")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("Risk = %q, want RiskReadOnly", tool.Risk)
	}
	if !strings.Contains(string(tool.Schema), `"path"`) {
		t.Fatalf("Schema = %s, want path property", tool.Schema)
	}

	res, err := invokeTool(t, reg, "diagnostics.check", `{"path":"foo.txt"}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.Summary != "diagnostics check complete" {
		t.Fatalf("Summary = %q", res.Summary)
	}
	if res.Content != "diagnostics: none" {
		t.Fatalf("Content = %q, want diagnostics: none", res.Content)
	}
}
