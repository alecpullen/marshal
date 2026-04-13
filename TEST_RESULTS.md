# Marshal Tool Use Test Results

**Date:** 2026-04-14
**Models Tested:** Qwen3-Coder-30B-A3B (local), Kimi K2.5 Turbo (Fireworks API)

## Test Summary

All 6 tests **PASSED**. Both local and API models successfully used the `read_file`, `write_file`, and `run_command` tools.

## Individual Test Results

### Local Model (Qwen3-Coder-30B-A3B)

| Test | Tool | Prompt | Verdict | Tokens | Duration |
|------|------|--------|---------|--------|----------|
| 1 | read_file | "read go.mod" | PASS | 5,015 | 35s |
| 2 | write_file | "write to test1.txt: hello world" | PASS | 13,968 | 68s |
| 3 | run_command | "run: pwd" | PASS | 3,008 | 15s |

### Hybrid Profile (Kimi K2.5 Turbo via Fireworks API)

| Test | Tool | Prompt | Verdict | Tokens | Duration |
|------|------|--------|---------|--------|----------|
| 4 | read_file | "read go.mod" | PASS | 2,587 | 6s |
| 5 | write_file | "write to test2.txt: hello from api" | PASS | 5,223 | 7s |
| 6 | run_command | "run: pwd" | PASS | 2,668 | 2s |

## Key Observations

### Local Model (30B Qwen3-Coder)
- **Higher token usage**: 3-5x more tokens than API model
- **Verbose responses**: Repeated tool call attempts, redundant explanations
- **Slower**: 15-68s per task vs 2-7s for API
- **Works correctly**: All tools executed successfully
- **Context pressure**: Test 2 used 13,968 tokens (near 16K limit)

### API Model (Kimi K2.5 Turbo)
- **Efficient**: Lower token usage, faster responses
- **Concise**: Direct tool calls, minimal verbosity
- **Reliable**: Consistent sub-10s response times
- **Cost-effective**: ~$0.02 total for all 3 tests

## Tool Functionality Verified

✅ **read_file**: Successfully reads files, returns content
✅ **write_file**: Creates files, writes content, handles paths
✅ **run_command**: Executes commands, captures stdout/stderr
✅ **Multi-turn**: API model used 2-3 tool calls per task
✅ **Fallback**: Both paths (tool-use and edit-format) working

## Configuration Required

For 16K context models, add to `marshal.toml`:
```toml
supports_tools = true
```

For local Qwen3-Coder:
```toml
[model.executor]
supports_tools = true
```

## Issues Found

1. **Local model verbosity**: Generated excessive token usage with repetitive tool calls
2. **No streaming for tool-use**: UI shows blank during tool execution (expected)
3. **Test 2 near limit**: 13,968 tokens close to 16K context boundary

## Recommendations

1. **Use API for production**: Faster, cheaper, more reliable
2. **Keep prompts short**: For 16K models, use "read FILE" not "read FILE and explain..."
3. **Monitor token usage**: Add warning when approaching context limit
4. **Local good for testing**: Works but expect 3-5x cost in tokens/time

## Next Steps

- M13: Benchmarks and documentation
- Consider token limit warnings
- Add tool-use streaming output
