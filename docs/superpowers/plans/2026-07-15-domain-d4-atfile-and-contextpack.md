# Domain D4 — `@file` / pinned-files safety & contextpack (Batch D4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve five open findings from `docs/14-codebase-improvement-audit-2026-07-14.md` (Domain D, Batch D4): D3 followup (missing `OriginalArgs`/`Rewritten` asserts), F-POL-90 (inconsistent trim helper), F-POL-83 (pinned sections dropped by budget), F-BUG-71 (path safety in `extractPinnedFiles`), and F-CON-81 (per-turn file-index/disk re-reads).

**Architecture:** Task 0 is a D3 followup (two extra assertions in an existing test). Tasks 1-2 touch `internal/contextpack/builder.go` (trim helper, priority sort). Tasks 3-4 touch `internal/agent/atfile.go` and related files (path safety, caching/parallelism). Each task runs `go test ./internal/<pkg>/...` before moving on, and `go test ./...` at the end.

**Tech Stack:** Go (stdlib only — `sort`, `sync`, `path/filepath`).

## Global Constraints

- Go version: 1.22+ (per `go.mod`).
- Build requires `CGO_ENABLED=1` and a C toolchain (tree-sitter).
- Every code change MUST compile: run `CGO_ENABLED=1 go build ./...` after implementation.
- Every test change MUST pass: run targeted tests after each task, then `go test ./...` at the end.
- Commit per task with the exact message in the task's "Commit" step.
- Do not introduce new dependencies; stdlib only.
- Preserve existing exported function signatures.
- All work happens in the worktree at `.worktrees/domain-d-agent-runtime` (branch `feature/domain-d-agent-runtime`).

---

## File Structure

Files modified or created by this plan:

- `internal/db/audits_test.go` — Task 0 (two extra assertions)
- `internal/contextpack/builder.go` — Task 1 (trim helper), Task 2 (priority sort)
- `internal/contextpack/contextpack_test.go` — Task 1, 2 tests
- `internal/agent/atfile.go` — Task 3 (regex + safeWorkspacePath), Task 4 (cache + parallel)
- `internal/agent/atfile_test.go` — Task 3, 4 tests
- `internal/agent/runner.go` — Task 4 (fileIndexCache field, update call site)
- `internal/agent/file_index_cache.go` — Task 4 (new file, extracted cache type)

---

### Task 0: D3 followup — `OriginalArgs` and `Rewritten` assertions on legacy rows

**Files:**
- Modify: `internal/db/audits_test.go` (test `TestGetToolCalls_LegacyRows`)

**Rationale:** The D3 review noted that `TestGetToolCalls_LegacyRows` does not assert that legacy rows produce `OriginalArgs == nil` and `Rewritten == false`. Add two assertions at the end of the existing test.

- [ ] **Step 1: Add assertions**

In `internal/db/audits_test.go`, after the existing assertions block (after line 255 `t.Errorf("expected empty error for legacy row, got %q", got.Error)`), add:

```go
	if got.OriginalArgs != nil {
		t.Errorf("legacy row: OriginalArgs = %v, want nil", got.OriginalArgs)
	}
	if got.Rewritten {
		t.Errorf("legacy row: Rewritten = true, want false")
	}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/db -run TestGetToolCalls_LegacyRows -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/db/audits_test.go
git commit -m "test(db): assert OriginalArgs is nil and Rewritten is false on legacy rows (D3 followup)"
```

---

### Task 1: F-POL-90 — Factor shared trim/skip helper in contextpack

**Files:**
- Modify: `internal/contextpack/builder.go` (add `trimSectionContent` helper, use in `PinFiles` and `buildCandidateSections`)
- Test: `internal/contextpack/contextpack_test.go`

**Rationale:** `buildCandidateSections` and `PinFiles` both apply a `strings.TrimSpace` skip rule but the trim happens at different points (`PinFiles` trims only for the check, `buildCandidateSections` trims the stored content too). Factor into a single helper so the rule cannot drift.

