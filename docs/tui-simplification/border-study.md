# Round 7: the framed console

Date: 2026-10-27. Status: decided. Spec: [feature-spec.md](feature-spec.md)
§5.9. Plan: [P-F](plans/2026-10-27-p-f-framed-console-plan.md). Colour
version: [border-study.html](border-study.html).

After round 6 put the frame back around the tray, the user asked where else
the tray's border could go, and asked for the surface tint to go from the
prompt input line as it had already gone from the status line. This round
mocked up every placement. Every "Today" frame below is a real capture of
`marshal` at 100×34 with `depth = "raised"`, driven against qwen3.5-9b and
nemotron-3.5 in LM Studio. Each "Chosen" frame keeps the captured transcript
and tray rows and redraws only the chrome.

## Decision

The user took the recommended option at every placement, including the
optional drill-in frame:

| Placement | Chosen | Rows against today |
|---|---|---|
| Prompt input | Framed, with the status line as its bottom border; no tint | +1 at idle |
| Tray and prompt | One outline: the tray's `╰─╯` becomes `├─┤` | 0 |
| Command popup | A section of the outline, its keys in the header | 0 |
| Access and approval prompts | An amber section; title and keys in the header | −3 |
| Questions | A violet section | −1 |
| Session sheet | Sections as `├─ NAME ─┤` dividers inside the outline | fewer |
| Full-frame panels | One closed frame; groups as dividers; the count in the bottom border | 0 |
| Subagent drill-in | A coral frame with the crumb in its top border | +1, 4 columns narrower |
| Transcript history | Stays unframed | — |

The rule behind every choice: **a frame means live, or waiting on you.**
History keeps the spine and the gutter. Chrome paints no tint; the only tint
left marks work that is still moving (running tray rows and the live thinking
row). A border in a state colour means Marshal is asking: amber for a decision,
violet for a question or a focused panel, coral inside a child. The section's
label says the same thing in words, so it still reads without colour.

## Frames

### Prompt input

Today. depth = raised: the prompt row is painted with the surface tint.

```
  ● marshal · local-friendly coding agent

  Type a question, or / for commands.





























▍❯ Ask Marshal...
 Act · untrusted · qwen/qwen3.5-9b @ lmstudio · local · think default · ±1 file · repo · ⎇ main
```

Chosen: Framed · status in border. +1 row at idle. The status line is the composer's bottom border.

```
  ● marshal · local-friendly coding agent

  Type a question, or / for commands.




























╭──────────────────────────────────────────────────────────────────────────────────────────────────╮
│ ❯ Ask Marshal...                                                                                 │
╰─ Act · untrusted · qwen/qwen3.5-9b @ lmstudio · local · think default · ±1 file · repo · ⎇ main ─╯
```

### Tray and prompt as one outline

Today. The tray closes with ╰─╯; the prompt sits under it on a tinted band.

```
   └ shell   go test ./... &                                                         ┆ job-1 started

 ┆ job-1 go test ./... & · done · 0s                                                               ▹
     ./calc_test.go:12:12: undefined: Subtract

 ● Spawn subagent to review calc.go for bugs while tests run in background.              thought 14s
   └ agent   Review calc.go for bugs while tests run in background                     ⧉ #1 started

 ● Wait for both the background test job and subagent to complete before           thought 16s · 16s
     proceeding.
   └ agent   await                                                                         waited 0s

 ● Peek at subagent progress and find calc.go file.                                      thought 12s
   ├ agent   await                                                                         waited 0s
   └ search  "calc.go"                                                 found 0 matches (mode: regex)

 ● Search for calc.go using substring mode to find the file.                              thought 8s
   └ search  "calc.go"                                                               found 0 matches

 ⠦ thinking                                                                                      10s
 ▍ The subagent is still running and hasn't found calc.go yet. Let me wait a bit more to see if it
 ▍ completes its task, or I can check the git status to understand what files exist in the
 ▍ repository.

╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done · ./calc_test.go:12:12: undefined: Subtract                 0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                    46s ⠦ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · The repo.search didn't find calc.go with wildcard patter… │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
▍❯ Ask Marshal...
 Act · untrusted · ⠦ working 2m 41s · qwen/qwen3.5-9b @ lmstudio · local · ctx 6k/32k   Ctrl+C stop
```

