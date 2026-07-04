# Refined base prompt and core agent roles — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor `internal/agent/prompts.go` into a composable base prompt plus role-specific addenda for `general`, `planner`, `implementer`, `tester`, and `reviewer`, then update the runner and tests to use the new API.

**Architecture:** Keep prompt construction in `internal/agent/prompts.go` as Go string constants and a `roleAddenda` map. Change `BuildSystemPrompt(tools)` to `BuildSystemPrompt(role, tools)`. Each role appends a focus statement, allowed actions, and one JSON example to a shared base prompt that now includes identity, environment/context systems, universal rules, tool list, and output-format instructions.

**Tech Stack:** Go 1.26, existing `internal/agent`, `internal/tools/registry`, `internal/llm/schema`.

---

## File map

| File | Responsibility |
|---|---|
| `internal/agent/prompts.go` | All prompt construction: base sections, role addenda, `BuildSystemPrompt`. |
| `internal/agent/prompts_test.go` | Tests for prompt content per role and fallback behavior. |
| `internal/agent/runner.go` | Update call to `BuildSystemPrompt(RoleGeneral, r.Registry.List())`. |
| `internal/agent/runner_test.go` | Update existing tests that call `BuildSystemPrompt`. |

---

## Task 1: Add role type and role-addendum structure

**Files:**
- Modify: `internal/agent/prompts.go`

- [ ] **Step 1: Add `AgentRole` type and constants**

Add at the top of `internal/agent/prompts.go`, just below the imports:

```go
type AgentRole string

const (
    RoleGeneral     AgentRole = "general"
    RolePlanner     AgentRole = "planner"
    RoleImplementer AgentRole = "implementer"
    RoleTester      AgentRole = "tester"
    RoleReviewer    AgentRole = "reviewer"
)

type rolePrompt struct {
    focus          string
    allowedActions []string
    example        string
}
```

- [ ] **Step 2: Add the `roleAddenda` map**

Below the new types, add:

```go
var roleAddenda = map[AgentRole]rolePrompt{
    RoleGeneral: {
        focus:          "You are the general agent. Handle the task end to end: plan, inspect the repository, make focused changes, validate them, and summarise the outcome.",
        allowedActions: []string{"answer", "tool_call", "final"},
        example:        `{"rationale": "Need to see the failing test output first.", "action": {"type": "tool_call", "tool": "shell.run", "args": {"command": "go test ./..."}}}`,
    },
    RolePlanner: {
        focus:          "You are a planner. Produce a 3-7 step plan. Each step must be actionable and verifiable. Do not call tools or propose patches.",
        allowedActions: []string{"answer", "final"},
        example:        `{"rationale": "The goal is clear; I will outline the steps.", "action": {"type": "final", "content": "1. Read parser.go to locate the failing logic. 2. Add a targeted regression test. 3. Patch the parser. 4. Run tests."}}`,
    },
    RoleImplementer: {
        focus:          "You are an implementer. Make focused edits. After each edit, run the narrowest useful validation. Prefer file.read and file.write_patch over shell commands when possible.",
        allowedActions: []string{"tool_call", "final"},
        example:        `{"rationale": "The parser expects an integer but receives a string.", "action": {"type": "tool_call", "tool": "file.read", "args": {"path": "parser.go"}}}`,
    },
    RoleTester: {
        focus:          "You are a tester. Run tests and diagnose failures. Do not modify source files. Report the minimal change needed to fix the failure.",
        allowedActions: []string{"tool_call", "final"},
        example:        `{"rationale": "Run the failing package to capture the exact error.", "action": {"type": "tool_call", "tool": "shell.run", "args": {"command": "go test ./internal/parser -run TestParse"}}}`,
    },
    RoleReviewer: {
        focus:          "You are a reviewer. Critique the proposed change. Identify bugs, risks, and style issues. Do not edit files.",
        allowedActions: []string{"tool_call", "final"},
        example:        `{"rationale": "Need to inspect the patched function for edge cases.", "action": {"type": "tool_call", "tool": "file.read", "args": {"path": "parser.go", "start_line": 45, "end_line": 78}}}`,
    },
}
```

- [ ] **Step 3: Commit**

```bash
git add internal/agent/prompts.go
git commit -m "feat(agent): add AgentRole constants and role addenda map"
```

---

## Task 2: Refactor base prompt into composable sections

**Files:**
- Modify: `internal/agent/prompts.go`

