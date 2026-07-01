# 01. Product Vision

## Product summary

Marshal is a local-first TUI coding agent for developers who want control over inference, context, tooling, and repository knowledge.

It should feel like a serious coding partner inside the terminal, not a chatbot bolted onto a shell.

## Core user problem

Developers increasingly want coding agents that can:

- understand a repository
- inspect and edit files
- run tests and shell commands
- explain architecture
- maintain context across sessions
- use local or self-hosted models
- avoid leaking private source code to remote APIs

Most existing coding agents are cloud-first, model-provider-specific, or weak at persistent local repository knowledge.

## Target users

Primary users:

- developers running local models through Ollama, LM Studio, llama.cpp, vLLM, or similar
- privacy-conscious developers
- open-source maintainers
- students and researchers
- homelab/GPU users
- developers who prefer terminal workflows

Secondary users:

- teams wanting controlled remote model escalation
- security researchers needing auditable shell execution
- developers working with large repos where context management matters

## Product principles

### 1. Local-first

The tool should work well with local inference and should not assume a hosted frontier model.

### 2. Provider-flexible

Users should be able to swap providers without rewriting workflows.

### 3. Repo-aware

The product should build and maintain a project knowledge layer instead of repeatedly guessing from raw file snippets.

### 4. Tool-safe

Shell access is powerful and risky. Commands should be inspected, permissioned, logged, and reversible where possible.

### 5. Transparent

Users should know which model is being used, what context is being sent, what tools are being called, and why.

### 6. Incremental autonomy

The app should support several modes, from read-only Q&A to patch proposal to approved execution to later swarm workflows.

## Key differentiators

- Local inference is first-class, not a secondary feature.
- Different agent roles can use different model presets.
- Repo context is built from summaries, symbols, dependency structure, and project memory.
- Bash/tool usage is treated as a controlled subsystem.
- Swarm capabilities are designed around role specialization and shared state, not simple parallel prompting.

## Product tagline options

- Local-first coding agents for your terminal.
- Bring your own model. Keep your repo knowledge local.
- A terminal-native coding agent with safe tools and persistent project memory.
- The coding agent for developers who want control.

## Non-goals for the initial MVP

The first MVP should not attempt to solve everything.

Avoid starting with:

- fully autonomous background development
- complex swarm orchestration
- plugin marketplace
- web UI
- enterprise policy management
- every language parser
- perfect embeddings
- complex distributed execution

The MVP should prove that a single local-first agent can inspect a repo, use tools safely, make patches, run tests, and preserve useful project context.
