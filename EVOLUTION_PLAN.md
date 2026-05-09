# Evolution Plan: From Marshal to Swarm

## Executive Summary

**Marshal already implements ~50% of the swarm plan.** The goal is to evolve Marshal incrementally, reusing its solid foundation while adding the new capabilities from the swarm spec.

**Reuse Strategy:**
- Keep `internal/backend/` → evolves into `internal/gateway/`
- Keep `internal/git/` → core workflow unchanged
- Keep `internal/repomap/` → becomes KB foundation
- Keep `internal/session/` → schema extended for context store
- Keep `internal/tools/` → expanded with new edit tools
- Keep `internal/ui/tui/` → evolves into multi-mode TUI
- Keep `internal/pipeline/` → evolves into dynamic task graph

---

## Milestone Mapping: Marshal → Swarm

### Phase 0: Foundation Alignment (Week 1) — *M0 Equivalent*

**Goal:** Establish protocol types and event system alongside existing Marshal types.

| Swarm Component | Marshal Status | Action |
|----------------|---------------|--------|
| `pkg/protocol/events.go` | ❌ New | Create new |
| `pkg/protocol/context.go` | ❌ New | Create new |
| `internal/graph/task.go` | ⚠️ Partial | Extend existing `internal/pipeline/` types |

**Work Items:**
1. **Create `pkg/protocol/`** with `SwarmEvent` types alongside Marshal's existing types
2. **Extend `internal/pipeline/`** tasks with `TaskSpec` fields (output schema, context policy)
3. **Bridge layer**: Map Marshal's `Session`/`Task`/`Round` to swarm `SwarmEvent` stream
4. **Backward compatibility**: Marshal's CLI continues working during transition

**Files to Create:**
```
pkg/
├── protocol/
│   ├── events.go        # SwarmEvent, EventKind
│   └── context.go       # ContextRef, ContextEntry
```

**Files to Extend:**
```
internal/pipeline/
├── task.go              # Add TaskSpec fields
└── scheduler.go         # Emit SwarmEvents
```

---

### Phase 1: Gateway Refactor (Weeks 2–3) — *M1 Equivalent*

**Goal:** Marshal's backend becomes swarm's gateway with adapter normalization.

**Current State:**
- `internal/backend/backend.go` has clean `Backend` interface
- `internal/backend/openai_compat.go` handles multiple providers
- Missing: Anthropic native adapter, unified `Binding` type

**Evolution Steps:**

1. **Rename/Refactor `internal/backend/` → `internal/gateway/`**
   - Keep `Backend` interface as foundation
   - Add `Binding` struct with provider/model/endpoint/auth/loras fields
   
2. **Add Anthropic Adapter**
   - Create `internal/gateway/anthropic/adapter.go`
   - Handles content block arrays, tool_use normalization
   
3. **Extend OpenAI-Compatible Adapter**
   - Add provider auto-detection (already partially there)
   - Normalize tool call streaming differences
   
4. **Add Router**
   - `internal/gateway/router.go` with role→binding resolution
   - Budget tracking integration

**Files to Create:**
```
internal/gateway/
├── gateway.go           # Gateway interface (extends Backend)
├── router.go            # Role binding resolution
├── binding.go           # Binding struct
├── anthropic/
│   └── adapter.go       # Native Anthropic adapter
└── detect/
    └── detector.go      # Provider auto-detection
```

**Files to Modify:**
```
internal/backend/        # Gradual migration to gateway/
```

**Key Design Decision:**
Keep both `Backend` (low-level) and `Gateway` (high-level) interfaces. `Gateway.Complete()` wraps `Backend.Stream()` with normalization.

---

### Phase 1.5: Provider Detection & Profiles (Weeks 3.5–4) — *M1.5 Equivalent*

**Goal:** Marshal already has profiles; extend with swarm's auto-detection and role hints.

**Current State:**
- `internal/config/` has TOML config, profile support, env expansion
- Missing: Auto-detection at startup, role hints, local catalog