Chosen: Joined. 0 rows. The tray's ╰─╯ becomes ├─┤ and the status line closes the outline. The status line sheds ctx 6k/32k to fit.

```
   └ shell   go test ./... &                                                         ┆ job-1 started

 ┆ job-1 go test ./... & · done · 0s                                                               ▹
     ./calc_test.go:12:12: undefined: Subtract

 ● Spawn subagent to review calc.go for bugs while tests run in background.              thought 14s
   └ agent   Review calc.go for bugs while tests run in background                     ⧉ #1 started

 ● Wait for both the background test job and subagent to complete before           thought 16s · 16s
     proceeding.
   └ agent   await                                                                         waited 0s

 ● Peek at subagent progress and find calc.go file.                                      thought 12s
   ├ agent   await                                                                         waited 0s
   └ search  "calc.go"                                                 found 0 matches (mode: regex)

 ● Search for calc.go using substring mode to find the file.                              thought 8s
   └ search  "calc.go"                                                               found 0 matches

 ⠦ thinking                                                                                      10s
 ▍ The subagent is still running and hasn't found calc.go yet. Let me wait a bit more to see if it
 ▍ completes its task, or I can check the git status to understand what files exist in the
 ▍ repository.

╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done · ./calc_test.go:12:12: undefined: Subtract                 0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                    46s ⠦ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · The repo.search didn't find calc.go with wildcard patter… │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
├──────────────────────────────────────────────────────────────────────────────────────────────────┤
│ ❯ Ask Marshal...                                                                                 │
╰─ Act · untrusted · ⠦ working 2m 41s · qwen/qwen3.5-9b @ lmstudio · local ────────── Ctrl+C stop ─╯
```

### Command popup

Today. The popup is a ▍ panel inside the tinted input band (its title row is tinted).

```
 ● Search for calc.go using substring mode to find the file.                              thought 8s
   └ search  "calc.go"                                                               found 0 matches

 ● Wait for subagent to complete its review of calc.go.                                  thought 11s
   └ agent   await                                                                         waited 0s

 ● Peek at subagent progress to see what it's doing.                                      thought 8s
   └ agent   await                                                                         waited 0s

 ● Wait for subagent to complete its review.                                             thought 10s
   └ agent   await                                                                         waited 0s

 ⠋ thinking                                                                                       3s
 ▍ The subagent has found calc.go and needs to read it. Let me

╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done · ./calc_test.go:12:12: undefined: Subtract                 0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                  1m 7s ⠋ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · Found calc.go.                                            │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
▍ Commands                                                       ↑↓ select · ↵ run · esc dismiss
▍ ▸ /act  Act mode: routine workspace work proceeds without asking (Auto off)
▍   /agents  Configure the agent roster: roles, models, custom agents, swarm & SDD budgets
▍   /ask  Ask mode: every new action needs approval (Auto off)
▍   /auto  Toggle Full Auto: preapprove operations and System access, with no questions; /auto o
▍   /branches  List branches and switch to one (e.g. /branches 2)
▍   /clear  Start a new conversation (alias for /new)
▍   /config  Show current configuration summary
▍   /connect  Add or reconnect a provider
▍❯ /
 Act · untrusted · ⠋ working 3m 2s · qwen/qwen3.5-9b @ lmstudio · local · ctx 7k/32k    Ctrl+C stop
```

Chosen: Joined. 0 rows. The popup's title becomes a section header with its keys on the right.

