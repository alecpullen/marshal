# Studio visual and interaction foundation implementation plan

> **For agentic workers:** Use the repository's `marshal-executing-plans` workflow to execute these tasks sequentially. Do not implement before this plan is approved.

**Goal:** Restore visible, usable mobile Studio and introduce a coherent, accessible Warm Sunset visual foundation across representative Fleet, factory and settings surfaces.

**Architecture:** Repair the existing responsive shell first. Refine the semantic theme tokens and shared Svelte primitives in place, then apply them to representative current views and the shared session header. Preserve existing routes, agent and factory behavior, and both themes; no parallel UI or backend changes are required.

**Tech stack:** Svelte 5, TypeScript, Tailwind CSS, Vitest, Playwright/axe and the existing isolated `webbridge` fixture.

**Spec:** [Studio visual foundation specification](../studio-visual-foundation-spec.md); [approved design](../studio-visual-foundation-design.md).

**Status:** approved by the user on 2026-11-17.

**Base:** `origin/main` at `47fa29d7`. Start from an up-to-date `main`, reconcile changes made since authoring, and retain the approved design/spec and baseline images with this plan. The docs branch used while planning is not an implementation base.

**Non-goals:** a new palette or font family, new framework/component library, a parallel shell, a full work-item or settings redesign, new backend endpoints, changes to factory permissions or user actions, or claiming mocked fixtures are live factory behavior.

**Assumptions:** `web/ui` dependencies and Playwright Chromium are installed per `web/ui/e2e/README.md`; the browser harness uses its own temporary HOME, project and token. `scripts/e2e/run_browser_checks.sh` accepts Playwright CLI arguments. Tests that use `mockFleet` are presentation evidence only.

## Global constraints

- Keep Warm Sunset and Geist, existing dark/light choice, route/deep links, reduced motion, focus return and factory authority behavior.
- In the covered active views, meet 4.5:1 for normal text, 3:1 for large text and meaningful non-text controls against adjacent colors; do not rely on color alone.
- Preserve action names, semantics, availability, keyboard behavior and work-item selected view. Existing 44 px mobile primary-action targets remain at least 44 × 44 CSS px.
- The phone chrome must not paint an opaque full-screen background over main content; a drawer overlay is allowed while the drawer is open.
- No real credentials, network services or developer config in tests. `web/` remains standard-library-only.

## Review focus

- A transparent `pointer-events:none` hit-test is not proof content is painted: Task 1 asserts actual screenshot pixels and chrome background.
- Changing a single theme token can break tinted statuses and filled danger buttons: Task 2 checks both themes and actual rendered colors.
- The global `button { color: inherit }` currently overrides Tailwind's button text colors: Task 2 pins computed foreground as well as visual contrast.
- Wrapping the session header must not hide or disable access, navigation or composer actions: Task 4 tests narrow and embedded/full sessions.
- Shared primitives can affect many screens: Tasks 3 and 5 verify representative empty, error, long, busy and normal states, and retain interaction contracts.

## Contracts

These CSS custom properties are shared by Tasks 2–5. Put their concrete declarations in the existing `@theme` and `[data-theme='light']` blocks of `web/ui/src/app.css`; Tailwind exposes them as `text-on-accent` and `text-on-danger`.

```css
/* Added to @theme, alongside the existing semantic tokens. */
--color-on-accent: #121113;
--color-on-danger: #121113;

/* Added to [data-theme='light']; dark neutrals and signals remain unchanged. */
--color-accent: #7d290f;
--color-warn: #8a4b00;
--color-ok: #00654d;
--color-err: #a3234c;
--color-info: #005a81;
--color-violet: #702989;
--color-gold: #765000;
--color-muted: #5f5951;
--color-on-accent: #ffffff;
--color-on-danger: #ffffff;
/* Resolve aliases on the themed element, even when it is not :root. */
--color-running: var(--color-ok);
--color-attention: var(--color-warn);
--color-danger: var(--color-err);
```

