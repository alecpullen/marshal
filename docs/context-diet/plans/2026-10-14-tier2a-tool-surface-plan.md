# Context diet Tier 2a (tool surface) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shrink the tools offered to the model from 53 to about 25, and their schemas from about 31 KB to about 15 KB, keeping every capability that saw real use.

**Architecture:**
- A registry `Internal` flag hides runtime-only tools from the model.
- `file.edit` replaces `file.write_patch` on the shared apply path.
- Dead tools are deleted.
- The background-work, history and steering tools are merged into the `agent.*` and `transcript_read` families.
- Scratchpad is removed.
- Descriptions and the model roster are trimmed.
- The Tier 1 prompt-budget guard ceilings are lowered.

**Tech Stack:** Go with cgo, SQLite, Bubble Tea. Tests use the standard `go test`.

**Spec:** `docs/context-diet/tier2a-design.md`. It builds on `docs/context-diet/design.md` (Tier 1).

## Global Constraints

- **Base:** this plan lands after Tier 1 (`docs/context-diet/plans/2026-10-14-tier1-context-cuts-plan.md`) is merged.
  - Anchors are quoted code text, not line numbers. Find them with `grep -n`.
  - If an anchor has moved, apply the change at the anchor's new location.
  - If an anchor is gone, stop and report it. Don't improvise.
- Build and test with `CGO_ENABLED=1`. Run `gofmt -w` on changed Go files, and keep `go vet` clean per changed package.
- **Deletion rule:** when a task deletes a symbol, fix the compile errors only by deleting or redirecting the callers this plan names, or callers that exist only to serve the deleted symbol.
  - Never stub a deleted symbol back in.
  - If a caller outside that scope breaks, stop and report it.
- **Don't delete:**
  - the `scratchpad_entries` / `session_mail` DB tables, or the `[scratchpad]` config struct
  - the watch subsystem (`internal/watch`); ACP and the web UI use it
  - the patch parser package
  - the `file.write_patch` handler (it becomes internal)
  - the `git.*` tools
  - anything in Tier 2b's scope: LSP tools, `symbols.find`, `repo.search kind=`, `diagnostics.check`, post-edit diagnostics
- **Keep old tool names in display maps** (`internal/viewmodel/describe.go`, `internal/app/tui/toolnames.go`), so persisted transcripts still render. Add entries for new names beside them.
- Cache-stable prefix: `TestPromptPrefixStableAcrossSteps` and `TestCrossTurnStableRegionIsAppendOnly` must pass after every task.
- Known environmental failures on this machine are listed in the Tier 1 plan's Global Constraints. Confirm any other failure against a clean `git worktree add --detach` at the base commit.
- One commit per task, in `area: summary` style.

## Review Focus

1. **The user's existing `[[permissions.rules]] permission = 'file.write_patch' pattern = '**' action = 'allow'` must keep allowing `file.edit` without a prompt.** It's present in `~/.config/marshal/config.toml`. Task 2 adds `TestLegacyWritePatchRuleAppliesToFileEdit`.
2. **A `file.edit` whose `old_string` or `new_string` contains a line of `=======` or `>>>>>>> REPLACE` must apply exactly.** That was the corruption class. Task 2 adds `TestFileEditContentWithPatchMarkersAppliesExactly`.
3. **A pipeline plan-writer child must still be able to create its plan file.** It used to rely on an empty-SEARCH `file.write_patch`, which is no longer offered. Task 2 adds `TestPlanWriterViewAllowsScopedFileWrite`.
4. **A resumed session's transcript containing `file.write_patch`, `agent.output` or `parent.correct` calls must still render labels.** Tasks 2 and 5 keep the display-map entries; Task 9's full suite covers the transcript tests.
5. **`agent.await` with `peek: true` on a finished background job must return its exit status and output tail without blocking.** Task 4 adds `TestAgentAwaitPeekFinishedJob`.

---

### Task 1: `Internal` registry flag; `test.run` goes internal

**Files:**
- Modify: `internal/tools/registry/types.go` (the `Tool` struct, next to `Deferred bool`)
- Modify: `internal/agent/chat.go` (`buildToolDefinitions`)
- Modify: `internal/agent/prompts.go`:
  - the envelope `Available tools:` loop in `buildSystemPrompt`
  - `batchSafeReadOnlyNames`
  - `writeDeferredAnnouncement`'s caller
- Modify: `internal/tools/registry/view.go` (`TesterView` / `testerTestRunTool`)
- Modify: `internal/tools/native/command.go` (`testRunTool`: set `Internal: true`)
- Modify: `internal/app/prompt_budget_test.go` (from Tier 1: skip `Internal` tools when summing schema bytes)
- Test: `internal/agent/chat_internal_test.go` (new), `internal/tools/registry/view_test.go`

**Interfaces:**
- Produces: `registry.Tool.Internal bool`. An internal tool is dispatchable but is never in `Runner.buildToolDefinitions()`, the envelope tool list, `batchSafeReadOnlyNames`, or the deferred announcement. `TesterView` registers `test.run` with `Internal == false`.

- [ ] **Step 1: Write the failing tests.** Create `internal/agent/chat_internal_test.go`:

```go
package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"marshal/internal/app/config"
	"marshal/internal/tools/policy"
	"marshal/internal/tools/registry"
	"marshal/internal/agent/agenttest"
)

func TestInternalToolsAreNeverOfferedToTheModel(t *testing.T) {
	reg := registry.New()
	for _, tl := range []registry.Tool{
		{Name: "file.read", Description: "read", Risk: registry.RiskReadOnly, Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "hidden.tool", Description: "hidden", Risk: registry.RiskReadOnly, Internal: true, Schema: json.RawMessage(`{"type":"object"}`)},
	} {
		if err := reg.Register(tl); err != nil {
			t.Fatal(err)
		}
	}
	r := NewRunner(&agenttest.ScriptedProvider{}, reg, policy.NewEngine(&config.Config{}, nil), newTestState(t), "m")
	for _, d := range r.buildToolDefinitions() {
		if strings.Contains(d.Name, "hidden") {
			t.Fatalf("internal tool offered as a definition: %s", d.Name)
		}
	}
	sys := BuildSystemPromptWithSystem(SystemPromptOptions{Role: RoleGeneral, Tools: reg.List(), Mode: policy.ModeEdit})
	if strings.Contains(sys.Content, "hidden.tool") {
		t.Fatalf("internal tool listed in the envelope prompt:\n%s", sys.Content)
	}
	if _, ok := reg.Lookup("hidden.tool"); !ok {
		t.Fatal("internal tool must stay dispatchable")
	}
}
```

Add to `internal/tools/registry/view_test.go`:

```go
func TestTesterViewExposesInternalTestRun(t *testing.T) {
	src := New()
	_ = src.Register(Tool{Name: "test.run", Risk: RiskCommand, Internal: true, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil }})
	got, ok := TesterView(src).Lookup("test.run")
	if !ok || got.Internal {
		t.Fatalf("TesterView must register a non-internal test.run; ok=%v internal=%v", ok, got.Internal)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run TestInternalToolsAreNeverOfferedToTheModel -count=1; CGO_ENABLED=1 go test ./internal/tools/registry/ -run TestTesterViewExposesInternalTestRun -count=1`
Expected: compile error, `unknown field Internal`.

- [ ] **Step 3: Implement.**
  1. In `registry.Tool`, below `Deferred bool`, add:

```go
	// Internal tools are dispatchable by the runtime (envelope-mode patch
	// actions, role views that re-expose them) but are never offered to the
	// model: no tool definition, no prompt listing, no deferred announcement.
	Internal bool
```

  2. `internal/agent/chat.go` `buildToolDefinitions`: at the top of the `for _, tool := range tools` loop, add `if tool.Internal { continue }`.
  3. `internal/agent/prompts.go`:
     - In the envelope loop (`for _, tool := range tools {` under `b.WriteString("\n\nAvailable tools:\n")`), add `if tool.Internal { continue }` as the first statement.
     - In `batchSafeReadOnlyNames`, skip `tool.Internal`.
     - In the `notLoaded` loop that feeds `writeDeferredAnnouncement`, skip `tool.Internal`.
  4. `internal/tools/native/command.go` `testRunTool`: add `Internal: true,` to the `registry.Tool` literal, and this comment above it: `// Internal: the general agent runs tests through shell.run; only the swarm tester (TesterView) gets this constrained tool.`
  5. `internal/tools/registry/view.go` `testerTestRunTool`: on the returned copy, set `testerTool.Internal = false` before `return testerTool`.
  6. `internal/app/prompt_budget_test.go`: change `if tl.Deferred {` to `if tl.Deferred || tl.Internal {`.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ ./internal/tools/registry/ ./internal/tools/native/ -count=1 && CGO_ENABLED=1 go test ./internal/app/ -run TestDefaultPromptBudget -count=1 -v`
Expected: PASS. The budget log shows the tool schemas about 300 bytes smaller (`test.run` gone).

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal/tools internal/agent internal/app
git add -A internal/tools internal/agent internal/app/prompt_budget_test.go
git commit -m "registry: add Internal flag; stop offering test.run to the model"
```

---

### Task 2: `file.edit` replaces `file.write_patch` for the model

