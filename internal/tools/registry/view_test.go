package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
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

func TestOrchestratorViewDropsWriteAndShellTools(t *testing.T) {
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
	mustRegister(Tool{Name: "sdd.verify", Description: "verify", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(Tool{Name: "agent.run", Description: "agent", Risk: RiskReadOnly, Handler: nopHandler})

	view := OrchestratorView(src)

	if _, ok := view.Lookup("file.read"); !ok {
		t.Error("file.read should be present (read-only tool)")
	}
	if _, ok := view.Lookup("sdd.verify"); !ok {
		t.Error("sdd.verify should be present (sdd.* workspace-write tool)")
	}
	if _, ok := view.Lookup("agent.run"); !ok {
		t.Error("agent.run should be present (read-only tool)")
	}
	if _, ok := view.Lookup("file.write_patch"); ok {
		t.Error("file.write_patch should be dropped (non-sdd workspace-write tool)")
	}
	if _, ok := view.Lookup("shell.run"); ok {
		t.Error("shell.run should be dropped (command tool)")
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

func TestArtifactWriterViewFiltersTools(t *testing.T) {
	src := New()
	mustRegister := func(tool Tool) {
		t.Helper()
		if err := src.Register(tool); err != nil {
			t.Fatalf("Register(%s): %v", tool.Name, err)
		}
	}
	mustRegister(Tool{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler})
	mustRegister(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(Tool{Name: "patch.apply", Description: "patch", Risk: RiskWorkspaceWrite, Handler: nopHandler})
	mustRegister(Tool{Name: "shell.run", Description: "shell", Risk: RiskCommand, Handler: nopHandler})
	mustRegister(Tool{Name: "fetch", Description: "net", Risk: RiskNetwork, Handler: nopHandler})

	view := ArtifactWriterView(src, "@run")

	if _, ok := view.Lookup("file.read"); !ok {
		t.Error("ArtifactWriterView should include read-only tools")
	}
	if _, ok := view.Lookup("file.write_patch"); !ok {
		t.Error("ArtifactWriterView should include file.write_patch")
	}
	if _, ok := view.Lookup("patch.apply"); ok {
		t.Error("ArtifactWriterView must exclude other workspace-write tools")
	}
	if _, ok := view.Lookup("shell.run"); ok {
		t.Error("ArtifactWriterView must exclude command tools")
	}
	if _, ok := view.Lookup("fetch"); ok {
		t.Error("ArtifactWriterView must exclude network tools")
	}
	if got := len(view.List()); got != 2 {
		t.Fatalf("view.List() has %d tools, want 2", got)
	}
	if got := len(src.List()); got != 5 {
		t.Fatalf("source registry mutated: %d tools, want 5", got)
	}
}

func TestArtifactWriterPatchGuardEnforcesAliasPrefix(t *testing.T) {
	src := New()
	if err := src.Register(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.write_patch): %v", err)
	}

	view := ArtifactWriterView(src, "@run")
	tool, ok := view.Lookup("file.write_patch")
	if !ok {
		t.Fatal("file.write_patch missing from artifact-writer view")
	}

	allowed := []string{
		`{"patch":"File: @run/task-1.md\n<<<<<<< SEARCH\n=======\nhello\n>>>>>>> REPLACE"}`,
		`{"patch":"File: @run/task-1.md\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
	}
	for _, args := range allowed {
		if _, err := tool.Handler(context.Background(), ToolCall{Name: "file.write_patch", Args: []byte(args)}); err != nil {
			t.Errorf("patch %q should be allowed under alias prefix, got error: %v", args, err)
		}
	}

	rejected := []string{
		`{"patch":"internal/foo.go"}`,
		`{"patch":"File: internal/foo.go\n<<<<<<< SEARCH\n=======\nhello\n>>>>>>> REPLACE"}`,
		// A source patch that merely contains the alias substring anywhere
		// (here in a comment) must still be rejected: the guard parses the
		// target paths, not the blob text.
		`{"patch":"File: internal/foo.go\n<<<<<<< SEARCH\n// @run/ is not a target\n=======\n// changed\n>>>>>>> REPLACE"}`,
	}
	for _, args := range rejected {
		if _, err := tool.Handler(context.Background(), ToolCall{Name: "file.write_patch", Args: []byte(args)}); err == nil {
			t.Errorf("patch %q should be rejected (not under alias prefix), got no error", args)
		}
	}
}

func TestPlanWriterViewAllowsOnlyTheCandidatePlan(t *testing.T) {
	src := New()
	for _, tool := range []Tool{
		{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler},
		{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler},
		{Name: "shell.run", Description: "shell", Risk: RiskCommand, Handler: nopHandler},
		{Name: "question.ask", Description: "question", Risk: RiskReadOnly, Handler: nopHandler},
	} {
		if err := src.Register(tool); err != nil {
			t.Fatalf("Register(%s): %v", tool.Name, err)
		}
	}
	view := PlanWriterView(src, "@plan", []string{"@plan/feature.md"})

	if _, ok := view.Lookup("file.read"); !ok {
		t.Fatal("plan writer must retain file.read")
	}
	if _, ok := view.Lookup("file.write_patch"); !ok {
		t.Fatal("plan writer must retain filtered file.write_patch")
	}
	if _, ok := view.Lookup("shell.run"); ok {
		t.Fatal("plan writer must not expose shell.run")
	}
	if _, ok := view.Lookup("question.ask"); ok {
		t.Fatal("orphaned authoring child must not expose question.ask")
	}
}

func TestPlanWriterViewRejectsSourceAndSecondPlanWrites(t *testing.T) {
	src := New()
	if err := src.Register(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.write_patch): %v", err)
	}
	view := PlanWriterView(src, "@plan", []string{"@plan/feature.md"})

	for _, path := range []string{"internal/app/app.go", "@plan/other.md"} {
		_, err := view.Dispatch(context.Background(), ToolCall{
			Name: "file.write_patch",
			Args: json.RawMessage(`{"patch":"File: ` + path + `\n<<<<<<< SEARCH\n\n=======\nnew\n>>>>>>> REPLACE"}`),
		})
		if err == nil {
			t.Errorf("write to %q was accepted", path)
		}
	}
}

func TestFallbackWriterViewNarrowsFileWrite(t *testing.T) {
	src := New()
	if err := src.Register(Tool{Name: "file.write", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.write): %v", err)
	}
	view := FallbackWriterView(src, []string{"internal/foo"})

	if _, ok := view.Lookup("file.write"); !ok {
		t.Fatal("fallback view must retain file.write")
	}
	for _, args := range []string{
		`{"path":"internal/foo/x.go","content":"x"}`,
		`{"path":"internal/foo/sub/y.go","content":"y"}`,
		`{"path":"internal/foo","content":"z"}`,
	} {
		if _, err := view.Dispatch(context.Background(), ToolCall{Name: "file.write", Args: json.RawMessage(args)}); err != nil {
			t.Errorf("write to allowed path was rejected: %v\nargs: %s", err, args)
		}
	}
	for _, args := range []string{
		`{"path":"internal/other/y.go","content":"y"}`,
		`{"path":"cmd/server/main.go","content":"m"}`,
		// Path traversal must not bypass the declared scope.
		`{"path":"internal/foo/../other/z.go","content":"z"}`,
		`{"path":"./internal/foo/../other.go","content":"o"}`,
	} {
		if _, err := view.Dispatch(context.Background(), ToolCall{Name: "file.write", Args: json.RawMessage(args)}); err == nil {
			t.Errorf("write outside allowlist was accepted: %s", args)
		}
	}
}

func TestFallbackWriterViewNarrowsFileWritePatch(t *testing.T) {
	src := New()
	if err := src.Register(Tool{Name: "file.read", Description: "read", Risk: RiskReadOnly, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.read): %v", err)
	}
	if err := src.Register(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.write_patch): %v", err)
	}
	if err := src.Register(Tool{Name: "shell.run", Description: "shell", Risk: RiskCommand, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(shell.run): %v", err)
	}

	// marshal.agent scope is directory-prefix; the view must allow
	// descendants of any declared root.
	view := FallbackWriterView(src, []string{"internal/foo", "internal/foo/sub"})

	if _, ok := view.Lookup("file.read"); !ok {
		t.Error("fallback view must retain read-only tools")
	}
	if _, ok := view.Lookup("file.write_patch"); !ok {
		t.Error("fallback view must retain file.write_patch")
	}
	if _, ok := view.Lookup("shell.run"); !ok {
		t.Error("fallback view must retain shell.run (command execution is governed by sandbox/policy at a higher layer)")
	}

	allowed := []string{
		`{"patch":"File: internal/foo/x.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
		`{"patch":"File: internal/foo/sub/y.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
		`{"patch":"File: internal/foo\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
	}
	for _, args := range allowed {
		if _, err := view.Dispatch(context.Background(), ToolCall{Name: "file.write_patch", Args: json.RawMessage(args)}); err != nil {
			t.Errorf("write to allowed path was rejected: %v\nargs: %s", err, args)
		}
	}
	rejected := []string{
		`{"patch":"File: internal/other/y.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
		`{"patch":"File: cmd/server/main.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
		// Path traversal must not bypass the declared scope.
		`{"patch":"File: internal/foo/../other/z.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
		`{"patch":"File: internal/foo/sub/../../bar.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
		`{"patch":"File: ./internal/foo/../other.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`,
	}
	for _, args := range rejected {
		if _, err := view.Dispatch(context.Background(), ToolCall{Name: "file.write_patch", Args: json.RawMessage(args)}); err == nil {
			t.Errorf("write outside allowlist was accepted: %s", args)
		}
	}
}

// TestFallbackWriterViewRejectsPathTraversal also guards the exact-match
// plan writer and prefix artifact writer via the same cleaning helper.
func TestScopeViewsRejectPathTraversal(t *testing.T) {
	src := New()
	if err := src.Register(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.write_patch): %v", err)
	}

	for _, tc := range []struct {
		name  string
		view  *Registry
		path  string
		allow bool
	}{
		{"plan writer exact match", PlanWriterView(src, "@plan", []string{"@plan/feature.md"}), "@plan/feature.md", true},
		{"plan writer traversal", PlanWriterView(src, "@plan", []string{"@plan/feature.md"}), "@plan/../feature.md", false},
		{"artifact writer prefix", ArtifactWriterView(src, "@run"), "@run/task-1.md", true},
		{"artifact writer traversal", ArtifactWriterView(src, "@run"), "@run/../app.go", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.view.Dispatch(context.Background(), ToolCall{
				Name: "file.write_patch",
				Args: json.RawMessage(`{"patch":"File: ` + tc.path + `\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`),
			})
			if tc.allow && err != nil {
				t.Fatalf("expected %q to be allowed, got %v", tc.path, err)
			}
			if !tc.allow && err == nil {
				t.Fatalf("expected %q to be rejected", tc.path)
			}
		})
	}
}

// TestFallbackWriterViewRejectsEmptyAllowlist ensures an empty scope
// fails closed: with no allowed paths the view must reject every
// file.write_patch call so a controller bug that calls into the view
// without setting the scope cannot leak a full registry.
func TestFallbackWriterViewRejectsEmptyAllowlist(t *testing.T) {
	src := New()
	if err := src.Register(Tool{Name: "file.write_patch", Description: "write", Risk: RiskWorkspaceWrite, Handler: nopHandler}); err != nil {
		t.Fatalf("Register(file.write_patch): %v", err)
	}
	view := FallbackWriterView(src, nil)
	if _, err := view.Dispatch(context.Background(), ToolCall{
		Name: "file.write_patch",
		Args: json.RawMessage(`{"patch":"File: anything.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE"}`),
	}); err == nil {
		t.Fatal("empty allowlist must reject every write; got no error")
	}
}

func TestPathInAllowlistRootSlash(t *testing.T) {
	// When root is "/", the joined path should normalize to "/" not "//".
	allowed := map[string]bool{"/": true}
	if !pathInAllowlist("/foo/bar", allowed) {
		t.Error("pathInAllowlist with root=/ should match /foo/bar")
	}
}

func TestFallbackWriterFileWriteToolLogsUnmarshalError(t *testing.T) {
	// Capture slog output via a text handler writing to a buffer.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(slog.Default()) })

	allowed := map[string]bool{"/workspace": true}
	tool := Tool{
		Name: "file.write",
		Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
			return ToolResult{Summary: "ok"}, nil
		},
	}

	wrapped := fallbackWriterFileWriteTool(tool, allowed)
	// Pass invalid JSON — the unmarshal should fail and log a warning.
	_, err := wrapped.Handler(context.Background(), ToolCall{Args: []byte("{invalid json")})
	// The handler still proceeds (path is empty → pathInAllowlist returns false → error).
	if err == nil {
		t.Fatal("expected error for invalid JSON with empty path")
	}
	logOutput := buf.String()
	if !strings.Contains(logOutput, "failed to parse") || !strings.Contains(logOutput, "file.write") {
		t.Errorf("expected slog warning about JSON parse failure for file.write, got: %s", logOutput)
	}
}
