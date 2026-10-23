# Framed console

Status: design settled, 2026-10-23. The spec is
[`feature-spec.md`](feature-spec.md); this file keeps the frames. Earlier
rounds: [`alternatives.md`](alternatives.md) (layout) and
[`console-variants.md`](console-variants.md) (dock). Mockups in colour:
[`framed-console.html`](framed-console.html).

## Decisions so far

| Question | Decision |
|---|---|
| Layout | Console: history only grows; everything live sits in one dock above the input. |
| Dock | A framed box with labelled sections (the work in progress, running work, the plan), plus an optional split mode. |
| Plan modes | **Ctrl+T cycles stacked → split → folded.** `[tui.dock] layout` sets the starting mode. Split needs about 90 columns and is skipped on narrower frames. |
| Todo dividers | Removed from history. |
| Progress in history | A **plan line** where the agent updated the list, plus a **numbered tag** (`▸ 2/4`) on the first step of each todo. |
| Progress widgets | The live-work progress cards move into the box's running section (see "Progress cards" below). |

The frames use a session that exercises the progress cards. It shows a download
and a LAN transfer reporting progress, a Go test run and a reviewer subagent,
all running while the agent edits a file.

## Stacked

```

● Planning this in four steps.
  └ plan    ▸ · · ·  1/4  Start the weights download and the eval-set copy

● Starting the download and the copy in the background.                                    ▸ 1/4
  ├ shell   python3 scripts/fetch_model.py qwen3-coder                           ┆ job-1 started
  ├ shell   python3 scripts/push_evals.py evals/data media-box                   ┆ job-2 started
  └ plan    ✓ ▸ · ·  2/4  Point evals/config.toml at the new model

● Pointing the eval config at qwen3-coder.                                    thought 2s · ▸ 2/4
  ├ edit    evals/config.toml                                                              +3 −3
  └ agent   review the config change                                                ⧉ #1 started

● Checking the tokenizer settings match the new model.
  ├ read    evals/tokenizer.toml                                                        18 lines
  └ plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests

● Running the harness tests while the transfers finish.                                    ▸ 3/4
  └ shell   go test -json ./evals/...                                            ┆ job-3 started
╭─ ⠋ working 4m12s ───────────────────────────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│ ⠋ Checking the eval thresholds still make sense for a coder model.                           │
│   ├ read    evals/thresholds.toml                                                   22 lines │
│   └ edit    evals/thresholds.toml                                                       ⠋ 2s │
├─ running 4 ──────────────────────────────────────────────────────────────────────────────────┤
│ ▌⧉ #1     review the config change      Checking context_window against the card.   1m02s  ⠋ │
│ ▌┆ job-1  Download qwen3-coder weights  ━━━━━━━╸────── 57%  6.8/12.0 GiB · ~1m14s   2m03s  ⠋ │
│ ▌┆ job-2  Copy eval set → media-box     ━━━━━━──────── 46%  142/216 files · ~48s    1m40s  ⠋ │
│ ▌┆ job-3  Harness tests                 184 passed · 0 failed · 9 packages          0m31s  ⠋ │
├─ plan  ▰▰▱▱ 2/4 ─────────────────────────────────────────────────────────────────────────────┤
│ ✓ Start the weights download and the eval-set copy  ▸ Run the eval harness unit tests  0m31s │
│ ✓ Point evals/config.toml at the new model          · Run the eval suite on media-box        │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Running work** is one row per item. A measured item shows a thin bar in its
  kind's colour, its percentage and the most useful measurement. A test run
  shows its counts, and a subagent shows its latest activity.
- **The plan section** shows the full list in two columns.
- **Dock:** 13 rows at 96×34 with four running items, leaving 19 for history.

## One card expanded

```
● Starting the download and the copy in the background.                                    ▸ 1/4
  ├ shell   python3 scripts/fetch_model.py qwen3-coder                           ┆ job-1 started
  ├ shell   python3 scripts/push_evals.py evals/data media-box                   ┆ job-2 started
  └ plan    ✓ ▸ · ·  2/4  Point evals/config.toml at the new model

● Pointing the eval config at qwen3-coder.                                    thought 2s · ▸ 2/4
  ├ edit    evals/config.toml                                                              +3 −3
  └ agent   review the config change                                                ⧉ #1 started

● Checking the tokenizer settings match the new model.
  ├ read    evals/tokenizer.toml                                                        18 lines
  └ plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests

● Running the harness tests while the transfers finish.                                    ▸ 3/4
  └ shell   go test -json ./evals/...                                            ┆ job-3 started
