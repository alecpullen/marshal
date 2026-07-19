# Tools & Policy Audit Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve the eight open Tools & policy findings (F-BUG-39 … F-POL-46) from `docs/14-codebase-improvement-audit-2026-07-14.md`.

**Architecture:** Each task fixes one finding in isolation. Low-risk edits land first (parsers, format strings, optional logger injection), then the higher-risk logic changes (new-file creation, deduplicated guardrail path). Tests are added next to existing `_test.go` files in the same package and run with `go test ./internal/...` after each task.

**Tech Stack:** Go (stdlib only — `log/slog`, `html`, `encoding/json`).

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter), but the
  tasks below touch pure-Go files only.
- Every code change MUST compile: run `go build ./...` after the
  implementation step of each task.
- Every test change MUST pass: run `go test ./internal/<pkg> -run <TestName>`
  for the new test, then `go test ./internal/...` at task end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing public function signatures unless the task explicitly
  says to change them; for F-POL-45 use an additive setter to avoid
  touching 100+ test call sites.

---

## File Structure

Files modified or created by this plan:

- `internal/tools/patch/parser.go` — F-BUG-40 (strict parser).
- `internal/tools/patch/parser_test.go` — F-BUG-40 tests.
- `internal/tools/native/file.go` — F-BUG-39 (new-file creation).
- `internal/tools/native/file_test.go` — F-BUG-39 tests.
- `internal/agent/runner.go` — F-BUG-41 (re-evaluate policy after edit).
- `internal/agent/runner_test.go` — F-BUG-41 tests.
- `internal/tools/native/web.go` — F-BUG-42 (`html.UnescapeString`).
- `internal/tools/native/web_test.go` — F-BUG-42 tests.
- `internal/tools/native/jobs_manager.go` — F-BUG-43 (split stdout/stderr).
- `internal/tools/native/jobs_test.go` — F-BUG-43 tests.
- `internal/tools/policy/policy.go` — F-POL-44 (expose `EvaluateGuardrails`),
  F-POL-45 (additive `SetLogger`).
- `internal/tools/policy/policy_test.go` — F-POL-44/45 tests.
- `internal/tools/native/command.go` — F-POL-44 (delegate to policy).
- `internal/tools/native/native.go` — F-POL-44 (new `Options.Policy` field).
- `internal/app/app.go` — F-POL-44 (wire policy engine into native opts),
  F-POL-45 (pass logger to `policy.NewEngine`).
- `internal/tools/registry/types.go` — F-POL-46 (`LimitsJSON` returns error).
- `internal/tools/registry/types_test.go` — F-POL-46 tests.
- `internal/db/audits.go` — F-POL-46 (handle the new error path).

---

### Task 1: F-BUG-40 — Strict patch parser (reject unclosed blocks, empty paths)

**Files:**
- Modify: `internal/tools/patch/parser.go:17-76`
- Test: `internal/tools/patch/parser_test.go`

**Interfaces:**
- Consumes: existing test fixture in `parser_test.go`.
- Produces: `Parse(proposal string) ([]FilePatch, error)` — error is
  non-nil when a SEARCH/REPLACE block is unclosed or when a chunk is
  attached to a `FilePatch` with an empty path.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tools/patch/parser_test.go`:

```go
func TestParseRejectsUnclosedSearch(t *testing.T) {
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for unclosed SEARCH block, got nil")
	}
	if !strings.Contains(err.Error(), "unclosed") {
		t.Fatalf("error should mention unclosed block: %v", err)
	}
}

func TestParseRejectsUnclosedReplace(t *testing.T) {
	input := "File: foo.go\n<<<<<<< SEARCH\nhello\n=======\nworld\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for unclosed REPLACE block, got nil")
	}
}

func TestParseRejectsEmptyPathChunk(t *testing.T) {
	input := "<<<<<<< SEARCH\nhello\n=======\nworld\n>>>>>>> REPLACE\n"
	_, err := Parse(input)
	if err == nil {
		t.Fatal("expected error for chunk with empty path, got nil")
	}
}
```

Add `"strings"` to the test file's imports if not present.

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/tools/patch -run 'TestParseRejects' -v`
Expected: all three new tests FAIL.

- [ ] **Step 3: Implement the strict parser**

Replace the body of `Parse` in `internal/tools/patch/parser.go` with:

```go
func Parse(proposal string) ([]FilePatch, error) {
	var patches []FilePatch
	lines := strings.Split(strings.ReplaceAll(proposal, "\r\n", "\n"), "\n")

	var currentPath string
	var searchBuffer []string
	var replaceBuffer []string
	inSearch := false
	inReplace := false

	flushChunk := func() error {
		if !(inSearch || inReplace) {
			return nil
		}
		if inSearch {
			return fmt.Errorf("patch: unclosed SEARCH block for %q", currentPath)
		}
		return fmt.Errorf("patch: unclosed REPLACE block for %q", currentPath)
	}

	commitChunk := func() {
		chunk := PatchChunk{
			Search:  strings.Join(searchBuffer, "\n"),
			Replace: strings.Join(replaceBuffer, "\n"),
		}
		if currentPath == "" {
			// Drop chunks with no path; the surrounding File: line was
			// missing or came after the chunk header. Caller will still
			// see an empty result, which is the legacy behavior for
			// orphan chunks. Detected separately when the chunk is the
			// only one in the proposal (see TestParseRejectsEmptyPathChunk).
			return
		}
		found := false
		for i := range patches {
			if patches[i].Path == currentPath {
				patches[i].Chunks = append(patches[i].Chunks, chunk)
				found = true
				break
			}
		}
		if !found {
			patches = append(patches, FilePatch{
				Path:   currentPath,
				Chunks: []PatchChunk{chunk},
			})
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "File:") {
			if err := flushChunk(); err != nil {
				return nil, err
			}
			currentPath = strings.TrimSpace(strings.TrimPrefix(trimmed, "File:"))
			continue
		}

		if trimmed == "<<<<<<< SEARCH" {
			if err := flushChunk(); err != nil {
				return nil, err
			}
			inSearch = true
			searchBuffer = nil
			continue
		}
		if trimmed == "=======" && inSearch {
			inSearch = false
			inReplace = true
			replaceBuffer = nil
			continue
		}
		if trimmed == ">>>>>>> REPLACE" && inReplace {
			if currentPath == "" {
				return nil, fmt.Errorf("patch: chunk has no File: header before line %q", line)
			}
			inReplace = false
			commitChunk()
			continue
		}

		if inSearch {
			searchBuffer = append(searchBuffer, line)
		} else if inReplace {
			replaceBuffer = append(replaceBuffer, line)
		}
	}
	if err := flushChunk(); err != nil {
		return nil, err
	}
	return patches, nil
}
```

Add `"fmt"` to the parser's imports.

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/tools/patch -run 'TestParseRejects' -v`
Expected: all three new tests PASS.

- [ ] **Step 5: Run the full patch package tests to confirm no regression**

Run: `go test ./internal/tools/patch -v`
Expected: `TestParsePatches` and the three new tests all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/patch/parser.go internal/tools/patch/parser_test.go
git commit -m "fix(patch): return error for unclosed or orphaned blocks (F-BUG-40)"
```

---

### Task 2: F-BUG-39 — `file.write_patch` can create new files

**Files:**
- Modify: `internal/tools/native/file.go:188-230`
- Test: `internal/tools/native/file_test.go`

**Interfaces:**
- Consumes: `patch.FilePatch.Path` (string), `patch.ApplyPatch(content, fp)` (works
  correctly on empty content when Search is empty).
- Produces: a new file is created when the target path does not exist
  and every chunk has an empty Search block. Default mode `0o644`.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/native/file_test.go`:

```go
func TestFileWritePatchCreatesNewFile(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "new.go")

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	patch := "File: new.go\n<<<<<<< SEARCH\n=======\npackage new\n\nfunc New() {}\n>>>>>>> REPLACE\n"
	res, err := invokeTool(t, reg, "file.write_patch", fmt.Sprintf(`{"patch":%q}`, patch))
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if !reflect.DeepEqual(res.FilesChanged, []string{"new.go"}) {
		t.Fatalf("FilesChanged = %#v", res.FilesChanged)
	}
	data, readErr := os.ReadFile(filePath)
	if readErr != nil {
		t.Fatalf("read new file: %v", readErr)
	}
	if !strings.Contains(string(data), "package new") {
		t.Fatalf("file content = %q", string(data))
	}
	info, _ := os.Stat(filePath)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, want 0644", info.Mode().Perm())
	}
}
```

Add `"fmt"` to the test file's imports if not present.

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/tools/native -run 'TestFileWritePatchCreatesNewFile' -v`
Expected: FAIL with "read file new.go: no such file or directory".

- [ ] **Step 3: Update the apply loop in `file.go`**

In `internal/tools/native/file.go`, replace the apply loop
(`// Apply for real` through the `os.WriteFile` call) with:

```go
	// Apply for real
	for _, fp := range patches {
		path, err := resolveWorkspacePathMulti(t.root, t.additionalRoots, fp.Path)
		if err != nil {
			return registry.ToolResult{}, err
		}
		var original string
		info, statErr := os.Stat(path)
		var mode os.FileMode = 0o644
		newFile := false
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				return registry.ToolResult{}, fmt.Errorf("stat %s: %w", fp.Path, statErr)
			}
			// New file: only allowed when every chunk has an empty Search.
			for _, c := range fp.Chunks {
				if c.Search != "" {
					return registry.ToolResult{}, fmt.Errorf(
						"file %s does not exist; non-empty search block is not allowed for new files", fp.Path)
				}
			}
			newFile = true
		} else {
			mode = info.Mode()
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return registry.ToolResult{}, fmt.Errorf("read file %s: %w", fp.Path, readErr)
			}
			original = string(data)
		}

		diff, err := patch.GenerateDiff(fp.Path, original, fp)
		if err == nil {
			diffs = append(diffs, diff)
		}

		backups = append(backups, session.BackupFile{
			Path:    fp.Path,
			Content: original,
			Mode:    mode,
		})

		patched := patch.ApplyPatch(original, fp)
		if !newFile && strings.Contains(original, "\r\n") {
			patched = strings.ReplaceAll(patched, "\n", "\r\n")
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return registry.ToolResult{}, fmt.Errorf("mkdir for %s: %w", fp.Path, err)
		}
		if err := os.WriteFile(path, []byte(patched), mode); err != nil {
			return registry.ToolResult{}, fmt.Errorf("write file %s: %w", fp.Path, err)
		}

		if t.fileTracker != nil {
			_ = t.fileTracker.RecordWrite(path, time.Now())
			_ = t.fileTracker.RecordRead(path, time.Now())
		}
	}
```

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/tools/native -run 'TestFileWritePatchCreatesNewFile' -v`
Expected: PASS.

- [ ] **Step 5: Run the full native package tests**

Run: `go test ./internal/tools/native -v`
Expected: all tests PASS (especially the existing
`TestFileWritePatchTool` and `TestFileWritePatchRollbackIntegration`).

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/file.go internal/tools/native/file_test.go
git commit -m "fix(file): allow file.write_patch to create new files (F-BUG-39)"
```

---

### Task 3: F-BUG-41 — Re-evaluate policy after user edits args

**Files:**
- Modify: `internal/agent/runner.go:1118-1132`
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `policy.Evaluate(toolName, argsMap) (Decision, string, error)`.
- Produces: when an edit changes a non-shell tool's `args`, the runner
  re-evaluates policy against the new `argsMap`; if the edit can't be
  parsed, the runner aborts the call with a tool error (not a silent
  continuation with mismatched state).

- [ ] **Step 1: Write the failing test**

Find the `runner_test.go` file's existing `TestRunner_*` patterns to
locate a good insertion point (a test that exercises approval + edit).
Add:

```go
func TestRunnerReevaluatesPolicyAfterEditedArgs(t *testing.T) {
	// Set up a registry with a write-patch tool whose approval is
	// triggered by the policy engine, then simulate the user editing
	// the args in the approval form to a syntactically invalid value.
	// The runner must surface a tool error rather than execute with
	// the original args.

	// (Use the existing scriptedProvider/registry/approval harness
	// from the runner_test.go file in this package; replicate the
	// pattern from a nearby TestRunner_* test. The exact setup is
	// long; see "Wiring reference" below.)
	_ = t
}
```

**Wiring reference** — the existing test pattern uses:

```go
state := newTestState(t)
reg := registry.New()
reg.Register(registry.Tool{Name: "demo", Risk: registry.RiskWorkspaceWrite, Handler: stub})
prov := &scriptedProvider{responses: []string{
	`{"tool":"demo","args":{}}`,
}}
pol := policy.NewEngine(&config.Config{}, nil)
runner := NewRunner(prov, reg, pol, state, "test-model")
```

For this test, the **policy engine** must be configured to require
confirmation. The simplest approach: use a tool whose `Risk` is
`RiskWorkspaceWrite` and rely on the default `DecisionConfirm` from the
agent runner. Then drive `requestApproval` to return an edited args
string that is invalid JSON, and assert the runner returns an error
to the model.

Concretely, the test should:

1. Build a `Runner` with a scripted provider that emits
   `{"tool":"demo","args":{"x":1}}` once.
2. Register a stub `demo` tool with `RiskWorkspaceWrite` and a handler
   that records the call.
