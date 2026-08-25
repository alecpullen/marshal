---
name: verification-before-completion
description: Run the commands that prove work is done before claiming it is done. Use immediately before saying "fixed", "complete", "passing", or "working", and before committing or opening a PR.
risk: read_only
---

# Verification Before Completion

**Evidence before assertions, always.** A claim you have not verified is a guess presented as a fact, and it is worse than saying nothing.

## Before claiming completion

1. **Run the build.** `CGO_ENABLED=1 go build ./cmd/marshal`
2. **Run the tests.** `go test ./...` — not just the package you touched. Your change is exactly the kind that breaks a neighbour.
3. **Vet and format.** `go vet ./...` and `gofmt -l .`
4. **Read the output.** All of it. A suite that prints FAIL and exits 0 in a wrapper script has still failed.
5. **Re-check the original request.** Did you do everything asked, or the parts that were convenient?

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
