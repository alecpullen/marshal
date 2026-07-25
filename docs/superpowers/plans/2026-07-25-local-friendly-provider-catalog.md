# Local-Friendly Provider Catalog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expand Marshal's built-in provider and model catalogs, flip `remote_providers_allowed` to opt-out, and update first-run onboarding so users pick their default setup from a rich, curated list.

**Architecture:** Keep the existing static Go maps as the single source of truth for templates and model metadata, leaving a clean seam for the remote-catalog work on `main`. Update the connect/onboarding wizards to consume the expanded catalog directly. Preserve the privacy gate so explicit opt-out still works.

**Tech Stack:** Go, Bubble Tea, TOML, existing `internal/llm/provider`, `internal/llm/catalog`, `internal/llm/pricing`, `internal/app/config`, `internal/app/tui/connect`, `internal/app/onboarding`.

## Global Constraints

- All new provider templates use `Type = "openai_compatible"`; no new provider backend types are added.
- `config.Default()` must not contain concrete provider entries, API keys, or presets.
- `privacy.remote_providers_allowed` defaults to `true`.
- `web.enabled` remains `false` by default.
- Model context/max-output values are conservative and sourced from public docs; unknown models resolve to `(0, 0)`.
- The remote-catalog fetch on `main` is out of scope; do not add network loading code.
- Every code change is accompanied by a test change; run `go test ./...` before each commit.

---

## File map

| File | Responsibility |
|---|---|
| `internal/llm/provider/templates.go` | Built-in provider template definitions and lookup helpers. |
| `internal/llm/provider/templates_test.go` | Tests for template lookup, `All()`, `UniqueName()`. |
| `internal/llm/catalog/catalog.go` | Built-in model context-window / max-output metadata. |
| `internal/llm/catalog/catalog_test.go` | Tests for `catalog.Lookup()`. |
| `internal/llm/pricing/prices.go` | Static per-model pricing table. |
| `internal/app/config/defaults.go` | Default config values including `RemoteProvidersAllowed`. |
| `internal/app/config/config_test.go` | Default-config assertions. |
| `internal/app/tui/connect/connect.go` | Provider picker UI and setup wizard. |
| `internal/app/tui/connect/connect_test.go` | Wizard behavior tests. |
| `internal/app/onboarding.go` | First-run onboarding flow. |
| `internal/app/onboarding_test.go` | Onboarding output tests. |
| `internal/llm/routing/router_test.go` | Route resolution including remote gate. |
| `CLAUDE.md` | Project guidance; update framing. |
| `docs/09-configuration-examples.md` | Config examples; update default and add providers. |
| `docs/04-tooling-and-shell-safety.md` | Update remote-provider default mention if present. |

---

### Task 1: Add `Recommended` field and expand provider templates