╭─ ⠋ working 4m12s ───────────────────────────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│ ⠋ Checking the eval thresholds still make sense for a coder model.                           │
│   ├ read    evals/thresholds.toml                                                   22 lines │
│   └ edit    evals/thresholds.toml                                                       ⠋ 2s │
├─ running 4 ──────────────────────────────────────────────────────────────────────────────────┤
│ ▌⧉ #1     review the config change      Checking context_window against the card.   1m02s  ⠋ │
│ ▌┆ job-1  Download qwen3-coder weights                                   download · 2m03s  ⠋ │
│ ▌          ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━───────────────────────────  57%               │
│ ▌          6.8 / 12.0 GiB · 72 MiB/s · ~1m14s left                                           │
│ ▌          qwen3-coder-30b-q4_k_m.gguf → models/qwen3-coder.gguf                             │
│ ▌          ↵ output   s stop   esc collapse                                                  │
│ ▌┆ job-2  Copy eval set → media-box     ━━━━━━──────── 46%  142/216 files · ~48s    1m40s  ⠋ │
│ ▌┆ job-3  Harness tests                 184 passed · 0 failed · 9 packages          0m31s  ⠋ │
├─ plan  ▰▰▱▱ 2/4 ─────────────────────────────────────────────────────────────────────────────┤
│ ✓ Start the weights download and the eval-set copy  ▸ Run the eval harness unit tests  0m31s │
│ ✓ Point evals/config.toml at the new model          · Run the eval suite on media-box        │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

One item at a time can open to at most five rows: a full-width bar, the
measurement with rate and ETA, the detail line, and its actions. The other rows
stay one row each. Enter on a running row in browse mode opens it, as does a
click. In browse mode, `j` past the last history item moves into the box.

## Split mode

```
  ├ read    evals/config.toml                                                           64 lines
  └ search  "model_path"                                                     3 matches · 2 files

● Planning this in four steps.
  └ plan    ▸ · · ·  1/4  Start the weights download and the eval-set copy

● Starting the download and the copy in the background.                                    ▸ 1/4
  ├ shell   python3 scripts/fetch_model.py qwen3-coder                           ┆ job-1 started
  ├ shell   python3 scripts/push_evals.py evals/data media-box                   ┆ job-2 started
  └ plan    ✓ ▸ · ·  2/4  Point evals/config.toml at the new model

● Pointing the eval config at qwen3-coder.                                    thought 2s · ▸ 2/4
  ├ edit    evals/config.toml                                                              +3 −3
  └ agent   review the config change                                                ⧉ #1 started

● Checking the tokenizer settings match the new model.
  ├ read    evals/tokenizer.toml                                                        18 lines
  └ plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests

● Running the harness tests while the transfers finish.                                    ▸ 3/4
  └ shell   go test -json ./evals/...                                            ┆ job-3 started
╭─ ⠋ working 4m12s ──────── Ctrl+T plan · Ctrl+C ×2 stop ─┬─ plan  ▰▰▱▱ 2/4 ───────────────────╮
│ ⠋ Checking the eval thresholds still make sense for a   │ ✓ Start the weights download and   │
│   coder model.                                          │   the eval-set copy                │
│   ├ read    evals/thresholds.toml              22 lines │ ✓ Point evals/config.toml at the   │
│   └ edit    evals/thresholds.toml                  ⠋ 2s │   new model                        │
├─ running 4 ─────────────────────────────────────────────┤ ▸ Run the eval harness unit        │
│ ▌⧉ #1    review config       Checking context… 1m02s  ⠋ │   tests                      0m31s │
│ ▌┆ job-1 Download weights    ━━━━━╸──── 57%    2m03s  ⠋ │ · Run the eval suite on media-box  │
│ ▌┆ job-2 Copy eval set       ━━━━╸───── 46%    1m40s  ⠋ │                                    │
│ ▌┆ job-3 Harness tests       184 passed        0m31s  ⠋ │                                    │
╰─────────────────────────────────────────────────────────┴────────────────────────────────────╯
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

Running rows stay one row each in the narrower left column, keeping the bar
and percentage. The plan wraps in the right column, and the live step's
narration wraps on the left.

## Folded

```
● Checking how the eval harness finds its model and data.
  ├ read    evals/config.toml                                                           64 lines
  └ search  "model_path"                                                     3 matches · 2 files

● Planning this in four steps.
  └ plan    ▸ · · ·  1/4  Start the weights download and the eval-set copy