**Files:**
- Create: `internal/tools/patch/toolargs.go`, `internal/tools/patch/toolargs_test.go`
- Create: `internal/tools/native/file_edit.go`, `internal/tools/native/file_edit_test.go`
- Modify: `internal/tools/native/file.go`:
  - extract `applyFilePatches` from `fileWritePatchTool`'s handler
  - mark `file.write_patch` `Internal`
  - register `fileEditTool` in `native.go`'s `all` list
- Modify, using `patch.FromToolArgs`:
  - `internal/agent/runner.go` `changedFilesForTool`
  - `internal/agent/requests.go` (the approval diff preview)
  - `internal/agent/patch_preview.go` (add `PreviewFilePatches`)
  - `internal/agent/patch_hint.go` `failingPatchTarget`
  - `internal/agent/execute.go` (the failed-repeat hint condition)
  - `internal/tools/policy/policy.go` `subjectsForTool`
  - `internal/permissions/pattern.go` `PatternForApproval`
- Modify: `internal/permissions/rules.go` (`PermissionForTool`, `permissionMatches` legacy alias), `internal/hooks/runner.go` (`match` legacy alias)
- Modify, adding `"file.edit"` beside each `"file.write_patch"`:
  - `internal/agent/progress.go` `categorize`
  - `internal/agent/toolargs.go`
  - `internal/app/session/subagents.go`
  - `internal/viewmodel/stack.go`
  - `internal/acp/stacknode.go` (`isDiffToolName`)
  - `internal/viewmodel/describe.go` (`"file.edit": "Edit file"`)
  - `internal/app/tui/toolnames.go` (same label)
  - `internal/app/tui/symbolrow.go`
  - `internal/app/tui/transcript.go`
- Modify: `internal/tools/registry/view.go` (scoped `file.edit` / `file.write` in `FallbackWriterView`, `ArtifactWriterView`, `PlanWriterView`)
- Modify: `internal/agent/prompts.go` (role addenda, `ruleFileToolsWrite*`, `systemAccessDirective`, `nativePatchFormat` + `hasWritePatch` block, `ruleReplaceTerminator`)
- Modify: `internal/skills/builtin/postmortem.md` (`file.write_patch` → `file.edit`)
- Test: the new test files, `internal/permissions/rules_test.go`, `internal/tools/registry/view_test.go`, `internal/agent/prompts_test.go`

**Interfaces:**
- Produces:
  - `patch.FromToolArgs(toolName string, args map[string]any) ([]FilePatch, bool)`. It returns the patches for `file.write_patch` (parsed from `args["patch"]`) or `file.edit` (one chunk built from `path` / `old_string` / `new_string`), and `false` for any other tool or malformed args.
  - `(*toolSet).applyFilePatches(ctx context.Context, patches []patch.FilePatch, repairs []string) (registry.ToolResult, error)`
  - `agent.PreviewFilePatches(workspaceRoot string, patches []patch.FilePatch, system bool) (string, error)`
  - Tool `file.edit` with schema `{path, old_string, new_string}`, all required.

- [ ] **Step 1: Write `FromToolArgs` test-first.** Create `internal/tools/patch/toolargs_test.go`:

```go
package patch

import "testing"

func TestFromToolArgsFileEditKeepsMarkersVerbatim(t *testing.T) {
	old := "a\n=======\nb"
	repl := "c\n>>>>>>> REPLACE\nd"
	got, ok := FromToolArgs("file.edit", map[string]any{"path": "x.go", "old_string": old, "new_string": repl})
	if !ok || len(got) != 1 || got[0].Path != "x.go" || len(got[0].Chunks) != 1 {
		t.Fatalf("FromToolArgs = %+v, %v", got, ok)
	}
	if got[0].Chunks[0].Search != old || got[0].Chunks[0].Replace != repl {
		t.Fatalf("chunk altered: %+v", got[0].Chunks[0])
	}
}

func TestFromToolArgsWritePatchParses(t *testing.T) {
	got, ok := FromToolArgs("file.write_patch", map[string]any{"patch": "File: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE"})
	if !ok || len(got) != 1 || got[0].Path != "a.go" {
		t.Fatalf("FromToolArgs = %+v, %v", got, ok)
	}
}

func TestFromToolArgsRejectsOtherToolsAndMissingFields(t *testing.T) {
	if _, ok := FromToolArgs("shell.run", map[string]any{"command": "ls"}); ok {
		t.Fatal("shell.run must not yield patches")
	}
	if _, ok := FromToolArgs("file.edit", map[string]any{"path": "x.go"}); ok {
		t.Fatal("file.edit without old_string must not yield patches")
	}
}
```

Run `CGO_ENABLED=1 go test ./internal/tools/patch/ -run FromToolArgs -count=1`. Expected: FAIL (undefined). Then create `internal/tools/patch/toolargs.go`:

```go
package patch

// FromToolArgs returns the file patches a write tool call describes, so
// callers that need the target paths or edit chunks (approval preview,
// permission subjects, changed-files tracking, failure hints) can treat
// file.edit and file.write_patch alike. file.edit chunks are built from the
// structured fields directly — never round-tripped through patch text,
// whose delimiters a literal "=======" line in the content would corrupt.
func FromToolArgs(toolName string, args map[string]any) ([]FilePatch, bool) {
	switch toolName {
	case "file.edit":
		path, _ := args["path"].(string)
		old, _ := args["old_string"].(string)
		repl, _ := args["new_string"].(string)
		if path == "" || old == "" {
			return nil, false
		}
		return []FilePatch{{Path: path, Chunks: []PatchChunk{{Search: old, Replace: repl}}}}, true
	case "file.write_patch":
		text, _ := args["patch"].(string)
		patches, err := Parse(text)
		if err != nil || len(patches) == 0 {
			return nil, false
		}
		return patches, true
	}
	return nil, false
}
```

Run the tests again. Expected: PASS.

- [ ] **Step 2: Write the failing `file.edit` tests.** Create `internal/tools/native/file_edit_test.go`. It reuses the file-tracker setup pattern from `TestWritePatch_ChangedOnDiskErrorIncludesCurrentContent` in `file_test.go`:

```go
package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"marshal/internal/db"
	"marshal/internal/filetrack"
	"marshal/internal/tools/registry"
)

func newEditRegistry(t *testing.T, root string) *registry.Registry {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "ft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}, FileTracker: filetrack.New(database.SQLDB(), "s")}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestFileEditReplacesExactlyOnce(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nvar x = 1\n"), 0o644)
	reg := newEditRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.read", `{"path":"a.go"}`); err != nil {
		t.Fatal(err)
	}
	res, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"var x = 1","new_string":"var x = 2"}`)
	if err != nil {
		t.Fatalf("file.edit: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.go"))
	if string(got) != "package a\n\nvar x = 2\n" {
		t.Fatalf("content = %q", got)
	}
	if len(res.FilesChanged) != 1 || res.FilesChanged[0] != "a.go" || !strings.Contains(res.Content, "+var x = 2") {
		t.Fatalf("result = %+v", res)
	}
}

