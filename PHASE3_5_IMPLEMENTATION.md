# Phase 3.5 Implementation Summary: Knowledge Tier

## Overview

**Phase 3.5** implements the three-layer knowledge retrieval system for the swarm architecture:

- **Layer A (Deterministic)**: `ctx_fetch`, `ctx_list` - exact retrieval, ~1ms
- **Layer B (Search)**: `ctx_search` - BM25 full-text search, ~5-20ms  
- **Layer C (LLM-mediated)**: `query_knowledge` - Knowledge Agent with citations, ~500ms-2s

## Files Created

### Core Knowledge Package (`internal/knowledge/`)

| File | Purpose | Lines |
|------|---------|-------|
| `answer.go` | KnowledgeAnswer types, Confidence levels, EnforcementError | 186 |
| `scope.go` | Scope vocabulary (backend/frontend/docs/tests/all/auto), auto-detection | 57 |
| `cache.go` | Hybrid L1/L2 cache with search signature tracking | 261 |
| `persistent_cache.go` | SQLite-backed L2 persistent cache | 196 |
| `agent.go` | KnowledgeAgent with citation enforcement & auto-fix | 291 |
| `tier.go` | Three-layer retrieval interface, BM25 tuning | 118 |
| `tools.go` | ctx_fetch, ctx_list, ctx_search, query_knowledge tools | 320 |
| `knowledge_test.go` | Comprehensive unit tests (14 test functions) | 503 |

### Configuration

| File | Purpose |
|------|---------|
| `~/.config/marshal/roles/knowledge.yaml` | Knowledge agent manifest with schema enforcement |

### Protocol Extensions (`pkg/protocol/context.go`)

- Added `Scope` field to `EntryMetadata`
- Added `Scope` field to `SearchQuery`

### Runtime Extension (`internal/agent/runtime.go`)

- Added `GetAgent()` method to access underlying agent

## Key Features

### 1. Three-Layer Retrieval

```go
// Layer A: Deterministic
tier.Fetch(ctx, ref)           // Exact by ContextRef
tier.List(ctx, query)          // By tags/kinds/paths

// Layer B: Search  
tier.Search(ctx, query, mode, scope)  // BM25 with exact/fuzzy modes

// Layer C: LLM-mediated
knowledgeAgent.Run(ctx, question, scope)  // Natural language with citations
```

### 2. Citation Enforcement

KnowledgeAnswer schema enforces:
- **Non-empty citations** (min: 1, configurable)
- **Valid citations** (exist in context store)
- **Hybrid confidence scoring**:
  - 0 citations → `unknown` (forced)
  - 1 citation → `low` (forced)
  - 2+ citations → respect LLM (capped at `medium` if poor search)
  - 3+ citations + excellent search → allow `high`

### 3. Query Cache (Hybrid L1/L2)

```go
cache := knowledge.NewQueryCache(
    1000,                                    // L1: in-memory LRU size
    "~/.config/marshal/knowledge_cache.db",  // L2: persistent SQLite
)
```

**Cache key components:**
- Question hash (normalized)
- Cited entries hash (only cited, not all inspected)
- Search signature (top 10 results at query time)

**Invalidation triggers:**
- Cited entry superseded
- Search signature changed >30%

### 4. Auto-Fix by Re-Searching

When citations are invalid:
1. Re-search for the question
2. Provide new citations in feedback
3. Retry with updated context
4. Replace invalid citations if needed

### 5. Scope-Aware Queries

```go
// Auto-detect from question
scope := knowledge.AutoDetect("How do we handle auth?")  // → ScopeBackend

// Explicit scope
answer, err := ka.Run(ctx, "question", "frontend")
```

**Scope metadata added to context store:**
- Entries tagged with scope (backend, frontend, docs, tests)
- Search filters by scope
- Scope vocabularies map to context store tags

### 6. BM25 Tuning for Code

```go
kt := knowledge.NewKnowledgeTier(store)
// k1=1.2 (lower than default 1.5) - more term frequency sensitivity
// b=0.5 (lower than default 0.75) - less length normalization
```

## Test Coverage

```
ok  	github.com/alecpullen/marshal/internal/knowledge	0.342s

Test Coverage (14 tests):
✓ TestConfidenceValidation
✓ TestKnowledgeAnswerValidation  
✓ TestCacheKey
✓ TestSearchSignatureChange
✓ TestScopeAutoDetection
✓ TestScopeToTags
✓ TestPersistentCache
✓ TestFormatResults
✓ TestFormatResultsEmpty
✓ TestHashRefs
✓ TestNormalizeQuestion
✓ TestKnowledgeAnswerCitations
✓ TestCacheStats
✓ TestClearOld
```

## Integration Points

### Codegen Agent Calling Knowledge

```go
// Direct call (fast)
result, err := queryKnowledgeTool.Invoke(ctx, args)

// Retry with knowledge sub-agent (isolated)
subAgent, err := codegenAgent.SpawnSubAgent("knowledge", question)
```

### Tools Available to All Agents

```yaml
tools:
  - ctx_fetch      # Layer A: exact retrieval
  - ctx_list       # Layer A: structural listing
  - ctx_search     # Layer B: BM25 search
  - query_knowledge # Layer C: natural language (special enforcement)
```

### Context Store Schema Extension

```go
// New fields added
type EntryMetadata struct {
    // ... existing fields ...
    Scope string `json:"scope,omitempty"`  // backend|frontend|docs|tests
}

type SearchQuery struct {
    // ... existing fields ...
    Scope string `json:"scope,omitempty"`
}
```

## Configuration

```toml
[knowledge]
enabled = true
cache_l1_size = 1000
cache_l2_path = "~/.config/marshal/knowledge_cache.db"
cache_max_age = "7d"
bm25_k1 = 1.2
bm25_b = 0.5

[knowledge.enforcement]
require_citations = true
min_citations = 1
max_auto_fix_retries = 2
```

## Usage Example

```bash
# Query knowledge through CLI
$ marshal knowledge query "what's the error handling convention?" --scope=backend

[knowledge] (haiku-4-5) ✓ done (0.8s, $0.0002)

Answer:
  Errors are wrapped with pkg/errors.Wrap() and include a request-scoped
  correlation ID. HTTP handlers use echo.NewHTTPError with structured
  error codes from internal/errors/codes.go.

Confidence: high
Citations:
  - docs/error-handling.md@sha256:c8d3e1a...
  - internal/errors/codes.go@sha256:7f2b4c9...
```

## Success Criteria ✅

- ✅ Three-layer retrieval (ctx_fetch, ctx_list, ctx_search, query_knowledge)
- ✅ Citation enforcement with non-empty requirement
- ✅ Hybrid confidence scoring (algorithmic + LLM)
- ✅ Cache with L1 (memory) + L2 (SQLite) + search signature tracking
- ✅ Cache invalidation on cited entry changes + search signature changes (>30%)
- ✅ Auto-fix by re-searching when citations invalid
- ✅ Scope metadata in context store + scope-limited queries
- ✅ BM25 tuned for code/documentation (k1=1.2, b=0.5)
- ✅ Both short refs + full refs in output
- ✅ All 14 tests passing

## Next Steps (Phase 3.75)

1. Symbol index with tree-sitter integration
2. Deterministic KB tools (kb_symbol_lookup, kb_file_symbols, etc.)
3. File watcher for index maintenance
4. Pre-computed structured understanding
