package registry

import (
	"context"
	"testing"
)

func nopHandler(ctx context.Context, call ToolCall) (ToolResult, error) {
	return ToolResult{Summary: "ok"}, nil
}

func TestReadOnlyViewFiltersOutWriteTools(t *testing.T) {
	src := New()
	mustRegister := func(tool Tool) {
		t.Helper()
		if err := src.Register(tool); err != nil {
			t.Fatalf("Register(%s): %v", tool.Name, err)
		}
	}
	mustRegister(Tool{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler})
	mustRegister(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(Tool{Name: "shell.run", Description: "shell", Risk: RiskCommand, Handler: nopHandler})

	view := ReadOnlyView(src)

	if _, ok := view.Lookup("file.read"); !ok {
		t.Fatal("read-only tool missing from view")
	}
	if _, ok := view.Lookup("file.write_patch"); ok {
		t.Fatal("workspace_write tool must not be in read-only view")
	}
	if _, ok := view.Lookup("shell.run"); ok {
		t.Fatal("command tool must not be in read-only view")
	}
	if got := len(view.List()); got != 1 {
		t.Fatalf("view.List() has %d tools, want 1", got)
	}
	// Source registry is untouched.
	if got := len(src.List()); got != 3 {
		t.Fatalf("source registry mutated: %d tools, want 3", got)
	}
}
