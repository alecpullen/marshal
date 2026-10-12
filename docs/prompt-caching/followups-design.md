# Prompt caching follow-ups — design

**Status:** approved for planning, 2026-10-12.
**Plan:** [`plans/2026-10-12-prompt-caching-followups-plan.md`](plans/2026-10-12-prompt-caching-followups-plan.md)
**Base:** `main` at `c2f10349` (the merge of the first prompt-caching branch). Anchors below are as on that commit.
**Parent design:** [`design.md`](design.md). Its §4 non-goals listed these items; this document takes up four of them.

## 1. Scope and what research changed

The four candidate follow-ups were:

1. cross-turn history replay "not byte-identical", so a turn's first request
   re-pays the history;
2. finalize and handoff calls sent without tools, so they always miss;
3. an unused fourth Anthropic breakpoint;
4. `tools.select` churning the tool list and system prompt mid-turn.

Reading the code and measuring changed items 1 and 4.

**Item 1 is already mostly true.** A three-turn probe through the real runner
compared each turn's first request with the next turn's. Turn 3's first
request opened with every message of turn 2's first request, byte for byte.
`buildHistoryMessages` replays a summary (user message, tool ledger, final
answer), not the live tool loop. The replay is append-only because
`tierSelect` fills the budget oldest-first, so older exchanges keep their
tier as new ones arrive. The cross-turn stable region therefore already
extends through the replayed history. What remains:

- **The region has no marker.** Automatic prefix caches (OpenAI, DeepSeek,
  Kimi, Ollama Cloud, llama.cpp) find it unaided. Anthropic only reads
  entries written at explicit breakpoints, and the rolling breakpoint sits
  on the turn's newest block. The live tool loop of turn N is replayed as a
  summary in turn N+1, so the entries turn N wrote are never prefixes of
  turn N+1. **On Anthropic, cross-turn reuse today is only the tools and
  system prompt.** That is item 3, and it is where item 1's value lands.
- **Over budget, the newest exchange is forced full (`tierSelect` rule 5).**
  The previous turn's newest exchange is therefore re-tiered on the next
  turn: one exchange changes, everything older stays. Accepted.
- **Nothing pins the property.** A future change to history or hint
  placement could silently break it. There is no regression test.

**Item 4 is mostly unavoidable, and the real churn is elsewhere.**
`Registry.List` is name-sorted, so the tool list is deterministic. Loading a
deferred tool adds a definition. Tools head the prefix for Anthropic and
OpenAI alike, so that request misses no matter where the definition goes.
The system-prompt rewrite (`LoadedNames`) lands on the same request and
costs nothing extra. Result: one miss per load, which is inherent to
deferral. Research found a different, avoidable source of system-prompt
churn: `agentRoster()` re-reads the on-disk model-discovery cache on every
build and filters it by a 24-hour TTL against the wall clock. A background
refresh or a TTL expiry between turns changes the roster text, and with it
`messages[0]`, with no user action.

## 2. Decisions

**F1 — `schema.ChatMessage.CacheAnchor`.** A bool marking the last message of
the cross-turn stable region: system prompt, stable context pack, skill
bodies and replayed history. The runner sets exactly one anchor when it
builds a wire from scratch, directly before the skill hint and the goal. The
four build sites are turn start, the plan-first rebuild, the handoff and the
rollover. Turn N's region, up to and including its anchor, opens turn N+1's
first request unchanged. `setContextPackMessage` carries the flag over when
it replaces the pack message, and moves it to the previous message when it
removes the pack.

**F2 — Anthropic's fourth breakpoint goes on the anchor.** Its last block gets
`cache_control` unless it is a leading system message (the system block
already has one) or the same block as the rolling breakpoint. Turn N+1's
anchor sits a few blocks after turn N's (goal, ledger, answer), within the
API's ~20-block lookback, so each turn's first request reads the previous
turn's region instead of rewriting it. That makes four breakpoints at most.

**F3 — Tools-disabled calls keep the tool definitions.** In native-tools mode,
the finalize, handoff-summary and plan-first calls send the same tool
definitions as the loop, with `tool_choice: "none"`, so their prefix matches
the loop's. Ollama-native has no `tool_choice` field; it receives the tools
and the existing directives ("Do NOT call tools"). Fallback, latched per
provider for the runner's lifetime: if a call that sent tools fails for
any reason other than cancellation, or the model calls a tool anyway, the
call is retried once without tools, and later tools-disabled calls to that
provider omit them. Strict servers do not always name the rejected field,
and a failed handoff summary ends the turn, so any failure counts. A wrong
latch costs only the cache benefit. JSON-envelope mode is
unchanged: it never sends tool definitions.

**F4 — Snapshot the discovered models once per runner.** `agentRoster()` reads
the discovery cache the first time and reuses that map for the runner's
lifetime, which is one session (`app.go:897`). The config-derived parts are
still rendered on every build, so a preset added mid-session still appears.
Models discovered mid-session appear next session.

**F5 — `tools.select` stays as it is.** One miss per load is the price of
deferral. Document it; do not restructure.

## 3. Non-goals

- Replaying the live tool loop in history. Byte-identity with the previous
  turn's *last* request would mean replaying tool calls and results, which
  is exactly what the history budget exists to avoid.
- Stub hysteresis in `tierSelect`. Only the newest exchange re-tiers, and
  the cost is one exchange.
- Memory churn. `MergeMemories` at turn start rewrites the context pack when
  the knowledge agent has written a memory, which costs one cross-turn miss
  per memory update. Infrequent; not measured.
- Anthropic's `defer_loading` tool-search beta. Provider-specific, and it
  would not help other backends.
- The 1-hour TTL, the second rolling breakpoint, and the web UI cache
  display (parent design §4).

## 4. Verification

- **Unit:** across three turns, each turn's anchored region is a prefix of
  the next turn's first request; there is exactly one anchor, and it comes
  before the hint and the goal; the tool definitions are identical across
  turns.
- **Unit:** Anthropic bodies carry at most four breakpoints, never one on a
  volatile block or a leading system message, and one on the anchor's last
  block.
- **Unit:** tools-disabled calls carry the loop's tool definitions and
  `tool_choice: "none"`. They fall back, latched, on a `tool_choice` error or
  a returned tool call.
- **Live (with the parent design's pending checks):** on Anthropic, a second
  turn's first request reports `cache_read_tokens` covering the previous
  turn's history. On an OpenAI-compatible provider, a finalize call after a
  tool loop reports cache reads.

**Status (unit):** verified on the `prompt-caching-followups` branch. Live checks pending alongside the parent design's.
