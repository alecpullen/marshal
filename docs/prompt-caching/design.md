# Prompt caching — design

**Status:** approved for planning, 2026-10-11.
**Plan:** [`plans/2026-10-11-prompt-caching-plan.md`](plans/2026-10-11-prompt-caching-plan.md)
**Base:** `main` at `cab5ff06`. Every code anchor below is as on that commit.

## 1. Problem

Marshal already has the basic machinery for prompt caching: Anthropic
`cache_control` breakpoints on the system prompt and tool list
(`internal/llm/provider/anthropic.go:281-295`); cache-read and cache-write
token parsing for OpenAI, Anthropic, DeepSeek and Responses usage shapes;
`turn_metrics.cache_read_tokens` / `cache_write_tokens`; and cost math that
bills cached tokens at the cached rate. In practice almost none of it pays
off.

### Evidence

`turn_metrics` summed over every `.marshal/marshal.db` on the maintainer's
machine (2026-10-10):

| Provider / model | Prompt tokens | Cache reads |
|---|---|---|
| ollama-cloud (deepseek, glm, kimi, minimax models) | ~4.9 B | ~0 % |
| kimi (`api.kimi.com/coding/v1`) | ~0.45 B | 0 |
| openai-codex gpt-6.1-sol | 30 M | 0 |
| openai_compatible on OpenCode Zen (deepseek-v4-*, etc.) | ~35 M | 0 |
| ollama-cloud glm-5.3, turns on 2026-10-10 only | 3.6 M | **~90 %** |

The last row shows that the parsing and storage path works, and that
providers do serve cache hits when Marshal's prefix holds still. The zeros
mix two causes: prefixes that change between requests (below), and
endpoints that never report caching. `turn_metrics` cannot tell those apart.

### Measured prefix behaviour

A throwaway test ran a 5-request native-tool turn through the real runner.
A scripted provider changed the todo list between steps the way `todo.write`
does. Each captured request was then serialised through the real
`buildChatRequestBody` and `buildAnthropicRequestBody`. From the first todo
update on, consecutive requests first differed at **message index 1**, so
the shared byte prefix shrank to the system prompt plus tool list (about
4 KB of a growing body).

## 2. Root causes

1. **Turn-volatile content at index 1.** The context pack is one user
   message right after the system prompt (`route.go:410`
   `setContextPackMessage`), rebuilt before every model call
   (`runner.go:1172-1175`). It carries the todo list (`todo.write` after
   every step), the scratchpad projection (with relative ages, "3m ago",
   from `contextpack/builder.go:429`, which change every minute) and
   semantic snippets (re-queried mid-turn). Any change there invalidates
   every automatic prefix cache (OpenAI, DeepSeek, Kimi, Ollama Cloud,
   llama.cpp/vLLM KV reuse) for the whole transcript after it.
2. **Per-turn skill hint ahead of the history.** `appendSkillHint` runs
   right after the pack (`runner.go:923`), so the replayed history sits
   behind content that changes every turn. The first request of each turn
   re-pays the full history.
3. **System prompt drift on rebuild.** `rollover.go:150` and the plan-first
   rebuild (`runner.go:986`) use `BuildSystemPromptWithAddendum`, which drops
   `SystemAccess` and `Narration`. Their system prompt differs from the one
   built at turn start, which is a cache miss and also a real prompt
   difference (the narration addendum disappears).
4. **Anthropic never caches the transcript.** The only breakpoints are on
   the system prompt and tools. Every tool-loop step re-pays the whole
   conversation at full price. The API allows four breakpoints; two are
   used.
5. **Anthropic moves mid-conversation system messages to the top.**
   `toAnthropicMessages` (`anthropic.go:165`) moves every `RoleSystem`
   message, wherever it appears, into the top-level `system` field, and the
   system breakpoint lands on the last one. Skill hints, nudges and
   finalize directives therefore rewrite the cached system prefix mid-turn
   and lose their position. The OpenAI Chat and Responses encoders already
   keep them in place.
6. **No `prompt_cache_key`.** OpenAI (and the ChatGPT Codex backend, per
   `docs/codex-spike-findings-2026-09-14.md` §3) uses `prompt_cache_key`
   to route a conversation's requests to a warm cache. Marshal never sends
   it. This is the likely, unverified, cause of Codex's 0 %.
7. **Kimi's cache field may not be parsed.** Third-party documentation
   puts Moonshot's cache hits in a flat `usage.cached_tokens`. The parser
   reads only `prompt_tokens_details.cached_tokens` and DeepSeek's
   `prompt_cache_hit_tokens`. Unverified until a live response is captured.
8. **The only "cache" figure in the UI is a different metric.** The
   Context section's "N% cache" (`sessionsheet/section_context.go:91`) is
   the share of turns with at least one *tool-result* cache hit
   (`turn_metrics.cache_hits`). Next to token stats it reads as a
   prompt-cache rate, and there is no real prompt-cache rate anywhere.

## 3. Decisions