The light palette candidates were checked mathematically against the existing light `bg`, `surface`, `raise` and `line` colors; actual badge tints, compositing and overrides still require browser checks. Adjust a concrete value only when a measured rendered combination fails, retain the same warm semantic role, and update these declarations and visual evidence together. `--color-running`, `--color-attention` and `--color-danger` continue to alias their semantic signals.

The shared visual scale is page title `text-xl leading-7 font-semibold` (20/28 px), section title `text-base leading-6 font-semibold` (16/24), body `text-sm leading-5` (14/20), and label/metadata `text-xs leading-4` (12/16). Code and identifiers may use 12 px Geist Mono. Existing `studio-panel` and `studio-control` establish 12 px/8 px radii; use Tailwind spacing steps 1, 2, 3, 4, 6 and 8. Keep `Button`'s current `variant: 'default' | 'ghost' | 'danger'` API and existing accessible names; `default` is primary, `ghost` is secondary/quiet, and `danger` is destructive.

## Task 1: Make phone content visibly usable

**Goal:** Phone navigation no longer paints over the page, and a browser regression catches a recurrence.

**Files:** `web/ui/src/lib/nav/MobileNav.svelte`, `web/ui/e2e/studio-shell.pw.ts`, `web/ui/e2e/factory-item.pw.ts`, and new Playwright screenshot baselines created for these assertions.

**Steps:**

1. Add `phoneChromeDoesNotCoverFleetContent` in the real-bridge shell suite and `phoneChromeDoesNotCoverFactoryContent` in the mocked factory-item suite. At 390 × 844 and 390 × 600 in each theme, wait for the known heading and assert `.studio-phone-chrome > div` has computed transparent background. Capture an element screenshot of the heading region and compare it with checked-in baselines: an opaque covering layer must produce a visibly different image. Click a real navigation or item action beneath the bars and verify the destination; exercise drawer open/close and focus return. Do not rely on `isVisible()` or `elementFromPoint` as the sole visual check.
2. Run the tests against the current code and confirm the transparent-background or screenshot check fails. In `MobileNav.svelte`, replace exactly:

   ```svelte
   <div class="flex h-full w-full min-w-0 flex-col bg-bg">
   ```

   with:

   ```svelte
   <div class="flex h-full w-full min-w-0 flex-col">
   ```

   Leave the header/nav backgrounds and the open drawer's overlay unchanged. Generate the screenshot baselines from the fixed UI and verify they actually show the heading, not a blank background.
