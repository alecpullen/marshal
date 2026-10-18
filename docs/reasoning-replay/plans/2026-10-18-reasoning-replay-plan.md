# Reasoning replay — implementation plan

Date: 2026-10-18.
Base: `main` at `ab22c47b`.
Spec: [../feature-spec.md](../feature-spec.md).
Authoring: `marshal-writing-plans`. Execution: `marshal-executing-plans`.

## Goal

Each assistant message the runner appends from a model step carries that step's reasoning. Provider adapters send it back on later requests in their own wire format: `reasoning_content`/`reasoning` (OpenAI-compatible), `thinking` (Ollama native), and encrypted reasoning items (Responses/Codex).

## Non-goals

- Anthropic signed thinking (the Anthropic adapter and `messagesChat` are unchanged).
- Persisting opaque items across turns.
- Mid-turn effort changes.
- Settings-panel editing of `reasoning_history`.

## Assumptions and execution rules

- Tasks are sequential. Tasks 3–5 share `internal/llm/provider` and are build-coupled, so no task is parallel-safe.
- Commit each verified task as `reasoning-replay: task N — <title>`.
- **Known baseline failures.** These fail before any change; do not investigate them:
  - `TestCLIGitOpsCommitAllWithoutARepoIdentity` (worktree; global git identity)
  - `TestStatusLineUntrustedIsWarningColored` (tui)
  - `TestRunManagedCancellationKillsTree` (snapshot; flaky)
- **Temp directory for tests.** On macOS, `internal/app` and `internal/snapshot` tests need a temp directory outside the `/var` symlink. Run them with `TMPDIR=/private/tmp/marshal-gotmp` after `mkdir -p /private/tmp/marshal-gotmp`. Keep that path short: ACP tests bind Unix sockets under `TMPDIR`, and macOS limits socket paths to 104 bytes.
- **Prompt budget.** It is at 8,995 of 9,000 bytes (`TestDefaultPromptBudget`). No task adds system-prompt text.
- **Unchanged default history.** With `reasoning_history` unset, cross-turn history must be byte-identical to today's.

## Contracts

```go
// internal/llm/schema/chat.go
type ChatMessage struct {
	// ...existing fields...
	// Reasoning is the model's reasoning for this assistant message, replayed
	// to the provider that produced it. Nil on non-assistant messages and on
	// assistant messages Marshal synthesizes.
	Reasoning *Reasoning
}

// Reasoning is one step's reasoning: plain text and/or opaque provider items.
type Reasoning struct {
	Text  string
	Items []ReasoningItem
}

// ReasoningItem is an opaque, provider-bound reasoning payload. Only the
// adapter that produced Kind parses Data, and it is replayed only when
// Provider and Model match the request.
type ReasoningItem struct {
	Provider string
	Model    string
	Kind     string
	Data     json.RawMessage
}

// ReasoningKindOpenAI is a Responses API reasoning output item
// ({"type":"reasoning","id",...,"encrypted_content",...,"summary":[...]}).
const ReasoningKindOpenAI = "openai.reasoning"

func (r *Reasoning) Empty() bool                     // nil, or no text and no items
func StripReasoning(msgs []ChatMessage) []ChatMessage // copy with every Reasoning nil
func ReasoningItemsFor(msgs []ChatMessage, provider, model string) []ChatMessage
	// copy; keeps only items whose Provider and Model match; text untouched;
	// a Reasoning left empty becomes nil
func WithoutReasoningItems(msgs []ChatMessage) []ChatMessage
	// copy; Items removed, text kept; a Reasoning left empty becomes nil

// internal/llm/schema/event.go
type ChatEvent struct {
	// ...existing fields...
	// ReasoningItems is populated only when Type == ChatEventDone, by
	// adapters with opaque reasoning (Responses). Provider and Model are left
	// empty; the agent stamps them.
	ReasoningItems []ReasoningItem
}

// internal/app/config/types.go (ProviderConfig)
	ReasoningField string `toml:"reasoning_field,omitempty"` // "", auto, reasoning_content, reasoning, off

// internal/llm/routing/types.go (Preset)
	ReasoningHistory string `toml:"reasoning_history,omitempty"` // "", turn, final_answers
const (
	ReasoningHistoryTurn         = "turn"
	ReasoningHistoryFinalAnswers = "final_answers"
)

// internal/llm/provider (shared Options; CodexOptions gains the same field)
	ReasoningField string
const (
	ReasoningFieldAuto      = "auto"
	ReasoningFieldContent   = "reasoning_content"
	ReasoningFieldReasoning = "reasoning"
	ReasoningFieldOff       = "off"
)
func resolveReplayField(setting string) string // "" or auto -> reasoning_content; off -> ""; else unchanged
func buildChatRequestBody(req schema.ChatRequest, reasoningSummary bool, replayField string) ([]byte, error)
func chatMessageToBody(m schema.ChatMessage, replayField string) chatMessageBody
func replayFieldRejected(body []byte, field string) bool

// internal/agent
type chatResult struct {
	// ...existing fields...
	Reasoning *schema.Reasoning
}
func trimReplayedReasoning(msgs []schema.ChatMessage, over func([]schema.ChatMessage) bool) ([]schema.ChatMessage, int)
func buildHistoryMessages(prior []session.Message, maxTokens int, genInfo session.GenerationInfo,
	audits map[int64][]db.ToolAuditEntry, reasoningHistory string) []schema.ChatMessage
// TurnMetrics (internal/agent/metrics.go) and db.TurnMetric gain:
	ReasoningReplayedChars int
	ReasoningTrimmed       int
// turn_metrics columns: reasoning_replayed_chars, reasoning_trimmed (INTEGER NOT NULL DEFAULT 0)
```

