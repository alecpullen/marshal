# 06. TUI Design

> **2026-07-05:** The dashboard layout described below is superseded by the
> single-column transcript design in
> `docs/superpowers/specs/2026-07-05-tui-simplification-design.md`
> (borderless transcript, symbol-bullet messages, one status line,
> `/context` + `/log` instead of the sidebar).

## Goal

The TUI should make the agent feel transparent, inspectable, and fast.

It should not just be a chat box. It should show plans, tool calls, diffs, context, and model activity.

## Main layout concept

```text
┌──────────────────────────── Marshal ────────────────────────────┐
│ Model: qwen2.5-coder:14b via Ollama   Repo: /project/marshal    │
├────────────── Chat ──────────────┬──────────── Context ────────┤
│ user: fix the failing tests       │ Relevant files              │
│ agent: I found parser failures... │ 1. parser.go                │
│                                  │ 2. parser_test.go           │
│ Tool: go test ./...              │ 3. token.go                 │
│ output: FAIL ...                 │                              │
├────────────── Diff ──────────────┼──────────── Plan ───────────┤
│ - old line                        │ ✓ inspect failure           │
│ + new line                        │ ✓ find parser logic         │
│                                  │ → patch parser              │
│                                  │   run tests                 │
└──────────────────────────────────┴─────────────────────────────┘
```

## Primary panels

| Panel | Purpose |
|---|---|
| Chat | Main user-agent interaction |
| Plan | Current task plan and progress |
| Diff | Proposed and applied code changes |
| Tool Log | Commands, searches, file reads, test output |
| Context | Selected files, symbols, summaries, repo map |
| Agents | Specialist/swarm agent state |
| Memory | Durable project facts |
| Config | Provider, model, profile, privacy settings |

## Modes

```text
Ask      answer questions only
Plan     create plan, no edits
Edit     propose patches
Auto     patch and test with approvals
Swarm    multi-agent workflow
```

## Keyboard shortcuts

```text
Ctrl+P    command palette
Ctrl+M    switch model/profile
Ctrl+T    tool log
Ctrl+D    diff view
Ctrl+R    repo map
Ctrl+A    agents
Ctrl+Y    approve action
Ctrl+N    deny action
Ctrl+E    edit proposed command
Ctrl+S    save session summary
Ctrl+K    context browser
```

## Command palette actions

Potential commands:

```text
Switch model profile
Toggle local-only mode
Run repo index
Show repo map
Show current context pack
Show project memories
Approve pending tool
Deny pending tool
Create git checkpoint
Export patch
Run tests
Summarise session
Open config
```

## Model visibility

The status bar should show:

```text
Agent: Implementer | Model: qwen2.5-coder:14b | Provider: Ollama | Local | Context: 18k/32k
```

For swarm mode:

```text
Planner: local_heavy | Repo Scout: fast | Tester: tiny | Reviewer: reasoning
```

## Tool approval UI

Example:

```text
Agent wants to run:

  go test ./...

Reason:
  Validate the package after modifying the parser.

Risk:
  Low - test command, no destructive flags detected.

[Enter] approve   [e] edit   [d] deny   [a] always allow go test
```

## Remote escalation UI

```text
Escalating reviewer:
  from qwen2.5-coder:14b
  to claude-sonnet-4

Reason:
  Patch touches shell sandbox and command approval policy.

Data to send:
  Diff, relevant files, test output, no gitignored files

Approve remote call?
[y] yes  [n] no  [l] local only
```

## Diff UX

Diff view should support:

- side-by-side or unified diff
- per-file approval
- apply all / reject all
- edit patch manually
- rollback applied patch
- show tests associated with patch

## Context browser

Context browser should show:

```text
Current context pack
  Repo card
  File summaries
  Symbol snippets
  Raw file ranges
  Recent tool results
  Relevant memories
```

Users should be able to remove context items before a remote call.

## Design principle

The TUI should make autonomy feel safe by showing what the agent is doing and why.
