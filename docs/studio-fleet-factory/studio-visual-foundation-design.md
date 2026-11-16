# Marshal Studio visual and interaction foundation

Date: 2026-11-17
Status: approved product design. See the [specification](studio-visual-foundation-spec.md)
and [implementation plan](plans/2026-11-17-studio-visual-foundation-plan.md).

## Intent

Make Studio feel sleeker, more cohesive and easier to use across Fleet,
Factory, agent sessions and settings, with phones as first-class working
devices. Begin with a shared foundation so later, separately planned workflow
improvements fit together. Evolve the existing Warm Sunset palette and Geist
typography rather than replacing Marshal's visual identity.

## What the baseline shows

The current Fleet-first navigation and factory journey from the prior
[UI/UX alignment design](studio-uiux-alignment-design.md) are implemented;
its older plan header does not reflect the delivery record. Keep the useful
existing information architecture and interaction contracts while improving
their presentation and usability.

On `main` at `47fa29d7`, an isolated browser preview with representative test
fixtures revealed that the phone navigation wrapper paints an opaque
full-screen background over the page, hiding otherwise-present content. The
unmodified [phone baseline](screenshots/ui-foundation-2026-11-17/mobile-overlay-baseline.png)
captures the problem. A [diagnostic Fleet view](screenshots/ui-foundation-2026-11-17/fleet-phone-diagnostic.png)
shows the existing layout after removing that background **only in the
temporary preview copy**; it is not a screenshot of the unmodified app.
Neither screenshot uses live user data.

The [desktop Fleet baseline](screenshots/ui-foundation-2026-11-17/fleet-desktop.png)
shows the current palette, navigation and card patterns. The [active work-item
baseline](screenshots/ui-foundation-2026-11-17/work-item-active-desktop.png)
shows a crowded embedded session header and a large plan-revision panel.
Existing browser verification records known color-contrast exceptions. The
baseline is a visual starting point, not evidence that every workflow or
state has been tested. Desktop captures are 1920 × 1080; phone captures are
430 × 932 at native browser screenshot resolution, not high-DPI device images.

## Approaches considered

1. **Shared foundation first (selected):** restore phone visibility and
   accessibility, refine shared tokens and primitives, then apply the same
   language to a small representative set of existing surfaces. Follow with
   independently planned work-item/session and settings/navigation journeys.
2. **Journey first:** redesign the work-item and agent flow before agreeing on
   shared patterns, extracting common pieces along the way. It can improve one
   journey quickly but risks inconsistent choices across the rest of Studio.

The selected approach does not require a new frontend framework, replacement
component library, parallel Studio, or broad backend rewrite.

## Foundation direction

- **Legibility and contrast:** retain warm dark and light themes while
  adjusting semantic foreground/background pairs so normal text, links,
  statuses and controls are readable. Aim for WCAG AA contrast: 4.5:1 for
  normal text and 3:1 for large text and necessary non-text control/state
  indicators. Do not rely on color alone to convey meaning. Replace the
  existing pinned contrast exceptions with passing checks rather than moving
  or widening the exception list.
- **Typography and hierarchy:** retain Geist and Geist Mono, but establish a
  small consistent scale for page titles, section headings, body text, labels,
  metadata and code. Give primary information enough weight to scan; avoid
  treating explanatory copy as tiny decorative text. Use monospace for code,
  identifiers and values where it helps, not as a default for ordinary prose.
- **Surfaces and spacing:** use a restrained set of background elevations,
  borders, corner radii and spacing rules. Cards should group related content,
  with fewer nested boxes and less repetitive chrome where that aids scanning;
  avoid oversized empty space or decorative effects that compete with work.
  Preserve enough separation for dense operational content and both themes.
- **Actions and statuses:** define clear primary, secondary, quiet and
  destructive action treatments using shared primitives. Use concise,
  consistent status labels and supporting iconography where useful; make
  focus, hover, pressed, disabled and pending states distinguishable. Do not
  hide an available action solely for visual neatness, or represent a request
  as completed before the backend confirms it.
- **Responsive shell:** fix the mobile navigation overlay so content is
  visible, scrollable and interactive beneath its top and bottom bars. Respect
  safe areas and onscreen keyboards; keep the current destinations, route and
  return behavior. Use touch-friendly primary controls and keyboard-visible
  focus. Avoid fixed desktop-sized conversation content on phone where it
  obscures the composer or creates unnecessary nested scrolling.
- **Shared vocabulary:** reuse the same visual language for Fleet, Factory,
  project settings and agent sessions. Preserve recognizable concepts and
  interaction conventions from the TUI where they describe the same session
  or agent state, without forcing the web interface to imitate a terminal.

## Scope and delivery

The first implementation slice fixes the opaque phone overlay and protects it
with a rendered regression that checks visibility, not merely DOM presence or
horizontal overflow. The shared foundation follows: semantic theme tokens,
type and spacing guidance, common button/input/card/status/focus patterns,
and the known dark/light contrast fixes. Apply and validate those patterns on
representative Fleet, work-item/session and project/settings surfaces so the
foundation is proven in real layouts, not just in a component gallery.

Keep existing routes, capabilities, factory authority, drafts, agent state,
and data contracts intact. Existing light/dark selection, reduced-motion
support, keyboard navigation and scroll/focus restoration remain functional.
Do not use fixture data as evidence of live agents or backend behavior. Show
loading, empty, stale, failed and unavailable states accurately.

The later work-item/session plan can address worker naming, conversation
header crowding, plan-revision prominence, activity history and the approved
subagent UX. The later settings/navigation plan can address discovered CI
checks, simplifying configuration and cross-surface orientation. These are
related follow-ups, not required wholesale redesigns in the foundation slice.
Preserve the user's ability to override settings and take actions throughout.

## Verification

Use the real current frontend with isolated deterministic fixtures, plus the
existing isolated-bridge checks where applicable. Compare screenshots at
desktop, tablet and phone widths in dark and light themes; include empty,
busy, error, decision, review and long-content states. Verify content is
visibly painted and interactable under phone chrome, and that a real browser
can navigate, scroll and use the composer with a simulated constrained
viewport. Test safe areas, focus and return, keyboard-only navigation,
reduced motion, 200% zoom and no unintended horizontal overflow. Test
contrast against actual rendered colors for enabled and interactive states;
delete fixed known-exception assertions as violations are resolved. Record
uncovered states and any actual-device/virtual-keyboard testing not performed.