**Files:**
- Modify: `internal/llm/provider/templates.go`
- Test: `internal/llm/provider/templates_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `ProviderTemplate.Recommended bool`; expanded `templates` map; unchanged `Lookup`, `All`, `UniqueName` signatures.

- [ ] **Step 1: Write failing tests for new templates and recommended flag**

  In `internal/llm/provider/templates_test.go`, add:

  ```go
  func TestProviderTemplates_ContainsExpectedEntries(t *testing.T) {
      expected := []string{
          "ollama", "lmstudio", "llamacpp", "vllm", "tabbyapi", "koboldcpp",
          "openai", "anthropic", "google", "groq", "openrouter",
          "together", "fireworks", "deepseek", "perplexity", "mistral",
          "cohere", "azure_openai", "xai", "openai_compatible",
      }
      for _, id := range expected {
          if _, ok := Lookup(id); !ok {
              t.Errorf("expected provider template %q to exist", id)
          }
      }
  }

  func TestProviderTemplates_RecommendedFirst(t *testing.T) {
      tpl, ok := Lookup("openai")
      if !ok {
          t.Fatal("openai template missing")
      }
      if !tpl.Recommended {
          t.Error("openai should be recommended")
      }
      if tpl, ok := Lookup("xai"); ok && tpl.Recommended {
          t.Error("xai should not be recommended")
      }
  }

  func TestProviderTemplates_LocalTemplates(t *testing.T) {
      for _, id := range []string{"ollama", "lmstudio", "llamacpp", "vllm", "tabbyapi", "koboldcpp"} {
          tpl, ok := Lookup(id)
          if !ok {
              t.Fatalf("template %q missing", id)
          }
          if !tpl.Local {
              t.Errorf("%q should be local", id)
          }
      }
  }
  ```

- [ ] **Step 2: Run tests to verify they fail**

  Run:
  ```bash
  go test ./internal/llm/provider/... -run TestProviderTemplates -v
  ```
  Expected: FAIL — missing templates and `Recommended` field.

- [ ] **Step 3: Expand `ProviderTemplate` and `templates`**

  In `internal/llm/provider/templates.go`, add `Recommended bool` to the struct and expand the map. Replace the current map with the full set (abbreviated example below; use the values from the spec):

  ```go
  type ProviderTemplate struct {
      ID          string
      Label       string
      Type        string
      BaseURL     string
      Local       bool
      Recommended bool
      ToolCalling bool
      KeyEnv      string
      KeyHint     string
      Models      []string
  }

  var templates = map[string]ProviderTemplate{
      "ollama": {
          ID:          "ollama",
          Label:       "Ollama (local)",
          Type:        "openai_compatible",
          BaseURL:     "http://localhost:11434/v1",
          Local:       true,
          Recommended: true,
          Models:      []string{"qwen2.5-coder:7b", "qwen2.5-coder:14b", "llama3.1:8b", "deepseek-coder-v2:16b"},
      },
      "lmstudio": {
          ID:      "lmstudio",
          Label:   "LM Studio (local)",
          Type:    "openai_compatible",
          BaseURL: "http://localhost:1234/v1",
          Local:   true,
      },
      "llamacpp": {
          ID:      "llamacpp",
          Label:   "llama.cpp server (local)",
          Type:    "openai_compatible",
          BaseURL: "http://localhost:8080/v1",
          Local:   true,
      },
      "vllm": {
          ID:          "vllm",
          Label:       "vLLM (local)",
          Type:        "openai_compatible",
          BaseURL:     "http://localhost:8000/v1",
          Local:       true,
          ToolCalling: true,
      },
      "tabbyapi": {
          ID:      "tabbyapi",
          Label:   "TabbyAPI (local)",
          Type:    "openai_compatible",
          BaseURL: "http://localhost:5000/v1",
          Local:   true,
      },
      "koboldcpp": {
          ID:      "koboldcpp",
          Label:   "koboldcpp (local)",
          Type:    "openai_compatible",
          BaseURL: "http://localhost:5001/v1",
          Local:   true,
      },
      "openai": {
          ID:          "openai",
          Label:       "OpenAI",
          Type:        "openai_compatible",
          BaseURL:     "https://api.openai.com/v1",
          Recommended: true,
          ToolCalling: true,
          KeyEnv:      "OPENAI_API_KEY",
          KeyHint:     "Get a key at https://platform.openai.com/api-keys",
          Models:      []string{"gpt-4o", "gpt-4o-mini", "o3-mini"},
      },
      "anthropic": {
          ID:          "anthropic",
          Label:       "Anthropic",
          Type:        "openai_compatible",
          BaseURL:     "https://api.anthropic.com/v1",
          Recommended: true,
          ToolCalling: true,
          KeyEnv:      "ANTHROPIC_API_KEY",
          KeyHint:     "Get a key at https://console.anthropic.com/settings/keys",
          Models:      []string{"claude-sonnet-4-20250514", "claude-opus-4-20250514", "claude-haiku-4-20250514"},
      },
      "google": {
          ID:          "google",
          Label:       "Google Gemini",
          Type:        "openai_compatible",
          BaseURL:     "https://generativelanguage.googleapis.com/v1beta/openai",
          Recommended: true,
          ToolCalling: true,
          KeyEnv:      "GEMINI_API_KEY",
          KeyHint:     "Get a key at https://aistudio.google.com/app/apikey",
          Models:      []string{"gemini-2.5-pro", "gemini-2.5-flash"},
      },
      "groq": {
          ID:          "groq",
          Label:       "Groq",
          Type:        "openai_compatible",
          BaseURL:     "https://api.groq.com/openai/v1",
          Recommended: true,
          ToolCalling: true,
          KeyEnv:      "GROQ_API_KEY",
          KeyHint:     "Get a key at https://console.groq.com/keys",
          Models:      []string{"llama-3.3-70b-versatile", "qwen-2.5-32b", "deepseek-r1-distill-llama-70b"},
      },
      "openrouter": {
          ID:          "openrouter",
          Label:       "OpenRouter",
          Type:        "openai_compatible",
          BaseURL:     "https://openrouter.ai/api/v1",
          Recommended: true,
          ToolCalling: true,
          KeyEnv:      "OPENROUTER_API_KEY",
          KeyHint:     "Get a key at https://openrouter.ai/keys",
          Models:      []string{"anthropic/claude-sonnet-4", "google/gemini-2.5-pro", "meta-llama/llama-3.3-70b-instruct"},
      },
      "together": {
          ID:          "together",
          Label:       "Together AI",
          Type:        "openai_compatible",
          BaseURL:     "https://api.together.xyz/v1",
          ToolCalling: true,
          KeyEnv:      "TOGETHER_API_KEY",
          KeyHint:     "Get a key at https://api.together.xyz/settings/api-keys",
          Models:      []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo", "Qwen/Qwen2.5-Coder-32B-Instruct"},
      },
      "fireworks": {
          ID:          "fireworks",
          Label:       "Fireworks AI",
          Type:        "openai_compatible",
          BaseURL:     "https://api.fireworks.ai/inference/v1",
          ToolCalling: true,
          KeyEnv:      "FIREWORKS_API_KEY",
          KeyHint:     "Get a key at https://app.fireworks.ai/account/api-keys",
          Models:      []string{"accounts/fireworks/models/llama-v3p3-70b-instruct"},
      },
      "deepseek": {
          ID:          "deepseek",
          Label:       "DeepSeek",
          Type:        "openai_compatible",
          BaseURL:     "https://api.deepseek.com",
          Recommended: true,
          ToolCalling: true,
          KeyEnv:      "DEEPSEEK_API_KEY",
          KeyHint:     "Get a key at https://platform.deepseek.com/api_keys",
          Models:      []string{"deepseek-chat", "deepseek-reasoner"},
      },
      "perplexity": {
          ID:          "perplexity",
          Label:       "Perplexity",
          Type:        "openai_compatible",
          BaseURL:     "https://api.perplexity.ai",
          ToolCalling: true,
          KeyEnv:      "PERPLEXITY_API_KEY",
          KeyHint:     "Get a key at https://www.perplexity.ai/settings/api",
          Models:      []string{"sonar", "sonar-pro", "sonar-reasoning"},
      },
      "mistral": {
          ID:          "mistral",
          Label:       "Mistral AI",
          Type:        "openai_compatible",
          BaseURL:     "https://api.mistral.ai/v1",
          ToolCalling: true,
          KeyEnv:      "MISTRAL_API_KEY",
          KeyHint:     "Get a key at https://console.mistral.ai/api-keys/",
          Models:      []string{"mistral-large-latest", "codestral-latest"},
      },
      "cohere": {
          ID:          "cohere",
          Label:       "Cohere",
          Type:        "openai_compatible",
          BaseURL:     "https://api.cohere.ai/v1",
          ToolCalling: true,
          KeyEnv:      "COHERE_API_KEY",
          KeyHint:     "Get a key at https://dashboard.cohere.com/api-keys",
          Models:      []string{"command-r-plus", "command-r"},
      },
      "azure_openai": {
          ID:          "azure_openai",
          Label:       "Azure OpenAI",
          Type:        "openai_compatible",
          BaseURL:     "https://{your-resource}.openai.azure.com/openai/deployments/{deployment-id}",
          ToolCalling: true,
          KeyEnv:      "AZURE_OPENAI_API_KEY",
          KeyHint:     "Set your resource name and deployment id in the base URL",
      },
      "xai": {
          ID:          "xai",
          Label:       "xAI",
          Type:        "openai_compatible",
          BaseURL:     "https://api.x.ai/v1",
          ToolCalling: true,
          KeyEnv:      "XAI_API_KEY",
          KeyHint:     "Get a key at https://console.x.ai/",
          Models:      []string{"grok-3", "grok-3-mini"},
      },
      "openai_compatible": {
          ID:      "openai_compatible",
          Label:   "Custom (OpenAI-compatible)",
          Type:    "openai_compatible",
          BaseURL: "",
          Local:   false,
      },
  }
  ```

- [ ] **Step 4: Run tests to verify they pass**

  Run:
  ```bash
  go test ./internal/llm/provider/... -v
  ```
  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/llm/provider/templates.go internal/llm/provider/templates_test.go
  git commit -m "feat(provider): expand built-in catalog and add Recommended flag"
  ```

