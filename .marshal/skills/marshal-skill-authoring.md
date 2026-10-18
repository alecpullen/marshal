---
name: marshal-skill-authoring
description: Use when adding, editing, or reviewing Marshal's built-in skills (internal/skills/builtin/) or this repository's project skills (.marshal/skills/). Decides where guidance belongs so this repo's specifics never ship inside the binary to every Marshal user.
risk: workspace_write
---

# Marshal Skill Authoring

Built-in skills are embedded in the binary (`//go:embed` in
`internal/skills/builtin.go`) and run in every project of every Marshal user,
in any language. This repository is only one of those projects.

## Where guidance belongs

| Guidance | Home |
|---|---|
| How to use Marshal's own tools, and general engineering practice | built-in skill (`internal/skills/builtin/`) |
| Facts about this repo: commands, layout, constraints | `AGENTS.md`, which is already in context every session |
| Procedures only this repo needs (e.g. reading Marshal's postmortems) | project skill (`.marshal/skills/`, tracked in this repo) |

**Placement test:** would this sentence be true and useful to someone using
Marshal on a Python web app? If not, it does not go in a built-in.

In a built-in, never name:

- this repo's commands (`CGO_ENABLED=1`, `go build ./cmd/marshal`, the usability
  suite);
- its packages or paths (`internal/app`, `internal/agent`, `web/bridge`);
- its test names, its known-failing tests, or "this codebase uses…".

Point at "the repository instructions (AGENTS.md or equivalent)" instead.
Examples use placeholders (`internal/foo`, `./pkg`) and are labelled with
their language ("in Go, …").

`TestBuiltInSkillsAreRepoNeutral` bans the common leaks. Passing it is not
proof; apply the placement test.

## Names

User and project skills load after the built-ins, and a skill with the same
name replaces the built-in entirely. Give a project skill its own name so it
adds to the built-in instead of hiding it, as `debugging-marshal` does next
to `systematic-debugging`. `TestProjectSkillsDoNotShadowBuiltIns` enforces
this for `.marshal/skills/`.

When a user reports that a built-in "does not behave as written", check
`~/.config/marshal/skills/` and `.marshal/skills/` for a skill of the same
name first.

## Mechanics of a built-in

1. Frontmatter: `name`, `description`, and `risk` (`read_only` or
   `workspace_write`).
   - The description is the trigger. It sits in every session's skill
     roster, so say when to use the skill and when not to, briefly.
2. Add the `//go:embed` variable and the map entry in `builtin.go`. Then add
   the name to `TestBuiltInSkillsAllLoad`.
3. `TestBuiltInSkillWorkflowContracts` pins key phrases. It matches against
   the **lowercased** body, so write expectations in lowercase.
4. Mention only tools that exist. Check the names and arguments in
   `internal/tools/native/`.

## Cost and consistency

- An active skill's body is re-sent on every turn, and `skills.max_active`
  caps how many can be active. Keep bodies short. Write procedures, not
  reference material.
- Skills point at each other (`using-skills` lists the chains), and tool code
  points at skills (for example, `workspace.go` says the `using-worktrees`
  skill covers judgment). When you change a skill's contract, grep for its
  name in `internal/` and `docs/` and keep the references true.

## Check the change in real sessions

The tests prove that the text says what you meant. They do not prove that
agents follow it. After changing a procedure skill, look at real sessions in
`.marshal/marshal.db`:

- `tool_calls` where `tool_name = 'skill.load'` shows what gets loaded and
  what loads together.
- `turn_metrics` where `role = 'subtask'` shows subagent duration,
  iterations and tokens. Child transcripts are not persisted.
- the postmortem reports (see `debugging-marshal`).
