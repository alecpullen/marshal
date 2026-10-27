# P-F the framed console — implementation plan

Date: 2026-10-27. Status: ready for review.
Base: `main` at `88fbde8f` or later. Every anchor below was checked at `88fbde8f`. Line numbers are there to find the code fast; the function names are the anchors.

Approved design: [feature-spec.md](../feature-spec.md) §3 (round 7 rows), §5.1, §5.5 (the closing row), §5.7 (the bottom border), **§5.9 (the framed console)**, §6.3 (the P-F bullet), §6.5, §7 (P-F), §8 (P-F), §9 and §10. The spec was updated on 2026-10-27 with the decisions below.
Design record and frames: [border-study.md](../border-study.md) and its colour version [border-study.html](../border-study.html). The user took the recommended option at every placement, including the drill-in frame.
Previous phase: [P-E plan](2026-10-25-p-e-tray-plan.md), whose tray this phase opens into the console.
Authoring workflow: [marshal-writing-plans](../../../internal/skills/builtin/marshal-writing-plans.md).

**How this plan is written.** No prototype was built, and the plan embeds no implementation code. The Contracts section holds declarations and the exact strings the tests pin. Every other step states its decision in prose. Executing the plan also tests how well Marshal follows a plan. "As `X` does" means `X` exists and is the model to copy.

**Goal:** the tray's rounded outline becomes the console's.
- The tray, a docked panel, the input area and the status line are drawn as one outline.
- Prompts are coloured sections of it, and the command popup is one of its sections.
- Every chrome panel is framed.
- A drilled-in child's transcript is framed in coral.
- Chrome paints no tint.

**Non-goals:**
- **History stays unframed.** `TestTranscriptIsBorderless` (`view_test.go`) keeps guarding it, and no task touches it.
- **No key changes**, and no projection, wire or web change.
- **No renames** of the tray's model methods (`boxInput`, `boxLayout`, `boxRows`, `boxTop`, `handleBoxClick`).
- **No change** to what any panel shows, beyond its frame, its title row, and the hint, count or section rows that move into its borders.

**Assumptions:**
- Every Go test command below runs with `TMPDIR=/private/tmp/pf-suite`. Create it first: `mkdir -p /private/tmp/pf-suite`. Keep the path short: a long `TMPDIR` breaks the unix-socket tests in `internal/acp` and `web/bridge`.
- **Pre-existing failures.**
  - `internal/app/tui/...` at `88fbde8f`: only `TestStatusLineUntrustedIsWarningColored` fails; the other 34 packages pass.
  - Full suite (recorded at `75ee412d` with `-timeout 20m`, the P2 plan 3a baseline; `88fbde8f` adds only docs):
    - `internal/app`: `TestSubagentChildRegistryBorrowsConversationService`, `TestPipelineRegistryCommandsCaptureDispatchWorktree`, `TestSubagentShellOutputDoesNotReachParent` and `TestDefaultPromptBudget` (tool schemas 15322 bytes against 15000);
    - `internal/app/tui`: `TestStatusLineUntrustedIsWarningColored`;
    - `internal/tools/desktop/browser`: `TestStandaloneBackendNavigateAndRead`;
    - `internal/worktree`: `TestCLIGitOpsCommitAllWithoutARepoIdentity`;
    - flaky under load, passing alone: `TestRuntimeAccessSerializesWithReloadAndQuiesce` and `TestRuntimeCloseRunsNoGitGC` (`internal/app`), and `TestRunManagedCancellationKillsTree` (`internal/snapshot`).

  A flaky test that fails in a full run counts as pre-existing only when it passes on its own with `-count=3`.
- `m.leftWidth` equals `m.width` (`resize`, `model.go:1819`), so the status line and the left column have one width. `minTerminalWidth` is 80, so `layout.PanelWidth` always returns the dock width minus 2.

**Decisions this plan makes beyond the spec** (all now recorded in the spec):
1. **The tray always opens downward.** `livebox.build` stops appending its plain closing row. At shed step 1, beside running work, the folded plan stays the tray's last row, drawn with tees as `├─ plan … ─┤` instead of `╰─ plan … ─╯`. When the folded line cannot fit (below `foldedMin`), the tray simply ends at its last section. `Layout.Open` is set when the last row is a border row. `Layout.Framed()` is false for an empty layout and for the compact row.
2. **Console headers.** The input area is a list of sections drawn by one renderer.
   - The first section's header is `╭─ label ─╮` when nothing framed is above, or `├─ label ─┤` when something is.
   - An unlabelled first section under an open border row (decision 1) draws no header.
   - Every later section draws `├─ label ─┤`, or a plain `├───┤` when it has no label.
   - "Above" is the docked panel when one is open, else the tray. The status line closes the outline (decision 6).
3. **The console's border colour** (`consoleColor`, which replaces `inputBarColor`):
   - the success colour during `successPulse`;
   - the warning colour while the sudo prompt or an approval is pending;
   - violet while the skill gate or a question is pending;
   - `theme.Current().BorderMuted` otherwise, whether focused, unfocused or during an SDD run.

   The composer's `❯` keeps its own prompt style. The status border is drawn in the same colour.
4. **Prompts are labelled sections.**
   - The approval, question, skill-gate and sudo views stop wrapping themselves in `chromeRail`. Their inner gutter glyphs (`⚠`, `?`) stay.
   - Each prompt's section header carries the Contracts' label, and on its right, `help.PromptKeys(m.footerHints())`. Sudo has no right label: its own `[↵] authenticate  [Esc] deny` row stays.
   - With the keys in the header, `footerHints()` sets `KeysInFrame`, and `help.Footer` then returns `Ctrl+C stop` alone while busy and "" otherwise for those three prompt states.
   - A patch approval writes its diff lines unpainted (no `codeSurfaceStyle`), each truncated to the view's width.
5. **The command popup is a section.** Its label is its existing title (`Commands`, `Files` or `Settings`), in the accent-secondary colour and bold, as `chrome.PanelWithHints` draws a focused title. Its right label is its existing hint text, muted. Its rows are built as `renderCompletionPopup` builds them, cut to `m.leftWidth-4`. A plain divider follows, then the composer section.
6. **The status line is the bottom border.**
   - `renderStatusLine`'s segment shedding and hint fallbacks move into `statusClusters(width)`.
   - `renderStatusLine(width)` draws the plain row from `statusClusters(width)`, as today.
   - `renderStatusBorder(width, c)` draws `╰─ <left> ─…─ <right> ─╯` from `statusClusters(width-6)`, with `chrome.BorderLine`.
   - The plain row is used only while a full-frame panel is open.
   - Shed priorities change, so the context meter outlives the `local` cue: `ctx` 3 → 2, the quota segment 3 → 2, and both `local` segments 2 → 3.
