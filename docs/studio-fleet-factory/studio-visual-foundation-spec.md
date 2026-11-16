# Studio visual and interaction foundation: specification

Date: 2026-11-17

Status: specification for the [approved design](studio-visual-foundation-design.md). Execute the [implementation plan](plans/2026-11-17-studio-visual-foundation-plan.md) only after plan approval.

## Scope

Restore the mobile shell, fix existing theme contrast defects, and establish a restrained, coherent visual foundation across existing Studio screens. Preserve Warm Sunset, Geist, the current Fleet-first navigation, supported features, light/dark choice and user-controlled workflows. Demonstrate the foundation on Fleet, a factory work item and project settings; deeper changes to worker/subagent experience, CI configuration, plan revision, and information architecture belong to subsequent plans. Do not add an alternate shell, invented data, a new component library or backend contracts.

## Immediate mobile repair

At widths below 768 CSS pixels the fixed `.studio-phone-chrome` occupies the viewport with pointer events disabled outside the bars. Its `MobileNav` child currently paints `bg-bg` across the whole viewport and visually covers `<main>`, despite the page appearing in the accessibility tree. Remove that full-screen paint while retaining the top bar, bottom navigation, drawer overlay when open, safe-area insets, focus return and z-order. The unmodified baseline and separately labeled diagnostic captures are linked in the design.

Add a rendered phone regression that verifies a known contrasting piece of actual page content is visible between the bars in both themes, the chrome wrapper itself has no opaque background, and an action beneath it is clickable. `elementFromPoint` alone is insufficient because the overlay already has `pointer-events: none`. Exercise Fleet and a factory item at 390 × 844 and a shorter 390 × 600 viewport. The drawer should still open, obscure content intentionally and close with focus restored. Verify phone navigation and content after rotation/resize; do not change project or item data in the test.

## Shared visual language

Keep the existing semantic color token names and warm dark neutrals in `app.css`; introduce explicit on-accent/on-danger tokens for filled controls. Dark uses the existing dark foreground `#121113` on the warm filled accent/danger surfaces; the light theme uses an accessible light foreground on appropriately darkened filled colors. Override light-theme semantic signal colors rather than reusing dark-theme signal colors on light surfaces. Preserve color meanings: accent for primary action/navigation, teal for running/success, warm amber for attention, rose for danger, blue for information; colors supplement text and icons. Choose the concrete candidate values in the plan, verify their rendered combinations and adjust if measurements require it.

In both themes, normal text must meet 4.5:1, large text 3:1 and meaningful control boundaries and state indicators 3:1 against adjacent colors. Evaluate actual backgrounds including tinted badges, hover/focus and text opacity; do not infer success from tokens alone. Disabled controls must remain identifiable and legible but are not claimed to meet active-control thresholds. Fix the unlayered `button { color: inherit }` cascade that overrides the shared button's intended text color, and the session empty-state muted text reduced by opacity. Keep form elements inheriting usable text color without defeating explicit component styles. Fix all known exception pairs in the Fleet, handoff and work-item browser suites and remove their allowed/required violation lists. New accessibility scans on the covered active views must report no axe violations; do not add new blanket exemptions.

Retain Geist/Geist Mono. Use a small shared hierarchy: page title 20 px/28 px semibold; section title 16 px/24 px semibold; body 14 px/20 px; label/metadata 12 px/16 px, with 12 px as the minimum for user-facing explanatory text. Code/identifiers may use monospace at 12 px with sufficient line height. Shared panels use a 12 px radius, controls 8 px; a four-pixel spacing scale provides 4, 8, 12, 16, 24 and 32 px steps. Use background, surface and raised layers and subtle borders rather than heavy shadows or nested boxes. Preserve content density for operations instead of imposing a marketing-page layout.

Shared buttons provide primary, secondary/quiet and destructive variants with consistent height, labels and hover, focus-visible, pressed, pending and disabled behavior. Primary actions should be visually distinct but not every action should be filled. Inputs, selects and text areas share readable labels, sufficient contrast, clear focus and error descriptions. Status badges carry words and remain understandable without color. Apply touch targets of at least 44 × 44 CSS px to primary actions and mobile navigation; do not turn every dense read-only row into a large touch target. Preserve semantics and accessible names of existing controls.

## Representative rollout

Use shared tokens and components on Fleet, work-item activity/review and project settings: align headings, panels, action hierarchy, status and field treatments without reordering decisions, modifying API state or redesigning the workflows. Keep decision and warning visibility, scroll/focus restoration, selected view, route/deep links, factory approval, and independent review behavior. Changes must not make the approved subagent UI or other in-flight feature plans incompatible. Shared primitives may improve other screens incidentally, so test affected global UI and both themes.

The session header must not clip its controls at a narrow width. On phone, wrap or collapse secondary header controls into an accessible labeled menu while keeping back, identity, connection, composer and send reachable; do not remove actions. Let the embedded conversation shrink within available viewport, reserve space for phone chrome, and avoid an inflexible 32rem minimum. On desktop retain the existing split view. The full work-item/session redesign and changes in content ordering remain follow-up work.

## Verification and evidence

Use the existing browser fixture suite and an isolated bridge, with no developer configuration, service or credential access. Capture actual rendered desktop, tablet and phone views in dark and light for Fleet, active/decision/review work item and project settings, including long content and failed/empty states. Assert no unexpected axe violations, no horizontal page overflow, proper focus/keyboard return, drawer behavior, controls reachable at 200% zoom, and reduced-motion support. Simulate a shorter viewport for an onscreen keyboard without claiming it is a physical-device test. Save representative before/after screenshots with viewport size, fixture status and any temporary diagnostics labeled. The first task is independently shippable; remaining tasks must preserve its regression and current behavior.