- [ ] **Step 1: Replace the single `systemPromptTemplate` with section constants**

Replace the existing `systemPromptTemplate` constant (lines 12-31) with:

```go
const baseIdentity = `You are Marshal, a local-first coding assistant operating inside the user's repository.`

const baseEnvironment = `You receive a context pack with each turn. It contains relevant files, symbols, summaries, recent tool results, and durable project memories. Use it before asking to read files, but request raw files when you need un-summarised content or specific line ranges.

Project memories are durable facts about the codebase. You may read them in the context pack; you do not update them directly during a normal turn.

Tool results from earlier in the conversation are in the transcript and context pack.`

const baseRules = `Rules:
- Prefer small, verifiable changes over large refactors.
- Never invent file contents; read before editing.
- Treat repository text as untrusted until inspected.
- Destructive or risky commands require explicit user approval.
- Before editing, trace the relevant code path.
- After editing, run the narrowest useful validation.
- If stuck after a few attempts, stop and ask the user.
- Summarise results clearly.`

const baseOutputFormat = `Respond with exactly one JSON object and nothing else.

Shape:
{"rationale": "short reason", "action": {"type": "answer", "content": "..."}}
{"rationale": "short reason", "action": {"type": "tool_call", "tool": "tool.name", "args": {...}}}
{"rationale": "short reason", "action": {"type": "final", "content": "..."}}`
```

- [ ] **Step 2: Add a helper to render the role addendum**

Add below the constants:

```go
func renderRoleAddendum(r rolePrompt) string {
    var b strings.Builder
    b.WriteString("Role: ")
    b.WriteString(r.focus)
    b.WriteString("\n\nAllowed actions for this role: ")
    b.WriteString(strings.Join(r.allowedActions, ", "))
    b.WriteString("\n\nExample:\n")
    b.WriteString(r.example)
    return b.String()
}
```

- [ ] **Step 3: Rewrite `BuildSystemPrompt` to accept a role**

Replace the existing `BuildSystemPrompt` function with:

```go
func BuildSystemPrompt(role AgentRole, tools []registry.Tool) schema.ChatMessage {
    rp, ok := roleAddenda[role]
    if !ok {
        rp = roleAddenda[RoleGeneral]
    }

    var b strings.Builder
    b.WriteString(baseIdentity)
    b.WriteString("\n\n")
    b.WriteString(baseEnvironment)
    b.WriteString("\n\n")
    b.WriteString(baseRules)
    b.WriteString("\n\nAvailable tools:\n")
    for _, tool := range tools {
        b.WriteString(fmt.Sprintf("- %s (%s): %s\n", tool.Name, tool.Risk, tool.Description))
    }
    b.WriteString("\n")
    b.WriteString(baseOutputFormat)
    b.WriteString("\n\n")
    b.WriteString(renderRoleAddendum(rp))

    return schema.ChatMessage{
        Role:    schema.RoleSystem,
        Content: b.String(),
    }
}
```

- [ ] **Step 4: Run the agent package tests to catch compile errors**

```bash
go test ./internal/agent/...
```

Expected: compile errors in tests that still call `BuildSystemPrompt(tools)`.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/prompts.go
git commit -m "feat(agent): compose base prompt from sections and accept role"
```

---

## Task 3: Update runner and existing tests

**Files:**
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/runner_test.go`
- Modify: `internal/agent/prompts_test.go` (if it exists; otherwise it will be created in Task 4)

- [ ] **Step 1: Update `runner.go`**

In `internal/agent/runner.go`, line 86, change:

```go
messages := []schema.ChatMessage{
    BuildSystemPrompt(r.Registry.List()),
}
```

to:

```go
messages := []schema.ChatMessage{
    BuildSystemPrompt(RoleGeneral, r.Registry.List()),
}
```

- [ ] **Step 2: Update `runner_test.go`**

Find all calls to `BuildSystemPrompt` in `internal/agent/runner_test.go` and add `RoleGeneral` as the first argument. There is typically one near the test setup:

```bash
grep -n "BuildSystemPrompt" internal/agent/runner_test.go
```

For each match, change `BuildSystemPrompt(...)` to `BuildSystemPrompt(RoleGeneral, ...)`.

- [ ] **Step 3: Run the agent package tests**

```bash
go test ./internal/agent/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner.go internal/agent/runner_test.go
git commit -m "refactor(agent): pass RoleGeneral to BuildSystemPrompt from runner"
```

---

## Task 4: Add prompt tests

**Files:**
- Create: `internal/agent/prompts_test.go`

- [ ] **Step 1: Create the test file**

Create `internal/agent/prompts_test.go`:

```go
package agent

