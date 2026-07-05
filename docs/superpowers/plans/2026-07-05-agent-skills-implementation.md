# Agent Skills v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Marshal agents the ability to load skill `.md` files (TOML frontmatter + markdown body) that inject specialized instructions into the agent's context via a `skill.load` tool.

**Architecture:** New `internal/skills/` package handles parsing and loading. Skills are discovered at startup from `~/.config/marshal/skills/` and `.marshal/skills/`. A `skill.load` tool is registered in the tool registry. The system prompt lists available skills so the LLM auto-detects relevance. Active skills are tracked on `session.State`.

**Tech Stack:** Go 1.26, go-toml v2 (already a dependency), Bubble Tea (TUI, unaffected), SQLite (unaffected)

## Global Constraints

- `go build ./cmd/marshal` must succeed after every task.
- `go test ./...` must pass after every task.
- `gofmt -w .` after any file change.
- Marshal's existing agent loop (non-skill tool calls, planning, final answer) must remain unchanged.
- The `tools` field in skill frontmatter is parsed but ignored — no tool registration from skills in v1.
- A missing `.marshal/skills/` or `~/.config/marshal/skills/` is not an error — returns empty index.
- Project skills override global skills with the same name.

---

### Task 1: Add active skills tracking to session.State

**Files:**
- Modify: `internal/app/session/session.go:99-128` (State struct + new methods)
- Modify: `internal/app/session/session_test.go:415-465` (add tests at end)

**Interfaces:**
- Produces: `func (s *State) ActivateSkill(name string)`, `func (s *State) DeactivateSkill(name string)`, `func (s *State) ActiveSkills() []string`, `func (s *State) HasActiveSkill(name string) bool`
- Produces: `activeSkills map[string]bool` field on State struct

- [ ] **Step 1: Write failing tests for active skills tracking**

Append to `internal/app/session/session_test.go`:

```go
func TestStateActiveSkillsRoundTrip(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	if len(state.ActiveSkills()) != 0 {
		t.Fatal("initial active skills should be empty")
	}

	state.ActivateSkill("debugging")

	if !state.HasActiveSkill("debugging") {
		t.Fatal("HasActiveSkill(debugging) = false, want true")
	}
	if state.HasActiveSkill("nonexistent") {
		t.Fatal("HasActiveSkill(nonexistent) = true, want false")
	}

	active := state.ActiveSkills()
	if len(active) != 1 || active[0] != "debugging" {
		t.Fatalf("ActiveSkills() = %v, want [debugging]", active)
	}

	state.DeactivateSkill("debugging")
	if len(state.ActiveSkills()) != 0 {
		t.Fatal("active skills should be empty after deactivate")
	}
	if state.HasActiveSkill("debugging") {
		t.Fatal("HasActiveSkill(debugging) = true after deactivate")
	}
}

func TestStateDeactivateSkillNonexistentNoop(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.DeactivateSkill("nonexistent")
	if len(state.ActiveSkills()) != 0 {
		t.Fatal("deactivating nonexistent skill should no-op")
	}
}

func TestStateActivateSkillDuplicateNoop(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.ActivateSkill("debugging")
	state.ActivateSkill("debugging")

	active := state.ActiveSkills()
	if len(active) != 1 {
		t.Fatalf("duplicate activation should produce 1 entry, got %d", len(active))
	}
}

func TestStateActiveSkillsReturnsCopy(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	state.ActivateSkill("debugging")
	active := state.ActiveSkills()
	active[0] = "mutated"

	got := state.ActiveSkills()
	if got[0] != "debugging" {
		t.Fatalf("ActiveSkills() returned mutable slice: %v", got)
	}
}

func TestStateActiveSkillsRaceFree(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			state.ActivateSkill("a")
			state.ActivateSkill("b")
			state.DeactivateSkill("a")
			state.DeactivateSkill("b")
		}
	}()

	for i := 0; i < 100; i++ {
		_ = state.ActiveSkills()
		_ = state.HasActiveSkill("a")
	}
	<-done
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/session/ -run "TestStateActiveSkills" -v`
Expected: FAIL with "undefined" or compilation error

- [ ] **Step 3: Add activeSkills field and methods to State**

In `internal/app/session/session.go`, add to the State struct after `plan` field (line ~122):

```go
	activeSkills    map[string]bool
```

In the `New` function (line ~131), add initialization after `turnToolCache`:

```go
		activeSkills:    make(map[string]bool),
```

Append these methods at the end of the file (after `ResetRemoteCost`, before closing):

```go
func (s *State) ActivateSkill(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeSkills[name] = true
}

func (s *State) DeactivateSkill(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeSkills, name)
}

func (s *State) ActiveSkills() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.activeSkills))
	for name := range s.activeSkills {
		names = append(names, name)
	}
	return names
}

func (s *State) HasActiveSkill(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeSkills[name]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/app/session/ -run "TestStateActiveSkills" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
go fmt ./internal/app/session/
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat: add active skills tracking to session.State"
```

