# Marshal Studio — design system

Status: **draft for approval**, 2026-11-17. Normative once approved.

This is the rulebook for everything under `app/lib/src/ui/`. It exists
because the Svelte Studio drifted: contributors without the original context
each made reasonable local choices, and the result was not one product.

**How to read it.** "Must" and "never" are rules. Most are enforced by a
test named in §11; the rest are checked in review. If a screen needs
something this document does not provide, the work stops and the system is
extended first (§10). Nobody invents a one-off.

The clickable reference is [`mock/`](mock/). Where the mock and this
document disagree, this document wins; F0 brings the code into line.

## 1. Principles

1. **One way to do each thing.** One button, one card, one list row, one
   way to show status. A second way needs a reason written here.
2. **Borders group, fill marks state.** Panels and chrome are separated by a
   one-pixel border. A background fill appears only for selection, hover,
   code, and a changed diff line. Never tint a panel, a header or a bar.
3. **Colour means status.** Amber needs you, green passed, red failed, coral
   is the action. Nothing is coloured to decorate or to tell categories
   apart. Roles and kinds are named in words and icons.
4. **Quiet until it matters.** The only loud things on a screen are what
   needs the user and the one primary action.
5. **The same thing looks the same everywhere.** An ask, a pull request, a
   thread row are one component each, reused on every screen that shows one.
6. **Nothing is unexplained.** Every control that is not labelled in words
   has a tooltip.

## 2. Colour

All colour comes from the Ember tokens in `app/lib/src/ui/tokens.dart`. No
other file contains a colour value.

| Token | Dark | Light | Use |
|---|---|---|---|
| `bg` | `#151414` | `#FBF8F4` | Every surface. There is one background |
| `border` | `#2B2928` | `#E5DDD3` | Every border and hairline |
| `fg` | `#ECE7E1` | `#2A2420` | Text; the running state; neutral data marks |
| `muted` | `#9E9790` | `#75695F` | Secondary text |
| `faint` | `#6A645F` | `#8A7E73` | Decoration only: chevrons, placeholders, pending marks |
| `accent` | `#F0865E` | `#BF4A22` | Primary action, selection, the current tab |
| `onAccent` | `#1B0F0A` | `#FFFFFF` | Text and icons on `accent` |
| `ok` | `#6FBF8E` | `#2C7A52` | Passed, ready, done |
| `warn` | `#E2B04A` | `#8F6300` | Needs you |
| `err` | `#E0606E` | `#B8324A` | Failed, blocked, destructive |

Derived, defined once in `tokens.dart` and nowhere else:

| Token | Definition | Use |
|---|---|---|
| `selected` | `accent` at 18% dark, 11% light | The selected row |
| `hover` | `fg` at 5% | A hovered row or button |
| `codeBg` | `fg` at 5% dark, 3.5% light | Code and log blocks |
| `diffRemoved` | `err` at 12% | A removed line |
| `diffAdded` | `ok` at 12% | An added line |
| `attention` | `warn` at 55% | The border of something that needs you |
| `failing` | `err` at 55% | The border of something that failed |
| `scrim` | black at 45% dark, 30% light | What a sheet dims behind it |
| `none` | fully transparent | An absent underline. Not a colour, the lack of one (added in F0) |

### Terminal colours

Used **only inside the terminal widget** (`terminal_view.dart`), never
elsewhere in the UI. The eight ANSI colours map onto Ember where it has the
hue, and three tokens cover the hues it lacks. Bright variants reuse the same
eight.

| ANSI | Token | Dark | Light |
|---|---|---|---|
| black | `faint` | `#6A645F` | `#8A7E73` |
| red | `err` | `#E0606E` | `#B8324A` |
| green | `ok` | `#6FBF8E` | `#2C7A52` |
| yellow | `warn` | `#E2B04A` | `#8F6300` |
| white | `fg` | `#ECE7E1` | `#2A2420` |
| blue | `termBlue` | `#7AA6DA` | `#2F6294` |
| magenta | `termMagenta` | `#C49ADB` | `#7E4A9E` |
| cyan | `termCyan` | `#6FBFC4` | `#1F6F75` |