// Review focus 2.
func TestFileEditContentWithPatchMarkersAppliesExactly(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "doc.md"), []byte("intro\nold\n"), 0o644)
	reg := newEditRegistry(t, root)
	invokeTool(t, reg, "file.read", `{"path":"doc.md"}`)
	if _, err := invokeTool(t, reg, "file.edit", `{"path":"doc.md","old_string":"old","new_string":"<<<<<<< SEARCH\n=======\n>>>>>>> REPLACE"}`); err != nil {
		t.Fatalf("file.edit: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "doc.md"))
	if string(got) != "intro\n<<<<<<< SEARCH\n=======\n>>>>>>> REPLACE\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestFileEditRequiresReadAndNonEmptyOldString(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("x\n"), 0o644)
	reg := newEditRegistry(t, root)
	if _, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"x","new_string":"y"}`); err == nil || !strings.Contains(err.Error(), "never read") {
		t.Fatalf("unread edit error = %v, want never-read rejection", err)
	}
	invokeTool(t, reg, "file.read", `{"path":"a.go"}`)
	if _, err := invokeTool(t, reg, "file.edit", `{"path":"a.go","old_string":"","new_string":"y"}`); err == nil || !strings.Contains(err.Error(), "file.write") {
		t.Fatalf("empty old_string error = %v, want pointer to file.write", err)
	}
}

func TestFileWritePatchIsInternal(t *testing.T) {
	reg := newEditRegistry(t, t.TempDir())
	tool, ok := reg.Lookup("file.write_patch")
	if !ok || !tool.Internal {
		t.Fatalf("file.write_patch must stay registered as Internal; ok=%v", ok)
	}
}
```

Run: `CGO_ENABLED=1 go test ./internal/tools/native/ -run 'TestFileEdit|TestFileWritePatchIsInternal' -count=1`
Expected: FAIL (`Lookup("file.edit") ok=false`).

- [ ] **Step 3: Extract the shared apply path and add the tool.**
  1. In `internal/tools/native/file.go` `fileWritePatchTool`, move everything in the handler after `patches, patchRepairs := parsed.Patches, parsed.Repairs` / the `len(patches) == 0` check, through the final `return registry.ToolResult{…}`, into a new method on `toolSet`:

```go
// applyFilePatches is the single write path for file.write_patch and
// file.edit: stale-file and read-before-edit guards, validation, backups,
// diff, TOCTOU re-check, write, diagnostics. repairs are format repairs the
// patch parser made (always nil for file.edit).
func (t *toolSet) applyFilePatches(ctx context.Context, patches []patch.FilePatch, repairs []string) (registry.ToolResult, error) {
```

     The handler body becomes `return t.applyFilePatches(ctx, patches, patchRepairs)`. Inside the moved code, rename `patchRepairs` to `repairs`. Leave the logic byte-for-byte otherwise.
  2. In the `registry.Tool` literal in `fileWritePatchTool`, add `Internal: true,` and this comment: `// Internal: reached only by envelope-mode patch actions; the model edits through file.edit.`
  3. Create `internal/tools/native/file_edit.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"marshal/internal/tools/patch"
	"marshal/internal/tools/registry"
)

type fileEditArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func (t *toolSet) fileEditTool() registry.Tool {
	tool := registry.Tool{
		Name: "file.edit",
		Description: "Replace one exact occurrence of old_string with new_string in a file you have read this session. " +
			"old_string must match the file byte-for-byte (including indentation) and be unique; include surrounding lines to disambiguate. " +
			"Use file.write to create a file or replace most of it. Edit several files with several calls.",
		Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":` +
			t.pathDescription("file to edit, relative to the workspace") +
			`},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["path","old_string","new_string"],"additionalProperties":false}`),
		Risk: registry.RiskWorkspaceWrite,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		args, err := decodeArgs[fileEditArgs](tool, call.Args)
		if err != nil {
			return registry.ToolResult{}, err
		}
		if args.OldString == "" {
			return registry.ToolResult{}, errors.New("file.edit: old_string is required; use file.write to create or overwrite a whole file")
		}
		if args.OldString == args.NewString {
			return registry.ToolResult{}, errors.New("file.edit: old_string and new_string are identical; nothing to change")
		}
		res, err := t.applyFilePatches(ctx, []patch.FilePatch{{
			Path:   args.Path,
			Chunks: []patch.PatchChunk{{Search: args.OldString, Replace: args.NewString}},
		}}, nil)
		if err != nil {
			return registry.ToolResult{}, err
		}
		res.Summary = fmt.Sprintf("Edited %s", args.Path)
		return res, nil
	}
	return tool
}
```

  4. In `internal/tools/native/native.go`'s `all` list, add `tools.fileEditTool(),` directly after `tools.fileWritePatchTool(),`.

  Run the Step 2 tests. Expected: PASS.

- [ ] **Step 4: Route the name-keyed callers through `FromToolArgs`.**
  1. `internal/agent/runner.go` `changedFilesForTool`: replace everything from `if toolName != "file.write_patch" {` to the end of the function with:

```go
	patches, ok := patch.FromToolArgs(toolName, argsMap)
	if !ok {
		return nil
	}
	files := make([]string, 0, len(patches))
	for _, p := range patches {
		files = append(files, p.Path)
	}
	return files
```

  2. `internal/agent/patch_preview.go`:
     - Rename the body of `PreviewPatchDiff` after its `Parse` into `func PreviewFilePatches(workspaceRoot string, patches []patch.FilePatch, system bool) (string, error)`.
     - `PreviewPatchDiff` then parses and calls it.
     - In `internal/agent/requests.go`, replace the `if toolName == "file.write_patch" { … }` block with:

```go
	if patches, ok := patch.FromToolArgs(toolName, argsMap); ok {
		if preview, previewErr := PreviewFilePatches(r.State.WorkingDir, patches, r.State.SystemAccess()); previewErr == nil {
			diff = preview
		}
	}
```

  3. `internal/agent/execute.go`: change `if toolName == "file.write_patch" && tier >= failedRepeatInject {` to `if (toolName == "file.write_patch" || toolName == "file.edit") && tier >= failedRepeatInject {`. In `internal/agent/patch_hint.go` `failingPatchTarget`, where it parses the raw args into a patch, first unmarshal `args` into `map[string]any`, then call `patch.FromToolArgs` for the tool. To get the name, add a `toolName string` parameter to `failedPatchTargetHint` and `failingPatchTarget`, and pass `toolName` from `execute.go`. Use the first chunk's `Search` and the patch `Path`, as today.
  4. `internal/tools/policy/policy.go` `subjectsForTool`: replace the `case "file.write_patch":` body so it serves both tools:

```go
	case "file.write_patch", "file.edit":
		patches, ok := patch.FromToolArgs(toolName, args)
		if !ok {
			return nil
		}
		paths := make([]string, 0, len(patches))
		for _, p := range patches {
			paths = append(paths, p.Path)
		}
		return paths
```

     Keep whatever the old case returned for a parse failure. If it returned something other than `nil`, return that instead.
  5. `internal/permissions/pattern.go`: change `if tc.Name == "file.write_patch" {` to cover `file.edit`. Unmarshal `tc.Args` into `map[string]any`, then `patches, ok := patch.FromToolArgs(tc.Name, m)`, with `!ok` treated exactly like the old `err != nil || len(patches) == 0` branch. The `commonDir(patches)` logic is unchanged.
  6. `internal/permissions/rules.go`:
     - `PermissionForTool`: `case "file.write_patch", "file.edit": return "file.edit"`.
     - `permissionMatches`: add, after the shell alias:

```go
	// Legacy alias: rules written for the retired model-facing
	// file.write_patch tool keep governing its successor.
	if rulePermission == "file.write_patch" && toolPermission == "file.edit" {
		return true
	}
```

  7. `internal/hooks/runner.go` `match`: before `filepath.Match`, add `if pattern == "file.write_patch" && value == "file.edit" { return true }` with the same legacy-alias comment.
  8. Add `"file.edit"` beside `"file.write_patch"` in each of these:
     - `internal/agent/progress.go` `categorize` (`case "file.write_patch", "file.edit": return catPatch`)
     - `internal/agent/toolargs.go`: the `file.edit` case returns `m["path"]` as a string
     - `internal/app/session/subagents.go` (`case "file.write_patch", "file.edit":`)
     - `internal/viewmodel/stack.go` and `internal/acp/stacknode.go` (the diff-tool predicates)
     - the display maps `internal/viewmodel/describe.go` and `internal/app/tui/toolnames.go`: add `"file.edit": "Edit file"`
     - `internal/app/tui/symbolrow.go` and `internal/app/tui/transcript.go`: wherever they test `== "file.write_patch"`, use `== "file.write_patch" || == "file.edit"`

- [ ] **Step 5: Write the permission and scoped-view tests.** Add to `internal/permissions/rules_test.go`:

```go
// Review focus 1: the user's real config allows file.write_patch on **.
func TestLegacyWritePatchRuleAppliesToFileEdit(t *testing.T) {
	if !permissionMatches("file.write_patch", PermissionForTool("file.edit")) {
		t.Fatal("a file.write_patch rule must match file.edit calls")
	}
	if PermissionForTool("file.write_patch") != "file.edit" {
		t.Fatal("file.write_patch and file.edit must share one permission key")
	}
}
```

  Add to `internal/tools/registry/view_test.go`:

```go
// Review focus 3: the plan-writer child creates its plan with file.write.
func TestPlanWriterViewAllowsScopedFileWrite(t *testing.T) {
	src := New()
	ok := func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Summary: "ok"}, nil }
	_ = src.Register(Tool{Name: "file.write", Risk: RiskWorkspaceWrite, Handler: ok})
	_ = src.Register(Tool{Name: "file.edit", Risk: RiskWorkspaceWrite, Handler: ok})
	view := PlanWriterView(src, "@run", []string{"@run/plan.md"})
	for _, name := range []string{"file.write", "file.edit"} {
		tool, found := view.Lookup(name)
		if !found {
			t.Fatalf("%s missing from plan-writer view", name)
		}
		if _, err := tool.Handler(context.Background(), ToolCall{Name: name, Args: []byte(`{"path":"@run/plan.md"}`)}); err != nil {
			t.Fatalf("%s on the allowed plan path: %v", name, err)
		}
		if _, err := tool.Handler(context.Background(), ToolCall{Name: name, Args: []byte(`{"path":"internal/x.go"}`)}); err == nil {
			t.Fatalf("%s outside the plan path must be rejected", name)
		}
	}
}
```

  Run: `CGO_ENABLED=1 go test ./internal/permissions/ ./internal/tools/registry/ -run 'Legacy|PlanWriterViewAllowsScopedFileWrite' -count=1`
  Expected: the rules test passes after Step 4. The view test FAILS (`file.write missing`).

- [ ] **Step 6: Scope `file.write` and `file.edit` by path in the views.** In `internal/tools/registry/view.go`:
  1. Generalise `fallbackWriterFileWriteTool` into:

```go
// pathScopedTool narrows a path-addressed write tool (file.write, file.edit)
// to paths check accepts. check receives the cleaned path and returns a
// non-nil error to reject it.
func pathScopedTool(tool Tool, check func(cleaned, raw string) error) Tool {
	original := tool.Handler
	filtered := tool
	filtered.Handler = func(ctx context.Context, call ToolCall) (ToolResult, error) {
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Args, &args); err != nil {
			return ToolResult{}, fmt.Errorf("%s could not parse arguments: %w", tool.Name, err)
		}
		if err := check(cleanScopePath(args.Path), args.Path); err != nil {
			return ToolResult{}, err
		}
		return original(ctx, call)
	}
	return filtered
}
```

  2. In `FallbackWriterView`:
     - Replace `case tool.Name == "file.write":` with `case tool.Name == "file.write" || tool.Name == "file.edit":`, registering `pathScopedTool(tool, …)` with a check that returns `fmt.Errorf("%s in fallback scope may only write under the declared scope (path %q is outside)", tool.Name, raw)` when `!pathInAllowlist(cleaned, allowedSet)`.
     - Delete `fallbackWriterFileWriteTool`.
  3. In `ArtifactWriterView`, add `case tool.Name == "file.write" || tool.Name == "file.edit":`, registering `pathScopedTool` with a check rejecting `!strings.HasPrefix(cleaned, artifactAlias+"/")` using the message pattern of `artifactWriterPatchTool`.
  4. In `PlanWriterView`, add the same case with a check rejecting `!allowedSet[cleaned]`, using `planWriterPatchTool`'s message pattern.
  5. Keep the existing `file.write_patch` wrappers. The internal tool is still reachable by envelope-mode children.

  Run the Step 5 tests again. Expected: PASS.

- [ ] **Step 7: Update the prompt and the skill.** In `internal/agent/prompts.go`:
  - Role addenda: in `RoleImplementer`'s focus, `file.read, file.write_patch, and file.write` → `file.read, file.edit, and file.write`. In `RoleSubtask`'s focus, `file.write_patch, etc.` → `file.edit, etc.`.
  - `ruleFileToolsWrite` and `ruleFileToolsWriteSystem`: `file.write or file.write_patch` → `file.write or file.edit`. In `systemAccessDirective`, make the same replacement.
  - Delete the `nativePatchFormat` constant and the block in `buildSystemPrompt` that starts `hasWritePatch := false` and ends after `b.WriteString(nativePatchFormat)`.
  - Delete `ruleReplaceTerminator`. Make `ruleStopAfterValidation` the last rule in both compositions, with its trailing `\n` removed, so the block still ends without a newline.
  - In `prompts_test.go`, Tier 1's `TestBaseRulesCarryNoRepoSpecificWorkflow` asserts the REPLACE terminator is present. Change that assertion to `if strings.Contains(rules, ">>>>>>> REPLACE") { t.Errorf(...) }` with the message `base rules must not teach the retired patch format`.
  - In `internal/skills/builtin/postmortem.md`, replace `` `file.write_patch` `` with `` `file.edit` ``.
  - In `internal/tools/native/command.go`, in the `shell.run` description, change `use file.write or file.write_patch instead` to `use file.write or file.edit instead`.
  - In `internal/tools/native/file.go`, in the `file.write` description, change `Prefer this over file.write_patch when writing a whole new file or replacing most of a file's content; use file.write_patch for targeted edits.` to `Prefer this over file.edit when writing a whole new file or replacing most of a file's content; use file.edit for targeted edits.`

- [ ] **Step 8: Migrate the broken tests and run them.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/... 2>&1 | grep -E '^(FAIL|---)' | head -80`

For each failure, apply exactly one of these rules:
1. **A test asserts the model-facing tool list or prompt contains `file.write_patch`:** change it to `file.edit`.
2. **A test asserts the prompt contains the patch-format section or the REPLACE rule:** invert it to assert absence.
3. **A test dispatches `file.write_patch` through a registry:** leave it, since the tool is still registered as internal. If it fails, report it.
4. **Anything else:** stop and report it.

Re-run until clean, apart from the known environmental failures.

- [ ] **Step 9: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "tools: add file.edit, make file.write_patch internal, retire the patch-format prompt"
```

---

### Task 3: Delete dead tools

**Files:**
- Modify: `internal/app/app.go` (remove the `watch.RegisterTools(reg, watchManager)` call and its comment block)
- Delete: `internal/watch/tools.go`, plus its test file if one covers only those tools (check with `grep -ln 'watchStartTool\|RegisterTools' internal/watch/*_test.go`)
- Delete: `internal/tools/native/sessions_mail.go`, `sessions_mail_test.go`, `repo_card.go`, `repo_card_test.go`, `repo_map.go`, `repo_map_test.go`, `csv_inspect.go`, `csv_inspect_test.go`, `json_query.go`, `json_query_test.go`
- Modify: `internal/tools/native/repo_index.go`: delete only `repoIndexTool` (keep any index helpers other code calls)
- Modify: `internal/tools/native/native.go` (registration lines)
- Modify: `internal/tools/native/file.go` (delete `filePageTool` and its args type), `internal/agent/runner.go` (`case "file.read", "file.page":` → `case "file.read":`), `internal/agent/prompts.go` (the `baseEnvironment` sentence about large tool results)
- Modify: `internal/tools/native/question.go` (delete `askUserTool`), `internal/agent/chat.go` (delete the `ask_user` fallback), `internal/agent/execute.go` (delete the native `ask_user` branch, `executeNativeAskUser` if unused, and `"ask_user"` in `requiresSerialTool`)
- Modify: `internal/skills/tool.go` (delete the `skill.unload` and `skill.install` `reg.Register` blocks; keep their implementation functions if the TUI or CLI call them)
- Modify: `internal/agent/runner.go` (`RunTask`: delete the `UnreadMail` block)
- Modify: `internal/tools/registry/view.go` (drop the `"skill.install"` and `"ask_user"` cases that now name nothing)
- Modify: `internal/tools/native/symbols_find.go` (description: delete the sentence `Run repo.index first if no index exists.`)
- Test: `internal/tools/native/native_test.go`, `internal/app/app_test.go`

**Interfaces:**
- Produces: none of these names is registered: `watch.start`, `watch.stop`, `watch.status`, `watch.list`, `sessions.list`, `session.send`, `ask_user`, `skill.install`, `skill.unload`, `repo.map`, `repo.card`, `repo.index`, `csv.inspect`, `json.query`, `file.page`.

- [ ] **Step 1: Write the failing test.** Add to `internal/app/app_test.go` (it needs the full runtime registry, including the watch and skill tools):

```go
func TestRuntimeRegistersNoRetiredTools(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755)
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	for _, name := range []string{
		"watch.start", "watch.stop", "watch.status", "watch.list",
		"sessions.list", "session.send", "ask_user", "skill.install", "skill.unload",
		"repo.map", "repo.card", "repo.index", "csv.inspect", "json.query", "file.page",
	} {
		if _, ok := rt.Runner.Registry.Lookup(name); ok {
			t.Errorf("retired tool %s is still registered", name)
		}
	}
	for _, name := range []string{"skill.load", "file.read", "file.edit", "agent.await"} {
		if _, ok := rt.Runner.Registry.Lookup(name); !ok {
			t.Errorf("kept tool %s is missing", name)
		}
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestRuntimeRegistersNoRetiredTools -count=1`
Expected: FAIL, listing every retired tool.

- [ ] **Step 3: Delete the tools.**
  1. Apply the file deletions and registration removals in the Files list.
  2. In `native.go`, remove from `all`: `tools.repoIndexTool()`, `tools.repoMapTool()`, `tools.repoCardTool()`, `tools.filePageTool()`, `tools.askUserTool()`. Remove the csv/json deferred block entirely. Remove the `if tools.db != nil && tools.sessionState != nil { … sessionsListTool … sessionSendTool … }` block.
  3. In `prompts.go` `baseEnvironment`, delete the paragraph `When a tool result is large, use file.read with start_line/end_line (1-based, inclusive) or use file.page to read one page at a time instead of re-reading the spilled output file.`. Replace it with `When a tool result is large, read the spilled file with file.read start_line/end_line (1-based, inclusive).`.
  4. In `chat.go` `buildToolDefinitions`:
     - Delete `hasAskUser := false`, the `if tool.Name == "ask_user" { hasAskUser = true }` lines, and the trailing fallback block with its comment (`// Fallback for a registry that doesn't already register the real ask_user tool …`).
     - Keep the envelope-mode `ActionAskUser` handling in `runner.go` untouched.
  5. In `runner.go` `RunTask`, delete the block that starts `if db := r.State.DB(); db != nil {` / `if mails, err := db.UnreadMail(` and ends with its closing `}`.
  6. Remove stale mentions of the retired tools in model-facing text:
     - `internal/tools/native/file.go`, `file.read` description: change `(file.read start_line / file.page / repo.search kind)` to `(file.read start_line / repo.search)`.
     - The same file's two continuation footers (`continue with file.read start_line=%d, page with file.page, or narrow with repo.search kind:...`): change both to `continue with file.read start_line=%d, or narrow with repo.search`.
     - `internal/agent/spill.go`: delete `, or use file.page with the desired page number` from the truncation notice.
     - `internal/tools/native/command.go`, `shell.run` description: change `repo.search, symbols.find, file.read, and repo.map` to `repo.search, symbols.find, and file.read`.
     - `internal/agent/prompts.go` `ruleReadBeforeEdit`: change `repo.search, symbols.find, or repo.map` to `repo.search or symbols.find`.
     - Update any `prompts_test.go` or `file_test.go` assertions that pinned those strings.
  7. Delete the now-unused tests:
     - in `native_test.go`, any test exercising csv/json deferral
     - `internal/agent/execute*_test.go` tests of the native `ask_user` branch
     - `skills` tests of the unload/install tools (keep tests of the underlying install functions)

Also add this guard to `internal/app/app_test.go`. Tier 2b extends its list:

```go
// No model-facing text may teach a tool the model no longer has.
func TestNoToolDescriptionNamesARetiredTool(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755)
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	retired := []string{"file.page", "repo.map", "repo.card", "repo.index", "ask_user", "skill.install", "skill.unload", "csv.inspect", "json.query", "sessions.list", "session.send", "watch.start"}
	for _, tl := range rt.Runner.Registry.List() {
		if tl.Internal {
			continue
		}
		text := tl.Description + string(tl.Schema)
		for _, name := range retired {
			if strings.Contains(text, name) {
				t.Errorf("tool %s still mentions retired tool %s", tl.Name, name)
			}
		}
	}
}
```

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/app/ -run 'TestRuntimeRegistersNoRetiredTools|TestNoToolDescriptionNamesARetiredTool|TestDefaultPromptBudget' -count=1 -v && CGO_ENABLED=1 go test ./internal/agent/ ./internal/tools/... ./internal/skills/ ./internal/watch/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "tools: delete unused watch, mail, ask_user, skill install/unload, repo map/card/index, csv/json and file.page tools"
```

---

### Task 4: Merge background-work tools into `agent.await` / `agent.kill`; add `agent.steer`

**Files:**
- Modify: `internal/agent/await_sources.go` (extend `JobAwaitSource`)
- Modify: `internal/app/awaitsources.go` (implement the new methods on `jobAwaitAdapter`)
- Modify: `internal/agent/subagent_async.go`:
  - `agentAwaitArgs`, `NewSubagentAwaitTool`: add `peek`, drop `watch_id`
  - `NewSubagentKillTool`: accept `job_id`
  - delete `NewSubagentOutputTool`, keeping `subagentActivityTail`
  - add `NewSubagentSteerTool`
- Modify: `internal/agent/parentask.go` (delete `NewParentCorrectTool` and `parentCorrectArgs`)
- Modify: `internal/tools/native/jobs.go` (delete the three job tools; keep `jobOutputArgs` only if still used)
- Modify: `internal/tools/native/native.go` (remove the job tools from `all`)
- Modify: `internal/app/app.go`:
  - registration: delete `agent.output` and `parent.correct`
  - add `agent.steer`
  - pass `agent.WithAwaitJobs(...)` to `NewSubagentKillTool`
  - drop `agent.WithAwaitWatches(...)`
- Modify: `internal/agent/subagent.go`:
  - the `SubtaskScopeView` exclusion list: drop `agent.output`/`parent.correct`, add `agent.steer`
  - the `agent.run` description and start message: `agent.output` → `agent.await with "peek": true`
- Modify: `internal/viewmodel/describe.go`, `internal/app/tui/toolnames.go` (add `"agent.steer": "Steer subagent"`; keep the old labels)
- Test: `internal/agent/subagent_async_test.go`, `internal/app/awaitsources_test.go` (create if absent)

**Interfaces:**
- Consumes: `session.State.Subagent`, `PushSteering` (exactly what `NewParentCorrectTool` uses today), `native.JobManager.Output` / `Kill`.
- Produces:
  - `JobAwaitSource` gains `PeekJob(id string, tailLines int) (AwaitJobInfo, string, error)` and `KillJob(id string) error`.
  - `NewSubagentKillTool(state *session.State, opts ...AwaitOption) registry.Tool`
  - `NewSubagentSteerTool(state *session.State) registry.Tool`, with tool name `agent.steer` and args `{id int, message string}`.
  - `agent.await` args: `id`, `job_id`, `any`, `all`, `timeout_seconds`, `peek`.

- [ ] **Step 1: Write the failing tests.** Add to `internal/agent/subagent_async_test.go`. Use the file's existing fake job source if it has one; otherwise add this fake:

```go
type fakeJobs struct {
	info   AwaitJobInfo
	tail   string
	killed string
}

func (f *fakeJobs) OutstandingJobs() []AwaitJobInfo                      { return nil }
func (f *fakeJobs) AwaitJob(context.Context, string) (AwaitJobInfo, error) { return f.info, nil }
func (f *fakeJobs) JobOutputTail(string, int) string                     { return f.tail }
func (f *fakeJobs) PeekJob(id string, n int) (AwaitJobInfo, string, error) {
	if id != f.info.ID {
		return AwaitJobInfo{}, "", fmt.Errorf("unknown job %s", id)
	}
	return f.info, f.tail, nil
}
func (f *fakeJobs) KillJob(id string) error { f.killed = id; return nil }

// Review focus 5.
func TestAgentAwaitPeekFinishedJob(t *testing.T) {
	code := 1
	jobs := &fakeJobs{info: AwaitJobInfo{ID: "job-1", Command: "go test", Status: "exited", ExitCode: &code}, tail: "FAIL x"}
	tool := NewSubagentAwaitTool(newTestState(t), WithAwaitJobs(jobs))
	done := make(chan struct{})
	var res registry.ToolResult
	var err error
	go func() {
		res, err = tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-1","peek":true}`)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("peek blocked")
	}
	if err != nil || !strings.Contains(res.Content, "exited") || !strings.Contains(res.Content, "exit: 1") || !strings.Contains(res.Content, "FAIL x") {
		t.Fatalf("peek result = %+v, %v", res, err)
	}
}

func TestAgentAwaitPeekRequiresATarget(t *testing.T) {
	tool := NewSubagentAwaitTool(newTestState(t))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"any":true,"peek":true}`)}); err == nil {
		t.Fatal("peek with any must be rejected")
	}
}

func TestAgentKillKillsJob(t *testing.T) {
	jobs := &fakeJobs{info: AwaitJobInfo{ID: "job-2"}}
	tool := NewSubagentKillTool(newTestState(t), WithAwaitJobs(jobs))
	if _, err := tool.Handler(context.Background(), registry.ToolCall{Args: []byte(`{"job_id":"job-2"}`)}); err != nil || jobs.killed != "job-2" {
		t.Fatalf("kill job: err=%v killed=%q", err, jobs.killed)
	}
}
```

Copy the existing `parent.correct` handler tests into `agent.steer` tests: `grep -n 'NewParentCorrectTool' internal/agent/*_test.go` finds them. Rename the tool constructor, `child_id` → `id`, and `correction` → `message` in the args. The assertions stay the same: steering is delivered to the live child, unknown ids error, an empty message errors.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/agent/ -run 'TestAgentAwaitPeek|TestAgentKillKillsJob|Steer' -count=1`
Expected: compile errors (`PeekJob` / `KillJob` not in the interface, `NewSubagentSteerTool` undefined).

- [ ] **Step 3: Implement.**
  1. In `await_sources.go`, add to `JobAwaitSource`:

```go
	// PeekJob returns the job's current state and a bounded output tail
	// without waiting.
	PeekJob(id string, tailLines int) (AwaitJobInfo, string, error)
	// KillJob terminates a running background job.
	KillJob(id string) error
```

  2. In `internal/app/awaitsources.go`:

```go
func (a jobAwaitAdapter) PeekJob(id string, tailLines int) (agent.AwaitJobInfo, string, error) {
	info, out, err := a.m.Output(id, tailLines)
	if err != nil {
		return agent.AwaitJobInfo{}, "", err
	}
	return agent.AwaitJobInfo{ID: info.ID, Command: info.Command, Status: string(info.Status), ExitCode: info.ExitCode}, strings.TrimRight(out, "\n"), nil
}

func (a jobAwaitAdapter) KillJob(id string) error { return a.m.Kill(id) }
```

  3. In `NewSubagentAwaitTool`:
     - Add `Peek bool \`json:"peek"\`` to `agentAwaitArgs` and delete `WatchID`.
     - Delete every `watch`/`WatchID` branch and the `watches` field from `awaitOptions`, plus `WithAwaitWatches`. Remove the watch half of `scanPending` and the `watch` fields of `awaitArrival`. `AwaitWatchInfo`/`WatchAwaitSource` stay only if something outside `agent.await` uses them; otherwise delete them and `watchAwaitAdapter`.
     - Replace the description and schema with:

```go
		Description: `Wait for background work: a subagent ("id"), a background shell job ("job_id"), the first to finish ("any": true), or everything ("all": true). Pass exactly one. "peek": true with "id" or "job_id" returns current status and recent output immediately instead of waiting. "timeout_seconds" bounds a wait; on expiry you get what is still running. Subagent reports also arrive on their own when they finish.`,
		Schema:      json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"},"job_id":{"type":"string"},"any":{"type":"boolean"},"all":{"type":"boolean"},"peek":{"type":"boolean"},"timeout_seconds":{"type":"integer"}},"additionalProperties":false}`),
```

     - After the exactly-one-selection check, add:

```go
		if args.Peek {
			switch {
			case args.ID != 0:
				return peekSubagent(state, args.ID)
			case args.JobID != "":
				if cfg.jobs == nil {
					return registry.ToolResult{}, fmt.Errorf("%s: background jobs are not available", tool.Name)
				}
				info, tail, err := cfg.jobs.PeekJob(args.JobID, 20)
				if err != nil {
					return registry.ToolResult{}, err
				}
				var b strings.Builder
				fmt.Fprintf(&b, "status: %s\ncommand: %s\n", info.Status, info.Command)
				if info.ExitCode != nil {
					fmt.Fprintf(&b, "exit: %d\n", *info.ExitCode)
				}
				if tail != "" {
					b.WriteString("\n" + tail)
				}
				return registry.ToolResult{Summary: fmt.Sprintf("job %s is %s", info.ID, info.Status), Content: strings.TrimRight(b.String(), "\n")}, nil
			default:
				return registry.ToolResult{}, fmt.Errorf("%s: peek needs \"id\" or \"job_id\"", tool.Name)
			}
		}
```

     - Create `peekSubagent(state *session.State, id int64) (registry.ToolResult, error)` from the body of `NewSubagentOutputTool`'s handler, minus the `transcript` option and the `tail_lines` argument (use 5).
     - Delete `NewSubagentOutputTool`, `agentOutputArgs`, `subagentTranscriptText` and its constants, if nothing else uses them.
  4. `NewSubagentKillTool(state *session.State, opts ...AwaitOption)`:
     - Add `JobID string \`json:"job_id"\`` to `agentKillArgs`, and require exactly one of `id` / `job_id`.
     - For `job_id`, call `cfg.jobs.KillJob` and return `Summary: "killed job <id>"`.
     - Description: `Cancel a background subagent ("id") or shell job ("job_id"). A killed subagent's report arrives marked failed ("context canceled").` Schema: `{"type":"object","properties":{"id":{"type":"integer"},"job_id":{"type":"string"}},"additionalProperties":false}`.
     - Replace the remaining `agent.output` mentions in its result text with `agent.await with "peek": true`.
  5. `NewSubagentSteerTool(state)`:
     - Move `NewParentCorrectTool`'s handler body here, with name `agent.steer`, `Risk: registry.RiskReadOnly`.
     - Description: `Send an instruction to a running subagent; it arrives as steering at the child's next step (e.g. "wrap up and report now", an environment note, a scope change). A finished subagent cannot be steered.`
     - Schema: `{"type":"object","properties":{"id":{"type":"integer"},"message":{"type":"string"}},"required":["id","message"],"additionalProperties":false}`.
     - Delete `NewParentCorrectTool` and `parentCorrectArgs`.
  6. In `app.go`, change the registrations to:

```go
	if err := reg.Register(agent.NewSubagentAwaitTool(state, agent.WithAwaitJobs(jobAwaitAdapter{m: jobManager}))); err != nil { … }
	if err := reg.Register(agent.NewSubagentKillTool(state, agent.WithAwaitJobs(jobAwaitAdapter{m: jobManager}))); err != nil { … }
	if err := reg.Register(agent.NewSubagentSteerTool(state)); err != nil { … }
```

     Keep the existing error returns. Delete the `agent.output` and `parent.correct` registrations and their comments.
  7. In `jobs.go`, delete `jobOutputTool`, `jobKillTool` and `jobListTool`, and remove them from `native.go`'s `all`.
  8. Update `SubtaskScopeView`, the `agent.run` description and start message, and the display maps, as listed in Files.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/agent/... ./internal/app/ ./internal/tools/native/ -count=1`
Expected: PASS. Remove tests of the deleted `job.*`/`agent.output` tools only where they test the tool wrapper. Keep `JobManager` tests.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "agent: fold agent.output and job.* into agent.await/kill; parent.correct becomes agent.steer"
```

---

### Task 5: Remove the subagent question channel (`parent.ask` / `parent.respond`)

**Files:**
- Modify: `internal/agent/parentask.go`. After Task 4 it holds only the ask/respond pieces, so delete the file and `parentask_test.go`.
- Modify: `internal/agent/subagent.go`:
  - delete the `parentQuestioner`, `parentAskAllowed`, `parentAskTimeout` and `parentAskMaxPerTask` fields
  - delete the `WithParentQuestioner`, `WithParentAskAllowed` and `WithParentAskLimits` options
  - delete the `if cfg.parentAskAllowed && …` registration block
  - delete their defaults
- Modify: `internal/agent/runner.go` (delete `pendingChildQuestionHint` and its loop-top call in `RunTask`: `if hint, ok := r.pendingChildQuestionHint(); ok {`)
- Modify: `internal/app/app.go`:
  - delete the parent-question block, from `// Parent/child question bridge:` through `parentAskAllowed` setup
  - delete the three `agent.WithParent…` options passed to `NewSubagentTool`
  - delete the `parent.respond` registration
- Modify: `internal/app/session/pending.go`, `session.go`: delete `PendingChildQuestion`, `PushChildQuestion`, `ResolveChildQuestion`, `ChildQuestions`, `childQuestionHeadLocked`, `snapshotChildQuestionLocked`, the `childQuestions` field, `EventChildQuestionChanged`, and the `Event.PendingChildQuestion` field
- Modify: `internal/app/tui/question.go`, `model.go`, `view.go`, `keypress.go`: delete `newChildQuestionModel`, `childQuestionModel`, `handleChildQuestion`, `formatChildAnsweredNotice` and `renderChildQuestionPanel`. Remove each `|| m.state.PendingChildQuestion() != nil` / `&& m.state.PendingChildQuestion() == nil` term from boolean conditions, keeping the rest of each condition.
- Modify: `internal/acp/questions.go` (delete `AskChild`, `ErrNilPendingChildQuestion`, `childAttribution`, and the `fromSubagent` plumbing if only `AskChild` sets it), `internal/acp/turn.go` (delete the child-question event forwarding)
- Modify: `internal/app/config/types.go`, `file_types.go`, `defaults.go`, `merge.go`, `save.go`: delete `ParentQuestionRoles`, `ParentQuestionTimeout` and `ParentQuestionMaxPerTask`. Unknown TOML keys are ignored on load, so existing config files keep working.
- Modify: `internal/viewmodel/describe.go`, `internal/app/tui/toolnames.go`: keep the old `parent.*` labels for history rendering.
- Test: `internal/app/app_test.go`

**Interfaces:**
- Consumes: Task 4 (`parent.correct` already moved to `agent.steer`).
- Produces: no `parent.ask` or `parent.respond` tool, and no child-question state, events or UI. `question.ask` for the main agent is unchanged, and so is the SDD gate panel (`gatepanel`, which uses `session.SDDGate`).

- [ ] **Step 1: Write the failing test.** Add to `internal/app/app_test.go`:

```go
func TestNoSubagentQuestionChannel(t *testing.T) {
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, ".marshal"), 0755)
	rt, err := StartRuntime(context.Background(), WithWorkingDir(tmp), WithHomeDir(t.TempDir()), WithTrustResolver(&fakeTrustResolver{decision: trust.DecisionTrustPermanent}))
	if err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	defer rt.Close(context.Background())
	for _, name := range []string{"parent.respond", "parent.correct", "parent.ask"} {
		if _, ok := rt.Runner.Registry.Lookup(name); ok {
			t.Errorf("%s must not be registered", name)
		}
	}
	if _, ok := rt.Runner.Registry.Lookup("agent.steer"); !ok {
		t.Error("agent.steer must be registered")
	}
}
```

- [ ] **Step 2: Run the test and confirm it fails.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestNoSubagentQuestionChannel -count=1`
Expected: FAIL (`parent.respond must not be registered`).

- [ ] **Step 3: Delete the channel.** Apply the deletions in the Files list, following the Deletion rule in Global Constraints. In `RoleSubtask`'s focus text (`prompts.go`), the sentence about `question.ask` being unavailable stays true; leave it as is.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/agent/... ./internal/app/... ./internal/acp/ -count=1`
Expected: PASS, apart from the known environmental failures. Delete tests whose only subject is the removed channel. Keep `TestForcedPrompts*` and every `question.ask` test.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "agent: remove the unused subagent question channel (parent.ask/parent.respond)"
```

---

### Task 6: Merge `recall_history` into `transcript_read`

**Files:**
- Modify: `internal/tools/native/transcript.go` (add `query` and `limit_matches` handling; new description and schema)
- Modify: `internal/tools/native/recall.go`:
  - delete `NewRecallTool`, `recallHistoryTool` and `recallToolEnabled`
  - keep the search-and-format body as `func searchArchivedTurns(database *db.DB, query string, limit int) (registry.ToolResult, error)`
- Modify: `internal/tools/native/native.go` (delete the `recallToolEnabled` registration block)
- Modify: `internal/agent/rollover.go`, `internal/agent/handoff.go` (`continueInstruction(r.hasTool("recall_history"))` → `continueInstruction(r.hasTool("transcript_read"))`; the instruction text names `transcript_read with a "query"`)
- Modify: `internal/tools/registry/view.go` (`PlanWriterView`: drop `"recall_history"` from the stripped list)
- Test: `internal/tools/native/transcript_test.go`, `internal/agent/rollover_test.go`

**Interfaces:**
- Produces: `transcript_read` args are the existing ones plus `query string` and `limit_matches int`. With `query` set, it searches archived turns across the project, as `recall_history` did, and ignores the paging args. The default is 5 matches and the maximum 20.

- [ ] **Step 1: Write the failing test.** In `internal/tools/native/recall_test.go`, change the fixture helper `newTestRecallDB` so it returns only the `*db.DB`: replace its last three lines (`cfg := …`, `tool := NewRecallTool(…)`, `return database, tool`) with `return database`, and change its signature to `func newTestRecallDB(t *testing.T, content string) *db.DB`. Its remaining callers that tested truncation (`TestRecallHistory_TruncatesOversizedHits` and its siblings) switch to `searchArchivedTurns(database, query, limit)`, which Step 3 creates. Delete the `TestRecallToolEnabled_*` and `TestNewRecallTool_*` tests. Then add to `internal/tools/native/transcript_test.go`:

```go
func TestTranscriptReadQuerySearchesArchivedTurns(t *testing.T) {
	database := newTestRecallDB(t, "ran distinctive-phrase-42 here")
	defer database.Close()
	state := session.New(config.Default(), "/test/repo", time.Now(), session.Persistence{})
	tool := NewTranscriptTool(state, database)
	res, err := tool.Handler(context.Background(), registry.ToolCall{Name: "transcript_read", Args: []byte(`{"query":"distinctive-phrase-42"}`)})
	if err != nil {
		t.Fatalf("transcript_read query: %v", err)
	}
	if !strings.Contains(res.Content, "distinctive-phrase-42") {
		t.Fatalf("query result missing the match: %q", res.Content)
	}
}
```

  In `internal/agent/rollover_test.go`, change `TestCompactionTellsTheModelItCanRecall` to register `transcript_read` instead of `recall_history`, and to assert the instruction contains `transcript_read`.

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/ -run TestTranscriptReadQuery -count=1; CGO_ENABLED=1 go test ./internal/agent/ -run TestCompactionTellsTheModelItCanRecall -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement.**
  1. Extract `searchArchivedTurns` from `NewRecallTool`'s handler: the part from `hits, err := t.db.SearchArchivedTurns(…)` to the end, with `limit` clamping kept.
  2. In `transcript.go`'s handler, before the existing logic: if the decoded args have a non-empty `Query`, return `searchArchivedTurns(t.db, args.Query, args.LimitMatches)` when `t.db != nil`, or an error `"transcript_read: history search needs the session database"` otherwise.
  3. Replace the description with: `Read this session's transcript, including turns aged out of the replayed history, or search archived turns across the project. Default: the live transcript, paged by offset/limit. "generation_seq" reads an archived generation (0 is the first). "turn_seq" re-reads one turn by its header number. "query" searches archived turns for a distinctive phrase (an exact command, error text, path or decision) and ignores paging.`
  4. Add these to the schema properties, keeping the others:
     - `"query":{"type":"string"}`
     - `"limit_matches":{"type":"integer"}`
  5. Shorten each existing property `description` to at most one short clause:
     - `generation_seq`: "archived generation to read; omit for the live transcript"
     - `session_id`: "defaults to the current session"
     - `turn_seq`: "one turn by its header number"
     - `offset`: "turns to skip"
     - `limit`: "max turns (default 20, max 100)"
     - `max_chars`: "character cap (default 16000, max 64000)"
     - `include_all`: "live only: include non-replayed narration and notes"
  6. In `rollover.go`, change the `continueInstruction` text from `call recall_history to search the archived transcript` to `call transcript_read with a "query" to search the archived transcript`. Update its doc comment, and update the two `hasTool` call sites as listed in Files.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/tools/native/ ./internal/agent/ ./internal/tools/registry/ -count=1`
Expected: PASS. Delete `recall_test.go` cases that tested the tool wrapper. Keep a search-formatting test pointed at `searchArchivedTurns`.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "tools: merge recall_history into transcript_read as a query"
```

---

### Task 7: Remove the scratchpad from the model's view

**Files:**
- Delete: `internal/tools/native/scratchpad.go`, `scratchpad_test.go`, `internal/agent/scratchpad_integration_test.go`
- Modify: `internal/tools/native/native.go` (delete the four scratchpad tools from the `sessionState != nil` block)
- Modify: `internal/agent/route.go` (delete `mergeScratchpad`), `internal/agent/runner.go` (delete both `r.mergeScratchpad(route.ContextBudget.MaxRepoContextTokens)` calls)
- Modify: `internal/agent/prompts.go` (delete `scratchpadAddendum` and the `if nativeTools { b.WriteString(scratchpadAddendum) }` block)
- Modify: `internal/tools/policy/policy.go` (`case "todo.write", "scratchpad.write", "scratchpad.delete":` → `case "todo.write":`; fix the comment above it)
- Modify: `internal/contextpack/builder.go`, `contextpack.go`, `render.go`: delete `MergeScratchpad`, `ScratchpadEntry` and `SectionScratchpad` if nothing else references them after the agent change
- Modify: `internal/app/session/session.go`: delete the scratchpad map, `SetScratchpadEntry` and the other scratchpad methods, the `scratchpadConfig` field and the `WithClock` option, if nothing else uses them. Keep the `config.ScratchpadConfig` type.
- Modify: `internal/pipeline/exec_context.go` (comment: `todo.write, scratchpad.*` → `todo.write`)
- Test: `internal/app/app_test.go` (extend `TestRuntimeRegistersNoRetiredTools`)

**Interfaces:**
- Produces: no `scratchpad.*` tool, no scratchpad context-pack section, no scratchpad prompt text. The DB table and config struct remain.

- [ ] **Step 1: Write the failing test.** In `TestRuntimeRegistersNoRetiredTools` (Task 3), add `"scratchpad.write", "scratchpad.read", "scratchpad.list", "scratchpad.delete"` to the retired list. Add to `internal/agent/prompts_test.go`:

```go
func TestNativePromptHasNoScratchpadAddendum(t *testing.T) {
	msg := BuildSystemPromptWithSystem(SystemPromptOptions{Role: RoleGeneral, NativeTools: true, Mode: policy.ModeEdit})
	if strings.Contains(msg.Content, "scratchpad") {
		t.Fatalf("native prompt still mentions the scratchpad:\n%s", msg.Content)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestRuntimeRegistersNoRetiredTools -count=1; CGO_ENABLED=1 go test ./internal/agent/ -run TestNativePromptHasNoScratchpadAddendum -count=1`
Expected: both FAIL.

- [ ] **Step 3: Delete,** following the Files list and the Deletion rule.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/agent/... ./internal/app/... ./internal/contextpack/ ./internal/tools/... -count=1`
Expected: PASS, apart from the known environmental failures.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "agent: remove the scratchpad tools, projection and prompt addendum"
```

---

### Task 8: Conditional `codebase_search`, description trims, roster slimming

**Files:**
- Modify: `internal/tools/native/native.go` (register `codebaseSearchTool` only when an embedding route resolves)
- Modify: `internal/agent/subagent.go` (`agent.run` description and the per-property schema descriptions)
- Modify: `internal/tools/native/question.go` (`question.ask` description)
- Modify: `internal/tools/native/todos.go` (`todo.write` description), `internal/agent/prompts.go` (delete `todoAddendum` and its loop in `buildSystemPrompt`)
- Modify: `internal/agent/prompts.go` roster:
  - `pricingNote`
  - `presetRoleNote` / its caller
  - `RenderAgentRosterWithDiscovered`: skip the embedding preset, shorter guidance
- Test: `internal/agent/prompts_test.go`, `internal/tools/native/codebase_search_test.go`

**Interfaces:**
- Produces:
  - `codebase_search` is registered iff `routing.NewStaticRouter(cfg.RoutingConfig()).ResolveEmbedding()` returns no error.
  - `pricingNote` returns `""` for all-zero rates.
  - The roster omits the preset named by `cfg.RoutingConfig().EmbeddingPreset`.
  - Role notes list only `implementer`, `reviewer`, `security_reviewer`, `planner`, `repo_scout`, `tester` and `subtask`.

- [ ] **Step 1: Write the failing tests.** Add to `internal/tools/native/codebase_search_test.go`:

```go
func TestCodebaseSearchRegisteredOnlyWithEmbeddings(t *testing.T) {
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: t.TempDir(), CommandRunner: &fakeRunner{}, Config: config.Default()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("codebase_search"); ok {
		t.Fatal("codebase_search must not be registered without an embedding route")
	}
}
```

  If the file already builds a config with an embedding preset for other tests, also assert that such a config does register the tool.

  Add to `internal/agent/prompts_test.go`:

```go
func TestRosterDropsNoiseAndEmbeddingPreset(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = map[string]config.ProviderConfig{"p": {Type: "openai_compatible", BaseURL: "http://x"}}
	cfg.Models.Presets = map[string]routing.ModelPreset{
		"p/chat":  {Provider: "p", Model: "chat"},
		"p/embed": {Provider: "p", Model: "embed"},
	}
	cfg.Indexing.EmbeddingPreset = "p/embed"
	got := RenderAgentRoster(cfg)
	if strings.Contains(got, "no cost data") {
		t.Errorf("roster still carries the unpriced label:\n%s", got)
	}
	if strings.Contains(got, "p/embed") {
		t.Errorf("roster offers the embedding preset as a subagent model:\n%s", got)
	}
	if !strings.Contains(got, "p/chat") {
		t.Errorf("roster lost the chat preset:\n%s", got)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail.**

Run: `CGO_ENABLED=1 go test ./internal/tools/native/ -run TestCodebaseSearchRegisteredOnlyWithEmbeddings -count=1; CGO_ENABLED=1 go test ./internal/agent/ -run TestRosterDropsNoiseAndEmbeddingPreset -count=1`
Expected: both FAIL.

- [ ] **Step 3: Implement.**
  1. In `native.go`, remove `tools.codebaseSearchTool()` from `all`, and after the `all` literal add:

```go
	// codebase_search only works with an embedding route; offering it
	// otherwise teaches the model a tool that answers "unavailable".
	if _, err := routing.NewStaticRouter(opts.Config.RoutingConfig()).ResolveEmbedding(); err == nil {
		all = append(all, tools.codebaseSearchTool())
	}
```

     `RegisterAll` receives `opts`; use it there. `routing` is already imported.
  2. `agent.run`:
     - Description:

```go
		Description: fmt.Sprintf("Delegate a scoped subtask to a fresh child agent. It runs in the background: this returns a handle (subagent N) immediately and the report arrives later as a [subagent N finished] message. Do not treat the task as done until then; use agent.await to wait or peek, agent.steer to redirect, agent.kill to stop. Up to %d run at once; writes across agents are serialized. The child has your tools except agent.run and question.ask. \"model\" (provider/model) overrides the model; \"agent\" names a configured custom agent.", concurrency),
```

     - Shorten the schema property descriptions:
       - `prompt`: "the subtask, passed verbatim"
       - `description`: "short label"
       - `agent`: "configured custom agent name"
       - `model`: "provider/model pair"
       - `temperature`: "sampling temperature override"
       - `thinking`: "reasoning effort override (low/medium/high)"
       - `system`: "grant system access (parent must have it)"
  3. `question.ask` description:

```go
		Description: "Ask the user one or more questions in a single round-trip, only when you need information or a decision to proceed. Put known choices in options (never as lettered text); the user can always answer outside them. Don't use it to give instructions or to request a mode switch (use mode.request).",
```

  4. `todo.write` description:

```go
		Description: "Replace the session todo list. Use it for work with 3+ steps or several requirements, and mark each item completed as soon as it is done. Unfinished items you omit are carried over; pass \"drop_unfinished\": true to make the submitted list the whole list.",
```

     Delete `todoAddendum` and the `for _, tool := range tools { if tool.Name == "todo.write" { b.WriteString(todoAddendum) … } }` loop. Update or delete the `prompts_test.go` assertions that expect the addendum text in the system prompt; they move to the tool description.
  5. Roster:
     - In `pricingNote`, change the all-zero branch to `return ""`.
     - In `presetRoleNote`, filter `seg.Roles` to the set `{implementer, reviewer, security_reviewer, planner, repo_scout, tester, subtask}`. Drop a profile segment whose filtered list is empty.
     - In `RenderAgentRosterWithDiscovered`'s preset loop, `continue` when `pname == rc.EmbeddingPreset` (`rc := cfg.RoutingConfig()` is already in scope).
     - Replace the two trailing guidance strings with:

```go
	if annotated > 0 {
		b.WriteString("Pick a subagent model by the role bindings above (the user's own ranking); prefer a preset marked local for secrets or private data; omit model to use your own.\n")
	}
	b.WriteString("model must be a provider/model pair from a configured provider.")
```

     - Update the roster tests in `prompts_test.go` that pin the old strings (`grep -n 'no cost data\|Choosing a subagent model\|discovered entries above' internal/agent/prompts_test.go`) to the new text.

- [ ] **Step 4: Run the tests and confirm they pass.**

Run: `CGO_ENABLED=1 go build ./... && CGO_ENABLED=1 go test ./internal/agent/ ./internal/tools/native/ -count=1 && CGO_ENABLED=1 go test ./internal/app/ -run TestDefaultPromptBudget -count=1 -v`
Expected: PASS. Record the two budget numbers from the log for Task 9.

- [ ] **Step 5: Commit.**

```bash
gofmt -w internal
git add -A internal
git commit -m "prompt: register codebase_search only with embeddings; trim tool descriptions and the roster"
```

---

### Task 9: Lower the budget ceilings, update docs, full verification

**Files:**
- Modify: `internal/app/prompt_budget_test.go`
- Modify: `CHANGELOG.md` (`## [Unreleased]`: `### Removed` / `### Changed`)
- Modify: `docs/context-diet/tier2a-design.md` (append a measured-results section)
- Modify: `README.md`, `docs/ARCHITECTURE.md`: anything naming `file.write_patch`, `recall_history`, the scratchpad tools or `parent.ask` as model tools (`grep -n 'write_patch\|recall_history\|scratchpad\|parent\.ask' README.md docs/ARCHITECTURE.md`)

**Interfaces:**
- Consumes: Tasks 1–8.

- [ ] **Step 1: Lower the ceilings.** In `prompt_budget_test.go`, set:

```go
const (
	maxSystemPromptBytes = 9000
	maxToolSchemaBytes   = 16000
)
```

  Extend its banned-text list with `"file.write_patch format"`, `"scratchpad.write"` and `"recall_history"`.
  In `internal/app/app_test.go` `TestNoToolDescriptionNamesARetiredTool`, extend `retired` with `"agent.output"`, `"job.output"`, `"job.kill"`, `"job.list"`, `"recall_history"`, `"scratchpad."`, `"parent.respond"`, `"parent.ask"`, `"parent.correct"`, `"file.write_patch"` and `"watch_id"`. Run it, and fix any description still naming one of them.

- [ ] **Step 2: Run it.**

Run: `CGO_ENABLED=1 go test ./internal/app/ -run TestDefaultPromptBudget -count=1 -v`
Expected: PASS, with the log showing the system prompt near 8,000 bytes and the tool schemas near 15,000 bytes. If either is over its ceiling, don't raise the ceiling. Report which tool or prompt section is larger than the design expected.

- [ ] **Step 3: Docs.**
  - Append `## Measured result` to `tier2a-design.md`, with the two numbers from Step 2, the date, and the count of model-facing tools from `TestDefaultPromptBudget`'s registry walk. Add a `t.Logf` of the count in the budget test so it's printed.
  - `CHANGELOG.md` under `### Removed`:

```markdown
- Model-facing tools retired: `watch.*`, `sessions.list`, `session.send`,
  `ask_user`, `skill.install`, `skill.unload`, `repo.map`, `repo.card`,
  `repo.index`, `csv.inspect`, `json.query`, `file.page`, `job.*`,
  `agent.output`, `recall_history`, `scratchpad.*`, `parent.ask`,
  `parent.respond`. `test.run` and `file.write_patch` remain internal only.
- The subagent question channel and its `[agent] parent_question_*` settings.
```

  - Under `### Changed`:

```markdown
- `file.edit {path, old_string, new_string}` replaces `file.write_patch` for
  the model. Permission rules and hooks written for `file.write_patch` keep
  applying.
- `agent.await` gains `peek`, `agent.kill` accepts `job_id`, and
  `parent.correct` is now `agent.steer {id, message}`.
- `transcript_read` takes a `query` to search archived turns.
- `codebase_search` is offered only when an embedding route is configured.
- The model roster no longer labels unpriced models "cheap", hides
  internal-only roles, and leaves out the embedding preset.
```

  - Update the README/ARCHITECTURE mentions found by the grep, so they describe the current tools.

- [ ] **Step 4: Full verification.**

```bash
gofmt -l . | grep -v '^\.home/' ; CGO_ENABLED=1 go vet ./... && CGO_ENABLED=1 go test ./... 2>&1 | tail -60
CGO_ENABLED=1 go build -o marshal ./cmd/marshal && go test ./test/usability/... -run TestScripted -timeout 5m
cd web/ui && npm run build && cd ../.. && go build ./cmd/webbridge && go test ./web/... -count=1
```

Expected: `gofmt -l` prints nothing, `go vet` is clean, the Go suite passes apart from the known environmental failures, the usability suite passes, and the web bridge builds and passes. The web side is in scope only as a regression check; nothing under `web/` changes.

- [ ] **Step 5: Commit.**

```bash
git add internal/app/prompt_budget_test.go CHANGELOG.md docs README.md
git commit -m "test: lower the prompt budget ceilings after the Tier 2a tool diet"
```

## Integration notes

- **Order:** Task 1 → Task 2 → Tasks 3, 6, 7, 8 (any order) → Task 4 → Task 5 → Task 9. Task 5 depends on Task 4 moving `parent.correct` out of `parentask.go`. Tasks 3 and 7 both extend `TestRuntimeRegistersNoRetiredTools`; if 7 lands first, it creates that test using Task 3's Step 1 body.
- **Envelope mode** keeps working through the internal `file.write_patch` and its own `ask_user` action. Tier 3 deletes envelope mode, `file.write_patch` and its views' patch wrappers together.
- **Swarm** is untouched. Its tester still gets `test.run` through `TesterView`. The swarm archive is a separate plan.
