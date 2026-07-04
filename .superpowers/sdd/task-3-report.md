# Task 3 Report: Add ClearMessages to Session State

## What I implemented
Added `ClearMessages()` method to `session.State` in `internal/app/session/session.go`.
It locks the mutex, records the current message count, sets `s.messages = nil`, and returns the count.
Placed after `Messages()` (line 182), before `BeginStreaming()` (line 197).

## Test results
All 20 tests pass, no regressions.

```
PASS
ok  	marshal/internal/app/session	0.514s
```

## Files changed
- `internal/app/session/session.go` — +11 lines (ClearMessages method)

## Self-review findings
- Follows existing patterns: `s.mu.Lock()`/`defer s.mu.Unlock()`, return before unlock.
- Consistent with `ClearBackup()` and `ClearTurnToolCache()` naming conventions.
- Does not touch audit log, backups, context pack, or persistence — as specified.
- One concern: if there is any downstream code that holds a reference to an element of `s.messages` (e.g. from `Messages()` returning a copy), that reference is unaffected since `Messages()` already returns a deep-ish copy of the slice. No issue.

## Issues or concerns
None.