**D1 — Split the context pack by volatility at render time.** Sections of
kind `todos`, `scratchpad`, `semantic` and `tool_output` are *volatile*;
everything else (repo card, map, memories, session summaries, plan, pinned
files) is *stable*. The pack is still built and budgeted as one unit:
stable sections have lower priority numbers (10–30, plus pinned) and are
admitted before volatile ones (35–50), so stable content never depends on
volatile sizes. Rendering splits: `RenderStable` stays at index 1 under
"Project context pack:", and `RenderVolatile` becomes the working-state
message. The pack's truncation note (`Estimated tokens: N/M`) moves to the
volatile render, because its estimate changes whenever a volatile section
does.

**D2 — Working state is a per-request tail, never stored.** The runner
appends the working-state message (`Role: user`, `Volatile: true`) inside
`chatOnceAttempt`, the single dispatch point, so it is never part of the
runner's `messages` slice. It cannot leak into compaction, pruning,
archives or history replay, and every request (main loop, finalize,
handoff summary, plan) carries the current copy. Budget checks measure the
wire including the tail. Moving the tail makes the relative scratchpad
ages harmless, so they stay: they are useful to the model, and absolute
timestamps would add nothing once the section no longer sits in the prefix.

**D3 — `schema.ChatMessage.Volatile`.** A new bool marks a message that is
regenerated for every request. Encoders send it like any other message.
Providers that place explicit breakpoints put them before it.

**D4 — One system-prompt builder.** `(*Runner).systemPromptMessage()`
replaces all five build sites, so a rebuild produces byte-identical bytes
to the turn-start prompt (unless skills, loaded tools or system access
really changed).

**D5 — Skill hint immediately before the goal.** It changes every turn, so
it goes after the replayed history, directly before the user's new
message.

**D6 — Anthropic: leading system only, plus a rolling breakpoint.** Only the
leading run of system messages becomes `system`. Later system messages stay
in place as user text blocks wrapped in `<system-reminder>…</system-reminder>`,
which Claude models are trained to read as harness guidance rather than
user speech. A third breakpoint goes on the last block of the last
non-`Volatile` message. Each request reads the previous request's entry
through the API's lookback window and writes a longer one. That makes three
breakpoints, within the API's limit of four.

**D7 — `prompt_cache_key` only where it is known to be accepted.** The key
is `"marshal-" + hex(sha256(sessionID)[:12])`: stable per conversation,
opaque, 32 characters. It is sent by the Codex provider (always) and by
OpenAI-compatible providers whose base URL host is exactly
`api.openai.com`. Strict OpenAI-compatible servers reject unknown fields,
so every other endpoint keeps its current wire. The session ID already
reaches `OpenAICompatible` (it powers `x-opencode-session`); Codex gains
the same option.

**D8 — Kimi: accept the flat `usage.cached_tokens` field** as a fallback
after the OpenAI detail object and DeepSeek's field, then confirm against
one captured live response.

**D9 — Replace the misleading figure.** `TelemetryStats.CacheHitPct`
becomes `PromptCachePct`: cache-read tokens ÷ prompt tokens over the
telemetry window, shown as "N% cached". The Session section shows the same
share for the whole session. The tool-result cache ratio is dropped from
the UI; it was never labelled as what it was.

## 4. Non-goals and follow-ups

- **Tool-less finalize and handoff calls.** `finalize` and
  `summarizeAndContinue` send no tools. Tools head both Anthropic's and
  OpenAI's cache prefix, so these calls always miss, and the handoff
  summary is the largest request in a long session. Sending the tools with
  `tool_choice: none` would fix it, but changes provider-facing behaviour
  that some backends may not support. Separate change.
- **Lookback limit.** Anthropic looks back about 20 blocks from a
  breakpoint. A step that adds more (more than about 10 parallel tool calls)
  misses once and re-writes. A second rolling breakpoint could extend
  coverage; not worth the complexity until the data shows it happening.
- **1-hour Anthropic TTL.** It costs 2× on writes; only worth a config knob
  if users routinely pause longer than 5 minutes mid-session.
- **Cross-turn history.** History replay (`buildHistoryMessages`) is not
  byte-identical to the live wire (tool results are folded into ledger
  summaries), so the cross-turn prefix ends at the first replayed
  exchange. Fixing that is a history-format change, not a caching change.
- **Web UI display** of cache share; the ACP usage payload already carries
  `cacheReadTokens`.
- **DeepSeek `prompt_cache_miss_tokens` → `CacheWriteTokens`.** DeepSeek has
  no write premium; the mapping is harmless only while the pricing table
  bills writes at the input rate. Out of scope.

## 5. Verification

- Unit: a runner-level regression test asserts that, across the steps of a
  turn where todos change and the clock advances, every request's stable
  prefix is byte-identical to the next request's opening messages, and the
  volatile content still arrives in the tail.
- Live: with `MARSHAL_WIRE_CAPTURE_REQUESTS` set, consecutive request bodies
  from one turn share everything up to the working-state tail. On
  ollama-cloud and Codex, `turn_metrics.cache_read_tokens` for a fresh
  multi-step turn is non-zero.
