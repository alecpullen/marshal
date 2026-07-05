# Task 6: Replace textinput with textarea — Report

## Status: Complete

## Changes

**File modified:** `internal/app/tui/model.go`

| Step | Change | Details |
|------|--------|---------|
| 1 | Import swap | `textinput` → `textarea` |
| 2 | Field type | `input textinput.Model` → `input textarea.Model` |
| 3 | New() updated | Creates `textarea.New()`, sets `ShowLineNumbers=false`, `MaxHeight=8`, `SetHeight(1)`, `SetWidth(80)`, remaps `InsertNewline` to `shift+enter` |
| 4 | `blinkCmd()` | New package-level function wrapping `textarea.Blink` |
| 5 | `Init()` | Now calls `blinkCmd()` |
| 6 | `resize()` | `m.input.Width =` → `m.input.SetWidth(...)` |
| 7 | Enter key | Already submits — left unchanged |
| 8 | Up/Down keys | No viewport-scrolling blocks existed to remove (already cleaned up in prior task); command suggestion blocks kept |
| 9 | `acceptCommandSuggestion()` | Removed `m.input.CursorEnd()` call |
| — | view.go | No changes needed — already used `m.input.View()` |

## Build Verification

- `go build ./internal/app/tui/` — PASS
- `go build ./cmd/marshal/` — pre-existing error in `app.go:397` (unrelated)

## Commit

```
b492942 feat(tui): replace textinput with auto-growing multiline textarea
```

---

## Review Fixes (applied after review)

### Issue 1: Missing `input.Focus()` in `New()`

**Problem:** The textarea was created without calling `Focus()`, so it ignored keyboard input on startup.

**Fix:** Added `input.Focus()` after the keymap setup in `New()` at `internal/app/tui/model.go:147`.

### Issue 2: Shift+Enter submits instead of inserting newline

**Problem:** Both plain Enter and Shift+Enter hit the `case tea.KeyEnter:` handler, submitting the input instead of inserting a newline.

**Fix:** Added a `key.Matches(msg, m.input.KeyMap.InsertNewline)` guard at `internal/app/tui/model.go:407`. When the key event matches the `shift+enter` binding, it `break`s out of the switch, falling through to `m.input.Update(msg)` which inserts the newline.

### Files changed

| File | Change |
|------|--------|
| `internal/app/tui/model.go` | Added `"github.com/charmbracelet/bubbles/key"` import; added `input.Focus()` after keymap setup; added `key.Matches` guard on `KeyEnter` handler |

### Verification

```
$ go build ./internal/app/tui/
$ echo $?
0
```

`go vet ./internal/app/tui/` reports one pre-existing error in `model_test.go:1633` (undefined `lastMessageCount` field) — not introduced by these changes.

### Confirmation

Both fixes compile cleanly and address the two review issues.