7. **Chrome frames have the gutter panel's height and content width.**
   - `chrome.Frame.Render(content, outer, h, th)` draws a frame exactly `outer` cells wide and at most `h` rows tall: the top border, then up to `h-1` content rows (`h-2` when closed), then the bottom border when closed.
   - `Panel` and `PanelWithHints` keep their signatures and call it with `outer = w + 2`. The frame's right edge takes the 2-cell margin `layout.PanelWidth` leaves, so every panel's content keeps the `w-2` cells it has today and the frame spans the dock exactly.
   - `layout.PanelWidth` does not change.
   - The one caller that passes the dock width itself, `modeloptions` (`panel.go:307–310`), switches to `layout.PanelWidth(width)`.
8. **The dock closes full-frame panels.**
   - `dock.Host.View` gives a `FullFrame` panel one row less than today's budget (`frameHeight - statusLineRows - 1`).
   - It appends `chrome.Close(view, width, chrome.FocusColor(theme.Current()))` to the panel's view before measuring. `Close` leaves a view whose last row is already a bottom border unchanged.
   - Docked panels are drawn and measured as they are: open frames.
9. **Joining a docked panel.**
   - In `viewString`, after `m.dock.View`, the model records `m.dockFramed = chrome.IsFramed(dockView)`.
   - When the tray layout is `Framed()`, the docked view is passed through `chrome.JoinTop` before it is stacked.
   - `consoleAbove()` reports `aboveFramed` for an open, framed docked panel. Otherwise it reports the tray's state: `aboveOpen` when `Layout.Open`, `aboveFramed` when `Framed()`, else `aboveNothing`.
10. **Dividers.**
    - `chrome.Divider(title)` returns a content line made of a private-use rune (U+E0D0) followed by the title.
    - `Frame.Render` draws such a line as `├─ title ─┤` in the frame's colour, and a plain `├───┤` when the title is "".
    - When the first content row is a divider with a title, it is not drawn. Its title joins the top border's left label as `<Title> · <divider title>`.
    - The session sheet emits `chrome.Divider` where it emits `chrome.Header` today.
    - `listpanel.FieldList` draws header rows as dividers when `SetGroupDividers` is on. Settings and agents turn it on only in single-column layouts, because in two columns a divider would erase the detail pane's text on its row.
11. **Panel specifics.**
    - **Access panel:**
      - its frame is amber and its title gains `⚠ `;
      - its key row moves into the header as `↑↓/Tab choose · ↵ decide · Esc deny`, with ` · PgUp/PgDn details` when its details scroll;
      - the row that held it returns to the details budget.
    - **Plugins and Skills** gain frames titled `Plugins` and `Skills`, with their list width `layout.PanelWidth(width) - 3`, as the other panels' inner width is.
    - **Settings and agents** close their own frames, with their count line as the footer, and drop the count line from their content.
12. **Drill-in frame.**
    - **Borders.** While drilled in, `renderTranscriptFrame` draws:
      - the crumb as a coral top border, with `↑/Esc to go back` muted on its right;
      - the scroll hint and the viewport lines inside coral `│ … │`, each truncated to `leftWidth-4`;
      - then a coral `╰───╯`.
    - **Budgets.** `drillCloseRows()` (1 while drilled in, else 0) joins the four height budgets and `boxTop`.
    - **Width.** `syncTranscriptWidth()` sets the viewport width to `leftWidth-4` while drilled in, and to `leftWidth` otherwise, and invalidates the transcript when the width changes.
    - **Clicks.** `transcriptLeft()` (2 while drilled in, else 0) offsets the x coordinate in `contentLineForClick` and `contentPosAt`.
13. **No chrome tint, and `raised` retired.**
    - `renderInputArea` stops band-painting with `ChromeBG`.
    - With nothing left to paint at that level, `DepthRaised`, `Theme.ChromeBG` and `Theme.OverlayBG` (neither has a production caller after this) are deleted.
    - `ParseDepth("raised")` returns `DepthFlat`, so old configs still load, and `DepthNames()` returns `flat` and `full`.
    - A stored `raised` shows as-is in `/settings` until it is changed; `listpanel`'s `cycleEnum` already handles an unlisted value.

**Execution:**
- Branch first: `git switch -c p-f-framed-console` from `main`.
- If `git status` shows the round-7 docs uncommitted (`docs/tui-simplification/feature-spec.md`, `README.md`, `border-study.md`, `border-study.html` and this plan), commit them first as explicit paths, with the message `p-f-framed-console: record the round-7 design, spec and plan`.
- Run the tasks in order. Run each task's Verify and commit it as `p-f-framed-console: task N — <title>`.
- Stage explicit paths only. Never `git add -A` without a path.
- "Delete test X" means delete the whole function and its doc comment. "Rename test X to Y" keeps its body apart from the changes named.

## Contracts

**`internal/app/tui/chrome` (Task 2).**

```go
// Frame is a panel's outline: the title and hints in the top border, the
// content inside "│ … │", and, when Closed, a bottom border carrying Footer.
type Frame struct {
	Title   string
	Hints   string      // right-aligned in the top border
	Footer  string      // the bottom border's left label; drawn only when Closed
	Closed  bool        // draw the bottom border; an open frame continues into what follows
	Color   color.Color // the border colour; nil uses FocusColor(th) when Focused, th.BorderMuted otherwise
	Focused bool
}

func (f Frame) Render(content string, outer, h int, th theme.Theme) string
func BorderLine(open, left, right, close string, w int, c color.Color) string
func Divider(title string) string
func Close(view string, w int, c color.Color) string
func JoinTop(view string) string
func IsFramed(view string) bool
func FocusColor(th theme.Theme) color.Color // th.AccentSecondary
```

- `BorderLine` draws one row of exactly `w` cells: `<open><left> ─…─ <right> ─<close>`. With `right` empty it is `<open><left> ─…─<close>`, as `livebox.borderLine` is today. The glyphs are drawn in `c`; `left` and `right` are drawn as passed (already styled).
  - When the row is too narrow for both labels, `left` is truncated with `…`, down to 12 cells.
  - Below that, `right` is dropped and `left` truncated to fit.
  - At least one fill cell always stays.
- `Close` appends `╰` + `─`×(w−2) + `╯` in `c`, unless the view's last row already starts with `╰` (visible runes, ANSI ignored).
- `JoinTop` replaces the first `╭` and the last `╮` of the view's first row with `├` and `┤`.
- `IsFramed` reports whether the first visible rune of the first row is `╭` or `├`.

**`internal/app/tui/livebox` (Task 5).**

```go
type Layout struct {
	Rows  []Row
	Shed  int      // shed steps applied, 0–6
	Stops []Target // browse stops in row order
	Open  bool     // the last row is a border row: the section below continues under it without a divider
}

func (l Layout) Framed() bool // the tray draws an outline: it has rows and is not the compact row
```

**`internal/app/tui/listpanel` (Task 4).**

```go
func FieldListSetGroupDividers(fl *FieldList, on bool) // header rows draw as chrome dividers
```

`settings/aliases.go` gains `FieldListSetGroupDividers = listpanel.FieldListSetGroupDividers`, beside `FieldListSetDescSuppressed`.

**`internal/app/tui/help` (Task 8).**