## Task 1: Schema types and helpers

**Goal** — `schema` carries reasoning on messages and Done events, with copy-safe helpers.

**Files**

- `internal/llm/schema/chat.go`
- `internal/llm/schema/event.go`
- `internal/llm/schema/reasoning.go` (**new**)
- `internal/llm/schema/reasoning_test.go` (**new**)

**Steps**

1. In `ChatMessage` (`chat.go`), add after `CacheAnchor bool`:

   ```go
   	// Reasoning is the model's reasoning for this assistant message, replayed
   	// to the provider that produced it. Nil on non-assistant messages and on
   	// assistant messages Marshal synthesizes.
   	Reasoning *Reasoning
   ```

2. In `ChatEvent` (`event.go`), add after `Quota *QuotaInfo`:

   ```go
   	// ReasoningItems is populated only when Type == ChatEventDone, by
   	// adapters with opaque reasoning (Responses). Provider and Model are left
   	// empty; the agent stamps them.
   	ReasoningItems []ReasoningItem
   ```

3. Create `reasoning.go`:

   ```go
   package schema

   import "encoding/json"

   // Reasoning is one step's reasoning: plain text and/or opaque provider items.
   type Reasoning struct {
   	Text  string
   	Items []ReasoningItem
   }

   // ReasoningItem is an opaque, provider-bound reasoning payload. Only the
   // adapter that produced Kind parses Data, and it is replayed only when
   // Provider and Model match the request.
   type ReasoningItem struct {
   	Provider string
   	Model    string
   	Kind     string
   	Data     json.RawMessage
   }

   // ReasoningKindOpenAI is a Responses API reasoning output item.
   const ReasoningKindOpenAI = "openai.reasoning"

   // Empty reports whether r carries nothing to replay.
   func (r *Reasoning) Empty() bool {
   	return r == nil || (r.Text == "" && len(r.Items) == 0)
   }

   // StripReasoning returns a copy of msgs with every Reasoning removed.
   func StripReasoning(msgs []ChatMessage) []ChatMessage {
   	out := make([]ChatMessage, len(msgs))
   	copy(out, msgs)
   	for i := range out {
   		out[i].Reasoning = nil
   	}
   	return out
   }

   // ReasoningItemsFor returns a copy of msgs keeping only the reasoning items
   // produced by provider and model. Text is kept. A Reasoning left empty
   // becomes nil.
   func ReasoningItemsFor(msgs []ChatMessage, provider, model string) []ChatMessage {
   	return filterItems(msgs, func(it ReasoningItem) bool { return it.Provider == provider && it.Model == model })
   }

   // WithoutReasoningItems returns a copy of msgs with every opaque item
   // removed and text kept. A Reasoning left empty becomes nil.
   func WithoutReasoningItems(msgs []ChatMessage) []ChatMessage {
   	return filterItems(msgs, func(ReasoningItem) bool { return false })
   }

   func filterItems(msgs []ChatMessage, keep func(ReasoningItem) bool) []ChatMessage {
   	out := make([]ChatMessage, len(msgs))
   	copy(out, msgs)
   	for i := range out {
   		r := out[i].Reasoning
   		if r == nil {
   			continue
   		}
   		next := &Reasoning{Text: r.Text}
   		for _, it := range r.Items {
   			if keep(it) {
   				next.Items = append(next.Items, it)
   			}
   		}
   		if next.Empty() {
   			next = nil
   		}
   		out[i].Reasoning = next
   	}
   	return out
   }
   ```

4. Create `reasoning_test.go` with these tests:
   - `TestReasoningEmpty`: nil, `{}`, text-only and items-only cases.
   - `TestStripReasoningCopies`: the input slice's messages keep their `Reasoning`; the output has none.
   - `TestReasoningItemsForKeepsMatchingOnly`: one item for (`p`,`m1`) and one for (`p`,`m2`), filtered for `m1`. Exactly the first item is kept, text is kept, and a message whose only item was dropped and that has no text becomes nil.
   - `TestWithoutReasoningItemsKeepsText`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/llm/schema/ && go build ./...
