# Studio programme — implementation plans

The plans that execute phases F1–F8 of [the programme](../programme.md).
Each follows the `marshal-writing-plans` skill
(`internal/skills/builtin/marshal-writing-plans.md`) and is executed task by
task, one commit per task, as `<plan-slug>: task N — <title>`.

F0 was executed directly from its spec and has no plan.

## Rules every plan here follows

- **Prose steps, no implementation code.** The only code is a plan's
  Contracts section, which holds declarations only. Executing a plan also
  tests how well Marshal follows one.
- **Names are pinned.** Each task names its files, what each piece does
  after the change, and each test with the behaviour it asserts. A name
  from an earlier plan is in that plan's Contracts; re-check it in the tree
  before starting, because the plans were written ahead of the code.
- **Verification runs on Linux**, where the factory and CI run. Anything
  that can only be confirmed on a Mac is listed under **Needs a Mac**.
- **The design system binds every UI task.** Screens are built from the
  kit and registered in `app/test/support/screens.dart`, so the enforcement
  tests cover them. A task that needs something the kit lacks first extends
  `design-system.md`, the kit, the gallery and the goldens.
- **Goldens are generated on Linux.**
- **Nothing reads the developer's machine**: no real config, keychain or
  running daemon in a test.

## Order

| # | Plan | Runs after |
|---|---|---|
| 1 | [F1 client](2026-11-17-f1-client-plan.md) | F0, on `main` |
| 2 | [F2 shell, Overview, Inbox](2026-11-17-f2-shell-plan.md) | 1 |
| 3 | [F3 agent thread](2026-11-17-f3-agent-thread-plan.md) | 2 |
| 4 | [F4 inspector](2026-11-17-f4-inspector-plan.md) | 3 |
| 5 | [F5.1 pull requests in the bridge](2026-11-17-f5-1-pulls-bridge-plan.md) | F0; Go only, may run beside 2–4 |
| 6 | [F5.2 pull requests in the app](2026-11-17-f5-2-pulls-app-plan.md) | 4, 5 |
| 7 | [F6.1 work items](2026-11-17-f6-1-work-items-plan.md) | 4, 6 |
| 8 | [F6.2 planning, runs, project page](2026-11-17-f6-2-planning-runs-project-plan.md) | 7 |
| 9 | [F7.1 list pattern, Automations, Library](2026-11-17-f7-1-automations-library-plan.md) | 2; may run beside 3–8 |
| 10 | [F7.2 Settings](2026-11-17-f7-2-settings-plan.md) | 9 |
| 11 | [F8 cutover](2026-11-17-f8-cutover-plan.md) | all |
| 12 | [F9.1 reachable surfaces](2026-11-18-f9-1-reachable-surfaces-plan.md) | 11; may run before or after 13 |
| 13 | [F9.2 start from anywhere](2026-11-18-f9-2-start-from-anywhere-plan.md) | 11; may run before or after 12 |
| 14 | [F9.3 Overview and project parity](2026-11-18-f9-3-overview-parity-plan.md) | 11; may run beside 12 and 13 |
| 15 | [Studio visual fixes](2026-11-18-studio-visual-fixes-plan.md) | 12–14, P8 on `main` |
| 16 | [Studio controls that do nothing](2026-11-18-studio-dead-controls-plan.md) | 15 |
| 17 | [Studio conversation readability](2026-11-18-studio-conversation-readability-plan.md) | 15, 16 |
| 18 | [Studio places polish](2026-11-18-studio-places-polish-plan.md) | 15, 16 |
| 19 | [Studio navigation: search, Settings, kept preferences](2026-11-18-studio-navigation-plan.md) | 15, 16, 18 |

The Svelte Studio is frozen, bug fixes only, from the start of plan 2.