Each of the three new tokens meets 4.5:1 on `bg` in its theme. They must not
be used for anything else: no status, no accent, no chart. A rule in
`design_rules_test.dart` fails on a reference outside `tokens.dart` and
`terminal_view.dart`.

Rules:

- `fg`, `muted`, `accent`, `ok`, `warn` and `err` meet 4.5:1 on `bg` in both
  themes. `faint` meets 3:1 and so **never carries information by itself**:
  anything a user must read is `muted` or `fg`.
- Running is `fg` with the pulse. It has no colour of its own.
- There are no role colours, no category colours and no gradients.
- Light and dark follow the system unless the user chooses one. Every
  screen works in both; neither is the afterthought.
- Status is never colour alone. It always has an icon shape or a word too.

## 3. Type

Two families, bundled with the app: **Geist** for the interface and **Geist
Mono** for code, paths, identifiers, counts and durations. No other family,
and no font fetched at run time.

Bundled beside them, and never a role's first family, is **Noto Sans Symbols
2** as a fallback (`Ember.symbols`). Geist has no glyph for ⌘ ⌥ ⏎ ✓ ✗ ▸ ▾ ⋯,
and the transcript and the key map need them; every role falls back to it
rather than drawing tofu. It is from notofonts/symbols
`NotoSansSymbols2-v2.008`, under the OFL, shipped with its licence like the
other two. A role's `fontFamilyFallback` names it; nothing sets it as a
`fontFamily`.

Text is set with a named role from `tokens.dart`. No widget sets a size, a
weight or a family directly.

| Role | Size / weight | Use |
|---|---|---|
| `display` | 22 / 600 | One headline number on a card |
| `heading` | 20 / 600 | The title of a page |
| `title` | 15 / 600 | A thread's title; the prompt in the new-work dialog |
| `strong` | 14 / 600 | The title of a card or an ask |
| `reading` | 14 / 400, line height 1.55 | Conversation prose |
| `body` | 13 / 400 | Default |
| `bodyStrong` | 13 / 600 | A row's name, an emphasised value |
| `small` | 12 / 400 | Secondary lines |
| `caption` | 11 / 500 | Chips, timestamps |
| `label` | 11 / 600, uppercase, tracking 0.6 | Section labels only |
| `mono` | 12 / 400, line height 1.45 | Code, commands, paths, diffs |
| `monoSmall` | 11 / 400 | Counts, durations, short hashes |

Rules:

- Sizes are whole numbers from this table. There are no half sizes.
- Uppercase is for `label` only.
- Numbers that change or line up (counts, durations, sizes, money) are mono.
- Truncate with an ellipsis; never shrink text to fit and never let it
  overflow its container.
- `mono` text is drawn, never typed, with one exception: `Field.mono` (§5),
  the input that holds a document — a workspace's source — rather than prose.

## 4. Space, shape and size

| Scale | Values |
|---|---|
| Spacing | 2, 4, 6, 8, 12, 16, 20, 24, 32 |
| Radius | 12 panel, 8 control, 2 bar, full for chips and dots |
| Border | 1 |
| Icon | 11 inline, 13 in a button or row, 15 in navigation |
| Control height | 28 button and icon button, 30 navigation row and input |
| Touch target | At least 44 on the narrow layout |

| Layout | Value |
|---|---|
| Sidebar | 252 wide |
| Inspector | 400 wide docked |
| Reading column | 760 maximum |
| Page | 1180 maximum; 860 for a single list |
| Page padding | 24 sides, 20 top |
| Narrow | below 760: sidebar becomes a drawer |
| Medium | below 1080: inspector becomes a sheet |

Rules:

- Every padding, gap and margin is a value from the spacing scale.
- Every screen is built for all three widths and never scrolls sideways.
- Content does not stretch to fill a wide window; it stops at its maximum.

## 5. The kit

Screens are assembled from the kit in `app/lib/src/ui/kit/`. A screen file
imports the kit and Flutter's widgets layer and **nothing from Material or
Cupertino**. If the kit lacks a piece, see §10.