---

### Task 2: Create skills package — types and parsing

**Files:**
- Create: `internal/skills/skill.go`
- Create: `internal/skills/skill_test.go`

**Interfaces:**
- Produces: `type Skill struct { Name, Description, Risk, Body string; Tools []ToolDef }`
- Produces: `type ToolDef struct { Name, Description, Risk, Schema, Handler, Command string }`
- Produces: `type Index struct` with `Load(name string) (Skill, bool)`, `List() []Skill`
- Produces: `func parseFrontmatter(raw string) (Skill, error)` (unexported, used by loader in Task 3)

- [ ] **Step 1: Write tests for Skill types and parseFrontmatter**

Create `internal/skills/skill_test.go`:

```go
package skills

import (
	"testing"
)

func TestParseFrontmatterValid(t *testing.T) {
	raw := `+++
name = "systematic-debugging"
description = "Systematic debugging process for bugs, test failures, and unexpected behavior"
risk = "read_only"
+++

# Systematic Debugging

When debugging, follow this process:
1. Reproduce the bug
2. Isolate
3. Identify root cause
`

	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if skill.Name != "systematic-debugging" {
		t.Fatalf("Name = %q, want systematic-debugging", skill.Name)
	}
	if skill.Description != "Systematic debugging process for bugs, test failures, and unexpected behavior" {
		t.Fatalf("Description = %q", skill.Description)
	}
	if skill.Risk != "read_only" {
		t.Fatalf("Risk = %q, want read_only", skill.Risk)
	}
	if skill.Body != "# Systematic Debugging\n\nWhen debugging, follow this process:\n1. Reproduce the bug\n2. Isolate\n3. Identify root cause\n" {
		t.Fatalf("Body = %q", skill.Body)
	}
	if len(skill.Tools) != 0 {
		t.Fatalf("Tools = %v, want empty", skill.Tools)
	}
}

func TestParseFrontmatterMissingName(t *testing.T) {
	raw := `+++
description = "A skill without a name"
+++

Body text.
`
	_, err := parseFrontmatter(raw)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseFrontmatterMissingDescription(t *testing.T) {
	raw := `+++
name = "my-skill"
+++

Body text.
`
	_, err := parseFrontmatter(raw)
	if err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestParseFrontmatterNoFrontmatter(t *testing.T) {
	raw := `# Just a heading

No frontmatter here.
`
	_, err := parseFrontmatter(raw)
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseFrontmatterDefaultRisk(t *testing.T) {
	raw := `+++
name = "my-skill"
description = "A skill without explicit risk"
+++

Body.
`
	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if skill.Risk != "read_only" {
		t.Fatalf("Risk = %q, want read_only (default)", skill.Risk)
	}
}

func TestParseFrontmatterToolDefinitions(t *testing.T) {
	raw := `+++
name = "k8s-deploy"
description = "Kubernetes deployment workflows"
risk = "command"

[[tools]]
name = "kubectl_get_pods"
description = "List pods in a namespace"
risk = "command"
schema = '{"type": "object", "properties": {"namespace": {"type": "string"}}}'
handler = "shell"
command = "kubectl get pods -n {{.namespace}}"
+++

# K8s Deploy

Safe deployment instructions.
`
	skill, err := parseFrontmatter(raw)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if len(skill.Tools) != 1 {
		t.Fatalf("Tools length = %d, want 1", len(skill.Tools))
	}
	if skill.Tools[0].Name != "kubectl_get_pods" {
		t.Fatalf("Tools[0].Name = %q", skill.Tools[0].Name)
	}
	if skill.Tools[0].Command != "kubectl get pods -n {{.namespace}}" {
		t.Fatalf("Tools[0].Command = %q", skill.Tools[0].Command)
	}
}

func TestIndexLoadAndList(t *testing.T) {
	idx := NewIndex()
	idx.Set("a", Skill{Name: "a", Description: "Skill A"})
	idx.Set("b", Skill{Name: "b", Description: "Skill B"})

	skill, ok := idx.Load("a")
	if !ok {
		t.Fatal("Load(a) returned false")
	}
	if skill.Name != "a" {
		t.Fatalf("Load(a).Name = %q", skill.Name)
	}

	_, ok = idx.Load("nonexistent")
	if ok {
		t.Fatal("Load(nonexistent) should return false")
	}

	list := idx.List()
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}
	if list[0].Name != "a" || list[1].Name != "b" {
		t.Fatalf("List order: %v, want [a, b]", []string{list[0].Name, list[1].Name})
	}
}

func TestIndexListEmpty(t *testing.T) {
	idx := NewIndex()
	list := idx.List()
	if list == nil {
		t.Fatal("List() returned nil, want empty slice")
	}
	if len(list) != 0 {
		t.Fatalf("List length = %d, want 0", len(list))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/skills/ -v`
