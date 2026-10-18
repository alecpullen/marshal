---
name: test-driven-development
description: Write the failing test before the implementation, for any feature or bugfix. Use when you are about to add a function, fix a defect, or change behaviour that a test could pin down. Not for pure refactors with existing coverage, or config-only edits.
risk: workspace_write
---

# Test-Driven Development

## The cycle

1. **Red** — write one test for the behaviour you want. Be specific: real inputs, real expected output.
2. **Verify red** — run it with `shell.run` and confirm it fails, *and that it fails for the right reason*. A test that passes before you write code is testing nothing.
3. **Green** — write the minimum implementation that passes, with `file.edit` (or `file.write` for a new file).
4. **Verify green** — run it again with `shell.run`.
5. **Refactor** — clean up with the test as your safety net.
6. **Commit** — only if your workflow commits at this point (a plan's per-task commit, or the user asked). Then the test and implementation go in together. Never commit just because a cycle ended.

## Running tests

Use the test commands from the repository instructions (AGENTS.md or equivalent) or the project's own tooling. Run the single test while cycling, for example `go test ./internal/<pkg>/ -run TestName -v` in Go or `pytest path/to/test_file.py::test_name` in Python. Run the package's tests before moving on.

Follow the surrounding tests' style: their structure (table-driven or not), helpers, assertion style and naming.

## Test design

- One behaviour per test. A test that asserts five things fails uninformatively.
- Name the test for the behaviour, not the function: `TestQuietLoadsCountAgainstMaxActive`, not `TestLoad2`.
- Assert on observable outcomes, not internal calls.
- When fixing a bug, the first test must reproduce it. If you cannot reproduce it, you do not yet understand it — use `systematic-debugging`.

## Red flags

- "I'll write the tests after" — you will write tests that pass, not tests that check.
- "This is too simple to test" — simple code with a test costs one minute.
- Writing the test and implementation in the same edit — you skipped the red step and never learned whether the test works.
