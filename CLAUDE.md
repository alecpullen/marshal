# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Run (currently Milestone 1 verification)
go run cmd/marshal/main.go

# Build
go build ./...

# Test
go test ./...

# Test a single package
go test ./internal/backend/...

# Format (non-negotiable — gofmt is enforced)
gofmt -w .
```

## Environment variables

Marshal reads these from the shell — they must be exported before running:

```bash
export FIREWORKS_API_KEY="..."
```

`marshal.toml` uses `${VAR}` interpolation via `os.Expand` — missing vars log a warning and substitute as empty strings, which causes `Validate()` to fail.

## Architecture

Marshal is a loop-first coding agent orchestrator. The core loop runs an **executor** model (writes code) against a **critic** model (reviews code as structured JSON), applies changes to a real git repo on an isolation branch, and merges only on a PASS verdict.

**Layer overview** (bottom to top):

| Layer | Package | Status |
|---|---|---|
| Backend interface + OpenAI-compat client | `internal/backend/` | Done (Milestone 1) |
| Config loader | `internal/config/` | Done (Milestone 1) |
| Loop engine (rounds, feedback, verdict parsing, compaction hook) | `internal/loop/` | Planned (M2) |
| Git layer (branch, diff, commit, revert) | `internal/git/` | Planned (M3) |
| Session store (SQLite) | `internal/store/` | Planned (M4) |
| Cobra CLI (`marshal run`, `marshal sessions`) | `cmd/marshal/` | Planned (M4) |
| Bubble Tea TUI | `internal/tui/` | Planned (M5-6) |
| Multi-agent pipeline (planner, parallel execution, DAG) | `internal/pipeline/` | Planned (Phase 3) |

### Backend interface

`internal/backend/backend.go` defines the core interface that all model providers implement:

```go
type Backend interface {
    Complete(ctx context.Context, model string, messages []Message) (Response, error)
    Name() string
}
```

`OpenAICompatibleBackend` implements this against any OpenAI-compatible endpoint (RunPod, Ollama, Anthropic, vLLM). Future backends add by implementing the interface.

### Configuration

`marshal.toml` is the single config file. Loaded by `config.Load(path)` which expands env vars, then validated by `cfg.Validate()`. Config struct mirrors the TOML sections: `[executor]`, `[critic]`, `[loop]`, `[session]`, `[retry]`.

### Loop lifecycle (once implemented)

1. Isolation branch created from HEAD
2. Executor generates/modifies code
3. `git diff` extracted, sent to Critic with structured JSON verdict schema
4. PASS → merge branch, commit, done. FAIL → inject feedback, retry up to `max_rounds`
5. Exhaustion → hard reset to pre-session HEAD, delete branch, exit 1

### Critic verdict schema

```json
{"verdict": "PASS"|"FAIL", "summary": "...", "issue": "...", "fix": "...", "concerns": []}
```

### Skills system (three-tier resolution)

Skills are TOML files that add to (never override) executor/critic system prompts. Resolution order:
1. `.marshal/skills/` — project-local, committed to repo
2. `~/.config/marshal/skills/` — user-global
3. Built-in skills inside the binary

Skills use `system_prompt_additions` only — any other prompt key is a validation error. Security standing instructions are always-on and cannot be overridden by skills.

## Development milestones

The project follows a strict milestone order. Do not implement a later milestone before the earlier one is verified working:

1. **M1** ✅ Backend interface + `OpenAICompatibleBackend` + config loader
2. **M2** Loop engine (`internal/loop/`) — executor, critic, JSON verdict, compaction hook, security prompts, skills
3. **M3** Git layer (`internal/git/`) — branch isolation, diff, commit, revert
4. **M4** Session store + Cobra CLI — SQLite, `marshal run`, `marshal sessions`, headless `--no-tui --json`
5. **M5-6** Bubble Tea TUI — loop view, session browser, diff viewer
6. **M7-8** Real compaction + think-block panel
7. **M9-13** Multi-agent pipeline — planner, DAG scheduler, parallel execution, integration critic