```go
// FooterHints gains:
KeysInFrame bool // the pending prompt's keys are drawn in its console header

func PromptKeys(h FooterHints) string // the key pairs Footer shows for a pending question, skill gate or approval; "" otherwise
```

**`internal/app/tui` (Tasks 6 to 9).**

```go
type consoleSection struct {
	label, right string      // the header's labels, already styled; both "" draws a plain divider
	color        color.Color // this section's border colour
	lines        []string    // content rows, drawn inside "│ … │"
}

type consoleAbove int

const (
	aboveNothing consoleAbove = iota // nothing framed above: the first header is ╭─ … ─╮
	aboveFramed                      // a framed part above ends in a content row: the first header is ├─ … ─┤
	aboveOpen                        // a framed part above ends in a border row: an unlabelled first section draws no header
)

func (m Model) inputSections() []consoleSection
func renderConsole(sections []consoleSection, above consoleAbove, w int) string
func (m Model) consoleAbove() consoleAbove
func (m Model) inputHeaderRows() int
func (m Model) consoleColor() color.Color
func (m *Model) composerView() string // renames gutteredInput; no bar
func (m Model) statusClusters(width int) (left, right string)
func (m Model) renderStatusBorder(width int, c color.Color) string
func (m Model) drillCloseRows() int
func (m Model) transcriptLeft() int
func (m *Model) syncTranscriptWidth()

// Model gains:
dockFramed bool // the docked panel's last view was framed (decision 9)
```

**Strings the tests pin:**

| Where | Text |
|---|---|
| Approval section label | `⚠ Approval` (warning colour) |
| Question section label | `? Question` (violet) |
| Skill gate section label | `? Skill gate` (violet) |
| Sudo section label | `⚠ Sudo password` (warning colour) |
| Prompt keys | exactly the pairs `help.Footer` shows today for that state, e.g. `Enter answer · Esc skip` |
| Access panel title and hints | `⚠ ` + its title; `↑↓/Tab choose · ↵ decide · Esc deny` (+ ` · PgUp/PgDn details`) |
| Drill border right label | `↑/Esc to go back` |
| Plugins and Skills frame titles | `Plugins`, `Skills` |
| Depth names | `flat`, `full`; settings description `background planes the TUI paints (flat · full)` |

## Task 1: No chrome tint, and `raised` retired

**Goal:** the input area paints no background at any depth, and `raised` is an alias of `flat` (decision 13).

**Files:** `internal/app/tui/view.go`, `status.go`, `theme/depth.go`, `theme/depth_test.go`, `theme/contrast_test.go`, `depth_golden_test.go`, `frame_invariants_test.go`, `view_test.go`, `settings/frames_tui.go`.

**Steps:**

1. **`renderInputArea` (`view.go:204`).** Both returns stop calling `chrome.PaintBand(…, theme.Current().ChromeBG())` (lines 216 and 272) and return the joined rows as they are.
2. **`theme/depth.go`.**
   - Delete `DepthRaised`, `ChromeBG` and `OverlayBG`, with their doc comments.
   - The ladder comment describes two levels: `flat` paints nothing, and `full` paints the transcript with `BGBase`.
   - `ParseDepth` maps `"raised"` to `DepthFlat`, with a comment: retired in P-F, when the input band, the only thing it painted, went.
   - `String` and `DepthNames` cover `flat` and `full`.
3. **`status.go:182–186`.** The comment explains that the status line is the console's bottom border or a plain row, with no tint, and stops naming `ChromeBG`.
4. **`settings/frames_tui.go:32`.** The description is the Contracts' text.
5. **Tests.**
   - **`theme/depth_test.go`:**
     - `TestParseDepth`'s `raised` cases expect `DepthFlat`;
     - `TestDepthStringRoundTrips` and `TestDepthNamesMatchParse` loop over flat and full;
     - `TestBackgroundAccessorsByDepth` keeps only its transcript column, for flat and full;
     - `TestAccessorsReturnTheRealSlots` keeps only its `TranscriptBG` check;
     - `TestDepthClampsBelowTier256`, `TestRawSlotsSurviveEveryDepth`, `TestLoadWithConfigStampsDepth`, `TestLoadWithConfigClampsBelow256` and `TestOverridesPreserveDepth` use `DepthFull` where they used `DepthRaised`, and `TranscriptBG` where they read `ChromeBG`;
     - delete `TestOverlayBGFollowsDepth`.

     `TestOverlayIsLightestPlane` and `TestAllPresetsDefineOverlay` read the `BGOverlay` slot and stay.
   - **`theme/contrast_test.go:145`:** loop over flat and full.
   - **`depth_golden_test.go`:** delete `TestRaisedPaintsChrome`, `TestRaisedPreservesFrameGeometry` and `TestRaisedBandsAreFullWidth`. `TestFullPaintsTranscript` compares flat with full.
   - **`frame_invariants_test.go:184`:** loop over flat and full.
   - **Add `TestInputAreaPaintsNoBackground` (`view_test.go`).** At `DepthFull`, idle and with an approval pending, `renderInputArea()` contains no `48;` SGR parameter. Model the theme swap on `newDepthFrame`'s `renderAt`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/theme/ ./internal/app/tui/ ./internal/app/tui/settings/ -run 'Depth|Overlay|Contrast|Full|InputArea|FrameInvariants|Accessor|Slots|Clamp|Overrides|Planes' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/... ./internal/app/config/...
