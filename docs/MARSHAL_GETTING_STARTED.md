# Marshal — Getting Started

**Project** Marshal  
**Date** March 2026  
**Prerequisite** Marshal Design Specification v0.8  


> **How to use this guide**
>
> Follow sections in order. Do not skip ahead to the implementation before completing the environment setup — each section builds directly on the previous one.


---


# 1. Environment setup


## 1.1 Install Go


Download the macOS ARM64 package installer from [go.dev/dl](https://go.dev/dl). Use the official installer, not Homebrew, to avoid PATH issues.


```bash
# After running the installer, open a new terminal and verify
go version
# Expected: go version go1.22.x darwin/arm64 or later

go env GOPATH
# Expected: /Users/<you>/go
```


## 1.2 GoLand setup


Open GoLand and install the Go plugin if prompted. Key settings to enable:


- **Go → Go Modules**: enable Go modules integration
- **Editor → Code Style → Go**: enable format on save with `gofmt`
- **Tools → File Watchers**: add a `goimports` watcher for auto-import management


> **gofmt is non-negotiable**
>
> Go code is formatted by the compiler — there is no style debate. If your code looks different from what you typed, that is gofmt doing its job. Do not fight it.


## 1.3 Set up Fireworks AI


Marshal uses Fireworks AI for inference. Sign up at [fireworks.ai](https://fireworks.ai) and get an API key — you start with $1 of free credits.


```bash
# Add to ~/.zshrc
export FIREWORKS_API_KEY="your_fireworks_api_key"

# Verify the API key works and models are listed
curl https://api.fireworks.ai/inference/v1/models \
  -H "Authorization: Bearer $FIREWORKS_API_KEY"

# Test the executor — Devstral Small 2 (2512), on-demand deployment
# Note: the deployment must be created first in the Fireworks dashboard
# before this endpoint is active (see section 1.4)
curl https://api.fireworks.ai/inference/v1/chat/completions \
  -H "Authorization: Bearer $FIREWORKS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "accounts/fireworks/models/devstral-small-2-24b-instruct-2512",
    "messages": [{"role":"user","content":"Reply in one word."}],
    "max_tokens": 5
  }'

# Test the critic — DeepSeek-R1-0528, on-demand deployment
# R1-0528 supports system prompts (the original R1 did not)
curl https://api.fireworks.ai/inference/v1/chat/completions \
  -H "Authorization: Bearer $FIREWORKS_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "accounts/fireworks/models/deepseek-r1-0528",
    "messages": [
      {"role":"system","content":"You are a concise assistant."},
      {"role":"user","content":"Reply in one word."}
    ],
    "max_tokens": 20
  }'
```

Both should return HTTP 200 with a `choices` array. 401 = wrong API key. 404 = wrong model ID or deployment not yet created. 503 = deployment is scaling up from zero — wait 30–60s and retry.


## 1.4 Create Fireworks on-demand deployments (optional)

Both models require on-demand deployments — they are not available on Fireworks serverless. Create them in the Fireworks dashboard before writing any Go.

**Executor — Devstral Small 2 (2512)**

```bash
# Install firectl CLI
brew install fireworks-ai/tap/firectl   # macOS
# or: pip install firectl

firectl deployment create \
  accounts/fireworks/models/devstral-small-2-24b-instruct-2512 \
  --accelerator-type NVIDIA_A100_80GB \
  --accelerator-count 1 \
  --scale-to-zero-window 5m
```

Cost: $2.90/hr while active, $0 when idle (scales to zero after 5 minutes of no requests).

**Critic — DeepSeek-R1-0528**

```bash
firectl deployment create \
  accounts/fireworks/models/deepseek-r1-0528 \
  --accelerator-type NVIDIA_H200_141GB \
  --accelerator-count 8 \
  --scale-to-zero-window 5m
```

Cost: $6.00/hr × 8 = $48/hr while active, $0 when idle. For a typical 10-minute coding session this is about $8 in critic GPU time. The 5-minute scale-to-zero window means it spins down promptly after your last session.

> ⚠️ **Verify deployment state before running Milestone 1**
>
> After creating a deployment, run `firectl deployment list` and confirm both show `READY` state. The first scale-up from zero takes 30–60 seconds — this is normal.

> 💡 **Alternative: Local Ollama (free, no deployments)**
>
> Skip Fireworks entirely and run locally with Ollama. See section 1.5 below for setup.

## 1.5 Local Ollama setup (alternative to Fireworks)

For local development and testing without cloud costs, use Ollama with small models that fit on a 16GB MacBook.

### Install Ollama

```bash
brew install ollama
ollama serve  # starts the API server on localhost:11434
```

### Quick setup

Run the provided setup script:

```bash
./scripts/setup-ollama.sh
```

This pulls the recommended models and creates `ollama.toml`:

| Model | Role | RAM Usage | Output Style |
|-------|------|-----------|--------------|
| `qwen2.5-coder:7b` | Executor | ~4.5GB | Direct code generation |
| `deepseek-r1:7b` | Critic | ~4GB |  reasoning blocks + JSON verdict |

Total ~8.5GB RAM usage — well within 16GB limit.

### Manual model pull (if not using the script)

```bash
ollama pull qwen2.5-coder:7b
ollama pull deepseek-r1:7b
```

### Testing the local setup

```bash
# Test both models and see  tags in action
./scripts/test-ollama.sh

# Or run the milestone 1 verification with local models
export FIREWORKS_API_KEY=ollama  # dummy value for validation
./scripts/setup-ollama.sh  # creates ollama.toml
go run cmd/marshal/main.go
```

### Why this model split?

- **Executor (qwen2.5-coder:7b)**: Fast code generation, good quality, deterministic output at low temperature.
- **Critic (deepseek-r1:7b)**: Reasoning model that outputs chain-of-thought in  tags before the structured verdict. This matches the expected M2 critic format where reasoning is extracted separately from the final PASS/FAIL judgment.

The `deepseek-r1` model is key for testing M2 — it naturally produces the  reasoning blocks that Marshal will parse and display in the TUI's "think panel" (Milestone 5–6).

## 1.6 Claim the GitHub repository


1. Create a new **private** repository named `marshal` on github.com
2. Do not initialise with a README, licence, or .gitignore
3. Note your repo URL: `github.com/<your-username>/marshal`

Do this before running `go mod init` — your module path should match the repo URL.


---


# 2. Project setup


```bash
mkdir ~/projects/marshal && cd ~/projects/marshal
git init
go mod init github.com/<your-username>/marshal

# Directory structure from the spec
mkdir -p cmd/marshal
mkdir -p internal/backend
mkdir -p internal/config
mkdir -p internal/loop
mkdir -p internal/git
mkdir -p internal/store
mkdir -p internal/tui
mkdir -p .marshal/skills

# Minimal entry point
cat > cmd/marshal/main.go << 'EOF'
package main

import "fmt"

func main() {
    fmt.Println("marshal")
}
EOF

go run cmd/marshal/main.go
# Expected: marshal
```


## 2.1 Add dependencies


```bash
go get github.com/BurntSushi/toml
go get github.com/mattn/go-sqlite3   # requires Xcode CLI tools
go get github.com/spf13/cobra

# If go-sqlite3 fails: xcode-select --install, then retry
```


## 2.2 Create marshal.toml

Choose **either** Fireworks (cloud) or Ollama (local) configuration:

### Option A: Fireworks (cloud, higher quality)

```toml
[executor]
# Devstral Small 2 (2512) — 68.0% SWE-Bench Verified, dense 24B
# Fireworks on-demand: A100 × 1, $2.90/hr, scale-to-zero 5m
model       = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.2
max_tokens  = 4096

[critic]
# DeepSeek-R1-0528 — full 671B MoE reasoning model
# Fireworks on-demand: H200 × 8, $48/hr active, scale-to-zero 5m
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6   # R1 series: must be 0.5-0.7
max_tokens  = 8192  # headroom for think-blocks before the verdict
json_output = true

[loop]
max_rounds        = 3
auto_commit       = true
auto_revert       = true
branch_isolation  = true
compact_after     = 2
token_budget_warn = 0.80

[session]
db_path = ".marshal/sessions.db"

[retry]
max_attempts       = 3
initial_backoff_ms = 1000
backoff_factor     = 2.0
retry_status_codes = [429, 502, 503]
```

### Option B: Ollama (local, free, faster iteration)

Use the generated `ollama.toml` from `./scripts/setup-ollama.sh`:

```toml
[executor]
# Qwen 2.5 Coder 7B - fast, good code quality, ~4.5GB RAM
model       = "qwen2.5-coder:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"  # required but ignored by Ollama
temperature = 0.2       # low temp for consistent code
max_tokens  = 4096

[critic]
# DeepSeek-R1 7B - reasoning model with  tags, ~4GB RAM
# The model outputs reasoning in  tags before the verdict
model       = "deepseek-r1:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.6       # higher temp for reasoning diversity
max_tokens  = 8192      # headroom for think blocks + JSON verdict
json_output = false     # R1 doesn't reliably follow JSON mode

[loop]
max_rounds        = 3
auto_commit       = false   # manual review for local testing
auto_revert       = true
branch_isolation  = true
compact_after     = 2
```


## 2.3 Create .gitignore


```gitignore
.marshal/*.db
marshal
*.exe
.DS_Store
.env
```


## 2.4 Initial commit


```bash
git add .
git commit -m "init: project scaffold"
git remote add origin https://github.com/<your-username>/marshal.git
git branch -M main
git push -u origin main
```


---


# 3. Milestone 1 — Scaffold and provider layer


By the end of this section both Fireworks on-demand deployments will be responding through the Backend abstraction. Do not move to Milestone 2 until the verification script at the end prints success.


> **Build order**
>
> Write the three files in the order shown. Each depends only on what came before it. Resist the urge to write the loop engine or TUI — those are later milestones.


## 3.1 internal/backend/backend.go


```go
package backend

import "context"

// Message is a single turn in a conversation.
type Message struct {
    Role    string // "system" | "user" | "assistant"
    Content string
}

// Response is a completed model call.
type Response struct {
    Content          string
    PromptTokens     int
    CompletionTokens int
    CacheHit         bool
    CachedTokens     int
}

// Backend is the interface all model providers must implement.
type Backend interface {
    Complete(ctx context.Context, model string, messages []Message) (Response, error)
    Name() string
}
```


## 3.2 internal/config/config.go


```go
package config

import (
    "fmt"
    "os"
    "github.com/BurntSushi/toml"
)

type AgentConfig struct {
    Model       string  `toml:"model"`
    BaseURL     string  `toml:"base_url"`
    APIKey      string  `toml:"api_key"`
    Temperature float64 `toml:"temperature"`
    MaxTokens   int     `toml:"max_tokens"`
    JSONOutput  bool    `toml:"json_output"`
}

type LoopConfig struct {
    MaxRounds       int     `toml:"max_rounds"`
    AutoCommit      bool    `toml:"auto_commit"`
    AutoRevert      bool    `toml:"auto_revert"`
    BranchIsolation bool    `toml:"branch_isolation"`
    CompactAfter    int     `toml:"compact_after"`
    TokenBudgetWarn float64 `toml:"token_budget_warn"`
}

type Config struct {
    Executor AgentConfig `toml:"executor"`
    Critic   AgentConfig `toml:"critic"`
    Loop     LoopConfig  `toml:"loop"`
}

func Load(path string) (*Config, error) {
    raw, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("reading %s: %w", path, err)
    }
    expanded := os.Expand(string(raw), func(key string) string {
        val := os.Getenv(key)
        if val == "" {
            fmt.Printf("warning: $%s is not set\n", key)
        }
        return val
    })
    var cfg Config
    if _, err := toml.Decode(expanded, &cfg); err != nil {
        return nil, fmt.Errorf("parsing %s: %w", path, err)
    }
    return &cfg, nil
}

func (c *Config) Validate() error {
    if c.Executor.Model == ""   { return fmt.Errorf("executor.model is required") }
    if c.Executor.BaseURL == "" { return fmt.Errorf("executor.base_url is required") }
    if c.Executor.APIKey == ""  { return fmt.Errorf("executor.api_key / $FIREWORKS_API_KEY not set") }
    if c.Critic.Model == ""     { return fmt.Errorf("critic.model is required") }
    if c.Critic.BaseURL == ""   { return fmt.Errorf("critic.base_url is required") }
    if c.Critic.APIKey == ""    { return fmt.Errorf("critic.api_key / $FIREWORKS_API_KEY not set") }
    return nil
}
```


## 3.3 internal/backend/openai_compat.go


```go
package backend

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
)

type OpenAICompatibleBackend struct {
    name      string
    baseURL   string
    apiKey    string
    sessionID string // x-session-affinity value for Fireworks KV cache routing
    client    *http.Client
}

func NewOpenAICompatible(name, baseURL, apiKey string) *OpenAICompatibleBackend {
    return &OpenAICompatibleBackend{
        name: name, baseURL: baseURL, apiKey: apiKey,
        client: &http.Client{},
    }
}

func (b *OpenAICompatibleBackend) WithSession(id string) *OpenAICompatibleBackend {
    b.sessionID = id
    return b
}

func (b *OpenAICompatibleBackend) Name() string { return b.name }

func (b *OpenAICompatibleBackend) Complete(
    ctx context.Context, model string, messages []Message,
) (Response, error) {
    reqBody, err := json.Marshal(struct {
        Model     string    `json:"model"`
        Messages  []Message `json:"messages"`
        MaxTokens int       `json:"max_tokens"`
    }{model, messages, 1024})
    if err != nil {
        return Response{}, fmt.Errorf("marshal request: %w", err)
    }
    req, err := http.NewRequestWithContext(
        ctx, http.MethodPost,
        b.baseURL+"/chat/completions",
        bytes.NewReader(reqBody),
    )
    if err != nil {
        return Response{}, fmt.Errorf("build request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+b.apiKey)
    // Session affinity routes all rounds of a session to the same Fireworks
    // replica so the system-prompt KV cache is reused (50% cached-token pricing).
    if b.sessionID != "" {
        req.Header.Set("x-session-affinity", b.sessionID)
    }

    resp, err := b.client.Do(req)
    if err != nil {
        return Response{}, fmt.Errorf("do request: %w", err)
    }
    defer resp.Body.Close()
    raw, _ := io.ReadAll(resp.Body)

    if resp.StatusCode != http.StatusOK {
        return Response{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
    }

    var result struct {
        Choices []struct {
            Message struct{ Content string `json:"content"` } `json:"message"`
        } `json:"choices"`
        Usage struct {
            PromptTokens     int `json:"prompt_tokens"`
            CompletionTokens int `json:"completion_tokens"`
        } `json:"usage"`
    }
    if err := json.Unmarshal(raw, &result); err != nil {
        return Response{}, fmt.Errorf("decode response: %w", err)
    }
    if len(result.Choices) == 0 {
        return Response{}, fmt.Errorf("no choices in response")
    }
    return Response{
        Content:          result.Choices[0].Message.Content,
        PromptTokens:     result.Usage.PromptTokens,
        CompletionTokens: result.Usage.CompletionTokens,
    }, nil
}
```


## 3.4 Verify Milestone 1


Replace `cmd/marshal/main.go` with this verification script:


```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/<your-username>/marshal/internal/backend"
    "github.com/<your-username>/marshal/internal/config"
)

func main() {
    cfg, err := config.Load("marshal.toml")
    if err != nil { log.Fatal("config:", err) }
    if err := cfg.Validate(); err != nil { log.Fatal("validate:", err) }

    executor := backend.NewOpenAICompatible("executor", cfg.Executor.BaseURL, cfg.Executor.APIKey)
    critic   := backend.NewOpenAICompatible("critic",   cfg.Critic.BaseURL,   cfg.Critic.APIKey)
    ctx      := context.Background()
    msgs     := []backend.Message{{Role: "user", Content: "Reply in exactly three words."}}

    // executor = Devstral Small 2 (2512) on A100 × 1
    fmt.Println("--- executor ---")
    exResp, err := executor.Complete(ctx, cfg.Executor.Model, msgs)
    if err != nil { log.Fatal("executor:", err) }
    fmt.Println("response:", exResp.Content)
    fmt.Printf("tokens:   %d prompt + %d completion\n", exResp.PromptTokens, exResp.CompletionTokens)

    fmt.Println("\n--- critic ---")
    crResp, err := critic.Complete(ctx, cfg.Critic.Model, msgs)
    if err != nil { log.Fatal("critic:", err) }
    fmt.Println("response:", crResp.Content)
    fmt.Printf("tokens:   %d prompt + %d completion\n", crResp.PromptTokens, crResp.CompletionTokens)

    fmt.Println("\nMilestone 1 complete.")
}
```


```bash
go run cmd/marshal/main.go
```


> ⚠️ **Common failures (Fireworks)**
>
> 401 = `$FIREWORKS_API_KEY` not exported in this shell — run `source ~/.zshrc`. 404 = model ID wrong or deployment not yet created — run `firectl deployment list` to confirm both deployments are in `READY` state. 503 = deployment scaling up from zero — wait 30–60s and retry; the `[retry]` config handles this automatically once the loop engine is built.
>
> ⚠️ **Common failures (Ollama)**
>
> Connection refused = Ollama server not running — run `ollama serve` in another terminal. 404 = model not pulled — run `ollama pull <model-name>`. Empty response = model is still loading — wait 10–30s for first inference on slower machines.


## 3.5 Commit


```bash
git add .
git commit -m "feat: milestone 1 — backend interface and OpenAI-compatible client"
git push
```


---


# 4. What comes next


| Milestone | What you build | New Go concepts |
| --- | --- | --- |
| 2 — Loop engine | `internal/loop/loop.go`, `executor.go`, `critic.go`. Round management, feedback injection, JSON verdict parsing, think-block stripping, security prompts, skill loading. | Structs with methods. JSON unmarshaling into nested types. String parsing. |
| 3 — Git layer | `internal/git/git.go`. Branch creation, diff extraction, commit, hard reset. | More `os/exec` patterns. Error wrapping. Subprocess state management. |
| 4 — Session store + CLI | `internal/store/store.go` (SQLite). Cobra CLI. `marshal run`, `marshal sessions`. `--no-tui --json`. | `database/sql` query patterns. Cobra command structure. `os.Exit` with documented codes. |
| 5 — Bubble Tea TUI | `internal/tui/`. Loop view, session browser, diff viewer. Bubble Tea Model/Update/View. | Bubble Tea architecture. Lip Gloss styling. Goroutine → channel → `tea.Msg`. |


> **The rule**
>
> Do not start Milestone 2 until Milestone 1's verification prints "Milestone 1 complete." Do not start the TUI until you have a working CLI with correct output. The milestones are ordered for a reason — the temptation to jump to the TUI is real and it will slow you down.