```
 ● Search for calc.go using substring mode to find the file.                              thought 8s
   └ search  "calc.go"                                                               found 0 matches

 ● Wait for subagent to complete its review of calc.go.                                  thought 11s
   └ agent   await                                                                         waited 0s

 ● Peek at subagent progress to see what it's doing.                                      thought 8s
   └ agent   await                                                                         waited 0s

 ● Wait for subagent to complete its review.                                             thought 10s
   └ agent   await                                                                         waited 0s

 ⠋ thinking                                                                                       3s
 ▍ The subagent has found calc.go and needs to read it. Let me

╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done · ./calc_test.go:12:12: undefined: Subtract                 0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                  1m 7s ⠋ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · Found calc.go.                                            │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
├─ commands ───────────────────────────────────────────────────── ↑↓ select · ↵ run · esc dismiss ─┤
│ ▸ /act  Act mode: routine workspace work proceeds without asking (Auto off)                      │
│   /agents  Configure the agent roster: roles, models, custom agents, swarm & SDD budgets         │
│   /ask  Ask mode: every new action needs approval (Auto off)                                     │
│   /auto  Toggle Full Auto: preapprove operations and System access, with no questions; /auto o   │
│   /branches  List branches and switch to one (e.g. /branches 2)                                  │
│   /clear  Start a new conversation (alias for /new)                                              │
│   /config  Show current configuration summary                                                    │
│   /connect  Add or reconnect a provider                                                          │
├──────────────────────────────────────────────────────────────────────────────────────────────────┤
│ ❯ /                                                                                              │
╰─ Act · untrusted · ⠋ working 3m 2s · qwen/qwen3.5-9b @ lmstudio · local ─────────── Ctrl+C stop ─╯
```

### Access and approval prompts

Today. Title row, body, a hint row, a blank row, then the status line.

```
 ● The tool output is consistently showing only lines 1-6 of  calc.go . I must assume the file is
     larger than 6 lines, and the previous tool call was not sufficient to retrieve the whole
     content. I need to try reading up to a larger line number, say 100, to see if more code is
     available. If this still only returns lines 1-6, I will proceed by adding the function at the
     end of the existing code block.
   └ read    calc.go                                                          read calc.go lines 1-6

 ✗ The tool output is still only lines 1-6, despite requesting up to line 100. Since I cannot    22s
     retrieve the rest of the file through  file.read  in this manner, and the user specifically
     requested to add  Subtract(a, b int) int  to make tests compile, I will append the new function
     immediately after  Add  in the existing code block, using  file.edit  to replace the last line
     of  Add 's body plus a newline, or more safely, by overwriting the whole file content if I
     assume it's small enough. Given  file.edit  requires an exact match, and I only have lines 1-6,
     I will append the new function after the existing content block, effectively overwriting lines
     1-6 with all known code plus the new function.
╭─ running 1 ──────────────────────────────────────────────────────────────────────────────────────╮
│ ▌⧉ #prom… awaiting an access decision                                                       2s ⠴ │
│ ▌         ⚠ needs access                                                                         │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... in background.               · Fix any failures identified by tests or suba… │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
▍ Allow editing?
▍ Current: Ask · rev 2
▍ Request: Act; for this conversation
▍ Editing uses normal Act; policy and sandbox still apply; sudo is separate.
▍ From Marshal / step 44. To implement the user's request to add Subtract(a, b int) int to calc.go
▍ and fix compilation failures.
▍ > Deny
▍   Act
▍ Arrows/Tab choose; Enter decide; Esc deny

 Ask · untrusted · ⠴ working 1m 34s · nvidia-nemotron-3.5-bolt-e4b-mlx @ lmstudio        ⚠ approval
```

Chosen: Framed. −3 rows. Title and keys move into the amber header; the status line closes it.