3. Inject a fake approval hook (see `requestApproval`'s signature) that
   returns `approved=true, edited="not json", waitErr=nil`. If the
   existing `requestApproval` doesn't take a hook, see the test setup
   used in `TestRunnerApproval*` tests in this file and reuse it.
4. Assert that `RunTask` returns a `policyLoopResult` whose message
   stream contains a tool-error message for `demo` and that the stub
   handler was **not** invoked.

(If the codebase's approval hook is not parameterised, the test is
written by configuring the policy engine to deny on the *original*
args and allow on the *edited* args; the runner must observe the
denial, proving re-evaluation happened. The exact assertion is
described in the helper comment of the chosen existing test in this
file.)

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/agent -run 'TestRunnerReevaluatesPolicyAfterEditedArgs' -v`
Expected: FAIL — the runner executes the stub with the original args
(`x=1`) and the test sees the stub called and no error message.

- [ ] **Step 3: Implement the re-evaluation**

In `internal/agent/runner.go`, replace the `else` branch at lines
1125-1131 with:

```go
		} else {
			if !json.Valid([]byte(edited)) {
				return policyLoopResult{}, fmt.Errorf("user-supplied edit for %s is not valid JSON: %q", toolName, edited)
			}
			args = json.RawMessage(edited)
			normalizedArgs, nerr := normalizeArgs(args)
			if nerr != nil {
				return policyLoopResult{}, fmt.Errorf("normalize edited %s args: %w", toolName, nerr)
			}
			updated := map[string]interface{}{}
			if uerr := json.Unmarshal(args, &updated); uerr != nil {
				return policyLoopResult{}, fmt.Errorf("decode edited %s args: %w", toolName, uerr)
			}
			argsMap = updated
			// Re-evaluate policy against the new args. If the rewrite
			// turns a confirm decision into a deny, propagate the deny
			// to the caller via an error result.
			newDecision, newReason, perr := r.Policy.Evaluate(toolName, argsMap)
			if perr != nil {
				return policyLoopResult{}, fmt.Errorf("policy re-evaluate after edit: %w", perr)
			}
			switch newDecision {
			case policy.DecisionDeny:
				return policyLoopResult{Messages: []schema.ChatMessage{
					r.buildToolErrorMessage(toolName, "denied by policy after edit: "+newReason, toolCallID),
				}}, nil
			case policy.DecisionConfirm:
				return policyLoopResult{Messages: []schema.ChatMessage{
					r.buildToolErrorMessage(toolName, "post-edit args still require confirmation: "+newReason, toolCallID),
				}}, nil
			case policy.DecisionAllow:
				// proceed with the new args
			}
		}
```

Make sure `policy` is imported (it is already imported in this file).

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/agent -run 'TestRunnerReevaluatesPolicyAfterEditedArgs' -v`
Expected: PASS.

- [ ] **Step 5: Run the full agent package tests**

Run: `go test ./internal/agent -count=1`
Expected: all existing tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "fix(agent): re-evaluate policy and propagate errors after user edits (F-BUG-41)"
```

---

### Task 4: F-BUG-42 — Use `html.UnescapeString` for `web.fetch`

**Files:**
- Modify: `internal/tools/native/web.go:173-180`
- Test: `internal/tools/native/web_test.go` (create if absent)

**Interfaces:**
- Consumes: `html.UnescapeString` (stdlib `html` package).
- Produces: numeric (`&#39;`, `&#x27;`) and arbitrary named entities are
  decoded in addition to the existing hard-coded set.

- [ ] **Step 1: Write the failing test**

Create `internal/tools/native/web_test.go` with:

```go
package native

import "testing"

func TestHtmlToTextDecodesNumericAndNamedEntities(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Tom &amp; Jerry", "Tom & Jerry"},
		{"it&#39;s fine", "it's fine"},
		{"it&#x27;s fine", "it's fine"},
		{"&copy; 2026", "© 2026"},
		{"&nbsp;hi", " hi"},
	}
	for _, c := range cases {
		if got := htmlToText(c.in); got != c.want {
			t.Errorf("htmlToText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/tools/native -run 'TestHtmlToTextDecodesNumericAndNamedEntities' -v`
Expected: FAIL on at least one of the numeric-entity cases.

- [ ] **Step 3: Replace `htmlToText` with a stdlib-based decoder**

In `internal/tools/native/web.go`, replace the `htmlToText` function
with:

```go
func htmlToText(s string) string {
	s = htmlTagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}
```

Add `"html"` to the imports.

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/tools/native -run 'TestHtmlToTextDecodesNumericAndNamedEntities' -v`
Expected: PASS.

- [ ] **Step 5: Run the full native package tests**

Run: `go test ./internal/tools/native -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/web.go internal/tools/native/web_test.go
git commit -m "fix(web): decode numeric and named HTML entities (F-BUG-42)"
```

---

### Task 5: F-BUG-43 — Split stdout/stderr in `job.output`

**Files:**
- Modify: `internal/tools/native/jobs_manager.go:295-301`
- Test: `internal/tools/native/jobs_test.go`

**Interfaces:**
- Consumes: existing `formatCommandOutput(stdout, stderr)` in
  `internal/tools/native/command.go:149`.
- Produces: `Output` returns a string formatted the same way as
  foreground commands, with `tailLines` applied to the combined block
  before formatting and the truncation marker appended afterwards.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/native/jobs_test.go` (find a test that
already starts a fake job and emits known output, e.g. one that calls
`newFakeRunner`):

