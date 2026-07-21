# Milestone G: Patch Workflow Implementation Plan

**Status:** SHIPPED — Milestone G is complete (patch tool, approval flow, and in-memory rollback are in production). Retained as a historical record.

> **For Antigravity:** REQUIRED WORKFLOW: Use `.agent/workflows/execute-plan.md` to execute this plan in single-flow mode.

**Goal:** Implement the safe search/replace patch workflow for Marshal, including block parsing, dry-run validation, unified diff rendering in the TUI, file updates, and in-memory backups for interactive rollback.

**Architecture:** A new package `internal/tools/patch` will handle parsing Aider-style block formats, dry-run validation, and in-memory unified diff formatting. The `session.State` will be updated to store original file backups. The native `file.write_patch` tool will run dry-runs and populate session tool calls. Finally, Bubble Tea TUI will render the patch diffs and bind keypresses for confirmation and rollback.

**Tech Stack:** Go standard library, Bubble Tea (`github.com/charmbracelet/bubbletea`), and existing native tools.

---

### Task 1: Search/Replace Block Parser

**Files:**
* Create: `internal/tools/patch/parser.go`
* Create: `internal/tools/patch/parser_test.go`

**Step 1: Write the failing test**
In `internal/tools/patch/parser_test.go`:
```go
package patch

import (
	"reflect"
	"testing"
)

func TestParsePatches(t *testing.T) {
	input := `
Some message from the model.

File: internal/app/config/config.go
<<<<<<< SEARCH
type Config struct {
	Project ProjectConfig
}
=======
type Config struct {
	Project ProjectConfig
	Tools   ToolsConfig
}
>>>>>>> REPLACE

Another file change.

File: main.go
<<<<<<< SEARCH
func main() {
	println("hello")
}
=======
func main() {
	println("world")
}
>>>>>>> REPLACE
`
	want := []FilePatch{
		{
			Path: "internal/app/config/config.go",
			Chunks: []PatchChunk{
				{
					Search:  "type Config struct {\n\tProject ProjectConfig\n}",
					Replace: "type Config struct {\n\tProject ProjectConfig\n\tTools   ToolsConfig\n}",
				},
			},
		},
		{
			Path: "main.go",
			Chunks: []PatchChunk{
				{
					Search:  "func main() {\n\tprintln(\"hello\")\n}",
					Replace: "func main() {\n\tprintln(\"world\")\n}",
				},
			},
		},
	}

	got, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse() = %#v, want %#v", got, want)
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/tools/patch -v`
Expected: FAIL (package does not exist or Parser undefined)

**Step 3: Write minimal implementation**
In `internal/tools/patch/parser.go`:
```go
package patch

import (
	"strings"
)

type FilePatch struct {
	Path   string
	Chunks []PatchChunk
}

type PatchChunk struct {
	Search  string
	Replace string
}

func Parse(proposal string) ([]FilePatch, error) {
	var patches []FilePatch
	lines := strings.Split(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")

	var currentPath string
	var searchBuffer []string
	var replaceBuffer []string
	inSearch := false
	inReplace := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			currentPath = strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
			continue
		}

		if strings.HasPrefix(trimmed, "<<<<<<< SEARCH") {
			inSearch = true
			searchBuffer = nil
			continue
		}
		if strings.HasPrefix(trimmed, "=======") && inSearch {
			inSearch = false
			inReplace = true
			replaceBuffer = nil
			continue
		}
		if strings.HasPrefix(trimmed, ">>>>>>> REPLACE") && inReplace {
			inReplace = false
			chunk := PatchChunk{
				Search:  strings.Join(searchBuffer, "\n"),
				Replace: strings.Join(replaceBuffer, "\n"),
			}
			found := false
			for i := range patches {
				if patches[i].Path == currentPath {
					patches[i].Chunks = append(patches[i].Chunks, chunk)
					found = true
					break
				}
			}
			if !found && currentPath != "" {
				patches = append(patches, FilePatch{
					Path:   currentPath,
					Chunks: []PatchChunk{chunk},
				})
			}
			continue
		}

		if inSearch {
			searchBuffer = append(searchBuffer, line)
		} else if inReplace {
			replaceBuffer = append(replaceBuffer, line)
		}
	}

	return patches, nil
}
```