3. Run the checks below. Rotate the test viewport to 844 × 390 and back and verify navigation/content remains visible; assert the drawer keeps focus behavior. Commit this independently shippable fix.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/studio-shell.pw.ts e2e/factory-item.pw.ts --grep 'phoneChromeDoesNotCover'` and `cd web/ui && npm run check && npm test -- --run` pass. Record both real-bridge and mocked-fixture limits.

## Task 2: Correct semantic theme contrast

**Goal:** Filled actions, signal text, statuses and the session empty state pass the specified contrast in both themes.

**Files:** `web/ui/src/app.css`, `web/ui/src/lib/ui/Button.svelte`, `web/ui/src/lib/ui/Badge.svelte`, `web/ui/src/lib/ui/Tag.svelte`, `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/e2e/studio-fleet.pw.ts`, `web/ui/e2e/factory-handoff.pw.ts`, `web/ui/e2e/factory-item.pw.ts`, and relevant component tests in `web/ui/src/lib/ui/`.

**Steps:**

1. Add failing component/browser checks for computed text and background on enabled primary and danger buttons, neutral and signal badges/tags, links and the session empty hint in both themes. Cover hovered/focused interactive examples and assert role/status remains in text. List the exact existing `KNOWN_CONTRAST_PAIRS` and `PALETTE_DEFECTS` from the three browser suites in failure output while making the scans fail on all axe violations; remove both the `allowed` and `required` exception logic rather than replacing it with a larger allowlist.
2. Add the Contracts' on-color and light signal tokens. In the global `button, select, input, textarea { color: inherit; }` rule, remove `button` so component color utilities can win; retain inheritance for the three remaining form controls. In `Button.svelte`, change `default` from `text-bg` to `text-on-accent` and `danger` from `text-bg` to `text-on-danger`. Fix the session empty hint's `color-mix`/opacity styling so it uses full semantic muted color. Preserve disabled appearance and native button semantics.
3. Keep `Badge.svelte` and `Tag.svelte` tone APIs and text. Adjust tint/foreground combinations only as needed to pass actual rendered contrast against page and card surfaces in both themes; retain named status meaning. Use the concrete candidate colors in Contracts and update them only after recording a measured failure. Verify focus outlines meet 3:1 against the surface they adjoin, adding a contrasting outer ring where required.
4. Run the browser checks below, including enabled button states and full-page work-item scans. Re-run all three suites with no color-contrast exceptions and preserve the non-contrast rules. Capture color-pair/ratio evidence and update the foundation spec with any adjusted token values.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/studio-fleet.pw.ts e2e/factory-handoff.pw.ts e2e/factory-item.pw.ts` passes in desktop/tablet/phone projects with no axe violations; `cd web/ui && npm run check && npm test -- --run` passes.

## Task 3: Unify typography, controls and representative pages

**Goal:** Fleet, factory activity/review and project settings share the approved hierarchy and primitives without changing workflows.

