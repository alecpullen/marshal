# TUI simplification: alternatives

Status: proposal, 2026-10-23. Decided: layout B, Console. The dock's treatment
is still open; see [`console-variants.md`](console-variants.md).
Mockups: [`mockups.html`](mockups.html) has every frame below in colour (open it
in a browser). The frames in this file are the same frames as plain text,
generated from one source.

## Summary

The goal is a simpler TUI. It has to meet three requirements:

1. **A transcript you can follow.** Each tool call sits visibly under the
   narration that explains it, and what you read does not change shape under you.
2. **Todos always visible.**
3. **Subagents and background jobs pinned to the bottom of the transcript**
   while they run, drawn so they cannot be mistaken for anything else.

Section 2 is a foundation that every alternative shares. It meets requirement 1
and the visual half of requirement 3 whatever the layout, and it removes most of
today's rules. Sections 3 to 5 are three layouts on top of it:

| | Principle |
|---|---|
| **A. Ledger** | The plan sits above the conversation and running work sits below it. |
| **B. Console** | History never changes. Everything that can still change is in one dock above the input. |
| **C. Sticky** | Everything is a transcript object. The plan and running cards stick to the viewport edges while they are out of view. |

**Recommendation: B, Console.** It is the only layout where "the transcript only
grows" is true, and that one rule replaces beat folding, the density ladder,
in-place card updates, tail-marker merging, the live mirror row and the now
bar's three size variants. It also fits the daemon P2 wire shape (§8).

## 1. Today

This frame was rendered through the real `Model.View()` from a fixture (100×40,
busy turn, 4 todos, one subagent, one background job). Runs of blank rows are
trimmed to two.

```
 ❯ Add retry with backoff to the provider client and cover it with tests

 · Reading the client to see how requests are sent and where errors surface.                 2 tools

   ── 1/4 Add a retry loop with exponential backoff ────────────────────────────────────────────────
 · Planning the change.
 · Adding the retry loop around doRequest with jittered backoff.
     ✎ Edit file · +38 -6
     › go build ./internal/llm/... · exit 0

   ── 3/4 Table tests for the backoff schedule ─────────────────────────────────────────────────────
 · Starting the full suite in the background and asking a reviewer to check the change.
     › go test ./... · started job-1

 ▸ review retry change                                                                            0s
 ▍ Checking that the backoff respects context cancellation.

 ▸ Writing table tests for the backoff schedule.

     ▸ File write · 0s
       {"path":"internal/llm/client_retry_test.go"}


▍ ▸ ▰▰▱▱ 2/4 · Table tests for the backoff schedule                                            3m 0s
▍  ✓ 2 done · ▸ next: Run the full suite and get a review
▍ ▸ command go test ./...                                                         running · 1m 14s
▍  ok  marshal/internal/agent 4.1s
▍❯ Ask Marshal...
 default · untrusted · no model · local · 001
```

| # | What the frame shows | Source |
|---|---|---|
| 1 | `▸` means five things at once: the running beat, the running tool, the subagent card, the plan progress head and the job card. | `glyph.Running` |
| 2 | The now bar's rows wear the same `▍` rail as the input, so running work reads as part of the composer. | `nowBarRail`, input area |
| 3 | The first beat is already folded to `… 2 tools` with its calls hidden, because finished beats fold except the newest three. The link between narration and calls disappears exactly when you scroll back to read it. | beat folding, `fold_finished_tasks` |
| 4 | Todos show only while a turn the list "drives" is running (`DrivenByTodos`, at least `MinTaskTodos` = 2 items), as two compressed rows. Finished items are a count; the full list is behind Ctrl+T. | `viewmodel/band.go`, `labels.go` |
| 5 | The subagent is an in-transcript card with its own live activity line. With live work it is also a pinned card, and it leaves a tail marker when it finishes. A job is a launch row, a pinned card and a tail marker. | `viewmodel/cards.go`, `nowbar.go` |
| 6 | The running tool row prints raw JSON arguments: `{"path":"internal/llm/client_retry_test.go"}`. | running tool row |
| 7 | Calls hang under their narration by indentation only, each with a category glyph (`✎ ≡ › ◈ ⧉ ┆`) you have to learn. | `renderCompletedToolCall` |

The frame does not show the size rules: the now bar ranges from 0 to 10 rows,
with a separate one-row layout below 30 rows and an open checklist from 44 rows,
plus a live mirror row when you scroll away, receipt and attention rows for
finished work, and a card that can expand inside the bar. Ctrl+G cycles three
densities on top of the folding rules.