| Component | It is | Rules |
|---|---|---|
| `Box` | A bordered group | The only container with a border. Border colour is `border`, `attention` or `failing`. No fill, no shadow |
| `Hairline` | A one-pixel divider | Between rows in a `Box`, between regions of the shell |
| `Btn` | A text button | Primary or plain. **At most one primary in any group of actions**; it is first. Destructive actions are plain and confirm |
| `IconBtn` | An icon-only button | Tooltip is required by its constructor |
| `Pill` | A small outlined chip | A fact or a setting, never an action that leaves the screen |
| `Dot` | A status dot | The five statuses only; tooltip names the status |
| `Label` | An uppercase section label | Optional count and one trailing link |
| `Tabs` | Underlined text tabs | Views of one object. Never used for navigation between objects |
| `Segments` | Progress as steps | Done is `ok`, current is the status colour, pending is `border` |
| `StageStrip` | A work item's four stages, the one it is at marked | The stages as a `Segments` bar and as words; a stage the item left is `muted` and one ahead is `faint`. Never a fifth stage: an item that is ending says so in one word beside it |
| `Lanes` | A run's tasks by role | One row per task, one column per lane — Implement, Verify, Review, Commit. Every cell is a status dot and its word, never colour alone; a cell with an action opens that role's conversation and one without is a fact |
| `TaskGraph` | A plan's tasks as a dependency graph | Tasks laid out in dependency layers, left to right, with straight `border` edges and no free dragging. A plan that declares no dependency is drawn as one chain, in order, because that is the work it describes |
| `ActivityRow` | One entry of a thread's activity | Who acted, in words, and what they did, with the event's own kind small beneath it and its age at the right. A timeline, not a transcript. Its age may carry the exact time in a `Tip` (§8) |
| `Meter` | A labelled bar | Capacity and quota. Bar is `muted` unless it reports a fault |
| `Spark` | A small line | One series, `fg`, no axes |
| `CheckStrip` | One block per CI check | Order is the forge's order; tooltip names each check |
| `DiffView` | A unified diff | One row per line, in `mono`; a removed line is filled `diffRemoved` and an added one `diffAdded`; the gutter shows the line's number and, on a removed or added line, takes a comment. Unified only: there is no split view |
| `GateList` | The gate's checks | One row per check: the state's icon in the state's colour, the check's name, its detail in mono at the right. The order is the gate's own |
| `Rows` | A list of name, description, value, and optionally a day's `Spark` | Every settings-style list |
| `NavRow` | A row of the sidebar | An icon, a label, an optional count; a project's row has a disclosure chevron instead of an icon. Only threads carry a status, in a `ThreadRow` |
| `PageBody` | Title, subtitle, actions, content | Every page that is not a thread |
| `AskCard` | Something waiting on the user | The only way an ask is shown. Primary is the action the caller marks, at most one and first; a question's options are equals, so none of them is |
| `PrCard` | A pull request with its CI | The only way a pull request is shown in a list |
| `ThreadRow` | A thread in a list | Dot, title, kind icon |
| `TurnPrompt` | What the user said | Right-aligned, bordered, no fill. A row of the transcript, never a chat bubble |
| `StepHeader` | A step's ordinal, headline and duration | The running dot while it is worked on. Its disclosure folds the step's rows; the duration is mono |
| `ToolRow` | One tool call | Verb and target in mono, the result at the right. Indented to the step's text; `err` colours only a failed result |
| `Narration` | The agent's prose | Markdown, parsed and drawn with the text roles. No third-party widget sets a size, weight or colour |
| `Notice` | Something in a conversation that is not the agent talking to the person: a harness notice, the agent's thinking, a run event or a finished job | Its first line in `small` `muted`; the rest folded behind *Details*. No icon, fill or border. Never the agent's prose, which is `Narration` |
| `OutputBlock` | Output the agent produced | The only filled block: `codeBg`, mono at every density. A capped block says so in words |
| `LiveRow` | The row work is happening on | One running dot, the headline, the latest line. The only row that moves |
| `StateMessage` | Empty, loading, stale, error, planned | The only way these states are shown (§7) |
| `Field` | A text input | Border only; the label is above it. `Field.multiline` is the same input for prose, taller and growing with what it holds; its `mono` flag sets the text in `mono` for a document a person edits line by line. `Field.search` is the one without a label above it, led by a magnifying glass |
| `Menu` | A popover of choices | Each item is a name and one line of explanation |
| `Sheet` | A dialog or bottom sheet; it dims what is behind it with `scrim` | One per screen at most |
| `Composer` | The field a thread is driven from | The draft, the mode and model chips, Stop while running, Send or Steer; the mode chip is left out while the mode is not known, as the model chip is when no model is named. A read-only session is one line saying why |