---

### Task 2: Expand model metadata catalog

**Files:**
- Modify: `internal/llm/catalog/catalog.go`
- Test: `internal/llm/catalog/catalog_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: more entries in the `builtin` map; unchanged `Lookup` signature.

- [ ] **Step 1: Write failing tests for new model ids**

  In `internal/llm/catalog/catalog_test.go`, add:

  ```go
  func TestLookup_HostedModels(t *testing.T) {
      cases := map[string]struct{ context, output int }{
          "gpt-4o":                   {128000, 16384},
          "gpt-4o-mini":              {128000, 16384},
          "claude-sonnet-4-20250514": {200000, 8192},
          "gemini-2.5-pro":           {1000000, 8192},
          "deepseek-chat":            {64000, 8192},
          "llama-3.3-70b-versatile":  {128000, 8192},
          "text-embedding-3-small":   {0, 0},
      }
      for model, want := range cases {
          ctx, out := Lookup(model)
          if ctx == 0 && want.context != 0 {
              t.Errorf("Lookup(%q) context = 0, want %d", model, want.context)
          }
          if out == 0 && want.output != 0 {
              t.Errorf("Lookup(%q) maxOutput = 0, want %d", model, want.output)
          }
      }
  }
  ```

  Note: exact values will be set in Step 3; adjust the expected values to match whatever conservative public values you choose.

- [ ] **Step 2: Run tests to verify they fail**

  Run:
  ```bash
  go test ./internal/llm/catalog/... -run TestLookup_HostedModels -v
  ```
  Expected: FAIL — context/output values are zero for unknown models.

- [ ] **Step 3: Add model entries to `builtin`**

  In `internal/llm/catalog/catalog.go`, replace/extend the `builtin` map. Keep existing local entries and add (values are conservative; cite sources in comments):

  ```go
  var builtin = map[string]entry{
      // Local / Ollama-style
      "qwen2.5-coder:7b":      {contextWindow: 32768, maxOutput: 8192},
      "qwen2.5-coder:14b":     {contextWindow: 32768, maxOutput: 8192},
      "qwen2.5-coder:32b":     {contextWindow: 32768, maxOutput: 8192},
      "qwen2.5:7b":            {contextWindow: 32768, maxOutput: 8192},
      "qwen2.5:14b":           {contextWindow: 32768, maxOutput: 8192},
      "llama3.1:8b":           {contextWindow: 128000, maxOutput: 4096},
      "llama3.1:70b":          {contextWindow: 128000, maxOutput: 4096},
      "deepseek-coder-v2:16b": {contextWindow: 128000, maxOutput: 8192},
      "codestral:22b":         {contextWindow: 32000, maxOutput: 8192},
      "mistral:7b":            {contextWindow: 32000, maxOutput: 8192},
      "phi3:14b":              {contextWindow: 128000, maxOutput: 4096},

      // OpenAI
      "gpt-4o":      {contextWindow: 128000, maxOutput: 16384},
      "gpt-4o-mini": {contextWindow: 128000, maxOutput: 16384},
      "o3-mini":     {contextWindow: 200000, maxOutput: 100000},

      // Anthropic
      "claude-sonnet-4-20250514": {contextWindow: 200000, maxOutput: 8192},
      "claude-opus-4-20250514":   {contextWindow: 200000, maxOutput: 8192},
      "claude-haiku-4-20250514":  {contextWindow: 200000, maxOutput: 8192},

      // Google Gemini (OpenAI-compatible ids)
      "gemini-2.5-pro":   {contextWindow: 1000000, maxOutput: 8192},
      "gemini-2.5-flash": {contextWindow: 1000000, maxOutput: 8192},

      // Groq
      "llama-3.3-70b-versatile":       {contextWindow: 128000, maxOutput: 8192},
      "qwen-2.5-32b":                  {contextWindow: 32768, maxOutput: 8192},
      "deepseek-r1-distill-llama-70b": {contextWindow: 128000, maxOutput: 8192},

      // OpenRouter / shared hosted ids
      "anthropic/claude-sonnet-4":         {contextWindow: 200000, maxOutput: 8192},
      "google/gemini-2.5-pro":             {contextWindow: 1000000, maxOutput: 8192},
      "meta-llama/llama-3.3-70b-instruct": {contextWindow: 128000, maxOutput: 8192},

      // Together AI
      "meta-llama/Llama-3.3-70B-Instruct-Turbo": {contextWindow: 131072, maxOutput: 8192},
      "Qwen/Qwen2.5-Coder-32B-Instruct":         {contextWindow: 32768, maxOutput: 8192},

      // Fireworks
      "accounts/fireworks/models/llama-v3p3-70b-instruct": {contextWindow: 131072, maxOutput: 8192},

      // DeepSeek
      "deepseek-chat":     {contextWindow: 64000, maxOutput: 8192},
      "deepseek-reasoner": {contextWindow: 64000, maxOutput: 8192},

      // Perplexity
      "sonar":           {contextWindow: 128000, maxOutput: 8192},
      "sonar-pro":       {contextWindow: 200000, maxOutput: 8192},
      "sonar-reasoning": {contextWindow: 128000, maxOutput: 8192},

      // Mistral
      "mistral-large-latest": {contextWindow: 131000, maxOutput: 8192},
      "codestral-latest":     {contextWindow: 256000, maxOutput: 8192},

      // Cohere
      "command-r-plus": {contextWindow: 128000, maxOutput: 8192},
      "command-r":      {contextWindow: 128000, maxOutput: 8192},

      // xAI
      "grok-3":      {contextWindow: 131072, maxOutput: 8192},
      "grok-3-mini": {contextWindow: 131072, maxOutput: 8192},

      // Embeddings
      "text-embedding-3-small": {contextWindow: 8192, maxOutput: 0},
      "text-embedding-3-large": {contextWindow: 8192, maxOutput: 0},
      "text-embedding-004":     {contextWindow: 2048, maxOutput: 0},
  }
  ```

  Add a file-level comment that these are conservative defaults and overrideable via `[models.presets.<id>]`.

- [ ] **Step 4: Run tests to verify they pass**

  Run:
  ```bash
  go test ./internal/llm/catalog/... -v
  ```
  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/llm/catalog/catalog.go internal/llm/catalog/catalog_test.go
  git commit -m "feat(catalog): expand model metadata for local and hosted providers"
  ```

