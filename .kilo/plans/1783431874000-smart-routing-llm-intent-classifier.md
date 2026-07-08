# Smart Model Routing via LLM Intent Classifier

## Goal

Replace the static keyword-based `agent.Classify` with an optional LLM-based intent classifier. When a `[classifier]` preset is configured, Marshal sends the user message to a small local model, gets an intent label, maps it deterministically to an `AgentRole`, and routes the turn to the corresponding preset in the active `AgentProfile`. If the classifier is missing, errors, or returns low-confidence/unknown output, the system falls back to the existing static keyword classification.

## Decisions (final)

| Topic | Decision |
|---|---|
| Classifier output | Intent label (e.g. `explain`, `search`, `write`, `refactor`, `test`, `fix`, `review`, `security_review`, `knowledge`, `summarize`, `run_command`, `fallback`). |
| Role mapping | Deterministic intent → `AgentRole` map in code. |
| Config | Optional `[classifier]` preset in config (provider + model + temperature). |
| Fallback | Static keyword classification if classifier is unconfigured, errors, or returns `confidence < 0.7` / unknown intent. |
| Integration point | Replace `agent.Classify`; the rest of the routing pipeline (`ResolveRole`, `AgentProfile`) stays unchanged. |
| New role | Add `RoleCommandRunner` for shell/git operations; default `command-runner` preset maps to a small local model. |

## Intent → Role map

```
explain          → RoleRepoScout
search           → RoleRepoScout
write            → RoleImplementer
refactor         → RoleImplementer
fix              → RoleImplementer
review           → RoleReviewer
security_review  → RoleSecurityReviewer
test             → RoleTester
run_command      → RoleCommandRunner
knowledge        → RoleKnowledge
summarize        → RoleSummarizer
fallback         → RoleImplementer
```

Unknown intent or missing intent falls back to static classification.

## Classifier output schema

The classifier prompts the model to return JSON:

```json
{"intent": "search", "confidence": 0.92}
```

- `intent`: one of the allowed intent labels.
- `confidence`: float between 0 and 1.
- If the model returns prose instead of JSON, attempt to extract a JSON object. If extraction fails, treat as low confidence and fall back.

## Config changes

Add a new optional top-level config section:

```toml
[classifier]
provider = "ollama"
model = "llama3.2:3b"
temperature = 0.1
max_tokens = 64
```

Defaults if omitted:
- `temperature = 0.1`
- `max_tokens = 64` (classification output is tiny)

Add a new default preset for the command-runner role:

```toml
[models.presets.command-runner]
provider = "ollama"
model = "llama3.2:3b"
local_only = true
```

Add the role mapping to the default agent profile:

```toml
[agent_profiles.default]
# existing roles...
command_runner = "command-runner"
```

## Source edits

1. **`internal/llm/routing/types.go`**
   - Add `RoleCommandRunner AgentRole = "command_runner"`.

2. **`internal/app/config/config.go`**
   - Add `Classifier` struct (Provider, Model, Temperature, MaxTokens) to the config surface.
   - Add `Classifier` field to `AgentConfig` or top-level `Config`.
   - Add `Classifier` to the defaults.
   - Add merge/save logic for the new section (follow existing provider/preset patterns).

3. **`internal/app/config/defaults.go`** (or wherever defaults live)
   - Add default `command-runner` preset.
   - Add `command_runner` mapping to the default agent profile.

4. **`internal/agent/classifier.go`** (new file)
   - Define `Classifier` interface with `Classify(ctx, goal string) (Intent, Confidence, error)`.
   - Implement `LLMClassifier` backed by a provider.
   - Implement `StaticClassifier` that wraps the existing keyword logic.
   - Implement `classifierPrompt(goal string) string`.
   - Implement `parseClassifierResponse(raw string) (Intent, Confidence, error)`.
   - Define the intent → role map.

5. **`internal/agent/runner.go`**
   - Replace the direct call to `Classify(goal)` with `r.classifier.Classify(ctx, goal)`.
   - Add `Classifier` field to `Runner`.
   - Wire the classifier in `NewRunner` or via `app.go`.

6. **`internal/app/app.go`**
   - If `cfg.Classifier` is configured, build a provider for it and inject it into the runner as `LLMClassifier`.
   - Otherwise, inject `StaticClassifier`.

7. **`internal/agent/classifier_test.go`** (new file)
   - Test `StaticClassifier` keyword paths.
   - Test `LLMClassifier` with scripted responses.
   - Test fallback on low confidence / unknown intent / JSON parse failure.
   - Test intent → role mapping.

8. **`internal/app/config/config_test.go`**
   - Test config parsing for `[classifier]`.
   - Test defaults include `command-runner` preset and `command_runner` role.

9. **`internal/llm/routing/router_test.go`**
   - Add test for `RoleCommandRunner` resolution if it changes routing behavior.

## Failure modes and handling

| Scenario | Behavior |
|---|---|
| `[classifier]` not configured | Use `StaticClassifier` (existing behavior). |
| Classifier provider fails to build | Log error, use `StaticClassifier`. |
| Classifier LLM call errors | Use `StaticClassifier`. |
| Confidence < 0.7 | Use `StaticClassifier`. |
| Unknown intent | Use `StaticClassifier`. |
| JSON parse failure | Use `StaticClassifier`. |
| Model returns valid intent but high confidence | Use the intent → role map. |

## Out of scope

- Caching classifier results across turns (can be added later).
- Learning or updating the intent map from user feedback.
- Per-user or per-project custom intent maps.
- Fine-tuning a custom classifier model.
- Streaming the classification result.

## Validation

- `go build ./cmd/marshal` clean.
- `go test ./internal/agent/...` green, including new classifier tests.
- `go test ./internal/app/config/...` green.
- `go test ./internal/llm/routing/...` green.
- Manual test: configure `[classifier]` with a local model, ask "run git status" and confirm it routes to `command_runner`; ask "explain this file" and confirm it routes to `repo_scout`; ask ambiguous input and confirm it falls back to static classification.

## Risks

- A 3B classifier can mis-route ambiguous requests. The confidence threshold + static fallback mitigates this.
- Adding an extra LLM call increases per-turn latency by ~0.5–2s on CPU. This is acceptable for a turn-based coding agent and can be skipped by omitting `[classifier]`.
- The new `RoleCommandRunner` requires users to add the `command-runner` preset to their active profile if they use a custom profile. The default profile will include it.

## Next step

Implement this plan in an implementation-capable agent.