```

Expected: pass; the whole module still builds, because the new fields are additive.

## Task 2: Configuration fields

**Goal** — `reasoning_field` on providers and `reasoning_history` on presets load, save, hoist and round-trip through ACP. `reasoning_field` is editable in the settings panel.

**Files**

- `internal/app/config/types.go`
- `internal/app/config/save.go`
- `internal/app/config/hoist.go`
- `internal/app/config/config_test.go`
- `internal/llm/routing/types.go`
- `internal/acp/config.go`
- `internal/app/tui/settings/frames_collections.go`

**Steps**

1. In `ProviderConfig` (`types.go`), after the `ReasoningSummary` field and its comment, add:

   ```go
   	// ReasoningField chooses how prior reasoning is replayed to this
   	// provider: "" or "auto" (the adapter's default: reasoning_content for
   	// openai_compatible, thinking for ollama, items for openai_codex),
   	// "reasoning_content", "reasoning" (openai_compatible only), or "off".
   	ReasoningField string `toml:"reasoning_field,omitempty"`
   ```

2. In `save.go`, where provider fields are merged (around `merged.ReasoningSummary = in.ReasoningSummary`):
   - In the full-definition branch, add `merged.ReasoningField = in.ReasoningField`.
   - In the credential-only branch, add `if in.ReasoningField != "" { merged.ReasoningField = in.ReasoningField }`.

3. In `providerFieldsEqual` (`hoist.go`), add `a.ReasoningField == b.ReasoningField &&` next to the `ReasoningSummary` comparison.

4. In `routing.Preset` (`internal/llm/routing/types.go`), after `ThinkingOptions`, add:

   ```go
   	// ReasoningHistory controls cross-turn reasoning replay: "" or "turn"
   	// replays reasoning only within a turn; "final_answers" also carries the
   	// reasoning behind earlier turns' final answers (text only).
   	ReasoningHistory string `toml:"reasoning_history,omitempty"`
   ```

   Then add at package level in the same file:

   ```go
   // Preset.ReasoningHistory values.
   const (
   	ReasoningHistoryTurn         = "turn"
   	ReasoningHistoryFinalAnswers = "final_answers"
   )
   ```

5. In `internal/acp/config.go`, mirror `ReasoningSummary`:
   - Add the wire field `ReasoningField string `json:"reasoningField,omitempty"`` after `ReasoningSummary`.
   - Include it in the `ProviderConfig` → wire conversion (around line 99) and the wire → `ProviderConfig` assignment (around line 110).

6. In `frames_collections.go`, after the `reasoning_summary` toggle field, add an enum field. Build it with `NewField`/`SetFieldOptions` as in `frames_custom_agents.go:141`:
   - ID `"providers." + k + ".reasoning_field"`, title `"Reasoning replay"`, kind `KindEnum`.
   - TomlPath `"providers." + k + ".reasoning_field"`.
   - Desc `"send prior reasoning back on later steps (auto picks the provider's field)"`.
   - Options `auto`, `reasoning_content`, `reasoning`, `off`; the getter returns `"auto"` when the value is empty.
   - The setter stores `""` for `auto` and the value otherwise, via `mut(func(p *config.ProviderConfig) { … })`.
   - Mark it write-global like the API key field (`SetFieldWriteGlobal`).

7. Add `TestReasoningConfigFieldsRoundTrip` to `config_test.go`. It loads a user config with `reasoning_field = "off"` on a provider and `reasoning_history = "final_answers"` on a preset, saves it, reloads it, and asserts both values survive.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/app/config/ ./internal/llm/routing/ ./internal/acp/ ./internal/app/tui/settings/
```

Expected: pass.

## Task 3: OpenAI-compatible text replay and fallback

**Goal** — assistant messages carry `reasoning_content` (or the configured field) on the chat-completions path. An endpoint that rejects the field is retried once without it and never sent it again in this process.

**Files**

- `internal/llm/provider/openai_compatible.go`
- `internal/llm/provider/factory.go`
- `internal/llm/provider/reasoning_replay.go` (**new**)
- `internal/llm/provider/openai_compatible_test.go`

**Steps**

1. Create `reasoning_replay.go`:

   ```go
   package provider

   import "regexp"

   // ReasoningField values for Options.ReasoningField / CodexOptions.ReasoningField.
   const (
   	ReasoningFieldAuto      = "auto"
   	ReasoningFieldContent   = "reasoning_content"
   	ReasoningFieldReasoning = "reasoning"
   	ReasoningFieldOff       = "off"
   )

   // resolveReplayField maps the configured setting to the wire field the
   // chat-completions encoder writes; "" means do not replay.
   func resolveReplayField(setting string) string {
   	switch setting {
   	case "", ReasoningFieldAuto:
   		return ReasoningFieldContent
   	case ReasoningFieldOff:
   		return ""
   	default:
   		return setting
   	}
   }

   // replayFieldRejected reports whether an error body names field as a whole
   // word, so "reasoning" does not match "reasoning_effort".
   func replayFieldRejected(body []byte, field string) bool {
   	if field == "" {
   		return false
   	}
   	re := regexp.MustCompile(`(^|[^a-z_])` + regexp.QuoteMeta(field) + `([^a-z_]|$)`)
   	return re.Match(body)
   }
   ```

2. In `Options` (`openai_compatible.go`), add after `ReasoningSummary bool`:

   ```go
   	// ReasoningField chooses how prior reasoning is replayed (see
   	// ReasoningField* constants). "" behaves as auto.
   	ReasoningField string
   ```

   In `OpenAICompatible`, add the fields `replayField string` and `replayRejected atomic.Bool`. Import `sync/atomic` if it is not already imported. In `NewOpenAICompatible`, set `replayField: resolveReplayField(opts.ReasoningField)`.

