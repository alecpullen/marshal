# Partial Patch Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When a model emits a multi-file search/replace patch where one block is truncated (unclosed SEARCH/REPLACE), apply the well-formed blocks and return a structured, recoverable error naming the broken block and its captured buffers — instead of discarding the entire proposal.

**Architecture:** Change `patch.Parse` to a `ParsePartial` model: it returns every fully-closed `FilePatch` it collected, plus an optional `PartialError` struct describing the first unclosed block (path, captured search/replace buffers, block index). Keep the old `Parse` as a strict wrapper (used by read-only callers — policy, preview, runner's changed-file list) that returns `nil, err` on any partial, preserving their existing behavior. The `file.write_patch` handler switches to `ParsePartial`: when partials exist it applies only the well-formed patches, returns a result listing applied paths plus a `Content` section echoing the truncated block and instructing the model to re-send only that block. Add a complementary prompt nudge in `baseOutputFormat` about splitting large multi-part patches across calls.

**Tech Stack:** Go 1.x, `marshal/internal/tools/patch` (parser), `marshal/internal/tools/native` (file tool handler), `marshal/internal/agent` (prompts, patch preview, runner changed-files helper), `marshal/internal/tools/policy` (subject extraction). Tests via `go test ./...`; formatting via `gofmt -w .`.

## Global Constraints

- No new external dependencies; all changes use the standard library only.
- Preserve existing strict behavior for read-only `Parse` callers (policy `subjectsForTool`, `patch_preview.go`, `runner.changedFilesForTool`) — they must continue to reject partials so a truncated patch never silently bypasses permission/preview.
- `ParsePartial` must not panic on empty input or input with only a `File:` header and no block.
- Error messages returned to the model must be deterministic and contain no file *content* beyond the model's own search/replace buffers (those came from the model, so echoing them back is safe; never echo on-disk file contents).
- Commit messages follow the repo style: `<type>(<scope>): <subject>` (see `git log --oneline`), e.g. `feat(patch): ...`, `test(patch): ...`.
- Run `gofmt -w .` before each commit; run `go vet ./...` is not required per task but the final task runs the full suite.
- Every code step ships with its test; TDD ordering (test fails → implement → test passes → commit).

---

### Task 1: Add `PartialError` type and `ParsePartial` to the patch package

**Files:**
- Create: `internal/tools/patch/partial.go`
- Create: `internal/tools/patch/partial_test.go`

**Interfaces:**
- Consumes: nothing (new foundation).
- Produces:
  - `type PartialError struct { Path string; BlockIndex int; SearchBuffer string; ReplaceBuffer string; Stage string }` where `Stage` is `"search"` or `"replace"`.
  - `func (e *PartialError) Error() string`
  - `func ParsePartial(proposal string) ([]FilePatch, *PartialError)` — returns all fully-closed `FilePatch`s collected, and a non-nil `*PartialError` if the proposal ended (or a new `File:`/`SEARCH` header appeared) mid-block. Both the patches slice and the error may be non-empty at once. The patches slice may be non-empty and the error nil (clean parse).

- [ ] **Step 1: Write the failing tests**

Create `internal/tools/patch/partial_test.go`:

```go
package patch

import (
	"reflect"
	"testing"
)

func TestParsePartialCleanProposal(t *testing.T) {
	input := "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n"
	got, perr := ParsePartial(input)
	if perr != nil {
		t.Fatalf("expected nil partial error, got %v", perr)
	}
	want := []FilePatch{
		{Path: "a.go", Chunks: []PatchChunk{{Search: "old", Replace: "new"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePartial = %#v, want %#v", got, want)
	}
}

func TestParsePartialUnclosedReplaceKeepsPriorClosedBlocks(t *testing.T) {
	input := "File: a.go\n<<<<<<< SEARCH\nold1\n=======\nnew1\n>>>>>>> REPLACE\n" +
		"File: b.go\n<<<<<<< SEARCH\nold2\n=======\nnew2\n"
	got, perr := ParsePartial(input)
	if len(got) != 1 {
		t.Fatalf("expected 1 fully-closed FilePatch, got %d: %#v", len(got), got)
	}
	if got[0].Path != "a.go" || len(got[0].Chunks) != 1 {
		t.Fatalf("first patch wrong: %#v", got[0])
	}
	if perr == nil {
		t.Fatal("expected partial error for unclosed REPLACE, got nil")
	}
	if perr.Path != "b.go" {
		t.Errorf("partial error Path = %q, want %q", perr.Path, "b.go")
	}
	if perr.Stage != "replace" {
		t.Errorf("partial error Stage = %q, want %q", perr.Stage, "replace")
	}
	if perr.SearchBuffer != "old2" {
		t.Errorf("partial error SearchBuffer = %q, want %q", perr.SearchBuffer, "old2")
	}
	if perr.ReplaceBuffer != "new2" {
		t.Errorf("partial error ReplaceBuffer = %q, want %q", perr.ReplaceBuffer, "new2")
	}
}

func TestParsePartialUnclosedSearch(t *testing.T) {
	input := "File: a.go\n<<<<<<< SEARCH\nold1\n=======\nnew1\n>>>>>>> REPLACE\n" +
		"File: b.go\n<<<<<<< SEARCH\nsearchonly\n"
	got, perr := ParsePartial(input)
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("expected a.go closed, got %#v", got)
	}
	if perr == nil || perr.Stage != "search" {
		t.Fatalf("expected search-stage partial, got %#v", perr)
	}
	if perr.SearchBuffer != "searchonly" {
		t.Errorf("SearchBuffer = %q, want %q", perr.SearchBuffer, "searchonly")
	}
	if perr.ReplaceBuffer != "" {
		t.Errorf("ReplaceBuffer = %q, want empty", perr.ReplaceBuffer)
	}
}

func TestParsePartialMultipleChunksOneFileThenTruncate(t *testing.T) {
	// Two closed chunks for the same file, then a third unclosed.
	input := "File: a.go\n<<<<<<< SEARCH\ns1\n=======\nr1\n>>>>>>> REPLACE\n" +
		"<<<<<<< SEARCH\ns2\n=======\nr2\n>>>>>>> REPLACE\n" +
		"<<<<<<< SEARCH\ns3\n=======\nr3\n"
	got, perr := ParsePartial(input)
	if len(got) != 1 || got[0].Path != "a.go" || len(got[0].Chunks) != 2 {
		t.Fatalf("expected a.go with 2 chunks, got %#v", got)
	}
	if perr == nil || perr.Path != "a.go" || perr.BlockIndex != 2 {
		t.Fatalf("expected a.go block index 2 partial, got %#v", perr)
	}
}

func TestParsePartialBlockIndexCountsPerFile(t *testing.T) {
	// a.go gets 1 chunk, b.go gets its first chunk truncated → block index 0 for b.go.
	input := "File: a.go\n<<<<<<< SEARCH\ns1\n=======\nr1\n>>>>>>> REPLACE\n" +
		"File: b.go\n<<<<<<< SEARCH\ns2\n=======\nr2\n"
	got, perr := ParsePartial(input)
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("expected a.go only, got %#v", got)
	}
	if perr == nil || perr.Path != "b.go" || perr.BlockIndex != 0 {
		t.Fatalf("expected b.go block index 0, got %#v", perr)
	}
}

func TestParsePartialEmptyInput(t *testing.T) {
	got, perr := ParsePartial("")
	if got != nil {
		t.Fatalf("expected nil patches, got %#v", got)
	}
	if perr != nil {
		t.Fatalf("expected nil partial error, got %v", perr)
	}
}

func TestPartialErrorMessageMentionsPathAndStage(t *testing.T) {
	e := &PartialError{Path: "x.go", Stage: "replace", BlockIndex: 1}
	msg := e.Error()
	if !strings.Contains(msg, "x.go") {
		t.Errorf("error %q missing path", msg)
	}
	if !strings.Contains(msg, "replace") {
		t.Errorf("error %q missing stage", msg)
	}
}
```

Add the `"strings"` import to the test file's import block since `TestPartialErrorMessageMentionsPathAndStage` uses `strings.Contains`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/patch/ -run TestParsePartial -run TestPartialError -v`
Expected: build failure / `undefined: ParsePartial`, `undefined: PartialError`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/tools/patch/partial.go`:

```go
package patch

import (
	"fmt"
	"strings"
)

// PartialError describes a search/replace block that was never closed
// (the proposal ended, or a new File: / SEARCH header appeared, while the
// block was still open). It is returned by ParsePartial alongside any
// fully-closed FilePatches so a caller can apply the valid blocks and ask
// the model to re-send only the truncated one.
type PartialError struct {
	// Path is the File: of the truncated block, or "" if none was seen.
	Path string
	// BlockIndex is the zero-based index of this block among all blocks
	// belonging to Path in the proposal (closed + this one).
	BlockIndex int
	// SearchBuffer is the SEARCH content captured before the block broke.
	SearchBuffer string
	// ReplaceBuffer is the REPLACE content captured ("" if the block
	// broke while still in SEARCH, before ======= appeared).
	ReplaceBuffer string
	// Stage is "search" if the block broke before =======, else "replace".
	Stage string
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("patch: unclosed %s block for %q (block %d)", e.Stage, e.Path, e.BlockIndex)
}

// ParsePartial parses a search/replace patch proposal and returns every
// fully-closed FilePatch it could collect. If the proposal ends (or a new
// File: / SEARCH header appears) while a block is still open, the returned
// *PartialError describes that block; the patches slice contains all
// blocks closed before the break. Both may be non-nil/non-empty at once.
// A clean proposal returns (patches, nil). Empty input returns (nil, nil).
func ParsePartial(proposal string) ([]FilePatch, *PartialError) {
	lines := strings.Split(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")

	var patches []FilePatch
	var currentPath string
	var searchBuffer []string
	var replaceBuffer []string
	var blockCountForPath int
	inSearch := false
	inReplace := false

	findFile := func(path string) int {
		for i := range patches {
			if patches[i].Path == path {
				return i
			}
		}
		return -1
	}

	commitChunk := func() {
		chunk := PatchChunk{
			Search:  strings.Join(searchBuffer, "\n"),
			Replace: strings.Join(replaceBuffer, "\n"),
		}
		if currentPath == "" {
			return
		}
		if idx := findFile(currentPath); idx >= 0 {
			patches[idx].Chunks = append(patches[idx].Chunks, chunk)
		} else {
			patches = append(patches, FilePatch{
				Path:   currentPath,
				Chunks: []PatchChunk{chunk},
			})
		}
	}

	// breakPartial returns a PartialError for the currently-open block,
	// or nil if no block is open. It captures the accumulated buffers.
	breakPartial := func() *PartialError {
		if !inSearch && !inReplace {
			return nil
		}
		stage := "search"
		var replaceBuf string
		if inReplace {
			stage = "replace"
			replaceBuf = strings.Join(replaceBuffer, "\n")
		}
		return &PartialError{
			Path:          currentPath,
			BlockIndex:    blockCountForPath,
			SearchBuffer:  strings.Join(searchBuffer, "\n"),
			ReplaceBuffer: replaceBuf,
			Stage:         stage,
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			if perr := breakPartial(); perr != nil {
				return patches, perr
			}
			currentPath = strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
			blockCountForPath = 0
			inSearch = false
			inReplace = false
			continue
		}
		if trimmed == "<<<<<<< SEARCH" {
			if perr := breakPartial(); perr != nil {
				return patches, perr
			}
			inSearch = true
			inReplace = false
			searchBuffer = nil
			replaceBuffer = nil
			continue
		}
		if trimmed == "=======" && inSearch {
			inSearch = false
			inReplace = true
			replaceBuffer = nil
			continue
		}
		if trimmed == ">>>>>>> REPLACE" && inReplace {
			inReplace = false
			inSearch = false
			commitChunk()
			blockCountForPath++
			searchBuffer = nil
			replaceBuffer = nil
			continue
		}

		if inSearch {
			searchBuffer = append(searchBuffer, line)
		} else if inReplace {
			replaceBuffer = append(replaceBuffer, line)
		}
	}
	if perr := breakPartial(); perr != nil {
		return patches, perr
	}
	return patches, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/patch/ -v`
Expected: all tests PASS (new `TestParsePartial*` and `TestPartialError*`, plus the existing `TestParse*` suite still green — `Parse` is unchanged in this task).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/patch/partial.go internal/tools/patch/partial_test.go
git add internal/tools/patch/partial.go internal/tools/patch/partial_test.go
git commit -m "feat(patch): add ParsePartial returning closed blocks plus a structured PartialError"
```

---

### Task 2: Make `Parse` a strict wrapper over `ParsePartial`

**Files:**
- Modify: `internal/tools/patch/parser.go`
- Modify: `internal/tools/patch/parser_test.go`

**Interfaces:**
- Consumes: `ParsePartial`, `PartialError` from Task 1.
- Produces: `func Parse(proposal string) ([]FilePatch, error)` whose behavior is unchanged for all existing callers — returns `(nil, error)` if any block is unclosed. The error message matches the prior `fmt.Errorf("patch: unclosed %s block for %q", ...)` shape so existing tests stay green.

- [ ] **Step 1: Add the regression test asserting strict Parse drops partials**

Append to `internal/tools/patch/parser_test.go`:

```go
func TestParsePartialDoesNotLeakIntoStrictParse(t *testing.T) {
	// A closed block followed by an unclosed one: strict Parse must
	// return nil patches (no partial application leaks).
	input := "File: a.go\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> REPLACE\n" +
		"File: b.go\n<<<<<<< SEARCH\nold2\n=======\nnew2\n"
	got, err := Parse(input)
	if err == nil {
		t.Fatal("expected error from strict Parse on unclosed block, got nil")
	}
	if got != nil {
		t.Fatalf("strict Parse must not return partial patches, got %#v", got)
	}
	if !strings.Contains(err.Error(), "unclosed") {
		t.Fatalf("error should mention unclosed block: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/patch/ -run TestParsePartialDoesNotLeakIntoStrictParse -v`
Expected: PASS (the current strict `Parse` already drops partials). This is a characterization test guarding the refactor — confirm it passes *before* the refactor, then again after.

- [ ] **Step 3: Refactor `Parse` to delegate to `ParsePartial`**

Replace the body of `func Parse(proposal string) ([]FilePatch, error)` in `internal/tools/patch/parser.go` with:

```go
func Parse(proposal string) ([]FilePatch, error) {
	patches, perr := ParsePartial(proposal)
	if perr != nil {
		return nil, perr
	}
	if len(patches) == 0 && strings.TrimSpace(proposal) != "" {
		// Proposal had content but produced no closed patches and no
		// partial error (e.g. only a File: header, or orphan lines).
		return nil, fmt.Errorf("patch: no valid patches found in proposal")
	}
	return patches, nil
}
```

Then delete the now-unused original line-by-line state machine in `Parse` (the `var currentPath` block through the final `flushChunk()` and `return patches, nil`). The `FilePatch` and `PatchChunk` types and the `PartialError`/`ParsePartial` symbols remain in `partial.go`. Keep `fmt` and `strings` imports.

Note: the legacy `TestParseRejectsEmptyPathChunk` expects `Parse` to error on a chunk with no `File:` header. `ParsePartial` silently drops no-path chunks (it returns them only if a `File:` was seen). The proposal `"<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>> REPLACE\n"` parses fully closed in `ParsePartial` (one chunk, path ""), `commitChunk` drops it, so `len(patches)==0` and `TrimSpace(proposal) != ""` → the new `Parse` returns the `"no valid patches found"` error. That satisfies the test (it checks `err != nil` only). Confirm by running the test.

- [ ] **Step 4: Run the full patch package suite**

Run: `go test ./internal/tools/patch/ -v`
Expected: all PASS — existing `TestParsePatches`, `TestParseRejectsUnclosedSearch`, `TestParseRejectsUnclosedReplace`, `TestParseRejectsEmptyPathChunk`, plus the new characterization test.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/patch/parser.go internal/tools/patch/parser_test.go
git add internal/tools/patch/parser.go internal/tools/patch/parser_test.go
git commit -m "refactor(patch): implement strict Parse as a wrapper over ParsePartial"
```

---

### Task 3: Apply valid blocks in `file.write_patch` and report the truncated one

**Files:**
- Modify: `internal/tools/native/file.go`
- Modify: `internal/tools/native/file_test.go`

**Interfaces:**
- Consumes: `patch.ParsePartial`, `patch.PartialError` from Task 1; the existing `fileWritePatchTool` handler shape.
- Produces: behavior change only — on a partial proposal, the handler applies the fully-closed `FilePatch`s and returns a `registry.ToolResult` whose `Content` includes: the applied paths' diff, a header `WARNING: 1 truncated block was skipped`, the `PartialError` path and block index, the captured SEARCH/REPLACE buffers fenced as code, and the directive `Re-send file.write_patch with only the block for <path>`. `Summary` becomes `Applied patches to: <applied paths> (1 block truncated in <path>)`. If zero patches are closed AND a partial exists, return an error (nothing applied) so the model still gets the structured message via `BuildToolErrorMessage` — but with the buffers echoed so it can resume.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/native/file_test.go` (near the existing `write_patch` tests, e.g. after the CRLF test). The test seeds two files, sends a patch where the second file's block is truncated, and asserts the first file was applied while the result content names the truncated block:

```go
func TestFileWritePatchAppliesValidBlocksAndReportsTruncated(t *testing.T) {
	reg, _, root := setupNativeRegistry(t)
	// Seed two files.
	mustWriteFile(t, root, "a.go", "package a\n\nvar X = 1\n")
	mustWriteFile(t, root, "b.go", "package b\n\nvar Y = 2\n")

	// Closed block for a.go, unclosed REPLACE for b.go.
	patchStr := "File: a.go\n" +
		"<<<<<<< SEARCH\nvar X = 1\n=======\nvar X = 2\n>>>>>>> REPLACE\n" +
		"File: b.go\n" +
		"<<<<<<< SEARCH\nvar Y = 2\n=======\nvar Y = 3\n"

	args, _ := json.Marshal(map[string]string{"patch": patchStr})
	res, err := invokeTool(t, reg, "file.write_patch", string(args))
	if err != nil {
		t.Fatalf("write_patch returned error (expected applied-with-partial result): %v", err)
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Fatalf("result content should mention truncated block:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "b.go") {
		t.Fatalf("result content should name the truncated file b.go:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "Re-send file.write_patch with only the block for b.go") {
		t.Fatalf("result should contain re-send directive:\n%s", res.Content)
	}
	// a.go must be applied.
	data, err := os.ReadFile(filepath.Join(root, "a.go"))
	if err != nil {
		t.Fatalf("read a.go: %v", err)
	}
	if !strings.Contains(string(data), "var X = 2") {
		t.Fatalf("a.go not applied: %s", string(data))
	}
	// b.go must be unchanged.
	dataB, err := os.ReadFile(filepath.Join(root, "b.go"))
	if err != nil {
		t.Fatalf("read b.go: %v", err)
	}
	if strings.Contains(string(dataB), "var Y = 3") {
		t.Fatalf("b.go should not have been applied: %s", string(dataB))
	}
}

func TestFileWritePatchAllTruncatedReturnsErrorWithBuffers(t *testing.T) {
	reg, _, root := setupNativeRegistry(t)
	mustWriteFile(t, root, "c.go", "package c\n\nvar Z = 9\n")
	// Entirely unclosed block, no closed blocks at all.
	patchStr := "File: c.go\n<<<<<<< SEARCH\nvar Z = 9\n=======\nvar Z = 10\n"

	args, _ := json.Marshal(map[string]string{"patch": patchStr})
	_, err := invokeToolExpectError(t, reg, "file.write_patch", string(args))
	if err == nil {
		t.Fatal("expected error when no blocks are closed")
	}
	if !strings.Contains(err.Error(), "truncated") && !strings.Contains(err.Error(), "unclosed") {
		t.Fatalf("error should describe the unclosed block: %v", err)
	}
	// c.go unchanged.
	data, _ := os.ReadFile(filepath.Join(root, "c.go"))
	if strings.Contains(string(data), "var Z = 10") {
		t.Fatalf("c.go should not have been applied: %s", string(data))
	}
}
```

Before writing, check the existing test file for the exact helper names. If `mustWriteFile` and `invokeToolExpectError` do not exist, use the existing helpers instead — `setupNativeRegistry`, `invokeTool`, and write files directly with `os.WriteFile`. Inspect `internal/tools/native/file_test.go` around the existing `TestFileWritePatch...` functions and mirror their setup exactly. Add any missing helper locally in the test file following the existing style; do not invent helpers that don't compile. Add imports (`encoding/json`, `os`, `path/filepath`, `strings`) only if not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tools/native/ -run TestFileWritePatchAppliesValidBlocksAndReportsTruncated -v`
Run: `go test ./internal/tools/native/ -run TestFileWritePatchAllTruncatedReturnsErrorWithBuffers -v`
Expected: both FAIL — the current handler returns `parse patch error: ...` and applies nothing.

- [ ] **Step 3: Implement partial handling in the handler**

In `internal/tools/native/file.go`, inside the `fileWritePatchTool` handler, replace the current parse-and-bail block:

```go
patches, err := patch.Parse(args.Patch)
if err != nil {
	return registry.ToolResult{}, fmt.Errorf("parse patch error: %w", err)
}
if len(patches) == 0 {
	return registry.ToolResult{}, fmt.Errorf("no valid patches found in proposal")
}
```

with:

```go
patches, perr := patch.ParsePartial(args.Patch)
if perr != nil && len(patches) == 0 {
	// Nothing closed — nothing to apply. Surface the truncated block
	// and its captured buffers so the model can resume.
	return registry.ToolResult{}, partialParseError(perr)
}
if len(patches) == 0 {
	return registry.ToolResult{}, fmt.Errorf("no valid patches found in proposal")
}
```

Leave the rest of the handler (dry-run validate loop, apply loop, diff/diagnostics) operating on `patches` unchanged. After the existing `content := strings.Join(diffs, "\n\n")` and diagnostics block, before the final `return`, insert the partial-report when `perr != nil`:

```go
if perr != nil {
	content += formatPartialReport(perr)
}
summary := fmt.Sprintf("Applied patches to: %s", strings.Join(paths, ", "))
if perr != nil {
	summary += fmt.Sprintf(" (1 block truncated in %s)", perr.Path)
}
```

and change the final return to use the local `summary` instead of the inline `fmt.Sprintf(...)`.

Add the two helper functions at the bottom of `file.go` (after `fileWritePatchTool`):

```go
func partialParseError(p *patch.PartialError) error {
	var b strings.Builder
	b.WriteString("patch: no closed blocks; ")
	b.WriteString(p.Error())
	b.WriteString("\n\nTruncated block captured:\n")
	b.WriteString("<<<<<<< SEARCH\n")
	b.WriteString(p.SearchBuffer)
	b.WriteString("\n=======\n")
	if p.Stage == "replace" {
		b.WriteString(p.ReplaceBuffer)
		b.WriteString("\n")
	}
	b.WriteString("(block not closed — no >>>>>>> REPLACE)\n\n")
	b.WriteString("Re-send file.write_patch with only the block for ")
	b.WriteString(p.Path)
	b.WriteString(", closing it with ======= and >>>>>>> REPLACE.")
	return fmt.Errorf("%s", b.String())
}

func formatPartialReport(p *patch.PartialError) string {
	var b strings.Builder
	b.WriteString("\n\nWARNING: 1 truncated block was skipped.\n")
	b.WriteString("File: ")
	b.WriteString(p.Path)
	b.WriteString(" (block ")
	b.WriteString(fmt.Sprintf("%d", p.BlockIndex))
	b.WriteString(", stage: ")
	b.WriteString(p.Stage)
	b.WriteString(")\n")
	b.WriteString("Truncated block captured:\n")
	b.WriteString("<<<<<<< SEARCH\n")
	b.WriteString(p.SearchBuffer)
	b.WriteString("\n=======\n")
	if p.Stage == "replace" {
		b.WriteString(p.ReplaceBuffer)
		b.WriteString("\n")
	}
	b.WriteString("(no closing >>>>>>> REPLACE)\n\n")
	b.WriteString("Re-send file.write_patch with only the block for ")
	b.WriteString(p.Path)
	b.WriteString(".")
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tools/native/ -run TestFileWritePatch -v`
Expected: the two new tests PASS, and existing `TestFileWritePatch*` tests (clean single-block, empty-SEARCH new-file, CRLF) still PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/native/file.go internal/tools/native/file_test.go
git add internal/tools/native/file.go internal/tools/native/file_test.go
git commit -m "feat(native): file.write_patch applies valid blocks and reports truncated ones"
```

---

### Task 4: Add prompt guidance for splitting large multi-part patches

**Files:**
- Modify: `internal/agent/prompts.go`
- Modify: `internal/agent/prompts_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: an updated `baseOutputFormat` trailing line and a new assertion in `prompts_test.go`.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/prompts_test.go` (mirror the existing `TestSystemPromptContains...` style):

```go
func TestSystemPromptAdvisesSplittingLargePatches(t *testing.T) {
	msg := BuildSystemPrompt(RoleImplementer, nil, nil, nil, false)
	if !strings.Contains(msg.Content, "one file per file.write_patch call when large") {
		t.Errorf("system prompt should advise splitting large patches across calls; got:\n%s", msg.Content)
	}
}
```

Confirm the import `"strings"` is present in the test file (it is, per existing `TestSystemPrompt...` tests).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSystemPromptAdvisesSplittingLargePatches -v`
Expected: FAIL — current `baseOutputFormat` does not contain that phrase.

- [ ] **Step 3: Update the prompt**

In `internal/agent/prompts.go`, change the final line of `baseOutputFormat`:

```go
For patch actions use search/replace blocks, one block per file. Do not use unified diff syntax.`
```

to:

```go
For patch actions use search/replace blocks, one block per file. Do not use unified diff syntax. Prefer one file per file.write_patch call when large; long multi-part proposals are more likely to be truncated mid-block, which skips the unclosed block.`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestSystemPrompt -v`
Expected: PASS — new test plus all existing `TestSystemPrompt...` tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/prompts.go internal/agent/prompts_test.go
git add internal/agent/prompts.go internal/agent/prompts_test.go
git commit -m "docs(agent): advise splitting large patches to avoid mid-block truncation"
```

---

### Task 5: Full-suite verification and documentation note

**Files:**
- No new source. Run the full suite and record a one-line note in the existing patch docs comment.

- [ ] **Step 1: Run the complete test suite**

Run: `go test ./...`
Expected: PASS across all packages. If any test outside the four touched packages breaks, it means a caller relied on `Parse` returning partials (it never did, since strict `Parse` already dropped them) or on a `file.write_patch` error shape — investigate and fix the caller, do not weaken the new behavior.

- [ ] **Step 2: Run gofmt + vet**

Run: `gofmt -w . && go vet ./...`
Expected: clean.

- [ ] **Step 3: Document the behavior in the diff_test.go commentary**

The existing comment at `internal/tools/patch/diff_test.go:127-134` describes the tab/space fix as the dominant real-world failure. Append a sibling paragraph documenting the unclosed-block partial-recovery fix so future readers understand both mitigations. After the closing of `TestValidatePatchToleratesTabVsSpaceIndentation`, insert a new test that documents (and guards) the partial path rather than prose-only:

Append to `internal/tools/patch/diff_test.go`:

```go
// TestParsePartialRecoversClosedBlocksOnTruncation documents the second
// dominant real-world patch failure: on long multi-part proposals a model
// can truncate mid-block (token budget / early stop), leaving a SEARCH or
// REPLACE block unclosed. Before the partial-recovery change, one unclosed
// block aborted the whole proposal — the model had to regenerate the
// entire multi-file patch, which truncated again (a negative feedback
// loop). ParsePartial now returns the closed blocks so file.write_patch
// can apply them and ask the model to re-send only the truncated block.
func TestParsePartialRecoversClosedBlocksOnTruncation(t *testing.T) {
	input := "File: a.go\n<<<<<<< SEARCH\ns1\n=======\nr1\n>>>>>>> REPLACE\n" +
		"File: b.go\n<<<<<<< SEARCH\ns2\n=======\nr2\n"
	got, perr := ParsePartial(input)
	if len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("closed block for a.go should survive: %#v", got)
	}
	if perr == nil || perr.Path != "b.go" {
		t.Fatalf("expected partial for b.go, got %#v", perr)
	}
}
```

- [ ] **Step 4: Run the new guard test**

Run: `go test ./internal/tools/patch/ -run TestParsePartialRecoversClosedBlocksOnTruncation -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tools/patch/diff_test.go
git add internal/tools/patch/diff_test.go
git commit -m "docs(patch): document partial-recovery for unclosed-block truncation"
```

---

## Self-Review

**1. Spec coverage.** The investigation identified three fix surfaces: (a) parser partial recovery → Task 1 (`ParsePartial`/`PartialError`) + Task 2 (strict `Parse` wrapper). (b) `file.write_patch` applies valid blocks and reports the truncated one → Task 3. (c) prompt guidance for splitting large patches → Task 4. Task 5 verifies and documents. Read-only callers (`policy.subjectsForTool`, `patch_preview.go`, `runner.changedFilesForTool`) all use strict `Parse`, which Task 2 preserves — they continue to reject partials, so a truncated patch never silently bypasses permission/preview. Covered.

**2. Placeholder scan.** No "TBD", "handle edge cases", or hand-wavy steps. The one place where the exact test helper is uncertain (`mustWriteFile`/`invokeToolExpectError` in Task 3) is addressed explicitly: the step instructs inspecting the existing test file and using existing helpers, adding only what compiles. Concrete code is given for every implementation step.

**3. Type consistency.** `PartialError` fields (`Path`, `BlockIndex`, `SearchBuffer`, `ReplaceBuffer`, `Stage`) are used identically in Task 1 (definition), Task 3 (`p.Path`, `p.BlockIndex`, `p.SearchBuffer`, `p.ReplaceBuffer`, `p.Stage`), and Task 5 (`perr.Path`). `ParsePartial` returns `([]FilePatch, *PartialError)` in Task 1 and is consumed with that exact signature in Task 2 and Task 3. `patch.ParsePartial` and `patch.PartialError` are the exported names used across packages. `partialParseError` and `formatPartialReport` are package-local to `native` and defined in Task 3. Consistent.