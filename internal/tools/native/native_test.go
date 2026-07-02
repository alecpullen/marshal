package native

import (
	"context"
	"strings"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func TestRegisterAllRegistersExpectedTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()

	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll returned error: %v", err)
	}

	want := map[string]registry.RiskLevel{
		"file.read":        registry.RiskReadOnly,
		"file.write_patch": registry.RiskWorkspaceWrite,
		"repo.search":      registry.RiskReadOnly,
		"repo.index":       registry.RiskReadOnly,
		"repo.map":         registry.RiskReadOnly,
		"repo.card":        registry.RiskReadOnly,
		"symbols.find":     registry.RiskReadOnly,
		"git.status":       registry.RiskReadOnly,
		"git.diff":         registry.RiskReadOnly,
		"shell.run":        registry.RiskCommand,
		"test.run":         registry.RiskCommand,
	}
	if got := reg.List(); len(got) != len(want) {
		t.Fatalf("len(List()) = %d, want %d", len(got), len(want))
	}
	for name, risk := range want {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) ok=false", name)
		}
		if tool.Risk != risk {
			t.Fatalf("%s risk = %q, want %q", name, tool.Risk, risk)
		}
		if tool.Handler == nil {
			t.Fatalf("%s Handler is nil", name)
		}
		if len(tool.Schema) == 0 {
			t.Fatalf("%s Schema is empty", name)
		}
	}
}

func TestRegisterAllRequiresWorkspaceRoot(t *testing.T) {
	err := RegisterAll(registry.New(), Options{CommandRunner: &fakeRunner{}})
	if err == nil {
		t.Fatal("RegisterAll returned nil error, want workspace root error")
	}
	if !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("error = %q, want workspace root", err.Error())
	}
}

func TestResolveWorkspacePathRejectsTraversalAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()

	if _, err := resolveWorkspacePath(root, "../outside"); err == nil {
		t.Fatal("resolveWorkspacePath traversal returned nil error")
	}
	if _, err := resolveWorkspacePath(root, root); err == nil {
		t.Fatal("resolveWorkspacePath absolute path returned nil error")
	}
}

func TestLimitOutputTruncatesWithMarker(t *testing.T) {
	got := limitOutput("abcdef", 3)
	if got != "abc\n[output truncated]" {
		t.Fatalf("limitOutput = %q", got)
	}
}

type fakeRunner struct {
	requests []CommandRequest
	result   CommandResult
	err      error
}

func (f *fakeRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	f.requests = append(f.requests, req)
	if req.Timeout <= 0 {
		return CommandResult{}, context.DeadlineExceeded
	}
	return f.result, f.err
}

func invokeTool(t *testing.T, reg *registry.Registry, name string, args string) (registry.ToolResult, error) {
	t.Helper()
	tool, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) ok=false", name)
	}
	return tool.Handler(context.Background(), registry.ToolCall{Name: name, Args: []byte(args)})
}

func assertTimeout(t *testing.T, got time.Duration, want time.Duration) {
	t.Helper()
	if got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
}
