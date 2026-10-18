---
name: verification-before-completion
description: Run the commands that prove work is done before claiming it is done. Use immediately before saying "fixed", "complete", "passing", or "working", and before committing or opening a PR.
risk: read_only
---

# Verification Before Completion

**Evidence before assertions, always.** A claim you have not verified is a guess presented as a fact, and it is worse than saying nothing.

## Find the project's commands

Take the build, test, lint, and format commands from the repository instructions (AGENTS.md, CLAUDE.md, or equivalent), or from the project's own tooling (Makefile, package.json scripts, CI config). Do not assume a language or toolchain. For a Go project that is typically `go build ./...`, `go test ./...`, `go vet ./...` and `gofmt -l .`, but the repo's instructions win, including any required environment (for example `CGO_ENABLED=1`).

## Scale the check to the claim

Run each command with `shell.run` and read the output it returns.

1. **The claim's own proof.** The narrowest command that shows the thing you changed works: the targeted test, the reproduction of the bug, the task's Verify block. Always.
2. **The neighbours.** The tests of every package or module you touched, plus its direct dependents when you changed an interface. Before committing.
3. **The whole project.** Full build, full test suite, lint and format. Once at the end of a change set — before you report a multi-step change complete, or before a merge or PR — not after every edit.

When a workflow defines its own gate — a plan task's Verify block, a pipeline's build+test gate — that gate is the check for that step. Do not widen it on your own; the full-project check runs once at the end of the workflow.

## Known failures

Some tests may fail before you change anything. If the workflow or the repository already records them, compare against that list: only new failures are yours. If nothing records them and a failure looks unrelated, check it once at the base commit, write down the result, and do not re-prove it on every later step.

## Before claiming completion

- **Read the output.** All of it. A suite that prints FAIL and exits 0 in a wrapper script has still failed.
- **Re-check the original request.** Did you do everything asked, or the parts that were convenient?

## Reporting

- Passing: say so plainly, and name what you ran.
- Failing: say so, and paste the actual output. Never round a failure down to "mostly working".
- Skipped: say what you skipped and why. Silently narrowing scope is the user's decision to make, not yours.
- Untested: "I implemented X but could not run the tests because Y" is an honest and useful report.

## Red flags

- "That should work" — run it.
- "The tests were passing before" — run them now.
- "It's a trivial change" — trivial changes break builds constantly. Run it.
- Claiming completion in the same breath as the last edit, with no command in between.
- Running the full suite after every small edit, or re-proving a known failure each time — that is not rigor, it is the same evidence bought again.