**Evolution Steps:**

1. **Add `internal/gateway/detect/detector.go`**
   - Parallel probing of Ollama (:11434), LM Studio (:1234), vLLM (:8000)
   - Env var detection for API keys
   
2. **Extend `internal/config/` with role hints**
   - Add `role_hint` field to profile bindings
   - `role_hint: small|code|large` vocabulary
   
3. **Create `configs/local-catalog.yaml`**
   - Curated model recommendations
   
4. **Enhance First-Run Experience**
   - `cmd/marshal/` first-run wizard with detection results
   - Auto-profile selection logic

**Files to Create:**
```
internal/gateway/detect/
├── detector.go          # Provider detection
└── probes.go            # HTTP probes for local servers

configs/
├── profiles/
│   ├── local-only.yaml  # Swarm profile definitions
│   ├── balanced.yaml
│   ├── quality.yaml
│   └── budget.yaml
└── local-catalog.yaml   # Model recommendations
```

**Files to Extend:**
```
internal/config/
├── config.go            # Add role_hint, binding extensions
└── profile.go           # Auto-selection logic
```

---

### Phase 2: Context Store Layer (Week 5) — *M2 Equivalent*

**Goal:** Add searchable content store on top of Marshal's existing SQLite.

**Current State:**
- `internal/session/store.go` has SQLite with WAL mode
- Schema: sessions, tasks, rounds, plans, goals
- Missing: Content-addressed blobs, FTS5 search

**Evolution Steps:**

1. **Extend Schema in `internal/session/store.go`**
   ```sql
   -- Add to existing schema
   CREATE TABLE ctx_entries (...);
   CREATE VIRTUAL TABLE entries_fts USING fts5(...);
   ```

2. **Create `internal/context/store.go`**
   - Implements `Store` interface (Put/Get/List/Search/Supersede)
   - File-backed blobs: `.swarm/sessions/<id>/ctx/<sha>.blob`
   - Metadata in SQLite with FTS5 index

3. **Content Hash Integration**
   - `read_file` tool returns `ContextRef` with SHA256
   - Foundation for staleness protection (Phase 6)

**Files to Create:**
```
internal/context/
├── store.go             # Context store interface
├── entry.go             # Entry, ContextRef types
└── search.go            # SearchQuery, SearchResult
```

**Files to Extend:**
```
internal/session/
├── store.go             # Add context tables to schema
└── schema.sql           # Extract schema for clarity
```

---

### Phase 3: Agent Runtime Refactor (Weeks 6–7) — *M3 Equivalent*

**Goal:** Marshal's loop becomes swarm's agent runtime with read-set tracking.

**Current State:**
- `internal/loop/engine.go` has executor-critic round loop
- Supports tool-use and edit formats
- Missing: Read-set tracking, output schema enforcement

**Evolution Steps:**

1. **Create `internal/agent/` package**
   - Extract runtime from `internal/loop/`
   - `Agent` struct with manifest-based configuration
   
2. **Add Read-Set Tracking**
   - `ReadSet` tracks files read at which content hash
   - Foundation for read-before-edit enforcement
   
3. **Add Manifest Support**
   - YAML role manifests in `configs/roles/`
   - `codegen.yaml` as first role

4. **Bridge Old and New**
   - `internal/loop/` delegates to `internal/agent/` for new tasks
   - Legacy tasks continue using old path

**Files to Create:**
```
internal/agent/
├── agent.go             # Agent runtime
├── manifest.go          # Role manifest parsing
├── readset.go           # Read-set tracking
└── runtime.go           # Run loop

configs/roles/
├── codegen.yaml         # First role manifest
├── knowledge.yaml       # Prepared for Phase 3.5
└── summariser.yaml      # Prepared for Phase 3.8
```

**Files to Extend:**
```
internal/loop/
├── engine.go            # Add delegation to agent package
```

