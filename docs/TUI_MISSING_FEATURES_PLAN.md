# TUI Missing Features — Implementation Plan

**Status:** ✅ COMPLETE — All features implemented  
**Date:** April 2026

---

## Summary

All five planned features have been implemented:

1. ✅ **Fuzzy File Picker** — `ctrl+f` in composer opens live file search with preview
2. ✅ **`:cancel` Command** — Gracefully terminates running tasks with UI feedback
3. ✅ **`:retry` Command** — Reopens composer pre-filled with last completed task
4. ✅ **`:sessions` Browser** — Full SQLite-backed session list with resume/diff
5. ✅ **`?` Help Overlay** — Keyboard shortcut reference (global shortcut)

---

## Original Implementation Details

**Scope:** Complete the stubbed peripheral features identified during M5 review.

---

## 1. Fuzzy File Picker

**Current state:** `ctrl+f` in composer hardcodes a placeholder file (`src/components/Example.tsx`).  
**Goal:** Live fuzzy search over repo files with immediate preview.

### UI Design
```
┌─────────────────────────────────────────────────────────────┐
│ 🔍 eventform                      12 matches                │
├─────────────────────────────────────────────────────────────┤
│ ❯ src/components/EventForm.tsx                              │
│   src/components/EventForm.test.tsx                         │
│   src/forms/EventForm/schema.ts                             │
├─────────────────────────────────────────────────────────────┤
│ 📄 src/components/EventForm.tsx    234 lines  TypeScript    │
├─────────────────────────────────────────────────────────────┤
│ ctrl+f to search · ↵ pin · esc close                        │
└─────────────────────────────────────────────────────────────┘
```

### Implementation

| Step | File | Work |
|------|------|------|
| 1 | `internal/tui/fuzzy.go` | New file: `FuzzyModel` with `textinput` for query, filtered results list, preview pane |
| 2 | `internal/tui/fuzzy.go` | Index all files (respect `.gitignore`, `ftSkip` from `filetree.go`) using `filepath.Walk` at init |
| 3 | `internal/tui/fuzzy.go` | Scoring: substring match > prefix match; sort by score then path depth |
| 4 | `internal/tui/fuzzy.go` | Preview: read first 20 lines, render in `colTx3` with file info footer |
| 5 | `internal/tui/app.go` | Add `overlayFuzzy` case; wire `ctrl+f` in composer to open it |
| 6 | `internal/tui/composer.go` | On fuzzy close with selection, append `fileChip` to `pinnedFiles` |

### Keyboard
- `ctrl+f` — open from composer
- `esc` — close without selection
- `↵` — pin selected file to composer
- `↑/↓` — navigate results
- `ctrl+k` — clear query

---

## 2. `:cancel` Command

**Current state:** Logs "cancel not yet implemented".  
**Goal:** Gracefully terminate the running loop task.

### Requirements
1. Send cancellation signal to active `LoopAdapter.runTask()` goroutine
2. Git cleanup: hard reset to pre-task HEAD, delete isolation branch
3. UI: immediate "cancelling..." state in sidebar + log line
4. Block new task submissions until cancel completes

### Implementation

| Step | File | Work |
|------|------|------|
| 1 | `internal/loop/loop.go` | Add `context.Context` support to `Loop.Run()`; check cancellation between rounds |
| 2 | `internal/tui/loop_adapter.go` | Add `cancelFunc context.CancelFunc` field; call it in `runTask` |
| 3 | `internal/tui/loop_adapter.go` | `CancelTask()` method: call `cancelFunc()`, wait for `running` to go false |
| 4 | `internal/tui/app.go` | `:cancel` handler calls `loopAdapter.CancelTask()` |
| 5 | `internal/tui/sidebar.go` | Add `taskCancelling` state with amber spinner dot |
| 6 | `internal/tui/main_panel.go` | Log "cancelling..." line when cancel initiated |

### Edge Cases
- Cancel during git operations: wait for operation, then reset
- Cancel after critic PASS but before commit: revert anyway
- Double-cancel: ignore if already cancelling

---

## 3. `:retry` Command

**Current state:** Logs "retry not yet implemented".  
**Goal:** Re-run the most recently completed (failed or passed) task.

### Requirements
1. Identify last completed task from `main.blocks` history
2. Pre-fill composer with its description
3. Optionally preserve pinned files from original (v2)

