# Deferred: a `using-tools` skill

**Status: not built.** Nothing is registered today, and no prompt text
references this file. This is a written-down idea, not a plan.

## What it would be

A skill that explains the tool families and their boundaries — the same content
as `docs/tools/`, in skill form. A model that loaded it would get the
family-by-family surface, the call sequences, and the failure modes in one
block of context, rather than discovering them one rejected call at a time.

## Why it is deferred

It would be loaded on demand like any other skill, not autoloaded. The
postmortem corpus shows skill over-loading already costs a large share of one
transcript, so adding to the always-on prefix is the wrong move — the whole
point of the skill mechanism is that a body is paid for only when it is used.

The two `baseRules` rules and the tool-description cross-references cover the
highest-frequency confusions at a fraction of the cost: roughly 800 bytes of
always-on text against a skill body measured in kilobytes. If the cheap
intervention works, the expensive one is unnecessary.

## How it would be wired if built

- Registered as a built-in skill in `internal/skills/builtin.go` with a
  `//go:embed` directive, alongside the other eleven.
- Referenced from `using-skills` — which *is* autoloaded — as a pointer, so a
  model that is confused about which tool to reach for has a route to it.
- Its body would be a condensed form of `docs/tools/`, not a copy: the docs
  carry Go anchors and line numbers that are useful to a human reader and
  noise to a model.

## What would have to be true first

Evidence that the rules and descriptions did not close the selection errors they
target. The corpus is the measurement: re-run the postmortem analysis over a
comparable window of sessions and compare the wrong-tool and wrong-argument
counts against the baseline recorded in `docs/tool-usage/design.md`.

If the counts fall, the skill is not needed. If they hold, the skill is one
candidate response — and the corpus will say which confusions remain, which is
what its body should be written against.