**Files:** `web/ui/src/app.css`, `web/ui/src/lib/ui/PageHeader.svelte`, `web/ui/src/lib/ui/Card.svelte`, `web/ui/src/lib/ui/StateMessage.svelte`, `web/ui/src/lib/ui/Button.svelte`, `web/ui/src/views/Fleet.svelte`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/views/Project.svelte`, and their existing component/view tests.

**Steps:**

1. Add tests that the page keeps one `h1`, meaningful headings, action names and error/status roles; check primary/secondary/destructive treatment, focus-visible and the touch target dimensions. Pin that long titles and metadata wrap rather than forcing horizontal page overflow.
2. Apply the typography and spacing Contracts to `PageHeader` and the existing headings in the three views. Use `studio-panel` and `studio-control` for 12 px panel and 8 px control corners; align `Card` and `StateMessage` to those rules and use 12 px minimum user-facing metadata. Preserve monospace only for code and technical identifiers. Keep backgrounds and borders restrained; remove redundant nested visual framing only where it does not remove meaningful grouping.
3. Use the existing `Button` API for consistent action hierarchy; keep already working route links as anchors. Give form fields in `Project.svelte` consistent 12 px labels, spacing, focus and inline error descriptions without changing settings, inheritance or save behavior. Do not add a forced contrast between global and project settings or alter the content order.
4. Update existing tests only for intentional presentation changes. Add tests for long title, loading, unavailable, and focused input states; retain all current saved-draft and factory ownership assertions.

**Verify:** `cd web/ui && npm run check && npm test -- --run` passes; `scripts/e2e/run_browser_checks.sh e2e/studio-fleet.pw.ts e2e/factory-item.pw.ts e2e/project-settings.pw.ts` passes from the repository root.

## Task 4: Responsive shared session header

**Goal:** Worker and ordinary agent conversations keep every header action and the composer reachable at desktop and phone widths.

**Files:** `web/ui/src/lib/session/AgentConversation.svelte`, `web/ui/src/views/FactoryItem.svelte`, `web/ui/src/lib/session/AgentConversation.test.ts`, `web/ui/src/views/FactoryItem.test.ts`, `web/ui/e2e/factory-item.pw.ts`.

**Steps:**

1. Add `embeddedSessionHeaderFitsPhone` and `fullSessionHeaderFitsPhone` browser tests at 390 × 844 and 390 × 600, with the active item fixture and an ordinary session fixture. Verify back, agent name, connection status, detail selection, access controls, open-full/review/network actions as applicable, composer and send can all be reached by keyboard and touch without horizontal clipping; keep access state and session content unchanged when resizing. A shorter viewport is a keyboard approximation, not a physical keyboard test.
2. In `AgentConversation.svelte`'s `<header>`/component styles, use a responsive wrapping layout: keep back, identity and connection in the first row; allow density, `ModeSwitcher`, and open-full or review/network/share controls to wrap onto subsequent rows at narrow container widths. Do not hide controls or add a new menu. Set min-width and overflow rules so long names/branches can wrap or truncate with accessible full labels.
3. In `FactoryItem.svelte`, replace the embedded conversation's unconditional `min-h-[32rem]` with a phone-safe available-height layout that reserves the shell's top/bottom chrome, allows its internal transcript to scroll, and keeps the composer reachable. Keep the current desktop split and preserve the item activity pane's scroll position on return.
4. Add/update component tests for embedded and full session, long names, access errors, connection state and narrow-to-wide resize. Preserve child transcript drill-down and the planned subagent controls' existing entry points.

**Verify:** `scripts/e2e/run_browser_checks.sh e2e/factory-item.pw.ts --grep 'SessionHeaderFitsPhone|keyboardOnlyDrillInAndReturn|twoHundredPercentZoom'` and `cd web/ui && npm run check && npm test -- --run` pass.

## Task 5: Cross-surface visual and interaction verification

**Goal:** The foundation is verified against realistic states in both themes, with reproducible evidence and explicit coverage limits.

**Files:** `web/ui/e2e/studio-fleet.pw.ts`, `web/ui/e2e/factory-handoff.pw.ts`, `web/ui/e2e/factory-item.pw.ts`, `web/ui/e2e/project-settings.pw.ts`, `docs/studio-fleet-factory/studio-visual-foundation-spec.md`, and new approved after screenshots under `docs/studio-fleet-factory/screenshots/ui-foundation-2026-11-17/`.

**Steps:**

1. Exercise Fleet, active/decision/review work items, handoff and project settings at 1440 × 900, 768 × 1024, 390 × 844 and a short 390 × 600 in dark and light. Include long content, empty/unavailable/error, busy and enabled-action states. Assert actual visible content, no horizontal page overflow, no unexpected axe violations, reduced motion, keyboard focus/return and 200% zoom.
2. Capture representative desktop and phone after screenshots through the fixture suite. Store filenames that identify viewport, theme and fixture status; compare them against the before baselines in the approved design. Report which previews, session transcripts and forge states are mocked or unavailable. Do not overwrite unmodified baseline images or present the temporary phone diagnostic as before UI.
3. Append a concise delivery record to the spec with actual commands/results, screenshot paths, the measured contrast changes, any unresolved exceptions and physical phone/virtual-keyboard coverage. A failed check is not a passed check; do not claim real factory execution from mocked browser fixtures.

**Verify:** `scripts/e2e/run_browser_checks.sh` passes all existing and new browser cases; `cd web/ui && npm run check && npm test -- --run && npm run build` passes. Examine every saved screenshot for nonblank content and correct viewport before linking it.

## Final verification and integration

Run `go test ./...`, `go vet ./...`, `CGO_ENABLED=1 go build ./cmd/marshal`, `go test ./web/bridge/contract/tsgen -run TestTypeScriptContractIsCurrent`, and `go test ./web/bridge -run TestWebIsStdlibOnly`. Build the SPA with `cd web/ui && npm run build`, then from the repository root run `go build ./cmd/webbridge` so the embedded assets match. Review `git diff --check` and the final browser evidence. Report browser/physical-device limitations and any unrelated failures explicitly. Commit each verified task as `studio-visual-foundation: task N — <title>`.
