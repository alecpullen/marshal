# TUI simplification

The redesign of Marshal's main screen around one rule: history only grows, and
running work and the plan are pinned in a tray above the input. Goal:
simplification.

| Doc | What it covers |
|---|---|
| [`feature-spec.md`](feature-spec.md) | **The spec**: decisions, UX, architecture, phases, tests |
| [`plans/2026-10-24-p-a-renderer-foundation-plan.md`](plans/2026-10-24-p-a-renderer-foundation-plan.md) | P-A execution plan (landed): beats on a spine, plan line, no folding, one level of detail |
| [`plans/2026-10-24-p-b-lifecycle-plan.md`](plans/2026-10-24-p-b-lifecycle-plan.md) | P-B execution plan: completion events, one projection, history that only grows, web adoption |
| [`plans/2026-10-24-p-c-live-box-plan.md`](plans/2026-10-24-p-c-live-box-plan.md) | P-C execution plan: the live box, measured bars, plan layouts and Ctrl+T, skill-load duplicate fix |
| [`plans/2026-10-25-p-d-progress-skill-plan.md`](plans/2026-10-25-p-d-progress-skill-plan.md) | P-D execution plan: the progress-reporting skill and its producers, the schema pointer, room in the prompt budgets |
| [`plans/2026-10-25-p-e-tray-plan.md`](plans/2026-10-25-p-e-tray-plan.md) | P-E execution plan: the step in progress back in history, the tray, held rows, the turn clock in the status line, `[tui.live_box]` retired |
| [`plans/2026-10-27-p-f-framed-console-plan.md`](plans/2026-10-27-p-f-framed-console-plan.md) | P-F execution plan: no chrome tint, chrome frames for every panel, the console outline with prompt sections and the status line as its bottom border, the drill-in frame |
| [`border-study.md`](border-study.md) | Round 7, after round 6 restored the tray frame: where else the frame goes (the framed console, P-F), from real captures; [`border-study.html`](border-study.html) in colour |
| [`pinned-work.md`](pinned-work.md) | Round 5, after testing P-C: the tray that replaces the live box (P-E), and the options it was chosen from; [`pinned-work.html`](pinned-work.html) in colour |
| [`framed-console.md`](framed-console.md) | Round 4: the live box P-C built, with text frames; [`framed-console.html`](framed-console.html) in colour |
| [`console-variants.md`](console-variants.md) | Round 2: five dock treatments and four history styles |
| [`alternatives.md`](alternatives.md) | Round 1: today's frame, the shared foundation, Ledger / Console / Sticky |
| [`mockups.html`](mockups.html), [`console-variants.html`](console-variants.html) | Colour versions of rounds 1 and 2 |

The frames are generated, so the text and colour versions match. The generator
was a throwaway script and is not kept. Edit the frames by hand from now on, or
regenerate them from the running TUI once the live box exists.
