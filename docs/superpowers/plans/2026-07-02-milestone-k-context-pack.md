# Milestone K Context Pack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Context Pack v1: a deterministic context-pack builder, session storage, runner prompt injection, and read-only TUI visibility.

**Architecture:** `internal/contextpack` owns pure pack construction, token estimation, budgeting, and rendering. `session.State` stores the current pack, `agent.Runner` injects rendered packs when present, and `tui.Model.View` displays pack summary metadata without interactive browsing.

**Tech Stack:** Go 1.26.1, standard library, existing `internal/agent`, `internal/app/session`, and `internal/app/tui` packages. No new external dependencies.

## Global Constraints

- Keep Milestone J integration as upstream inputs only; do not edit J-owned scanner/indexing files.
- Default context budget is `12000` estimated tokens.
- Token estimation uses `ceil(len([]rune(text)) / 4)`.
- Empty packs render as an empty string and must not alter runner behavior.
- Context-pack building performs no filesystem IO.
- TUI context browser is read-only.
- `go test ./...` must pass.

---

## File Structure

- Create `internal/contextpack/contextpack.go` for public types, constants, and `IsEmpty`/`Clone`.
- Create `internal/contextpack/builder.go` for `Builder`, token estimation, ordering, truncation, and build logic.
- Create `internal/contextpack/render.go` for stable prompt rendering.
- Create `internal/contextpack/contextpack_test.go` for package unit tests.
- Modify `internal/app/session/session.go` to store the current context pack copy-safely.
- Modify `internal/app/session/session_test.go` to test context pack storage.
- Modify `internal/agent/prompts.go` to add `BuildContextPackMessage`.
- Modify `internal/agent/prompts_test.go` to test context-pack prompt formatting.
- Modify `internal/agent/runner.go` to inject stored packs and refresh plan sections after planning.
- Modify `internal/agent/runner_test.go` to verify provider request behavior.
- Modify `internal/app/tui/model.go` to render a `Context` panel.
- Modify `internal/app/tui/model_test.go` to verify empty and populated panel states.
- Modify `docs/10-mvp-implementation-checklist.md` after implementation passes.
- Create `docs/plans/2026-07-02-milestone-k-context-pack.md` as the task status table.

---

### Task 1: Add Pure Context Pack Package

**Files:**
- Create: `internal/contextpack/contextpack.go`
- Create: `internal/contextpack/builder.go`
- Create: `internal/contextpack/render.go`
- Create: `internal/contextpack/contextpack_test.go`

**Interfaces:**
- Produces: `contextpack.Pack`, `contextpack.Section`, `contextpack.BuildInput`, `contextpack.Builder`, `contextpack.NewBuilder()`, `contextpack.EstimateTokens(string) int`, `contextpack.Render(Pack) string`, `Pack.IsEmpty() bool`, `Pack.Clone() Pack`.
- Consumes: no project packages outside the standard library.

- [ ] **Step 1: Write the failing tests**

Create `internal/contextpack/contextpack_test.go`:

```go
package contextpack

import (
	"strings"
	"testing"
	"time"
)

func TestEstimateTokensRoundsUpByFourRunes(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefghi", 3},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.text); got != tc.want {
			t.Fatalf("EstimateTokens(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestBuilderOrdersSectionsAndTracksTokens(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	pack := NewBuilder().Build(BuildInput{
		RepoCard: "Project: marshal",
		Plan:     []string{"1. Read files", "2. Patch tests"},
		FileSnippets: []FileSnippet{
			{Path: "internal/app/app.go", StartLine: 1, EndLine: 3, Content: "package app"},
		},
		RecentToolOutput: []ToolOutput{
			{ToolName: "go.test", Summary: "ok"},
		},
		MaxTokens: 12000,
		Now:       func() time.Time { return now },
	})

	if pack.GeneratedAt != now {
		t.Fatalf("GeneratedAt = %s, want %s", pack.GeneratedAt, now)
	}
	if len(pack.Sections) != 4 {
		t.Fatalf("len(Sections) = %d, want 4: %#v", len(pack.Sections), pack.Sections)
	}
	wantKinds := []SectionKind{SectionRepoCard, SectionPlan, SectionFileSnippet, SectionToolOutput}
	for i, want := range wantKinds {
		if pack.Sections[i].Kind != want {
			t.Fatalf("section %d kind = %q, want %q", i, pack.Sections[i].Kind, want)
		}
	}
	if pack.TokenUsage.MaxTokens != 12000 || pack.TokenUsage.EstimatedTokens <= 0 {
		t.Fatalf("TokenUsage = %#v", pack.TokenUsage)
	}
	if pack.TokenUsage.Truncated {
		t.Fatalf("TokenUsage.Truncated = true, want false")
	}
}

func TestBuilderTruncatesToBudget(t *testing.T) {
	pack := NewBuilder().Build(BuildInput{
		RepoCard:  strings.Repeat("a", 80),
		MaxTokens: 5,
		Now:       func() time.Time { return time.Unix(100, 0).UTC() },
	})

	if len(pack.Sections) != 1 {
		t.Fatalf("len(Sections) = %d, want 1", len(pack.Sections))
	}
	if !strings.Contains(pack.Sections[0].Content, "...[truncated]") {
		t.Fatalf("section content missing truncation marker: %q", pack.Sections[0].Content)
	}
	if !pack.TokenUsage.Truncated {
		t.Fatalf("TokenUsage.Truncated = false, want true")
	}
	if pack.TokenUsage.EstimatedTokens > pack.TokenUsage.MaxTokens {
		t.Fatalf("estimated tokens %d exceeds max %d", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
	}
}

func TestRenderUsesStableSectionFormat(t *testing.T) {
	pack := Pack{
		Sections: []Section{
			{Kind: SectionRepoCard, Title: "Repo Card", Source: "repo.card", Content: "Project: marshal", EstimatedTokens: 4},
			{Kind: SectionPlan, Title: "Current Plan", Content: "1. Test\n2. Build", EstimatedTokens: 5},
		},
		TokenUsage: TokenUsage{MaxTokens: 12000, EstimatedTokens: 9},
	}

	rendered := Render(pack)
	for _, want := range []string{
		"Project context pack:",
		"## Repo Card",
		"Source: repo.card",
		"Estimated tokens: 4",
		"Project: marshal",
		"## Current Plan",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Render() missing %q:\n%s", want, rendered)
		}
	}
}

func TestEmptyPackRendersEmptyAndClonesSafely(t *testing.T) {
	var pack Pack
	if !pack.IsEmpty() {
		t.Fatal("zero Pack should be empty")
	}
	if rendered := Render(pack); rendered != "" {
		t.Fatalf("Render(empty) = %q, want empty", rendered)
	}

	pack = Pack{Sections: []Section{{Kind: SectionRepoCard, Title: "Repo Card", Content: "Project"}}}
	clone := pack.Clone()
	clone.Sections[0].Content = "mutated"
	if pack.Sections[0].Content != "Project" {
		t.Fatalf("Clone did not protect section content: %#v", pack.Sections)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/contextpack -v
```

Expected: FAIL because `internal/contextpack` has no implementation files yet.

- [ ] **Step 3: Add public types**

Create `internal/contextpack/contextpack.go`:

```go
package contextpack

import "time"

const DefaultMaxTokens = 12000

type SectionKind string

const (
	SectionRepoCard    SectionKind = "repo_card"
	SectionPlan        SectionKind = "plan"
	SectionFileSnippet SectionKind = "file_snippet"
	SectionToolOutput  SectionKind = "tool_output"
)

type Pack struct {
	Sections    []Section
	TokenUsage  TokenUsage
	GeneratedAt time.Time
}

type Section struct {
	Kind            SectionKind
	Title           string
	Content         string
	Source          string
	Priority        int
	EstimatedTokens int
}

type TokenUsage struct {
	MaxTokens       int
	EstimatedTokens int
	Truncated       bool
}

type BuildInput struct {
	RepoCard         string
	FileSnippets     []FileSnippet
	RecentToolOutput []ToolOutput
	Plan             []string
	MaxTokens        int
	Now              func() time.Time
}

type FileSnippet struct {
	Path      string
	StartLine int
	EndLine   int
	Content   string
}

type ToolOutput struct {
	ToolName string
	Summary  string
	Content  string
}

func (p Pack) IsEmpty() bool {
	return len(p.Sections) == 0
}

func (p Pack) Clone() Pack {
	clone := p
	if p.Sections != nil {
		clone.Sections = append([]Section(nil), p.Sections...)
	}
	return clone
}
```

- [ ] **Step 4: Add builder implementation**

Create `internal/contextpack/builder.go`:

```go
package contextpack

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const truncationMarker = "\n\n...[truncated]"

type Builder struct{}

func NewBuilder() Builder {
	return Builder{}
}

func EstimateTokens(text string) int {
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return 0
	}
	return (runes + 3) / 4
}

func (b Builder) Build(input BuildInput) Pack {
	maxTokens := input.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	now := time.Now
	if input.Now != nil {
		now = input.Now
	}

	candidates := buildCandidateSections(input)
	pack := Pack{
		TokenUsage:  TokenUsage{MaxTokens: maxTokens},
		GeneratedAt: now().UTC(),
	}

	remaining := maxTokens
	for _, section := range candidates {
		section.EstimatedTokens = EstimateTokens(section.Content)
		if section.EstimatedTokens == 0 {
			continue
		}
		if section.EstimatedTokens <= remaining {
			pack.Sections = append(pack.Sections, section)
			pack.TokenUsage.EstimatedTokens += section.EstimatedTokens
			remaining -= section.EstimatedTokens
			continue
		}

		truncated, ok := truncateToTokens(section.Content, remaining)
		if !ok {
			pack.TokenUsage.Truncated = true
			continue
		}
		section.Content = truncated
		section.EstimatedTokens = EstimateTokens(section.Content)
		pack.Sections = append(pack.Sections, section)
		pack.TokenUsage.EstimatedTokens += section.EstimatedTokens
		pack.TokenUsage.Truncated = true
		remaining -= section.EstimatedTokens
	}

	return pack
}

func buildCandidateSections(input BuildInput) []Section {
	var sections []Section
	if strings.TrimSpace(input.RepoCard) != "" {
		sections = append(sections, Section{
			Kind:     SectionRepoCard,
			Title:    "Repo Card",
			Source:   "repo.card",
			Priority: 10,
			Content:  strings.TrimSpace(input.RepoCard),
		})
	}
	if len(input.Plan) > 0 {
		sections = append(sections, Section{
			Kind:     SectionPlan,
			Title:    "Current Plan",
			Priority: 20,
			Content:  strings.Join(input.Plan, "\n"),
		})
	}
	for _, snippet := range input.FileSnippets {
		content := strings.TrimSpace(snippet.Content)
		if content == "" {
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
	for _, output := range input.RecentToolOutput {
		content := strings.TrimSpace(output.Summary)
		if strings.TrimSpace(output.Content) != "" {
			content += "\n\n" + strings.TrimSpace(output.Content)
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		sections = append(sections, Section{
			Kind:     SectionToolOutput,
			Title:    output.ToolName,
			Source:   output.ToolName,
			Priority: 40,
			Content:  content,
		})
	}
	return sections
}

func truncateToTokens(content string, maxTokens int) (string, bool) {
	if maxTokens <= 0 {
		return "", false
	}
	markerTokens := EstimateTokens(truncationMarker)
	if maxTokens <= markerTokens {
		return "", false
	}
	maxRunes := (maxTokens - markerTokens) * 4
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content, true
	}
	truncated := strings.TrimRight(string(runes[:maxRunes]), "\n\t ")
	return truncated + truncationMarker, true
}
```