---

### Phase 3.5: Knowledge Tier (Week 7.5) — *M3.5 Equivalent*

**Goal:** Three-layer retrieval system using existing context store.

**Current State:**
- Phase 2 added context store with FTS5
- Missing: Knowledge agent, query cache

**Evolution Steps:**

1. **Create `internal/knowledge/`**
   - Layer A: `ctx_fetch`, `ctx_list` (deterministic)
   - Layer B: `ctx_search` (BM25 via FTS5)
   - Layer C: Knowledge agent with tool access to A+B

2. **Add Knowledge Agent**
   - Implements `query_knowledge(question)` tool
   - Returns `KnowledgeAnswer` with required citations

3. **Add Query Cache**
   - LRU keyed on `(normalized_question, hash_of_referenced)`

**Files to Create:**
```
internal/knowledge/
├── knowledge.go         # Three-layer interface
├── agent.go             # Knowledge agent implementation
├── cache.go             # Query cache
└── answer.go            # KnowledgeAnswer types

internal/tools/
├── ctx_fetch.go         # Tool: exact context retrieval
├── ctx_list.go          # Tool: structural listing
└── ctx_search.go        # Tool: FTS5 search
```

---

### Phase 3.75: Knowledge Base Foundation (Week 8) — *M3.75 Equivalent*

**Goal:** Tree-sitter symbol index building on Marshal's repomap.

**Current State:**
- `internal/repomap/` has PageRank-ranked symbol extraction
- Uses `smacker/go-tree-sitter`
- Missing: Structured symbol index, deterministic tools

**Evolution Steps:**

1. **Extend `internal/repomap/` → `internal/kb/`**
   - Add `SymbolIndex` with content hashing
   - Cross-file reference resolution
   
2. **Add Deterministic Tools**
   - `kb_symbol_lookup(name)`
   - `kb_symbol_references(name)`
   - `kb_file_symbols(path)`
   - `kb_project_map()`

3. **Add Maintainer**
   - `fsnotify` watcher for changed files
   - Reindex on hash mismatch

**Files to Create:**
```
internal/kb/
├── index.go             # SymbolIndex types
├── tools.go             # kb_* tool implementations
├── maintainer.go        # File watcher + reindexer
└── parser.go            # Tree-sitter integration

cmd/kb_commands.go       # CLI: swarm kb status/rebuild/verify
```

**Files to Extend:**
```
internal/repomap/
├── map.go               # Export symbol data for KB
```

---

### Phase 3.8: KB Derived Summaries (Weeks 8.5–9) — *M3.8 Equivalent*

**Goal:** LLM-generated summaries building on symbol index.

**Current State:**
- Phase 3.75 adds symbol index
- Missing: File summaries, package summaries, convention extraction

**Evolution Steps:**

1. **Create `internal/kb/summary.go`**
   - `FileSummary`, `PackageSummary`, `ProjectMap` types
   - Content-hash + symbol-hash for invalidation

2. **Add Summariser Role**
   - `configs/roles/summariser.yaml`
   - Generates structured summaries verifiable against symbols

3. **Add Convention Extraction**
   - `convention_extractor` role samples call sites
   - User approval required before `ApprovedByUser=true`

4. **Budget & Backpressure**
   - Daily summarisation cap (default $0.50)
   - Priority queue in maintainer

**Files to Create:**
```
internal/kb/
├── summary.go           # Summary types
├── convention.go        # ExtractedConvention
├── summariser.go        # Summariser agent integration
└── budget.go            # Cost tracking

cmd/kb_summary.go        # kb file-summary, kb conventions
```

---

### Phase 4: Task Graph & Orchestrator (Weeks 9.5–11.5) — *M4 Equivalent*

**Goal:** Marshal's static pipeline → dynamic task graph with replanning.

**Current State:**
- `internal/pipeline/scheduler.go` has tier-based DAG execution
- Missing: Dynamic expansion, replanning, orchestrator LLM