3. Change `chatMessageToBody(m schema.ChatMessage)` to `chatMessageToBody(m schema.ChatMessage, replayField string)`. After building `body`, add:

   ```go
   	if replayField != "" && m.Role == schema.RoleAssistant && m.Reasoning != nil && m.Reasoning.Text != "" {
   		switch replayField {
   		case ReasoningFieldReasoning:
   			body.Reasoning = m.Reasoning.Text
   		default:
   			body.ReasoningContent = m.Reasoning.Text
   		}
   	}
   ```

   Update the `chatMessageBody` field comment to say the reasoning fields are also written on outgoing assistant messages when replay is on.

4. Change `buildChatRequestBody(req, reasoningSummary)` to `buildChatRequestBody(req schema.ChatRequest, reasoningSummary bool, replayField string)`, and pass `replayField` to `chatMessageToBody`. Update every existing caller in `_test.go` files to pass `""`.

5. In `(*OpenAICompatible).chat`:
   - Compute `field := p.replayField` and set `field = ""` when `p.replayRejected.Load()`.
   - Build the body with `buildChatRequestBody(req, p.reasoningSummary, field)`.
   - Record `sentReplay`: true when `field != ""` and any assistant message has non-empty `Reasoning.Text`.
   - Replace the non-200 branch with:

   ```go
   	if resp.StatusCode == http.StatusBadRequest && sentReplay {
   		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
   		_ = resp.Body.Close()
   		if replayFieldRejected(errBody, field) {
   			if p.replayRejected.CompareAndSwap(false, true) {
   				slog.Default().Warn("provider rejected reasoning replay; disabling it for this session",
   					"provider", p.name, "field", field)
   			}
   			return p.chat(ctx, req)
   		}
   		resp.Body = io.NopCloser(bytes.NewReader(errBody))
   	}
   	if resp.StatusCode != http.StatusOK {
   		defer resp.Body.Close()
   		return nil, HTTPError(p.name, resp)
   	}
   ```

   The recursive call terminates because the latch is now set, so `sentReplay` is false on the retry. Add any imports this needs (`io`, `log/slog`).

6. In `factory.go`, pass `ReasoningField: pc.ReasoningField` in the `NewOpenAICompatible(Options{…})` literal (around line 81).

7. Add these tests to `openai_compatible_test.go`:
   - `TestBuildChatRequestBodyReplaysReasoningContent`: an assistant message with `Reasoning{Text: "plan"}` and field `reasoning_content` gives `"reasoning_content":"plan"` on that message only. User and tool messages have no reasoning key.
   - `TestBuildChatRequestBodyReplaysReasoningField`: the same with field `reasoning`, giving `"reasoning":"plan"` and no `reasoning_content`.
   - `TestBuildChatRequestBodyNoReplayWhenOff`: field `""` gives neither key.
   - `TestOpenAICompatibleRetriesWithoutRejectedReplayField`: an `httptest` server returns 400 with body `{"error":{"message":"unknown field reasoning_content"}}` when the request contains `reasoning_content`, and a valid non-streaming response otherwise. The first `Chat` succeeds after exactly two requests, and a second `Chat` sends no `reasoning_content`.
   - `TestOpenAICompatibleSurfacesUnrelatedBadRequest`: a 400 with body `{"error":{"message":"bad reasoning_effort"}}` returns an error after one request.
   - `TestReplayFieldRejectedWholeWord`: `reasoning` matches `"field reasoning is unknown"` but not `"reasoning_effort"`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/llm/provider/ -run 'BuildChatRequestBody|Replay|OpenAICompatible' && go vet ./internal/llm/provider/
```

Expected: pass.

## Task 4: Ollama native `thinking` replay

**Goal** — the Ollama native adapter sends an assistant message's reasoning as `thinking`, unless `reasoning_field = "off"`.

**Files**

- `internal/llm/provider/ollama_native.go`
- `internal/llm/provider/factory.go`
- `internal/llm/provider/ollama_native_test.go`

**Steps**

1. In `ollamaMessage`, add `Thinking string `json:"thinking,omitempty"`` after `Content`.
2. In `OllamaNative`, add `replayThinking bool`. In `NewOllamaNative`, set `replayThinking: opts.ReasoningField != ReasoningFieldOff`.
3. In `(*OllamaNative).buildChatRequestBody`, after constructing `om`, set `om.Thinking = m.Reasoning.Text` when `p.replayThinking && m.Role == schema.RoleAssistant && m.Reasoning != nil`.
4. In `factory.go`, pass `ReasoningField: pc.ReasoningField` in the `NewOllamaNative(Options{…})` literal (around line 107).
5. Add these tests:
   - `TestOllamaNativeReplaysThinking`: the assistant message's `thinking` equals the reasoning text, and other roles have no `thinking`.
   - `TestOllamaNativeNoThinkingWhenOff`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/llm/provider/ -run 'OllamaNative'
```