- [ ] **Step 5: Add rendering implementation**

Create `internal/contextpack/render.go`:

```go
package contextpack

import (
	"fmt"
	"strings"
)

func Render(pack Pack) string {
	if pack.IsEmpty() {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Project context pack:\n")
	fmt.Fprintf(&b, "Estimated tokens: %d/%d\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
	if pack.TokenUsage.Truncated {
		fmt.Fprintf(&b, "Truncated: true\n")
	}

	for _, section := range pack.Sections {
		fmt.Fprintf(&b, "\n## %s\n", section.Title)
		fmt.Fprintf(&b, "Kind: %s\n", section.Kind)
		if section.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", section.Source)
		}
		fmt.Fprintf(&b, "Estimated tokens: %d\n\n", section.EstimatedTokens)
		fmt.Fprintf(&b, "%s\n", section.Content)
	}

	return strings.TrimRight(b.String(), "\n")
}
```

- [ ] **Step 6: Run tests**

Run:

```bash
go test ./internal/contextpack -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/contextpack
git commit -m "feat(contextpack): add deterministic context pack builder"
```

---

### Task 2: Store Context Packs On Session State

**Files:**
- Modify: `internal/app/session/session.go`
- Modify: `internal/app/session/session_test.go`

**Interfaces:**
- Consumes: `contextpack.Pack`.
- Produces: `State.SetContextPack(contextpack.Pack)` and `State.ContextPack() contextpack.Pack`.

- [ ] **Step 1: Write the failing test**

Append to `internal/app/session/session_test.go`:

```go
func TestStateContextPackStoresCopies(t *testing.T) {
	state := New(config.Default(), "/repo", time.Unix(100, 0), Persistence{})
	pack := contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal"},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	}

	state.SetContextPack(pack)
	pack.Sections[0].Content = "mutated before read"

	got := state.ContextPack()
	if got.Sections[0].Content != "Project: marshal" {
		t.Fatalf("ContextPack() = %#v, want stored copy", got)
	}

	got.Sections[0].Content = "mutated after read"
	gotAgain := state.ContextPack()
	if gotAgain.Sections[0].Content != "Project: marshal" {
		t.Fatalf("ContextPack() returned mutable internal slice: %#v", gotAgain)
	}
}
```

Add this import to `internal/app/session/session_test.go`:

```go
"marshal/internal/contextpack"
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/app/session -run TestStateContextPackStoresCopies -v
```

Expected: FAIL because `SetContextPack` and `ContextPack` are undefined.

- [ ] **Step 3: Add state field and methods**

Modify `internal/app/session/session.go` imports:

```go
"marshal/internal/contextpack"
```

Add this field to `State`:

```go
contextPack contextpack.Pack
```

Add methods near the other state accessors:

```go
func (s *State) SetContextPack(pack contextpack.Pack) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextPack = pack.Clone()
}

func (s *State) ContextPack() contextpack.Pack {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextPack.Clone()
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/app/session -run TestStateContextPackStoresCopies -v
go test ./internal/app/session -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/session/session.go internal/app/session/session_test.go
git commit -m "feat(session): store current context pack"
```

---

### Task 3: Inject Context Packs Into Agent Prompts

**Files:**
- Modify: `internal/agent/prompts.go`
- Modify: `internal/agent/prompts_test.go`
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `contextpack.Render`, `session.State.ContextPack`, `session.State.SetContextPack`.
- Produces: `BuildContextPackMessage(pack contextpack.Pack) (schema.ChatMessage, bool)`.

- [ ] **Step 1: Write prompt helper failing tests**

Add imports in `internal/agent/prompts_test.go`:

```go
"marshal/internal/contextpack"
```

Append tests:

```go
func TestBuildContextPackMessageReturnsFalseForEmptyPack(t *testing.T) {
	msg, ok := BuildContextPackMessage(contextpack.Pack{})
	if ok {
		t.Fatalf("ok = true, want false")
	}
	if msg.Content != "" {
		t.Fatalf("msg.Content = %q, want empty", msg.Content)
	}
}

func TestBuildContextPackMessageRendersPack(t *testing.T) {
	msg, ok := BuildContextPackMessage(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if msg.Role != schema.RoleUser {
		t.Fatalf("Role = %q, want %q", msg.Role, schema.RoleUser)
	}
	if !strings.Contains(msg.Content, "Project context pack:") || !strings.Contains(msg.Content, "Project: marshal") {
		t.Fatalf("context message missing rendered pack:\n%s", msg.Content)
	}
}
```

- [ ] **Step 2: Write runner failing tests**

Modify `scriptedProvider` in `internal/agent/runner_test.go`:

```go
type scriptedProvider struct {
	responses []string
	errs      []error
	calls     int
	requests  []schema.ChatRequest
}
```

At the start of `Chat`, after `idx := p.calls`, add:

```go
p.requests = append(p.requests, req)
```

Append tests:

```go
func TestRunInjectsStoredContextPack(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"Marshal is indexed."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(p.requests))
	}
	var found bool
	for _, msg := range p.requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") && strings.Contains(msg.Content, "Project: marshal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("request missing context pack: %#v", p.requests[0].Messages)
	}
}

func TestRunOmitsContextPackWhenEmpty(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		`{"rationale":"simple","action":{"type":"answer","content":"No pack."}}`,
	}}
	reg := registry.New()
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "What does this project do?"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, msg := range p.requests[0].Messages {
		if strings.Contains(msg.Content, "Project context pack:") {
			t.Fatalf("empty context pack was injected: %#v", p.requests[0].Messages)
		}
	}
}

func TestRunAddsPlanToContextPackForActionCalls(t *testing.T) {
	p := &scriptedProvider{responses: []string{
		"1. Inspect the repo.\n2. Run the demo tool.",
		`{"rationale":"need data","action":{"type":"tool_call","tool":"demo.read","args":{}}}`,
		`{"rationale":"done","action":{"type":"final","content":"Done."}}`,
	}}
	reg := registry.New()
	if err := reg.Register(registry.Tool{
		Name: "demo.read",
		Risk: registry.RiskReadOnly,
		Handler: func(ctx context.Context, call registry.ToolCall) (registry.ToolResult, error) {
			return registry.ToolResult{Summary: "ok"}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	pol := policy.NewEngine(&config.Config{}, nil)
	state := newTestState(t)
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{Kind: contextpack.SectionRepoCard, Title: "Repo Card", Content: "Project: marshal", EstimatedTokens: 4},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	runner := NewRunner(p, reg, pol, state, "test-model")

	if err := runner.Run(context.Background(), "Add a test"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(p.requests) < 2 {
		t.Fatalf("provider requests = %d, want at least 2", len(p.requests))
	}
	var foundPlan bool
	for _, msg := range p.requests[1].Messages {
		if strings.Contains(msg.Content, "## Current Plan") && strings.Contains(msg.Content, "Inspect the repo") {
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Fatalf("action request missing plan context: %#v", p.requests[1].Messages)
	}
}
```

Add imports to `internal/agent/runner_test.go`:

```go
"strings"
"marshal/internal/contextpack"
```

- [ ] **Step 3: Run tests to verify they fail**

Run:

```bash
go test ./internal/agent -run 'TestBuildContextPackMessage|TestRunInjectsStoredContextPack|TestRunOmitsContextPackWhenEmpty|TestRunAddsPlanToContextPackForActionCalls' -v
```

Expected: FAIL because prompt helper and runner integration do not exist.

- [ ] **Step 4: Add prompt helper**

Modify `internal/agent/prompts.go` imports:

```go
"marshal/internal/contextpack"
```

Add:

```go
func BuildContextPackMessage(pack contextpack.Pack) (schema.ChatMessage, bool) {
	rendered := contextpack.Render(pack)
	if rendered == "" {
		return schema.ChatMessage{}, false
	}
	return schema.ChatMessage{Role: schema.RoleUser, Content: rendered}, true
}
```

- [ ] **Step 5: Add runner injection**

Modify `internal/agent/runner.go` imports:

```go
"marshal/internal/contextpack"
```

Add helper functions near `Run`:

```go
func appendContextPackMessage(messages []schema.ChatMessage, pack contextpack.Pack) []schema.ChatMessage {
	if msg, ok := BuildContextPackMessage(pack); ok {
		return append(messages, msg)
	}
	return messages
}

func packWithPlan(pack contextpack.Pack, plan []string, now func() time.Time) contextpack.Pack {
	input := contextpack.BuildInput{
		Plan:      plan,
		MaxTokens: pack.TokenUsage.MaxTokens,
		Now:       now,
	}
	for _, section := range pack.Sections {
		switch section.Kind {
		case contextpack.SectionRepoCard:
			input.RepoCard = section.Content
		case contextpack.SectionFileSnippet:
			input.FileSnippets = append(input.FileSnippets, contextpack.FileSnippet{
				Path:    section.Title,
				Content: section.Content,
			})
		case contextpack.SectionToolOutput:
			input.RecentToolOutput = append(input.RecentToolOutput, contextpack.ToolOutput{
				ToolName: section.Title,
				Summary:  section.Content,
			})
		}
	}
	return contextpack.NewBuilder().Build(input)
}
```

In `Run`, replace this initial message construction:

```go
messages := []schema.ChatMessage{
	BuildSystemPrompt(r.Registry.List()),
	{Role: schema.RoleUser, Content: goal},
}
```

with:

```go
messages := []schema.ChatMessage{
	BuildSystemPrompt(r.Registry.List()),
}
messages = appendContextPackMessage(messages, r.State.ContextPack())
messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
```

After planning succeeds and `task.Plan = splitPlanLines(planText)`, add:

```go
if current := r.State.ContextPack(); !current.IsEmpty() {
	updatedPack := packWithPlan(current, task.Plan, r.Now)
	r.State.SetContextPack(updatedPack)
	messages = []schema.ChatMessage{BuildSystemPrompt(r.Registry.List())}
	messages = appendContextPackMessage(messages, updatedPack)
	messages = append(messages, schema.ChatMessage{Role: schema.RoleUser, Content: goal})
}
```

Keep the existing assistant plan append immediately after this block.

- [ ] **Step 6: Run agent tests**

Run:

```bash
go test ./internal/agent -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/prompts.go internal/agent/prompts_test.go internal/agent/runner.go internal/agent/runner_test.go
git commit -m "feat(agent): inject context packs into prompts"
```

---

### Task 4: Render Context Pack Summary In The TUI

**Files:**
- Modify: `internal/app/tui/model.go`
- Modify: `internal/app/tui/model_test.go`

**Interfaces:**
- Consumes: `session.State.ContextPack`.
- Produces: read-only `Context` panel in `Model.View()`.

- [ ] **Step 1: Write failing TUI tests**

Add import to `internal/app/tui/model_test.go`:

```go
"marshal/internal/contextpack"
```

Update `TestViewContainsExpectedPanels` expected strings to include:

```go
"Context",
```

Append:

```go
func TestViewShowsEmptyContextPanel(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	model := New(state)

	view := model.View()
	if !strings.Contains(view, "Context") {
		t.Fatalf("View() missing Context panel:\n%s", view)
	}
	if !strings.Contains(view, "No context pack built yet.") {
		t.Fatalf("View() missing empty context message:\n%s", view)
	}
}

func TestViewShowsContextPackSummary(t *testing.T) {
	state := session.New(config.Default(), "/repo", time.Unix(100, 0), session.Persistence{})
	state.SetContextPack(contextpack.Pack{
		Sections: []contextpack.Section{
			{
				Kind:            contextpack.SectionRepoCard,
				Title:           "Repo Card",
				Source:          "repo.card",
				Content:         "Project: marshal",
				EstimatedTokens: 4,
			},
		},
		TokenUsage: contextpack.TokenUsage{MaxTokens: 12000, EstimatedTokens: 4},
	})
	model := New(state)

	view := model.View()
	for _, want := range []string{
		"Context Pack: 4/12000 tokens",
		"repo_card",
		"Repo Card",
		"repo.card",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/app/tui -run 'TestViewContainsExpectedPanels|TestViewShowsEmptyContextPanel|TestViewShowsContextPackSummary' -v
```

Expected: FAIL because the view has no `Context` panel.

- [ ] **Step 3: Add context panel rendering**

In `internal/app/tui/model.go`, after the Tool Log section and before Diff, add:

```go
	fmt.Fprintf(&b, "\nContext\n")
	pack := m.state.ContextPack()
	if pack.IsEmpty() {
		fmt.Fprintf(&b, "  No context pack built yet.\n")
	} else {
		fmt.Fprintf(&b, "  Context Pack: %d/%d tokens\n", pack.TokenUsage.EstimatedTokens, pack.TokenUsage.MaxTokens)
		for _, section := range pack.Sections {
			source := section.Source
			if source == "" {
				source = "no source"
			}
			fmt.Fprintf(&b, "  [%s] %s (%s, %d tokens)\n", section.Kind, section.Title, source, section.EstimatedTokens)
		}
	}
```

- [ ] **Step 4: Run TUI tests**

Run:

```bash
go test ./internal/app/tui -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/app/tui/model.go internal/app/tui/model_test.go
git commit -m "feat(tui): show context pack summary"
```

---

### Task 5: Final Verification And Milestone Tracking

**Files:**
- Modify: `docs/10-mvp-implementation-checklist.md`
- Create: `docs/plans/2026-07-02-milestone-k-context-pack.md`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: checked-off Milestone K items and a completed task status table.

- [ ] **Step 1: Run full test suite**

Run:

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 2: Update MVP checklist**

In `docs/10-mvp-implementation-checklist.md`, change Milestone K to:

```markdown
## Milestone K: Context pack v1

- [x] Build context pack from repo card
- [x] Include selected file snippets
- [x] Include recent tool output
- [x] Include current plan
- [x] Track approximate token usage
- [x] Add context browser in TUI
```

- [ ] **Step 3: Add task status doc**

Create `docs/plans/2026-07-02-milestone-k-context-pack.md`:

```markdown
| Task | Status | Details |
| --- | --- | --- |
| Task 1: Add Pure Context Pack Package | completed | Added deterministic pack types, builder, token estimation, budget handling, and renderer |
| Task 2: Store Context Packs On Session State | completed | Added copy-safe session storage for the current context pack |
| Task 3: Inject Context Packs Into Agent Prompts | completed | Added context-pack prompt helper and runner injection, including plan refresh after planning |
| Task 4: Render Context Pack Summary In The TUI | completed | Added read-only Context panel with token and section summary |
| Task 5: Final Verification And Milestone Tracking | completed | Ran full tests and updated Milestone K checklist |
```

- [ ] **Step 4: Run full test suite again**

Run:

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 5: Check worktree status**

Run:

```bash
git status --short
```

Expected: only `docs/10-mvp-implementation-checklist.md` and `docs/plans/2026-07-02-milestone-k-context-pack.md` are modified/untracked.

- [ ] **Step 6: Commit**

```bash
git add docs/10-mvp-implementation-checklist.md docs/plans/2026-07-02-milestone-k-context-pack.md
git commit -m "docs: mark Milestone K context pack complete"
```

- [ ] **Step 7: Final verification**

Run:

```bash
go test ./...
git status --short
```

Expected: tests pass and `git status --short` prints no output.