---

### Task 3: Update pricing table for hosted models

**Files:**
- Modify: `internal/llm/pricing/prices.go`
- Test: `internal/llm/pricing/pricing_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: new entries in the static pricing map; unchanged `Lookup` signature.

- [ ] **Step 1: Write failing test for hosted model pricing**

  In `internal/llm/pricing/pricing_test.go`, add:

  ```go
  func TestLookup_HostedModels(t *testing.T) {
      for _, model := range []string{"gpt-4o", "claude-sonnet-4-20250514", "gemini-2.5-pro", "deepseek-chat"} {
          p := Lookup(model)
          if p == nil {
              t.Errorf("expected pricing entry for %q", model)
              continue
          }
          if p.Input == 0 && p.Output == 0 {
              t.Errorf("%q has zero input and output pricing", model)
          }
      }
  }
  ```

- [ ] **Step 2: Run test to verify it fails**

  Run:
  ```bash
  go test ./internal/llm/pricing/... -run TestLookup_HostedModels -v
  ```
  Expected: FAIL.

- [ ] **Step 3: Add pricing entries**

  In `internal/llm/pricing/prices.go`, add entries for hosted models where reliable public per-token pricing exists. Use dollars per million tokens as the existing table does. Example (adjust to match existing field names/scale):

  ```go
  "gpt-4o":                   {Input: 2.50, Output: 10.00},
  "gpt-4o-mini":              {Input: 0.15, Output: 0.60},
  "o3-mini":                  {Input: 1.10, Output: 4.40},
  "claude-sonnet-4-20250514": {Input: 3.00, Output: 15.00},
  "claude-opus-4-20250514":   {Input: 15.00, Output: 75.00},
  "claude-haiku-4-20250514":  {Input: 0.25, Output: 1.25},
  "gemini-2.5-pro":           {Input: 1.25, Output: 10.00},
  "gemini-2.5-flash":         {Input: 0.30, Output: 2.50},
  "deepseek-chat":            {Input: 0.27, Output: 1.10},
  "deepseek-reasoner":        {Input: 0.55, Output: 2.19},
  "llama-3.3-70b-versatile":  {Input: 0.59, Output: 0.79},
  "mistral-large-latest":     {Input: 2.00, Output: 6.00},
  "codestral-latest":         {Input: 0.30, Output: 0.90},
  "grok-3":                   {Input: 3.00, Output: 15.00},
  "grok-3-mini":              {Input: 0.30, Output: 0.50},
  ```

  Leave provider-prefixed OpenRouter ids unpriced unless you have exact numbers; zero cost is acceptable.

- [ ] **Step 4: Run tests to verify they pass**

  Run:
  ```bash
  go test ./internal/llm/pricing/... -v
  ```
  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/llm/pricing/prices.go internal/llm/pricing/pricing_test.go
  git commit -m "feat(pricing): add pricing entries for hosted chat models"
  ```