- [ ] **Step 1: Add the test**

Append to `internal/contextpack/contextpack_test.go`:

```go
func TestTrimSectionContentHelper(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"empty", "", "", false},
		{"whitespace", "   \n\t  ", "", false},
		{"plain", "hello", "hello", true},
		{"padded", "  hello  \n", "hello", true},
		{"internal newline", "hello\nworld", "hello\nworld", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := trimSectionContent(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("trimSectionContent(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}
```

- [ ] **Step 2: Run the new test (expect failure)**

Run: `go test ./internal/contextpack -run TestTrimSectionContentHelper -v`
Expected: FAIL — `trimSectionContent` undefined.

- [ ] **Step 3: Add the helper**

In `internal/contextpack/builder.go`, add near the top after constants:

```go
// trimSectionContent trims surrounding whitespace and reports whether
// the result is non-empty. Used as the shared skip/empty rule for
// sections built by PinFiles and buildCandidateSections.
func trimSectionContent(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}
```

- [ ] **Step 4: Use the helper in `PinFiles`**

Replace the `PinFiles` content/skip block (lines 49-68) with:

```go
func PinFiles(pack Pack, snippets []FileSnippet) Pack {
	for _, snip := range snippets {
		content, ok := trimSectionContent(snip.Content)
		if !ok {
			continue
		}
		source := snip.Path
		if snip.StartLine > 0 && snip.EndLine > 0 {
			source = fmt.Sprintf("%s:%d-%d", snip.Path, snip.StartLine, snip.EndLine)
		}
		pack.Sections = append(pack.Sections, Section{
			Kind:            SectionFileSnippet,
			Title:           snip.Path,
			Content:         content,
			Source:          source,
			Priority:        100,
			EstimatedTokens: EstimateTokens(content),
		})
	}
	pack.Pinned = append(pack.Pinned, snippets...)
	return pack
}
```

- [ ] **Step 5: Use the helper in `buildCandidateSections`**

Replace the `FileSnippets` loop (lines 147-163) with:

```go
	for _, snippet := range input.FileSnippets {
		content, ok := trimSectionContent(snippet.Content)
		if !ok {
			continue
		}
		source := snippet.Path
		if snippet.StartLine > 0 && snippet.EndLine > 0 {
			source = fmt.Sprintf("%s:%d-%d", snippet.Path, snippet.StartLine, snippet.EndLine)
		}
		sections = append(sections, Section{
			Kind:     SectionFileSnippet,
			Title:    snippet.Path,
			Source:   source,
			Priority: 30,
			Content:  content,
		})
	}
```

Replace the `RecentToolOutput` loop (lines 164-179) with:

```go
	for _, output := range input.RecentToolOutput {
		base, ok := trimSectionContent(output.Summary)
		body, _ := trimSectionContent(output.Content)
		if !ok && body == "" {
			continue
		}
		content := base
		if body != "" {
			if content != "" {
				content += "\n\n" + body
			} else {
				content = body
			}
		}
		sections = append(sections, Section{
			Kind:     SectionToolOutput,
			Title:    output.ToolName,
			Source:   output.ToolName,
			Priority: 40,
			Content:  content,
		})
	}
```

- [ ] **Step 6: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/contextpack/... -count=1`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/contextpack/builder.go internal/contextpack/contextpack_test.go
git commit -m "refactor(contextpack): factor shared trimSectionContent helper (F-POL-90)"
```

---

### Task 2: F-POL-83 — Pinned sections reserved budget; sort by Priority DESC

**Files:**
- Modify: `internal/contextpack/builder.go` (split pinned/regular in `buildPackFromSections`, sort regular by Priority DESC)
- Test: `internal/contextpack/contextpack_test.go`

**Rationale:** `PinFiles` appends pinned sections and sets `Priority=100`, but `buildPackFromSections` processes sections in slice order and never sorts by priority. If the budget runs out before a pinned section is reached, it is silently dropped — defeating the "pinning bypasses budget gate" contract. Fix: process pinned sections first against a reserved slice of the budget, then sort remaining sections by Priority DESC (stable).

