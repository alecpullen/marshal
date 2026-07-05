### Task 5 Report: Create renderers.go

**Status:** DONE

**Commit:** `970c2cb` — feat(tui): add renderPlain, renderMarkdown, renderCodeBlock, and markdown parser

**Build:** `go build ./...` — clean, no errors

**Tests:** `go test ./...` — all packages pass (18 ok, 3 no test files)

**Changes:**
- Created `internal/app/tui/renderers.go` (201 lines)
- `renderPlain` — exact copy of current `renderMessage` body from model.go
- `mdBlock` struct (`kind`, `text`) and `splitFencedBlocks` — splits content into prose/code blocks by ``` fences
- `parseMarkdownLine` — per-line markdown: headings (#/##/###), horizontal rules (---/***/___), blockquotes (>), unordered lists (- /*)
- `renderCodeBlock` — wraps content in lipgloss rounded border with dim foreground
- `renderMarkdown` — combines splitFencedBlocks, parseMarkdownLine, and renderCodeBlock with role label prepended per block

**Concerns:** None. The existing `renderMessage` in model.go is untouched — rename/replace happens in Task 7.
