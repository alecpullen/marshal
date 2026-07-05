# Task 6 Report: renderPlan, renderDiff, renderToolResult

## Summary

Added three renderer functions to `internal/app/tui/renderers.go`:

- **renderPlan(role, content, width)**: Renders a "Plan" bordered panel. Each content line is a step with an accent-colored bold bullet `•`. Uses role label prefix (matching renderPlain/renderMarkdown conventions). Wrapped via `renderPanel("Plan", ...)`.

- **renderDiff(role, content, width)**: Renders a "Diff" bordered panel. Lines starting with `+` are green (successColor), `-` are red (errorColor), `@@` are dimmed (mutedStyle). Uses role label prefix. Wrapped via `renderPanel("Diff", ...)`.

- **renderToolResult(role, content, width)**: Renders inline (no panel). First line is styled with toolRoleStyle (bold, warning color). Remaining lines are dimmed via mutedStyle. Lines are wrapped to width.

## Verification

- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — all passing

## Commit

```
d269bb6 feat(tui): add renderPlan, renderDiff, renderToolResult renderers
```

## Concerns

None.