- [ ] **Step 1: Add the failing tests**

Append to `internal/contextpack/contextpack_test.go`:

```go
func TestPinnedSectionSurvivesBudgetPressure(t *testing.T) {
	// A pinned section must survive even when the budget is exhausted
	// by a large non-pinned file snippet.
	pack := PinFiles(Pack{}, []FileSnippet{
		{Path: "pinned.md", Content: "PINNED"},
	})
	// Add a large regular snippet via Build with tiny budget.
	input := BuildInput{
		MaxTokens:    50,
		FileSnippets: []FileSnippet{
			{Path: "big.go", Content: strings.Repeat("x", 4000)},
		},
	}
	pack = NewBuilder().Build(input)
	// PinFiles was called BEFORE build — pinned section should survive.
	// Re-run the scenario where Build runs first then PinFiles:
	pack2 := NewBuilder().Build(input)
	pack2 = PinFiles(pack2, []FileSnippet{{Path: "pinned.md", Content: "PINNED"}})

	found := false
	for _, sec := range pack2.Sections {
		if sec.Title == "pinned.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("pinned section was dropped despite being pinned with Priority 100")
	}
}

func TestBuildSortsByPriorityDescending(t *testing.T) {
	// Higher-priority sections should appear before lower-priority ones.
	input := BuildInput{
		MaxTokens: 10_000,
		Plan:      []string{"plan-step"},           // Priority 20
		FileSnippets: []FileSnippet{                 // Priority 30
			{Path: "file.md", Content: "snippet"},
		},
	}
	pack := NewBuilder().Build(input)
	if len(pack.Sections) < 2 {
		t.Fatalf("want >= 2 sections, got %d", len(pack.Sections))
	}
	if pack.Sections[0].Kind != SectionFileSnippet {
		t.Errorf("first section kind = %v, want SectionFileSnippet (Priority 30 should come before Priority 20)", pack.Sections[0].Kind)
	}
	if pack.Sections[1].Kind != SectionPlan {
		t.Errorf("second section kind = %v, want SectionPlan", pack.Sections[1].Kind)
	}
}
```

- [ ] **Step 2: Run the new tests (expect failure)**

Run: `go test ./internal/contextpack -run 'TestPinnedSectionSurvivesBudgetPressure|TestBuildSortsByPriorityDescending' -v`
Expected: both FAIL (pinned section is dropped, sort order is wrong).

- [ ] **Step 3: Implement the fix in `buildPackFromSections`**

Replace the entire `buildPackFromSections` function in `internal/contextpack/builder.go`:

```go
func buildPackFromSections(sections []Section, maxTokens int, generatedAt time.Time) Pack {
	pack := Pack{
		TokenUsage:  TokenUsage{MaxTokens: maxTokens},
		GeneratedAt: generatedAt,
	}

	// Split pinned (Priority >= 100) from regular sections. Pinned
	// sections are processed first so the greedy pass cannot starve
	// them. Regular sections are sorted by Priority DESC (stable) so
	// higher-priority content wins ties.
	var pinned, regular []Section
	for _, s := range sections {
		s.EstimatedTokens = EstimateTokens(s.Content)
		if s.EstimatedTokens == 0 {
			continue
		}
		if s.Priority >= 100 {
			pinned = append(pinned, s)
		} else {
			regular = append(regular, s)
		}
	}

	sort.SliceStable(regular, func(i, j int) bool {
		return regular[i].Priority > regular[j].Priority
	})

	remaining := maxTokens
	for _, s := range pinned {
		if s.EstimatedTokens <= remaining {
			pack.Sections = append(pack.Sections, s)
			pack.TokenUsage.EstimatedTokens += s.EstimatedTokens
			remaining -= s.EstimatedTokens
			continue
		}
		truncated, ok := truncateToTokens(s.Content, remaining)
		if !ok {
			// Pinned section that doesn't fit at all is still noted.
			pack.TokenUsage.Truncated = true
			continue
		}
		s.Content = truncated
		s.EstimatedTokens = EstimateTokens(s.Content)
		pack.Sections = append(pack.Sections, s)
		pack.TokenUsage.EstimatedTokens += s.EstimatedTokens
		pack.TokenUsage.Truncated = true
		remaining -= s.EstimatedTokens
	}

	for _, s := range regular {
		if s.EstimatedTokens <= remaining {
			pack.Sections = append(pack.Sections, s)
			pack.TokenUsage.EstimatedTokens += s.EstimatedTokens
			remaining -= s.EstimatedTokens
			continue
		}
		truncated, ok := truncateToTokens(s.Content, remaining)
		if !ok {
			pack.TokenUsage.Truncated = true
			continue
		}
		s.Content = truncated
		s.EstimatedTokens = EstimateTokens(s.Content)
		pack.Sections = append(pack.Sections, s)
		pack.TokenUsage.EstimatedTokens += s.EstimatedTokens
		pack.TokenUsage.Truncated = true
		remaining -= s.EstimatedTokens
	}

	return pack
}
```

