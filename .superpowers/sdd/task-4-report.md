# Task 4 Report: Update DB schema and persistence for content_type

**Status: DONE**

**Commit:** `92f395b` — `feat(db): add content_type column to messages table`

## Changes Made

### 1. `internal/db/migrations.go`
- Added `content_type TEXT` column to messages table DDL (after `content TEXT NOT NULL`)

### 2. `internal/db/sessions.go` — Message struct
- Added `ContentType string` field to `db.Message` struct

### 3. `internal/db/sessions.go` — SaveMessage
- Added `contentType string` parameter to signature
- Added `sql.NullString` handling (stores NULL for empty `""` or `"plain"`, stores the value for `"markdown"`)
- Updated INSERT to include `content_type` column

### 4. `internal/db/sessions.go` — GetMessages
- Added `content_type` to SELECT columns
- Added `contentType sql.NullString` to Scan
- Populates `m.ContentType` from NullString (defaults to `""` which means `"plain"`)

### 5. `internal/app/session/session.go`
- Updated `SaveMessage` call to pass `string(contentType)` as 4th argument

### 6. `internal/db/sessions_test.go`
- Test call 1: `"plain"` content_type for user message "hello"
- Test call 2: `"markdown"` content_type for assistant message "hi there"

## Test Results

```
go build ./...            — PASS
go test ./internal/db/... — PASS (all 24 tests)
go test ./...             — PASS (all packages)
```

## Concerns

None. Null-string semantics: `""` and `"plain"` are stored as SQL NULL and round-trip back as `""` (default). `"markdown"` is stored as `"markdown"` and round-trips correctly.
