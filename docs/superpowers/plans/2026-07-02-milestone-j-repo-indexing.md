# Milestone J: Repo Indexing v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a repository scanner that walks files, respects `.gitignore` and config ignore patterns, hashes files, detects languages, stores the index in SQLite, and exposes `repo.index`, `repo.map`, and `repo.card` tools.

**Architecture:** A new `internal/repo` package owns scanning, gitignore parsing, language detection, and rendering repo maps/cards. Three new read-only tools (`repo.index`, `repo.map`, `repo.card`) live in `internal/tools/native`. `repo.index` scans and persists; `repo.map` and `repo.card` read the persisted index and render human-readable summaries. The native tool set gains optional `DB` and `ProjectID` fields so repo tools can read and write the project database.

**Tech Stack:** Go 1.26.1, standard library, existing `internal/db` and `internal/tools/registry` packages.

---

## File Structure

- `internal/repo/scanner.go` — `Scanner` type and `Scan() ([]db.FileIndex, error)`.
- `internal/repo/scanner_test.go` — tests for scanning and ignore rules.
- `internal/repo/gitignore.go` — minimal `.gitignore` parser and matcher.
- `internal/repo/gitignore_test.go` — tests for gitignore matching.
- `internal/repo/language.go` — extension-to-language map and `DetectLanguage(path string) string`.
- `internal/repo/language_test.go` — tests for language detection.
- `internal/repo/map.go` — `RenderDirectoryMap(files []db.FileIndex, maxFiles int) string`.
- `internal/repo/map_test.go` — tests for directory map rendering.
- `internal/repo/card.go` — `RenderRepoCard(projectName string, files []db.FileIndex) string`.
- `internal/repo/card_test.go` — tests for repo card rendering.
- `internal/tools/native/repo_index.go` — `repo.index` tool.
- `internal/tools/native/repo_map.go` — `repo.map` tool.
- `internal/tools/native/repo_card.go` — `repo.card` tool.
- `internal/tools/native/native.go` — add `DB`/`ProjectID` to `Options`/`toolSet`, register new tools.
- `internal/tools/native/native_test.go` — update expected tool count and add tests.
- `docs/plans/task.md` — update Milestone J task statuses.

---

### Task 1: Create Repo Scanner with Config Ignore Rules

**Files:**
- Create: `internal/repo/scanner.go`
- Create: `internal/repo/scanner_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/repo/scanner_test.go`:

```go
package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScannerFindsFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme"), 0644)

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["main.go"] || !paths["README.md"] {
		t.Fatalf("missing expected files: %+v", files)
	}
}

func TestScannerSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("// js"), 0644)

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}

func TestScannerAppliesConfigIgnore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "foo_test.go"), []byte("package main"), 0644)

	scanner := NewScanner(Config{Root: dir, Ignore: []string{"*_test.go"}})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo -run 'TestScannerFindsFiles|TestScannerSkipsIgnoredDirs|TestScannerAppliesConfigIgnore' -v`
