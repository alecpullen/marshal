# 10. MVP Implementation Checklist

## Milestone A: Project skeleton

- [ ] Create Go module
- [ ] Add CLI entrypoint at `cmd/marshal/main.go`
- [ ] Add config loader
- [ ] Add logging
- [ ] Add basic app state
- [ ] Add graceful shutdown handling

## Milestone B: TUI shell

- [ ] Add Bubble Tea app skeleton
- [ ] Add chat input
- [ ] Add streaming output area
- [ ] Add status bar
- [ ] Add command palette placeholder
- [ ] Add tool log panel placeholder
- [ ] Add diff panel placeholder

## Milestone C: Provider abstraction

- [ ] Define `Provider` interface
- [ ] Define `ChatRequest`, `ChatMessage`, `ChatEvent`
- [ ] Implement generic OpenAI-compatible provider
- [ ] Add streaming response support
- [ ] Add provider config
- [ ] Test with Ollama
- [ ] Test with LM Studio
- [ ] Add provider error display in TUI

## Milestone D: Tool registry

- [ ] Define `Tool` type
- [ ] Define `ToolHandler`
- [ ] Define risk levels
- [ ] Add registry lookup
- [ ] Add schema validation placeholder
- [ ] Add tool call audit event

## Milestone E: Basic native tools

- [ ] `file.read`
- [ ] `repo.search`
- [ ] `git.status`
- [ ] `git.diff`
- [ ] `shell.run`
- [ ] `test.run`

## Milestone F: Approval system

- [ ] Command risk classifier
- [ ] Approval prompt in TUI
- [ ] Deny/edit/approve actions
- [ ] Per-session allow rules
- [ ] Config allow/confirm/deny rules
- [ ] Tool call logging

## Milestone G: Patch workflow

- [ ] Parse model patch proposal
- [ ] Validate patch applies cleanly
- [ ] Show unified diff
- [ ] Approve/reject patch
- [ ] Apply patch
- [ ] Show git diff after patch
- [ ] Rollback option

## Milestone H: Agent loop

- [ ] Task object
- [ ] Basic task classification
- [ ] Planning prompt
- [ ] Tool-use prompt
- [ ] Tool result summarisation
- [ ] Retry/error handling
- [ ] Final response summary

## Milestone I: SQLite persistence

- [ ] Add SQLite connection
- [ ] Add migrations
- [ ] Store sessions
- [ ] Store messages
- [ ] Store tool calls
- [ ] Store file index metadata
- [ ] Store basic project config state

## Milestone J: Repo indexing v1

- [ ] Scan files
- [ ] Respect `.gitignore`
- [ ] Hash files
- [ ] Detect language by extension
- [ ] Store file records
- [ ] Generate simple directory map
- [ ] Generate simple repo card

## Milestone K: Context pack v1

- [ ] Build context pack from repo card
- [ ] Include selected file snippets
- [ ] Include recent tool output
- [ ] Include current plan
- [ ] Track approximate token usage
- [ ] Add context browser in TUI

## Milestone L: Role-based model routing v1

- [ ] Define `AgentRole`
- [ ] Define `ModelPreset`
- [ ] Define `AgentProfile`
- [ ] Implement static router
- [ ] Show active model in TUI
- [ ] Add local-only flag
- [ ] Add role-specific context budget

## Milestone M: Tree-sitter indexing v1

- [ ] Add Tree-sitter dependency
- [ ] Parse Go files first
- [ ] Extract functions/types/imports
- [ ] Store symbols
- [ ] Add `symbols.find` tool
- [ ] Add symbol summaries to repo map

## Milestone N: Knowledge agent v1

- [ ] Summarise session at end
- [ ] Summarise changed files
- [ ] Store durable project memories
- [ ] Add confidence state
- [ ] Add memory browser in TUI
- [ ] Mark stale memories manually

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