Expected: FAIL (package doesn't exist)

- [ ] **Step 3: Create skills.go with types and parseFrontmatter**

Create `internal/skills/skill.go`:

```go
package skills

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Skill struct {
	Name        string
	Description string
	Risk        string
	Body        string
	Tools       []ToolDef
}

type ToolDef struct {
	Name        string
	Description string
	Risk        string
	Schema      string
	Handler     string
	Command     string
}

type Index struct {
	skills map[string]Skill
}

func NewIndex() *Index {
	return &Index{skills: make(map[string]Skill)}
}

func (idx *Index) Set(name string, skill Skill) {
	idx.skills[name] = skill
}

func (idx *Index) Load(name string) (Skill, bool) {
	skill, ok := idx.skills[name]
	return skill, ok
}

func (idx *Index) List() []Skill {
	names := make([]string, 0, len(idx.skills))
	for name := range idx.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	skills := make([]Skill, 0, len(names))
	for _, name := range names {
		skills = append(skills, idx.skills[name])
	}
	return skills
}

type frontmatter struct {
	Name        string    `toml:"name"`
	Description string    `toml:"description"`
	Risk        string    `toml:"risk"`
	Tools       []ToolDef `toml:"tools"`
}

func parseFrontmatter(raw string) (Skill, error) {
	const delimiter = "+++\n"

	idx := strings.Index(raw, delimiter)
	if idx != 0 {
		return Skill{}, fmt.Errorf("skill file must start with +++ delimiter")
	}

	end := strings.Index(raw[len(delimiter):], delimiter)
	if end == -1 {
		return Skill{}, fmt.Errorf("skill file missing closing +++ delimiter")
	}

	fmRaw := raw[len(delimiter) : len(delimiter)+end]
	body := raw[len(delimiter)+end+len(delimiter):]

	var fm frontmatter
	if err := toml.Unmarshal([]byte(fmRaw), &fm); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	if fm.Name == "" {
		return Skill{}, fmt.Errorf("skill frontmatter missing required field: name")
	}
	if fm.Description == "" {
		return Skill{}, fmt.Errorf("skill frontmatter missing required field: description")
	}
	if fm.Risk == "" {
		fm.Risk = "read_only"
	}

	return Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Risk:        fm.Risk,
		Body:        body,
		Tools:       fm.Tools,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/skills/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/skills/
git add internal/skills/skill.go internal/skills/skill_test.go
git commit -m "feat: add skills package with Skill type and TOML frontmatter parsing"
```

---

### Task 3: Create skills loader — scan directories and build index

**Files:**
- Create: `internal/skills/loader.go`
- Create: `internal/skills/loader_test.go`

**Interfaces:**
- Consumes: `parseFrontmatter` from Task 2, `Skill`, `Index` from Task 2
- Produces: `func LoadSkills(globalDir, projectDir string) (*Index, error)`

- [ ] **Step 1: Write tests for LoadSkills**

Create `internal/skills/loader_test.go`:

```go
package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	return path
}

func skillContent(name, description string) string {
	return "+++\nname = \"" + name + "\"\ndescription = \"" + description + "\"\n+++\n\n# " + name + "\n\nBody for " + name + ".\n"
}

func TestLoadSkillsBothDirs(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeSkillFile(t, globalDir, "global-skill.md", skillContent("global-skill", "A global skill"))
	writeSkillFile(t, projectDir, "project-skill.md", skillContent("project-skill", "A project skill"))

	idx, err := LoadSkills(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	list := idx.List()
	if len(list) != 2 {
		t.Fatalf("List length = %d, want 2", len(list))
	}

	names := make(map[string]bool)
	for _, s := range list {
		names[s.Name] = true
	}
	if !names["global-skill"] || !names["project-skill"] {
		t.Fatalf("missing expected skills in index: %v", names)
	}
}

func TestLoadSkillsProjectOverridesGlobal(t *testing.T) {
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	writeSkillFile(t, globalDir, "same-name.md", skillContent("same-name", "Global version"))
	writeSkillFile(t, projectDir, "same-name.md", skillContent("same-name", "Project version"))

	idx, err := LoadSkills(globalDir, projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	skill, ok := idx.Load("same-name")
	if !ok {
		t.Fatal("Load(same-name) returned false")
	}
	if skill.Description != "Project version" {
		t.Fatalf("Description = %q, want Project version", skill.Description)
	}
}

func TestLoadSkillsNeitherDirExists(t *testing.T) {
	idx, err := LoadSkills("/nonexistent/global", "/nonexistent/project")
	if err != nil {
		t.Fatalf("LoadSkills should not error for missing dirs: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Fatal("expected empty index for missing dirs")
	}
}

func TestLoadSkillsOnlyProjectDir(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "proj.md", skillContent("proj", "Project only"))

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}
	if len(idx.List()) != 1 {
		t.Fatalf("List length = %d, want 1", len(idx.List()))
	}
}

func TestLoadSkillsSkipsNonMdFiles(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "skill.md", skillContent("skill", "A skill"))
	writeSkillFile(t, projectDir, "notes.txt", "not a skill file")
	writeSkillFile(t, projectDir, "README.md", "# Not a skill, no frontmatter")

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	list := idx.List()
	if len(list) != 1 {
		t.Fatalf("List length = %d, want 1", len(list))
	}
	if list[0].Name != "skill" {
		t.Fatalf("List[0].Name = %q, want skill", list[0].Name)
	}
}

func TestLoadSkillsMalformedFileSkipped(t *testing.T) {
	projectDir := t.TempDir()
	writeSkillFile(t, projectDir, "good.md", skillContent("good", "A valid skill"))
	writeSkillFile(t, projectDir, "bad.md", "# No frontmatter here\n\nJust text.")

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	list := idx.List()
	if len(list) != 1 {
		t.Fatalf("List length = %d, want 1", len(list))
	}
	if list[0].Name != "good" {
		t.Fatalf("List[0].Name = %q, want good", list[0].Name)
	}
}

func TestLoadSkillsBodyPreserved(t *testing.T) {
	projectDir := t.TempDir()
	content := "+++\nname = \"test-skill\"\ndescription = \"A test\"\n+++\n\n## Section 1\n\nSome markdown content.\n\n## Section 2\n\nMore content.\n"
	writeSkillFile(t, projectDir, "test.md", content)

	idx, err := LoadSkills("/nonexistent/global", projectDir)
	if err != nil {
		t.Fatalf("LoadSkills: %v", err)
	}

	skill, ok := idx.Load("test-skill")
	if !ok {
		t.Fatal("Load(test-skill) returned false")
	}
	if skill.Body != "## Section 1\n\nSome markdown content.\n\n## Section 2\n\nMore content.\n" {
		t.Fatalf("Body = %q", skill.Body)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/skills/ -run "TestLoadSkills" -v`
Expected: FAIL with "undefined: LoadSkills"

- [ ] **Step 3: Create loader.go**

Create `internal/skills/loader.go`:

```go
package skills

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func LoadSkills(globalDir, projectDir string) (*Index, error) {
	idx := NewIndex()

	if err := loadFromDir(idx, globalDir, slog.Default()); err != nil {
		return nil, fmt.Errorf("load global skills from %s: %w", globalDir, err)
	}
	if err := loadFromDir(idx, projectDir, slog.Default()); err != nil {
		return nil, fmt.Errorf("load project skills from %s: %w", projectDir, err)
	}

	return idx, nil
}

func loadFromDir(idx *Index, dir string, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("failed to read skill file", "path", path, "error", err)
			continue
		}

		skill, err := parseFrontmatter(string(raw))
		if err != nil {
			logger.Warn("skipping invalid skill file", "path", path, "error", err)
			continue
		}

		idx.skills[skill.Name] = skill
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/skills/ -run "TestLoadSkills" -v`
Expected: PASS

- [ ] **Step 5: Run all skills tests**

Run: `go test ./internal/skills/ -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/skills/
git add internal/skills/loader.go internal/skills/loader_test.go
git commit -m "feat: add skills loader — scan directories and build index"
```

---

### Task 4: Register skill.load tool in the tool registry

**Files:**
- Create: `internal/skills/tool.go`
- Create: `internal/skills/tool_test.go`

**Interfaces:**
- Consumes: `Skill`, `Index` from Task 2; `LoadSkills` from Task 3; `session.State.ActiveSkills/ActivateSkill/ContextPack` from Task 1
- Consumes: `registry.Registry`, `registry.Tool`, `registry.ToolCall`, `registry.ToolResult`, `registry.RiskReadOnly`
- Consumes: `contextpack.EstimateTokens`
- Produces: `func RegisterTool(reg *registry.Registry, idx *Index, state *session.State)`

- [ ] **Step 1: Write tests for the skill.load tool handler**

Create `internal/skills/tool_test.go`:

```go
package skills

import (
	"context"
	"testing"
	"time"

	"marshal/internal/app/config"
	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/tools/registry"
)

func newTestState() *session.State {
	return session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
}

func TestSkillLoadToolSuccess(t *testing.T) {
	idx := NewIndex()
	idx.skills["debug"] = Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Risk:        "read_only",
		Body:        "# Debug\n\nReproduce, isolate, fix.\n",
	}

	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, ok := reg.Lookup("skill.load")
	if !ok {
		t.Fatal("skill.load tool not registered")
	}
	if tool.Risk != registry.RiskReadOnly {
		t.Fatalf("Risk = %s, want read_only", tool.Risk)
	}
	if tool.Cacheable {
		t.Fatal("skill.load should not be cacheable")
	}

	result, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_1",
		Name: "skill.load",
		Args: []byte(`{"name": "debug"}`),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if result.Summary == "" {
		t.Fatal("expected summary in result")
	}

	if !state.HasActiveSkill("debug") {
		t.Fatal("expected debug to be active after load")
	}

	msgs := state.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Messages length = %d, want 1", len(msgs))
	}
	if msgs[0].Role != session.RoleSystem {
		t.Fatalf("Message role = %q, want system", msgs[0].Role)
	}
	if msgs[0].Content != "# Debug\n\nReproduce, isolate, fix.\n" {
		t.Fatalf("Message content = %q", msgs[0].Content)
	}
}

func TestSkillLoadToolUnknownName(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_1",
		Name: "skill.load",
		Args: []byte(`{"name": "nonexistent"}`),
	})
	if err == nil {
		t.Fatal("expected error for unknown skill name")
	}
}

func TestSkillLoadToolAlreadyActive(t *testing.T) {
	idx := NewIndex()
	idx.skills["debug"] = Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Body:        "# Debug\n\nBody.\n",
	}

	state := newTestState()
	state.ActivateSkill("debug")

	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_2",
		Name: "skill.load",
		Args: []byte(`{"name": "debug"}`),
	})
	if err == nil {
		t.Fatal("expected error for already-active skill")
	}
}

func TestSkillLoadToolContextBudgetExceeded(t *testing.T) {
	idx := NewIndex()
	idx.skills["large"] = Skill{
		Name:        "large",
		Description: "A very large skill",
		Body:        "This skill has a body that exceeds the context budget. " + string(make([]byte, 50000)),
	}

	state := newTestState()
	state.SetContextPack(contextpack.Pack{
		TokenUsage: contextpack.TokenUsage{
			MaxTokens:       12000,
			EstimatedTokens: 11990,
		},
	})

	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_3",
		Name: "skill.load",
		Args: []byte(`{"name": "large"}`),
	})
	if err == nil {
		t.Fatal("expected error for budget exceeded")
	}
}

func TestSkillLoadToolInvalidArgs(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_4",
		Name: "skill.load",
		Args: []byte(`not json`),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON args")
	}
}

func TestSkillLoadToolMissingNameArg(t *testing.T) {
	idx := NewIndex()
	state := newTestState()
	reg := registry.New()
	RegisterTool(reg, idx, state)

	tool, _ := reg.Lookup("skill.load")
	_, err := tool.Handler(context.Background(), registry.ToolCall{
		ID:   "call_5",
		Name: "skill.load",
		Args: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for missing name arg")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/skills/ -run "TestSkillLoad" -v`
Expected: FAIL with "undefined: RegisterTool"

- [ ] **Step 3: Create tool.go**

Create `internal/skills/tool.go`:

```go
package skills

import (
	"context"
	"encoding/json"
	"fmt"

	"marshal/internal/app/session"
	"marshal/internal/contextpack"
	"marshal/internal/tools/registry"
)

func RegisterTool(reg *registry.Registry, idx *Index, state *session.State) {
	reg.Register(registry.Tool{
		Name:        "skill.load",
		Description: "Load a skill into the agent's context by name. The system prompt lists available skills. Call this when a skill's expertise is relevant to the task.",
		Schema:      json.RawMessage(`{"type": "object", "properties": {"name": {"type": "string", "description": "Name of the skill to load"}}, "required": ["name"]}`),
		Risk:        registry.RiskReadOnly,
		Cacheable:   false,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return handleSkillLoad(call, idx, state)
		},
	})
}

func handleSkillLoad(call registry.ToolCall, idx *Index, state *session.State) (registry.ToolResult, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return registry.ToolResult{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Name == "" {
		return registry.ToolResult{}, fmt.Errorf("missing required argument: name")
	}

	skill, ok := idx.Load(args.Name)
	if !ok {
		available := idx.List()
		names := make([]string, len(available))
		for i, s := range available {
			names[i] = s.Name
		}
		return registry.ToolResult{}, fmt.Errorf("unknown skill %q. Available: %v", args.Name, names)
	}

	if state.HasActiveSkill(args.Name) {
		return registry.ToolResult{}, fmt.Errorf("skill %q is already active", args.Name)
	}

	pack := state.ContextPack()
	if !pack.IsEmpty() {
		estimatedBody := contextpack.EstimateTokens(skill.Body)
		remaining := pack.TokenUsage.MaxTokens - pack.TokenUsage.EstimatedTokens
		if estimatedBody > remaining && remaining > 0 {
			return registry.ToolResult{}, fmt.Errorf(
				"cannot load skill: body is ~%d tokens but only %d tokens remain in context budget",
				estimatedBody, remaining,
			)
		}
	}

	state.AddMessage(session.RoleSystem, skill.Body)
	state.ActivateSkill(skill.Name)

	return registry.ToolResult{
		Summary: fmt.Sprintf("Skill %q loaded into context (%d chars).", skill.Name, len(skill.Body)),
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/skills/ -run "TestSkillLoad" -v`
Expected: PASS

- [ ] **Step 5: Run all skills tests and entire suite**

Run: `go test ./internal/skills/ -v && go test ./...`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/skills/
git add internal/skills/tool.go internal/skills/tool_test.go
git commit -m "feat: add skill.load tool to register and activate skills"
```

---

### Task 5: Add skills section to BuildSystemPrompt

**Files:**
- Modify: `internal/agent/prompts.go:103-128` (BuildSystemPrompt signature + body)
- Modify: `internal/agent/prompts_test.go` (if exists; otherwise create)

**Interfaces:**
- Consumes: `skills.Index.List()`, `session.State.ActiveSkills/HasActiveSkill`
- Produces: Updated `BuildSystemPrompt` with skills section

- [ ] **Step 1: Check if prompts_test.go exists and read it**

Run: `ls internal/agent/prompts_test.go` — if it doesn't exist, note that we create it.

- [ ] **Step 2: Write tests for skills section in system prompt**

Create `internal/agent/prompts_test.go` (or append if it exists):

```go
package agent

import (
	"strings"
	"testing"

	"marshal/internal/skills"
)

func TestBuildSystemPromptIncludesAvailableSkills(t *testing.T) {
	idx := skills.NewIndex()
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil)
	content := msg.Content

	if !strings.Contains(content, "Available Skills") {
		t.Fatal("system prompt should contain 'Available Skills' section placeholder")
	}
}