Expected: pass.

## Task 5: Responses and Codex reasoning items

**Goal** — capture completed reasoning items and replay them as `reasoning` input items to the same provider and model only. A rejection latches items off for the process and retries once.

**Files**

- `internal/llm/provider/openai_compatible_responses.go`
- `internal/llm/provider/openai_codex.go`
- `internal/llm/provider/factory.go`
- `internal/llm/provider/openai_codex_test.go`
- `internal/llm/provider/openai_compatible_test.go`

**Steps**

1. In `responsesOutputItem`, add `ID string `json:"id,omitempty"`` and `EncryptedContent string `json:"encrypted_content,omitempty"``.

2. Add this helper in `openai_compatible_responses.go`:

   ```go
   // reasoningItemFrom captures a completed reasoning output item for replay.
   // Items without encrypted content cannot be replayed statelessly and are
   // skipped.
   func reasoningItemFrom(item *responsesOutputItem) (schema.ReasoningItem, bool) {
   	if item == nil || item.Type != "reasoning" || item.ID == "" || item.EncryptedContent == "" {
   		return schema.ReasoningItem{}, false
   	}
   	summary := make([]map[string]string, 0, len(item.Summary))
   	for _, s := range item.Summary {
   		summary = append(summary, map[string]string{"type": "summary_text", "text": s.Text})
   	}
   	data, err := json.Marshal(map[string]any{
   		"type": "reasoning", "id": item.ID,
   		"encrypted_content": item.EncryptedContent, "summary": summary,
   	})
   	if err != nil {
   		return schema.ReasoningItem{}, false
   	}
   	return schema.ReasoningItem{Kind: schema.ReasoningKindOpenAI, Data: data}, true
   }
   ```

3. In `streamResponsesEvents`, collect items in a local `var reasoningItems []schema.ReasoningItem`. On `response.output_item.done` with `ev.Item.Type == "reasoning"`, append `reasoningItemFrom(ev.Item)` when its second result is true. Set `ReasoningItems: reasoningItems` on the `ChatEventDone` event the function sends. In `readResponsesResponse`, do the same inside the `case "reasoning":` branch and on its Done event.

4. Add `RawItem json.RawMessage `json:"-"`` to `responsesInputItem`, and a custom `MarshalJSON` on `responsesInputItem` that returns `RawItem` verbatim when it is non-empty. Otherwise it marshals the struct through a local alias type, to avoid recursion.

5. In `buildResponsesInputWithSystemRole`'s `case schema.RoleAssistant:`, before emitting the message item and the `function_call` items, emit every item in `m.Reasoning.Items` with `Kind == schema.ReasoningKindOpenAI` as `responsesInputItem{RawItem: it.Data}`. Do this when `m.Reasoning != nil`. Reasoning items therefore precede that turn's message and calls.

6. Filter the messages at the start of `(*OpenAICompatible).responsesChat` and at the very top of `(*OpenAICodex).Chat`, before the first `p.post`. `Chat` re-posts the same `req` on a 401 refresh and on the `prompt_cache_key` retry, so filtering once at the top keeps every retry consistent.
   - Filter with `req.Messages = schema.ReasoningItemsFor(req.Messages, p.name, req.Model)`.
   - Then, when the provider's replay is off or latched, use `req.Messages = schema.WithoutReasoningItems(req.Messages)`.
   - `OpenAICodex.stream` forwards Done events unchanged except for `Quota`, so captured items pass through it with no change there.
   - For Codex, add `ReasoningField string` to `CodexOptions` and `replayOff bool` (= `opts.ReasoningField == ReasoningFieldOff`) plus `itemsRejected atomic.Bool` to the provider.
   - Pass `ReasoningField: pc.ReasoningField` in `factory.go`'s `NewOpenAICodex(CodexOptions{…})`.

7. In `(*OpenAICodex).Chat`'s 400 handling, after the existing `prompt_cache_key` block, add the reasoning-item retry. Use the same read-body/restore-body pattern. The retry applies when the request carried at least one reasoning item, `itemsRejected` is false, and the body contains `encrypted_content`, `"reasoning item"` or `rs_`. In that case set `itemsRejected`, log once (`"provider rejected reasoning item replay; disabling it for this session"`), and resend with `schema.WithoutReasoningItems(req.Messages)`. Otherwise restore the body for `mapError`.

8. Add these tests:
   - `TestResponsesStreamCapturesReasoningItems`: an SSE fixture with an `output_item.done` reasoning item carrying `id` and `encrypted_content` gives one item, of kind `openai.reasoning`, on the Done event.
   - `TestResponsesStreamSkipsReasoningWithoutEncryptedContent`.
   - `TestBuildResponsesInputPlacesReasoningBeforeFunctionCall`: the `input` order is reasoning, then `function_call`, then `function_call_output`.
   - `TestCodexDropsItemsFromOtherModel`: an item stamped with model `other` is not sent for model `gpt-x`.
   - `TestCodexRetriesWithoutRejectedReasoningItems`: an `httptest` server returns 400 naming `encrypted_content` when the input contains a reasoning item. The call succeeds after two requests, and the next call sends no reasoning items.
   - `TestCodexNoItemsWhenReplayOff`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/llm/provider/ && go vet ./internal/llm/provider/