### Implementation

| Step | File | Work |
|------|------|------|
| 1 | `internal/tui/app.go` | `:retry` handler: find last block where `state == blockPass \|\| blockFail` |
| 2 | `internal/tui/app.go` | Open composer with `composer.SetValue(block.description)` |
| 3 | `internal/tui/main_panel.go` | Log "retrying: <description>" system line |
| 4 | (optional) | Store `pinnedFiles` in taskBlock history for true replay |

---

## 4. `:sessions` Command (Session Browser Overlay)

**Current state:** Opens config overlay as placeholder.  
**Goal:** Full-screen overlay showing persisted sessions from SQLite store.

### UI Design
```
┌─ sessions ─────────────────────────────────────────────────┐
│                                                             │
│  ID              Task                    Status    When      │
│  ─────────────────────────────────────────────────────────  │
│  marshal-abc123  add venue column...   PASS       2h ago    │
│  marshal-def456   fix auth bug...        FAIL       5h ago    │
│  marshal-ghi789   update docs            PASS       1d ago    │
│                                                             │
│  q/esc close · ↵ resume session · d diff · x delete          │
└─────────────────────────────────────────────────────────────┘
```

### Implementation

| Step | File | Work |
|------|------|------|
| 1 | `internal/tui/sessions.go` | New file: `SessionsModel` with table list |
| 2 | `internal/tui/sessions.go` | Load from `store.Store.ListSessions()` |
| 3 | `internal/tui/sessions.go` | Columns: truncated ID, truncated task, status badge, relative time |
| 4 | `internal/tui/sessions.go` | Selection with `↑/↓`, actions: `↵` resume, `d` diff, `x` delete (confirm) |
| 5 | `internal/tui/app.go` | Add `overlaySessions`, wire `:sessions` to open it |
| 6 | `internal/tui/app.go` | Resume session: load task into composer, switch to main panel |

### Resume Flow
1. User selects session, presses `↵`
2. Close overlay, set `main.input.Value(session.Task)`
3. Optional: restore pinned files if stored in session metadata

---

## 5. `?` Help Overlay

**Current state:** Streams to log as system lines.  
**Goal:** Dedicated modal help with categorized shortcuts.

### UI Design
```
┌─ keyboard shortcuts ─────────────────────────────────────────┐
│                                                             │
│  Navigation        Task Control       Composer             │
│  ─────────────────────────────────────────────────────────  │
│  ↑/↓ scroll        ↵ submit task      tab open               │
│  tab cycle focus   :cancel abort      ctrl+↵ submit          │
│  esc clear/unfocus :retry last        ctrl+f add files       │
│                                                             │
│  Overlays            Diff Viewer       File Editor          │
│  ─────────────────────────────────────────────────────────  │
│  c config            tab next file      ctrl+s save          │
│  d diff (sidebar)    q/esc close        ctrl+e external      │
│  ? this help                                              │
│                                                             │
│  q / esc  close                                             │
└─────────────────────────────────────────────────────────────┘
```

### Implementation

| Step | File | Work |
|------|------|------|
| 1 | `internal/tui/help.go` | New file: `HelpModel` static content, two-column layout |
| 2 | `internal/tui/help.go` | Sections: Navigation, Task Control, Composer, Overlays |
| 3 | `internal/tui/app.go` | Add `overlayHelp`, wire `?` key (global, not just main panel) |
| 4 | `internal/tui/commands.go` | Update `helpLines()` to mention `?` shortcut |

---

## Priority Order

1. **Fuzzy picker** — highest impact, unblocks composer usefulness
2. **`:cancel`** — safety feature, required for production use
3. **Help overlay** — low effort, high discoverability benefit
4. **`:retry`** — convenience, small surface area
5. **`:sessions`** — requires store integration testing, largest scope

---

## Files to Create

```
internal/tui/
├── fuzzy.go      # Fuzzy file picker overlay
├── sessions.go   # Session browser overlay
└── help.go       # Keyboard shortcuts help overlay
```

## Files to Modify

```
internal/tui/
├── app.go            # Wire new overlays, command handlers
├── composer.go       # Hook ctrl+f to fuzzy
├── loop_adapter.go   # Add cancellation support
├── commands.go       # Update help text
└── sidebar.go        # Add cancelling state
```
