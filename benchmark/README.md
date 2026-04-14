# Marshal Benchmarks

Benchmark suite for measuring Marshal's coding performance against standard benchmarks.

## Exercism Benchmark

The Exercism benchmark measures edit success rate on programming exercises from [Exercism](https://exercism.org/).

### Running the Benchmark

```bash
# Run all Go exercises (easy and medium)
go run ./cmd/benchmark --language go --difficulty easy,medium

# Run with specific model configuration
go run ./cmd/benchmark --language go --difficulty easy --config ~/.config/marshal/benchmark.toml

# Run a single exercise
go run ./cmd/benchmark --exercise two-fer --language go
```

### Benchmark Structure

```
benchmark/
├── cmd/benchmark/       - Benchmark runner CLI
├── exercises/           - Cloned exercism exercises (gitignored)
├── results/             - Benchmark results and reports
└── README.md            - This file
```

### Metrics

- **Edit Success Rate**: Percentage of exercises where all test cases pass after editing
- **Average Rounds**: Average number of rounds needed per exercise
- **Token Usage**: Total prompt and completion tokens consumed
- **Duration**: Total time to complete exercises

### Comparison

Results are comparable to Aider's exercism benchmark for measuring relative performance.

### Requirements

- Go 1.23+
- `marshal` binary in PATH
- API keys configured for benchmark models

### Configuration

Create `~/.config/marshal/benchmark.toml`:

```toml
[model.executor]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/routers/kimi-k2p5-turbo"
supports_tools = true

[model.critic]
provider = "openai-compat"
base_url = "https://api.fireworks.ai/inference/v1"
api_key = "${FIREWORKS_API_KEY}"
model = "accounts/fireworks/models/qwen2p5-14b-instruct"

[loop]
max_rounds = 5
```