```
 ● The tool output is consistently showing only lines 1-6 of  calc.go . I must assume the file is
     larger than 6 lines, and the previous tool call was not sufficient to retrieve the whole
     content. I need to try reading up to a larger line number, say 100, to see if more code is
     available. If this still only returns lines 1-6, I will proceed by adding the function at the
     end of the existing code block.
   └ read    calc.go                                                          read calc.go lines 1-6

 ✗ The tool output is still only lines 1-6, despite requesting up to line 100. Since I cannot    22s
     retrieve the rest of the file through  file.read  in this manner, and the user specifically
     requested to add  Subtract(a, b int) int  to make tests compile, I will append the new function
     immediately after  Add  in the existing code block, using  file.edit  to replace the last line
     of  Add 's body plus a newline, or more safely, by overwriting the whole file content if I
     assume it's small enough. Given  file.edit  requires an exact match, and I only have lines 1-6,
     I will append the new function after the existing content block, effectively overwriting lines
     1-6 with all known code plus the new function.
╭─ running 1 ──────────────────────────────────────────────────────────────────────────────────────╮
│ ▌⧉ #prom… awaiting an access decision                                                       2s ⠴ │
│ ▌         ⚠ needs access                                                                         │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... in background.               · Fix any failures identified by tests or suba… │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
├─ ⚠ allow editing? ───────────────────────────────────────── ↑↓/Tab choose · ↵ decide · Esc deny ─┤
│ Current: Ask · rev 2                                                                             │
│ Request: Act; for this conversation                                                              │
│ Editing uses normal Act; policy and sandbox still apply; sudo is separate.                       │
│ From Marshal / step 44. To implement the user's request to add Subtract(a, b int) int to calc.go │
│ and fix compilation failures.                                                                    │
│ > Deny                                                                                           │
│   Act                                                                                            │
╰─ Ask · untrusted · ⠴ working 1m 34s · nvidia-nemotron-3.5-bolt-e4b-mlx @ lmstudio ── ⚠ approval ─╯
```

### Questions

Today. Violet ▍ rail, an answer field, then a blank row above the status line.

```
 · Agent failed: read stream: context canceled

 · Turn stopped.

 · Agent turn cancelled.

 · Switched to Ask mode. New actions need approval.

   ─────────────────────────────────────────────────────────────────────────────────────────────────

 ❯ Run go vet ./... with shell.run and tell me the result.

 · Turn interrupted (read stream: context canceled). The next turn continues from this state.

 ✗ Running go vet as requested.                                                    thought 20s · 20s
   └ shell   go vet ./...                                denied: Ask admits only read-only operatio…
             denied: Ask admits only read-only operations

 ⠋ I need Act mode to run go vet via shell.run, which is a mutating operation.   thought 9s · 1m 10s

╭─ running 2 ──────────────────────────────────────────────────────────────────────────────────────╮
│ ▌⧉ #2     Review calc.go for bugs whil…                                                 1m 52s ⠋ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · The file.read tool is truncating results showing only 6 … │
│ ▌⧉ #prom… awaiting an answer                                                             1m 1s ⠋ │
│ ▌         ⚠ needs an answer                                                                      │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
▍ ? Run  go vet ./...  — grant Act mode to allow this command.
▍   ┃ ❯

 Ask · untrusted · ⠋ working 1m 40s · qwen/qwen3.5-9b @ lmstudio · local · ctx 5k/32k   Ctrl+C stop
```

Chosen: Framed. −1 row. The prompt takes the composer's section and turns its border violet.

```
 · Agent failed: read stream: context canceled

 · Turn stopped.

 · Agent turn cancelled.

 · Switched to Ask mode. New actions need approval.

   ─────────────────────────────────────────────────────────────────────────────────────────────────

 ❯ Run go vet ./... with shell.run and tell me the result.

 · Turn interrupted (read stream: context canceled). The next turn continues from this state.

 ✗ Running go vet as requested.                                                    thought 20s · 20s
   └ shell   go vet ./...                                denied: Ask admits only read-only operatio…
             denied: Ask admits only read-only operations

 ⠋ I need Act mode to run go vet via shell.run, which is a mutating operation.   thought 9s · 1m 10s

╭─ running 2 ──────────────────────────────────────────────────────────────────────────────────────╮
│ ▌⧉ #2     Review calc.go for bugs whil…                                                 1m 52s ⠋ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · The file.read tool is truncating results showing only 6 … │
│ ▌⧉ #prom… awaiting an answer                                                             1m 1s ⠋ │
│ ▌         ⚠ needs an answer                                                                      │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
├─ ? question ──────────────────────────────────────────────────────────── ↵ answer · Esc dismiss ─┤
│ Run  go vet ./...  — grant Act mode to allow this command.                                       │
│ ❯                                                                                                │
╰─ Ask · untrusted · ⠋ working 1m 40s · qwen/qwen3.5-9b @ lmstudio · local ────────── Ctrl+C stop ─╯
```

