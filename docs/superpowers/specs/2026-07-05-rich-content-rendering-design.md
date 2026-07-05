# Rich Content Rendering Design

**Date**: 2026-07-05
**Author**: Alec Pullen
**Status**: Draft

## Summary

Extend Marshal's TUI with structured content rendering so the agent can produce markdown prose, code blocks, plans, diffs, and tool results — each with their own visual treatment in the chat viewport. Currently all content is plain text with a single role-colored label.

## Motivation

- Agent plans and tool results are visually indistinct from prose
- No support for bold/italic/code/headings in agent output
- Diff output lacks colorization
- All content shares one `renderMessage()` path with no dispatch

## Design

### 1. Content Type System

A new `ContentType` enum on `session.Message`:

| Constant | Value | Purpose |
|---|---|---|
| `ContentTypePlain` | `"plain"` | Backward-compatible default; no structural markup |
| `ContentTypeMarkdown` | `"markdown"` | Prose with inline formatting |
| `ContentTypeCode` | `"code"` | Monospace code block with bordered panel and dim background |
| `ContentTypePlan` | `"plan"` | Structured step list sourced from `state.Plan()` |
| `ContentTypeDiff` | `"diff"` | Colored +/- diff output in a bordered panel |
| `ContentTypeToolResult` | `"tool_result"` | Tool output with compact summary header |

`AddMessage()` gains a third parameter: `AddMessage(role, content, contentType)`. All existing callers pass `ContentTypePlain`.

### 2. Agent → Message Mapping

In `runner.go`, after `ParseAction()`, the runner maps the action type:

| Action Type | ContentType |
|---|---|
| `answer` | `markdown` |
| `final` | `markdown` |
| tool result | `tool_result` |
| `patch` | `diff` |
| plan text | `plan` |

### 3. Renderer Dispatch

`renderMessage(msg session.Message, width int)` dispatches by `ContentType` to standalone render functions in new file `internal/app/tui/renderers.go`.

### 4. Markdown Renderer

Line-by-line + inline parser: `**bold**`, `*italic*`, `# Heading`, `## Subheading`, `- list items`, `` `code spans` ``, `> blockquotes`, `---` HRs, fenced code blocks. Uses Lipgloss styles. Pure function, no AST.

### 5. Other Renderers

- **`renderCode`**: Bordered panel, dim background
- **`renderPlan`**: Numbered steps, bordered panel, accent color
- **`renderDiff`**: `+` green, `-` red, `@@` dim, bordered
- **`renderToolResult`**: Summary header + dimmed body
- **`renderPlain`**: Renamed from current `renderMessage()`, no behavior change

### 6. Files

| File | Change |
|---|---|
| `internal/app/session/session.go` | Add `ContentType` to `Message`; update `AddMessage()` |
| `internal/agent/runner.go` | Map action types → content types |
| `internal/app/tui/model.go` | Dispatch switch; wire content type in `refreshViewport()` |
| `internal/app/tui/renderers.go` | **New**: all renderers + markdown parser |
| `internal/db/` | Add `content_type` column to messages table |

### 7. Out of Scope

Full CommonMark, syntax highlighting, terminal hyperlinks, user-input markdown, protocol changes.
