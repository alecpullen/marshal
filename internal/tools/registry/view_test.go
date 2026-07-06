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

func TestTesterViewAllowsReadAndCommandButNotWrites(t *testing.T) {
	src := New()
	mustRegister := func(tool Tool) {
		t.Helper()
		if err := src.Register(tool); err != nil {
			t.Fatalf("Register(%s): %v", tool.Name, err)
		}
	}
	mustRegister(Tool{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler})
	mustRegister(Tool{Name: "shell.run", Description: "shell", Risk: RiskCommand, Handler: nopHandler})
	mustRegister(Tool{Name: "test.run", Description: "test", Risk: RiskCommand, Handler: nopHandler})
	mustRegister(Tool{Name: "patch.apply", Description: "patch", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(Tool{Name: "fetch", Description: "net", Risk: RiskNetwork, Handler: nopHandler})

	view := TesterView(src)

	if _, ok := view.Lookup("file.read"); !ok {
		t.Error("TesterView should include read-only tools")
	}
	if _, ok := view.Lookup("test.run"); !ok {
		t.Error("TesterView should include test.run")
	}
	if _, ok := view.Lookup("shell.run"); ok {
		t.Error("TesterView must exclude arbitrary shell command tools")
	}
	if _, ok := view.Lookup("patch.apply"); ok {
		t.Error("TesterView must exclude workspace-write tools")
	}
	if _, ok := view.Lookup("fetch"); ok {
		t.Error("TesterView must exclude network tools")
	}
}

func TestTesterViewTestRunIgnoresCommandOverride(t *testing.T) {
	src := New()
	var gotArgs string
	if err := src.Register(Tool{
		Name:        "test.run",
		Description: "test",
		Risk:        RiskCommand,
		Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
			gotArgs = string(call.Args)
			return ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register(test.run): %v", err)
	}

	view := TesterView(src)
	tool, ok := view.Lookup("test.run")
	if !ok {
		t.Fatal("test.run missing from tester view")
	}
	if _, err := tool.Handler(context.Background(), ToolCall{Name: "test.run", Args: []byte(`{"command":"sed -i s/x/y/g file.go"}`)}); err != nil {
		t.Fatalf("test.run handler returned error: %v", err)
	}
	if gotArgs != "{}" {
		t.Fatalf("tester test.run args = %q, want {}", gotArgs)
	}
}