### Session sheet

Today. Ctrl+B: a ▍ panel docked between the tray and the prompt.

```
 ● Spawn subagent to review calc.go for bugs while tests run in background.              thought 14s
   └ agent   Review calc.go for bugs while tests run in background                      ⧉ #1 started

 ● Wait for both the background test job and subagent to complete before           thought 16s · 16s
     proceeding.
   └ agent   await                                                                         waited 0s

 ● Peek at subagent progress and find calc.go file.                                      thought 12s
   ├ agent   await                                                                         waited 0s
   └ search  "calc.go"                                                 found 0 matches (mode: regex)
╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done · ./calc_test.go:12:12: undefined: Subtract                 0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                  1m 5s ⠦ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · Found calc.go.                                            │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
▍ Session                                                          ↑↓ section · ↵ open · esc close
▍ ▸ CONTEXT ─────────────────────────────────────────────────────────────────────────────────────
▍  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ 0%
▍ CHANGED ───────────────────────────────────────────────────────────────────────────────────────
▍ ± 1 files · +0 -0
▍ TOOLS ─────────────────────────────────────────────────────────────────────────────────────────
▍ ⚒ 10 calls · 0.0s max
▍ REPO ──────────────────────────────────────────────────────────────────────────────────────────
▍ ⎇ main · 4 files · 4 symbols
▍ SKILLS ────────────────────────────────────────────────────────────────────────────────────────
▍ ◆ 1 skills active
▍ ───────────────────────────────────────────────────────────────────────────────────────────────
▍ turn 0 · 3m04s · 0/0 · tools 12/49
▍❯ Ask Marshal...
 Act · untrusted · ⠦ working 3m 0s · qwen/qwen3.5-9b @ lmstudio · local · ctx 7k/32k    Ctrl+C stop
```

Chosen: Joined. −3 rows. Sections become ├─ NAME ─┤ dividers, like the tray's plan header; violet while focused.

```
 ● Spawn subagent to review calc.go for bugs while tests run in background.              thought 14s
   └ agent   Review calc.go for bugs while tests run in background                      ⧉ #1 started

 ● Wait for both the background test job and subagent to complete before           thought 16s · 16s
     proceeding.
   └ agent   await                                                                         waited 0s

 ● Peek at subagent progress and find calc.go file.                                      thought 12s
   ├ agent   await                                                                         waited 0s
   └ search  "calc.go"                                                 found 0 matches (mode: regex)
╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done · ./calc_test.go:12:12: undefined: Subtract                 0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                  1m 5s ⠦ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · Found calc.go.                                            │
├─ plan  ▱▱▱ 0/3 ──────────────────────────────────────────────────────────────────────────────────┤
│ · Run go test ./... as background job            · Fix any failing tests or bugs found           │
│ · Spawn subagent to review calc.go for bugs wh…                                                  │
├─ ▸ CONTEXT ────────────────────────────────────────── session · ↑↓ section · ↵ open · esc close ─┤
│  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░ 0% │
├─ CHANGED ────────────────────────────────────────────────────────────────────────────────────────┤
│ ± 1 files · +0 -0                                                                                │
├─ TOOLS ──────────────────────────────────────────────────────────────────────────────────────────┤
│ ⚒ 10 calls · 0.0s max                                                                            │
├─ REPO ───────────────────────────────────────────────────────────────────────────────────────────┤
│ ⎇ main · 4 files · 4 symbols                                                                     │
├─ SKILLS ─────────────────────────────────────────────────────────────────────────────────────────┤
│ ◆ 1 skills active                                                                                │
├─ turn 0 · 3m04s · 0/0 · tools 12/49 ─────────────────────────────────────────────────────────────┤
│ ❯ Ask Marshal...                                                                                 │
╰─ Act · untrusted · ⠦ working 3m 0s · qwen/qwen3.5-9b @ lmstudio · local ─────────── Ctrl+C stop ─╯
```

### Full-frame panels

Today. /settings: a full-frame ▍ panel, groups as bold violet rows.