**Evolution Steps:**

1. **Create `internal/graph/`**
   - `Graph` with mutable task map and edges
   - `ApplyMutation()` for runtime changes
   - Version tracking

2. **Extend `internal/pipeline/` → `internal/orchestrator/`**
   - New `Orchestrator` struct with LLM-driven planning
   - `Plan()` creates initial graph from goal
   - `Replan()` handles dynamic changes

3. **Integrate with Phase 3 Knowledge**
   - Orchestrator prompt includes KB project map
   - Uses `kb_*`, `query_knowledge`, `ctx_search` tools

4. **Critic Loop as Composite**
   - `CompositeCriticLoop` treated as single graph node
   - Spawns executor/critic pairs internally

**Files to Create:**
```
internal/graph/
├── graph.go             # Mutable task graph
├── task.go              # Task, TaskStatus, TaskSpec
├── mutation.go          # GraphMutation types
└── topo.go              # Topological operations

internal/orchestrator/
├── orchestrator.go      # LLM-driven planner
├── replanner.go         # Replanning logic
└── scheduler.go         # Dynamic execution (extends pipeline/)
```

**Files to Extend/Migrate:**
```
internal/pipeline/
├── scheduler.go         # Gradual migration to orchestrator/
```

**Key Challenge:**
This is the biggest architectural shift. Marshal's pipeline is static (all tasks known upfront). Swarm's orchestrator is dynamic (tasks added during execution). 

**Migration Strategy:**
- Keep `pipeline/` working for existing code
- `orchestrator/` uses `graph/` for new swarm mode
- Add `--swarm` flag to enable dynamic mode
- Eventually `pipeline/` becomes legacy wrapper

---

### Phase 5: Session Manager & Enhanced TUI (Week 12) — *M5 Equivalent*

**Goal:** Marshal's TUI gets swarm's session management and slash commands.

**Current State:**
- `internal/ui/tui/` has Bubbletea chat interface
- Missing: Session resume, swarm panel, slash command expansion

**Evolution Steps:**

1. **Extend `internal/session/`**
   - Add `Session` struct with `Thread []Turn`
   - `Resume()` restores context store + graph state

2. **Enhance `internal/ui/tui/`**
   - Two-pane layout: chat + collapsible swarm panel
   - Live task tree visualization (preparation for Phase 12)

3. **Expand Slash Commands**
   - `/cancel`, `/approve`, `/cost`
   - `/profile use`, `/profile show`
   - `/model <role> <binding>`
   - `/knowledge <question>`
   - `/kb status`

4. **Live Cost Rendering**
   - Status bar: session cost, daily budget, KB cost
   - Soft warnings at 50%/80%, pause at 100%

**Files to Extend:**
```
internal/session/
├── session.go           # Add Thread, Resume

internal/ui/tui/
├── model.go             # Two-pane layout
├── swarm_panel.go       # Task tree visualization
└── slash_commands.go    # Command handlers

internal/commands/
├── dispatcher.go        # Extend existing slash commands
```

---

### Phase 6: Edit Robustness & Proposed-Edits (Weeks 13–14) — *M6 Equivalent*

**Goal:** Major upgrade to edit tools with staleness protection.

**Current State:**
- `internal/tools/` has `read_file`, `write_file`, `run_command`
- `internal/edit/` has search/replace, udiff, wholefile formats
- Missing: Symbol-based editing, proposed-edits model, staleness checks

**Evolution Steps:**

1. **Extend `internal/tools/`**
   - `edit_symbol` (structural editing via KB)
   - `apply_patch` (unified diff with 3-way merge)
   - Enhanced `edit_file` with fuzzy matching

2. **Add Staleness Protection**
   - All mutating tools take `expected_hash`
   - Check against read-set (Phase 3)

3. **Create `internal/edit/proposals/`**
   - `EditProposal` with dependency tracking
   - `Applier` commits in dependency order with retry

