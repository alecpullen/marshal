# Reasoning replay

Status: implemented.
Date: 2026-10-18.
Source baseline: `ab22c47b`.
Implementation: complete. Plan: [plans/2026-10-18-reasoning-replay-plan.md](plans/2026-10-18-reasoning-replay-plan.md).

## Purpose

Send a model's own reasoning from earlier steps of a turn back to it on later steps, so it continues its plan instead of re-deriving it. Today every request carries only role, content and tool calls; the reasoning each step produced is shown in the TUI, persisted, and then dropped from the wire.

This was chosen ahead of mid-turn effort changes ("phase 1", a separate later spec) because it may change agent behaviour enough to affect what that phase needs, and because some providers constrain thinking changes once reasoning is replayed.

## Evidence

**Agent behaviour (week to 2026-10-18).** The main agent averaged ~10.9k reasoning characters per step against ~170 characters of visible content, with up to 217k characters in one step and 39 thinking-loop aborts (`.marshal/marshal.log`), typically "OK. Let me write. Writing. Let me write…" before an edit. See the decisiveness baseline recorded the same day.

**Provider guidance.** Every reasoning family in use recommends replay during tool loops, without independent benchmarks:

- DeepSeek's own API requires `reasoning_content` on every later request once tools are involved, otherwise HTTP 400 ([docs](https://api-docs.deepseek.com/guides/thinking_mode/)). This rule does not bind the hosts Marshal uses (below).
- OpenAI: reusing reasoning items between function calls "boosts intelligence, reduces token usage, and increases cache hit rates" ([announcement](https://openai.com/index/new-tools-and-features-in-the-responses-api)). Marshal already requests `reasoning.encrypted_content` on every Codex call and discards it.
- Z.ai GLM "preserved thinking" ([docs](https://docs.z.ai/guides/capabilities/thinking-mode)), Moonshot Kimi ([docs](https://platform.kimi.ai/docs/guide/use-thinking-models)), and MiniMax, which says dropping it "significantly" lowers performance ([post](https://www.minimax.io/news/why-is-interleaved-thinking-important-for-m2)).

**Live probes (2026-10-18, throwaway scripts, not committed).** A codeword planted only in a replayed assistant message's reasoning field, then requested inside the same tool loop, with three trials per variant on `deepseek-v4.1-flash`:

| Route | none | `reasoning_content` | `reasoning` | `thinking` |
|---|---|---|---|---|
| Ollama Cloud `/v1` (OpenAI-compatible, Marshal's route) | 0/3 | **3/3** | **3/3** | 0/3 |
| Ollama Cloud `/api/chat` (native) | 0/3 | 0/3 | 0/3 | **2/3** |
| opencode-go `/v1` | 0/3 | **3/3** | 0/3 | 0/3 |

No route rejected requests that omit reasoning. A small A/B on a five-step bug-fix task (three runs each) showed every run passing in five steps. Reasoning after step 1 fell from an average of 621 to 562 characters. The model re-derived "where was I" after writing the fix in 2 of 3 runs without replay and in 0 of 3 with it, for +14% prompt tokens. The task was too easy to be decisive. Codex was not probed live; its contract is documented by OpenAI.

## Scope

In scope:

- Plain reasoning text replay for OpenAI-compatible providers and Ollama native.
- Codex/Responses encrypted reasoning items, replayed only to the provider and model that produced them.
- Within-turn replay always, plus an optional per-preset cross-turn mode for final answers (text only).
- Automatic fallback when an endpoint rejects the field.
- Budget accounting and oldest-first trimming.
- Metrics, and opt-in live tests.

Out of scope:

- Anthropic signed thinking blocks; the Anthropic adapter is unchanged.
- Persisting opaque items across turns.
- Mid-turn effort or model changes (phase 1, separate).
- Settings-panel editing of the per-preset cross-turn option (TOML only in this delivery).

## Design

### Data model

`schema.ChatMessage` gains `Reasoning *Reasoning`:

- `Text`: the step's reasoning as streamed, identical to what the TUI shows.
- `Items`: opaque provider payloads. Each item carries `Provider`, `Model`, `Kind` and raw `Data`, and is never parsed outside its adapter.

The agent's per-step `chatResult` carries the same value. Providers emit opaque items on the Done event (`ChatEvent.ReasoningItems`); text is accumulated from the existing `DeltaThinking` deltas (`thinkingBuf` in `chatOnceAttempt`).

### Capture and attachment

The runner attaches `Reasoning` to every assistant message it appends from a model step: the native tool-call, narration and envelope sites in `runner.go`, and the final-answer sites in `finalize.go`. It is nil when:

- the stream ended in a thinking-loop abort (no result is returned today);
- the result came from an error-terminated stream (salvage path);
- the step stopped at the output-token limit;
- the message is synthesized by Marshal (plan text, progress summaries, compaction handoffs, the not-executed tool notice).

Empty text with no items is nil.

### Encoding per adapter

- **OpenAI-compatible.** Assistant messages carry `reasoning_content` (default) or `reasoning`, chosen by the provider's `reasoning_field` setting: `auto` (= `reasoning_content`), `reasoning_content`, `reasoning`, or `off`.
  - **Fallback:** a 400 whose body names the active field is retried once without it, and the provider stops sending it for the rest of the process, logging one warning. Other errors are unchanged.
- **Ollama native.** Assistant messages carry `thinking`; `off` disables it.
- **Responses/Codex.**
  - **Capture:** completed `reasoning` output items (`id`, `encrypted_content`, `summary`) become items of kind `openai.reasoning`.
  - **Replay:** on encoding, each item is emitted as an input item of type `reasoning` immediately before that assistant message's first `function_call` item (or its message item when there are no calls). Only items whose `Provider` and `Model` match the request are emitted.
  - **Rejection:** an error naming a reasoning item drops items for the rest of the process and retries once.
- **Anthropic:** unchanged.

### Scope across turns

`routing.Preset.ReasoningHistory` takes one of two values:

- `"turn"` (default): today's cross-turn history, unchanged.
- `"final_answers"`: `buildHistoryMessages` attaches each kept full final answer's stored `session.Message.Reasoning` as text. Stubbed and dropped exchanges carry none, and opaque items are never carried across turns.

### Budget

- `estimateTokens` counts reasoning text and item payload bytes.
- When the turn is over its threshold (`runner.go`, before `pruneStaleToolOutputs`), `trimReplayedReasoning` clears `Reasoning` on whole assistant messages, oldest first, until the turn is under threshold. It never touches the most recent assistant message, and never edits text partially.
- Only then do the existing pruning and rollover run.
- Each trim changes the prompt prefix once. With no trims, the prefix is append-only, so `TestPromptPrefixStableAcrossSteps` still holds.

### Helper requests

The rollover digest (`internal/rollover/digest.go`) strips `Reasoning` from the wire before calling the summarizer. Title generation and the classifier send no history and are unaffected.

### Observability

`turn_metrics` gains two columns:

- `reasoning_replayed_chars`: text characters sent as replay, summed over the turn's requests' new attachments.
- `reasoning_trimmed`: assistant messages trimmed.

A fallback that disables replay logs once per provider.

### Compatibility with phase 1

Changing the effort level mid-turn leaves replay as it is. Setting thinking to `off` mid-turn would stop emitting opaque items. Phase 1 will allow level changes only. A later model switch mid-turn would drop opaque items, which are bound to provider and model, and keep text on the same provider.

## Error handling

- **Rejected field.** The OpenAI-compatible 400 that names the field and the Codex error that names a reasoning item each trigger one retry and a per-process latch. Any other error surfaces unchanged.
- **Degenerate or partial reasoning is never replayed.** That covers thinking-loop aborts, salvaged results and token-limit cuts.
- **Cancelled streams:** no assistant message is appended, so there is nothing to attach.
- **Parallel tool calls:** one reasoning value per assistant message.
- **Envelope mode:** the same mechanism applies whenever the provider returns reasoning.
- **Subagents:** each child turn replays its own reasoning, and reports to the parent are unchanged.
- **Rollover:** a rollover seeds a fresh context without prior reasoning.
- **Exposure:** reasoning is only sent back to the provider that produced it and is already persisted locally, so nothing new leaves the machine.

## Verification

**Unit tests:**

- Golden request JSON for each adapter, field choice and `off`.
- Responses ordering, and dropping items for a mismatched model.
- The fallback, using `httptest`: the 400 is retried without the field and the latch persists, and an unrelated 400 surfaces.
- Runner attachment at each site, and the nil cases.
- Estimator counting.
- Trim order and that the most recent message is never trimmed.
- Digest stripping.
- Cross-turn behaviour under both `reasoning_history` values.
- A replay variant of `TestPromptPrefixStableAcrossSteps`.

**Live tests** (build tag `integration`, opt-in):

- The Ollama Cloud codeword probe.
- A Codex two-step tool loop with replayed items, accepted, recording reasoning and cached tokens with and without replay.

**Effectiveness.** Compare real sessions with replay on against `reasoning_field = "off"` as the control. Measure reasoning characters per step, steps and tool calls per turn, thinking-loop aborts, prompt tokens per turn, and Codex cached tokens. On/off rather than before/after, because prompt-rule and effort changes landed the same week.

## Open questions

None.

## Implementation record

Implemented on branch `reasoning-replay` from `320c7a8f`, one commit per plan
task:

| Task | Commit | Title |
|------|--------|-------|
| 1 | `aaeb84e8` | Schema types and helpers |
| 2 | `a00056ae` | Configuration fields |
| 3 | `f0ecb523` | OpenAI-compatible text replay and fallback |
| 4 | `26284e08` | Ollama native thinking replay |
| 5 | `93d5962a` | Responses and Codex reasoning items |
| 6 | `02fc5e9f` | Capture in the agent and attach to history |
| 7 | `0830c510` | Budget trimming and metrics |
| 8 | `1a8c6ace` | Cross-turn final-answer reasoning |
| 9 | `152920e4` | Strip reasoning from the rollover digest and pin cache stability |
| 10 | `c2794d82` | Opt-in live tests |
| 11 | — | Documentation and full verification |

Deviations from the plan:

- `ReasoningHistory` landed on `routing.ModelPreset`, not `routing.Preset`:
  `Preset` is the string field on `RoleBinding`, while the anchored
  `ThinkingOptions` field and the `route.Preset` accessor both name
  `ModelPreset`.
- The Codex rejection matcher (`isReasoningItemRejection`) matches the bare
  phrase `reasoning item` rather than a quoted one, alongside
  `encrypted_content` and `rs_`.
- `TestPromptPrefixStableAcrossStepsWithReasoning` copies the setup, the
  `RunTask` call and the `assertPrefixStable` assertion of the original test,
  not its pack/tail assertions.

The live tests in task 10 are opt-in and were not run; they are verified to
compile under the `integration` tag only.