```
▍ Settings                                        ←→ cycle · ↵ pick · → project config · Esc close
▍ / >
▍ Agent
▍ ▸ Agent · Default profile                                                               local ▾
▍     agent.default_profile · active agent profile that sets model routing roles [toml: profile.d…
▍   Agent · Preset                                                       lmstudio/qwen/qwen3.5-9b
▍   Agent · Max tool iterations                                                                 0
▍   Agent · Max retries                                                                         0
▍   Agent · Max turn context tokens                                                             0
▍   Agent · Max concurrent subagents                                                            3
▍   Agent · Max tool result chars                                                               0
▍   Agent · Subtask iterations                                                                  0
▍   Agent · Thinking budget margin                                                           4096
▍   Agent · Plan first                                                                      off ○
▍   Agent · Narration prompt                                                                 on ●
▍   Agent · Intent nudge                                                                     on ●
▍   Agent · Mode                                                                            act ▾
▍   Agent · Auto                                                                            off ○
▍ Providers
▍   Providers · lmstudio  http://127.0.0.1:1234/v1 · local · no key                             ›
▍ Profiles
▍   Profiles · local  (3 roles)                                                                 ›
▍ Privacy
▍   Privacy · Remote providers allowed                                                      off ○
▍   Privacy · Redact secrets                                                                 on ●
▍   Privacy · Include gitignored files                                                      off ○
▍   Privacy · models.dev enrichment                                                         ask ▾
▍ Shell
▍   Shell · Foreground wait (s)                                                                10
▍   Shell · Max output size                                                              195.3 KB
▍   ↓ more
▍ 138 settings

 Act · untrusted · qwen/qwen3.5-9b @ lmstudio · local · think default · ±1 file · repo · ⎇ main
```

Chosen: Framed. 0 rows. Groups become ├─ Agent ─┤ dividers; the count and ↓ more move into the bottom border, which frees one row for another setting.

```
╭─ Settings ──────────────────────────────────── ←→ cycle · ↵ pick · → project config · Esc close ─╮
│ / >                                                                                              │
├─ Agent ──────────────────────────────────────────────────────────────────────────────────────────┤
│ ▸ Agent · Default profile                                                                local ▾ │
│     agent.default_profile · active agent profile that sets model routing roles [toml: profile.d… │
│   Agent · Preset                                                        lmstudio/qwen/qwen3.5-9b │
│   Agent · Max tool iterations                                                                  0 │
│   Agent · Max retries                                                                          0 │
│   Agent · Max turn context tokens                                                              0 │
│   Agent · Max concurrent subagents                                                             3 │
│   Agent · Max tool result chars                                                                0 │
│   Agent · Subtask iterations                                                                   0 │
│   Agent · Thinking budget margin                                                            4096 │
│   Agent · Plan first                                                                       off ○ │
│   Agent · Narration prompt                                                                  on ● │
│   Agent · Intent nudge                                                                      on ● │
│   Agent · Mode                                                                             act ▾ │
│   Agent · Auto                                                                             off ○ │
├─ Providers ──────────────────────────────────────────────────────────────────────────────────────┤
│   Providers · lmstudio  http://127.0.0.1:1234/v1 · local · no key                              › │
├─ Profiles ───────────────────────────────────────────────────────────────────────────────────────┤
│   Profiles · local  (3 roles)                                                                  › │
├─ Privacy ────────────────────────────────────────────────────────────────────────────────────────┤
│   Privacy · Remote providers allowed                                                       off ○ │
│   Privacy · Redact secrets                                                                  on ● │
│   Privacy · Include gitignored files                                                       off ○ │
│   Privacy · models.dev enrichment                                                          ask ▾ │
├─ Shell ──────────────────────────────────────────────────────────────────────────────────────────┤
│   Shell · Foreground wait (s)                                                                 10 │
│   Shell · Max output size                                                               195.3 KB │
╰─ 138 settings ────────────────────────────────────────────────────────────────────────── ↓ more ─╯


 Act · untrusted · qwen/qwen3.5-9b @ lmstudio · local · think default · ±1 file · repo · ⎇ main
```

### Subagent drill-in