Each of these rules was added for a good reason. Together they come from one
cause: work in progress is drawn in several places at once, and each place has
its own rules for when it shows, folds or shrinks. The fix is fewer places, not
better rules.

## 2. Foundation (shared by every alternative)

```
A beat: one narration line, and the calls it explains on a spine beneath it

● Reading the client to see how requests are sent and where errors surface.           thought 3s
  ├ read    client.go, errors.go, backoff.go                                             3 files
  └ search  "doRequest"                                                      4 matches · 2 files

✗ Running the retry tests.
  └ shell   go test ./internal/llm/ -run Retry                                            exit 1
            --- FAIL: TestRetryHonoursContext (0.00s)
                client_retry_test.go:41: waited 2s after cancel

A model that rarely narrates still gets one rule: the beat shows its last calls

● Fixing the remaining call sites.
  ├ … 14 earlier calls
  ├ edit    internal/llm/anthropic.go                                                      +4 −1
  └ edit    internal/llm/ollama.go                                                         +4 −1

Running work is tinted and edged in its kind's colour; nothing else is

▌⧉ #1     review the retry loop   Checking that the backoff respects context cancella…  1m02s  ⠋
▌┆ job-1  go test ./...           ok  marshal/internal/agent  4.1s                      1m14s  ⠋

When it finishes, one event lands in the transcript, untinted, at that moment

⧉ #1 review the retry loop · done · 14 calls · 1m21s                                           ▹
    One nit: cap the jitter so the last wait cannot pass 30 s.
┆ job-1 go test ./... · exit 1 · 2m02s                                                         ▹
    --- FAIL: TestRetryHonoursContext (0.00s)

✓ done · 6m40s · 9 beats · 23 calls · ±3 files · 41k tokens
```

### 2.1 The beat spine

A beat stays what it is today: a narrated model response plus the narrationless
responses after it (`viewmodel` beats). Only its rendering changes.

- **Head:** `●` and the narration, wrapped in full. The first-sentence headline
  with a muted continuation goes; the narration prompt keeps it short anyway.
- **Head meta (right):** owner when it is not the orchestrator, `thought Ns`,
  and the duration from 15 s up. These are today's rules.
- **Calls:** one line each, hung on a spine (`├`, `└`) so membership is drawn
  rather than implied. Three columns: a verb (`read`, `search`, `edit`,
  `write`, `shell`, `agent`, `web`, `mcp`), the subject, and the result
  right-aligned. Words replace the category glyphs.
- **Failure:** a failed call turns the head glyph to `✗` and shows its first
  three output lines under the call, as today.
- **Same-tool runs** merge into one line, as today.
- **Long beats** from models that rarely narrate show their last 8 calls behind
  `… N earlier calls`. The same rule applies live and settled.
- **`todo.write`** calls are not drawn; the plan (§2.4) shows the list.
- **Todo transitions** keep their dim rule, `── 3/4 Table tests ──`, as chapter
  marks in the scrollback.
- **A running call** shows a spinner and elapsed time on the right. It never
  prints raw arguments.

### 2.2 One glyph, one meaning

| Glyph | Means | Colour |
|---|---|---|
| `❯` | your prompt, and the input | accent |
| `●` | a settled beat | muted |
| spinner | running right now (beat head, call, pinned row) | accent |
| `✗` | failed | error |
| `✓` | done (plan item, completion event, receipt) | success |
| `├ └` | this call belongs to the beat above | border |
| `⧉ #n` | subagent *n* | violet (`AccentSecondary`) |
| `┆ job-n` | background job *n* | cyan (`StatusInfo`) |
| `○` | watch | default |
| `▸` | the plan item in progress | accent |
| `·` | a plan item not started | border |
| tint + `▌` | running work, pinned | kind colour |

Retired: `▸` as "running", `·` as "settled step", the call-row category glyphs
`✎ ≡ › ◈`, and the `▍` rail on now-bar rows. No new colours are needed; every
colour above is already in the Warm Sunset palette.

### 2.3 Running work: launch line, pinned row, completion event

Each subagent, background job, watch or browser session is drawn in exactly one
place at a time.

1. **Launch.** The call that starts it is an ordinary line in its beat. Its
   result names the entity in its colour: `┆ job-1 started`, `⧉ #1 started`.
   The line states a fact about that moment, so it never needs updating.
