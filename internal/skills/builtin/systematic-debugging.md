---
name: systematic-debugging
description: Find the root cause of a bug, test failure, crash, or unexpected behaviour before changing any code. Use the moment something does not work as expected, and especially when a first fix attempt did not work.
risk: read_only
---

# Systematic Debugging

The goal is to find the cause. Changing code before you have one is guessing, and guessing is what turns a one-line bug into an afternoon.

## The loop

1. **Reproduce it.** Get a reliable command that shows the failure. If you cannot reproduce it, everything after this is speculation. For intermittent failures, run it in a loop until you have a rate.
2. **Read the actual error.** The whole message, the whole stack trace, the actual line. Not the summary you assumed it said.
3. **Form one hypothesis.** State what you think is wrong and — critically — what you would expect to observe if you were right.
4. **Test that hypothesis cheaply.** A log line, a targeted test, reading the function. One variable at a time.
5. **Confirm or discard, then repeat.** A discarded hypothesis is progress: write down what it ruled out.
6. **Only now, fix.** With the cause identified, the fix is usually small and obvious.
7. **Verify.** Re-run the original reproduction. Then run the wider suite for regressions.

## Narrowing

- **Bisect.** Last known-good commit vs now, or comment out half.
- **Check the boundary.** Most bugs live where two components meet: what one produces, what the other expects.
- **Question the assumption.** "That can't be nil" and "that's always sorted" are where bugs hide. Verify, don't assert.

## Red flags

- "Let me just try changing this and see" — that is guessing. Form a hypothesis first.
- "It's probably a caching issue" — probably is not a diagnosis.
- Fixing a symptom you found while looking for the cause, then declaring victory.
- Adding a retry, sleep, or `try/catch` to make a failure stop appearing. The bug is still there and now it is invisible.