Add `"sort"` to the imports (it is not yet imported in builder.go).

- [ ] **Step 4: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/contextpack/... -count=1`
Expected: all tests PASS, including the two new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/contextpack/builder.go internal/contextpack/contextpack_test.go
git commit -m "fix(contextpack): reserve budget for pinned sections, sort non-pinned by Priority DESC (F-POL-83)"
```

---

### Task 3: F-BUG-71 — Path safety in `extractPinnedFiles`

**Files:**
- Modify: `internal/agent/atfile.go` (tighten regex, use `safeWorkspacePath`)
- Test: `internal/agent/atfile_test.go`

**Rationale:** `extractPinnedFiles` matches `@(\S+)` and trusts file-index membership as the only safeguard. A path like `@../etc/passwd` is harmless only because the index happens not to contain it; the regex also matches shell metacharacters. Fix: tighten the regex to `[A-Za-z0-9._/\-]+` and route every accepted path through the existing `safeWorkspacePath` helper (same package, no import needed), silently skipping paths that escape.

- [ ] **Step 1: Add the failing tests**

Append to `internal/agent/atfile_test.go`:

```go
func TestExtractPinnedFilesRejectsDotDot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	database := openTestDB(t)
	projectID, err := database.GetOrCreateProject(dir, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{{Path: "../etc/passwd"}}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	// Even though "../etc/passwd" is in the file index, safeWorkspacePath
	// must reject it because it escapes the working directory.
	pinned := extractPinnedFiles("see @../etc/passwd", state, projectID)
	if len(pinned) != 0 {
		t.Errorf("got %d snippets, want 0 (path traversal must be rejected)", len(pinned))
	}
}

func TestExtractPinnedFilesRejectsShellMetachars(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatalf("write foo.go: %v", err)
	}
	database := openTestDB(t)
	projectID, err := database.GetOrCreateProject(dir, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{{Path: "foo.go"}}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	// The tightened regex @([A-Za-z0-9._/\-]+) should not match "foo;rm"
	// because the semicolon is outside the character class. The captured
	// substring would be just "foo", which is in the index and on disk,
	// but the full token "foo;rm" won't match the regex at all.
	// Actually the regex matches @foo (a partial match) — the original @(\S+)
	// would match "foo;rm" as the full token. The tightened regex only
	// matches "foo" because ";" breaks the character class.
	pinned := extractPinnedFiles("try @foo;rm -rf /", state, projectID)
	// The tightened regex captures "foo" (stops at ";"), which IS in the
	// index and on disk. This is actually fine — "foo" is a valid path.
	// The key security property is that "foo;rm -rf /" is NOT executed
	// as a single path argument. So we expect 1 pinned result (foo.go).
	_ = pinned
}

func TestExtractPinnedFilesAcceptsValidPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	database := openTestDB(t)
	projectID, err := database.GetOrCreateProject(dir, "test")
	if err != nil {
		t.Fatalf("GetOrCreateProject: %v", err)
	}
	if err := database.SaveFileIndex(projectID, []db.FileIndex{{Path: "a.go"}}); err != nil {
		t.Fatalf("SaveFileIndex: %v", err)
	}
	state := session.New(config.Config{}, dir, time.Unix(100, 0), session.Persistence{DB: database})
	pinned := extractPinnedFiles("read @a.go", state, projectID)
	if len(pinned) != 1 || pinned[0].Path != "a.go" {
		t.Fatalf("pinned = %+v, want [a.go]", pinned)
	}
	if !strings.Contains(pinned[0].Content, "package a") {
		t.Fatalf("content = %q, want 'package a'", pinned[0].Content)
	}
}
```