---

### Task 4: Flip `remote_providers_allowed` default to `true`

**Files:**
- Modify: `internal/app/config/defaults.go`
- Test: `internal/app/config/config_test.go`, `internal/app/config/save_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.Default().Privacy.RemoteProvidersAllowed == true`.

- [ ] **Step 1: Update the default**

  In `internal/app/config/defaults.go`, change:
  ```go
  Privacy: PrivacyConfig{
      RemoteProvidersAllowed: true,
      RedactSecrets:          true,
      IncludeGitignoredFiles: false,
  },
  ```

- [ ] **Step 2: Update failing tests**

  In `internal/app/config/config_test.go`, update the test that asserts `RemoteProvidersAllowed` is `false` to expect `true`. Search for `remote_providers_allowed = false` assertions and invert them. Also check `save_test.go` line 59 ("remote_providers_allowed = true, want false") — this test likely needs to be removed or inverted.

  In `internal/llm/routing/router_test.go`, find tests that build a default `routing.Config` with `RemoteAllowed: false` and expect `ErrRemoteProviderBlocked`. Update them to use `RemoteAllowed: true` for the "allowed by default" case and add an explicit `RemoteAllowed: false` case that still blocks.

- [ ] **Step 3: Run all config/routing tests**

  Run:
  ```bash
  go test ./internal/app/config/... ./internal/llm/routing/... -v
  ```
  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/app/config/defaults.go internal/app/config/config_test.go internal/app/config/save_test.go internal/llm/routing/router_test.go
  git commit -m "feat(config): default remote_providers_allowed to true"
  ```

---

### Task 5: Connect wizard surfaces recommended providers first and key hints

**Files:**
- Modify: `internal/app/tui/connect/connect.go`
- Test: `internal/app/tui/connect/connect_test.go`

**Interfaces:**
- Consumes: `ProviderTemplate.Recommended` from Task 1.
- Produces: picker items sorted recommended-first; `KeyHint` shown as detail line.

- [ ] **Step 1: Write failing test for sort order and key hint**

  In `internal/app/tui/connect/connect_test.go`, add:

  ```go
  func TestConnect_RecommendedProvidersFirst(t *testing.T) {
      m := New(Opts{Cfg: config.Default()})
      if m.step != stepPickTemplate {
          t.Fatalf("expected stepPickTemplate, got %d", m.step)
      }
      items := m.picker.Items()
      if len(items) == 0 {
          t.Fatal("picker has no items")
      }
      sawNonRecommended := false
      for _, it := range items {
          tpl, ok := provider.Lookup(it.Value)
          if !ok {
              continue
          }
          if sawNonRecommended && tpl.Recommended {
              t.Error("recommended item appeared after non-recommended item")
          }
          if !tpl.Recommended {
              sawNonRecommended = true
          }
      }
  }
  ```

  Also add a test that verifies the picker detail includes the `KeyHint` for hosted providers (inspect `picker.Item.Detail`).

- [ ] **Step 2: Run test to verify it fails**

  Run:
  ```bash
  go test ./internal/app/tui/connect/... -run TestConnect_RecommendedProvidersFirst -v
  ```
  Expected: FAIL — sort order is not implemented.

- [ ] **Step 3: Sort picker items and show key hint**

  In `internal/app/tui/connect/connect.go`, modify `enterPickTemplate`:

  ```go
  func (m *Model) enterPickTemplate() {
      m.step = stepPickTemplate
      m.title = "Connect a provider"
      m.subtitle = ""
      m.footer = "[↑↓] move [↵] pick [Esc] cancel"
      m.err = ""
      all := provider.All()
      sort.Slice(all, func(i, j int) bool {
          if all[i].Recommended != all[j].Recommended {
              return all[i].Recommended && !all[j].Recommended
          }
          return all[i].Label < all[j].Label
      })
      items := make([]picker.Item, 0, len(all))
      for _, tpl := range all {
          detail := tpl.BaseURL
          if !tpl.Local && tpl.KeyHint != "" {
              detail = tpl.KeyHint
          }
          items = append(items, picker.Item{
              Label:  tpl.Label,
              Detail: detail,
              Badge:  badgeForTemplate(tpl),
              Value:  tpl.ID,
          })
      }
      p := picker.New(m.title, "pick a template", items)
      p.SetAllowCustom(true)
      m.picker = p
  }
  ```

  Add `import "sort"` if not already imported. Verify `picker.Model` exposes `Items()` for tests; if not, use a different observable (e.g., the rendered view or a test helper method).

- [ ] **Step 4: Run connect tests**

  Run:
  ```bash
  go test ./internal/app/tui/connect/... -v
  ```
  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/app/tui/connect/connect.go internal/app/tui/connect/connect_test.go
  git commit -m "feat(connect): surface recommended providers first and key hints"
  ```

