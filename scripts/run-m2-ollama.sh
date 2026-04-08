#!/bin/bash
# Run Milestone 2 with Ollama local models
# Usage: ./scripts/run-m2-ollama.sh [task]

set -e

OLLAMA_URL="http://localhost:11434"
EXECUTOR_MODEL="qwen2.5-coder:7b"
CRITIC_MODEL="deepseek-r1:7b"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=== Marshal Milestone 2 - Ollama Runner ==="
echo ""

# Check Ollama is running
if ! curl -s ${OLLAMA_URL}/api/tags > /dev/null 2>&1; then
    echo -e "${RED}ERROR: Ollama server not running${NC}"
    echo "Start it with: ollama serve"
    exit 1
fi

echo -e "${GREEN}✓${NC} Ollama server running"

# Check models are pulled
echo ""
echo "Checking models..."

if ! curl -s ${OLLAMA_URL}/api/tags | grep -q "${EXECUTOR_MODEL}"; then
    echo -e "${YELLOW}⚠${NC} Executor model not found: ${EXECUTOR_MODEL}"
    echo "Pulling now..."
    ollama pull ${EXECUTOR_MODEL}
else
    echo -e "${GREEN}✓${NC} Executor: ${EXECUTOR_MODEL}"
fi

if ! curl -s ${OLLAMA_URL}/api/tags | grep -q "${CRITIC_MODEL}"; then
    echo -e "${YELLOW}⚠${NC} Critic model not found: ${CRITIC_MODEL}"
    echo "Pulling now..."
    ollama pull ${CRITIC_MODEL}
else
    echo -e "${GREEN}✓${NC} Critic: ${CRITIC_MODEL}"
fi

# Check ollama.toml exists
if [ ! -f "ollama.toml" ]; then
    echo ""
    echo -e "${YELLOW}⚠${NC} ollama.toml not found, creating..."
    cat > ollama.toml << 'EOF'
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
EOF
    echo -e "${GREEN}✓${NC} Created ollama.toml"
fi

# Set dummy API key for validation
export FIREWORKS_API_KEY="ollama"

# Get task from argument or use default
TASK="${1:-Write a Go function that calculates the nth Fibonacci number with memoization}"

echo ""
echo "=== Task ==="
echo "${TASK}"
echo ""
echo "=== Running Milestone 2 Loop ==="
echo ""

# Run the loop
go run cmd/marshal/main.go

echo ""
echo "=== Complete ==="
