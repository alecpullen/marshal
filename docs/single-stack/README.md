# Single Stack TUI

This folder holds the design for Marshal's single-column TUI. The work is
delivered as three stacked phases, and each phase has its own branch and
draft PR.

| Doc | What it covers |
|---|---|
| [`feature-spec.md`](feature-spec.md) | The whole feature: goals, decisions, UX, architecture, phasing |
| [`mockups.html`](mockups.html) | Terminal mockups (open in a browser) |
| [`p1-layout-and-keys.md`](p1-layout-and-keys.md) | P1: remove the rail; add the now bar, session sheet, Tasks panel and Esc/Ctrl+C changes |
| `p2-steps-and-ownership.md` | P2: step identity, persistence, owner labels, narration prompt, step renderer (added on the P2 branch) |
| `p3-tasks-and-navigation.md` | P3: task headers and folding, density ladder, browse mode, inspector (added on the P3 branch) |
| [`reference/p1-execution-plan-draft.md`](reference/p1-execution-plan-draft.md) | An early P1 execution plan, kept for reference only |

The phases are stacked in dependency order:

```
main ← single-stack/p1-layout-and-keys ← single-stack/p2-steps-and-ownership ← single-stack/p3-tasks-and-navigation
```
