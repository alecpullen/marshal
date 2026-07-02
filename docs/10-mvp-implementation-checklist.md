# 10. MVP Implementation Checklist

## Milestone A: Project skeleton

- [x] Create Go module
- [x] Add CLI entrypoint at `cmd/marshal/main.go`
- [x] Add config loader
- [x] Add logging
- [x] Add basic app state
- [x] Add graceful shutdown handling

## Milestone B: TUI shell

- [x] Add Bubble Tea app skeleton
- [x] Add chat input
- [x] Add streaming output area
- [x] Add status bar
- [x] Add command palette placeholder
- [x] Add tool log panel placeholder
- [x] Add diff panel placeholder

## Milestone C: Provider abstraction

- [x] Define `Provider` interface
- [x] Define `ChatRequest`, `ChatMessage`, `ChatEvent`
- [x] Implement generic OpenAI-compatible provider
- [x] Add streaming response support
- [x] Add provider config
- [x] Test with Ollama
- [x] Test with LM Studio
- [x] Add provider error display in TUI

## Milestone D: Tool registry

- [x] Define `Tool` type
- [x] Define `ToolHandler`
- [x] Define risk levels
- [x] Add registry lookup
- [x] Add schema validation placeholder
- [x] Add tool call audit event

## Milestone E: Basic native tools

- [x] `file.read`
- [x] `repo.search`
- [x] `git.status`
- [x] `git.diff`
- [x] `shell.run`
- [x] `test.run`

## Milestone F: Approval system

- [x] Command risk classifier
- [x] Approval prompt in TUI
- [x] Deny/edit/approve actions
- [x] Per-session allow rules
- [x] Config allow/confirm/deny rules
- [x] Tool call logging

## Milestone G: Patch workflow

- [x] Parse model patch proposal
- [x] Validate patch applies cleanly
- [x] Show unified diff
- [x] Approve/reject patch
- [x] Apply patch
- [x] Show git diff after patch
- [x] Rollback option

## Milestone H: Agent loop

- [x] Task object
- [x] Basic task classification
- [x] Planning prompt
- [x] Tool-use prompt
- [x] Tool result summarisation
- [x] Retry/error handling
- [x] Final response summary

## Milestone I: SQLite persistence

- [x] Add SQLite connection
- [x] Add migrations
- [x] Store sessions
- [x] Store messages
- [x] Store tool calls
- [x] Store file index metadata
- [x] Store basic project config state

## Milestone J: Repo indexing v1

- [x] Scan files
- [x] Respect `.gitignore`
- [x] Hash files
- [x] Detect language by extension
- [x] Store file records
- [x] Generate simple directory map
- [x] Generate simple repo card

## Milestone K: Context pack v1

- [x] Build context pack from repo card
- [x] Include selected file snippets
- [x] Include recent tool output
- [x] Include current plan
- [x] Track approximate token usage
- [x] Add context browser in TUI

## Milestone L: Role-based model routing v1

- [x] Define `AgentRole`
- [x] Define `ModelPreset`
- [x] Define `AgentProfile`
- [x] Implement static router
- [x] Show active model in TUI
- [x] Add local-only flag
- [x] Add role-specific context budget

## Milestone M: Tree-sitter indexing v1

- [x] Add Tree-sitter dependency
- [x] Parse Go files first
- [x] Extract functions/types/imports
- [x] Store symbols
- [x] Add `symbols.find` tool
- [x] Add symbol summaries to repo map

## Milestone N: Knowledge agent v1

- [ ] Summarise session at end
- [ ] Summarise changed files
- [ ] Store durable project memories
- [ ] Add confidence state
- [ ] Add memory browser in TUI
- [ ] Mark stale memories manually

## Milestone N.5: Settings TUI

- [x] Add `config.SaveProjectConfig`
- [x] Add settings form fields
- [x] Add settings Bubble Tea model
- [x] Wire settings overlay into main TUI
- [x] Apply saved config to agent runner
- [x] Add `Ctrl+O` shortcut

## Milestone O: First swarm prototype

- [ ] Shared task state
- [ ] Planner role
- [ ] Repo Scout role
- [ ] Implementer role
- [ ] Reviewer role
- [ ] Sequential orchestration
- [ ] Read-only parallel scout experiment
- [ ] Write lock

## First demo target

The first useful demo should support this flow:

```text
1. Start Marshal in a Go repository.
2. Ask what the project does.
3. Agent builds a basic repo map.
4. Ask it to fix or add a small test.
5. Agent searches and reads files.
6. Agent proposes a patch.
7. User approves patch.
8. Agent runs `go test ./...`.
9. Agent summarises the result.
```

## Development warning

Avoid starting with swarm, embeddings, and complex background indexing. Build the safe single-agent loop first, then improve context, then add role routing, then add knowledge, then add swarm.
