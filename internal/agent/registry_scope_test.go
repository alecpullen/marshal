package agent

import (
	"context"
	"testing"

	"marshal/internal/tools/registry"
)

func nopHandler(_ context.Context, _ registry.ToolCall) (registry.ToolResult, error) {
	return registry.ToolResult{Summary: "ok"}, nil
}

func TestDenylistViewRemovesNamedTools(t *testing.T) {
	src := registry.New()
	mustReg := func(tool registry.Tool) {
		if err := src.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	mustReg(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: nopHandler})
	mustReg(registry.Tool{Name: "file.write", Risk: registry.RiskWorkspaceWrite, Handler: nopHandler})
	mustReg(registry.Tool{Name: "shell.run", Risk: registry.RiskCommand, Handler: nopHandler})
	view := DenylistView(src, []string{"file.write", "shell.run"})
	names := toolNames(view)
	if len(names) != 1 || names[0] != "file.read" {
		t.Fatalf("got %v, want [file.read]", names)
	}
}

func TestDenylistViewEmptyInheritsAll(t *testing.T) {
	src := registry.New()
	if err := src.Register(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: nopHandler}); err != nil {
		t.Fatalf("register: %v", err)
	}
	view := DenylistView(src, nil)
	if len(toolNames(view)) != 1 {
		t.Fatalf("empty denylist should inherit all, got %v", toolNames(view))
	}
}

func toolNames(r *registry.Registry) []string {
	var out []string
	for _, t := range r.List() {
		out = append(out, t.Name)
	}
	return out
}