2. **Running.** One pinned row per entity at the bottom of the transcript. The
   row has a tinted background (`BGSurface`) and a `▌` edge in its kind's
   colour, then glyph, id, label, latest activity and elapsed time. **Only
   running work is tinted.** Nothing else in the transcript has a background,
   which is what makes these rows read as a different kind of thing. Under
   NO_COLOR the edge, glyph and id still set them apart.
3. **Done.** The pinned row leaves and one completion event is appended to the
   transcript at that moment: `⧉ #1 review the retry loop · done · 14 calls ·
   1m21s ▹`, with one summary line. This is where the agent learns the result,
   so its reaction lands directly beneath it.

A subagent waiting on an approval turns its row to warning colour with
`⚠ needs approval`; the approval itself takes over the input as it does today.
Drill-in (Ctrl+F, or Enter on the row or the event) is unchanged.

### 2.4 The plan

- The plan is the session's current todo list. It shows whenever a list
  exists, busy or idle, at any length. The `DrivenByTodos` and `MinTaskTodos`
  gates go away.
- Rows: `✓` done (muted), `▸` in progress (with its elapsed time), `·` not
  started (default).
- The whole list shows up to 6 items. A longer list shows a window around the
  active item with `✓ N done` and `+N more`. A short frame gets one row.
- A finished list collapses to `✓ Plan 4/4 done` until your next prompt.
- The stale tag (`stale 12m`) stays, because the model nudge shares its rule.
- Ctrl+T toggles the plan between its full list and one row. The Tasks panel's
  Plan view becomes redundant; its Work view stays as the history of finished
  work.

### 2.5 One level of detail

- Finished beats never fold on their own. A beat looks the same when you
  scroll back as it did when it finished.
- Enter (in browse mode) or a click expands one line. Ctrl+G toggles expand-all.
  The outline and full densities go.
- Browse mode, the inspector, copy and open stay as they are.

### 2.6 What the foundation removes

- The density ladder (`density.go`, `tui.transcript.density`).
- Beat auto-folding, `tui.transcript.fold_finished_tasks` and the `z` toggle.
- In-place subagent, job and await cards and tail-marker merging in
  `viewmodel/cards.go`. The launch line and completion event replace them.
- Todo gating in `viewmodel/band.go` and `labels.go`.
- The headline/continuation split.
- Live work's receipt and attention rows. The completion event is the receipt,
  and the Work view keeps the history.

## 3. Alternative A: Ledger

*The plan sits above the conversation and running work sits below it.*

```
Plan  ▰▰▱▱  2/4
 ✓ Add a retry loop with exponential backoff
 ✓ Classify retryable errors (429, 5xx, resets)
 ▸ Table tests for the backoff schedule                                                    1m40s
 · Run the full suite and get a review
────────────────────────────────────────────────────────────────────────────────────────────────
❯ Add retry with backoff to the provider client and cover it with tests

● Reading the client to see how requests are sent and where errors surface.
  ├ read    internal/llm/client.go                                                     212 lines
  └ search  "doRequest"                                                      4 matches · 2 files

── 1/4  Add a retry loop with exponential backoff ──────────────────────────────────────────────
● Adding the retry loop around doRequest with jittered backoff.                       thought 3s
  ├ edit    internal/llm/client.go                                                        +38 −6
  └ shell   go build ./internal/llm/...                                                   exit 0

── 2/4  Classify retryable errors (429, 5xx, resets) ───────────────────────────────────────────
● Splitting provider errors into retryable and final.
  ├ edit    internal/llm/errors.go                                                        +21 −2
  └ shell   go test ./internal/llm/ -run Classify                                   ok · 6 tests

── 3/4  Table tests for the backoff schedule ───────────────────────────────────────────────────
● Starting the full suite in the background and asking for a review.
  ├ shell   go test ./...                                                        ┆ job-1 started
  └ agent   review the retry loop                                                   ⧉ #1 started

⠋ Writing table tests for the backoff schedule.
  ├ read    internal/llm/backoff.go                                                     48 lines
  └ write   internal/llm/client_retry_test.go                                               ⠋ 4s
▌⧉ #1     review the retry loop   Checking that the backoff respects context cancella…  1m02s  ⠋
▌┆ job-1  go test ./...           ok  marshal/internal/agent  4.1s                      1m14s  ⠋
❯ Ask Marshal…
⠋ working 3m00s · act · qwen3-coder@ollama · ctx 34% · ±2 files        main · ~/projects/marshal
```

