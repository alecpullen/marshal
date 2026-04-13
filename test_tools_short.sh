#!/bin/bash
# Short context tool use tests for 16K context window models
# Focused tests that fit within Qwen3-30B's 16384 token limit

set -e

echo "=== Marshal Tool Use Test Suite (Short Context) ==="
echo ""

# Check prerequisites
echo "Checking LM Studio..."
if ! curl -s http://localhost:1234/v1/models > /dev/null 2>&1; then
    echo "ERROR: LM Studio not running on port 1234"
    exit 1
fi
echo "✓ LM Studio is running"

echo "Checking Fireworks API key..."
if [ -z "$FIREWORKS_API_KEY" ]; then
    if [ -f .env ]; then
        export $(grep -v '^#' .env | xargs)
    fi
fi
if [ -z "$FIREWORKS_API_KEY" ]; then
    echo "ERROR: FIREWORKS_API_KEY not set"
    exit 1
fi
echo "✓ Fireworks API key is set"

# Create tiny test file for reading
echo "tiny test content" > /tmp/tiny_test.txt

echo ""
echo "=== Test 1: Local - read_file (tiny file) ==="
./bin/marshal run --json "read /tmp/tiny_test.txt" 2>&1 | tee /tmp/test1_local_read.ndjson | tail -3

echo ""
echo "=== Test 2: Hybrid - read_file (tiny file) ==="
./bin/marshal run --profile hybrid --json "read /tmp/tiny_test.txt" 2>&1 | tee /tmp/test2_hybrid_read.ndjson | tail -3

echo ""
echo "=== Test 3: Local - write_file ==="
./bin/marshal run --json "write to /tmp/test_write_local.txt: hello local" 2>&1 | tee /tmp/test3_local_write.ndjson | tail -3

echo ""
echo "=== Test 4: Hybrid - write_file ==="
./bin/marshal run --profile hybrid --json "write to /tmp/test_write_hybrid.txt: hello api" 2>&1 | tee /tmp/test4_hybrid_write.ndjson | tail -3

echo ""
echo "=== Test 5: Local - run_command ==="
./bin/marshal run --json "run: echo 'local test'" 2>&1 | tee /tmp/test5_local_cmd.ndjson | tail -3

echo ""
echo "=== Test 6: Hybrid - run_command ==="
./bin/marshal run --profile hybrid --json "run: echo 'api test'" 2>&1 | tee /tmp/test6_hybrid_cmd.ndjson | tail -3

echo ""
echo "=== Test Results ==="
for f in /tmp/test*.ndjson; do
    verdict=$(jq -r 'select(.event == "session_end") | .verdict' "$f" 2>/dev/null | tail -1)
    duration=$(jq -r 'select(.event == "session_end") | .duration_ms' "$f" 2>/dev/null | tail -1)
    echo "  $(basename $f .ndjson): verdict=$verdict, duration=${duration}ms"
done

echo ""
echo "=== Verify Files Were Created ==="
ls -la /tmp/test_write*.txt 2>/dev/null || echo "No files created"
ls -la /tmp/tiny_test.txt 2>/dev/null || echo "Tiny test file missing"