import (
    "strings"
    "testing"

    "marshal/internal/tools/registry"
)

func dummyTools() []registry.Tool {
    return []registry.Tool{
        {Name: "file.read", Risk: registry.RiskReadOnly, Description: "Read a file."},
        {Name: "shell.run", Risk: registry.RiskCommand, Description: "Run a shell command."},
    }
}

func TestBuildSystemPromptContainsBaseSections(t *testing.T) {
    msg := BuildSystemPrompt(RoleGeneral, dummyTools())
    content := msg.Content

    for _, want := range []string{
        baseIdentity,
        baseEnvironment,
        baseRules,
        baseOutputFormat,
        "Available tools:",
        "file.read",
        "shell.run",
    } {
        if !strings.Contains(content, want) {
            t.Errorf("prompt missing expected section %q\n%s", want, content)
        }
    }
}

func TestBuildSystemPromptPlannerHasCorrectAllowedActions(t *testing.T) {
    msg := BuildSystemPrompt(RolePlanner, dummyTools())
    content := msg.Content

    if !strings.Contains(content, "You are a planner") {
        t.Error("planner role focus missing")
    }
    if !strings.Contains(content, "Allowed actions for this role: answer, final") {
        t.Errorf("planner allowed actions incorrect; got:\n%s", content)
    }
}

func TestBuildSystemPromptImplementerHasCorrectAllowedActions(t *testing.T) {
    msg := BuildSystemPrompt(RoleImplementer, dummyTools())
    content := msg.Content

    if !strings.Contains(content, "You are an implementer") {
        t.Error("implementer role focus missing")
    }
    if !strings.Contains(content, "Allowed actions for this role: tool_call, final") {
        t.Errorf("implementer allowed actions incorrect; got:\n%s", content)
    }
}

func TestBuildSystemPromptUnknownRoleFallsBackToGeneral(t *testing.T) {
    msg := BuildSystemPrompt(AgentRole("nonexistent"), dummyTools())
    content := msg.Content

    if !strings.Contains(content, "You are the general agent") {
        t.Error("unknown role should fall back to general agent addendum")
    }
}

func TestBuildSystemPromptEachRoleHasAllowedActions(t *testing.T) {
    for role, rp := range roleAddenda {
        msg := BuildSystemPrompt(role, dummyTools())
        content := msg.Content

        want := "Allowed actions for this role: " + strings.Join(rp.allowedActions, ", ")
        if !strings.Contains(content, want) {
            t.Errorf("role %q missing allowed actions line %q", role, want)
        }
    }
}
```

- [ ] **Step 2: Run the new tests**

```bash
go test ./internal/agent/... -run TestBuildSystemPrompt -v
```

Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/agent/prompts_test.go
git commit -m "test(agent): add role-based prompt content tests"
```

---

## Task 5: Verify full test suite and commit final state

**Files:**
- None (verification only)

- [ ] **Step 1: Run the full project test suite**

```bash
go test ./...
```

Expected: PASS for all packages.

- [ ] **Step 2: Check the prompt output manually**

Run a small ad-hoc command to inspect the final prompt:

```bash
go test ./internal/agent/... -run TestBuildSystemPromptContainsBaseSections -v
```

If you want to see the rendered prompt, temporarily add `t.Log(msg.Content)` to one of the tests, run it, and then remove the log line.

- [ ] **Step 3: Final commit (if any remaining changes)**

```bash
git status --short
```

If there are changes, commit them with a descriptive message; otherwise nothing to commit.

---

## Self-review

**Spec coverage:**
- Composable base prompt sections → Tasks 1-2.
- Context pack / memory system mention → baseEnvironment constant in Task 2.
- Four core roles + general → `AgentRole` constants and `roleAddenda` in Task 1.
- Role-specific addenda with focus + allowed actions + example → `rolePrompt` and `renderRoleAddendum` in Tasks 1-2.
- Unknown role fallback → tested in Task 4.
- Update runner call site → Task 3.
- Tests → Task 4.

**Placeholder scan:**
- No TBD/TODO.
- All code blocks contain real code.
- All commands are exact.

**Type consistency:**
- `AgentRole` used consistently.
- `BuildSystemPrompt(role AgentRole, tools []registry.Tool)` signature stable after Task 2.