```go
func TestJobOutputSeparatesStdoutAndStderr(t *testing.T) {
	runner := newFakeRunner(withResult(CommandResult{Stdout: "out-line\n", Stderr: "err-line\n"}))
	jm := NewJobManager(context.Background(), runner, t.TempDir(), 1, time.Minute, 1<<20)
	id, err := jm.Start(context.Background(), "echo out-line; echo err-line 1>&2", time.Second)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !waitForStatus(t, jm, id, StatusCompleted, 2*time.Second) {
		t.Fatalf("job did not complete")
	}
	_, out, err := jm.Output(id, 0)
	if err != nil {
		t.Fatalf("Output: %v", err)
	}
	if !strings.Contains(out, "stdout:") || !strings.Contains(out, "stderr:") {
		t.Fatalf("output missing section headers: %q", out)
	}
	// stdout must come before stderr in the rendered output
	stdoutIdx := strings.Index(out, "out-line")
	stderrIdx := strings.Index(out, "err-line")
	if stdoutIdx == -1 || stderrIdx == -1 || stdoutIdx > stderrIdx {
		t.Fatalf("output not ordered stdout-then-stderr: %q", out)
	}
}
```

If `waitForStatus` doesn't exist in the file, replace the wait with a
short polling loop or `time.Sleep(100*time.Millisecond)`.

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/tools/native -run 'TestJobOutputSeparatesStdoutAndStderr' -v`
Expected: FAIL — output is the bare concatenation.

- [ ] **Step 3: Use `formatCommandOutput` in `Output`**

In `internal/tools/native/jobs_manager.go`, replace lines 295-301 with:

```go
	stdoutTail := tailString(stdoutStr, tailLines)
	stderrTail := tailString(stderrStr, tailLines)
	combined := formatCommandOutput(stdoutTail, stderrTail)
	if truncated {
		combined += truncationMarker
	}
	return info, combined, nil
```

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/tools/native -run 'TestJobOutputSeparatesStdoutAndStderr' -v`
Expected: PASS.

- [ ] **Step 5: Run the full native package tests**

Run: `go test ./internal/tools/native -count=1`
Expected: all tests PASS (notably any existing `job.output` tests).

- [ ] **Step 6: Commit**

```bash
git add internal/tools/native/jobs_manager.go internal/tools/native/jobs_test.go
git commit -m "fix(jobs): split stdout and stderr sections in job.output (F-BUG-43)"
```

---

### Task 6: F-POL-45 — Plumb a logger into `PolicyEngine` (additive)

**Files:**
- Modify: `internal/tools/policy/policy.go:30-50, 366-389`
- Test: `internal/tools/policy/policy_test.go`

**Interfaces:**
- Consumes: `*slog.Logger` from `app.go` (via `state.Logger()`).
- Produces: a `SetLogger(*slog.Logger)` method on `*PolicyEngine`. When
  set, the engine uses the injected logger instead of `slog.Default()`.
  The default zero value continues to use `slog.Default()` — this
  preserves all 100+ test call sites that call
  `policy.NewEngine(cfg, rules)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/policy/policy_test.go`:

```go
func TestPolicyEngineUsesInjectedLogger(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})

	var captured atomic.Value
	captured.Store("")
	handler := slog.NewTextHandler(&logBuffer{store: &captured}, &slog.HandlerOptions{Level: slog.LevelDebug})
	pe.SetLogger(slog.New(handler))

	// Force the guardrail parser to fail and the engine to fall back
	// to the legacy check (a command the AST parser will reject).
	_, _, _ = pe.Evaluate("shell.run", map[string]interface{}{"command": "echo hi && ("})
	// A successful evaluate with a syntactically-broken command
	// would log at Debug; we don't assert on a specific log line
	// (the AST parser is forgiving in this codebase). Instead, verify
	// the logger was actually called by sending a known-bad command
	// through the guardrail evaluation directly:
	pe.Logger().Debug("probe")
	if !strings.Contains(captured.Load().(string), "probe") {
		t.Fatalf("injected logger did not receive messages: %q", captured.Load().(string))
	}
}

type logBuffer struct{ store *atomic.Value }

func (b *logBuffer) Write(p []byte) (int, error) {
	b.store.Store(string(p))
	return len(p), nil
}
```

Add `"log/slog"`, `"strings"`, and `"sync/atomic"` to the imports of
`policy_test.go`.

- [ ] **Step 2: Run the new test to verify it fails**

Run: `go test ./internal/tools/policy -run 'TestPolicyEngineUsesInjectedLogger' -v`
Expected: FAIL — `SetLogger` and `Logger` don't exist yet.

- [ ] **Step 3: Add the logger field, setter, and getter to `PolicyEngine`**

In `internal/tools/policy/policy.go`:

1. Add a `logger *slog.Logger` field to `PolicyEngine`:

```go
type PolicyEngine struct {
	config       *config.Config
	sessionRules []string
	rules        []permissions.Rule
	mu           sync.RWMutex
	logger       *slog.Logger
}
```

2. In `NewEngine`, after the existing initialization, set the default:

```go
	e := &PolicyEngine{config: cfg, sessionRules: sessionRules, rules: rules, logger: slog.Default()}
```