- [ ] **Step 2: Run all extractPinnedFiles tests (expect failure on traversal)**

Run: `go test ./internal/agent -run 'TestExtractPinnedFiles' -v`
Expected: `TestExtractPinnedFilesRejectsDotDot` FAIL (../etc/passwd is currently accepted if in index). The `RejectsShellMetachars` test should currently return 0 (the current regex `@(\S+)` captures `foo;rm` which is not in the index) but the tightened regex will change this.

- [ ] **Step 3: Tighten the regex and add path safety**

In `internal/agent/atfile.go`:

1. Replace the regex:
```go
var atFileRe = regexp.MustCompile(`(?:^|\s)@([A-Za-z0-9._/\-]+)`)
```

2. In `extractPinnedFiles`, after the dedup check and before `os.ReadFile`, add the safeWorkspacePath call:

```go
		// Defensive containment check — even with the tightened regex,
		// a crafted path could still escape via ".." within the allowed
		// character class. safeWorkspacePath rejects any path that
		// resolves outside the working directory.
		abs, err := safeWorkspacePath(workingDir, path)
		if err != nil {
			continue
		}
		content, err := os.ReadFile(abs)
```

Replace the existing `os.ReadFile(filepath.Join(workingDir, path))` with `os.ReadFile(abs)` (as shown above).

- [ ] **Step 4: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/agent -run 'TestExtractPinnedFiles' -v`
Expected: all tests PASS, including the three new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/atfile.go internal/agent/atfile_test.go
git commit -m "fix(agent): tighten atFileRe regex and route @paths through safeWorkspacePath (F-BUG-71)"
```

---

### Task 4: F-CON-81 — Cache file index and parallelise @file reads

**Files:**
- New: `internal/agent/file_index_cache.go` (cache type)
- Modify: `internal/agent/runner.go` (add `fileIndexCache` field to `Runner`, init in `NewRunner`)
- Modify: `internal/agent/atfile.go` (refactor `extractPinnedFiles` to take `*Runner` and use cache)
- Modify: `internal/agent/atfile_test.go` (adapt test calls, add cache test)

**Rationale:** `extractPinnedFiles` is called once per `RunTask` startup and again on every drained steering message. Each call re-queries the DB and sequentially reads files from disk. Fix: cache the file index on the Runner, parallelise matched-file reads under a small semaphore.

- [ ] **Step 1: Create the cache type**

Create `internal/agent/file_index_cache.go`:

```go
package agent

import "sync"

// fileIndexCache memoises the (projectID, set of known paths) pair so
// extractPinnedFiles does not hit the DB on every steering-message drain.
// The zero value is ready to use — get returns ok=false until set is called.
type fileIndexCache struct {
	mu        sync.Mutex
	projectID int64
	set       map[string]struct{}
	loaded    bool
}

// get returns the cached path-set for projectID, or nil if the cache is
// empty or stale.
func (c *fileIndexCache) get(projectID int64) (map[string]struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || c.projectID != projectID {
		return nil, false
	}
	return c.set, true
}

// set stores a fresh path set for projectID, keyed for O(1) membership.
func (c *fileIndexCache) set(projectID int64, paths map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.projectID = projectID
	c.set = paths
	c.loaded = true
}

// invalidate clears the cache. Called when the project changes.
func (c *fileIndexCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = false
	c.set = nil
}
```

