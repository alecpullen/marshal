#!/bin/bash
# Test Ollama models for Marshal
# Shows executor code generation and critic reasoning with  tags

set -e

OLLAMA_URL="http://localhost:11434"

echo "=== Testing Ollama Models ==="
echo ""

# Check Ollama is running
if ! curl -s ${OLLAMA_URL}/api/tags > /dev/null 2>&1; then
    echo "ERROR: Ollama server not running. Run: ollama serve"
    exit 1
fi

echo "Ollama server: OK"
echo ""

# Test Executor (qwen2.5-coder)
echo "=== Testing Executor: qwen2.5-coder:7b ==="
echo "Task: Write a Go function to reverse a string"
echo ""

curl -s ${OLLAMA_URL}/api/generate \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen2.5-coder:7b",
    "prompt": "Write a Go function to reverse a string. Include a doc comment and handle Unicode correctly.",
    "stream": false,
    "options": {
      "temperature": 0.2
    }
  }' | jq -r '.response' | head -30

echo ""
echo "---"
echo ""

# Test Critic (deepseek-r1) - showing  tags
echo "=== Testing Critic: deepseek-r1:7b (with  tags) ==="
echo "Task: Review code and provide reasoning"
echo ""

curl -s ${OLLAMA_URL}/api/generate \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-r1:7b",
    "prompt": "Review this Go function:\n\nfunc reverse(s string) string {\n    runes := []rune(s)\n    for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {\n        runes[i], runes[j] = runes[j], runes[i]\n    }\n    return string(runes)\n}\n\nProvide a structured review. First reason about the code in  tags, then give your verdict as JSON: {\"verdict\": \"PASS\" or \"FAIL\", \"issue\": \"...\", \"fix\": \"...\"}",
    "stream": false,
    "options": {
      "temperature": 0.6
    }
  }' | jq -r '.response'

echo ""
echo "=== Test Complete ==="
echo ""
echo "Note: The critic outputs reasoning in  tags before the verdict."
echo "This is the expected format for M2 critic parsing."