func TestBuildSystemPromptWithSkills(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging workflow"})
	idx.Set("deploy", skills.Skill{Name: "deploy", Description: "Deployment workflows"})

	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil)
	content := msg.Content

	if !strings.Contains(content, "`debug`") {
		t.Fatal("system prompt should list debug skill")
	}
	if !strings.Contains(content, "`deploy`") {
		t.Fatal("system prompt should list deploy skill")
	}
	if !strings.Contains(content, "Debugging workflow") {
		t.Fatal("system prompt should include skill descriptions")
	}
	if !strings.Contains(content, "skill.load") {
		t.Fatal("system prompt should mention skill.load")
	}
}

func TestBuildSystemPromptWithActiveSkills(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{Name: "debug", Description: "Debugging workflow"})

	active := []string{"debug"}
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, active)
	content := msg.Content

	if !strings.Contains(content, "Active Skills") {
		t.Fatal("system prompt should show 'Active Skills' when skills are loaded")
	}
	if !strings.Contains(content, "`debug`") {
		t.Fatal("system prompt should list active skill name")
	}
	if strings.Contains(content, "skill.load") {
		t.Fatal("system prompt should NOT mention skill.load when skills are active")
	}
}

func TestBuildSystemPromptNoSkills(t *testing.T) {
	msg := BuildSystemPrompt(RoleGeneral, nil, nil, nil)
	content := msg.Content

	if !strings.Contains(content, "No skills are available") {
		t.Fatal("system prompt should note no skills when index is nil")
	}
}