---

### Task 6: Update onboarding to use the full catalog and write richer config

**Files:**
- Modify: `internal/app/onboarding.go`
- Test: `internal/app/onboarding_test.go`

**Interfaces:**
- Consumes: `provider.All()` and `connect.Model`.
- Produces: a project-local `.marshal/config.toml` with `[project]`, `[profile]`, `[providers.*]`, `[models.presets.onboarded_preset]`, `[agent_profiles.onboarded]`, `[agent]`, and optionally `[privacy]`.

- [ ] **Step 1: Update tests to expect full catalog and richer config output**

  In `internal/app/onboarding_test.go`, update the test that checks the hardcoded provider list (`NewOnboardingModel` providers slice). Change it to assert that the onboarding model delegates to `connect.Model` and that the saved config includes `[privacy] remote_providers_allowed = true` for remote templates.

  Add:
  ```go
  func TestOnboarding_WritesRemotePrivacyForRemoteProvider(t *testing.T) {
      // Simulate completing onboarding with an OpenAI template.
      // Assert the saved .marshal/config.toml contains:
      //   [privacy]
      //   remote_providers_allowed = true
  }
  ```

  If existing tests assert exact strings that will change, update them.

- [ ] **Step 2: Run tests to verify they fail**

  Run:
  ```bash
  go test ./internal/app/... -run TestOnboarding -v
  ```
  Expected: FAIL.

