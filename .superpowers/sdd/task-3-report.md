# Task 3 Report: Rework refreshViewport with unified transcript timeline

## Status: Complete

## Changes Made

### model.go
- **Step 2**: Replaced `refreshViewport()` — uses `m.state.Transcript()` instead of interleaving messages + audit events; uses single `transcriptHash` for dirty tracking
- **Step 3**: Added `transcriptHash()` function at bottom of file
- **Step 4**: Replaced 6 dirty tracking fields (`lastMessageCount`, `lastStreamLen`, `lastHadApproval`, `lastHadError`, `lastActiveTool`, `lastAuditCount`) with single `lastTranscriptHash uint64`
- **Step 5**: Removed `"sort"` import (unused after removing `sortedAuditEvents`). `"registry"` kept — still used in rollback blocks
- **Step 6**: Updated Ctrl+G toggle to use `lastTranscriptHash = 0`
- Removed `sortedAuditEvents()` and `auditEventBeforeMessage()` — no longer needed

### transcript.go
- **Step 1**: Added `renderTranscriptItem()` — dispatches by `session.TranscriptKind` to `renderThinkingSummary`, `renderCompletedToolCall`, or `renderMessage` with reasoning
- **Step 7**: Updated `renderCompletedToolCall()` signature to remove `now time.Time` parameter; removed elapsed/time-ago suffix

## Build & Tests

- `go build ./internal/app/tui/` — **PASS**
- `go test ./internal/app/tui/ -run TestModel` — **FAIL** (expected, 2 test files reference `lastMessageCount` which was removed; Task 8 will fix)

## Commit

`e66f977` — `feat(tui): unified transcript timeline with Transcript()`

## Concerns

- `renderCompletedToolCall` no longer shows elapsed time (`· Xs ago`). This is intentional — Task 4 will re-add elapsed time using `TranscriptItem.Timestamp`
- The `registry` import is still required in both model.go and transcript.go