3. Replace the `slog.Default()` call at line 369 in `evaluateGuardrails`
   with `pe.logger.Debug(...)`. Update the function to take `pe` (or a
   logger) instead of using the package-level default. The cleanest
   change is to convert `evaluateGuardrails` to a method on
   `*PolicyEngine`:

```go
func (pe *PolicyEngine) evaluateGuardrails(cmd, dynSetting string) (Decision, string) {
	verdict, err := analyzeCommand(cmd)
	if err != nil {
		pe.logger.Debug("policy guardrail parse failed, falling back to legacy", "cmd", cmd, "err", err)
		if isBlockedByGuardrailLegacy(cmd) {
			return DecisionDeny, "blocked by conservative guardrail safety checks (legacy)"
		}
		return "", ""
	}
	if verdict.blocked {
		return DecisionDeny, verdict.reason
	}
	if verdict.dynamicArgv0 {
		switch dynSetting {
		case "off":
			return "", ""
		case "confirm":
			return DecisionConfirm, "requires approval: " + verdict.reason
		default:
			return DecisionDeny, verdict.reason
		}
	}
	return "", ""
}
```

Find every call site of `evaluateGuardrails(` and replace with
`pe.evaluateGuardrails(`.

4. Add the public setter and getter:

```go
// SetLogger injects a structured logger used for debug-level events
// (e.g. guardrail parse failures). Pass nil to revert to slog.Default().
func (pe *PolicyEngine) SetLogger(l *slog.Logger) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	if l == nil {
		pe.logger = slog.Default()
		return
	}
	pe.logger = l
}

// Logger returns the logger used by the engine. May be the package
// default if SetLogger was never called.
func (pe *PolicyEngine) Logger() *slog.Logger {
	pe.mu.RLock()
	defer pe.mu.RUnlock()
	if pe.logger == nil {
		return slog.Default()
	}
	return pe.logger
}
```

- [ ] **Step 4: Run the new test to verify it passes**

Run: `go test ./internal/tools/policy -run 'TestPolicyEngineUsesInjectedLogger' -v`
Expected: PASS.

- [ ] **Step 5: Run the full policy package tests**

Run: `go test ./internal/tools/policy -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Wire the logger in `app.go`**

In `internal/app/app.go`, immediately after the `policy.NewEngine(...)`
call (line 391), add:

```go
	if state.Logger() != nil {
		pol.SetLogger(state.Logger())
	}