Added in F2: `NavRow`, the sidebar's row (an icon or a disclosure chevron, a
label, an optional count; the selected row is filled and its icon is
`accent`). The selected row is the open place alone: a project whose thread is
open is not filled, because the thread's row is where the person is. A thread
is never a `NavRow`: it is a `ThreadRow`, which carries its status. A project's
`Earlier` row is a `NavRow` with a disclosure chevron and the count of the
threads it holds, and the threads it opens are `ThreadRow`s.

Added in F4: `DiffView` and `GateList`, the inspector's two read-only views of
a change. Both are reused by the work item's inspector (F6), so neither knows
what an agent or a session is: they take lines and checks.

Variants as built in F0, beyond the table: `Box.flush` (no inner padding,
for rows separated by `Hairline`s, as `Rows` uses); `Btn` and `Btn.primary`;
`Pill`, `Pill.icon` (whose tooltip is required) and a mono label for
identifiers; `Meter` and `Meter.fault`; `PageBody`'s width, `full` (1180) or
`list` (860), from §4; `AskCard`'s placement — Inbox (everything), Overview
(the body only for a decision) or its own thread (no source line);
`PrCard`'s five states — ready, failing, in progress, being fixed and checks
unavailable. Added in
F4: `RowItem.mono` and `NavRow.mono`, a row whose name is an identifier such
as a path, and `RowItem.selected`, the row a list is showing, filled with
`selected` as a `NavRow` is. Added in F7: `Field.mono`, the field set in
`mono` for the environment designer's source — a document a person edits line
by line, never prose. Added for the visual fixes: `Field.search`, the one field
without a label above it — the sidebar's search, which a leading magnifying
glass names.

Added in F5: `PrCard`'s **checks unavailable** state — the checks cannot be
read, and the card says why in its own line. It is never drawn as passing and
never as failing: its border is `border`, its mark `muted`. A failing card
whose dispatch is in flight reads `Sending…` with the primary disabled (§7: an
action taken is acknowledged at once), and the bridge's refusal, when there is
one, sits above its buttons in `err`. A ready card whose merge this build
cannot make draws Merge disabled under a `Tip` that names what brings it
(`PrActions.mergeNote`), never as a button that would fail. `AskKind.pull` is
the ask a fix that could not finish leaves: the pull request and the check,
what was tried, and Open, Try again and Dismiss.

Added in F6: `StageStrip` and `ActivityRow`, the work item's thread. The strip
is a fact, not a control, and it has four stages and no fifth: an item that is
stopping, stopped or closed says so in one word beside it. The activity row is
the item's history, one entry per thing that happened, attributed to the
planner, the implementer, the reviewer or the user in words. A work item's
four asks — a draft awaiting approval, a decision, a recovery and a completion
assessment — are drawn with the kit's existing `AskCard` kinds: the card's
title and body say what is being decided, and a new kind for each would be a
kind name that says nothing the title does not.

Added in F6 for the plan run (§10): `Lanes` draws a run's tasks by role, one
row per task and one column per lane, each cell a status dot and its word. It is
a grid of facts, not a control: a cell that opens a role's conversation is
clickable, and one that does not is read only. `TaskGraph` draws the same tasks
as a dependency graph — dependency layers left to right, straight edges in
`border`, no dragging — and draws a plan that declares no dependency as a single
chain, because that is the work the plan describes. The run's third view, the
timeline, is not a component of its own: it is `Segments`' bar per role over the
run's own span, so its colours are the ones `Segments` already states.

