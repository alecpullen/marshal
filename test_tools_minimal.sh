#!/bin/bash
# Minimal tool use tests for 16K context models
# Uses small files and short prompts

set -e

echo "=== Marshal Tool Use Test Suite (Minimal Context) ==="
echo ""

# Create a small test file first
echo "package main

func main() {
    println("Hello, World!")
}" > /tmp/small_test.go

export FIREWORKS_API_KEY=$(grep FIREWORKS_API_KEY .env 2>/dev/null | cut -d= -f2)

echo "=== Test 1: Local - read small file ==="
./bin/marshal run --json "read /tmp/small_test.go" 2>&1 | tee /tmp/minimal1.ndjson | tail -3

echo ""
echo "=== Test 2: Hybrid - read small file ==="
./bin/marshal run --profile hybrid --json "read /tmp/small_test.go" 2>&1 | tee /tmp/minimal2.ndjson | tail -3

echo ""
echo "=== Test 3: Local - write file ==="
./bin/marshal run --json "write to /tmp/test1.txt with 'test content'" 2>&1 | tee /tmp/minimal3.ndjson | tail -3

echo ""
echo "=== Test 4: Hybrid - write file ==="
./bin/marshal run --profile hybrid --json "write to /tmp/test2.txt with 'hybrid test'" 2>&1 | tee /tmp/minimal4.ndjson | tail -3

echo ""
echo "=== Test 5: Local - run command ==="
./bin/marshal run --json "run 'echo hello'" 2>&1 | tee /tmp/minimal5.ndjson | tail -3

echo ""
echo "=== Test 6: Hybrid - run command ==="
./bin/marshal run --profile hybrid --json "run 'echo hello from api'" 2>&1 | tee /tmp/minimal6.ndjson | tail -3

echo ""
echo "=== Results ==="
for f in /tmp/minimal*.ndjson; do
    verdict=$(jq -r 'select(.event == "session_end") | .verdict' "$f" 2>/dev/null | tail -1)
    tokens=$(jq -r 'select(.event == "session_end") | .total_tokens' "$f" 2>/dev/null | tail -1)
    echo "  $(basename $f): $verdict (${tokens} tokens)"
done
