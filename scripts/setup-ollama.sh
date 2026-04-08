#!/bin/bash
# Setup Ollama for local Marshal testing with split executor/critic models
# Optimized for 16GB RAM MacBooks

set -e

echo "=== Marshal Ollama Setup ==="
echo ""

# Check if Ollama is installed
if ! command -v ollama &> /dev/null; then
    echo "Ollama not found. Installing via Homebrew..."
    brew install ollama
fi

# Check if Ollama server is running
if ! curl -s http://localhost:11434/api/tags > /dev/null 2>&1; then
    echo "Starting Ollama server..."
    ollama serve &
    OLLAMA_PID=$!
    sleep 3

    # Verify it started
    if ! curl -s http://localhost:11434/api/tags > /dev/null 2>&1; then
        echo "ERROR: Failed to start Ollama server"
        exit 1
    fi
    echo "Ollama server started (PID: $OLLAMA_PID)"
else
    echo "Ollama server already running"
fi

echo ""
echo "=== Pulling Models ==="
echo ""

# Executor: Fast coding model (low RAM)
echo "Pulling executor model: qwen2.5-coder:7b (4.5GB RAM)..."
ollama pull qwen2.5-coder:7b

# Critic: Reasoning model with <think> tags
echo ""
echo "Pulling critic model: deepseek-r1:7b (4GB RAM, outputs <think> tags)..."
ollama pull deepseek-r1:7b

echo ""
echo "=== Creating ollama.toml Config ==="

cat > ollama.toml << 'EOF'
# Local Ollama configuration for Marshal testing
# Optimized for 16GB RAM with split executor/critic models

[executor]
# Qwen 2.5 Coder 7B - fast, good code quality, ~4.5GB RAM
model       = "qwen2.5-coder:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"  # required but ignored by Ollama
temperature = 0.2       # low temp for consistent code
max_tokens  = 4096

[critic]
# DeepSeek-R1 7B - reasoning model with <think> tags, ~4GB RAM
# The model outputs reasoning in <think>...</think> blocks before the verdict
model       = "deepseek-r1:7b"
base_url    = "http://localhost:11434/v1"
api_key     = "ollama"
temperature = 0.6       # higher temp for reasoning diversity
max_tokens  = 8192      # headroom for think blocks + JSON verdict
json_output = false     # R1 doesn't reliably follow JSON mode, we parse from text

[loop]
max_rounds        = 3
auto_commit       = false   # manual review for local testing
auto_revert       = true
branch_isolation  = true
compact_after     = 2
EOF

echo "Created ollama.toml"
echo ""

# Calculate total RAM usage
EXECUTOR_RAM="4.5"
CRITIC_RAM="4.0"
TOTAL_RAM=$(echo "$EXECUTOR_RAM + $CRITIC_RAM" | bc)

echo "=== Setup Complete ==="
echo ""
echo "Models:"
echo "  Executor: qwen2.5-coder:7b  (~${EXECUTOR_RAM}GB RAM) - code generation"
echo "  Critic:   deepseek-r1:7b    (~${CRITIC_RAM}GB RAM) - reasoning with <think> tags"
echo ""
echo "Estimated RAM usage: ~${TOTAL_RAM}GB (of 16GB available)"
echo ""
echo "Usage:"
echo "  export FIREWORKS_API_KEY=ollama  # dummy value for validation"
echo "  go run cmd/marshal/main.go -config ollama.toml"
echo ""
echo "To test the setup:"
echo "  curl http://localhost:11434/api/generate -d '{\"model\":\"qwen2.5-coder:7b\",\"prompt\":\"Hello\"}'"