Added for the visual fixes: `Notice`. A warning the harness writes into a
conversation — a snapshot not recorded, a rollback, a compaction — was drawn
as `Narration`, so it read as the agent speaking: six lines of paths in the
reading role, all of them open, breaking mid-word. A `Notice` is the harness's
own voice instead: one quiet line in `small` `muted`, and the rest of what it
said folded behind *Details* until a person asks for it, because a notice is a
fact to acknowledge and not prose to read. What else is not the agent talking
to the person is a `Notice` too: the agent's *Thinking* (hidden at outline
density), a run event, whose kind leads its line as `Review · …`, and a
finished job, whose line leads with its label and outcome. `Narration` stays the
agent's prose alone.

Supporting pieces, shared by the components and exported with them by
`kit.dart`. None is drawn on its own, and none has variants:

- `EmberApp` — the root every window, the gallery and every widget test put
  the kit under: tokens for light or dark, the one background, the default
  text role, an overlay, the default shortcuts.
- `Tip` — the one tooltip. The components put it on themselves; a screen uses
  it for anything else it must explain, such as the exact time behind a
  relative one (§8).
- `Status`, `ThreadKind`, `CheckState` and `Check` — the vocabulary of §6, so
  the kit takes data without importing `model/`.
- `Glyphs` — the one icon set (Cupertino's, bundled by `cupertino_icons`).
- `Press`, not exported — the one way anything is clickable (§9): pointer
  cursor, hover, keyboard focus with the `accent` ring, Enter and Space, and
  the 44 target on the narrow layout.

Rules:

- A component has the variants listed here and no others. A boolean flag
  that restyles a component for one caller is not a variant; it is a one-off.
- Components take data and callbacks. They never fetch and never read a
  store.

## 6. Status and kind

Five statuses, used for threads, agents, checks and pull requests alike:

| Status | Colour | Shape | Word |
|---|---|---|---|
| Needs you | `warn` | filled dot | Needs you |
| Running | `fg`, pulsing | filled dot | Running |
| Ready | `ok` | filled dot or check | Ready |
| Failed | `err` | filled dot or cross | Failed |
| Idle | `faint` | hollow dot | Idle |

Four kinds of thread, told apart by icon and tooltip, never by colour:
agent, work item, plan run, session. A session is a conversation a daemon's
project holds that no agent owns, usually one a terminal started, and its
icon is the desktop computer.

A new status or kind is a change to this document first.

## 7. States every screen has

Each screen that shows data handles all of these with `StateMessage`, and a
test covers each:

- **Loading** — the layout with quiet placeholders; no spinner page.
- **Empty** — one sentence saying what would appear here and one action.
- **Stale** — the last data, plainly marked with its age. Never present old
  data as current, and never infer failure from a lost connection.
- **Error** — what failed, in plain words, and one action to retry.
- **Unavailable** — the host is unreachable; say when it was last seen.
- **Unauthorized** — the bridge refused this page's token, which no retry
  clears: name the host that refused it and say how to sign in again. Never
  drawn as unreachability — that sends the reader to look at the network when
  the fix is a sign-in.
- **Planned** — a capability that does not exist yet is a disabled row where
  it will live, with one line saying so. It is never drawn as working.

An action the user took is acknowledged at once and shows as pending until
the daemon confirms it. A sent message is not shown as done work.

## 8. Words

- Sentence case everywhere. No title case, no full stops on labels and
  buttons.
- Buttons are a verb, or a verb and an object: "Review", "Fix with a
  worker". Never "OK", "Yes" or "Submit".
- Say what will happen, not what the system is. "A worker is on it", not
  "Dispatch accepted".
- No exclamation marks, no emoji, no apology, no "please".
- Use the product's nouns exactly: thread, agent, work item, plan run,
  project, host, environment, pull request, check, ask. Do not introduce a
  synonym.
- Times are relative and short: "8m", "1h 12m", "2d". Exact times are in the
  tooltip.
- A tooltip names the control or states the consequence. It does not repeat
  visible text.

## 9. Behaviour

- **Tooltips.** Every icon-only control, status dot, chip with an icon, bar
  and strip has one.
- **Keyboard.** Everything reachable by mouse is reachable by keyboard, with
  one visible focus ring in `accent`. Shortcuts are listed in one place and
  shown in tooltips.
- **Motion.** The running dot pulses. Nothing else animates. Reduced motion
  stops the pulse.
- **Selection.** Conversation text can be selected and copied, including
  while it streams.
- **Pointer.** Anything clickable shows the pointer cursor and a hover fill.
- **Confirmation.** Only destructive and irreversible actions confirm, and
  the confirmation names what will be lost.
- **Leaving unsaved input** asks first.

## 10. Changing the system

When a screen needs something the system lacks:

1. Stop the screen work.
2. Propose the addition as a change to this document: what it is, when to
   use it, what it must not be used for.
3. Add it to the kit, to the gallery (`ui/gallery/`), and to the goldens.
4. Resume the screen.

These land in one pull request that touches this document. A pull request
that adds to the kit without changing this document, or that works around
the kit inside a screen, is not merged.

Token values change only here and in `tokens.dart`, together.

### `Chart` (added for F7's Usage section)

What it is: a row of vertical bars, each labelled directly underneath with its
own name and value, and tipped with the same.

When to use it: the one place a series of numbers is easier to read as a shape
than as a list — today only Settings' Usage, whose detail the design makes the
one exception to the list-and-detail rule (F7 decision 8).

What it must not be used for:

- A trend over time inside a row: that is `Spark`.
- Progress towards a total: that is `Meter` or `Segments`.
- Two series in one chart: two charts, each with its own labels. There is no
  legend, so a second series could not be told apart without colour, and
  colour here means status.

Its colour: a single series is `fg`. A bar whose value *is* a status takes that
status's colour, and never more than `ok`, `warn` and `err` — never `accent`,
never a fourth hue. A bar with no status is `fg` in both themes.

Its numbers: every bar carries its own label and value as text, so the shape is
never the only way to read the chart, and text scaled up wraps under the bar
rather than being clipped.

## 11. Enforcement

These tests live in `app/test/` and run in CI. Each reads the source or the
widget tree; none needs a daemon.

| Test | Fails when |
|---|---|
| `design_rules_test.dart` · `colours come from tokens` | A file under `ui/` other than `tokens.dart` contains a colour literal or a named Material colour |
| · `text styles come from tokens` | A file under `ui/` other than `tokens.dart` constructs a text style or names a font size, weight or family |
| · `spacing is on the scale` | A padding, gap or box size under `ui/` uses a number that is not on the spacing, size or layout scales |
| · `radii are on the scale` | A radius is not one of the four |
| · `screens use the kit` | A file under `ui/` outside `kit/` and `tokens.dart` imports Material or Cupertino |
| · `kit components do not fetch` | A file under `ui/kit/` imports `model/` or `client/` |
| · `terminal colours stay in the terminal widget` | `termBlue`, `termMagenta` or `termCyan` is used under `ui/` outside `tokens.dart` and `terminal_view.dart` |
| `tooltips_test.dart` | Any screen, pumped with fixture data at each width, contains an icon-only control, dot, chip icon or strip without a tooltip |
| `states_test.dart` | A registered screen lacks a case for loading, empty, stale, error, unavailable or unauthorized |
| `contrast_test.dart` | A token pair in §2 falls below its ratio in either theme |
| `gallery_test.dart` | A kit component is missing from the gallery, or a gallery golden differs in light or dark |
| `overflow_test.dart` | Any screen overflows or scrolls sideways at 390, 800 or 1440 wide, or at text scale 2 |

The tests are deliberately blunt. A blunt rule a contributor cannot argue
with is the point: the answer to "the test is in my way" is §10.

## 12. For an agent working in `app/`

Read this document before changing anything under `app/lib/src/ui/`. Then:

- Build screens only from the kit. Do not import Material or Cupertino in a
  screen.
- Do not write a colour, a size, a weight, a radius or a spacing number that
  is not in §2–§4.
- Reuse `AskCard`, `PrCard`, `ThreadRow` and `StateMessage` wherever their
  subject appears. Do not draw a second version.
- Give every unlabelled control a tooltip.
- Handle every state in §7 and add the test.
- If the kit lacks what you need, do §10. Do not improvise.
- Run the tests in §11 before saying the work is done.