grep -rn 'DepthRaised\|ChromeBG\|OverlayBG' --include='*.go' internal/ ; echo "exit $?"
```

Expected: the named tests pass, the packages fail only on the Assumptions list, and the grep prints nothing (exit 1).

## Task 2: Chrome frames, and the dock closes full-frame panels

**Goal:** every `chrome.Panel` and `PanelWithHints` caller draws a frame. Docked panels are open frames, and full-frame panels are closed by the dock. Heights and content widths do not change.

**Files:**
- `internal/app/tui/chrome/chrome.go`, a new `chrome/frame.go`, and their tests;
- `internal/app/tui/dock/dock.go` and its test;
- `internal/app/tui/modeloptions/panel.go`;
- tests: `view_test.go`, `smoke_pickers_test.go`, `settings/nocolor_test.go`.

**Steps:**

1. **`chrome/frame.go`** holds the Contracts' `Frame`, `BorderLine`, `Divider`, `Close`, `JoinTop`, `IsFramed` and `FocusColor`.
   - **Top border.** `Frame.Render` draws it with `BorderLine("╭─ ", title, hints, "╮", outer, c)`. The title is styled as `PanelWithHints` styles it today: bold accent-secondary when focused, muted otherwise, with no bold in monochrome. The hints are muted.
   - **Content.** It splits the content into lines and keeps at most `h-1` (`h-2` when `Closed`). It draws each line as `│ ` + the line truncated and padded to `outer-4` + ` │`, with the borders in `c`. A divider line is drawn as decision 10 says.
   - **Footer.** When `Closed`, it draws the bottom border as `BorderLine("╰─ ", footer, "", "╯", outer, c)`, muted footer, or a plain `╰───╯` when `Footer` is "".
2. **`chrome.go`.**
   - `PanelWithHints` becomes `Frame{Title: title, Hints: hints, Focused: focused}.Render(content, w+2, h, th)`, and `Panel` calls it with no hints.
   - The doc comments describe the frame, the `w+2` rule (decision 7), and the unchanged height rule: the title row is the top border.
   - `isMonochrome` stays in use for the title style.
3. **`dock.go`.**
   - `Host.View`, for a `FullFrame` panel, passes `max(frameHeight-statusLineRows-1, 1)`.
   - It replaces the view with `chrome.Close(view, width, chrome.FocusColor(theme.Current()))` before `h.rows` is measured.
   - The `dock` package imports `chrome` and `theme`; neither imports `dock`.
4. **`modeloptions/panel.go:306–310`.** `View` computes `pw := layout.PanelWidth(width)`, sizes the fields to `pw-3`, and passes `pw` to `PanelWithHints`.
5. **Tests.**
   - **`chrome_test.go`:**
     - `TestPanelRendersGutterFrame` becomes `TestPanelRendersOpenFrame`: the first row starts `╭─ ` and ends `╮`, every other row starts `│ ` and ends ` │`, every row is `w+2` cells, and there is no `╰` row.
     - `TestPanelWithHintsRightAligns` checks that the hints end two cells before the top border's `╮`.
     - `TestPanelTruncatesToHeightBudget` keeps its budget.
   - **Add to `chrome` (`frame_test.go`):**
     - `TestFrameClosedDrawsTheFooter`: the last row starts `╰─ 3 items ` and ends `╯`, and the content keeps `h-2` rows.
     - `TestDividerDrawsAcrossTheFrame`: a `Divider("Agent")` line renders as a row starting `├─ Agent ` and ending `┤`, the same width as the others.
     - `TestFirstRowDividerJoinsTheTopBorder`: content starting with `Divider("▸ CONTEXT")` renders a top border starting `╭─ Session · ▸ CONTEXT `, and no `├─ ▸ CONTEXT` row.
     - `TestBorderLineKeepsTheRightLabel`: at 40 cells, a 60-cell left label is truncated with `…` and the right label stays. At 20 cells the right label is dropped. Every result is exactly the width.
     - `TestCloseAddsOneBorderOnce`: `Close` adds a `╰…╯` row to an open frame and leaves a closed one unchanged.
     - `TestJoinTopMakesTees`: the first row starts `├` and ends `┤`, and no other row changes.
     - `TestIsFramed` covers a framed view, a joined view and plain text.
     - `TestFrameMonochromeEmitsNoEscapes` uses the NO_COLOR theme, as `settings/nocolor_test.go` sets it up.
   - **`dock_test.go`:** add `TestFullFrameViewIsClosedInItsBudget`. A stub `FullFrame` panel that fills its `maxHeight` with an open `PanelWithHints` makes `Host.View` return `frameHeight-statusLineRows` rows, ending in a `╰` row.
   - **`view_test.go`:** `TestPickerRendersDockedAboveInput` and `TestConnectRendersDockedAboveInput` look for a panel row starting `╭─ ` where they looked for `▍`, and their double-rail check becomes "no row holds two `╭`".
   - **`smoke_pickers_test.go`:** `TestSmokePickersProgrammatic` expects `╭─ ` where it expects `▍`.
   - **`completions_test.go`:** `TestCompletionPopupShowsSlashPrefix` expects `╭─ ` where it expects the `▍` gutter. The popup is still a `PanelWithHints` until Task 7 makes it a console section.
   - **`settings/nocolor_test.go`:** `TestViewUsableWhenColorStripped` expects `│` where it expects `▍`.
   - Any other failure in this task is a test that read the `▍` panel gutter. Fix it the same way, and name it in the commit message.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/chrome/ ./internal/app/tui/dock/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
```

Expected: `chrome` and `dock` pass, and the TUI packages fail only on the Assumptions list.

## Task 3: The session sheet and the access panel

**Goal:** the sheet's sections are dividers, and the access panel is an amber frame with its keys in the header (decisions 10 and 11).

**Files:** `internal/app/tui/sessionsheet/panel.go`, `internal/app/tui/accesspanel/accesspanel.go`, `accesspanel/accesspanel_test.go`, and `sessionsheet_wiring_test.go`.

**Steps:**

1. **`sessionsheet/panel.go` `View`.**
   - Line 156 appends `chrome.Divider(title)` where it appends `chrome.Header(title, "", inner)`. The empty-titled stats section therefore becomes a plain divider.
   - The rest of `View` (budget, sliding window, `PanelWithHints` call) is unchanged. A merged first divider simply draws one row fewer.
2. **`accesspanel/accesspanel.go` `View` (lines 126–187).**
   - The title gains the `⚠ ` prefix.
   - `detailRows` stops reserving the hint row: drop the final `-1` in `max(0, maxHeight-1-len(rows)-actionRows-1)`.
   - The `hints` string becomes the Contracts' text, keeping its PgUp/PgDn extension. It is not appended to `rows`.
   - The return becomes `chrome.Frame{Title: title, Hints: hints, Color: theme.Current().StatusWarning, Focused: true}.Render(strings.Join(rows, "\n"), pw+2, maxHeight, theme.Current())`.
   - `inner := max(1, pw-2)` stays (decision 7).
3. **Tests.**
   - **`accesspanel_test.go`:**
     - `TestAccessPanelFitsBudgetsAndMonochrome` expects `↵ decide` where it expects `Enter decide`. Its width and height bounds hold unchanged: it sweeps 80, 120 and 200 columns, where `pw+2` is exactly the width;
     - add `TestAccessPanelHeaderCarriesTheKeys`: the first row starts `╭─ ⚠ Allow editing?`, holds `↵ decide`, and no later row holds `Esc deny`.
   - **`sessionsheet_wiring_test.go`:** the `▸ CHANGED` checks (lines 104 and 110) still hold, because a merged first divider keeps its text in the top border. Add `TestSheetSectionsAreDividers`: with the sheet open and nothing in the tray, at least one row after the sheet's top border starts `├─ ` followed by an upper-case section name, and no row that starts `│ ` holds a `──` rule.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/accesspanel/ ./internal/app/tui/sessionsheet/ ./internal/app/tui/ -run 'Access|Sheet|Session' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