Top to bottom: the plan, a rule, the transcript (the live beat streams at its
end), the pinned running rows, the input, and the status line, which takes the
turn timer.

- The plan's height changes only when the list changes, so the transcript does
  not jump while you read.
- When you scroll up the live beat leaves the view. The spinner and timer in the
  status line still say the turn is working.
- **Good:** the plan reads like a document header, and it never competes with
  running rows for space.
- **Costs:** the eye moves between the top and the bottom of the screen. The
  plan's rows come out of the transcript even while you read old history. A
  pinned strip at the top was tried in single-stack P4 and folded into the now
  bar by transcript-beats. The live beat still changes in place.
- **Removes:** the foundation list, the live mirror row, and the now bar
  planner down to the running rows.

## 4. Alternative B: Console (recommended)

*History never changes. Everything that can still change is in one dock above
the input.*

```

❯ Add retry with backoff to the provider client and cover it with tests

● Reading the client to see how requests are sent and where errors surface.
  ├ read    internal/llm/client.go                                                     212 lines
  └ search  "doRequest"                                                      4 matches · 2 files

── 1/4  Add a retry loop with exponential backoff ──────────────────────────────────────────────
● Adding the retry loop around doRequest with jittered backoff.                       thought 3s
  ├ edit    internal/llm/client.go                                                        +38 −6
  └ shell   go build ./internal/llm/...                                                   exit 0

── 2/4  Classify retryable errors (429, 5xx, resets) ───────────────────────────────────────────
● Splitting provider errors into retryable and final.
  ├ edit    internal/llm/errors.go                                                        +21 −2
  └ shell   go test ./internal/llm/ -run Classify                                   ok · 6 tests

── 3/4  Table tests for the backoff schedule ───────────────────────────────────────────────────
● Starting the full suite in the background and asking for a review.
  ├ shell   go test ./...                                                        ┆ job-1 started
  └ agent   review the retry loop                                                   ⧉ #1 started
⠋ working 3m00s ────────────────────────────────────────────────────────────────  Ctrl+C ×2 stop
⠋ Writing table tests for the backoff schedule.
  ├ read    internal/llm/backoff.go                                                     48 lines
  └ write   internal/llm/client_retry_test.go                                               ⠋ 4s
▌⧉ #1     review the retry loop   Checking that the backoff respects context cancella…  1m02s  ⠋
▌┆ job-1  go test ./...           ok  marshal/internal/agent  4.1s                      1m14s  ⠋
Plan  ▰▰▱▱  2/4
 ✓ Add a retry loop with exponential backoff
 ✓ Classify retryable errors (429, 5xx, resets)
 ▸ Table tests for the backoff schedule                                                    1m40s
 · Run the full suite and get a review
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

Top to bottom: history, then the dock (a rule carrying the turn state, the live
beat, the running rows and the plan), then the input and the status line.

- **History only grows.** A beat moves from the dock into history when it
  settles: when the next beat starts or the turn ends. While you follow the
  bottom this is invisible, because the beat is already directly above the
  rule. Completion events and the final answer append to history. A streamed
  answer commits line by line as each line completes, so only the line being
  written lives in the dock.
- **The dock is bounded** at 40% of the frame. When space is short it sheds in
  a fixed order: the plan drops to one row, then the live beat drops to its
  head and current call, then running rows cap at 3 with `+N more`.
- **Scrolling up leaves the dock in place,** so the live beat, the running work
  and the plan stay in view. There is no mirror row.
- **Idle,** the dock is the plan's summary row, or nothing.

At 80×24 everything still fits:

```
  └ search  "doRequest"                                      4 matches · 2 files

── 1/4  Add a retry loop with exponential backoff ──────────────────────────────
● Adding the retry loop around doRequest with jittered backoff.       thought 3s
  ├ edit    internal/llm/client.go                                        +38 −6
  └ shell   go build ./internal/llm/...                                   exit 0

── 2/4  Classify retryable errors (429, 5xx, resets) ───────────────────────────
● Splitting provider errors into retryable and final.
  ├ edit    internal/llm/errors.go                                        +21 −2
  └ shell   go test ./internal/llm/ -run Classify                   ok · 6 tests

── 3/4  Table tests for the backoff schedule ───────────────────────────────────
● Starting the full suite in the background and asking for a review.
  ├ shell   go test ./...                                        ┆ job-1 started
  └ agent   review the retry loop                                   ⧉ #1 started