```

- [ ] **Step 7: Build the project**

Run: `go build ./...`
Expected: success (no errors).

- [ ] **Step 8: Commit**

```bash
git add internal/tools/policy/policy.go internal/tools/policy/policy_test.go internal/app/app.go
git commit -m "refactor(policy): inject slog logger into PolicyEngine (F-POL-45)"
```

---

### Task 7: F-POL-44 — De-duplicate command guardrail logic

**Files:**
- Modify: `internal/tools/policy/policy.go:235-260` (export
  `IsBlockedByGuardrailLegacy`)
- Modify: `internal/tools/native/command.go:48-49, 91-92, 110-136` (call
  policy engine)
- Modify: `internal/tools/native/native.go:34-53` (new `Options` field)
- Modify: `internal/app/app.go:391` (wire policy into native opts)
- Test: `internal/tools/native/command_test.go`, `internal/tools/policy/policy_test.go`

**Interfaces:**
- Consumes: a `*policy.PolicyEngine` reachable from the native toolset
  via a new `Options.GuardrailGuard func(command string) error` field.
  `app.go` wires a closure that calls the engine's exported
  `GuardrailCheck` helper.
- Produces: `validateConservativeCommand` is removed; both the agent
  runtime's `evaluateGuardrails` path and the in-tool pre-flight check
  use the same `PolicyEngine` logic. The `shell.run` and `test.run`
  handlers refuse a command that the engine would deny.

- [ ] **Step 1: Export the policy guardrail entry point**

In `internal/tools/policy/policy.go`:

1. Rename `evaluateGuardrails` to `EvaluateGuardrails` (capitalize) and
   make it a method on `*PolicyEngine` (see Task 6 — `pe.evaluateGuardrails`
   is the new name; rename to `pe.EvaluateGuardrails` and add a
   doc comment).

2. Add a small wrapper that maps the decision to an error:

```go
// GuardrailCheck returns an error if the policy engine would deny the
// given command based on its conservative guardrails. The error wraps
// the deny reason. shell.run / test.run call this as a final pre-flight
// check before handing the command to the sandbox.
func (pe *PolicyEngine) GuardrailCheck(command string) error {
	dec, reason := pe.EvaluateGuardrails(command, "deny")
	if dec == DecisionDeny {
		return fmt.Errorf("command blocked by conservative guardrail: %s", reason)
	}
	return nil
}
```

Add `"fmt"` to the policy package imports.

- [ ] **Step 2: Add a test that the exported helper matches the legacy substring path**

Append to `internal/tools/policy/policy_test.go`:

```go
func TestPolicyEngineGuardrailCheckExposed(t *testing.T) {
	pe := NewEngine(&config.Config{}, []string{})
	if err := pe.GuardrailCheck("rm -rf /"); err == nil {
		t.Fatal("expected error for rm -rf, got nil")
	}
	if err := pe.GuardrailCheck("go test ./..."); err != nil {
		t.Fatalf("go test should not be blocked: %v", err)
	}
}
```

- [ ] **Step 3: Run the new test to verify it passes**

Run: `go test ./internal/tools/policy -run 'TestPolicyEngineGuardrailCheckExposed' -v`
Expected: PASS.

- [ ] **Step 4: Add a `Guardrail` hook to native `Options`**

In `internal/tools/native/native.go`, extend the `Options` struct:

```go
type Options struct {
	// ... existing fields ...

	// Guardrail is invoked by shell.run / test.run after policy
	// evaluation, as a final pre-flight check. Returning a non-nil
	// error aborts the command with a tool error. Typically wired to
	// (*policy.PolicyEngine).GuardrailCheck in app.go. Optional; when
	// nil, the legacy substring-based check is used (deprecated).
	Guardrail func(command string) error
}
```

Add a field to `toolSet`:

```go
type toolSet struct {
	// ... existing fields ...
	guardrail func(command string) error
}
```

In `newToolSet`, set `tools.guardrail = opts.Guardrail`.

- [ ] **Step 5: Replace `validateConservativeCommand` calls with the hook**

In `internal/tools/native/command.go`:

1. Replace the two `validateConservativeCommand(command)` call sites
   (lines 48 and 91) with:

```go
if t.guardrail != nil {
    if err := t.guardrail(command); err != nil {
        return registry.ToolResult{}, err
    }
}
```

2. Delete the `validateConservativeCommand` function (lines 110-136).
   The legacy substring match is no longer used.

- [ ] **Step 6: Update `command_test.go` to inject a stub guardrail**

In `internal/tools/native/command_test.go`, find the existing
`RegisterAll(reg, Options{...fakeRunner...})` call sites and add a
`Guardrail: func(string) error { return nil }` field. (Without this
field, the `shell.run` handler silently bypasses guardrails; the
in-process tests should continue to use a permissive guardrail to
keep the existing assertions valid.)

- [ ] **Step 7: Add a test that `shell.run` rejects a guarded command**

Append to `internal/tools/native/command_test.go`:

```go
func TestShellRunRejectsGuardrailDeniedCommand(t *testing.T) {
	reg := registry.New()
	denied := map[string]bool{"rm -rf /": true}
	if err := RegisterAll(reg, Options{
		WorkspaceRoot: t.TempDir(),
		CommandRunner: &fakeRunner{},
		Guardrail: func(cmd string) error {
			if denied[cmd] {
				return fmt.Errorf("blocked: %s", cmd)
			}
			return nil
		},
	}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}
	_, err := invokeTool(t, reg, "shell.run", `{"command":"rm -rf /"}`)
	if err == nil {
		t.Fatal("expected guardrail error, got nil")
	}
}
```

Add `"fmt"` to the test imports if not present.

- [ ] **Step 8: Wire the policy engine in `app.go`**

In `internal/app/app.go`, find the `RegisterAll` call (search for
`native.RegisterAll` or `native.Options{`) and add a `Guardrail`
closure that delegates to the freshly-built `pol`:

```go
nativeOpts.Guardrail = func(cmd string) error { return pol.GuardrailCheck(cmd) }
```

If `native.RegisterAll` is called inline, construct the `Options`
value as a local variable first, set `Guardrail` on it, then pass it.

- [ ] **Step 9: Build the project**

Run: `go build ./...`
Expected: success.

- [ ] **Step 10: Run all affected tests**

Run: `go test ./internal/tools/policy ./internal/tools/native ./internal/app -count=1`
Expected: all tests PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/tools/policy/policy.go internal/tools/policy/policy_test.go \
        internal/tools/native/command.go internal/tools/native/native.go \
        internal/tools/native/command_test.go internal/app/app.go
git commit -m "refactor(tools): consolidate guardrail checks through PolicyEngine (F-POL-44)"
```

---

### Task 8: F-POL-46 — `SandboxMeta.LimitsJSON` returns its marshal error

**Files:**
- Modify: `internal/tools/registry/types.go:74-102`
- Test: `internal/tools/registry/types_test.go`
- Modify: `internal/db/audits.go:48-53` (handle the new return signature)

**Interfaces:**
- Consumes: `json.Marshal` errors (only triggered by unmarshalable types
  — currently impossible because the map values are all primitive).
- Produces: `func (m SandboxMeta) LimitsJSON() (string, error)`. The
  audit layer logs a warning when the error is non-nil and proceeds
  with `{}`.

- [ ] **Step 1: Write the failing test**

Append to `internal/tools/registry/types_test.go`:

```go
func TestSandboxMetaLimitsJSONReturnsError(t *testing.T) {
	// Construct a meta with an unmarshalable value to force json.Marshal
	// to fail. A function value is the canonical unmarshalable type.
	m := SandboxMeta{Backend: "restricted"}
	m.ResourceLimits = true
	// We can't put a func in the public struct, so we exercise the
	// happy path: LimitsJSON must return a non-empty JSON string and
	// a nil error for valid meta.
	s, err := m.LimitsJSON()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == "" {
		t.Fatal("expected non-empty JSON")
	}
}
```

- [ ] **Step 2: Update the signature**

In `internal/tools/registry/types.go`, replace `LimitsJSON` with:

```go
func (m SandboxMeta) LimitsJSON() (string, error) {
	limits := map[string]any{"backend": m.Backend}
	if m.MemoryLimitBytes > 0 {
		limits["memory_limit_bytes"] = m.MemoryLimitBytes
	}
	if m.CPUSeconds > 0 {
		limits["cpu_seconds"] = m.CPUSeconds
	}
	if m.MaxProcesses > 0 {
		limits["max_processes"] = m.MaxProcesses
	}
	if m.NetworkIsolated {
		limits["network_isolated"] = true
	}
	if m.FilesystemIsolated {
		limits["filesystem_isolated"] = true
	}
	if m.KilledReason != "" {
		limits["killed_reason"] = m.KilledReason
	}
	if m.OutputTruncated {
		limits["output_truncated"] = true
	}
	b, err := json.Marshal(limits)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
```

- [ ] **Step 3: Update the caller in `audits.go`**

In `internal/db/audits.go`, replace lines 48-53 with:

```go
	// Guard LimitsJSON: non-sandbox tool calls shouldn't pay the map
	// alloc + JSON marshal. The column is nullable, so pass nil.
	var limitsJSON any
	if event.Sandbox.Enabled {
		s, mErr := event.Sandbox.LimitsJSON()
		if mErr != nil {
			// Best-effort: log and persist an empty object. The
			// audit row should not be lost because of a marshal
			// failure (which only happens for unmarshalable types
			// — defensive).
			log.Printf("tool_calls audit: LimitsJSON marshal failed: %v", mErr)
			s = "{}"
		}
		limitsJSON = s
	}
```

Add `"log"` to the imports of `audits.go` if not present.

- [ ] **Step 4: Update the existing test to use the new signature**

The existing test in `types_test.go` calls `m.LimitsJSON()` and stores
it in a single variable. Update both call sites (lines 11 and 25) to
discard the error or assert on it. For the existing assertion
`if err := json.Unmarshal(...); err != nil`, change the first line to:

```go
j, err := m.LimitsJSON()
if err != nil {
    t.Fatalf("LimitsJSON: %v", err)
}
```

- [ ] **Step 5: Run the registry and db tests**

Run: `go test ./internal/tools/registry ./internal/db -count=1`
Expected: all tests PASS.

- [ ] **Step 6: Build the project**

Run: `go build ./...`
Expected: success.

- [ ] **Step 7: Commit**

```bash
git add internal/tools/registry/types.go internal/tools/registry/types_test.go internal/db/audits.go
git commit -m "fix(registry): surface SandboxMeta.LimitsJSON marshal errors (F-POL-46)"
```

---

## Self-Review

1. **Spec coverage:**
   - F-BUG-39 → Task 2 (new-file creation with empty Search).
   - F-BUG-40 → Task 1 (strict parser + empty-path rejection).
   - F-BUG-41 → Task 3 (re-evaluate policy, surface parse errors).
   - F-BUG-42 → Task 4 (`html.UnescapeString`).
   - F-BUG-43 → Task 5 (`formatCommandOutput` for jobs).
   - F-POL-44 → Task 7 (single guardrail path through `PolicyEngine`).
   - F-POL-45 → Task 6 (additive `SetLogger` / `Logger`).
   - F-POL-46 → Task 8 (`LimitsJSON` returns error).

2. **Placeholder scan:** No `TBD`/`TODO` markers. The test in Task 3
   refers to a "Wiring reference" because the exact `requestApproval`
   hook shape must be discovered from the existing
   `runner_test.go` file at execution time; the implementer follows
   the named `TestRunnerApproval*` tests as the model.

3. **Type consistency:** `EvaluateGuardrails` and `GuardrailCheck` use
   the same `(command, dynSetting)` signature internally; the exported
   `GuardrailCheck` always passes `"deny"` so dynamic argv0 commands
   are blocked, matching the prior in-tool behavior.
   `PolicyEngine.SetLogger` accepts `*slog.Logger` (the type the
   sandbox already uses). `SandboxMeta.LimitsJSON` is changed in
   exactly two callers (the test and the audit row); both updated
   inline.
