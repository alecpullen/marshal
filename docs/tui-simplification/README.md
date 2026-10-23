# TUI simplification

The redesign of Marshal's main screen around one rule: history only grows, and
everything that can still change lives in one box above the input. Goal:
simplification.

| Doc | What it covers |
|---|---|
| [`feature-spec.md`](feature-spec.md) | **The spec**: decisions, UX, architecture, phases, tests |
| [`plans/2026-10-24-p-a-renderer-foundation-plan.md`](plans/2026-10-24-p-a-renderer-foundation-plan.md) | P-A execution plan: beats on a spine, plan line, no folding, one level of detail |
| [`framed-console.md`](framed-console.md) | The chosen design with text frames; [`framed-console.html`](framed-console.html) in colour |
| [`console-variants.md`](console-variants.md) | Round 2: five dock treatments and four history styles |
| [`alternatives.md`](alternatives.md) | Round 1: today's frame, the shared foundation, Ledger / Console / Sticky |
| [`mockups.html`](mockups.html), [`console-variants.html`](console-variants.html) | Colour versions of rounds 1 and 2 |

The frames are generated, so the text and colour versions match. The generator
was a throwaway script and is not kept. Edit the frames by hand from now on, or
regenerate them from the running TUI once the live box exists.