```

Expected: pass.

## Task 6: Capture in the agent and attach to history

**Goal** — every assistant message the runner appends from a model step carries that step's reasoning, except the token-limit sites. The estimator counts reasoning.

**Files**

- `internal/agent/chat.go`
- `internal/agent/runner.go`
- `internal/agent/finalize.go`
- `internal/agent/compact.go`
- `internal/agent/agenttest/provider.go`
- `internal/agent/reasoning_replay_test.go` (**new**)

**Steps**

1. Add `Reasoning *schema.Reasoning` to `chatResult`.

2. In `chatOnceAttempt`:
   - Keep a local `var reasoningItems []schema.ReasoningItem`.
   - On `schema.ChatEventDone`, copy `event.ReasoningItems`, stamping each item's `Provider = p.Name()` and `Model = model`.
   - In the final successful `return`, set `Reasoning: newReasoning(thinkingBuf.String(), reasoningItems)`.
   - The `ChatEventError` return and the thinking-loop return keep `Reasoning` nil, as they do now.
   - Add this helper:

   ```go
   // newReasoning builds the replay value for a completed step; nil when empty.
   func newReasoning(text string, items []schema.ReasoningItem) *schema.Reasoning {
   	r := &schema.Reasoning{Text: text, Items: items}
   	if r.Empty() {
   		return nil
   	}
   	return r
   }
   ```

3. Attach `Reasoning: res.Reasoning` to the assistant `schema.ChatMessage` at each of these sites:
   - the intent-only reply in `runner.go` (`Content: res.Text` before `intentNudgeMessage`);
   - the native tool-call append (`Content: res.Text, ToolCalls: res.ToolCalls` after `AddNarration`);
   - the envelope parse-failure append (`Content: assistantContent` before `BuildCorrectionMessage`);
   - the envelope valid-action append (`Content: raw` before `pendingRepairNote`);
   - the three model-result appends in `finalize.go`.

   Do **not** attach at the two token-limit sites: the native `isLengthFinish` branch (`Content: res.Text, ToolCalls: res.ToolCalls` before the not-executed tool notices) and the envelope truncated-arguments branch (`Content: assistantContent` before `BuildTruncationMessage`). Leave a one-line comment at each saying reasoning from a truncated step is not replayed.

4. In `estimateTokens` (`compact.go`), inside the loop, add:

   ```go
   		if m.Reasoning != nil {
   			runes += utf8.RuneCountInString(m.Reasoning.Text)
   			for _, it := range m.Reasoning.Items {
   				runes += len(it.Data)
   			}
   		}
   ```

5. In `agenttest.ScriptedProvider`, add `ReasoningItems [][]schema.ReasoningItem` and set `ReasoningItems: p.ReasoningItems[idx]` on the Done event when `idx < len(p.ReasoningItems)`.

6. Create `reasoning_replay_test.go` with these tests:
   - `TestReasoningReplayedOnNextRequest`: a scripted provider has `Thinking: {"plan A", "", ""}` and two tool-call steps. Request 2's assistant message has `Reasoning.Text == "plan A"`.
   - `TestReasoningItemsStampedWithProviderAndModel`: an item on step 1 arrives in request 2 with `Provider == "scripted"` and `Model == "test-model"`.
   - `TestNoReasoningFromLengthTruncatedStep`: step 1 has thinking `"x"` and finish reason `"length"`. Request 2's corresponding assistant message has nil `Reasoning`.
   - `TestEstimateTokensCountsReasoning`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/agent/
```

Expected: pass, including the existing `TestPromptPrefixStableAcrossSteps`.

## Task 7: Budget trimming and metrics

**Goal** — over the turn threshold, replayed reasoning is trimmed oldest-first, never from the latest assistant message, before tool-output pruning. Turn metrics record replayed and trimmed amounts.

**Files**

- `internal/agent/reasoning_trim.go` (**new**)
- `internal/agent/runner.go`
- `internal/agent/metrics.go`
- `internal/app/app.go`
- `internal/db/db.go`
- `internal/db/turnmetrics.go`
- `internal/agent/reasoning_replay_test.go`
- `internal/db/steps_test.go`

**Steps**

1. Create `reasoning_trim.go`:

   ```go
   package agent

   import "marshal/internal/llm/schema"

   // trimReplayedReasoning clears Reasoning on whole assistant messages,
   // oldest first, until over reports false. The most recent assistant
   // message keeps its reasoning. It returns the new slice and how many
   // messages it trimmed; the input slice is not modified.
   func trimReplayedReasoning(msgs []schema.ChatMessage, over func([]schema.ChatMessage) bool) ([]schema.ChatMessage, int) {
   	last := -1
   	for i := len(msgs) - 1; i >= 0; i-- {
   		if msgs[i].Role == schema.RoleAssistant {
   			last = i
   			break
   		}
   	}
   	out := make([]schema.ChatMessage, len(msgs))
   	copy(out, msgs)
   	trimmed := 0
   	for i := range out {
   		if !over(out) {
   			break
   		}
   		if i == last || out[i].Role != schema.RoleAssistant || out[i].Reasoning == nil {
   			continue
   		}
   		out[i].Reasoning = nil
   		trimmed++
   	}
   	return out, trimmed
   }
   ```

