# Task 4 Report: F17 Diff View + /diff
## Status: DONE
## Commits
- 0b2f375 feat(tui): syntax-highlighted side-by-side diff view + /diff (F17)
## Summary
- internal/diffview: new package with `Render(diff, Options)`, `Mode` (Auto ≥120 cols / SideBySide / Unified), `Options{Width, Mode, Highlight, Theme, Language}`, `Hunk`/`Line` types. Parser handles `--- / +++ / @@` headers + `+/-/space` body. Renderers: `renderUnified` (added/removed/context styled, optionally chroma-highlighted) and `renderSideBySide` (paired removed+added columns with intraline emphasis via `diffmatchpatch.DiffMain` finding the byte-offset runs that differ). Plain-text fallback below width floor or on parse/highlight failure; 500-line cap with truncation note. `detectLanguage` reads the `+++ b/...` path (or `Options.Language` override) and resolves to a chroma lexer (default Go). `highlightCode` (renamed from `highlight` to avoid shadowing `Options.Highlight`) wraps chroma tokens in lipgloss styles.
- approval integration: `approvalModel.View()` now renders `tc.Diff` via `diffview.Render(..., ModeAuto, Highlight=true)` above the `huh.Form`. The existing `width` field on `approvalModel` is reused. `internal/app/tui/approval_test.go` adds coverage for the diff-in-view path and the no-diff path.
- /diff command: added to `RegisterAll` in `internal/commands/commands.go`. Looks up `state.Snapshotter()` → `state.DB().SnapshotBefore(SessionID, TurnIndex)` → `sp.Diff(ctx, hash)` → emits the result via `state.AddMessage(RoleSystem, diff, ContentTypeDiff)` (the existing `renderDiffBlock` handles transcript rendering; the plan's `diffview.Render` call inside the transcript is a follow-up for full-width viewer). Friendly messages for "no snapshotter / no DB / no snapshot / no changes / diff error". Tests in `commands_test.go` cover the no-snapshot case and the registration.
- deps: `go get github.com/sergi/go-diff@latest` (v1.4.0) added; `chroma/v2 v2.14.0` promoted from `// indirect` to direct (resolved via `go mod tidy` after diffview started importing it). Clean-room provenance comment in `diffview.go`.
## Verification
- gofmt: clean
- go vet ./...: clean
- go test -count=1 ./internal/diffview/ -v: PASS (14 tests)
- go test -count=1 -race ./internal/diffview/ -v: PASS
- go test ./...: PASS
- go build ./cmd/marshal: success
## Concerns
- The transcript's `renderDiffBlock` in `internal/app/tui/transcript.go` was intentionally left as-is (plain colorized +/- lines, no chroma, no side-by-side, no intraline emphasis). Wiring the full `diffview.Render` into the transcript is a follow-up — the message is emitted with `ContentTypeDiff` so the existing path renders it. The approval dialog gets the rich renderer; the `/diff` transcript path gets a quick scroll-friendly view. Both share the same parser and styling primitives.
- 500-line cap with truncation note (per plan F17 R3 "Gaps and notes"); full virtualization is a follow-up.
- chroma v2.14.0 has no `chroma.Type` token type — used `chroma.NameClass`/`NameBuiltin` for type coloring in `highlightCode`.