⠋ working 3m00s ────────────────────────────────────────────────  Ctrl+C ×2 stop
⠋ Writing table tests for the backoff schedule.
  └ write   internal/llm/client_retry_test.go                               ⠋ 4s
▌⧉ #1     review the retry loop   Checking that the backoff respects …  1m02s  ⠋
▌┆ job-1  go test ./...           ok  marshal/internal/agent  4.1s      1m14s  ⠋
▰▰▱▱ 2/4  ▸ Table tests for the backoff schedule   next · Run the full su… 1m40s
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                               main
```

After the turn, finished work has landed in history in the order it happened,
each event directly above the agent's reaction to it:

```
  ├ shell   go test ./...                                                        ┆ job-1 started
  └ agent   review the retry loop                                                   ⧉ #1 started

● Writing table tests for the backoff schedule.
  ├ read    internal/llm/backoff.go                                                     48 lines
  └ write   internal/llm/client_retry_test.go                                               +112

┆ job-1 go test ./... · exit 1 · 2m02s                                                         ▹
    --- FAIL: TestRetryHonoursContext (0.00s)
● The context test fails because the wait ignores cancellation; fixing that.
  ├ edit    internal/llm/backoff.go                                                        +6 −2
  └ shell   go test ./internal/llm/ -run Retry                                      ok · 9 tests

── 4/4  Run the full suite and get a review ────────────────────────────────────────────────────
⧉ #1 review the retry loop · done · 14 calls · 1m21s                                           ▹
    One nit: cap the jitter so the last wait cannot pass 30 s.
● Capping the jitter at 30 s as the reviewer suggested, then running everything.
  ├ edit    internal/llm/backoff.go                                                        +3 −1
  └ shell   go test ./...                                                       ok · 1,284 tests

Every provider call now retries 429s, 5xx responses and connection resets with
jittered exponential backoff, capped at 30 s and cancelled with the request context.
The new table tests pin the schedule; the full suite passes.

✓ done · 6m40s · 9 beats · 23 calls · ±3 files · 41k tokens
✓ Plan  4/4 done                                                      clears on your next prompt
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Good:** one rule explains the whole screen. Everything live is one glance
  away from where you type. Settled text never reflows, which keeps text
  selection stable and lets the render cache draw each settled node once. The
  wire contract can become append-only history plus one live payload, and the
  `band` node already ships separately.
- **Costs:** a busy turn's dock can take 10 or more rows (bounded at 40%). The
  live beat moves from the dock into history when it settles, which is visible
  as history growing below you when you have scrolled up. Line-by-line commit of
  a streamed answer is new work.
- **Removes:** the foundation list, plus `nowbar.go`'s planner variants
  (compact and tall layouts, mirror row, in-bar card expansion, attention and
  receipt rows) and the live/settled split in the render cache.

## 5. Alternative C: Sticky

*Everything is a transcript object. The plan and running cards stick to the
viewport edges while they are out of view.*

The frame shows a reader scrolled to the middle of the turn. The plan block was
born at the `todo.write` above the view, so it sticks to the top. The two cards
were born at their launch lines below the view, so they stick to the bottom. The
live beat is out of view, so a mirror row stands in for it.

```
⇡ Plan  ▰▰▱▱  2/4                                                                 written 2m ago
 ✓ Add a retry loop with exponential backoff
 ✓ Classify retryable errors (429, 5xx, resets)
 ▸ Table tests for the backoff schedule                                                    1m40s
 · Run the full suite and get a review
── 1/4  Add a retry loop with exponential backoff ──────────────────────────────────────────────
● Adding the retry loop around doRequest with jittered backoff.                       thought 3s
  ├ edit    internal/llm/client.go                                                        +38 −6
  └ shell   go build ./internal/llm/...                                                   exit 0

● Routing the Anthropic and Ollama clients through the same retry path.
  ├ edit    internal/llm/anthropic.go                                                      +4 −1
  └ edit    internal/llm/ollama.go                                                         +4 −1

── 2/4  Classify retryable errors (429, 5xx, resets) ───────────────────────────────────────────
● Splitting provider errors into retryable and final.
  ├ edit    internal/llm/errors.go                                                        +21 −2
  └ shell   go test ./internal/llm/ -run Classify                                   ok · 6 tests

● Checking which errors the Ollama client wraps before they reach us.
  └ search  "fmt.Errorf" in internal/llm/ollama.go                                     7 matches

── 3/4  Table tests for the backoff schedule ───────────────────────────────────────────────────
▌⧉ #1 review the retry loop                                    qwen3-coder · 14 calls · 1m02s  ⠋
▌  Checking that the backoff respects context cancellation.
▌┆ job-1 go test ./...                                                          running 1m14s  ⠋
▌  ok  marshal/internal/agent  4.1s
↓ ⠋ Writing table tests for the backoff schedule.                                  End to follow
❯ Ask Marshal…
⠋ working 3m00s · act · qwen3-coder@ollama · ctx 34% · ±2 files        main · ~/projects/marshal
```