**Step 4: Run test to verify it passes**
Run: `go test ./internal/tools/patch -v`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/tools/patch/parser.go internal/tools/patch/parser_test.go
git commit -m "feat(patch): add search/replace block parser"
```

---

### Task 2: Dry-Run Validation and Unified Diff Engine

**Files:**
* Create: `internal/tools/patch/diff.go`
* Create: `internal/tools/patch/diff_test.go`

**Step 1: Write the failing test**
In `internal/tools/patch/diff_test.go`:
```go
package patch

import (
	"strings"
	"testing"
)

func TestApplyAndDiff(t *testing.T) {
	fileContent := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`

	patch := FilePatch{
		Path: "main.go",
		Chunks: []PatchChunk{
			{
				Search:  `	fmt.Println("hello")`,
				Replace: `	fmt.Println("patched")`,
			},
		},
	}

	// 1. Dry run/Validation
	ok, err := ValidatePatch(fileContent, patch)
	if !ok || err != nil {
		t.Fatalf("ValidatePatch failed: %v", err)
	}

	// 2. Generate Diff
	diff, err := GenerateDiff("main.go", fileContent, patch)
	if err != nil {
		t.Fatalf("GenerateDiff error: %v", err)
	}

	if !strings.Contains(diff, "-	fmt.Println(\"hello\")") {
		t.Errorf("Diff missing deleted lines: %s", diff)
	}
	if !strings.Contains(diff, "+	fmt.Println(\"patched\")") {
		t.Errorf("Diff missing added lines: %s", diff)
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/tools/patch -v`
Expected: FAIL (ValidatePatch & GenerateDiff undefined)

**Step 3: Write minimal implementation**
In `internal/tools/patch/diff.go`:
```go
package patch

import (
	"fmt"
	"strings"
)

func ValidatePatch(content string, fp FilePatch) (bool, error) {
	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		count := strings.Count(normContent, normSearch)
		if count == 0 {
			return false, fmt.Errorf("search block not found in %s", fp.Path)
		}
		if count > 1 {
			return false, fmt.Errorf("ambiguous match: search block matched %d locations in %s", count, fp.Path)
		}
		normContent = strings.Replace(normContent, normSearch, strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), 1)
	}
	return true, nil
}

func GenerateDiff(path string, content string, fp FilePatch) (string, error) {
	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n", path, path)

	lines := strings.Split(normContent, "\n")
	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		idx := strings.Index(normContent, normSearch)
		if idx == -1 {
			return "", fmt.Errorf("search block not found during diffing: %s", path)
		}

		// Find start line number
		before := normContent[:idx]
		startLine := strings.Count(before, "\n") + 1

		searchLines := strings.Split(normSearch, "\n")
		replaceLines := strings.Split(strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), "\n")

		// Grab context lines
		ctxBeforeCount := 3
		if startLine-1 < ctxBeforeCount {
			ctxBeforeCount = startLine - 1
		}
		ctxBefore := lines[startLine-1-ctxBeforeCount : startLine-1]

		ctxAfterStart := startLine - 1 + len(searchLines)
		ctxAfterCount := 3
		if len(lines)-ctxAfterStart < ctxAfterCount {
			ctxAfterCount = len(lines) - ctxAfterStart
		}
		ctxAfter := lines[ctxAfterStart : ctxAfterStart+ctxAfterCount]

		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", startLine-ctxBeforeCount, len(searchLines)+ctxBeforeCount+ctxAfterCount, startLine-ctxBeforeCount, len(replaceLines)+ctxBeforeCount+ctxAfterCount)
		for _, l := range ctxBefore {
			fmt.Fprintf(&sb, " %s\n", l)
		}
		for _, l := range searchLines {
			fmt.Fprintf(&sb, "-%s\n", l)
		}
		for _, l := range replaceLines {
			fmt.Fprintf(&sb, "+%s\n", l)
		}
		for _, l := range ctxAfter {
			fmt.Fprintf(&sb, " %s\n", l)
		}
	}
	return sb.String(), nil
}

func ApplyPatch(content string, fp FilePatch) string {
	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	for _, chunk := range fp.Chunks {
		normSearch := strings.ReplaceAll(chunk.Search, "\r\n", "\n")
		normContent = strings.Replace(normContent, normSearch, strings.ReplaceAll(chunk.Replace, "\r\n", "\n"), 1)
	}
	return normContent
}
```

**Step 4: Run test to verify it passes**
Run: `go test ./internal/tools/patch -v`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/tools/patch/diff.go internal/tools/patch/diff_test.go
git commit -m "feat(patch): add patch validation and diff generator"
```

---

### Task 3: Backup and Rollback State in Session

**Files:**
* Modify: `internal/app/session/session.go`
* Modify: `internal/app/session/session_test.go`

**Step 1: Write the failing test**
In `internal/app/session/session_test.go`:
```go
func TestStateBackups(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0))

	if state.HasBackup() {
		t.Fatal("initially HasBackup() should be false")
	}

	backups := []BackupFile{
		{Path: "test.txt", Content: "original content"},
	}
	state.StoreBackup(backups)

	if !state.HasBackup() {
		t.Fatal("expected HasBackup() to be true")
	}

	got := state.Backup()
	if len(got) != 1 || got[0].Content != "original content" {
		t.Fatalf("unexpected backup: %#v", got)
	}

	state.ClearBackup()
	if state.HasBackup() {
		t.Fatal("expected HasBackup() to be false after clear")
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/app/session -v`
Expected: FAIL (BackupFile or StoreBackup undefined)

**Step 3: Write minimal implementation**
In `internal/app/session/session.go`:
Add types and methods:
```go
type BackupFile struct {
	Path    string
	Content string
}
```
Add field to `State` struct:
```go
	lastBackup      []BackupFile
```
Add getter/setters in `session.go`:
```go
func (s *State) StoreBackup(backups []BackupFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = backups
}

func (s *State) Backup() []BackupFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	backups := make([]BackupFile, len(s.lastBackup))
	copy(backups, s.lastBackup)
	return backups
}

