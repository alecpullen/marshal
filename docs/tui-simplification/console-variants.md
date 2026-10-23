# Console variants

Status: proposal, 2026-10-23. Follows [`alternatives.md`](alternatives.md),
where Console was chosen. Decided here: 1, Framed, with 2, Split, as an
optional mode; continued in [`framed-console.md`](framed-console.md). Mockups in
colour: [`console-variants.html`](console-variants.html).

Every frame shows the same moment as round 1 at 96×34: a busy turn, plan 2/4, a
reviewer subagent and a background test run. History is identical in every
variant except Streaming, so the dock is the only thing that changes.

These stay from round 1:

- History only grows.
- A tint with a coloured `▌` edge marks running work, and only running work.
- A finished subagent or job lands in history as one completion event.
- The plan is visible whenever a list exists.

## The five docks

| | Idea | Dock rows | History rows | Plan shown as |
|---|---|---|---|---|
| Round 1 | Rule, live step, running rows, plan list | 11 | 21 | full list |
| 1. Framed | The dock is a bordered panel with labelled sections | 11 | 21 | full, two columns |
| 2. Split | Work on the left, plan on the right | 10 | 22 | full, wrapped |
| 3. Lanes | Everyone working right now gets an identical lane | 8 | 24 | stepper row |
| 4. Streaming | History streams line by line; the dock holds only what is still running | 5 | 27 | stepper row |
| 5. Shelf | Running work as bordered cards, side by side | 9 | 23 | numbered stepper |

Streaming's history rows include the live step's narration and finished calls,
which the other docks hold in the dock.

**Recommendation: 4, Streaming.** It is the simplest of the five: one rule
(nothing above the in-flight call ever changes), the smallest dock, and no step
moving from dock to history when it settles. If running work needs to stand out
more, Shelf's cards drop into Streaming's dock unchanged.

## 1. Framed

*The dock is a panel.* A bordered box with three labelled sections: now,
running, plan. The plan shows in full across two columns.

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
╭─ ⠋ working 3m00s ─────────────────────────────────────────────────────────── Ctrl+C ×2 stop ─╮
│ ⠋ Writing table tests for the backoff schedule.                                              │
│   ├ read    internal/llm/backoff.go                                                 48 lines │
│   └ write   internal/llm/client_retry_test.go                                           ⠋ 4s │
├─ running ────────────────────────────────────────────────────────────────────────────────────┤
│ ▌⧉ #1     review the retry loop   Checking that the backoff respects context canc…  1m02s  ⠋ │
│ ▌┆ job-1  go test ./...           ok  marshal/internal/agent  4.1s                  1m14s  ⠋ │
├─ plan 2/4 ───────────────────────────────────────────────────────────────────────────────────┤
│ ✓ Add a retry loop with exponential backoff     ▸ Table tests for the backoff schedule 1m40s │
│ ✓ Classify retryable errors (429, 5xx, resets)  · Run the full suite and get a review        │
╰──────────────────────────────────────────────────────────────────────────────────────────────╯
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Good:** the boundary between history and live work is unmistakable, and the
  labels make it readable on first use.
- **Costs:** the borders take 4 rows. The box can read as a modal sitting on the
  transcript, and it is the heaviest chrome of the five.

## 2. Split

*Work on the left, plan on the right.* The live step and running cards take the
left 58 columns; the plan takes the rest, wrapped rather than truncated.

```
✓ done · 48s · 1 beat · 2 calls · 9k tokens

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
⠋ Writing table tests for the backoff schedule.            │ Plan  ▰▰▱▱  2/4
  ├ read    internal/llm/backoff.go               48 lines │ ✓ Add a retry loop with exponential
  └ write   internal/llm/client_retry_test.go         ⠋ 4s │   backoff
▌⧉ #1 review the retry loop                       1m02s  ⠋ │ ✓ Classify retryable errors (429,
▌   Checking that the backoff respects context…            │   5xx, resets)
▌┆ job-1 go test ./...                            1m14s  ⠋ │ ▸ Table tests for the backoff
▌   ok  marshal/internal/agent  4.1s                       │   schedule                    1m40s
                                                           │ · Run the full suite and get a
                                                           │   review
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Good:** the full plan costs no extra rows, and running cards get two rows
  each.
- **Costs:** it is the only variant with columns, so it must stack below about
  90 columns, which is a breakpoint. Long todos wrap in the narrow column.

## 3. Lanes

*Everyone working right now gets a lane.* Marshal itself, each subagent and each
job get the same two-row lane: who, what, latest activity and elapsed time.

```
first one. Nothing retries, so a single 429 ends the turn.

✓ done · 48s · 1 beat · 2 calls · 9k tokens

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
now ───────────────────────────────────────────────────────────────────────────── Ctrl+C ×2 stop
▌● marshal  Writing table tests for the backoff schedule.                               3m00s  ⠋
▌           write  internal/llm/client_retry_test.go  4s
▌⧉ #1       review the retry loop                              qwen3-coder · 14 calls · 1m02s  ⠋
▌           Checking that the backoff respects context cancellation.
▌┆ job-1    go test ./...                                                               1m14s  ⠋
▌           ok  marshal/internal/agent  4.1s
 ✓ ✓ ▸ ·  2/4  Table tests for the backoff schedule   next · Run the full suite and get a… 1m40s
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Good:** parallel work reads uniformly and answers "what is happening" in one
  glance. It scales to pipelines and swarms: each role is a lane.
- **Costs:** Marshal's lane shows only its current call, so the step's finished
  calls appear when the step settles into history. The plan is one stepper row,
  with the full list on Ctrl+T.