```

## Task 4: Group dividers, and the settings, agents, plugins and skills frames

**Goal:** single-column settings and agents lists draw their groups as dividers and their counts in the bottom border, and Plugins and Skills are framed (decisions 10 and 11).

**Files:** `internal/app/tui/listpanel/fieldlist.go`, `listpanel/field.go`, `settings/aliases.go`, `settings/browser.go`, `agents/panel.go`, `plugins/panel.go`, `skills/panel.go`, and their tests.

**Steps:**

1. **`listpanel`.**
   - `FieldList` gains `groupDividers bool`, beside `descSuppressed`, with a setter.
   - `FieldListSetGroupDividers` lives in `field.go` beside `FieldListSetDescSuppressed` (line 291).
   - In `View` (`fieldlist.go:596`), a `KindHeader` row appends `chrome.Divider(row.Title)` when `groupDividers` is on, and today's styled, padded line otherwise.
2. **`settings/aliases.go`** gains the alias.
3. **`settings/browser.go` `View` (lines 828–897).**
   - Beside line 839, call `FieldListSetGroupDividers(b.activeList(), !twoCol)`.
   - `content` no longer appends the footer line.
   - The return becomes `chrome.Frame{Title: title, Hints: hints, Footer: footer, Closed: true, Focused: true}.Render(content, panelWidth+2, panelHeight, settingsTheme())`, with `panelHeight := min(lipgloss.Height(content)+2, maxHeight)`.
   - The `Confirm model limits` branch (line 825) is unchanged; the dock closes it.
4. **`agents/panel.go` `View` (lines 721–783).**
   - Beside line 741, call `settings.FieldListSetGroupDividers(l, !twoCol)`.
   - The footer moves into a closed `Frame` exactly as in settings, with the same height rule.
   - The drilled-in frame's `chrome.Header` line (line 757) stays.
5. **`plugins/panel.go` (640) and `skills/panel.go` (537).**
   - `pw := layout.PanelWidth(width)`, and the list is sized to `pw-3`.
   - The view is `chrome.PanelWithHints("Plugins", "", content, pw, min(lipgloss.Height(content)+1, maxHeight), true, theme.Current())`, and `"Skills"` in the other.
   - The list height stays `maxHeight-4`: the top border takes the row the content left spare.
6. **Tests.**
   - **`listpanel`:** add `TestGroupDividersDrawHeaderRowsAsDividers`. With dividers on, a header row's line is `chrome.Divider(title)`; off, it is not.
   - **`settings`:** add `TestBrowserGroupsAreDividersInOneColumn`. At 80 columns, a rendered row starts `├─ Agent`, and the last row starts `╰─ ` and holds `settings`. At 140 columns (two columns) no `├─ Agent` row appears.
   - **`agents`:** add `TestAgentsCountIsInTheBottomBorder`: the last row starts `╰─ ` and holds `entries`.
   - **`plugins` and `skills`:** add `TestPluginsPanelIsFramed` and `TestSkillsPanelIsFramed`. The first row starts `╭─ Plugins` (or `╭─ Skills`), and the view is no taller than `maxHeight`.
   - Any test that looked for the count as a content row or for a bold group row: fix it, and name it in the commit message.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/listpanel/ ./internal/app/tui/settings/ ./internal/app/tui/agents/ ./internal/app/tui/plugins/ ./internal/app/tui/skills/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
```

## Task 5: The tray opens downward

**Goal:** the tray draws no closing row, a folded plan is its last border row, and the layout says how it ends (decision 1).

**Files:** `internal/app/tui/livebox/box.go`, `livebox/plan.go`, `livebox/livebox.go`, `livebox/box_test.go`, `livebox/plan_test.go`, `livebox/testdata/`.

**Steps:**

1. **`borderLine` (`box.go:223`)** becomes a call to `chrome.BorderLine(open, label, "", close, w, theme.Current().BorderMuted)`, and its body goes. `borderStyle` stays for `planFolded`.
2. **`build` (`box.go:119`).**
   - Delete the plain `bottom` row (line 162) and the final append (line 195).
   - In the folded case (line 167), the folded plan is appended as the last row with `Frame: FrameBorder`, but only when `planFolded` returns a labelled line.
3. **`planFolded` (`plan.go:310`)** draws `├─ plan ` and closes with ` ─┤`, where it draws `╰`/`╯` today. Where it would return the plain border (no plan, or narrower than `foldedMin`), it returns "", and `build` appends nothing.
4. **`Layout`.** Add `Open`, set in `Plan` after `finish`: true when the last row's `Frame` is `FrameBorder`. Add `Framed()` (Contracts). Update the package and `Render` doc comments: the tray's outline is open at the bottom, and the console continues it.
5. **Tests.**
   - **`box_test.go`:**
     - `TestIdlePlanDrawsItsRows` (line 152) asserts the last line is the plan's last row (`│ ✓ todo b`), not a border.
     - `TestShedOrder`: the unshed row count drops by one; shed steps 1 to 6 keep their counts. Step 1's last row starts `├─ plan` (was `╰─ plan`), and `byShed[1].Open` is true.
     - `TestFrameOutlinesTheTray` becomes `TestTrayOpensAtTheBottom`: the top border as today, every later row except `├─ plan` inside the outline, no `╰` row, and `Plan(busyInput()).Open` false.
     - `TestCompactRowHasNoOutline` adds `Framed()` false.
   - **Add `TestFinishedPlanBesideWorkLeavesTheTrayOpen`:** with running work and a finished plan, the last row is the plan's header and `Open` is true.
   - **`plan_test.go`:** any folded-border test expects `├─ plan` and ` ─┤`. The narrow case expects "".
   - **Goldens.** Regenerate with `-update` and review every frame: each loses its bottom border, and nothing else changes.
   - **Integration.** If P2 plan 3a has merged, also regenerate `internal/app/tui/testdata/transcript/live*.golden`, whose scene renders `livebox.Render(livebox.Plan(m.boxInput()), …)`, and check that only the closing row went.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/livebox/ -v 2>&1 | grep -E '^(--- FAIL|ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
```

Expected: `livebox` passes. The TUI packages fail only on the Assumptions list. A test outside `livebox` that read the tray's `╰` row is fixed and named in the commit message.

## Task 6: The status border and the new shed priorities

**Goal:** the status line can be drawn as a bottom border, and `local` sheds before `ctx` (decision 6). Nothing draws the border yet; Task 7 wires it in.

**Files:** `internal/app/tui/status.go`, `status_test.go`.

**Steps:**

1. **`statusClusters(width)`** holds `renderStatusLine`'s body from line 117 up to the gap computation (line 176): the segments, the right cluster, the hint fallbacks and the shedding loop, run against `width`. It returns the joined left and the right cluster.
2. **`renderStatusLine(width)`** calls it and keeps its gap, padding and `statusBarStyle` rendering as today.
3. **`renderStatusBorder(width, c)`** returns `chrome.BorderLine("╰─ ", left, right, "╯", width, c)` with `left, right := m.statusClusters(width - 6)`.
4. **Priorities (`statusLeftSegments`).**
   - `local` (both appends, lines 290 and 331) becomes 3.
   - `ctx` (line 341) becomes 2.
   - The quota segment (line 364) becomes 2, and its comment keeps the "drops together with the context counter" reason.
   - The priority list in the doc comment (line 250) reads `…route=1, send now=1, ctx=2, quota=2, local=3, ±files=4…`.
5. **Tests.**
   - `TestStatusLineDropsLowPrioritySegment` becomes `TestStatusLineDropsLocalBeforeCtx`. At width 55 the route stays, `ctx` stays and `local` is gone. Its comment explains the new order.
   - Add `TestStatusBorderIsExactlyTheWidth`: at 80, 96 and 120 columns, idle and busy, the border starts `╰─ `, ends `─╯`, and is exactly the width.
   - Add `TestStatusBorderShedsLikeTheStatusLine`: the border's left text equals `statusClusters(width-6)`'s left, and at 80 columns busy it still holds the mode cue and `Ctrl+C stop`.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/ -run 'StatusLine|StatusBorder|TurnState|Footer' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
```

