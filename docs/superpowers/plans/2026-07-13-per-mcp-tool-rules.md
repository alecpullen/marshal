# Per-MCP-Tool Safety Rules Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close `docs/11-roadmap-and-future-enhancements.md` Feature #3's remaining gap. Allow users to write per-MCP-tool safety rules in the existing `[permissions.rules]` block — so a rule whose `permission` field is `"mcp.github.create_issue"` (the tool name) applies to that specific MCP tool, with the rule's `action` overriding the default confirm fallback.

**Architecture:** Extend `evaluateSubjects` to recognize MCP tool names. Today the F4 permission rules path (`evaluateSubjects` at `internal/tools/policy/policy.go:462`) runs only for native tools via `PermissionForTool(toolName)`. MCP tools short-circuit at line 78 before reaching that path. The fix: after the MCP `Policies` map check, fall through to the F4 rules path (just like native tools) so a rule with `permission = "mcp.<server>.<tool>"` can match the exact MCP tool. The rule's `action` then overrides the default confirm fallback. The existing namespace-prefix match in `MCP.Policies` is preserved as the highest-priority check.

**Tech Stack:** Go 1.26.1, stdlib only (no new deps). Builds on the existing `permissions.Rule` shape and `permissions.Evaluate`.

**Assumes Milestone R is complete** (it is). The F4 rule path is already live for native tools.

## Global Constraints

- **Backward compatible:** a project that has no `[permissions.rules]` entries for MCP tools continues to work as today. The fall-through is additive.
- **Priority order in `Evaluate`**: (1) conservative guardrail, (2) MCP `Policies` exact match, (3) MCP `Policies` pattern match, (4) F4 rules (new), (5) default MCP confirm fallback. A deny at any earlier step wins.
- **No new config blocks.** The existing `[permissions.rules]` with a `permission = "mcp.<server>.<tool>"` field is the surface. The doc-comment on `PermissionRule.Permission` is updated to mention MCP tools.
- **No comments unless asked.** Match existing gofmt style.
- **Verification:** `go test -count=1 ./internal/tools/policy/... ./internal/app/config/... ./internal/app/tui/settings/...` after every task; full gates before batch closeout.

## File Structure

**Modify:**
- `internal/tools/policy/policy.go` — in the MCP branch of `Evaluate` (around line 78-108), after the pattern-match loop, fall through to the F4 rules evaluation. Add a comment-free call to `evaluateSubjects` with `permissions.PermissionForTool(toolName)`.
- `internal/permissions/permissions.go` (if it has a doc comment on `Rule.Permission` that mentions "tool name") — add MCP mention. If the comment doesn't exist, skip this file.
- `docs/09-configuration-examples.md` — add a sample `[permissions.rules]` block targeting `mcp.github.create_issue`.
- `docs/11-roadmap-and-future-enhancements.md` — mark Feature #3 shipped.
- `docs/13-project-audit-2026-07-11.md` — append a new "Implementation batch — per-MCP-tool safety rules" section.

**Add tests:**
- `internal/tools/policy/policy_test.go` — new tests for the per-MCP-tool rule path.

---

## Task 1: F4 rules fall-through for MCP tools

**Files:**
- Modify: `internal/tools/policy/policy.go`
- Test: `internal/tools/policy/policy_test.go`

**Interfaces:**
- Produces: an `Evaluate(toolName, args)` call whose `toolName` is an MCP tool name (e.g. `mcp.github.create_issue`) returns the matching F4 rule's decision when one is configured. Falls through to the default `DecisionConfirm` when none is.

- [ ] **Step 1: Read the current MCP branch**

Open `internal/tools/policy/policy.go` lines 78-108. Confirm the structure: (1) exact match, (2) pattern match, (3) default confirm.

- [ ] **Step 2: Write the failing tests**

In `internal/tools/policy/policy_test.go` add 3 tests:

```go
func TestEvaluateMCPToolFallsThroughToF4Rule(t *testing.T) {
    cfg := &config.Config{
        Permissions: config.PermissionsConfig{
            Rules: []config.PermissionRule{
                {Permission: "mcp.github.create_issue", Action: "allow"},
            },
        },
    }
    pe := NewEngine(cfg, nil)
    dec, reason, err := pe.Evaluate("mcp.github.create_issue", map[string]interface{}{})
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if dec != DecisionAllow {
        t.Fatalf("dec = %s, want allow (resolved by F4 rule); reason = %s", dec, reason)
    }
}

func TestEvaluateMCPToolDenyRuleWins(t *testing.T) {
    cfg := &config.Config{
        MCP: config.MCPConfig{
            Policies: map[string]string{
                "mcp.github.delete_repo": "allow",
            },
        },
        Permissions: config.PermissionsConfig{
            Rules: []config.PermissionRule{
                {Permission: "mcp.github.delete_repo", Action: "deny"},
            },
        },
    }
    pe := NewEngine(cfg, nil)
    dec, _, err := pe.Evaluate("mcp.github.delete_repo", map[string]interface{}{})
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if dec != DecisionDeny {
        t.Fatalf("dec = %s, want deny (F4 deny must beat MCP policy allow)", dec)
    }
}

func TestEvaluateMCPToolUnmatchedFallsBackToConfirm(t *testing.T) {
    cfg := &config.Config{
        Permissions: config.PermissionsConfig{
            Rules: []config.PermissionRule{
                {Permission: "mcp.other.tool", Action: "allow"},
            },
        },
    }
    pe := NewEngine(cfg, nil)
    dec, reason, err := pe.Evaluate("mcp.github.create_issue", map[string]interface{}{})
    if err != nil {
        t.Fatalf("Evaluate: %v", err)
    }
    if dec != DecisionConfirm {
        t.Fatalf("dec = %s, want confirm (no rule matched, default fallback); reason = %s", dec, reason)
    }
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test -count=1 ./internal/tools/policy/ -run 'TestEvaluateMCPTool' -v`
Expected: FAIL. The current `Evaluate` returns `DecisionConfirm` for ALL MCP tool calls because the F4 rules path is never reached.

- [ ] **Step 4: Add the fall-through**

In `internal/tools/policy/policy.go`, replace the `return DecisionConfirm, ...` at line 107 with:

```go
// F4 permission rules: a rule whose `permission` field equals the MCP
// tool name (e.g. "mcp.github.create_issue") can override the default
// confirm fallback. The MCP.Policies exact match and pattern match above
// are still checked first; this fall-through is the third priority.
subjects := subjectsForTool(toolName, args, "")
if dec, matched := evaluateSubjects(pe.rules, permissions.PermissionForTool(toolName), subjects); matched {
    return dec, "resolved by permission rule", nil
}

return DecisionConfirm, "requires approval (unconfigured MCP tool secure default)", nil
```

Notes:
- `normCmd` is empty for MCP tools (no shell command to normalize).
- `subjectsForTool("mcp.github.create_issue", args, "")` falls into the `default` case and returns `[]string{"mcp.github.create_issue"}` (line 458).
- `permissions.PermissionForTool("mcp.github.create_issue")` returns the same string (the existing helper just returns its input for non-mapped tool names).

Verify `permissions.PermissionForTool` behavior: open `internal/permissions/permissions.go` and check. If it only handles a hard-coded set of native tools, extend it (or just pass `toolName` directly to `evaluateSubjects`).

If the existing helper doesn't return `toolName` for unknown names, the cleanest fix is: in the MCP branch, call `evaluateSubjects(pe.rules, toolName, subjects)` directly instead of going through `PermissionForTool`. The brief's snippet is aspirational — adapt to the actual code shape.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -count=1 ./internal/tools/policy/ -run 'TestEvaluateMCPTool' -v`
Expected: PASS.

- [ ] **Step 6: Verify no regression in existing tests**

Run: `go test -count=1 ./internal/tools/policy/`
Expected: all existing tests still PASS.

- [ ] **Step 7: Vet and format**

Run: `gofmt -w internal/tools/policy/policy.go internal/tools/policy/policy_test.go` and `go vet ./internal/tools/policy/`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/tools/policy/policy.go internal/tools/policy/policy_test.go
git commit -m "feat(policy): allow F4 rules to target specific MCP tools"
```

---

## Task 2: Update PermissionRule doc comment

**Files:**
- Modify: `internal/app/config/config.go` (the doc on `PermissionRule.Permission`, lines 102-106) — OR the matching doc in `internal/permissions/permissions.go` if that's where the canonical doc lives.

- [ ] **Step 1: Find the doc comment**

`grep -n "Permission string" internal/app/config/config.go internal/permissions/permissions.go` and read the existing comment.

- [ ] **Step 2: Update the comment**

