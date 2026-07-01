# 05. Context and Project Knowledge

## Goal

Marshal should maintain a persistent local understanding of a project so that each task does not start from zero.

The context system should combine:

- lexical search
- Tree-sitter symbols
- repo maps
- file summaries
- dependency/import graphs
- session history
- optional embeddings
- durable project memories

## Context layers

```text
Immediate context
  Current user request
  Current open files
  Current plan
  Recent tool results

Task context
  Files touched
  Relevant symbols
  Test failures
  Previous attempts
  Constraints

Project context
  Repo map
  File summaries
  Symbol graph
  Dependency graph
  Architecture notes
  Known conventions
  Historical sessions

Global user context
  User preferences
  Preferred commands
  Formatting style
```

## Context retrieval pipeline

```text
1. Classify task
   bugfix / feature / refactor / explain / test / security review

2. Generate retrieval plan
   likely files, symbols, commands, docs

3. Retrieve candidates
   repo search + summaries + symbol graph + optional vector search

4. Rank candidates
   recency, symbol relevance, imports, test failures, user mentions

5. Build compact context pack
   repo overview, relevant files, symbol snippets, constraints, prior attempts

6. Track usefulness
   mark which context was used, ignored, or contradicted
```

## Context levels

```text
L0: project card
L1: directory map
L2: file summaries
L3: symbol summaries
L4: code chunks
L5: raw file ranges
```

The model should usually see L0-L2 first, then request L3-L5 through tools.

## Context budget

```go
type ContextBudget struct {
    MaxTokens          int
    SystemTokens       int
    ConversationTokens int
    ToolSchemaTokens   int
    RetrievedTokens    int
    ReservedOutput     int
}
```

Role-specific budgets should be supported.

Example:

```toml
[agents.knowledge.context]
max_repo_context_tokens = 12000
include_raw_code = false
include_summaries = true
include_symbols = true

[agents.implementer.context]
max_repo_context_tokens = 48000
include_raw_code = true
include_summaries = true
include_symbols = true
```

## Project database

SQLite should be the default database.

Suggested tables:

```sql
CREATE TABLE projects (
    id INTEGER PRIMARY KEY,
    root_path TEXT UNIQUE NOT NULL,
    name TEXT,
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL,
    path TEXT NOT NULL,
    language TEXT,
    hash TEXT NOT NULL,
    size_bytes INTEGER,
    last_indexed_at TEXT,
    UNIQUE(project_id, path)
);

CREATE TABLE symbols (
    id INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    start_line INTEGER,
    end_line INTEGER,
    signature TEXT,
    doc TEXT
);

CREATE TABLE file_summaries (
    file_id INTEGER PRIMARY KEY,
    summary TEXT NOT NULL,
    model TEXT,
    updated_at TEXT
);

CREATE TABLE memories (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL,
    kind TEXT NOT NULL,
    text TEXT NOT NULL,
    source TEXT,
    confidence TEXT NOT NULL,
    created_at TEXT,
    updated_at TEXT
);

CREATE TABLE agent_sessions (
    id TEXT PRIMARY KEY,
    project_id INTEGER NOT NULL,
    title TEXT,
    started_at TEXT,
    ended_at TEXT,
    summary TEXT
);

CREATE TABLE tool_calls (
    id INTEGER PRIMARY KEY,
    session_id TEXT,
    agent_role TEXT,
    model TEXT,
    tool_name TEXT,
    args_json TEXT,
    result_summary TEXT,
    risk_level TEXT,
    approved BOOLEAN,
    created_at TEXT
);
```

## Tree-sitter indexing

Extract:

- functions
- methods
- classes/types/structs
- interfaces/traits/protocols
- imports
- exports
- comments/docstrings
- call-like expressions
- test functions
- routes/endpoints where detectable
- config files
- dependency manifests

## Repo map

Example output:

```text
Project: marshal

Languages:
  Go 83%
  Shell 5%
  Markdown 12%

Main packages:
  cmd/marshal           CLI entrypoint
  internal/tui        terminal interface
  internal/llm        provider abstraction
  internal/agent      planning and execution loop
  internal/tools      tool registry and built-in tools
  internal/repo       repository indexer
  internal/db         SQLite storage

Important symbols:
  Provider interface       internal/llm/provider.go
  ToolRegistry             internal/tools/registry.go
  AgentLoop                internal/agent/loop.go
  ProjectIndex             internal/repo/indexer.go
```

## Knowledge agent

The knowledge agent maintains project memory.

Responsibilities:

- watch changed files
- update symbol index
- summarise changed files
- detect architecture changes
- maintain repo map
- store durable decisions
- track known bugs
- link test failures to code areas
- build onboarding briefs for main agent
- detect stale memories

## Memory confidence states

```text
observed       found directly in files or tool output
inferred       likely true, model-derived
confirmed      verified by tests, user, or source files
stale          contradicted or old
deprecated     intentionally replaced
```

## Memory write policy

The knowledge agent should write memory only when one of these is true:

1. confirmed by repository content
2. confirmed by command output
3. explicitly stated by the user
4. derived from repeated successful operations

Example memory:

```json
{
  "kind": "architecture",
  "text": "The provider layer uses OpenAI-compatible chat requests as the common internal format.",
  "source": "internal/llm/provider/openai_compat.go",
  "confidence": "confirmed",
  "updated_at": "2026-07-02T10:00:00+10:00"
}
```

## Embeddings

Embeddings should be optional.

The system should still work using:

- text search
- symbols
- file summaries
- repo map
- dependency graph

Embeddings can improve semantic recall but should not be required for basic functionality.
