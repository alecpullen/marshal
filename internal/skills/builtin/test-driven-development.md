---
name: test-driven-development
description: Write the failing test before the implementation, for any feature or bugfix. Use when you are about to add a function, fix a defect, or change behaviour that a test could pin down. Not for pure refactors with existing coverage, or config-only edits.
risk: workspace_write
---

# Test-Driven Development

## The cycle

1. **Red** — write one test for the behaviour you want. Be specific: real inputs, real expected output.
2. **Verify red** — run it and confirm it fails, *and that it fails for the right reason*. A test that passes before you write code is testing nothing.
3. **Green** — write the minimum implementation that passes.
4. **Verify green** — run it again.
5. **Refactor** — clean up with the test as your safety net.
6. **Commit** — test and implementation together.

## In this repo

```bash
go test ./internal/<pkg>/ -run TestName -v   # single test
go test ./...                                # everything
```

Follow the surrounding package's style. This codebase uses table-driven tests with `t.Fatalf` for setup failures and `t.Errorf` for assertion failures.

## Test design

- One behaviour per test. A test that asserts five things fails uninformatively.
- Name the test for the behaviour, not the function: `TestQuietLoadsCountAgainstMaxActive`, not `TestLoad2`.
- Assert on observable outcomes, not internal calls.
- When fixing a bug, the first test must reproduce it. If you cannot reproduce it, you do not yet understand it — use `systematic-debugging`.

## Red flags

- "I'll write the tests after" — you will write tests that pass, not tests that check.
- "This is too simple to test" — simple code with a test costs one minute.
- Writing the test and implementation in the same edit — you skipped the red step and never learned whether the test works.