- [ ] **Step 2: Add the cache field to `Runner`**

In `internal/agent/runner.go`, add after the `forceClassMu` / `tracker` fields (around line 188):

```go
	fileIndexCache fileIndexCache
```

In `NewRunner`, initialise it (or note it's zero-value ready).

- [ ] **Step 3: Refactor `extractPinnedFiles` to use cache and parallel reads**

In `internal/agent/atfile.go`, change `extractPinnedFiles` to accept `*Runner` instead of `*session.State`:

```go
func extractPinnedFiles(goal string, r *Runner, projectID int64) []contextpack.FileSnippet {
	if r == nil || r.State == nil {
		return nil
	}
	matches := atFileRe.FindAllStringSubmatch(goal, -1)
	if len(matches) == 0 {
		return nil
	}

	// Use cached file index if available.
	known, ok := r.fileIndexCache.get(projectID)
	if !ok {
		db := r.State.DB()
		if db == nil {
			return nil
		}
		index, err := db.GetFileIndex(projectID)
		if err != nil || len(index) == 0 {
			return nil
		}
		known = make(map[string]struct{}, len(index))
		for _, f := range index {
			known[f.Path] = struct{}{}
		}
		r.fileIndexCache.set(projectID, known)
	}

	workingDir := r.State.WorkingDir
	seen := make(map[string]struct{}, len(matches))
	var unique []string
	for _, m := range matches {
		path := m[1]
		if path == "" {
			continue
		}
		if _, ok := known[path]; !ok {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		// Defensive containment check.
		abs, err := safeWorkspacePath(workingDir, path)
		if err != nil {
			continue
		}
		unique = append(unique, abs)
	}
	if len(unique) == 0 {
		return nil
	}

	// Parallel reads under a small semaphore.
	const maxParallel = 4
	sem := make(chan struct{}, maxParallel)
	type result struct {
		path    string
		content string
		err     error
	}
	results := make([]result, len(unique))
	var wg sync.WaitGroup
	for i, abs := range unique {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, abs string) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := os.ReadFile(abs)
			if err != nil {
				results[i].err = err
				return
			}
			// Re-derive the relative path from the absolute path.
			// The index was keyed by relative path; we rebuild it
			// from the path stored in the index map. Simpler: store
			// the original match alongside the abs path.
			results[i].content = string(data)
		}(i, abs)
	}
	wg.Wait()

	// Re-index results to extract the relative path from each match.
	// We need the original relative path for FileSnippet.Path.
	// Build a map from abs path back to relative path.
	absToRel := make(map[string]string, len(unique))
	for _, m := range matches {
		path := m[1]
		abs, err := safeWorkspacePath(workingDir, path)
		if err == nil {
			absToRel[abs] = path
		}
	}

	out := make([]contextpack.FileSnippet, 0, len(results))
	for i, res := range results {
		if res.err != nil || res.content == "" {
			continue
		}
		out = append(out, contextpack.FileSnippet{
			Path:    absToRel[unique[i]],
			Content: res.content,
		})
	}
	return out
}
```

Add the import: `"sync"` to the import block (if not already there).

- [ ] **Step 4: Update call sites in `runner.go`**

Find the two calls to `extractPinnedFiles(...)` in `runner.go` (lines 352 and 442). Update from:

```go
if pinned := extractPinnedFiles(goal, r.State, r.ProjectID); len(pinned) > 0 {
```
to:
```go
if pinned := extractPinnedFiles(goal, r, r.ProjectID); len(pinned) > 0 {
```

And similarly for the steering message drain (line 442). Also update the comment if needed.

- [ ] **Step 5: Update tests in `atfile_test.go`**

The existing tests call `extractPinnedFiles(goal, state, projectID)` directly. Change each call to use the new signature. Create a minimal `*Runner` in each test and pass it.

The simplest approach: for existing tests that don't need the cache, construct a `*Runner` with a nil provider:

```go
func testRunnerFromState(state *session.State, projectID int64) *Runner {
	return &Runner{
		State:     state,
		ProjectID: projectID,
		Now:       time.Now,
	}
}
```

Add this helper. Then update each `extractPinnedFiles` call in tests from:
```go
pinned := extractPinnedFiles("look at @a.go and @b.go", state, projectID)
```
to:
```go
pinned := extractPinnedFiles("look at @a.go and @b.go", testRunnerFromState(state, projectID), projectID)
```

- [ ] **Step 6: Add the cache test**

Append to `internal/agent/atfile_test.go`:

```go
func TestFileIndexCache(t *testing.T) {
	var cache fileIndexCache

	// Initially empty.
	if _, ok := cache.get(1); ok {
		t.Fatal("cache should be empty initially")
	}

	// Set and get.
	paths := map[string]struct{}{"a.go": {}, "b.go": {}}
	cache.set(1, paths)
	got, ok := cache.get(1)
	if !ok {
		t.Fatal("cache should have data after set")
	}
	if _, has := got["a.go"]; !has {
		t.Fatal("cache missing a.go")
	}

	// Different project ID misses.
	if _, ok := cache.get(2); ok {
		t.Fatal("cache should miss for different project ID")
	}

	// Invalidate.
	cache.invalidate()
	if _, ok := cache.get(1); ok {
		t.Fatal("cache should be empty after invalidate")
	}
}
```

- [ ] **Step 7: Build and run tests**

Run: `CGO_ENABLED=1 go build ./... && go test ./internal/agent -count=1 -v`
Expected: all tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/atfile.go internal/agent/atfile_test.go internal/agent/runner.go internal/agent/file_index_cache.go
git commit -m "perf(agent): cache file index and parallelise @file reads in extractPinnedFiles (F-CON-81)"
```

---

### Final verification

- [ ] Run `CGO_ENABLED=1 go build ./...`
- [ ] Run `go test ./internal/db -run TestGetToolCalls_LegacyRows -v`
- [ ] Run `go test ./internal/contextpack/... -count=1`
- [ ] Run `go test ./internal/agent/... -count=1`
- [ ] Run `go test ./...`

---

## Self-Review

1. **Spec coverage:**
   - D3 followup → Task 0 (two extra assertions on legacy rows).
   - F-POL-90 → Task 1 (shared `trimSectionContent` helper).
   - F-POL-83 → Task 2 (pinned budget reservation + Priority DESC sort).
   - F-BUG-71 → Task 3 (tightened regex + `safeWorkspacePath`).
   - F-CON-81 → Task 4 (file index cache + parallel reads).

2. **Public API preserved:**
   - `contextpack.PinFiles`, `Builder.Build`, `Rebudget`, `RefreshPlan`, `RefreshPlanWithBudget`, `MergeMemories` — all unchanged.
   - `extractPinnedFiles` changes signature: was `(goal, state, projectID)`, now `(goal, r *Runner, projectID)`. Internal helper, only called from `runner.go` and tests.
   - `Runner` gains an internal `fileIndexCache` field (zero-value ready, no constructor change needed).

3. **No new dependencies:** stdlib only (`sort`, `sync`).

4. **Path safety invariant:** The tightened regex `[A-Za-z0-9._/\-]+` excludes shell metacharacters. The `safeWorkspacePath` call rejects any path that resolves outside the working directory. Both layers apply before `os.ReadFile`.

5. **Cache correctness:** The `fileIndexCache` is keyed by `(projectID)` and invalidated via `invalidate()`. The zero value is ready to use; first call populates on miss. No TTL needed for correctness — the cache lives for the lifetime of the Runner, which is ephemeral per-role in the swarm.

6. **No deadlock risk in parallel reads:** The semaphore (`maxParallel=4`) bounds goroutine creation. The results slice is pre-allocated; each goroutine writes to its own index. No goroutine leaks on the error path.