func TestBuildSystemPromptEmptySkillIndex(t *testing.T) {
	idx := skills.NewIndex()
	msg := BuildSystemPrompt(RoleGeneral, nil, idx, nil)
	content := msg.Content

	if !strings.Contains(content, "No skills are available") {
		t.Fatal("system prompt should note no skills when index is empty")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run "TestBuildSystemPrompt.*kill" -v`
Expected: FAIL (signature mismatch or compilation error)

- [ ] **Step 4: Update BuildSystemPrompt signature and add skills section**

In `internal/agent/prompts.go`, change the signature:

```go
import (
	"fmt"
	"strings"

	"marshal/internal/contextpack"
	"marshal/internal/llm/schema"
	"marshal/internal/skills"
	"marshal/internal/tools/registry"
)

func BuildSystemPrompt(role AgentRole, tools []registry.Tool, skillIndex *skills.Index, activeSkills []string) schema.ChatMessage {
```

Between the tools section and `baseOutputFormat`, add the skills section:

```go
	b.WriteString("\nAvailable tools:\n")
	for _, tool := range tools {
		b.WriteString(fmt.Sprintf("- %s (%s): %s\n", tool.Name, tool.Risk, tool.Description))
	}
	b.WriteString("\n")

	activeMap := make(map[string]bool, len(activeSkills))
	for _, name := range activeSkills {
		activeMap[name] = true
	}

	if len(activeMap) > 0 {
		b.WriteString("\n## Active Skills\n")
		for _, name := range activeSkills {
			b.WriteString(fmt.Sprintf("- `%s` — (Injected into context above)\n", name))
		}
		b.WriteString("\n")
	} else if skillIndex != nil {
		list := skillIndex.List()
		if len(list) > 0 {
			b.WriteString("\n## Available Skills\n")
			for _, skill := range list {
				b.WriteString(fmt.Sprintf("- `%s` — %s\n", skill.Name, skill.Description))
			}
			b.WriteString("\nNo skills are active. Call skill.load <name> to activate a skill when relevant to the task.\n")
		} else {
			b.WriteString("\n## Available Skills\nNo skills are available for this project.\n")
		}
	} else {
		b.WriteString("\n## Available Skills\nNo skills are available for this project.\n")
	}

	b.WriteString("\n")
	b.WriteString(baseOutputFormat)
```

- [ ] **Step 5: Update all callers of BuildSystemPrompt in agent/runner.go**

In `internal/agent/runner.go`, update the two `BuildSystemPrompt` calls:

Line 174: Change `BuildSystemPrompt(RoleGeneral, r.Registry.List())` to `BuildSystemPrompt(RoleGeneral, r.Registry.List(), r.SkillIndex, r.State.ActiveSkills())`

Line 200: Same change.

- [ ] **Step 6: Add SkillIndex field to Runner struct**

In `internal/agent/runner.go`, add to the Runner struct (after `ForceClass`, around line 84):

```go
	SkillIndex          *skills.SkillIndex
```

Add import for `"marshal/internal/skills"` to runner.go's imports.

- [ ] **Step 7: Run agent tests to verify they pass**

Run: `go test ./internal/agent/ -run "TestBuildSystemPrompt" -v`
Expected: Depends on existing tests — if they pass, good. If they were checking exact string content, they may need updating to accommodate the new section.

- [ ] **Step 8: Update any failing tests in agent/runner_test.go**

Read `internal/agent/runner_test.go` and update any test that calls `BuildSystemPrompt` or constructs a `Runner` to include `SkillIndex: nil` (or compile will fail due to new field).

If `NewRunner` is called in tests with struct literal construction, add `SkillIndex: nil,` to the struct.

- [ ] **Step 9: Run all agent tests**

Run: `go test ./internal/agent/ -v`
Expected: ALL PASS

- [ ] **Step 10: Commit**

```bash
gofmt -w internal/agent/
git add internal/agent/prompts.go internal/agent/prompts_test.go internal/agent/runner.go
git commit -m "feat: add skills section to BuildSystemPrompt with available/active skills"
```

---

### Task 6: Add active skills rebuild to the runner loop

**Files:**
- Modify: `internal/agent/runner.go:147-261` (Run method's loop)
- Modify: `internal/agent/runner_test.go` (add skill integration test)

**Interfaces:**
- Consumes: `State.ActiveSkills()` from Task 1, `Runner.SkillIndex` from Task 5
- Produces: Runner updates system prompt message when active skills change across iterations

- [ ] **Step 1: Add active skills tracking variable to Run()**

In `runner.go`'s `Run()` method, right after the initial messages build (line ~175), add tracking of the last-rendered active skills set:

```go
	lastRenderedSkills := r.State.ActiveSkills()
```

- [ ] **Step 2: Add prompt rebuild at the top of each loop iteration**

In the for loop (line ~209), at the top of the iteration body (after `for iteration := 0; ...` and before `raw, err := ...`), add:

```go
		currentSkills := r.State.ActiveSkills()
		if skillsChanged(lastRenderedSkills, currentSkills) {
			messages[0] = BuildSystemPrompt(RoleGeneral, r.Registry.List(), r.SkillIndex, currentSkills)
			lastRenderedSkills = currentSkills
		}
```

- [ ] **Step 3: Add skillsChanged helper function**

At the end of `runner.go` (before the last function), add:

```go
func skillsChanged(prev, curr []string) bool {
	if len(prev) != len(curr) {
		return true
	}
	prevSet := make(map[string]bool, len(prev))
	for _, s := range prev {
		prevSet[s] = true
	}
	for _, s := range curr {
		if !prevSet[s] {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Import skills package in runner.go**

Add `"marshal/internal/skills"` to the imports in `runner.go` (if not already added in Task 5).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/agent/ -v`
Expected: ALL PASS (existing tests should be unaffected)

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent/runner.go
git add internal/agent/runner.go
git commit -m "feat: rebuild system prompt when active skills change across iterations"
```

---

### Task 7: Wire skills loading into app.Run

**Files:**
- Modify: `internal/app/app.go:150-196` (buildAgentRunner + Run)
- Modify: `internal/tools/native/native.go:56-81` (RegisterAll)

**Interfaces:**
- Consumes: `skills.LoadSkills` from Task 3, `skills.RegisterTool` from Task 4
- Produces: Skills are loaded at startup and wired into the runner + prompts

- [ ] **Step 1: Add skills loading to app.Run**

In `internal/app/app.go`, add import:

```go
	"marshal/internal/skills"
```

In `Run()`, after the config load (line ~223) and before `buildAgentRunner` (line ~253), add:

```go
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}
	globalSkillsDir := filepath.Join(homeDir, ".config", "marshal", "skills")
	projectSkillsDir := filepath.Join(workingDir, ".marshal", "skills")
	skillIndex, err := skills.LoadSkills(globalSkillsDir, projectSkillsDir)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}
```

- [ ] **Step 2: Pass SkillIndex to buildAgentRunner**

Change `buildAgentRunner` to accept the skill index:

```go
func buildAgentRunner(ctx context.Context, cfg config.Config, state *session.State, database *db.DB, projectID int64, skillIndex *skills.Index) (*agent.Runner, *registry.Registry, error) {
```

After the existing `native.RegisterAll` call (line ~158), add:
```go
	skills.RegisterTool(reg, skillIndex, state)
```

After `runner := agent.NewRunner(...)`, add:
```go
	runner.SkillIndex = skillIndex
```

- [ ] **Step 3: Update call site in Run()**

Change line ~253 from:
```go
	runner, toolReg, err = buildAgentRunner(ctx, cfg, state, database, projectID)
```
to:
```go
	runner, toolReg, err = buildAgentRunner(ctx, cfg, state, database, projectID, skillIndex)
```

- [ ] **Step 4: Build and verify compilation**

Run: `go build ./cmd/marshal`
Expected: SUCCESS (no compilation errors)

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/app/app.go
git add internal/app/app.go internal/tools/native/native.go
git commit -m "feat: wire skills loading into app startup and tool registration"
```

---

### Task 8: Integration test — runner loads skill via skill.load tool call

**Files:**
- Modify: `internal/agent/runner_test.go` (append test at end of file)

**Interfaces:**
- Consumes: `skills.Index`, `skills.RegisterTool`, `skills.Skill`, `State.ActivateSkill/ActiveSkills/HasActiveSkill`
- Produces: End-to-end test proving skill.load works inside the runner loop

- [ ] **Step 1: Write integration test**

Append to `internal/agent/runner_test.go` (after the last test function):

```go
func TestRunLoadsSkillViaToolCall(t *testing.T) {
	idx := skills.NewIndex()
	idx.Set("debug", skills.Skill{
		Name:        "debug",
		Description: "Debugging workflow",
		Body:        "# Debug\n\nSteps: reproduce, isolate, fix, verify.\n",
	})

	reg := registry.New()
	state := newTestState(t)

	pol := policy.NewEngine(&config.Config{}, nil)
	skills.RegisterTool(reg, idx, state)

	p := &scriptedProvider{responses: []string{
		"1. Load the debug skill.",
		`{"rationale":"need debugging workflow","action":{"type":"tool_call","tool":"skill.load","args":{"name":"debug"}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Debug skill loaded and used."}}`,
	}}
	runner := NewRunner(p, reg, pol, state, "test-model")
	runner.SkillIndex = idx

	if err := runner.Run(context.Background(), "Debug this"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !state.HasActiveSkill("debug") {
		t.Fatal("HasActiveSkill(debug) = false, want true")
	}

	msgs := state.Messages()
	foundBody := false
	for _, m := range msgs {
		if m.Content == "# Debug\n\nSteps: reproduce, isolate, fix, verify.\n" {
			foundBody = true
			break
		}
	}
	if !foundBody {
		t.Fatalf("skill body not found in messages: %#v", msgs)
	}

	var systemPromptMsgs []string
	for _, req := range p.requests {
		for _, msg := range req.Messages {
			if msg.Role == schema.RoleSystem {
				systemPromptMsgs = append(systemPromptMsgs, msg.Content)
			}
		}
	}
	if len(systemPromptMsgs) < 2 {
		t.Fatalf("expected at least 2 provider requests with system messages, got %d", len(systemPromptMsgs))
	}
	if !strings.Contains(systemPromptMsgs[0], "`debug`") {
		t.Fatal("first system prompt should list debug skill")
	}
	if !strings.Contains(systemPromptMsgs[0], "Debugging workflow") {
		t.Fatal("first system prompt should include skill description")
	}
	if !strings.Contains(systemPromptMsgs[1], "Active Skills") {
		t.Fatal("second system prompt should show Active Skills")
	}
}
```

- [ ] **Step 2: Add import for skills package**

Add `"marshal/internal/skills"` to the import block in `runner_test.go`.

- [ ] **Step 3: Run the integration test**

Run: `go test ./internal/agent/ -run "TestRunLoadsSkillViaToolCall" -v`
Expected: PASS

- [ ] **Step 4: Run all tests**

Run: `go test ./...`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/
git add internal/agent/runner_test.go
git commit -m "test: add integration test for skill.load via runner loop"
```

---

### Task 9: Create a sample skill for manual testing

**Files:**
- Create: `.marshal/skills/systematic-debugging.md`

- [ ] **Step 1: Create the sample skill**

```bash
mkdir -p .marshal/skills
```

Create `.marshal/skills/systematic-debugging.md`:

```markdown
+++
name = "systematic-debugging"
description = "Systematic debugging process for bugs, test failures, and unexpected behavior"
risk = "read_only"
+++

# Systematic Debugging

When debugging, follow this process:

1. Reproduce the bug — confirm it exists and understand expected vs actual
2. Isolate — narrow to the minimal reproduction case
3. Identify root cause — don't fix symptoms
4. Fix and verify — write a test that fails before and passes after
```

- [ ] **Step 2: Build and verify skill is loaded**

Run: `go build ./cmd/marshal`
Expected: SUCCESS

Run: `go run ./cmd/marshal` and verify the skill appears in the system prompt (visible in debug/log output or TUI context browser).

- [ ] **Step 3: Commit**

```bash
git add .marshal/skills/systematic-debugging.md
git commit -m "feat: add sample systematic-debugging skill for manual testing"
```