Expected: the named tests pass apart from the baseline's `TestStatusLineUntrustedIsWarningColored`.

## Task 7: The console

**Goal:** the input area is drawn as console sections under the tray or docked panel, the popup is one of them, and the status line closes the outline (decisions 2, 3, 5 and 9). Prompts are still the composer section's content; Task 8 gives them their own sections.

**Files:**
- `internal/app/tui/view.go`, `model.go`, `livebox_view.go`, and a new `console.go`;
- tests: `view_test.go`, `view_suggest_test.go`, `input_area_test.go`, `completions_test.go`, `frame_invariants_test.go`, and a new `console_test.go`.

**Steps:**

1. **`console.go`** holds the Contracts' `consoleSection`, `consoleAbove` and `renderConsole`.
   - `renderConsole` draws each section's header by decision 2, with `chrome.BorderLine` in the section's colour (`├─ `/`╭─ ` open, `┤`/`╮` close). Then it draws each line as `│ ` + the line truncated and padded to `w-4` + ` │` in that colour.
   - With no sections it returns "".
   - It never draws a bottom border.
2. **`consoleColor()`** replaces `inputBarColor` (`view.go:278`) with decision 3's order. Delete `inputBarColor`.
3. **`composerView()`** replaces `gutteredInput` (`view.go:300`): the same placeholder and ghost handling, with no `▍` prefix.
4. **`inputSections()`**, in the order `renderInputArea` checks its states today:
   - **Steering composer:** one unlabelled section with the muted notice line.
   - **Sudo, skill gate, question and approval:** for now, one unlabelled section whose lines are the prompt's view lines, exactly as today. Task 8 labels them.
   - **Access request owned by the access panel:** no sections. A queued one is one unlabelled section with `accessWaitingRow`.
   - **Editing a command:** one unlabelled section with `m.input.View()`.
   - **Otherwise:**
     - the popup section when a popup is active (decision 5), built by `completionSection()`, which replaces `renderCompletionPopup` (`view.go:511`) and keeps its row-building code;
     - then one unlabelled section: the SDD hint line when an SDD run is active, then the composer's lines.

   Every section's colour is `consoleColor()`.
5. **`renderInputArea()`** returns `renderConsole(m.inputSections(), m.consoleAbove(), m.leftWidth)`.
6. **`consoleAbove()`** follows decision 9. It reads `m.boxLayout()` for the tray.
7. **`inputHeaderRows()`** counts the header rows `renderConsole` draws for `inputSections()` and `consoleAbove()`, minus one when a popup section is present (its header is the `completionPanelChromeRows` row `inputChromeRows` already counts). Share the "does section i draw a header" rule with `renderConsole` in one helper.
8. **`inputChromeRows()` (`model.go:3512`)** adds `m.inputHeaderRows()` at the end. Its doc comment names the console headers.
9. **`resize` (`model.go:1825`).** `m.input.SetWidth(max(m.leftWidth-4, 1))`, with the comment explaining that `│ ` and ` │` take 4 cells.
10. **`viewString` (`view.go:71`).** The non-full-frame branch stacks:
    - the transcript frame;
    - the tray (`livebox.Render`);
    - the docked view: record `m.dockFramed = chrome.IsFramed(dockView)` right after `m.dock.View`, and pass the view through `chrome.JoinTop` when `layout.Framed()`;
    - `renderInputArea()`.

    The status row is `m.renderStatusBorder(m.width, m.consoleColor())`. The full-frame branch keeps `m.renderStatusLine(m.width)`. Add the `dockFramed` field to `Model`.
11. **Tests.**
    - **`view_test.go`:**
      - `TestInputAreaHasNoBorderBox` becomes `TestInputAreaIsFramed`: idle at 80×24, the input area's first row starts `╭` and ends `╮`, the second starts `│ ❯`, and nothing holds `▍`;
      - `TestInputAreaHasNoBlankRowsWhenIdle` counts rows holding `│ ❯`;
      - `TestInputWrapsBeforeTerminalWidth` checks the text after `│ ❯`;
      - `TestTranscriptFrameDoesNotMoveWhenActivityStarts`: the row at `inputTop` is the section's top border (starts `╭`), and the next holds `│ ❯`;
      - `TestInputBarPulsesTealOnSuccess` becomes `TestConsoleBorderPulsesTealOnSuccess`: `consoleColor()` is the success colour during the pulse and `BorderMuted` after, and the two renders differ;
      - `TestTodoPanelIsPinnedBelowTranscript` finds the input row by `│ ❯`.
    - **`view_suggest_test.go`:** call `composerView`. Expected first rows lose the `glyph.Rail` prefix.
    - **`input_area_test.go`:**
      - `TestInputChromeRowsExcludesTextarea`: idle chrome is 1 (the top border), and `inputAreaRows` is 1 plus the input's height;
      - `TestDynamicMaxHeightBudget`: 34.
    - **`completions_test.go`:**
      - `TestCompletionPopupShowsSlashPrefix` and `TestCompletionPopupTitleFollowsPopupKind` read `completionSection()`'s label and lines where they call `renderCompletionPopup`, and the first drops Task 2's `╭─ ` check;
      - `TestInputChromeRowsCountsPopupTitleRow` adds 1 for the composer's divider, with no tray, so the popup is the first section.
    - **Add `console_test.go`:**
      - `TestConsoleOpensWithNothingAbove`: idle, the input area starts `╭───`.
      - `TestConsoleJoinsTheTray`: with a running item, the frame has no `╰` row above the input. The tray's last row is followed by `├───┤`, then a row starting `│ ❯`. The last frame line starts `╰─ ` and ends `╯`.
      - `TestConsoleContinuesUnderAnOpenTray`: with running work and a finished plan (`Layout.Open`), the row after the plan's header starts `│ ❯`, with no plain divider between.
      - `TestPopupIsAConsoleSection`: typing `/` with a running item shows a row starting `├─ Commands`, then the matches, then `├───┤`, then `│ ❯ /`.
      - `TestDockedPanelJoinsTheOutline`: with a running item and the sheet open, the sheet's first row starts `├─ Session`, and the row after the sheet's last row is `├───┤`.
      - `TestFullFrameKeepsThePlainStatusLine`: with `/settings` open, the last line does not start `╰─`, and holds the mode cue.
    - **`frame_invariants_test.go`:** add variants for an open popup, the sheet under a running tray, a pending question, a pending approval, and the access panel. The existing assertions (no doubled rail, no row wider than the frame, height) hold for each.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/ -run 'Console|InputArea|Input|Popup|Completion|Ghost|Guttered|Composer|FrameInvariants|Geometry|Transcript|Todo|Picker|Connect' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
