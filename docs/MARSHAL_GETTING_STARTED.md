# Marshal — Getting Started

Quick start guide for running Marshal at each milestone.

---

## Prerequisites

- Go 1.22+ installed
- Git repository initialized
- (Optional) Ollama for local models
- (Optional) Fireworks account for cloud models

---

## Setup

### 1. Install Dependencies

```bash
cd /Users/alecpullen/projects/marshal
go mod tidy
```

### 2. Choose Your Backend

**Option A: Ollama (Local, Free, Recommended for Testing)**

Uses small models on your MacBook (~8.5GB RAM).

**Option B: Fireworks (Cloud, Higher Quality)**

Uses Devstral Small 2 + DeepSeek-R1-0528 on Fireworks infrastructure.

---

## Ollama Setup (Local)

### Step 1: Install Ollama

```bash
brew install ollama
```

### Step 2: Run Setup Script

```bash
./scripts/setup-ollama.sh
```

This will:
- Start the Ollama server
- Pull `qwen2.5-coder:7b` (executor, ~4.5GB RAM)
- Pull `deepseek-r1:7b` (critic with <thinking> tags, ~4GB RAM)
- Create `ollama.toml` config file

### Step 3: Test the Models

```bash
./scripts/test-ollama.sh
```

### Step 4: Run Milestones

```bash
# Milestone 1 verification
export FIREWORKS_API_KEY="ollama"
go run cmd/marshal/main.go -config ollama.toml

# Milestone 2 loop
./scripts/run-m2-ollama.sh

# Milestone 2 with custom task
./scripts/run-m2-ollama.sh "Write a function to reverse a string"
```

---

## Fireworks Setup (Cloud)

### Step 1: Get API Key

