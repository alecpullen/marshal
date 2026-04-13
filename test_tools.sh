#!/bin/bash
# Comprehensive tool use testing for Marshal
# Tests all three tools (read_file, write_file, run_command) with different models

set -e

echo "=== Marshal Tool Use Test Suite ==="
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

echo ""
echo "=== Test 1: Local Qwen3-coder (all roles) - Simple read_file ==="
./bin/marshal run --json "read the file internal/tools/read_file.go and tell me what it does" 2>&1 | tee /tmp/test1_local_read.ndjson | tail -5

echo ""
echo "=== Test 2: Hybrid Profile (Fireworks executor) - Simple read_file ==="
./bin/marshal run --profile hybrid --json "read the file internal/tools/read_file.go and tell me what it does" 2>&1 | tee /tmp/test2_hybrid_read.ndjson | tail -5

echo ""
echo "=== Test 3: Local model - write_file test ==="
./bin/marshal run --json "create a test file at test_output.txt with content 'Hello from Marshal tool test'" 2>&1 | tee /tmp/test3_local_write.ndjson | tail -5

echo ""
echo "=== Test 4: Hybrid - write_file test ==="
./bin/marshal run --profile hybrid --json "create a test file at test_output_hybrid.txt with content 'Hello from hybrid profile test'" 2>&1 | tee /tmp/test4_hybrid_write.ndjson | tail -5

echo ""
echo "=== Test 5: Local - run_command test ==="
./bin/marshal run --json "run the command 'ls -la' and show me the output" 2>&1 | tee /tmp/test5_local_cmd.ndjson | tail -5

echo ""
echo "=== Test 6: Hybrid - run_command test ==="
./bin/marshal run --profile hybrid --json "run the command 'pwd' and show me the output" 2>&1 | tee /tmp/test6_hybrid_cmd.ndjson | tail -5

echo ""
echo "=== Test 7: Complex multi-tool task (hybrid) ==="
./bin/marshal run --profile hybrid --json "read the file go.mod, then create a summary file at go_mod_summary.txt describing the module name and Go version" 2>&1 | tee /tmp/test7_multi.ndjson | tail -5

echo ""
echo "=== Test Results Summary ==="
echo "All test outputs saved to /tmp/test*.ndjson"
echo ""

# Check for PASS verdicts
echo "Verdicts:"
for f in /tmp/test*.ndjson; do
    verdict=$(jq -r 'select(.event == "session_end") | .verdict' "$f" 2>/dev/null | tail -1)
    echo "  $(basename $f): $verdict"
done