go vet ./internal/app/tui/...
grep -n 'inputBarColor\|gutteredInput\|renderCompletionPopup\|ChromeBG' internal/app/tui/*.go ; echo "exit $?"
```

Expected: the named tests pass, the package fails only on the Assumptions list, and the grep prints nothing (exit 1).

## Task 8: Prompt sections

**Goal:** approvals, questions, the skill gate and sudo are labelled, coloured sections with their keys in the header, and the `▍` rail is gone (decision 4).

**Files:**
- `internal/app/tui/console.go`, `approval.go`, `question.go`, `skillgate.go`, `sudoauth.go`, `transcript.go`, `view.go`, `status.go`, `help/help.go`;
- tests: `approval_test.go`, `question_test.go`, `view_test.go`, `help/help_test.go`, `console_test.go`.

**Steps:**

1. **Views without the rail.**
   - `approvalModel.View` (`approval.go:331`), `questionModel.View` (`question.go:268`), `skillGateModel.View` (`skillgate.go:71`), the sudo prompt's view (`sudoauth.go`, the function ending at line 133) and `renderQuestionPanel` (`transcript.go:1271`) return their lines without calling `chromeRail`.
   - Where a view computes its wrap width as `width-4` "for the ▍ rail cell plus the 3-cell gutter" (question.go:275, skillgate.go:74, sudoauth.go:75), it becomes `width-3`. Fix each comment.
2. **The patch diff (`approval.go:337–352`).** Each diff line is written truncated to `am.width` with `ansi.Truncate`, with no `codeSurfaceStyle`. Update the comment.
3. **`help/help.go`.**
   - `PromptKeys(h)` returns the segments `Footer`'s question, skill-gate and approval branches build today (lines 98–118), including the send-now pair, joined as `Footer` joins them. It returns "" when none of those states holds, or while editing a command.
   - Those three branches of `Footer` call it.
   - `FooterHints` gains `KeysInFrame`. At the top of `Footer`, after the `StopOnly` case: when `KeysInFrame` is set and `PromptKeys(h)` is not empty, return `pair("Ctrl+C", "stop")` when `Busy`, else "".
4. **`footerHints()` (`status.go:551`)** sets `KeysInFrame: !m.dock.FullFrameOpen()`.
5. **`inputSections()`.** The four prompt states get the Contracts' labels, the right label `help.PromptKeys(m.footerHints())` (none for sudo), and `consoleColor()`.
6. **Delete** `chromeRail` and `chromeRailWidth` (`view.go:448–484`).
7. **Tests.**
   - `TestApprovalViewHasChromeRail` becomes `TestApprovalViewHasNoRail`: no line starts with `▍`.
   - `TestQuestionModelViewUsesGutter` expects the prefix ` ? `.
   - `TestQuestionModelViewHasChromeRail` becomes `TestQuestionModelViewHasNoRail`.
   - Delete `TestChromeRailWidthPaintsNoBackground` and `TestChromeRailWidthTruncatesToWidth` (`view_test.go:952` and `968`).
   - **`help_test.go`:**
     - add `TestPromptKeysMatchTheFooter`: for a question, a skill gate and an approval, `PromptKeys(h)` equals `Footer(h)`;
     - add `TestFooterKeysInFrame`: with `KeysInFrame` and a question pending, `Footer` is exactly the `Ctrl+C stop` pair while busy and "" idle, and unchanged for states with no prompt.
   - **`console_test.go`:**
     - add `TestPromptSectionsCarryLabelsAndKeys`: with a pending approval, the input area's first row starts `├─ ⚠ Approval` (with a tray) or `╭─ ⚠ Approval` (without) and holds `←→ choose`; with a pending question it holds `? Question` and `Enter answer`;
     - add `TestStatusLineDropsPromptKeysWhileFramed`: with a question pending and busy, the status border's right label is `Ctrl+C stop`.
   - Any other test that read a prompt's `▍` rail: fix it, and name it in the commit message.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/ ./internal/app/tui/help/ -run 'Approval|Question|SkillGate|Sudo|Prompt|Footer|Console|ChromeRail' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
grep -n 'chromeRail' internal/app/tui/*.go ; echo "exit $?"
```

Expected: the named tests pass, the package fails only on the Assumptions list, and the grep prints nothing (exit 1).

## Task 9: The drill-in frame

**Goal:** a drilled-in child's transcript is framed in coral, with the crumb in the top border (decision 12).

**Files:** `internal/app/tui/view.go`, `model.go`, `click.go`, `selection_input.go`, `livebox_view.go`, `commands_dispatch.go`, `drill_test.go`, `view_test.go`.

**Steps:**

1. **`renderTranscriptFrame` (`view.go:171`).** While drilled in (`m.drilledInto()`):
   - the crumb text is built as today, without `  (↑/Esc to go back)`;
   - the top border is `chrome.BorderLine("╭─ ", crumb, mutedStyle().Render("↑/Esc to go back"), "╮", m.leftWidth, accentColor)`;
   - the scroll hint (when shown) and every viewport line are drawn as `│ ` + the line truncated and padded to `m.leftWidth-4` + ` │`, in `accentColor`;
   - then `╰` + `─`×(leftWidth−2) + `╯` in `accentColor`.

   The whole block is band-painted with `TranscriptBG` as today. Not drilled in, the function is unchanged.
2. **`drillCloseRows()`** is added beside `breadcrumbRows` (`model.go:4093`). Subtract it in `resize` (line 1830), `maxInputHeight` (3600), `updateViewportHeight` (3622) and `boxMaxRows` (`livebox_view.go:304`), and add it in `boxTop` (`livebox_view.go`). `breadcrumbRows` keeps meaning "rows above the viewport".
3. **`syncTranscriptWidth()`.**
   - `resize` calls it in place of `m.viewport.SetWidth(max(m.leftWidth, 1))` (line 1828).
   - `drillIntoSubagent` and `popDrill` (`model.go:4053`, `4079`) call it after changing `m.viewStack`, and so does the session swap after `m.viewStack = nil` (`commands_dispatch.go:230`).
4. **`transcriptLeft()`.**
   - `contentLineForClick` (`click.go:40`) accepts `x` in `[left, left+m.viewport.Width())`.
   - `contentPosAt` (`selection_input.go:17`) maps `x` to `x-left`, clamped to `[0, m.viewport.Width()-1]`.
   - Update both doc comments.
5. **Tests.**
   - **`drill_test.go`:**
     - `TestClickBeatWhileDrilledIntoSubagent` clicks at `x=3` (inside the border) where it clicks at 1;
     - `TestBreadcrumbShowsPathWhileDrilled` keeps its substrings, which still hold;
     - add `TestDrillFrameDrawsTheCrumbInTheBorder`: the transcript frame's first row starts `╭─ ` and holds `explore repo` and `↑/Esc to go back`, its rows start `│ `, and its last row starts `╰`;
     - add `TestDrillNarrowsTheTranscript`: after `drillIntoSubagent` the viewport is `leftWidth-4` wide, and after `popDrill` it is `leftWidth` again;
     - add `TestDrillClickOffsetsTheBorder`: drilled in, `contentLineForClick(1, y)` misses for a row inside the viewport, `(2, y)` hits, and `contentPosAt(2, y).col` is 0.
   - **`view_test.go`:** add `TestDrilledFrameFitsTheTerminal` at 80×24 and 120×40. The frame is the terminal's height, the tray's top border sits directly under the drill frame's `╰` row, and no row is wider than the width. The existing `todos+lane+drilled` variant of `TestFrameInvariants` must still pass, although it appends to `viewStack` without syncing the width; the per-line truncation in step 1 covers it.

**Verify:**

```bash
cd /Users/alecpullen/projects/marshal
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/ -run 'Drill|Breadcrumb|Click|Selection|FrameInvariants|Fits|Geometry' -v 2>&1 | grep -E '^(--- |ok|FAIL)'
TMPDIR=/private/tmp/pf-suite go test ./internal/app/tui/...
```

## Task 10: Verification, docs and changelog

**Goal:** the phase is verified by the suite and in a real session, and the docs describe what shipped.

**Files:** `README.md`, `CHANGELOG.md`, `docs/tui-simplification/feature-spec.md`.

**Steps:**

1. **Full verification.**

   ```bash
   cd /Users/alecpullen/projects/marshal
   TMPDIR=/private/tmp/pf-suite CGO_ENABLED=1 go test -timeout 20m ./... 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'
   go vet ./... && gofmt -l .
   ```

   Expected: every failure is on the Assumptions list. vet is clean, and `gofmt -l .` prints nothing.
2. **Mutation check.** Revert decision 2's "no header under an open border row" rule locally, and confirm `TestConsoleContinuesUnderAnOpenTray` fails. Restore the rule.
3. **A real session.**
   - Build with `CGO_ENABLED=1 go build -o /private/tmp/pf-suite/marshal ./cmd/marshal`.
   - In a scratch git repository with an isolated config (as `test/usability` sets one up), ask for a short plan, a background command (`sleep 60; echo done`) and a subagent task. Then switch to `/ask` and ask for a shell command.
   - Confirm:
     - the idle input sits in a `╭───╮` frame, and the status line is its bottom border;
     - while the tray is up there is one outline, from the tray's top border to the status line;
     - typing `/` adds a `Commands` section;
     - Ctrl+B joins the sheet into the outline, with `├─ NAME ─┤` sections;
     - an approval or the access panel turns its section amber, with its keys in the header;
     - a question is a violet section;
     - `/settings` is a closed frame with its count in the bottom border, over a plain status line;
     - Ctrl+F frames the child in coral, Esc returns, and a click in the child's transcript lands on the row under the pointer;
     - no chrome row is tinted at `tui.depth = "raised"`.
   - Record what was seen in the commit message. If something differs, record it and do not change code in this task.
   - Delete the binary afterwards.
4. **README (`### The TUI`, line 196).** After the tray sentence, add: the tray, any docked panel, the input and the status line are drawn as one framed outline; approvals and questions turn their part of it amber or violet; and a subagent you drill into is framed in coral.
5. **CHANGELOG.** Under `## [Unreleased]`, add a `### Changed` group `**The framed console**`:
   - one bullet for the outline and the status line as its bottom border;
   - one for prompts as coloured sections with their keys;
   - one for framed panels and the coral drill-in frame;
   - one for the input's tint going, with `tui.depth = "raised"` now meaning `flat`.
6. **Spec.** The status line says P-F landed, with this plan's commit range.
7. **Commit** `README.md`, `CHANGELOG.md` and `docs/tui-simplification/` as explicit paths. Merge `p-f-framed-console` only when the user asks.

**Verify:** step 1's commands. `git status --short` shows only the unrelated untracked docs that were there before.

## Integration notes

- **Daemon P2 plan 3a** is complete in `.marshal/worktrees/p2-3a-projection` and not merged at `88fbde8f`. It touches `spine.go` and `viewport_render.go` (not touched here) and adds `transcript_golden_test.go`. One of its scenes renders the tray through `boxInput`, so Task 5 regenerates that golden when 3a has merged first. Otherwise 3a regenerates it after this phase.
- **Daemon P2 plan 3b** touches `browse_actions.go`, `callers.go`, `moved_reads_test.go` and `viewport_render.go`, none of which this plan touches. Its inspector content changes merge with Task 2's frame without conflict, because the inspector keeps calling `PanelWithHints`.
- **Daemon P2 plans 3c and 3d.** 3d moves drill-in onto `Session.Stack`. Whichever of 3d and this plan lands second ports Task 9's frame and `syncTranscriptWidth` calls onto the other's drill-in sites.
- **The web is untouched.** It never read the TUI's chrome.
- **`TestStatusLineUntrustedIsWarningColored`** is in the baseline. Task 6 touches the status line; if that test starts passing or fails differently, say so in the commit message, but it is not this phase's to fix.

## Self-review

- **Cold-executor test.**
  - Every type, helper and string a test pins is in Contracts. Each step names the function it changes, with its line at `88fbde8f`.
  - Each test to rename, rewrite, delete or add is named with the one behaviour it asserts.
  - The two judgment steps (the golden review in Task 5 and the real session in Task 10) each have a checklist.
- **Scope per task.**
  - Tasks 1, 2 and 4 change shared layers (`theme`, `chrome`, `listpanel`) and name every caller and test they move.
  - Tasks 5 to 9 each touch one package plus its tests, apart from Task 9's wiring line in `commands_dispatch.go`.
- **Anchors**, all checked at `88fbde8f`:
  - `renderInputArea`'s two `PaintBand` calls, `inputBarColor`, `gutteredInput`, `chromeRail`/`chromeRailWidth` and their five callers;
  - `renderCompletionPopup`, `renderTranscriptFrame`, `viewString`'s branches, `resize`'s widths, `inputChromeRows`, `maxInputHeight`, `updateViewportHeight`, `breadcrumbRows`, `drillIntoSubagent`, `popDrill`, the session swap's `m.viewStack = nil`, `contentLineForClick` and `contentPosAt`;
  - `renderStatusLine`, `statusLeftSegments`' priorities and doc list, and `footerHints`;
  - `help.Footer`'s prompt branches and `StopOnly`;
  - `livebox.build`'s bottom row and folded case, `planFolded`, `borderLine` and the box tests that read the closing row;
  - `dock.Host.View`, `layout.PanelWidth` and the minimum terminal width;
  - every `chrome.Panel`/`PanelWithHints` caller and its width argument (only `modeloptions` passes the dock width);
  - `sessionsheet` `View`'s `chrome.Header` call;
  - `accesspanel` `View`'s title, `detailRows` and hint row, and its copy test;
  - `FieldList` header rows and `FieldListSetDescSuppressed`;
  - the settings and agents footers and two-column checks;
  - the plugins and skills views;
  - `theme/depth.go` and every test naming `DepthRaised`, `ChromeBG` or `OverlayBG`;
  - the tests listed under each task.
- **Parallel safety.** No task is marked parallel-safe.
- **Placeholders.** None.
- **Agreement with the spec.** §5.9 and the P-F rows were written from these decisions on 2026-10-27.