2. In `runner.go`, in the block that starts `if turnThreshold > 0 && r.calibratedEstimate(r.withWorkingState(messages)) > turnThreshold {`, insert as the first statement:

   ```go
   			over := func(m []schema.ChatMessage) bool {
   				return r.calibratedEstimate(r.withWorkingState(m)) > turnThreshold
   			}
   			if trimmedMsgs, n := trimReplayedReasoning(messages, over); n > 0 {
   				messages = trimmedMsgs
   				r.withStats(func(s *turnStats) { s.m.ReasoningTrimmed += n })
   			}
   ```

   Then make the existing logic run only if still over. Wrap the remainder of the block in `if over(messages) { … }`, keeping its body unchanged.

3. In `metrics.go` (`TurnMetrics`), add after `IntentNudges`:

   ```go
   	// ReasoningReplayedChars is the reasoning text attached for replay this
   	// turn, counted once per attached step.
   	ReasoningReplayedChars int
   	// ReasoningTrimmed counts assistant messages whose replayed reasoning
   	// was trimmed for the context budget.
   	ReasoningTrimmed int
   ```

   At every site Task 6 attaches reasoning, add `r.withStats(func(s *turnStats) { s.m.ReasoningReplayedChars += len(res.Reasoning.Text) })` when `res.Reasoning != nil`. Route this through one helper, `func (r *Runner) noteReplayedReasoning(rs *schema.Reasoning)`, called at each attach site.

4. In `internal/db/db.go`'s column list, next to `{"turn_metrics", "intent_nudges", "INTEGER NOT NULL DEFAULT 0"}`, add `{"turn_metrics", "reasoning_replayed_chars", "INTEGER NOT NULL DEFAULT 0"}` and `{"turn_metrics", "reasoning_trimmed", "INTEGER NOT NULL DEFAULT 0"}`.
   - In `turnmetrics.go`, add `ReasoningReplayedChars int` and `ReasoningTrimmed int` to the row type after `IntentNudges`.
   - Append both columns after `intent_nudges` in the INSERT, with two more `?` placeholders.
   - Append `row.ReasoningReplayedChars, row.ReasoningTrimmed` after `row.IntentNudges`.
   - In `app.go`, next to `IntentNudges: m.IntentNudges,`, add the two mappings.

5. Add these tests:
   - `TestTrimReplayedReasoningOldestFirstKeepsLatest`: with three assistant messages with reasoning and `over` true until two are trimmed, the first two are nil and the last is kept.
   - `TestTrimReplayedReasoningNeverTrimsLatest`: with `over` always true, only the latest keeps its reasoning.
   - In `steps_test.go`, extend the upgrade test pattern used for `intent_nudges` to the two new columns.

**Verify**

```sh
mkdir -p /private/tmp/marshal-gotmp
CGO_ENABLED=1 go test ./internal/agent/ ./internal/db/
TMPDIR=/private/tmp/marshal-gotmp CGO_ENABLED=1 go test ./internal/app/ -run 'Metrics|Turn|Budget'
```

Expected: pass.

## Task 8: Cross-turn final-answer reasoning

**Goal** — with `reasoning_history = "final_answers"`, kept full final answers carry their stored reasoning text in cross-turn history. The default is byte-identical to today.

**Files**

- `internal/agent/history.go`
- `internal/agent/runner.go`
- `internal/agent/history_test.go`

**Steps**

1. Add a `reasoning string` field to `candEntry`. Where `buildHistoryMessages` appends the `"assistant-full"` candidate from a final message (`m.Content`), also set `reasoning: m.Reasoning`.
2. Add the parameter `reasoningHistory string` to `buildHistoryMessages`. When emitting a kept, non-stub `"assistant-full"` entry with `reasoningHistory == routing.ReasoningHistoryFinalAnswers` and non-empty `ce.reasoning`, set `Reasoning: &schema.Reasoning{Text: ce.reasoning}` on the emitted message. Stubs and every other kind carry none.
3. Update both call sites in `runner.go` to pass `route.Preset.ReasoningHistory`. The `route` variable is in scope; it is resolved earlier in `RunTask`.
4. Add these tests:
   - `TestHistoryReasoningDefaultUnchanged`: the output equals the pre-change output for a two-turn transcript with stored reasoning, when called with `""`.
   - `TestHistoryReasoningFinalAnswersCarriesText`.
   - `TestHistoryReasoningStubbedAnswerCarriesNone`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/agent/
