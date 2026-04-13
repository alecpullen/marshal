#!/bin/bash
# Simple tests for 16K context models

set -e

echo "=== Simple Tool Tests (16K context safe) ==="
echo ""

# Export Fireworks key
if [ -f .env ]; then
    export $(grep -v '^#' .env | xargs)
fi

# Test 1: Simple read with local model (small file)
echo "Test 1: Local model - read small file..."
./bin/marshal run --json "read go.mod" 2>&1 | tee /tmp/test1.ndjson | jq -r 'select(.event == "session_end") | .verdict'

# Test 2: Write with local model
echo ""
echo "Test 2: Local model - write file..."
./bin/marshal run --json "write to test1.txt: hello world" 2>&1 | tee /tmp/test2.ndjson | jq -r 'select(.event == "session_end") | .verdict'

# Test 3: Command with local model
echo ""
echo "Test 3: Local model - run command..."
./bin/marshal run --json "run: echo test" 2>&1 | tee /tmp/test3.ndjson | jq -r 'select(.event == "session_end") | .verdict'

# Test 4: Read with hybrid (API)
echo ""
echo "Test 4: Hybrid (API) - read file..."
./bin/marshal run --profile hybrid --json "read go.mod" 2>&1 | tee /tmp/test4.ndjson | jq -r 'select(.event == "session_end") | .verdict'

# Test 5: Write with hybrid
echo ""
echo "Test 5: Hybrid - write file..."
./bin/marshal run --profile hybrid --json "write to test2.txt: hello from api" 2>&1 | tee /tmp/test5.ndjson | jq -r 'select(.event == "session_end") | .verdict'

# Test 6: Command with hybrid
echo ""
echo "Test 6: Hybrid - run command..."
./bin/marshal run --profile hybrid --json "run: pwd" 2>&1 | tee /tmp/test6.ndjson | jq -r 'select(.event == "session_end") | .verdict'

echo ""
echo "=== Summary ==="
for i in 1 2 3 4 5 6; do
    f="/tmp/test$i.ndjson"
    if [ -f "$f" ]; then
        verdict=$(jq -r 'select(.event == "session_end") | .verdict' "$f" 2>/dev/null | tail -1)
        tokens=$(jq -r 'select(.event == "session_end") | .total_tokens' "$f" 2>/dev/null | tail -1)
        echo "Test $i: $verdict ($tokens tokens)"
    fi
done