## 4. Streaming (recommended)

*History streams line by line; the dock holds only what is still running.* With
a rail (`│`) instead of `├ └`, a call line never needs rewriting: round 1's
closing `└` had to change whenever another call arrived. So the narration and
each finished call append to history the moment they complete. The in-flight
call sits directly under them, and the step reads straight across the boundary.

```
first one. Nothing retries, so a single 429 ends the turn.

✓ done · 48s · 1 beat · 2 calls · 9k tokens

❯ Add retry with backoff to the provider client and cover it with tests

● Reading the client to see how requests are sent and where errors surface.
  │ read    internal/llm/client.go                                                     212 lines
  │ search  "doRequest"                                                      4 matches · 2 files

── 1/4  Add a retry loop with exponential backoff ──────────────────────────────────────────────
● Adding the retry loop around doRequest with jittered backoff.                       thought 3s
  │ edit    internal/llm/client.go                                                        +38 −6
  │ shell   go build ./internal/llm/...                                                   exit 0

── 2/4  Classify retryable errors (429, 5xx, resets) ───────────────────────────────────────────
● Splitting provider errors into retryable and final.
  │ edit    internal/llm/errors.go                                                        +21 −2
  │ shell   go test ./internal/llm/ -run Classify                                   ok · 6 tests

── 3/4  Table tests for the backoff schedule ───────────────────────────────────────────────────
● Starting the full suite in the background and asking for a review.
  │ shell   go test ./...                                                        ┆ job-1 started
  │ agent   review the retry loop                                                   ⧉ #1 started

● Writing table tests for the backoff schedule.
  │ read    internal/llm/backoff.go                                                     48 lines
  ⠋ write   internal/llm/client_retry_test.go                                                 4s

▌⧉ #1     review the retry loop   Checking that the backoff respects context cancella…  1m02s  ⠋
▌┆ job-1  go test ./...           ok  marshal/internal/agent  4.1s                      1m14s  ⠋
 ✓ ✓ ▸ ·  2/4  Table tests for the backoff schedule   next · Run the full suite and get… ⠋ 3m00s
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Good:**
  - It has the smallest dock and the most history.
  - History is the whole story, and nothing jumps when a step settles.
  - The wire stays simple: appended lines plus a small live payload.
- **Costs:**
  - There is no drawn border between history and live work. The spinner, the
    tint and the plan row carry that boundary instead.
  - The beat loses its visible end marker.
  - The plan is one stepper row.

## 5. Shelf

*Running work as cards on a shelf.* Subagents and jobs are bordered cards in
their kind's colour, two across. The plan is a numbered stepper.

```

✓ done · 48s · 1 beat · 2 calls · 9k tokens

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
╭ ⧉ #1 review the retry loop ──────── 1m02s ⠋ ╮ ╭ ┆ job-1 go test ./... ────────────── 1m14s ⠋ ╮
│ Checking that the backoff respects          │ │ ok  marshal/internal/agent  4.1s             │
│ context cancellation.              14 calls │ │ ok  marshal/internal/llm    2.3s     38 pkgs │
╰─────────────────────────────────────────────╯ ╰──────────────────────────────────────────────╯
 ✓ 1 ── ✓ 2 ── ▸ 3 Table tests for the backoff schedule ── · 4                  Plan 2/4 · 1m40s
❯ Ask Marshal…
act · qwen3-coder@ollama · ctx 34% · ±2 files                          main · ~/projects/marshal
```

- **Good:** this is the most unmistakable look for running work. Two cards cost
  the same 4 rows as one, and each card has room for a second activity line.
- **Costs:** a third card needs a second shelf row or a `+1` card. Borders in the
  kind colour put a lot of colour on screen. The plan is a stepper.

## History styles

The history grammar is separate from the dock. Four options for the same step:

**Spine** (round 1). Membership is drawn and the end of the step is marked. The
closing `└` must be rewritten when a call arrives, so it cannot stream.

```
● Adding the retry loop around doRequest with jittered backoff.
  ├ edit    internal/llm/client.go                                      +38 −6
  ├ shell   go build ./internal/llm/...                                 exit 0
  └ shell   go test ./internal/llm/                              ok · 41 tests
```

**Rail.** Same membership, never rewritten. The next `●` or a blank line ends
the step.

```
● Adding the retry loop around doRequest with jittered backoff.
  │ edit    internal/llm/client.go                                      +38 −6
  │ shell   go build ./internal/llm/...                                 exit 0
  │ shell   go test ./internal/llm/                              ok · 41 tests
```

**Hook.** Each result on its own line under its call. Twice the rows per call.

```
● Adding the retry loop around doRequest with jittered backoff.
  edit internal/llm/client.go
    ⎿ +38 −6
  shell go build ./internal/llm/...
    ⎿ exit 0
  shell go test ./internal/llm/
    ⎿ ok · 41 tests
```

**Gutter.** Verbs right-aligned in a left gutter, like a log. The verb column
scans quickly; narration is indented and loses some width.

```
       ● Adding the retry loop around doRequest with jittered backoff.
  edit   internal/llm/client.go                                         +38 −6
 shell   go build ./internal/llm/...                                    exit 0
 shell   go test ./internal/llm/                                 ok · 41 tests
```

## Questions

1. Which dock, or which mix? The pieces are separable: for example, Streaming's
   dock with Shelf's cards and Split's plan.
2. Which history style?
3. What didn't work in round 1? The separation between history and dock, the
   plan, the running rows, or the history grammar?