If the existing comment is:
```go
type PermissionRule struct {
    Permission string `toml:"permission"`
    Pattern    string `toml:"pattern"`
    Action     string `toml:"action"`
}
```
without a doc comment, add ONE line above the struct:
```go
// PermissionRule maps a tool name (e.g. "mcp.github.create_issue" or a
// native tool subject) to a decision. The rule fires when Permission
// equals the tool's effective permission and either Pattern is empty
// (exact match) or matches the resolved subject.
type PermissionRule struct {
```

If a doc comment already exists, extend it with the MCP note (e.g., "Permission may also be the full MCP tool name like `mcp.github.create_issue`.").

- [ ] **Step 3: Commit**

```bash
git add internal/app/config/config.go
git commit -m "docs(config): document PermissionRule.MCP tool support"
```
(Or whichever file was modified.)

---

## Task 3: Docs + audit log

**Files:**
- Modify: `docs/09-configuration-examples.md`
- Modify: `docs/11-roadmap-and-future-enhancements.md`
- Modify: `docs/13-project-audit-2026-07-11.md`

- [ ] **Step 1: Add a config example**

In `docs/09-configuration-examples.md`, find the `[permissions]` section (or add one if absent) and add:

```toml
[permissions.rules]
"mcp.github.create_issue" = "allow"
"mcp.github.delete_repo"   = "deny"
"mcp.filesystem.read"      = "confirm"
"mcp.filesystem.write"     = "confirm"
```

- [ ] **Step 2: Mark Feature #3 shipped**

In `docs/11-roadmap-and-future-enhancements.md`, replace the Feature #3 section with:

```markdown
## 3. Fine-Grained Safety Policies — SHIPPED (see `docs/13-project-audit-2026-07-11.md`, batch "per-MCP-tool safety rules")

Regex guardrails, hard-coded conservative blocks, and per-tool F4 rules
ship today. Per-MCP-tool rules are supported via
`[permissions.rules]` with `permission = "mcp.<server>.<tool>"`. The
existing `[mcp.policies]` namespace-prefix match remains as the
highest-priority check.
```

- [ ] **Step 3: Add the audit-doc batch section**

In `docs/13-project-audit-2026-07-11.md`, append a new section:

```markdown
## Implementation batch — per-MCP-tool safety rules

The remaining gap in `docs/11` Feature #3 (per-MCP-tool safety rules)
was addressed by the following commits on branch
`feature/mcp-tool-rules`:

```
<commit> feat(policy): allow F4 rules to target specific MCP tools
<commit> docs(config): document PermissionRule.MCP tool support
```

### What changed

- `internal/tools/policy/policy.go`: the MCP branch of `Evaluate` now
  falls through to the F4 rules path after the existing
  `[mcp.policies]` exact-match and pattern-match checks. A rule whose
  `permission` field equals the MCP tool name (e.g.
  `mcp.github.create_issue`) overrides the default confirm fallback.
- `PermissionRule.Permission` doc comment now mentions MCP tools.

### Unchanged

- `[mcp.policies]` namespace-prefix match remains the highest-priority
  check. A user who wants to allow `mcp.github.*` can do so with one
  entry; a user who wants to deny `mcp.github.delete_repo` specifically
  can do so with another.
- The default confirm fallback for unconfigured MCP tools is preserved.
```

Use the real commit hashes after the batch lands.

- [ ] **Step 4: Commit**

```bash
git add docs/09-configuration-examples.md docs/11-roadmap-and-future-enhancements.md docs/13-project-audit-2026-07-11.md
git commit -m "docs(policy): document per-MCP-tool safety rules"
```

---

## Batch closeout

After Task 3, run the full verification gates:

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go build ./cmd/marshal
```

Update the `## Dated resolution note` section of `docs/13-project-audit-2026-07-11.md` with a one-paragraph entry citing the actual commit range and branch.

---

## Self-Review

**Spec coverage:**
- A user with `[permissions.rules]` entries whose `permission` field is an MCP tool name (e.g. `mcp.github.create_issue`) now gets per-tool control over that MCP tool. `deny` wins over `[mcp.policies]` allow, allow beats the default confirm fallback.
- Backward compatible: a project with no such entries behaves exactly as before (the fall-through is a no-op for unmatched rules).
- Doc comment on `PermissionRule` updated to mention MCP tools.

**Type consistency:**
- The fall-through reuses the existing `evaluateSubjects` and `subjectsForTool` helpers. No new types.
- The `permission` field stays a plain `string`; the user can put any tool name (native or MCP) there.

**Placeholder scan:** No TBDs. The implementer should adapt to the actual `PermissionForTool` helper behavior (Task 1 Step 4 has a fallback if it doesn't pass through the input).