- A running card shows in place under its launch line while that line is
  visible, and docks at the bottom edge while it is not. The plan does the same
  at the top.
- **Good:** each thing has one home, and the scrollback shows the plan and the
  cards where they happened.
- **Costs:** objects move between their home and an edge as you scroll and as
  output arrives, the opposite of a transcript that holds still. A finished card
  settles back at its launch point, usually out of view, so a completion event
  is still needed. It needs a mirror row and a new sticky layout pass in the
  viewport. It has the most rendering code of the three.
- **Removes:** the foundation list except the in-place cards, which it keeps by
  design.

## 6. Comparison

| | A. Ledger | B. Console | C. Sticky |
|---|---|---|---|
| Todos always visible | yes, top | yes, above the input | yes, top while out of view |
| Running work at the bottom | yes | yes | yes, while out of view |
| Transcript holds still while you read | mostly; the live beat changes in place | yes, history is append-only | no; cards and plan move |
| Live beat when you scroll up | out of view (status line timer) | stays in the dock | mirror row |
| Places that change during a turn | 3 (plan, transcript end, running rows) | 1 (the dock) | 3 (top edge, transcript, bottom edge) |
| New mechanism | none | line-by-line commit of streamed text | sticky layout pass |
| Removes now-bar planner | mostly | entirely | mostly |
| Fits 80×24 | yes | yes (sheds in a fixed order) | tight (cards take 2 rows each) |
| Risk | low | medium | medium-high |

## 7. Considered and rejected

- **A side rail at 120 columns and up** (plan and running work in a right
  column). It breaks requirement 3, because running work leaves the bottom of
  the transcript. It needs a second layout below the breakpoint, and the rail
  was removed in single-stack P1 for half-width terminals.
- **A transcript sectioned by todo** (task headers grouping their steps). This
  was built in single-stack P3/P4 and reverted by transcript-beats because
  models do not maintain todo state reliably, so the structure came out wrong.
  The plan and the todo rule give the same orientation without depending on it.
- **A flat stream with one bullet per call,** in the style of most coding
  CLIs. It is simple, but calls link to narration only by adjacency, which
  fails requirement 1.

## 8. Fit with work in flight

- **interaction-modes** (about to land): the status line's mode segment shows
  `act`, `ask` and `auto`. Approval and question takeovers of the input are
  unchanged in every alternative.
- **Daemon P2 (client seam):** the transcript is plan 3 of 9 and ships
  `WireTree`. Decide the layout before that plan is written. Console means the
  wire carries append-only history nodes plus the band (live beat, running
  work, plan), which is simpler than diffing a tree whose settled nodes still
  change.
- **web/ui** renders the same `viewmodel` projection. The lifecycle (§2.3) and
  plan (§2.4) changes are projection changes, so the web gets them too. The
  spine and glyphs are TUI rendering.
- **Text selection (daemon P0):** append-only history means a selection never
  sits on text that reflows under it.

## 9. Decisions for you

1. **Layout:** Console (recommended), Ledger or Sticky.
2. **Remove auto-folding and the density ladder?** Recommended: yes, keep
   per-line expand and Ctrl+G expand-all.
3. **A finished plan:** collapse to one row until your next prompt
   (recommended), or hide at once.
4. **Failed background work:** is the red completion event enough, or should
   live work's attention row and acknowledgement stay? Recommended: the event
   plus the Work view; drop acknowledgement.
5. **Narration:** show it whole (recommended) instead of a first-sentence
   headline with a muted continuation.

## 10. Suggested phasing

1. **Renderer foundation.** Spine, verbs, glyph cleanup, no auto-fold, one level
   of detail. TUI only, plus the removed settings. Visible on its own.
2. **Lifecycle in `viewmodel`.** Completion events replace in-place cards and
   tail markers; pinned rows come from the work snapshot. TUI and web.
3. **Layout.** The chosen layout replaces `nowbar.go`; the plan surface and the
   Ctrl+T toggle.
4. **Wire.** Align with daemon P2's transcript plan.