Sign up at [fireworks.ai](https://fireworks.ai) and get an API key.

### Step 2: Set Environment Variable

```bash
export FIREWORKS_API_KEY="your_key_here"
```

Add to your shell profile for persistence:

```bash
echo 'export FIREWORKS_API_KEY="your_key_here"' >> ~/.zshrc
```

### Step 3: Create Deployments

Install the Fireworks CLI:

```bash
brew install fireworks-ai/tap/firectl
```

Create the executor deployment:

```bash
firectl deployment create \
  accounts/fireworks/models/devstral-small-2-24b-instruct-2512 \
  --accelerator-type NVIDIA_A100_80GB \
  --accelerator-count 1 \
  --scale-to-zero-window 5m
```

Create the critic deployment:

```bash
firectl deployment create \
  accounts/fireworks/models/deepseek-r1-0528 \
  --accelerator-type NVIDIA_H200_141GB \
  --accelerator-count 8 \
  --scale-to-zero-window 5m
```

Verify deployments are ready:

```bash
firectl deployment list
```

### Step 4: Use marshal.toml

The default `marshal.toml` is pre-configured for Fireworks.

### Step 5: Run Milestones

```bash
# Milestone 1 verification
go run cmd/marshal/main.go

# Milestone 2 loop with Fireworks
go run cmd/marshal/main.go -config marshal.toml
```

---

## Running Each Milestone

### Milestone 1: Backend Verification

Tests that the executor and critic backends are working.

**Ollama:**
```bash
export FIREWORKS_API_KEY="ollama"
go run cmd/marshal/main.go -config ollama.toml
```

**Fireworks:**
```bash
go run cmd/marshal/main.go
```

**Expected output:**
```
--- executor ---
response: [3 words from executor]
tokens: N prompt + M completion

--- critic ---
response: [3 words from critic]
tokens: N prompt + M completion

Milestone 1 complete.
```

---

### Milestone 2: Loop Engine

Runs the full executor → critic → feedback loop.

**Ollama (using script):**
```bash
./scripts/run-m2-ollama.sh

# Or with custom task:
./scripts/run-m2-ollama.sh "Add a function to calculate fibonacci numbers"
```

**Ollama (manual):**
```bash
export FIREWORKS_API_KEY="ollama"
go run cmd/marshal/main.go -config ollama.toml
```

**Fireworks:**
```bash
go run cmd/marshal/main.go
```

**Expected behavior:**
1. Creates isolation branch
2. Executor generates code
3. Diff extracted
4. Critic reviews (shows <thinking> tags with Ollama/DeepSeek-R1)
5. Verdict parsed (PASS/FAIL)
6. If FAIL → feedback injected, loop repeats
7. If PASS → success
8. If max rounds → exhausted

**Output:**
```
=== Starting Marshal Loop ===
Task: Write a Go function that calculates the nth Fibonacci number

[mock git] Create isolation branch: marshal-session-1712534400-1234abcd
...

=== Result ===
Status: SUCCESS
Verdict: PASS
Summary: Fibonacci function implemented correctly
Rounds: 1
```

---

### Milestone 3: Git Layer (Coming Soon)

Replaces mock git with real git operations.

**To run (when implemented):**
```bash
go run cmd/marshal/main.go -config ollama.toml
```

**What changes:**
- Real branch creation (`git checkout -b`)
- Real diff extraction (`git diff HEAD`)
- Commits per round (`git add -A && git commit`)
- Merge on PASS (`git merge`)
- Cleanup on failure (`git reset --hard && git branch -D`)

---

## Configuration Files

### ollama.toml (Local)

Created by `./scripts/setup-ollama.sh`:

```toml
[executor]
model       = "qwen2.5-coder:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.2
max_tokens  = 4096

[critic]
model       = "deepseek-r1:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.6
max_tokens  = 8192
json_output = false

[loop]
max_rounds        = 3
auto_commit       = false
auto_revert       = true
branch_isolation  = true
compact_after     = 2
```

### marshal.toml (Fireworks)

```toml
[executor]
model       = "accounts/fireworks/models/devstral-small-2-24b-instruct-2512"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.2
max_tokens  = 4096

[critic]
model       = "accounts/fireworks/models/deepseek-r1-0528"
base_url    = "https://api.fireworks.ai/inference/v1"
api_key     = "${FIREWORKS_API_KEY}"
temperature = 0.6
max_tokens  = 8192
json_output = true

[loop]
max_rounds        = 3
auto_commit       = true
auto_revert       = true
branch_isolation  = true
compact_after     = 2
```

---

## Scripts Reference

### `./scripts/setup-ollama.sh`
- Installs Ollama if needed
- Starts Ollama server
- Pulls required models
- Creates `ollama.toml`

### `./scripts/test-ollama.sh`
- Tests executor model (qwen2.5-coder)
- Tests critic model (deepseek-r1)
- Shows <thinking> tag output

### `./scripts/run-m2-ollama.sh [task]`
- Full M2 loop with Ollama
- Optional custom task argument
- Colorized output

---

## Troubleshooting

### Ollama Issues

**Connection refused:**
```bash
ollama serve  # In another terminal
```

**Model not found:**
```bash
ollama pull qwen2.5-coder:7b
ollama pull deepseek-r1:7b
```

**Slow first response:**
- Normal for first inference after model load
- Wait 10-30 seconds

### Fireworks Issues

**401 Unauthorized:**
```bash
export FIREWORKS_API_KEY="your_key"
```

**404 Model not found:**
```bash
firectl deployment list  # Check status
# Deployments must be in READY state
```

**503 Service Unavailable:**
- Deployment scaling from zero
- Wait 30-60 seconds and retry

### General Issues

**Config file not found:**
```bash
# Use explicit path
go run cmd/marshal/main.go -config ./ollama.toml
```

**Test failures:**
```bash
go test ./... -v  # Verbose output
```

---

## Development Commands

```bash
# Build
go build ./...

# Test all
go test ./...

# Test specific package
go test ./internal/loop/...

# Format code
gofmt -w .

# Run with custom config
go run cmd/marshal/main.go -config my-config.toml
```

---

## Milestones Overview

| Milestone | Status | What It Does |
|-----------|--------|--------------|
| 1 | ✅ Complete | Backend interface, config loading, basic verification |
| 2 | ✅ Complete | Loop engine with executor/critic/feedback cycle |
| 3 | 📋 Planned | Real git operations (branch, commit, merge) |
| 4 | 📋 Planned | Session store (SQLite) and Cobra CLI |
| 5-6 | 📋 Planned | Bubble Tea TUI |
| 7-8 | 📋 Planned | Real compaction and think-block panel |
| 9-13 | 📋 Planned | Multi-agent pipeline |

---

## Next Steps

1. **Test M1 and M2** with Ollama (recommended) or Fireworks
2. **Review M3 plan**: `.claude/plans/m3-git-layer.md`
3. **Implement M3** when ready
4. **Check architecture**: See `CLAUDE.md` for detailed design

For implementation details, see:
- `CLAUDE.md` — Architecture and design decisions
- `.claude/plans/m2-loop-engine.md` — M2 implementation details
- `.claude/plans/m3-git-layer.md` — M3 implementation plan