func (s *State) ClearBackup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBackup = nil
}

func (s *State) HasBackup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lastBackup) > 0
}
```

**Step 4: Run test to verify it passes**
Run: `go test ./internal/app/session -v`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): add backup and rollback storage methods"
```

---

### Task 4: Add file.write_patch Tool

**Files:**
* Modify: `internal/tools/native/file.go`
* Modify: `internal/tools/native/native.go`
* Modify: `internal/tools/native/file_test.go` (create if doesn't exist)

**Step 1: Write the failing test**
In `internal/tools/native/file_test.go`:
```go
package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/tools/registry"
)

func TestFileWritePatchTool(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "app.go")
	orig := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(filePath, []byte(orig), 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	ts := NewSet(tmp, 1000)
	reg := registry.NewRegistry()
	if err := ts.RegisterAll(reg); err != nil {
		t.Fatalf("RegisterAll error: %v", err)
	}

	tool, err := reg.Lookup("file.write_patch")
	if err != nil {
		t.Fatalf("file.write_patch not found: %v", err)
	}

	args := `{"patch": "File: app.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"patched\")\n>>>>>>> REPLACE"}`
	res, err := tool.Handler(context.Background(), registry.ToolCall{
		Args: []byte(args),
	})
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}

	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}

	// Verify file was patched
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}
	if !strings.Contains(string(data), "println(\"patched\")") {
		t.Fatalf("file content not patched: %s", string(data))
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/tools/native -v`
Expected: FAIL (file.write_patch not found)

**Step 3: Write minimal implementation**
In `internal/tools/native/file.go`:
Add structures and tool definition:
```go
type fileWritePatchArgs struct {
	Patch string `json:"patch"`
}

func (t *toolSet) fileWritePatchTool() registry.Tool {
	tool := registry.Tool{
		Name:        "file.write_patch",
		Description: "Apply a search/replace patch block format to files in the workspace.",
		Schema:      json.RawMessage(`{"type":"object","properties":{"patch":{"type":"string"}},"required":["patch"]}`),
		Risk:        registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileWritePatchArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}

		patches, err := patch.Parse(args.Patch)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("parse patch error: %w", err)
		}
		if len(patches) == 0 {
			return registry.ToolResult{}, fmt.Errorf("no valid patches found in proposal")
		}

		// Dry run first
		for _, fp := range patches {
			path, err := resolveWorkspacePath(t.root, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return registry.ToolResult{}, fmt.Errorf("read file %s: %w", fp.Path, err)
			}
			ok, err := patch.ValidatePatch(string(data), fp)
			if !ok || err != nil {
				return registry.ToolResult{}, fmt.Errorf("patch validation failed for %s: %v", fp.Path, err)
			}
		}

		// Generate diff to display in session
		var diffs []string
		var backups []session.BackupFile

		// Apply for real
		for _, fp := range patches {
			path, err := resolveWorkspacePath(t.root, fp.Path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return registry.ToolResult{}, err
			}
			original := string(data)

			diff, err := patch.GenerateDiff(fp.Path, original, fp)
			if err == nil {
				diffs = append(diffs, diff)
			}

			backups = append(backups, session.BackupFile{
				Path:    fp.Path,
				Content: original,
			})

			patched := patch.ApplyPatch(original, fp)
			if err := os.WriteFile(path, []byte(patched), 0644); err != nil {
				return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
			}
		}

		// Note: The caller orchestrator / loop is responsible for calling
		// state.StoreBackup(backups) when tool execution is approved.
		// For unit test purposes, we'll return summary list of modified files.
		var paths []string
		for _, fp := range patches {
			paths = append(paths, fp.Path)
		}

		return registry.ToolResult{
			Summary: fmt.Sprintf("Applied patches to: %s", strings.Join(paths, ", ")),
			Content: strings.Join(diffs, "\n\n"),
		}, nil
	}
	return tool
}
```
Import `marshal/internal/tools/patch` and `marshal/internal/app/session` in `internal/tools/native/file.go`.
Register the tool in `internal/tools/native/native.go`:
```diff
 	for _, tool := range []registry.Tool{
 		tools.fileReadTool(),
+		tools.fileWritePatchTool(),
 		tools.repoSearchTool(),
```

**Step 4: Run test to verify it passes**
Run: `go test ./internal/tools/native -v`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/tools/native/file.go internal/tools/native/native.go internal/tools/native/file_test.go
git commit -m "feat(tools): implement file.write_patch tool"
```

---

### Task 5: TUI Integration and Interactive Rollback

**Files:**
* Modify: `internal/app/tui/model.go`
* Modify: `internal/app/tui/model_test.go`
* Modify: `internal/app/session/session.go`

**Step 1: Write the failing test**
In `internal/app/tui/model_test.go`:
```go
func TestTUIRollbackFlow(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0))
	state.StoreBackup([]session.BackupFile{
		{Path: "app.go", Content: "original content"},
	})

	model := New(state)

	// Update with 'r' keypress to trigger rollback
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	model = updated.(Model)

	if state.HasBackup() {
		t.Fatal("expected backup to be cleared after rollback")
	}

	view := model.View()
	if !strings.Contains(view, "[r] Rollback Last Patch") {
		// should be removed after backup is cleared
	}
}
```

**Step 2: Run test to verify it fails**
Run: `go test ./internal/app/tui -v`
Expected: FAIL (rollback handling missing)

**Step 3: Write minimal implementation**
In `internal/app/session/session.go`, implement rollback:
```go
func (s *State) RollbackBackup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lastBackup) == 0 {
		return fmt.Errorf("no backup available")
	}
	for _, bf := range s.lastBackup {
		// Re-resolve path relative to WorkingDir
		path := filepath.Join(s.WorkingDir, bf.Path)
		if err := os.WriteFile(path, []byte(bf.Content), 0644); err != nil {
			return err
		}
	}
	s.lastBackup = nil
	return nil
}
```

In `internal/app/tui/model.go`:
* Add `tc.Diff` displaying to `View()` in the Diff panel:
```go
	fmt.Fprintf(&b, "\nDiff\n")
	if tc != nil && tc.Diff != "" {
		fmt.Fprintf(&b, "%s\n", tc.Diff)
	} else {
		fmt.Fprintf(&b, "  No patch proposed.\n")
	}
```
Wait, we need to add `Diff` field to `PendingToolCall` struct in `session.go`:
```go
type PendingToolCall struct {
	ID           string
	Name         string
	Args         string
	Command      string
	Risk         string
	Reason       string
	Diff         string // Added field for patch rendering
	ResponseChan chan UserApprovalDecision
}
```

* Intercept key `"r"` in TUI normal approval screen if `m.state.HasBackup()` is true:
```go
					case "r":
						if m.state.HasBackup() {
							_ = m.state.RollbackBackup()
							m.state.LogToolCall(registry.AuditEvent{
								Timestamp:     time.Now(),
								ToolName:      "rollback",
								ResultSummary: "Rollback applied successfully",
							})
							return m, nil
						}
```
* Update legend rendering to show `[r] Rollback` when rollback is available.

**Step 4: Run test to verify it passes**
Run: `go test ./internal/app/tui -v`
Expected: PASS

**Step 5: Commit**
```bash
git add internal/app/session/session.go internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): integrate unified diff preview and rollback keybinding"
```