4. **Rich Error Responses**
   - Structured `ToolError` with `hint` field
   - Recovery instructions for agents

**Files to Create:**
```
internal/tools/
├── edit_symbol.go       # Symbol-based editing
├── apply_patch.go       # Patch application
└── edit_proposal.go     # Proposal capture

internal/edit/proposals/
├── proposal.go          # EditProposal types
├── applier.go           # Dependency-ordered application
└── rebase.go            # Rebase on stale-hash

internal/sandbox/
├── enhance.go           # bwrap/sandbox-exec integration
```

**Files to Extend:**
```
internal/tools/
├── write_file.go        # Add expected_hash parameter
├── read_file.go         # Return ContextRef with hash
```

---

### Phase 7: Telemetry & Observability (Week 15) — *M7 Equivalent*

**Goal:** OpenTelemetry integration and structured logging.

**Current State:**
- Uses `go.uber.org/zap` for logging
- Missing: OTel spans, context assembly logs, edit traces

**Evolution Steps:**

1. **Add `internal/telemetry/`**
   - OpenTelemetry spans: session → orchestrator → agent → tool → model
   - Context assembly logging
   - Knowledge query traces

2. **Extend Event System**
   - All phases emit `SwarmEvent` to event bus
   - `internal/events/` for pub/sub

3. **Trace Viewer**
   - `swarm trace <session-id>` opens local viewer
   - Web UI or TUI-based

**Files to Create:**
```
internal/telemetry/
├── tracer.go            # OTel integration
├── spans.go             # Span definitions
├── viewer.go            # Local trace viewer

internal/events/
├── bus.go               # Event bus
└── subscriber.go        # Event consumers
```

---

### Phase 8: Project Memory & Config (Week 16) — *M8 Equivalent*

**Goal:** Cross-session learning and project-level configuration.

**Current State:**
- Config is user-level (`~/.config/marshal/`)
- Missing: Project-level `.swarm/`, conventions, learnings

**Evolution Steps:**

1. **Add `.swarm/` Directory Support**
   - `.swarm/config.yaml`: profile, bindings, budget
   - `.swarm/conventions.md`: ingested into KB
   - `.swarm/learnings.md`: user-approved lessons

2. **Extend KB for Persistence**
   - Project KB persists across sessions
   - Convention extraction from learnings

3. **Config Hierarchy**
   - CLI flags > env vars > `./.swarm/config.yaml` > `~/.config/marshal/`

**Files to Create:**
```
internal/project/
├── config.go            # Project-level config
├── conventions.go       # Convention ingestion
└── learnings.go         # Learning management
```

---

### Phase 8.5: LoRA Adapters (Week 17) — *M8.5 Equivalent*

**Goal:** Optional LoRA support for power users.

**Current State:**
- `internal/backend/registry.go` has 4-role binding
- Missing: LoRA field, adapter registry

**Evolution Steps:**

1. **Extend `internal/gateway/binding.go`**
   - Add `LoRAs []string` field (v1: enforce len ≤ 1)

2. **Create `internal/lora/`**
   - Adapter registry (local:/hf:/fireworks: URIs)
   - Resolution logic

3. **Backend Support**
   - Fireworks: API-level adapter parameter
   - vLLM/SGLang: Dynamic LoRA loading

**Files to Create:**
```
internal/lora/
├── registry.go          # Adapter registry
├── resolve.go           # URI resolution
└── cli.go               # swarm lora attach/detach commands
```

---

### Phase 12: Hybrid TUI (Weeks 18–19) — *M12 Equivalent*

**Goal:** Full multi-mode terminal UI.

**Current State:**
- Phase 5 has basic chat + swarm panel
- Missing: Graph mode, focus mode, flow mode

**Evolution Steps:**

1. **Create `internal/ui/graph/`**
   - Sugiyama-style DAG layout
   - Unicode box-drawing rendering
   - Incremental layout (stable on add)