- [ ] **Step 3: Rewrite onboarding provider step to use connect wizard**

  In `internal/app/onboarding.go`:

  1. Remove the hardcoded `providers` slice from `OnboardingModel` and `NewOnboardingModel`.
  2. In `stateSelectProvider`, immediately transition to `stateProjectName` (we keep project name first) or directly to `stateConnect`. The simplest path:
     - Keep `stateProjectName` first.
     - After project name is entered, create the connect model with `connect.Opts{Cfg: config.Default(), CfgPath: ...}` as it already does.
  3. On `connect.DoneMsg`, call a refactored `saveConfig` that writes:
     - `[project]` name
     - `[profile] default = "onboarded"`
     - `[providers.<name>]` from `connect.DoneMsg.ProviderCfg`
     - `[models.presets.onboarded_preset]` with provider, model, and `local_only` derived from the selected template
     - `[agent_profiles.onboarded]` mapping `routing.AllRoles` to `onboarded_preset`
     - `[agent] max_tool_iterations = 32`
     - `[privacy] remote_providers_allowed = true` if the selected template was not `Local`

  Helper to determine `local_only` from the selected template:

  ```go
  func localOnlyForTemplate(providerID string) bool {
      tpl, ok := provider.Lookup(providerID)
      return ok && tpl.Local
  }
  ```

  Note: the provider id written by onboarding may be the renamed provider (e.g. `openai-2`), so look it up by the original template id passed in `connect.DoneMsg.Provider` rather than the renamed `providerName` if necessary. Use `connect.DoneMsg.Provider` (the original template id) for `local_only` and `privacy` decisions.