Expected: FAIL (`NewScanner`, `Config`, `Scanner.Scan` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/repo/scanner.go`:

```go
package repo

import (
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/db"
)

type Config struct {
	Root             string
	Ignore           []string
	IncludeGitignore bool
}

type Scanner struct {
	config Config
}

func NewScanner(config Config) *Scanner {
	return &Scanner{config: config}
}

func (s *Scanner) Scan() ([]db.FileIndex, error) {
	root := s.config.Root
	if root == "" {
		root = "."
	}

	var files []db.FileIndex
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(rel) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if s.isIgnored(rel) {
			return nil
		}
		files = append(files, db.FileIndex{Path: rel})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func shouldSkipDir(rel string) bool {
	name := filepath.Base(rel)
	switch name {
	case ".git", ".idea", ".superpowers", ".worktrees", ".agent", ".claude",
		"node_modules", "vendor", "dist", "build", "tmp":
		return true
	default:
		return false
	}
}

func (s *Scanner) isIgnored(rel string) bool {
	for _, pattern := range s.config.Ignore {
		if matched, _ := filepath.Match(pattern, rel); matched {
			return true
		}
		if matched, _ := filepath.Match(pattern, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/repo -run 'TestScannerFindsFiles|TestScannerSkipsIgnoredDirs|TestScannerAppliesConfigIgnore' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/scanner.go internal/repo/scanner_test.go
git commit -m "feat(repo): add repository scanner with config ignore rules"
```

---

### Task 2: Add Minimal Gitignore Support

**Files:**
- Create: `internal/repo/gitignore.go`
- Create: `internal/repo/gitignore_test.go`
- Modify: `internal/repo/scanner.go`
- Modify: `internal/repo/scanner_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/repo/gitignore_test.go`:

```go
package repo

import "testing"

func TestGitignoreMatch(t *testing.T) {
	g, err := ParseGitignore("*.log\nbuild/\n")
	if err != nil {
		t.Fatalf("ParseGitignore failed: %v", err)
	}
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"debug.log", false, true},
		{"main.go", false, false},
		{"build", true, true},
		{"build/output.js", false, true},
		{"src/build.go", false, false},
	}
	for _, tc := range cases {
		if got := g.Match(tc.path, tc.isDir); got != tc.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo -run TestGitignoreMatch -v`
Expected: FAIL (`ParseGitignore`, `Gitignore.Match` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/repo/gitignore.go`:

```go
package repo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type Gitignore struct {
	patterns []gitignorePattern
}

type gitignorePattern struct {
	anchored bool
	dirOnly  bool
	segments []string
}

func ParseGitignore(content string) (*Gitignore, error) {
	var patterns []gitignorePattern
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, parseGitignorePattern(line))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return &Gitignore{patterns: patterns}, nil
}

func LoadGitignore(path string) (*Gitignore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Gitignore{}, nil
		}
		return nil, err
	}
	return ParseGitignore(string(data))
}

func parseGitignorePattern(line string) gitignorePattern {
	p := gitignorePattern{}
	if strings.HasPrefix(line, "/") {
		p.anchored = true
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	p.segments = strings.Split(line, "/")
	return p
}

func (g *Gitignore) Match(path string, isDir bool) bool {
	path = filepath.ToSlash(path)
	for _, p := range g.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if matchPattern(p, path) {
			return true
		}
	}
	return false
}

func matchPattern(p gitignorePattern, path string) bool {
	pathSegments := strings.Split(path, "/")
	starts := []int{0}
	if !p.anchored {
		for i := range pathSegments {
			starts = append(starts, i)
		}
	}
	for _, start := range starts {
		if start+len(p.segments) > len(pathSegments) {
			continue
		}
		match := true
		for i, seg := range p.segments {
			if matched, _ := filepath.Match(seg, pathSegments[start+i]); !matched {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
```

Update `internal/repo/scanner.go`:

```go
type Scanner struct {
	config    Config
	gitignore *Gitignore
}

func NewScanner(config Config) *Scanner {
	var g *Gitignore
	if !config.IncludeGitignore {
		root := config.Root
		if root == "" {
			root = "."
		}
		loaded, _ := LoadGitignore(filepath.Join(root, ".gitignore"))
		g = loaded
	}
	return &Scanner{config: config, gitignore: g}
}
```

Update the walk function in `Scan`:

```go
	if s.gitignore != nil && s.gitignore.Match(rel, entry.IsDir()) {
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
```

Add to `internal/repo/scanner_test.go`:

```go
func TestScannerRespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.txt\n"), 0644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("secret"), 0644)

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected only main.go, got %+v", files)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/repo -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/gitignore.go internal/repo/gitignore_test.go internal/repo/scanner.go internal/repo/scanner_test.go
git commit -m "feat(repo): respect .gitignore when scanning"
```

---

### Task 3: Add Language Detection and File Hashing

**Files:**
- Create: `internal/repo/language.go`
- Create: `internal/repo/language_test.go`
- Modify: `internal/repo/scanner.go`
- Modify: `internal/repo/scanner_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/repo/language_test.go`:

```go
package repo

import "testing"

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.js", "javascript"},
		{"app.ts", "typescript"},
		{"main.py", "python"},
		{"README.md", "markdown"},
		{"Dockerfile", "dockerfile"},
		{"Makefile", "makefile"},
		{"file.unknown", ""},
		{"noext", ""},
	}
	for _, tc := range cases {
		if got := DetectLanguage(tc.path); got != tc.want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
```

In `internal/repo/scanner_test.go`, add:

```go
func TestScannerComputesHashesAndLanguages(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644)

	scanner := NewScanner(Config{Root: dir})
	files, err := scanner.Scan()
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Path != "main.go" {
		t.Fatalf("expected main.go, got %s", f.Path)
	}
	if f.Language != "go" {
		t.Fatalf("expected language go, got %s", f.Language)
	}
	if f.Hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if f.SizeBytes != 14 {
		t.Fatalf("expected size 14, got %d", f.SizeBytes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo -run 'TestDetectLanguage|TestScannerComputesHashesAndLanguages' -v`
Expected: FAIL (`DetectLanguage`, `Hash`, `SizeBytes` not populated).

- [ ] **Step 3: Write minimal implementation**

Create `internal/repo/language.go`:

```go
package repo

import (
	"path/filepath"
	"strings"
)

func DetectLanguage(path string) string {
	base := filepath.Base(path)
	if lang, ok := specialLanguages[base]; ok {
		return lang
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if lang, ok := extensionLanguages[ext]; ok {
		return lang
	}
	return ""
}

var specialLanguages = map[string]string{
	"Dockerfile": "dockerfile",
	"Makefile":   "makefile",
	"go.mod":     "go-module",
	"go.sum":     "go-sum",
}

var extensionLanguages = map[string]string{
	"go": "go", "js": "javascript", "ts": "typescript",
	"jsx": "javascript", "tsx": "typescript", "py": "python",
	"rs": "rust", "java": "java", "kt": "kotlin",
	"cpp": "cpp", "c": "c", "h": "c", "hpp": "cpp",
	"rb": "ruby", "php": "php", "sh": "shell",
	"md": "markdown", "json": "json", "yaml": "yaml",
	"yml": "yaml", "toml": "toml", "html": "html",
	"css": "css", "scss": "scss", "sql": "sql",
}
```

Update `internal/repo/scanner.go` imports:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/db"
)
```

Add helper:

```go
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
```

Update the append block in `Scan`:

```go
		fullPath := path
		hash, size, hashErr := hashFile(fullPath)
		if hashErr != nil {
			return nil
		}
		files = append(files, db.FileIndex{
			Path:      rel,
			Language:  DetectLanguage(rel),
			Hash:      hash,
			SizeBytes: size,
		})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/repo -run 'TestDetectLanguage|TestScannerComputesHashesAndLanguages' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/language.go internal/repo/language_test.go internal/repo/scanner.go internal/repo/scanner_test.go
git commit -m "feat(repo): detect language and hash files during scan"
```

---

### Task 4: Add repo.index Tool

**Files:**
- Create: `internal/tools/native/repo_index.go`
- Modify: `internal/tools/native/native.go`
- Modify: `internal/tools/native/native_test.go`
- Create: `internal/tools/native/repo_index_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/tools/native/repo_index_test.go`:

```go
package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRepoIndexTool(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.index")
	if !ok {
		t.Fatal("repo.index not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.index failed: %v", err)
	}
	if res.Summary == "" {
		t.Fatal("expected non-empty summary")
	}

	files, err := dbConn.GetFileIndex(projectID)
	if err != nil {
		t.Fatalf("GetFileIndex failed: %v", err)
	}
	if len(files) != 1 || files[0].Path != "main.go" {
		t.Fatalf("expected 1 indexed main.go, got %+v", files)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native -run TestRepoIndexTool -v`
Expected: FAIL (`repo.index` not found, `DB`/`ProjectID` fields missing).

- [ ] **Step 3: Write minimal implementation**

Modify `internal/tools/native/native.go`:

Add import:

```go
import "marshal/internal/db"
```

Update `Options`:

```go
type Options struct {
	WorkspaceRoot  string
	CommandRunner  CommandRunner
	TestCommand    string
	MaxOutputBytes int
	SessionState   *session.State
	DB             *db.DB
	ProjectID      int64
}
```

Update `toolSet`:

```go
type toolSet struct {
	root           string
	runner         CommandRunner
	testCommand    string
	maxOutputBytes int
	sessionState   *session.State
	db             *db.DB
	projectID      int64
}
```

Update `newToolSet`:

```go
	return &toolSet{
		root:           root,
		runner:         runner,
		testCommand:    testCommand,
		maxOutputBytes: maxOutputBytes,
		sessionState:   opts.SessionState,
		db:             opts.DB,
		projectID:      opts.ProjectID,
	}, nil
```

Update `RegisterAll` tool list:

```go
	for _, tool := range []registry.Tool{
		tools.fileReadTool(),
		tools.fileWritePatchTool(),
		tools.repoSearchTool(),
		tools.gitStatusTool(),
		tools.gitDiffTool(),
		tools.shellRunTool(),
		tools.testRunTool(),
		tools.repoIndexTool(),
		tools.repoMapTool(),
		tools.repoCardTool(),
	} {
```

Create `internal/tools/native/repo_index.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

func (t *toolSet) repoIndexTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.index",
		Description: "Scan the workspace, compute file hashes and languages, and store the file index in the project database.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, fmt.Errorf("database not configured for repo.index")
		}

		scanner := repo.NewScanner(repo.Config{Root: t.root})
		files, err := scanner.Scan()
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("scan repo: %w", err)
		}

		now := time.Now().UTC()
		for i := range files {
			files[i].LastIndexedAt = now
		}
		if err := t.db.SaveFileIndex(t.projectID, files); err != nil {
			return registry.ToolResult{}, fmt.Errorf("save file index: %w", err)
		}

		langCounts := map[string]int{}
		for _, f := range files {
			if f.Language != "" {
				langCounts[f.Language]++
			}
		}

		content := "Languages:\n"
		for lang, count := range langCounts {
			content += fmt.Sprintf("  %s: %d\n", lang, count)
		}
		return registry.ToolResult{
			Summary: fmt.Sprintf("Indexed %d files", len(files)),
			Content: content,
		}, nil
	}
	return tool
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/native -run TestRepoIndexTool -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/native.go internal/tools/native/repo_index.go internal/tools/native/repo_index_test.go
git commit -m "feat(tools): add repo.index tool"
```

---

### Task 5: Add repo.map Tool

**Files:**
- Create: `internal/repo/map.go`
- Create: `internal/repo/map_test.go`
- Create: `internal/tools/native/repo_map.go`
- Create: `internal/tools/native/repo_map_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/repo/map_test.go`:

```go
package repo

import (
	"strings"
	"testing"

	"marshal/internal/db"
)

func TestRenderDirectoryMap(t *testing.T) {
	files := []db.FileIndex{
		{Path: "cmd/marshal/main.go", Language: "go"},
		{Path: "internal/app/app.go", Language: "go"},
		{Path: "internal/db/db.go", Language: "go"},
		{Path: "README.md", Language: "markdown"},
	}
	out := RenderDirectoryMap(files, 100)
	for _, want := range []string{"cmd/", "internal/", "README.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in map:\n%s", want, out)
		}
	}
}
```

In `internal/tools/native/repo_map_test.go`:

```go
package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRepoMapTool(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	if err := dbConn.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "main.go", Language: "go", Hash: "abc", SizeBytes: 14, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("save file index: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.map")
	if !ok {
		t.Fatal("repo.map not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.map failed: %v", err)
	}
	if !strings.Contains(res.Content, "main.go") {
		t.Fatalf("expected main.go in map content: %s", res.Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo -run TestRenderDirectoryMap -v` and `go test ./internal/tools/native -run TestRepoMapTool -v`
Expected: FAIL (`RenderDirectoryMap`, `repo.map` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/repo/map.go`:

```go
package repo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/db"
)

// RenderDirectoryMap renders a simple indented directory tree from a file
// index. It shows up to maxFiles file entries; if there are more, it appends
// a truncation note.
func RenderDirectoryMap(files []db.FileIndex, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 200
	}

	tree := &dirNode{name: ".", children: map[string]*dirNode{}}
	for _, f := range files {
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		insertPath(tree, parts)
	}

	var b strings.Builder
	var fileCount int
	renderNode(&b, tree, "", &fileCount, maxFiles)

	if fileCount > maxFiles {
		fmt.Fprintf(&b, "\n... (%d more files)\n", fileCount-maxFiles)
	}
	return b.String()
}

type dirNode struct {
	name     string
	children map[string]*dirNode
	files    []string
}

func insertPath(node *dirNode, parts []string) {
	if len(parts) == 1 {
		node.files = append(node.files, parts[0])
		return
	}
	child, ok := node.children[parts[0]]
	if !ok {
		child = &dirNode{name: parts[0], children: map[string]*dirNode{}}
		node.children[parts[0]] = child
	}
	insertPath(child, parts[1:])
}

func renderNode(b *strings.Builder, node *dirNode, prefix string, fileCount *int, maxFiles int) {
	dirs := make([]string, 0, len(node.children))
	for name := range node.children {
		dirs = append(dirs, name)
	}
	sort.Strings(dirs)
	for _, name := range dirs {
		fmt.Fprintf(b, "%s%s/\n", prefix, name)
		renderNode(b, node.children[name], prefix+"  ", fileCount, maxFiles)
	}

	sort.Strings(node.files)
	for _, name := range node.files {
		if *fileCount < maxFiles {
			fmt.Fprintf(b, "%s%s\n", prefix, name)
		}
		*fileCount++
	}
}
```

Create `internal/tools/native/repo_map.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

func (t *toolSet) repoMapTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.map",
		Description: "Render a directory map of the indexed repository. Run repo.index first if no index exists.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, fmt.Errorf("database not configured for repo.map")
		}
		files, err := t.db.GetFileIndex(t.projectID)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load file index: %w", err)
		}
		if len(files) == 0 {
			return registry.ToolResult{
				Summary: "No indexed files",
				Content: "Run repo.index to build the file index first.",
			}, nil
		}
		content := repo.RenderDirectoryMap(files, 200)
		return registry.ToolResult{
			Summary: fmt.Sprintf("Directory map with %d files", len(files)),
			Content: content,
		}, nil
	}
	return tool
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/repo -run TestRenderDirectoryMap -v` and `go test ./internal/tools/native -run TestRepoMapTool -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/map.go internal/repo/map_test.go internal/tools/native/repo_map.go internal/tools/native/repo_map_test.go
git commit -m "feat(tools): add repo.map tool"
```

---

### Task 6: Add repo.card Tool

**Files:**
- Create: `internal/repo/card.go`
- Create: `internal/repo/card_test.go`
- Create: `internal/tools/native/repo_card.go`
- Create: `internal/tools/native/repo_card_test.go`

- [ ] **Step 1: Write the failing test**

In `internal/repo/card_test.go`:

```go
package repo

import (
	"strings"
	"testing"

	"marshal/internal/db"
)

func TestRenderRepoCard(t *testing.T) {
	files := []db.FileIndex{
		{Path: "main.go", Language: "go"},
		{Path: "cmd/marshal/main.go", Language: "go"},
		{Path: "README.md", Language: "markdown"},
	}
	out := RenderRepoCard("myproject", files)
	if !strings.Contains(out, "Project: myproject") {
		t.Errorf("expected project name in card:\n%s", out)
	}
	if !strings.Contains(out, "go: 2") {
		t.Errorf("expected go count in card:\n%s", out)
	}
	if !strings.Contains(out, "markdown: 1") {
		t.Errorf("expected markdown count in card:\n%s", out)
	}
}
```

In `internal/tools/native/repo_card_test.go`:

```go
package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/db"
	"marshal/internal/tools/registry"
)

func TestRepoCardTool(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	dbConn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer dbConn.Close()
	if err := dbConn.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	projectID, err := dbConn.GetOrCreateProject(tmp, "test")
	if err != nil {
		t.Fatalf("get or create project: %v", err)
	}
	if err := dbConn.SaveFileIndex(projectID, []db.FileIndex{
		{Path: "main.go", Language: "go", Hash: "abc", SizeBytes: 14, LastIndexedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("save file index: %v", err)
	}

	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: tmp, DB: dbConn, ProjectID: projectID}); err != nil {
		t.Fatalf("RegisterAll: %v", err)
	}

	tool, ok := reg.Lookup("repo.card")
	if !ok {
		t.Fatal("repo.card not found")
	}
	res, err := tool.Handler(context.Background(), registry.ToolCall{})
	if err != nil {
		t.Fatalf("repo.card failed: %v", err)
	}
	if !strings.Contains(res.Content, "test") {
		t.Fatalf("expected project name in card content: %s", res.Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/repo -run TestRenderRepoCard -v` and `go test ./internal/tools/native -run TestRepoCardTool -v`
Expected: FAIL (`RenderRepoCard`, `repo.card` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/repo/card.go`:

```go
package repo

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"marshal/internal/db"
)

// RenderRepoCard renders a short project summary: name, total files, language
// distribution, and top-level directories.
func RenderRepoCard(projectName string, files []db.FileIndex) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n", projectName)
	fmt.Fprintf(&b, "Files: %d\n", len(files))

	langCounts := map[string]int{}
	rootDirs := map[string]bool{}
	for _, f := range files {
		if f.Language != "" {
			langCounts[f.Language]++
		}
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		if len(parts) > 1 {
			rootDirs[parts[0]] = true
		} else {
			rootDirs["."] = true
		}
	}

	if len(langCounts) > 0 {
		b.WriteString("\nLanguages:\n")
		langs := make([]string, 0, len(langCounts))
		for lang := range langCounts {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		for _, lang := range langs {
			fmt.Fprintf(&b, "  %s: %d\n", lang, langCounts[lang])
		}
	}

	if len(rootDirs) > 0 {
		b.WriteString("\nTop-level directories:\n")
		dirs := make([]string, 0, len(rootDirs))
		for d := range rootDirs {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		for _, d := range dirs {
			fmt.Fprintf(&b, "  %s/\n", d)
		}
	}

	return b.String()
}
```

Create `internal/tools/native/repo_card.go`:

```go
package native

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/repo"
	"marshal/internal/tools/registry"
)

func (t *toolSet) repoCardTool() registry.Tool {
	tool := registry.Tool{
		Name:        "repo.card",
		Description: "Render a short project card from the indexed repository. Run repo.index first if no index exists.",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
		Risk:        registry.RiskReadOnly,
	}
	tool.Handler = func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
		if t.db == nil || t.projectID == 0 {
			return registry.ToolResult{}, fmt.Errorf("database not configured for repo.card")
		}
		files, err := t.db.GetFileIndex(t.projectID)
		if err != nil {
			return registry.ToolResult{}, fmt.Errorf("load file index: %w", err)
		}
		if len(files) == 0 {
			return registry.ToolResult{
				Summary: "No indexed files",
				Content: "Run repo.index to build the file index first.",
			}, nil
		}
		content := repo.RenderRepoCard(filepath.Base(t.root), files)
		return registry.ToolResult{
			Summary: fmt.Sprintf("Project card with %d files", len(files)),
			Content: content,
		}, nil
	}
	return tool
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/repo -run TestRenderRepoCard -v` and `go test ./internal/tools/native -run TestRepoCardTool -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/repo/card.go internal/repo/card_test.go internal/tools/native/repo_card.go internal/tools/native/repo_card_test.go
git commit -m "feat(tools): add repo.card tool"
```

---

### Task 7: Update Native Tool Tests and Final Verification

**Files:**
- Modify: `internal/tools/native/native_test.go`
- Modify: `docs/plans/task.md`
- Modify: `docs/10-mvp-implementation-checklist.md`

- [ ] **Step 1: Write the failing test update**

In `internal/tools/native/native_test.go`, the existing test `TestRegisterAllRegistersExpectedTools` expects 7 tools. Update it to expect 10 tools and verify the new tool names exist.

Find the existing test and replace the assertions:

```go
func TestRegisterAllRegistersExpectedTools(t *testing.T) {
	root := t.TempDir()
	reg := registry.New()
	if err := RegisterAll(reg, Options{WorkspaceRoot: root, CommandRunner: &fakeRunner{}}); err != nil {
		t.Fatalf("RegisterAll returned error: %v", err)
	}

	want := []string{
		"file.read",
		"file.write_patch",
		"repo.search",
		"git.status",
		"git.diff",
		"shell.run",
		"test.run",
		"repo.index",
		"repo.map",
		"repo.card",
	}
	for _, name := range want {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
	if got := len(reg.List()); got != len(want) {
		t.Errorf("expected %d tools, got %d", len(want), got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/native -run TestRegisterAllRegistersExpectedTools -v`
Expected: FAIL (expected 10 tools, got 7; repo.* missing).

- [ ] **Step 3: Verify all tests pass after implementation**

By this point the new tools exist. Run:

```bash
go test ./... -v
```
Expected: PASS.

- [ ] **Step 4: Update documentation**

In `docs/10-mvp-implementation-checklist.md`, mark Milestone J items complete:

```markdown
## Milestone J: Repo indexing v1

- [x] Scan files
- [x] Respect `.gitignore`
- [x] Hash files
- [x] Detect language by extension
- [x] Store file records
- [x] Generate simple directory map
- [x] Generate simple repo card
```

Create or update `docs/plans/task.md` with Milestone J tasks (if the project tracks current milestone in that file):

```markdown
| Task | Status | Details |
| --- | --- | --- |
| Task 1: Create repo scanner with ignore rules | completed | internal/repo/scanner.go |
| Task 2: Add .gitignore support | completed | internal/repo/gitignore.go |
| Task 3: Detect language and hash files | completed | internal/repo/language.go, hashFile |
| Task 4: Add repo.index tool | completed | internal/tools/native/repo_index.go |
| Task 5: Add repo.map tool | completed | internal/repo/map.go, repo_map.go |
| Task 6: Add repo.card tool | completed | internal/repo/card.go, repo_card.go |
| Task 7: Register tools and verify | completed | native_test.go, docs updated |
```

- [ ] **Step 5: Commit**

```bash
git add internal/tools/native/native_test.go docs/10-mvp-implementation-checklist.md docs/plans/task.md
git commit -m "test(tools): register repo tools and update docs"
```

---

## Self-Review

**Spec coverage (MVP checklist J):**
- Scan files → Task 1 (`Scanner.Scan`).
- Respect `.gitignore` → Task 2 (`gitignore.go` integration).
- Hash files → Task 3 (`hashFile`).
- Detect language by extension → Task 3 (`DetectLanguage`).
- Store file records → Task 4 (`repo.index` uses `db.SaveFileIndex`).
- Generate simple directory map → Task 5 (`RenderDirectoryMap`, `repo.map`).
- Generate simple repo card → Task 6 (`RenderRepoCard`, `repo.card`).

**Placeholder scan:** No TBD/TODO placeholders; every task contains exact file paths, code, and commands.

**Type consistency:**
- `db.FileIndex` is used consistently across `internal/repo` and `internal/tools/native`.
- `repo.Config.IncludeGitignore` controls whether gitignore is skipped.
- `native.Options.DB` and `ProjectID` are optional; repo tools fail gracefully when unset.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-02-milestone-j-repo-indexing.md`.

**Execution approach:** Subagent-Driven (recommended) — dispatch a fresh subagent per task, review between tasks, fast iteration.
