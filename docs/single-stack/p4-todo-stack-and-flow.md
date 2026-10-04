# Single Stack P4 — Todo Stack and Transcript Flow

Parent: `feature-spec.md`. Revises the task mechanic from `p3-tasks-and-navigation.md`
after hands-on use.

## Why

Two things read badly in practice:

1. Task headers appeared one at a time, only once the agent had started a todo,
   so the list as a whole was never visible and short plans got headers they
   did not need.
2. Every narrated step carried its own chrome: a ✓, a duration, a blank line
   before each tool row and a bullet per call in a merged run. A plain run
   with no todos looked like a column of badges.

## Todo stack

A todo list of two or more items drives the transcript (`viewmodel.MinTaskTodos`).
Whether a turn is driven is settled per turn, by the longest list any
`todo.write` in it wrote or by the current list, so rewriting the list to one
item later does not flatten a finished turn. Shorter lists, and turns that
never touch the list, render as plain narrated steps with no task chrome and
no task count on the receipt.

For a turn working from a list:

- **Pinned strip.** Above the transcript, the work done and under way: one
  line per finished todo (`✓ 1/5 title`) and one for the todo in progress
  (`▸ 2/5 title`). It stays put while the work scrolls, shows as soon as the
  list is written, and stays after the turn ends. Past six rows (three on a
  short terminal) it becomes a window around the active todo with `✓ N done`
  and `+N more` lines. It only appears while the latest turn is working from
  the list, so leftovers from an earlier turn do not linger.
- **Pinned band.** Below the transcript, above the now bar, the todos the
  agent has not started: `· 3/5 title`, `· 4/5 title`. They stack at the
  bottom so the top of the screen is the work done and the bottom is what is
  still to come. The band takes what is left of the strip's row budget, so
  the two surfaces together never cost the transcript more than the strip
  used to on its own; a plan longer than that is counted on one line
  (`+N waiting`). Both surfaces number todos against the whole list, so a
  todo reads `3/5` in whichever one it lands in, and an empty band takes no
  rows at all.
- **Transcript.** Finished todos are folded rows (P3 folding rule
  unchanged, so they still expand to their steps), stacked tight. The todo
  in progress is open: a rule header sitting directly on its steps. The
  waiting todos are not repeated here (`viewmodel.KindQueue` is the data the
  band reads, and it renders no rows).

Both surfaces are the same todo list the data model already carries; the
strip and the band are `todo_strip.go`, and both are part of the viewport
height budget.

Work under a todo that later left the list still renders under its `dropped`
header.

## Flow

- A narration and its calls form one paragraph: single-line tool rows are
  tight under it and under each other. A row that spans several lines
  (expanded, a failure tail, a card) is set off by blank lines on both sides.
- A settled step's marker is a quiet `·`. Only a running step (spinner) and a
  failed one (`✗`) get colour.
- A step header shows a duration only from 15 s up (`notableStepDuration`).
- A collapsed same-tool run is a single line, `Read files: ×3 · a.go, b.go`,
  ending in `…` when the targets overflow, so a growing run changes one row,
  not the layout. Result summaries now appear only when the run is expanded
  (errors, hooks and symbol results never join a run, so no failure is
  hidden). Expanded keeps the bullet list.
- Consecutive narration-only steps run on without a blank line.
- Folded task rows and the open header after them are joined without blank
  lines (`tight` in `refreshViewport`).
