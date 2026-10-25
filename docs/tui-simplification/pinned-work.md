# Pinned work

Status: decided 2026-10-25: the tray (option A) without its rule. The spec's
P-E phase records it. **Revised the same day (round 6):** after testing P-E,
the frame is back. Only the box's step in progress was meant to go; the
running work and the plan live inside P-C's outline again, with the running
section's header in the top border. The spec's §5.5 is current; the frames
below show the frameless tray as it was decided. Follows
[`framed-console.md`](framed-console.md), whose live box shipped in P-C. The
spec is [`feature-spec.md`](feature-spec.md). Mockups in colour:
[`pinned-work.html`](pinned-work.html).

## Decision

- **The tray (option A),** because it keeps the whole todo list in view.
- **No rule above it,** which saves a row. The tint on running rows and the
  plan's marks set the tray apart from the transcript.
- **The head's meta stays on the narration's first line.** A narration too long
  to sit beside its meta (the todo tag, the thinking time) wraps narrower to
  make room, so the meta never takes a line of its own. Today's `▸ 2/4` line
  under a wrapped narration goes.
- **A finished item keeps its row until your next prompt.** The row loses its
  tint, shows the outcome in place of the measurement, stops its clock, and
  shows `✓` or `✗` where the spinner was. Its completion event still lands in
  history when it finishes.

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the     ▸ 2/4
     config change meanwhile.
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines













▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌⧉ #1     review the config change  Checking context_window…                             1m 2s ⠋
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                               Ctrl+C stop
```

A finished item holding its row while the turn goes on:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the     ▸ 2/4
     config change meanwhile.
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⧉ #1 review the config change · done · 1m 48s                                                 ▹
     The config change is consistent; context_window matches the model card.

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines










▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌⧉ #1     review the config change  done · context_window matches the model card        1m 48s ✓
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                               Ctrl+C stop
```

The turn over, the download still running, the reviewer's row held until the
next prompt:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the     ▸ 2/4
     config change meanwhile.
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⧉ #1 review the config change · done · 1m 48s                                                 ▹
     The config change is consistent; context_window matches the model card.

 ● Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines

   The thresholds hold for a coder model, and the reviewer found no problems with the
   config change. The weights are still downloading; I'll run the harness tests once they
   land.






▌┆ job-3  Download qwen3-coder weights  ━━━━━━━━━━── 84%  3.1 GiB/3.7 GiB                3m 0s ⠋
▌⧉ #1     review the config change  done · context_window matches the model card        1m 48s ✓
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

80×24:

```
 ❯ Add a retry to the weights downloader, start the download, then check the
   eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry      ▸ 1/4
     with backoff.
   ├ read    internal/models/download.go                               212 lines
   ├ edit    internal/models/download.go                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a           ▸ 2/4
     reviewer to check the config change meanwhile.
   ├ shell   python3 .marshal/progress/download.py https://ex… started job job-3
   ├ agent   review the config change                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.        ▸ 3/4
   └ read    evals/thresholds.toml                                      64 lines

▌┆ job-3  Download qwen3-code…  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB        2m 0s ⠋
▌⧉ #1     review the config c…  Checking context_window…                 1m 2s ⠋
 ✓ Add retry with backoff to the down…  ▸ Check the eval thresholds
 ✓ Start the weights download           · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local               Ctrl+C stop
```

A card open:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the     ▸ 2/4
     config change meanwhile.
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines









▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━╸──────────────────────────────────── 57%
▌ 57%  2.1 GiB/3.7 GiB
▌ weights.gguf
▌ ↵ collapse · /work list
▌⧉ #1     review the config change  Checking context_window…                             1m 2s ⠋
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                               Ctrl+C stop
```

The rest of this file is the record of the options as they were presented.

## Why

After using the live box, it feels out of step with the rest of the TUI in four
ways:

- **It looks like a panel.** It is the only bordered, sectioned thing in the
  conversation, so it reads like a docked panel, but it never takes focus like
  one.
- **The step in progress moves.** It streams inside the box, then reappears
  above in another form when it settles.
- **It has its own controls.** Click-to-open cards, Ctrl+T's three layouts, and
  browse running on into the box don't work like transcript rows.
- **Its height jumps** as work starts and stops, pushing history up and down.

The direction from that review: the agent's output goes back into the
transcript, at the bottom, and settles where it was drawn. Only these stay
pinned:
- the todos;
- the plan run, if that is what "the other plan information" means (an SDD
  run's task count and phase);
- running subagent and job cards;
- progress widgets.

## Fixed in every option

| | |
|---|---|
| Agent output | Streams at the end of the transcript and settles in place. Narration, thinking and calls never move. |
| Frame | No border and no section headers. Ctrl+T and `[tui.live_box] layout` go. |
| Plan grammar | One form everywhere: the history's plan line, `✓ ✓ ▸ ·  3/4  Check the eval thresholds`, where `3/4` is the current item's position. Today the box's `▰▰▱▱ 2/4` counts finished items instead, so the same plan shows two numbers. |
| Running rows | Today's row format: the kind's glyph and id in its colour, the title, the measurement and the clock, on the surface tint. |
| Controls | A pinned row behaves like a transcript row. Browse (Esc) reaches it after the last history row, Enter or a click opens it, `s` stops it and `f` opens a subagent. |
| Turn clock | Moves to the status line, with the stop hint. |
| Plan run | An SDD or swarm run is a pinned row like a job, for example `▸ run  p-d-progress-skill  task 2/5 · implement 3m · ~12–16m left`. |

All frames show one session. The agent has added a retry to a downloader,
started the weights download in the background with a progress card, and asked
a reviewer subagent to check a config change. It is now checking the eval
thresholds.

The "today" frames are real renders of `main` at `fc0bceba`. Only the test
environment's status line, spinner glyph and missing surface tint are replaced
with what the app draws. The options are drawn from the same session.

## Today

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen…  started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds








╭─ working 4m 12s ────────────────────────────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│  ⠋ Checking the eval thresholds still make sense for a coder model.                    ▸ 3/4 │
│    └ read    evals/thresholds.toml                                                  64 lines │
│                                                                                              │
├─ running 2 ──────────────────────────────────────────────────────────────────────────────────┤
│ ▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB            2m 0s ⠋ │
│ ▌⧉ #1     review the config change  Checking context_window…                         1m 2s ⠋ │
├─ plan  ▰▰▱▱ 2/4 ─────────────────────────────────────────────────────────────────────────────┤
│ ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds                   │
│ ✓ Start the weights download                   · Run the harness tests                       │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

The render shows more than the four complaints:
- The plan is drawn three ways: the history's plan line, the box's two-column
  list, and the idle row `▸ Plan 2/4`. The box and the plan line give different
  counts.
- The box takes 11 of 34 rows here. At 80×24 it takes 8 of 24 and pushes your
  message off the screen.
- Idle with a job still running, the box keeps an empty top border above its
  `running 1` header (see "Idle, one job still running" below).

## Option A — Tray

Today's box without the frame or the live step: a plain rule, then the running
rows, then the plan list in two columns.

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines











────────────────────────────────────────────────────────────────────────────────────────────────
▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌⧉ #1     review the config change  Checking context_window…                             1m 2s ⠋
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                               Ctrl+C stop
```

- **Pinned:** the running rows and the full todo list. That is 7 rows with two
  running items, counting the input and status line (13 today), and 6 when idle
  with one job.
- **Height:** grows when work starts. A finished item keeps its slot, dimmed and
  showing its outcome, until you send the next prompt. The tray only shrinks
  between turns.
- **Controls:** rows only. Enter on a todo jumps to the beat that started it, as
  a click on the box's plan does today.
- **Cost:** the most rows of the three, and the plan list is a second plan
  grammar beside the plan line.

## Option B — One line

