# Task 5 Report: F18 Editor Completions
## Status: DONE
## Commits
- 4419659 feat(tui): /command and @file fuzzy completion popups + pinned context (F18)
## Summary
- internal/app/tui/completions.go: new file. `fuzzyScore(query, target) (int, bool)` is an in-package subsequence scorer that rewards consecutive matches (1+consecutive) and prefix matches (+5 for qi==0, +2 for ti==0) so "/pl" ranks "/plan" above "/help"; empty query matches with a non-zero tie-breaker score. `completionItem{Text, Description, Kind}` and `completionPopup{items, filtered, index, visible, acceptedText}`. `completionKind` constants `completionCommand` / `completionFile`. Popup methods: `update(query)`, `matches()`, `isVisible()`, `dismiss()`, `moveDown()`, `moveUp()`, `accept()`. `accept()` writes the chosen text into `acceptedText` (commands get a trailing space, file paths get a trailing space iff no whitespace inside) and dismisses.
- internal/app/tui/completions_test.go: 9 tests covering subsequence matching, ranking, empty-query, filter/select, no-match hidden, empty query hides, esc dismiss, navigation wraparound, and file-kind accepted text.
- internal/app/tui/model.go: replaced the legacy `commandSuggestions` / `commandSuggestionIndex` strip (and the `commandSuggestionRows` constant in view.go) with `cmdPopup` and `filePopup`. New fields on `Model` (cmdPopup, filePopup, fileIndex, fileIndexLoaded). New `WithFileIndex(paths)` option for eager seeding. New helpers: `updateCompletionPopups()` (inspects the current input value on every keystroke and routes to the appropriate popup), `commandTrigger(value)` and `fileTrigger(value)` (substring/word-boundary detection; `commandTrigger` requires no whitespace inside the value, `fileTrigger` requires the `@` to be at start-of-input or after a word-boundary byte and no whitespace after it), `populateFileIndexIfNeeded()` (lazy load from `m.memoryDB.GetFileIndex(m.memoryProject)` on the first `@`-keystroke), `activeCompletionPopup()`, `dismissCompletionPopups()`, `acceptCompletion()` (replaces the trigger token in `m.input` and calls `m.input.MoveToEnd()`), and `replaceTriggerToken(value, replacement)` (matches the leading `/cmd` run or the last `<sep>@<path>` run). Keystroke routing: Up/Down navigate the active popup; Tab/Enter accepts (and Enter on a popup is a selection, not a submit); Esc dismisses the active popup before falling through to `cancelTurn()`. After `m.input.Reset()` the popups are dismissed. The post-input `updateCommandSuggestions` call is replaced with `updateCompletionPopups`.
- internal/app/tui/view.go: `commandSuggestionRows` constant removed. `renderInputArea()` now calls `m.renderCompletionPopup()` instead of the legacy strip. New `renderCompletionPopup(p, width)` renders up to 8 rows, each prefixed with "▸ " (selected) or "  " (unselected), using `promptPrefixStyle` / `mutedStyle`. `inputAreaRows` accounts for the popup height.
- internal/app/tui/view_test.go: three new helpers `newViewTestModelWithRegistry`, `newViewTestModelWithFileIndex`, `newViewTestModelWithRegistryAndFileIndex` plus a `mustRegister` helper. These mirror the existing `newViewTestModel` shape and are used by the F18 integration tests.
- internal/app/tui/model_test.go: 6 new F18 tests — `TestSlashCompletionAcceptsPlan`, `TestAtFileCompletionMatchesRepoFiles`, `TestAtInsideWordDoesNotTrigger`, `TestEscDismissesPopupBeforeCancelTurn`, `TestSlashCompletionListsAllCommandsOnBareSlash`, `TestCommandTriggerDismissesFilePopup`. The pre-existing `TestSlashCommandBusyStillDispatched` was updated to use the new "Enter accepts the popup, then Enter dispatches" flow (the popup intercepts the first Enter; a second Enter dispatches the command). `TestSlashCommandHelp` / `TestSlashCommandUnknown` / `TestSlashCommandClearMessages` assertions switched back to `*Model` (the form bubbletea's interface boxing produced for those particular Update paths after the new pointer-receiver helpers were introduced).
- internal/contextpack/contextpack.go: `Pack` gains a `Pinned []FileSnippet` field that tracks accepted @file references without altering the budget-gated Sections.
- internal/contextpack/builder.go: new `PinFiles(pack, snippets)` appends each snippet as a `SectionFileSnippet` with `Priority: 100` (higher than the 30/40 of normal sections so it survives any future `Rebudget` pass) and `EstimatedTokens` set via the existing `EstimateTokens` helper. Skips empty content. Tracks the snippets on `pack.Pinned` for downstream visibility.
- internal/contextpack/contextpack_test.go: 3 new tests — `TestPinFilesAppendsSections` (section/source/priority/populated pinned list), `TestPinFilesSurvivesRebudget` (Pinned survives and full content is preserved on the appended section), `TestPinFilesSkipsEmptyContent`.
- internal/agent/runner.go: `RunTask` calls `extractPinnedFiles(goal, r.State, r.ProjectID)` after `r.mergeMemories(...)` and updates `r.State`'s context pack with `contextpack.PinFiles(pack, pinned)` before the pack is appended to the model messages. The TUI only inserts the literal "@path" text; the runner does the extraction and pinning.
- internal/agent/atfile.go: new file. Regex `(?:^|\s)@(\S+)` matches @path tokens. `extractPinnedFiles` resolves each against `state.DB().GetFileIndex(projectID)`, reads the file content from `state.WorkingDir`, dedupes by path, and silently skips unknown / unreadable references. The runner integration is the only caller.
- internal/agent/atfile_test.go: 6 tests — `TestExtractPinnedFilesFindsAndReadsKnownPaths`, `TestExtractPinnedFilesIgnoresUnknownPaths`, `TestExtractPinnedFilesNoTokensReturnsNil`, `TestExtractPinnedFilesIgnoresAtInsideEmail` (the `@` in `user@example.com` is preceded by `r`, not whitespace, so it must not match), `TestExtractPinnedFilesDeduplicates`, and the end-to-end `TestRunTaskPinsAtFileReferences` that drives a full `RunTask` and asserts the resulting `state.ContextPack().Pinned` and the Priority-100 section.
- internal/app/app.go: new `loadFileIndexPaths(database, projectID)` helper eagerly fetches the repo's file paths from the DB and is wired into the TUI model via `tui.WithFileIndex(...)` so production usage has the file index seeded at startup; failures (no DB, no project id, query error) are non-fatal — the TUI falls back to lazy population on the first @-keystroke.
## Verification
- gofmt: clean
- go vet ./...: clean
- go test -count=1 ./internal/app/tui/ -v -run 'TestFuzzy|TestCompletion|TestSlash|TestAtFile|TestEscDismiss|TestAtInsideWord|TestCommandTrigger': PASS (22 tests)
- go test -count=1 ./internal/contextpack/ -v: PASS (14 tests, including 3 new PinFiles tests)
- go test -count=1 ./internal/agent/ -run 'TestPin|TestAtFile|TestExtract|TestRunTaskPin' -v: PASS (6 tests)
- go test -race -count=1 ./internal/app/tui/ ./internal/contextpack/: PASS (no race warnings)
- go test ./...: PASS
- go build ./cmd/marshal: success
## Concerns
- The completion popup intercepts Enter (Enter is "accept" while a popup is visible). To dispatch a fully-typed command like `/help`, the user presses Enter once to accept the popup (which expands `/help` to `/help `) and Enter again to dispatch. This is a small UX shift from the legacy strip — the test `TestSlashCommandBusyStillDispatched` was updated to reflect the new two-step flow. Documented in the test comment.
- The fuzzy scorer is intentionally simple (subsequence + consecutive + prefix bonuses). It rewards prefix matches and consecutive runs; it does not understand word boundaries inside the target. Good enough for the small command set and the medium-sized repo file index. A future "smarter" scorer (e.g. fzf-style) is a follow-up.
- The `filePopup` source paths are the bare `Path` strings stored in the DB (relative to `WorkingDir`). Accepting a path inserts the literal text into the input — the runner resolves and reads the file. If the repo index contains a path the user has since deleted, the runner silently skips it; the input still carries the literal `@path` text.
- Pre-existing race in `TestGenerateTitleDoesNotOverwriteConcurrentRename` (unrelated to this task) — not introduced by F18.
- The new methods (`updateCompletionPopups`, `acceptCompletion`, etc.) are pointer-receiver. Combined with the existing `resize` and `refreshViewport` pointer-receiver methods, `Update`'s return value (typed `tea.Model` interface) sometimes boxes as `*Model` and sometimes as `Model` depending on which path through the switch was taken. The handful of pre-existing tests that asserted the now-changed form were updated to use the matching assertion (`*Model` where boxing produces a pointer). All TUI tests pass.

## Fix: preserve @ on file completion acceptance

### Summary

- Tightened `TestAtFileCompletionMatchesRepoFiles` first so accepting `@run` must produce the literal input token `@internal/agent/runner.go ` instead of merely containing `runner.go`.
- Updated `TestCompletionPopupFileKindAcceptedText` to document the same popup-level contract.
- Verified the focused model test failed before the production change with `input = "internal/agent/runner.go ", want "@internal/agent/runner.go "`.
- Fixed `completionPopup.accept` for `completionFile` by prepending `@` to the accepted path while preserving the existing trailing-space convenience for paths without spaces. Slash-command completion behavior remains unchanged.

### Tests run

- `go test -count=1 ./internal/app/tui/ -run 'TestAtFileCompletionMatchesRepoFiles|TestSlashCompletionAcceptsPlan' -v`: FAIL before fix as expected, then PASS after fix.
- `go test -count=1 ./internal/app/tui/ -run 'TestCompletionPopupFileKindAcceptedText' -v`: PASS.
- `gofmt -w internal/app/tui/completions.go internal/app/tui/completions_test.go internal/app/tui/model_test.go`: PASS.

### Commit hash

- `22c7c1f` fix(tui): preserve at-file completion trigger

## Fix: align @file completion triggers with runner extraction

### Test-first failures observed

- `go test -count=1 ./internal/app/tui/ -run 'TestAtFile|TestCompletionPopupFileKindAcceptedText|TestSlashCompletionAcceptsPlan' -v`: FAIL before fix as expected. `TestAtFileAfterPunctuationDoesNotTrigger` showed `see (@run` still opened the file popup, and `TestAtFileCompletionOmitsWhitespacePaths` showed `docs/has space.md` was offered.
- `go test -count=1 ./internal/app/tui/ -run 'TestCompletionPopupFileKindOmitsWhitespaceText' -v`: FAIL before the popup filter as expected. The popup offered `docs/has space.md`.

### Code changes

- Added model coverage proving `@` after punctuation does not trigger file completion and paths with whitespace are omitted while normal paths still match.
- Added popup coverage proving file completion items with whitespace are not offered, while normal file items remain available.
- Changed TUI `@` boundary handling to match the runner extraction contract: start-of-input or whitespace only.
- Filtered whitespace-containing file paths when building the file completion index and when updating the popup.
- Left slash command completion behavior unchanged and preserved normal non-whitespace file completion acceptance.

### Tests run

- `go test -count=1 ./internal/app/tui/ -run 'TestAtFile|TestCompletionPopupFileKindAcceptedText|TestSlashCompletionAcceptsPlan' -v`: FAIL before fix as expected, then PASS after fix.
- `go test -count=1 ./internal/app/tui/ -run 'TestCompletionPopupFileKindOmitsWhitespaceText' -v`: FAIL before popup filter as expected, then PASS via the expanded focused run.
- `gofmt -w internal/app/tui/model.go internal/app/tui/model_test.go internal/app/tui/completions.go internal/app/tui/completions_test.go`: PASS.
- `go test -count=1 ./internal/app/tui/ -run 'TestAtFile|TestCompletionPopupFileKind|TestSlashCompletionAcceptsPlan' -v`: PASS.
- `go test -count=1 ./internal/app/tui/`: PASS.
- `go test ./...`: PASS.

### Commit hash

- `28adb8e` fix(tui): align at-file completions with runner extraction
