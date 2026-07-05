# Agent Skills v1 Design

## Goal

Give Marshal agents the ability to use skill `.md` files — markdown files with TOML frontmatter that inject specialized instructions and workflows into the agent's context. Skills are auto-detected by the LLM via a listing in the system prompt and activated with a `skill.load` tool call. The design leaves a seam for v2 tool injection from skill files.

## Scope

In scope:

- Skill files in `.marshal/skills/` (project-local) and `~/.config/marshal/skills/` (global), with project skills overriding global ones by name.
- TOML frontmatter for metadata (`name`, `description`, `risk`, and optional future fields like `tools`).
- A `skill.load` tool in the tool registry that the agent calls to activate a skill.
- The system prompt lists available skills (name + description) so the LLM can decide which to load.
- Active skills are tracked so the system prompt distinguishes "available" from "active".
- Skill content counts against the context pack token budget.

Out of scope for v1:

- Tool injection from skill files (the `tools` frontmatter field is parsed but ignored).
- MCP-based skill tool registration.
- Builtin/handler-based tool definitions (only `shell` is relevant for future v2).
- Skills that introduce new agent roles or change routing rules.
- Auto-reloading skills when files change on disk (startup-only scan).

## Package Placement

New top-level package `internal/skills/`, sibling to `internal/agent` and `internal/contextpack`. It owns skill parsing, loading, and the `skill.load` tool registration. It depends on `internal/tools/registry`, `internal/app/session`, `internal/contextpack` — the same dependency shape `internal/agent` has today.

## Skill File Format

A skill file is a `.md` file with TOML frontmatter delimited by `+++`:

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

Frontmatter fields:

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | yes | string | Unique identifier; used in `skill.load <name>` |
| `description` | yes | string | Shown in the system prompt listing so the LLM can decide relevance |
| `risk` | no | string | Defaults to `"read_only"`; future: determines approval level for tools registered by this skill |
| `tools` | no (v2) | array of tool defs | Parsed but ignored in v1; future: tool definitions to register on load |

The body after frontmatter is markdown, injected verbatim into context as a system message when the skill is loaded.

### Why TOML frontmatter (not YAML)

Marshal already depends on `github.com/pelletier/go-toml/v2` for config parsing. Using TOML for skill frontmatter avoids adding a YAML dependency. The `+++` delimiter is the standard TOML convention for frontmatter in content files.

### Future v2 tool definitions

The `tools` field is parsed but ignored in v1. The shape is:

```toml
[[tools]]
name = "kubectl_get_pods"
description = "List pods in a namespace"
risk = "command"
schema = '''
{"type": "object", "properties": {"namespace": {"type": "string"}}}
'''
handler = "shell"
command = "kubectl get pods -n {{.namespace}}"
```

Tools would be namespaced by skill (e.g., `skill.kubectl_get_pods`) when registered into the tool registry.

## Core Types

`internal/skills/skill.go`:

```go
type Skill struct {
    Name        string
    Description string
    Risk        string
    Body        string // the markdown content after frontmatter
    Tools       []ToolDef // parsed but ignored in v1
}

type ToolDef struct {
    Name        string
    Description string
    Risk        string
    Schema      string // JSON Schema string
    Handler     string // "shell", "builtin", "mcp"
    Command     string // template for shell handler
}

type Index struct {
    skills map[string]Skill // name -> skill
}

func (idx *Index) Load(name string) (Skill, bool)
func (idx *Index) List() []Skill // returns sorted by name
```

`internal/skills/loader.go`:

```go
// LoadSkills scans globalDir then projectDir for *.md files with TOML frontmatter.
// Project skills override global skills with the same name.
// Returns nil error if neither directory exists.
func LoadSkills(globalDir, projectDir string) (*Index, error)

// parseFile reads a single .md file and extracts Skill from frontmatter + body.
func parseFile(path string) (Skill, error)
```

`internal/skills/tool.go`:

```go
// RegisterTool registers the skill.load tool in the given registry.
// It receives the Index so the handler can look up skills at call time.
// Also accepts the session State so it can inject skill body into messages
// and track active skills.
func RegisterTool(reg *registry.Registry, idx *Index, state *session.State)
```

## Data Flow

### Startup (in `app.Run`)

```
app.Run()
 ├─ config.Load()
 ├─ skills.LoadSkills(globalSkillsDir, projectSkillsDir) → *Index
 ├─ buildAgentRunner(...)  // receives Index + State
 │   ├─ registry.RegisterAll(skills.RegisterTool(index, state))
 │   └─ runner.Index = index  // passed to BuildSystemPrompt
 └─ tui.Run(...)
```

### Agent turn (in `Runner.Run`)

```
Runner.Run(ctx, goal)
 ├─ AddMessage(RoleUser, goal)
 ├─ classify(goal)
 ├─ resolveRoute()
 ├─ BuildSystemPrompt(role, tools, index, state.ActiveSkills())
 │   └─ renders "Available Skills" or "Active Skills" section
 ├─ BuildContextPackMessage(pack) + goal → messages
 ├─ [plan if not question]
 │
 LOOP:
    ├─ provider.Chat(messages) → streaming text
    ├─ ParseAction(text) → ModelAction
    ├─ if action.tool == "skill.load":
    │   ├─ Registry.Lookup("skill.load")
    │   ├─ handler: looks up skill in index
    │   ├─ injects skill body as system message into transcript
    │   ├─ tracks skill as "active" in state (ActiveSkills)
    │   ├─ rebuilds messages with updated skills section
    │   └─ returns confirmation to LLM
    ├─ ... other tools ...
```

### Skill load handler

The `skill.load` handler:

1. Parses args to get the skill name
2. Looks up `Index.Load(name)` — returns error if not found
3. Checks context budget: estimates tokens of skill body, returns error if it would exceed the remaining budget
4. Injects skill body as a system message via `state.AddMessage(RoleSystem, body)`
5. Records the skill name in active skills tracking (on `State`)
6. Returns a result confirming the skill was loaded

## System Prompt Integration

`internal/agent/prompts.go` `BuildSystemPrompt` gains parameters for the skill index and active skills set.

When no skills are active, the prompt includes:

```
## Available Skills
- `systematic-debugging` — Systematic debugging process for bugs, test failures, and unexpected behavior
- `kubernetes-deploy` — Safe deployment workflows for Kubernetes clusters

No skills are active. Call skill.load <name> to activate a skill when relevant to the task.
```

When one or more skills are active, the section becomes:

```
## Active Skills
- `systematic-debugging` — (Injected into context above)
```