- [ ] **Step 4: Run onboarding tests**

  Run:
  ```bash
  go test ./internal/app/... -run TestOnboarding -v
  ```
  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/app/onboarding.go internal/app/onboarding_test.go
  git commit -m "feat(onboarding): use full provider catalog and write richer default config"
  ```

---

### Task 7: Update documentation

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/09-configuration-examples.md`
- Modify: `docs/04-tooling-and-shell-safety.md`

**Interfaces:**
- Consumes: default and catalog changes from previous tasks.
- Produces: updated docs.

- [ ] **Step 1: Update `CLAUDE.md`**

  Find and replace:
  - "local-first" → "local-friendly" (context: project description, not every occurrence).
  - The bullet: "**Local-first**: default config has `remote_providers_allowed = false`. Don't assume a hosted model." → "**Local-friendly**: default config has `remote_providers_allowed = true` but no built-in providers; onboarding asks the user to choose a setup."

- [ ] **Step 2: Update `docs/09-configuration-examples.md`**

  - Update any example showing `remote_providers_allowed = false` to `true` (or note it is now the default).
  - Add example provider blocks for Anthropic, Google, Groq, DeepSeek, and OpenRouter under `[providers.*]`.

- [ ] **Step 3: Update `docs/04-tooling-and-shell-safety.md`**

  - If it mentions `remote_providers_allowed = false`, change it to `true` and clarify that the gate is still enforced when explicitly set to `false`.

- [ ] **Step 4: No tests; verify docs build**

  No Go tests. Optionally run:
  ```bash
  go build ./cmd/marshal
  ```
  to ensure no references broke.

- [ ] **Step 5: Commit**

  ```bash
  git add CLAUDE.md docs/09-configuration-examples.md docs/04-tooling-and-shell-safety.md
  git commit -m "docs: reframe local-first to local-friendly and update defaults"
  ```

---

### Task 8: Full test sweep and final fixes

**Files:**
- All modified above.

- [ ] **Step 1: Run full test suite**

  ```bash
  go test ./...
  ```

- [ ] **Step 2: Fix any remaining failures**

  Common remaining issues:
  - `config_test.go` still expecting `RemoteProvidersAllowed = false`.
  - `router_test.go` default-config remote gate tests.
  - `tui/settings` tests that map `privacy.remote_providers` to `privacy.remote_providers_allowed` may need no change, but verify.
  - Any test that asserts exact provider count in `provider.All()`.

- [ ] **Step 3: Run formatting and vet**

  ```bash
  gofmt -w .
  go vet ./...
  ```

- [ ] **Step 4: Final commit (or amend last doc commit)**

  ```bash
  git add -A
  git commit -m "chore(provider-catalog): final test fixes and formatting"
  ```

---

## Self-review checklist

1. **Spec coverage:**
   - Expand provider templates → Task 1.
   - Expand model catalog → Task 2.
   - Pricing updates → Task 3.
   - Flip privacy default → Task 4.
   - Connect wizard UX → Task 5.
   - Onboarding writes richer config → Task 6.
   - Documentation reframe → Task 7.
   - Remote-catalog seam → addressed in Task 1/2 by keeping static maps side-effect free.

2. **Placeholder scan:** No TBD/TODO/fill-in-details steps.
3. **Type consistency:** `ProviderTemplate.Recommended bool` used in Task 1 and consumed in Task 5. `catalog.Lookup` signature unchanged. `connect.DoneMsg` fields unchanged.

## Success criteria

- `go test ./...` passes.
- `provider.All()` returns at least 20 templates.
- `catalog.Lookup("gpt-4o")` returns non-zero values.
- `config.Default().Privacy.RemoteProvidersAllowed == true`.
- Onboarding with a remote provider writes `remote_providers_allowed = true`.
- Onboarding with Ollama writes `local_only = true`.
- `CLAUDE.md` no longer says "local-first" with the old default.