● Starting the download and the copy in the background.                                    ▸ 1/4
  ├ shell   python3 scripts/fetch_model.py qwen3-coder                           ┆ job-1 started
  ├ shell   python3 scripts/push_evals.py evals/data media-box                   ┆ job-2 started
  └ plan    ✓ ▸ · ·  2/4  Point evals/config.toml at the new model

● Pointing the eval config at qwen3-coder.                                    thought 2s · ▸ 2/4
  ├ edit    evals/config.toml                                                              +3 −3
  └ agent   review the config change                                                ⧉ #1 started

● Checking the tokenizer settings match the new model.
  ├ read    evals/tokenizer.toml                                                        18 lines
  └ plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests

● Running the harness tests while the transfers finish.                                    ▸ 3/4
  └ shell   go test -json ./evals/...                                            ┆ job-3 started
╭─ ⠋ working 4m12s ───────────────────────────────────────────── Ctrl+T plan · Ctrl+C ×2 stop ─╮
│ ⠋ Checking the eval thresholds still make sense for a coder model.                           │
│   ├ read    evals/thresholds.toml                                                   22 lines │
│   └ edit    evals/thresholds.toml                                                       ⠋ 2s │
├─ running 4 ──────────────────────────────────────────────────────────────────────────────────┤
│ ▌⧉ #1     review the config change      Checking context_window against the card.   1m02s  ⠋ │
│ ▌┆ job-1  Download qwen3-coder weights  ━━━━━━━╸────── 57%  6.8/12.0 GiB · ~1m14s   2m03s  ⠋ │
│ ▌┆ job-2  Copy eval set → media-box     ━━━━━━──────── 46%  142/216 files · ~48s    1m40s  ⠋ │
│ ▌┆ job-3  Harness tests                 184 passed · 0 failed · 9 packages          0m31s  ⠋ │
╰─ plan ✓ ✓ ▸ ·  3/4 Run the eval harness unit tests ──────────────────────────────────────────╯
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

The plan folds into the bottom border as a stepper with the active todo.

## At 80×24

```
  ├ shell   python3 scripts/push_evals.py evals/data media-box   ┆ job-2 started
  └ plan    ✓ ▸ · ·  2/4  Point evals/config.toml at the new model

● Pointing the eval config at qwen3-coder.                    thought 2s · ▸ 2/4
  ├ edit    evals/config.toml                                              +3 −3
  └ agent   review the config change                                ⧉ #1 started

● Checking the tokenizer settings match the new model.
  ├ read    evals/tokenizer.toml                                        18 lines
  └ plan    ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests

● Running the harness tests while the transfers finish.                    ▸ 3/4
  └ shell   go test -json ./evals/...                            ┆ job-3 started
╭─ ⠋ working 4m12s ─────────────────────────────────────────── Ctrl+C ×2 stop ─╮
│ ⠋ Checking the eval thresholds still make sense for a coder model.           │
│   └ edit    evals/thresholds.toml                                       ⠋ 2s │
├─ running 4 ──────────────────────────────────────────────────────────────────┤
│ ▌⧉ #1     review the config c…  Checking context_window…            1m02s  ⠋ │
│ ▌┆ job-1  Download qwen3-code…  ━━━━━━╸───── 57%  ~1m14s            2m03s  ⠋ │
│ ▌┆ job-2  Copy eval set → med…  ━━━━━╸────── 46%  ~48s              1m40s  ⠋ │
│ +1 more · ┆ job-3 Harness tests · /work                                      │
╰─ plan ✓ ✓ ▸ ·  3/4 Run the eval harness unit tests ──────────────────────────╯
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                               main
```

When space is short the box sheds in a fixed order:

1. The plan folds into the bottom border.
2. The live step drops to its narration and its current call.
3. Running rows cap at 3, followed by `+N more · /work`.

The dock here is 9 rows, leaving 13 for history.

## Progress in history

- **Plan line.** A `todo.write` call is drawn as an ordinary call line where it
  happened: `└ plan  ✓ ✓ ▸ ·  3/4  Run the eval harness unit tests`. Several
  writes in one step merge into one line showing the final state. If the model
  never updates the list, no marker appears.
- **Numbered tag.** The first step taken under each todo carries `▸ N/M` on its
  right edge, after any `thought Ns`. Later steps under the same todo carry no
  tag. In the frames, "Checking the tokenizer settings…" is the second step of
  todo 2 and has none.

## Progress cards (live work, rolled in)