2. **Create `internal/ui/focus/`**
   - Single task expanded view
   - Context, logs, output, cost details

3. **Create `internal/ui/flow/`**
   - Streaming structured event view
   - Semantic coloring and folding

4. **Mode Switching**
   - `/graph`, `/focus <task>`, `/flow`, `/chat`
   - Hotkeys: `Ctrl+G`, `Ctrl+F`, `Ctrl+L`

**Files to Create:**
```
internal/ui/
├── graph/
│   ├── layout.go        # Sugiyama algorithm
│   ├── render.go        # Unicode rendering
│   └── model.go         # Bubbletea model
├── focus/
│   ├── model.go         # Task detail view
│   └── inspect.go       # Context/log inspection
└── flow/
    ├── model.go         # Streaming event view
    └── filter.go        # Event filtering
```

---

## Cross-Cutting Concerns

### Database Schema Evolution

**Current Schema** (`internal/session/store.go`):
```sql
sessions, tasks, rounds, plans, goals, read_only_files
```

**Additions per Phase:**
- Phase 2: `ctx_entries`, `entries_fts` (FTS5)
- Phase 3.75: `kb_symbols`, `kb_files`
- Phase 3.8: `kb_summaries`, `kb_conventions`
- Phase 4: `graph_mutations` (audit log)
- Phase 8: `project_configs`, `learnings`

### Configuration Migration

**Current:** TOML with profiles, role bindings
**Evolution:**
- Add `role_hint` to bindings (Phase 1.5)
- Add `loras` to bindings (Phase 8.5)
- Add project-level `.swarm/config.yaml` (Phase 8)

### Event System Integration

**New Package:** `internal/events/`
- All phases emit `SwarmEvent`
- TUI subscribes for live updates
- Telemetry subscribes for tracing

---

## Testing Strategy

**Leverage Marshal's Existing Tests:**
- `internal/edit/` has search/replace/udiff tests
- `internal/git/` has branch management tests
- `internal/repomap/` has tree-sitter tests

**New Test Categories:**
- **Golden fixtures** for orchestrator plans
- **Knowledge tier evals** (30-50 questions)
- **Edit robustness evals** (50+ scenarios)
- **TUI snapshot tests** with `teatest`

---

## Suggested Execution Order

| Phase | Duration | Dependencies | Risk Level |
|-------|----------|--------------|------------|
| 0 | 1 week | None | Low |
| 1 | 2 weeks | 0 | Low |
| 1.5 | 1 week | 1 | Low |
| 2 | 1 week | 0 | Medium (FTS5) |
| 3 | 2 weeks | 2 | Medium |
| 3.5 | 0.5 week | 2, 3 | Low |
| 3.75 | 1 week | 0 (repomap exists) | Low |
| 3.8 | 1.5 weeks | 3.75 | Medium |
| 4 | 3 weeks | 3, 3.5, 3.8 | **High** |
| 5 | 1 week | 4 | Medium |
| 6 | 2 weeks | 3, 2 | Medium |
| 7 | 1 week | All | Low |
| 8 | 1 week | All | Low |
| 8.5 | 1 week | 1 | Low (optional) |
| 12 | 2 weeks | 5 | **High** |

**Total: ~19 weeks** (matches swarm plan timeline)

---

## Key Architectural Decisions

1. **Two-Mode Operation:** Keep Marshal's static pipeline working while adding swarm's dynamic orchestrator. Use `--swarm` flag to enable new behavior.

2. **Incremental Database Migration:** Extend existing SQLite schema rather than new database. Use migrations for new tables.

3. **Package Structure:** Create new `internal/` packages alongside existing ones. Gradually migrate functionality rather than big-bang rewrites.

4. **Event-Driven UI:** All state changes emit events. TUI subscribes rather than polling.

5. **Tool Compatibility:** New tools (`edit_symbol`, `apply_patch`) coexist with existing (`write_file`). Agents choose appropriate tool.
