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

A todo list of two or more items drives the transcript (`stack.MinTaskTodos`).
Shorter lists, and turns that never touch the list, render as plain narrated
steps with no task chrome and no task count on the receipt.

For a turn working from a list:

- finished todos are folded rows (P3 folding rule unchanged), stacked tight;
- the todo in progress is open: a rule header sitting directly on its steps;
- todos with no task yet (pending, or in progress before a step ran) are
  listed below the live work as `· 3/5 title` rows, capped at eight with a
  `+N more` row. This is `stack.KindQueue`, built by `addQueue`;
- the list shows as soon as it has been written, before any step runs under it;
- when the turn ends with todos left over, the list sits above the final
  answer rather than disappearing.

Work under a todo that later left the list still renders under its `dropped`
header, whatever the list length.

## Flow

- A narration and its calls form one paragraph: single-line tool rows are
  tight under it and under each other. A row that spans several lines
  (expanded, a failure tail, a card) is set off by blank lines on both sides.
- A settled step's marker is a quiet `·`. Only a running step (spinner) and a
  failed one (`✗`) get colour.
- A step header shows a duration only from 15 s up (`notableStepDuration`).
- A collapsed same-tool run is a single line, `Read files: ×3 · a.go, b.go`,
  so a growing run changes one row, not the layout. Expanded keeps the
  bullet list.
- Consecutive narration-only steps run on without a blank line.
- Folded task rows, the open header after them and the waiting list are
  joined without blank lines (`tight` in `refreshViewport`).