```

Expected: pass.

## Task 9: Strip reasoning from the rollover digest and pin cache stability

**Goal** — the rollover summarizer never receives reasoning, and the prompt-prefix guard covers replay.

**Files**

- `internal/rollover/digest.go`
- `internal/rollover/digest_test.go`
- `internal/agent/cacheprefix_test.go`

**Steps**

1. In `(*LLMSummaryProvider).Digest`, replace `outgoing.Wire = append(outgoing.Wire, …)` with `outgoing.Wire = append(schema.StripReasoning(outgoing.Wire), …)`, keeping the appended directive message unchanged.
2. Add `TestLLMSummaryProvider_Digest_StripsReasoning`: the recorded request has no message with non-nil `Reasoning`.
3. Add `TestPromptPrefixStableAcrossStepsWithReasoning` to `cacheprefix_test.go`. Copy `TestPromptPrefixStableAcrossSteps` with `Thinking: []string{"step one plan", "step two plan", "", ""}` on the scripted provider, and assert `assertPrefixStable(t, p.Requests)`.

**Verify**

```sh
CGO_ENABLED=1 go test ./internal/rollover/ ./internal/agent/ -run 'Digest|PromptPrefix'
```

Expected: pass.

## Task 10: Opt-in live tests

**Goal** — live tests prove the routes accept and use replay. They are excluded from the default suite.

**Files**

- `internal/llm/provider/reasoning_replay_integration_test.go` (**new**, `//go:build integration`)

**Steps**

1. `TestLiveOllamaCloudReplayReachesModel`. Skip unless `MARSHAL_TEST_OLLAMA_CLOUD_KEY` and `MARSHAL_TEST_OLLAMA_CLOUD_MODEL` are set. Use `NewOpenAICompatible` with base URL `https://ollama.com/v1` to send the probe history:
   - user: "Silently pick a secret codeword (do not say it yet), then call lookup with query 'weather'. After the tool returns, reply with ONLY the codeword you picked, or UNKNOWN if you cannot recall it.";
   - assistant: tool call `lookup`, with `Reasoning.Text` stating the codeword `ZEBRA-7731`;
   - tool result: `"sunny"`.

   Assert `ZEBRA-7731` appears in the answer in at least 2 of 3 attempts. Then assert it appears in none of 3 attempts with `ReasoningField: "off"`.
2. `TestLiveCodexReasoningItemsAccepted`. Skip unless `MARSHAL_TEST_CODEX=1`. Use the configured OAuth engine via `provider.NewFromConfigWithAuth` with `provider.DefaultAuthResolver()`. This is a live test that deliberately reads the real keychain, which is why it sits behind the tag and the env switch. Run a two-step tool loop: capture the step-1 Done event's items and stamp them like the agent does. Assert step 2 is accepted with the items replayed, and log `ReasoningTokens` and `CacheReadTokens` for both steps.

**Verify**

```sh
CGO_ENABLED=1 go vet -tags integration ./internal/llm/provider/ && CGO_ENABLED=1 go test ./internal/llm/provider/
```

Expected: vet passes with the tag; the default run does not compile or run these tests. Running them live is a manual step outside this plan.

## Task 11: Documentation and full verification

**Goal** — docs describe the settings, and the full suite passes against the baseline.

**Files**

- `docs/reasoning-replay/feature-spec.md` (status and implementation record)
- `docs/ARCHITECTURE.md` (one sentence in the model-layer section stating that assistant messages carry provider-bound reasoning)

**Steps**

1. In the spec, set `Status: implemented` and `Implementation: complete`, and add an `## Implementation record` listing the task commits.
2. Add the architecture sentence.

**Verify**

```sh
mkdir -p /private/tmp/marshal-gotmp
gofmt -l . | grep -v '^\.marshal/' ; go vet ./...
TMPDIR=/private/tmp/marshal-gotmp CGO_ENABLED=1 go test ./... 2>&1 | grep -E '^(--- FAIL|FAIL)'
```

Expected: `gofmt` prints nothing; `vet` passes. The only failures are the known baseline tests listed above. `TestDefaultPromptBudget` passes unchanged.

## Integration notes

- **Effectiveness check** (after shipping, over a few days of real use; not a plan task). Compare sessions with replay on against `reasoning_field = "off"` on the same provider. Measure:
  - reasoning characters per step (`messages.reasoning`);
  - steps and tool calls per turn, `reasoning_replayed_chars` and `reasoning_trimmed` (`turn_metrics`);
  - thinking-loop aborts (`.marshal/marshal.log`);
  - prompt tokens per turn, and Codex `cache_read_tokens`.
- Phase 1 (mid-turn effort changes) follows in its own spec and must keep thinking enabled within a turn whenever opaque items are being replayed.

## Plan authoring checks

- Every task has exact files, steps that state decisions, named tests and a runnable Verify block.
- Cross-task types and signatures are declared in Contracts.
- Anchors were checked at `ab22c47b`: `chatMessageToBody`, `buildChatRequestBody`, `chatOnceAttempt`/`thinkingBuf`, the runner append sites, `estimateTokens`, the over-threshold block, `candEntry`/`buildHistoryMessages`, `LLMSummaryProvider.Digest`, `ScriptedProvider.Thinking`, `responsesOutputItem`, `buildResponsesInputWithSystemRole`, `streamResponsesEvents`/`readResponsesResponse`, the Codex 400 block, `providerFieldsEqual`, and the `intent_nudges` column plumbing.
- No task adds system-prompt bytes.
