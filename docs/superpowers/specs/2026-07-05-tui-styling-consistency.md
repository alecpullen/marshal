# TUI Styling Consistency Pass

**Date:** 2026-07-05
**Status:** Design approved

## Overview

Unify and clean up Marshal's TUI styling. Fix border inconsistencies, add consistent padding, introduce visual breathing room between user and agent content, and color-code the three main zones (transcript, input box, status bar) using a muted, terminal-native palette.

## Design Principles

- **Distinct but muted panels** — each major zone has a recognizable color family with low saturation so content stays the focus.
- **Consistent borders** — all bordered panels use `RoundedBorder` and consistent border width.
- **Consistent padding** — panels and messages have predictable internal spacing.
- **Centralized theme** — move inline `lipgloss.NewStyle()` calls to package-level style variables in `model.go`.

## Panel System

### Transcript panel

- Border: `RoundedBorder`
- Border color: `panelBorderColor` (`240`)
- Background: default terminal background (no fill)
- Messages receive a consistent 2-space left gutter so content does not touch the frame

### Input area

- Border: `RoundedBorder`
- Border color: `accentColor` (`38`) — blue/cyan tint to signal "active control"
- Padding: `0, 1` (preserve current horizontal inset)
- Background: default terminal background

### Status bar

- Keep the existing filled background (`235`) and foreground (`252`)
- Continue using accent/warning/error colors for state indicators
- Add a subtle top border or separator to visually detach it from the input area

## Message Layout

- Insert a blank line between user messages and the following agent output for visual grouping.
- User messages keep the `❯ ` accent prefix.
- Agent markdown prose renders with a consistent left gutter instead of mixed indentation.
- Final answers and code blocks both use `RoundedBorder`; final answers use `accentColor` for the border, code blocks keep `dimColor`.

## Theme Centralization

Move these inline styles to package-level variables in `model.go`:

```go
var (
    transcriptFrameStyle = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(panelBorderColor).
        Padding(1, 1)

    inputBoxStyle = lipgloss.NewStyle().
        Border(lipgloss.RoundedBorder()).
        BorderForeground(accentColor).
        Padding(0, 1)

    statusBarStyle = lipgloss.NewStyle().
        Background(lipgloss.Color("235")).
        Foreground(lipgloss.Color("252"))
)
```

Remove or repurpose the unused `mutedColor` variable. `mutedStyle` remains `dimColor`.

## Files Changed

| File | Changes |
|------|---------|
| `internal/app/tui/view.go` | Use centralized frame/input styles, consistent border/padding |
| `internal/app/tui/transcript.go` | Consistent message spacing, centralized prose/code styles |
| `internal/app/tui/status.go` | Use centralized status-bar style |
| `internal/app/tui/model.go` | Add centralized style vars, remove unused color |
| Test files | Update assertions affected by spacing/border changes |

## Acceptance Criteria

- All three panels are visually distinct.
- Transcript and input borders are the same shape but different colors.
- Messages have consistent internal spacing.
- `go test ./...`, `go vet ./...`, `gofmt -l .` all pass.
