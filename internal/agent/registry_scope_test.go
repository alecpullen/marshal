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

// TestDenylistRemovesImageView pins the text-only-route gate: a registry that
// carries image.view loses it when the route cannot take images.
func TestDenylistRemovesImageView(t *testing.T) {
	src := registry.New()
	mustReg := func(tool registry.Tool) {
		if err := src.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	mustReg(registry.Tool{Name: "image.view", Risk: registry.RiskReadOnly, Handler: nopHandler})
	mustReg(registry.Tool{Name: "file.read", Risk: registry.RiskReadOnly, Handler: nopHandler})
	view := DenylistView(src, []string{"image.view"})
	if _, ok := view.Lookup("image.view"); ok {
		t.Fatal("image.view survived a denylist that names it")
	}
	names := toolNames(view)
	if len(names) != 1 || names[0] != "file.read" {
		t.Fatalf("got %v, want [file.read]", names)
	}
}

// TestSubtaskScopeViewExcludesEscapeHatches pins the second half of the child
// scope: the tools that would let a child leave the workspace its dispatcher
// bound it to. mode.request and tools.select widen the child's own surface,
// workspace.worktree and workspace.finish rebind or merge a checkout (the
// parent owns integration), and shell.sudo is privileged execution. A child
// asks its parent; it never re-targets itself.
func TestSubtaskScopeViewExcludesEscapeHatches(t *testing.T) {
	src := registry.New()
	for _, name := range []string{
		"mode.request", "tools.select",
		"workspace.worktree", "workspace.finish",
		"shell.sudo", "file.read", "shell.run",
	} {
		tool := registry.Tool{Name: name, Schema: []byte(`{"type":"object"}`), Risk: registry.RiskWorkspaceWrite}
		tool.Handler = nopHandler
		if err := src.Register(tool); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	view := SubtaskScopeView(src)
	for _, name := range []string{
		"mode.request", "tools.select", "workspace.worktree", "workspace.finish", "shell.sudo",
	} {
		if _, ok := view.Lookup(name); ok {
			t.Fatalf("%s must be excluded from the subtask scope view", name)
		}
	}
	for _, name := range []string{"file.read", "shell.run"} {
		if _, ok := view.Lookup(name); !ok {
			t.Fatalf("%s must remain visible to a subtask", name)
		}
	}
}

func toolNames(r *registry.Registry) []string {
	var out []string
	for _, t := range r.List() {
		out = append(out, t.Name)
	}
	return out
}