The listing of inactive skills is omitted when skills are loaded (the LLM already has the skill instructions in context and doesn't need the listing repeated).

## Active Skills Tracking

Active skills are tracked on `session.State`:

```go
// On State struct:
activeSkills map[string]bool  // guarded by mutex

func (s *State) ActivateSkill(name string)
func (s *State) DeactivateSkill(name string)
func (s *State) ActiveSkills() []string
func (s *State) HasActiveSkill(name string) bool
```

Active skills persist for the session. There is no `skill.unload` in v1 — once loaded, a skill stays active. The `BuildSystemPrompt` method uses `ActiveSkills()` to know which listing to render.

## Context Budget Integration

Skill body content counts against the context pack token budget. When `skill.load` is called:

1. Estimate tokens of the skill body using the existing `contextpack.EstimateTokens` heuristic
2. Compare against remaining budget: `maxTokens - pack.EstimatedTokens()`
3. If the skill body exceeds remaining budget, return an error: "Cannot load skill: body is ~N tokens but only M tokens remain in context budget. Free space first."
4. If it fits, proceed with injection

The context pack's `EstimatedTokens` is approximate (it estimates rendered pack tokens, not the full message list), so the gate is advisory, not a hard guarantee. The LLM sees truncation if total context exceeds the model's window, same as today.

## Changes to Existing Code

| File | Change |
|------|--------|
| `internal/app/app.go` | After config load, call `skills.LoadSkills()`. Pass `Index` and `State` to runner and prompt builder. |
| `internal/agent/runner.go` | `Runner` gains `skillIndex *skills.Index` field. After building system prompt, rebuilds messages if skill activation changes the listing. |
| `internal/agent/prompts.go` | `BuildSystemPrompt` gains `skillIndex *skills.Index` and `activeSkills []string` params. Renders skills section. |
| `internal/tools/native/native.go` | `RegisterAll` gains `skills.RegisterTool(index, state)` call to register `skill.load`. |
| `internal/app/session/state.go` | New fields `activeSkills map[string]bool` with `ActivateSkill`/`DeactivateSkill`/`ActiveSkills`/`HasActiveSkill` methods. |
| `internal/skills/` | New package: `skill.go`, `loader.go`, `tool.go`. |

### Message rebuild after skill activation

When `skill.load` injects the skill body as a system message, `BuildSystemPrompt` needs to reflect the updated active skills in the next iteration. `BuildSystemPrompt` is called once at the top of `Run` (before the loop), so the runner needs a way to update the first system message when the active skills set changes.

Approach: the runner's main loop tracks the last-rendered active skills set. At the top of each iteration (before `provider.Chat`), it compares `state.ActiveSkills()` to the last-rendered set. If they differ, it replaces the first system message in the transcript with a freshly-built prompt that includes the updated skills section. No special flag or handler callback needed — just a comparison of active skills between iterations.

This means the `skill.load` handler only needs to:
1. Look up the skill in the index
2. Inject the body as a system message
3. Call `state.ActivateSkill(name)`

The runner handles prompt rebuilding on its own, keeping the handler decoupled from message construction.

## Error Handling

- A missing skill directory (no `.marshal/skills/` or `~/.config/marshal/skills/`) is not an error — `LoadSkills` returns an empty index.
- A `.md` file with no TOML frontmatter or missing required fields (name, description) logs a warning and is skipped — other valid skills still load.
- A file with a duplicate name: the last one loaded wins (project overrides global), consistent with config merge semantics.
- `skill.load` for a nonexistent name returns a tool error: `unknown skill "foo". Available: ...`
- `skill.load` for an already-active skill returns a tool error: `skill "foo" is already active.`
- `skill.load` when skill body exceeds context budget: tool error with token counts.
- A skill with TOML parse errors in the `tools` field (future v2) logs a warning and loads with zero tools — the skill instructions still work.

## Testing

- `internal/skills/skill_test.go` (new): `parseFile` with valid frontmatter, missing required fields, no frontmatter, empty body.
- `internal/skills/loader_test.go` (new): `LoadSkills` with both dirs present, project-only, global-only, neither dir present, project override of global, skip non-.md files, duplicate names.
- `internal/skills/tool_test.go` (new): `skill.load` handler — success injects message and tracks active skill; unknown name; already-active name; context budget exceed; skill not found.
- `internal/agent/prompts_test.go` (extend): system prompt includes skills section with available skills; shows "No skills are active" when none loaded; shows active skills when loaded; omits available listing when skills are active.
- `internal/agent/runner_test.go` (extend): `Runner.Run` with `skill.load` tool call in the action loop — skill body injected, next iteration has updated prompt.
- `internal/app/session/state_test.go` (extend): `ActivateSkill`/`DeactivateSkill`/`ActiveSkills`/`HasActiveSkill` roundtrip, concurrent access safety.

## Acceptance Criteria

- `go build ./cmd/marshal` and `go test ./...` succeed.
- Placing a `.md` file with valid TOML frontmatter in `.marshal/skills/` makes it discoverable at startup.
- The system prompt lists available skills by name and description.
- An agent calling `skill.load <name>` receives the skill's markdown body injected into context.
- A subsequent `skill.load` call for the same skill name returns an error.
- The updated system prompt reflects the active skill.
- A missing or empty skills directory causes no errors or warnings.
- The existing agent loop (non-skill tool calls, planning, final answer) is unchanged.
- Skill content that would exceed the context budget is rejected with a descriptive error.