Everything pinned shares one line above the input. Cards open in `/work`.

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines















 ✓ ✓ ▸ ·  3/4  Check the eval thresholds  ·  ┆ job-3 ━━━━╸─── 57%  ·  ⧉ #1 Checking context_wi…
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                               Ctrl+C stop
```

- **Pinned:** the plan line, then a short segment per running item: a mini bar
  and percentage for measured work, the latest activity for a subagent. It is
  always one row while a plan or running work exists.
- **Height:** constant.
- **Controls:** none in the line. A click on it, or `/work`, opens the Work
  panel, which already shows full cards.
- **Cost:** the widgets shrink to a mini bar. With three or more running items
  the line has to fall back to counts (`+2 running`). Full measurements and the
  todo list are one step away.

## Option C — Rows on the input

Running rows sit directly on the input, only while something runs. The plan
line moves into the status line.

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines














▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌⧉ #1     review the config change  Checking context_window…                             1m 2s ⠋
▍❯ Ask Marshal...
 Act · ⠋ 4m 12s · qwen3-coder @ ollama                   ✓ ✓ ▸ ·  3/4  Check the eval thresholds
```

- **Pinned:** the running rows, which open to full cards, and the plan line,
  right-aligned in the status line, where it never moves.
- **Height:** no extra rows when nothing runs, otherwise one per running item,
  with A's keep-the-slot rule.
- **Controls:** rows only.
- **Cost:** only the current todo is named; the others are marks. At 80 columns
  the status line drops the model name to fit.

## Compared

| | Today | A Tray | B One line | C Rows on the input |
|---|---|---|---|---|
| Rows below the transcript, busy, 96×34 | 13 | 7 | 3 | 4 |
| Idle, one job running | 9 | 6 | 3 | 3 |
| Busy, 80×24 | 10 | 7 | 3 | 4 |
| One card open, 96×34 | 14 | 11 | — | 8 |
| Height changes | at every start and stop | between turns | never | between turns |
| Todo list on screen | full | full | current item | current item |
| Measured bars | full | full | mini | full |
| Controls of its own | Ctrl+T, three layouts | none | none | none |

Rows below the transcript include the input and the status line, which every
option keeps.

## Idle, one job still running

The turn has ended and the reviewer has finished; the download is still running.

Today:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen…  started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⧉ #1 review the config change · done · 1m 48s                                            ▹
     The config change is consistent; context_window matches the model card.

 ● Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines

   The thresholds hold for a coder model, and the reviewer found no problems with the
   config change. The weights are still downloading; I'll run the harness tests once they
   land.


╭──────────────────────────────────────────────────────────────────────────────── Ctrl+T plan ─╮
├─ running 1 ──────────────────────────────────────────────────────────────────────────────────┤
│ ▌┆ job-3  Download qwen3-coder weights  ━━━━━━━━━━── 84%  3.1 GiB/3.7 GiB            3m 0s ⠋ │
├─ plan  ▰▰▱▱ 2/4 ─────────────────────────────────────────────────────────────────────────────┤
│ ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds                   │
│ ✓ Start the weights download                   · Run the harness tests                       │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

A:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⧉ #1 review the config change · done · 1m 48s                                                 ▹
     The config change is consistent; context_window matches the model card.

 ● Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines

   The thresholds hold for a coder model, and the reviewer found no problems with the
   config change. The weights are still downloading; I'll run the harness tests once they
   land.





────────────────────────────────────────────────────────────────────────────────────────────────
▌┆ job-3  Download qwen3-coder weights  ━━━━━━━━━━── 84%  3.1 GiB/3.7 GiB                3m 0s ⠋
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

B:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⧉ #1 review the config change · done · 1m 48s                                                 ▹
     The config change is consistent; context_window matches the model card.

 ● Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines

   The thresholds hold for a coder model, and the reviewer found no problems with the
   config change. The weights are still downloading; I'll run the harness tests once they
   land.








 ✓ ✓ ▸ ·  3/4  Check the eval thresholds  ·  ┆ job-3 ━━━━━━╸─ 84%  3.1 GiB/3.7 GiB
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

C:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⧉ #1 review the config change · done · 1m 48s                                                 ▹
     The config change is consistent; context_window matches the model card.

 ● Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines

   The thresholds hold for a coder model, and the reviewer found no problems with the
   config change. The weights are still downloading; I'll run the harness tests once they
   land.








▌┆ job-3  Download qwen3-coder weights  ━━━━━━━━━━── 84%  3.1 GiB/3.7 GiB                3m 0s ⠋
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama                              ✓ ✓ ▸ ·  3/4  Check the eval thresholds
```

## 80×24

Today:

```
 ● The downloader gives up on the first network error. Adding a retry with
     backoff.
                                                                           ▸ 1/4
   ├ read    internal/models/download.go                               212 lines
   ├ edit    internal/models/download.go                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to
     check the config change meanwhile.
                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://e…  started job job-3
   ├ agent   review the config change                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

╭─ working 4m 12s ────────────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│  ⠋ Checking the eval thresholds still make sense for a coder model.    ▸ 3/4 │
│    └ read    evals/thresholds.toml                                  64 lines │
│                                                                              │
├─ running 2 ──────────────────────────────────────────────────────────────────┤
│ ▌┆ job-3  Download qwen3-coder…  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB   2m 0s ⠋ │
│ ▌⧉ #1     review the config ch…  Checking context_window…            1m 2s ⠋ │
╰─ plan ✓ ✓ ▸ · 3/4 Check the eval thresholds ─────────────────────────────────╯
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

A:

```

 ● The downloader gives up on the first network error. Adding a retry with
     backoff.
                                                                           ▸ 1/4
   ├ read    internal/models/download.go                               212 lines
   ├ edit    internal/models/download.go                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to
     check the config change meanwhile.
                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://ex… started job job-3
   ├ agent   review the config change                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.        ▸ 3/4
   └ read    evals/thresholds.toml                                      64 lines
────────────────────────────────────────────────────────────────────────────────
▌┆ job-3  Download qwen3-code…  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB        2m 0s ⠋
▌⧉ #1     review the config c…  Checking context_window…                 1m 2s ⠋
 ✓ Add retry with backoff to the down…  ▸ Check the eval thresholds
 ✓ Start the weights download           · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local               Ctrl+C stop
```

B:

```
 ❯ Add a retry to the weights downloader, start the download, then check the
   eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with
     backoff.
                                                                           ▸ 1/4
   ├ read    internal/models/download.go                               212 lines
   ├ edit    internal/models/download.go                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to
     check the config change meanwhile.
                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://ex… started job job-3
   ├ agent   review the config change                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.        ▸ 3/4
   └ read    evals/thresholds.toml                                      64 lines


 ✓ ✓ ▸ ·  3/4  Check the eva…  ·  ┆ job-3 ━━━─── 57%  ·  ⧉ #1 Checking…
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local               Ctrl+C stop
```

C:

```
 ❯ Add a retry to the weights downloader, start the download, then check the
   eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with
     backoff.
                                                                           ▸ 1/4
   ├ read    internal/models/download.go                               212 lines
   ├ edit    internal/models/download.go                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to
     check the config change meanwhile.
                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://ex… started job job-3
   ├ agent   review the config change                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.        ▸ 3/4
   └ read    evals/thresholds.toml                                      64 lines

▌┆ job-3  Download qwen3-code…  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB        2m 0s ⠋
▌⧉ #1     review the config c…  Checking context_window…                 1m 2s ⠋
▍❯ Ask Marshal...
 Act · ⠋ 4m 12s                          ✓ ✓ ▸ ·  3/4  Check the eval thresholds
```

## A card open

B has no card in place; it opens `/work`.

Today:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen…  started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds







╭─ working 4m 12s ────────────────────────────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│  ⠋ Checking the eval thresholds still make sense for a coder model.                    ▸ 3/4 │
│    └ read    evals/thresholds.toml                                                  64 lines │
│                                                                                              │
├─ running 2 ──────────────────────────────────────────────────────────────────────────────────┤
│ ▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB            2m 0s ⠋ │
│ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━╸─────────────────────────────────── 57%       │
│ 57%  2.1 GiB/3.7 GiB                                                                         │
│ weights.gguf                                                                                 │
│ ↵ collapse · /work list                                                                      │
│ ▌⧉ #1     review the config change  Checking context_window…                         1m 2s ⠋ │
╰─ plan ✓ ✓ ▸ · 3/4 Check the eval thresholds ─────────────────────────────────────────────────╯
▍❯ Ask Marshal...
 Act · qwen3-coder @ ollama · local
```

A:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines







────────────────────────────────────────────────────────────────────────────────────────────────
▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━╸──────────────────────────────────── 57%
▌ 57%  2.1 GiB/3.7 GiB
▌ weights.gguf
▌ ↵ collapse · /work list
▌⧉ #1     review the config change  Checking context_window…                             1m 2s ⠋
 ✓ Add retry with backoff to the downloader     ▸ Check the eval thresholds
 ✓ Start the weights download                   · Run the harness tests
▍❯ Ask Marshal...
 Act · ⠋ working 4m 12s · qwen3-coder @ ollama · local                               Ctrl+C stop
```

C:

```
 ❯ Add a retry to the weights downloader, start the download, then check the eval thresholds.

 ● The downloader gives up on the first network error. Adding a retry with backoff.        ▸ 1/4
   ├ read    internal/models/download.go                                               212 lines
   ├ edit    internal/models/download.go                                                  +18 -3
   └ plan    ✓ ▸ · ·  2/4  Start the weights download

 ● Starting the weights download in the background, and asking a reviewer to check the config
     change meanwhile.
                                                                                           ▸ 2/4
   ├ shell   python3 .marshal/progress/download.py https://example.test/qwen3… started job job-3
   ├ agent   review the config change                                               ⧉ #1 started
   └ plan    ✓ ✓ ▸ ·  3/4  Check the eval thresholds

 ⠋ Checking the eval thresholds still make sense for a coder model.                        ▸ 3/4
   └ read    evals/thresholds.toml                                                      64 lines










▌┆ job-3  Download qwen3-coder weights  ━━━━━━━───── 57%  2.1 GiB/3.7 GiB                2m 0s ⠋
▌ ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━╸──────────────────────────────────── 57%
▌ 57%  2.1 GiB/3.7 GiB
▌ weights.gguf
▌ ↵ collapse · /work list
▌⧉ #1     review the config change  Checking context_window…                             1m 2s ⠋
▍❯ Ask Marshal...
 Act · ⠋ 4m 12s · qwen3-coder @ ollama                   ✓ ✓ ▸ ·  3/4  Check the eval thresholds
```

## Recommendation

(You chose A instead, for the todo list. The recommendation as written:)
**C, with A's keep-the-slot rule.** It is the only option that adds nothing
when nothing runs. The plan gets one fixed place, and running work keeps full
cards with bars, which is what the P-D progress skill draws. If having the
whole todo list on screen matters more than the rows it costs, A is the
fallback.

## What it would take

A rough scope, whichever option you choose. It is not a plan.
- Draw the live part in the transcript again. `SplitLive` stays in the
  projection, but the TUI renders history and the live part together, as it did
  before P-C.
- Cut `livebox` down to the pinned rows. The frame, the section headers, the
  layouts and their shed order go, and `[tui.live_box]` is deprecated with a
  warning, as `[tui.transcript]` was.
- Keep P-C's running-row and card renderers, its browse targets and its stop
  command.
- Move the turn clock and stop hint to the status line. C moves the plan line
  there too.
- P-D needs no change for A or C. Under B, check that `/work`'s card shows the
  `detail` and checklist the skill describes.

## Answers

1. **Option:** A, the tray, without the rule.
2. **"The other plan information":** not answered yet. The spec assumes the plan
   run (an SDD or swarm run) is a pinned row like a job.
3. **Finished items:** keep their row until the next prompt.