Today. Ctrl+F: a crumb row on top; the child's transcript looks like the parent's.

```
● orchestrator ▸ ⧉ subtask — Review calc.go for bugs while tests run in background · qwen/qwen3.5-9b
   - Division by zero
   - Integer overflow
   - Race conditions
   - Resource leaks
   - Incorrect error handling
   - Off-by-one errors
   - Logic errors

   Provide a detailed analysis of any issues found.

 ● I need to find and read calc.go before analyzing it for bugs.          subtask · thought 8s · 18s
   └ search  "calc.go"                                                               found 0 matches

 ● The initial search didn't find calc.go. Let me try a broader file search     subtask · thought 6s
     to locate it.
   └ search  "*.go.*calc*"                                                           found 0 matches

 ● The repo.search didn't find calc.go with wildcard patterns. Let me    subtask · thought 15s · 17s
     try a broader approach - using git status or looking at what Go
     files exist in the repo.
   └ shell   find /private/tmp/scratch/go-calc-broken---------------------------------…  exit 0 · 0s

 ⠇ thinking                                                                                      10s
 ▍ The find command found calc.go exists at that path. I should read both calc.go and its test file
 ▍ to understand the full picture before analyzing for bugs.

╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done                                                             0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                    55s ⠇ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · The repo.search didn't find calc.go with wildcard patter… │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
▍❯ Ask Marshal...
 Act · untrusted · ⠇ working 2m 50s · qwen/qwen3.5-9b @ lmstudio · local · ctx 6k/32k   Ctrl+C stop
```

Chosen: Framed. +1 row and −4 columns. The child's transcript sits in a coral frame whose top border is the crumb.

```
╭─ ● orchestrator ▸ ⧉ subtask — Review calc.go for bugs while tests run in backgrou… ─ ↑/Esc back ─╮
│    - Division by zero                                                                            │
│    - Integer overflow                                                                            │
│    - Race conditions                                                                             │
│    - Resource leaks                                                                              │
│    - Incorrect error handling                                                                    │
│    - Off-by-one errors                                                                           │
│    - Logic errors                                                                                │
│                                                                                                  │
│    Provide a detailed analysis of any issues found.                                              │
│                                                                                                  │
│  ● I need to find and read calc.go before analyzing it for bugs.      subtask · thought 8s · 18s │
│    └ search  "calc.go"                                                           found 0 matches │
│                                                                                                  │
│  ● The initial search didn't find calc.go. Let me try a broader file        subtask · thought 6s │
│      search to locate it.                                                                        │
│    └ search  "*.go.*calc*"                                                       found 0 matches │
│                                                                                                  │
│  ● The repo.search didn't find calc.go with wildcard patterns. Let   subtask · thought 15s · 17s │
│      me try a broader approach - using git status or looking at                                  │
│      what Go files exist in the repo.                                                            │
│    └ shell   find /private/tmp/scratch/go-calc-broken-----------------------------…  exit 0 · 0s │
│                                                                                                  │
│  ⠇ thinking                                                                                  10s │
│  ▍ The find command found calc.go exists at that path. I should read both calc.go and its test   │
│  ▍ file to understand the full picture before analyzing for bugs.                                │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
╭─ running 1 · 1 finished ─────────────────────────────────────────────────────────────────────────╮
│ ▌┆ job-1  go test ./... &  done                                                             0s ✓ │
│ ▌⧉ #1     Review calc.go for bugs whil…                                                    55s ⠇ │
│ ▌         qwen/qwen3.5-9b @ lmstudio · The repo.search didn't find calc.go with wildcard patter… │
├──────────────────────────────────────────────────────────────────────────────────────────────────┤
│ ❯ Ask Marshal...                                                                                 │
╰─ Act · untrusted · ⠇ working 2m 50s · qwen/qwen3.5-9b @ lmstudio · local ────────── Ctrl+C stop ─╯
```

### Not chosen: framed history

Boxing every block of history costs two rows a block, which is what the July
hairline-gutter redesign took out of the transcript, and the spine already
ties calls to their narration. The frame would stop meaning "live, or waiting
on you". The colour version shows the frame.