The progress widget is live-work stages 2 and 3
([`docs/live-work/feature-spec.md`](../live-work/feature-spec.md)), merged on
main. It covers command-bound progress on `shell.run` and `test.run`, the
`@@marshal-progress/v1` stdout records, the generic, tests, download, transfer
and custom profiles, the Go test JSON adapter, and retained final progress.
Measured producers are in
[`docs/live-work/examples/`](../live-work/examples/README.md). The rework keeps
all of that data and changes only how it is shown and taught.

```
Collapsed: one row per item, the same in stacked and split mode

▌┆ job-4  npm run build                 > vite build · transforming (412) src/componen… 0m22s  ⠋
▌┆ job-3  Harness tests                 184 passed · 2 failed · 9 packages              0m48s  ⠋
▌┆ job-1  Download qwen3-coder weights  ━━━━━━━━━━──────── 57%  6.8/12.0 GiB · ~1m14s   2m03s  ⠋
▌┆ job-5  Download eval cache           2.1 GiB · 40 MiB/s · size unknown               0m54s  ⠋
▌┆ job-2  Copy eval set → media-box     ━━━━━━━━────────── 46%  142/216 files · ~48s    1m40s  ⠋
▌┆ job-6  Prepare model bundle          2/4 Verify files ━━━━━━━╸──── 63% of phase      4m10s  ⠋
▌┆ job-7  Sync checkpoints              ━━━━━╸──────────── 31%  ⚠ no update for 42s     6m02s  ⠋
▌⧉ #2     update the eval README        ⚠ needs approval: shell git push                0m40s  ⠋

Expanded: one item at a time, up to five rows (Enter in browse mode, or a click)

▌┆ job-6  Prepare model bundle                                                 custom · 4m10s  ⠋
▌          Phase 2/4 · Verify files  ━━━━━━━━━━━━━━━━━━━━━━━━━━━╸────────────────  63% of phase
▌          1,280/2,048 files · weights/layer-028.bin
▌          ✓ Extract archive   ⠋ Verify files   · Build index   · Package
▌          ↵ output   s stop   esc collapse

Finished: the row leaves and one event lands in history, with the final measurement

┆ job-1 Download qwen3-coder weights · done · 12.0 GiB in 2m48s · 71 MiB/s                     ▹
    models/qwen3-coder.gguf
┆ job-2 Copy eval set → media-box · done · 216 files, 8.0 GiB in 2m31s                         ▹
    workstation → media-box.local:/srv/evals
┆ job-3 Harness tests · exit 1 · 210 passed, 2 failed · 48s                                    ▹
    --- FAIL: TestThresholdsForCoderModels/context_window
```

### What changes

| Today (`nowbar.go`) | In the framed box |
|---|---|
| Cards in the now bar, two rows collapsed and six expanded | One row collapsed, one card expanded at a time to five rows, in the running section |
| Text-only measurement: `6.8/12.0 GiB · 57% · 72 MiB/s · ~1m14s` | A thin measured bar, plus the percentage and the most useful measurement |
| A separate one-row layout below 30 rows; a `min(10, h/4)` budget | The box's shed order (above) |
| A five-second success receipt row, then retirement | A completion event in history with the final measurement |
| An aggregate attention row for failures | A red completion event in history (acknowledgement still open; see questions) |
| Ctrl+T opens the Tasks panel (Plan and Work tabs) | Ctrl+T cycles the plan modes; the Work list stays on `/work` and the `+N more` row |

The bar appears only when the producer reported a total. Without one the row
shows the count and the rate, never a percentage. The rules are unchanged from
live work: no invented denominators, a phase-local percentage is labelled "of
phase", stale measurements hide rate and ETA and show their age, and 100%
reported while the process still runs says so.

### Teaching the agent

The model sees `progress` in the `shell.run` and `test.run` schemas (profile,
source and title, by design), but nothing in its context describes the
`@@marshal-progress/v1` record. It can opt in to a profile, but it cannot write
its own download, transfer or custom producer without first finding
`docs/live-work/`.

Proposal: a built-in skill, `progress-reporting`, holding the record format,
its limits and the three example producers. The skill loads only when relevant,
so the record format stays out of the cached prompt prefix. The `progress`
parameter in both schemas gains a short description pointing at the skill.

## Answers (2026-10-23)

1. **Ctrl+T and the Tasks panel:** fine for now. The keyboard shortcuts will be
   reworked later, so none of the keys here are fixed.
2. **Failed background work:** a red completion event for now. Expanding it, like
   any other entry, shows a fuller issue report.
3. **`progress-reporting` skill:** yes.
4. **Consolidated spec:** yes. See [`feature-spec.md`](feature-spec.md).
