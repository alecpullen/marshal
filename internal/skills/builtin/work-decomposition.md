---
name: work-decomposition
description: Break one large piece of work into independently completable units before starting or before parallelising. Use when a task touches many files or subsystems, when you cannot hold the whole change in your head, or when deciding what can safely run concurrently.
risk: read_only
---

# Work Decomposition

Decomposition is the step that makes parallel dispatch safe. Do it first, then decide whether to parallelise.

## Find the seams

Split by **responsibility and data flow**, not by technical layer. "All the tests" and "all the types" are bad units — neither is independently verifiable.

A good unit:
- Has one clear deliverable you can describe in a sentence.
- Can be verified on its own (a test run, a build, a command with expected output).
- Names the exact files it touches.
- Declares what it consumes from other units and what it produces for them.

## Classify dependencies honestly

For each pair of units, ask what happens if they run at the same time:

- **Independent** — disjoint files, no shared types. Safe to parallelise.
- **Interface-coupled** — B needs a signature A defines. Sequence them, or define the interface up front in a shared first unit and then parallelise.
- **File-coupled** — both edit the same file. Not parallelisable. Merge them into one unit or sequence them.

Write the dependency list down. Coupling you did not notice is the usual cause of a failed parallel run.

## Order the sequential ones

Put units that define shared interfaces first. Every later unit then has concrete types to code against instead of guesses.

## Sizing

A unit is the smallest thing worth a fresh reviewer's gate. Fold setup, config, and docs into the unit whose deliverable needs them. Split only where a reviewer could reject one unit while approving its neighbour.

## Then decide

Two or more genuinely independent units → `dispatching-parallel-agents`.
Otherwise, do them in order yourself. Parallelism has coordination cost; it is not free.
